package runs

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5"

	"github.com/kesokaj/crew/internal/cli"
	"github.com/kesokaj/crew/internal/harness"
	"github.com/kesokaj/crew/internal/settings"
)

// cliTotals is what the CLI last reported for the conversation, under its id of it (12.10).
type cliTotals struct {
	CLIID     string `json:"cliId"`
	TokensIn  int64  `json:"tokensIn"`
	TokensOut int64  `json:"tokensOut"`
	// TokensCached is nil in totals written before Crew kept it (0.44): the first reading after
	// that is the conversation's baseline, not one turn's use.
	TokensCached *int64 `json:"tokensCached,omitempty"`
}

// fromHome are the reasons a turn's exit.json may give: crew-run's own and the CLI's (12.4). exit.json
// is in a home the sandbox writes as root, so a reason only the backend sets, such as restart or
// operator, is not taken from it: it counts as an error.
// A setup that a run says failed is checked: Crew runs the setup on main, and where it passes there the
// failures count as the subtask's stops (13.5), so saying so gains a run nothing.
var fromHome = map[string]bool{
	cli.ReasonOK: true, cli.ReasonTimeout: true, cli.ReasonError: true, cli.ReasonRefused: true,
	cli.ReasonLogin: true, cli.ReasonQuota: true, cli.ReasonModel: true, cli.ReasonSetup: true,
}

// keepCLIFilesDays is how long a run's CLI files are kept after its turn (12.10): the machine's
// keep_cli_files_days, or the listing's 7 when it cannot be read, since a turn's end is not lost for
// a setting; for a conversation of the operator's or a ticket's chat, conversations.keep_cli_files_days,
// or its 30 (D13).
func (d *Driver) keepCLIFilesDays(ctx context.Context, conv string) int {
	key, def := "keep_cli_files_days", 7
	var purpose string
	if d.Pool.QueryRow(ctx, `SELECT purpose FROM conversations WHERE id = $1`, conv).Scan(&purpose) == nil && (purpose == "operator" || purpose == "chat") {
		key, def = "conversations.keep_cli_files_days", 30
	}
	v, err := settings.MachineValue(ctx, d.Pool, key)
	if n, ok := v.(int); err == nil && ok && n >= 1 && n <= 365 {
		return n
	}
	d.Log.Warn("reading "+key+": the CLI's files are kept the listing's days", "value", v, "days", def, "error", err)
	return def
}

// keptFile is one of the CLI's files of a turn as it is stored: sealed, with the sum of what is stored.
type keptFile struct {
	gz  []byte
	sum string
}

// sealCLIFiles is a turn's CLI files as they are stored: redacted, compressed, and sealed with the key
// file for their conversation and path (12.10, rule 10). When one cannot be sealed, none is, and drop
// says the conversation's stored files go too: the next sandbox is then rebuilt from the turns, never
// given the CLI's state of an older turn (plan of 2026-10-26, D5).
func (d *Driver) sealCLIFiles(lr *liveRun, in map[string][]byte) (out map[string]keptFile, drop bool) {
	if len(in) == 0 {
		return nil, false
	}
	if d.Seal == nil {
		d.Log.Warn("the CLI's files of a turn are not kept: Crew has no key file to seal them with", "run", lr.id)
		return nil, true
	}
	out = map[string]keptFile{}
	for p, data := range in {
		gz, _ := gzipBytes(lr.redact.Bytes(data))
		sealed, err := d.Seal.Seal(cliFilePurpose(lr.conversation, p), gz)
		if err != nil {
			d.Log.Warn("the CLI's files of a turn are not kept", "run", lr.id, "path", p, "error", err)
			return nil, true
		}
		sum := sha256.Sum256(sealed)
		out[p] = keptFile{sealed, hex.EncodeToString(sum[:])}
	}
	return out, false
}

// finishTurn ends a turn: its end event, its use, the agent's turn of the conversation, and the
// CLI's own files of it, in one transaction (12.10). A turn that ended well leaves the run idle in
// its microVM; any other end ends the run. It says whether the run ended.
func (d *Driver) finishTurn(ctx context.Context, lr *liveRun, ev harness.RunEvent) (bool, error) {
	end := ev.End
	a, _ := cli.Get(lr.cli)
	reason, detail := turnEnd(end, lr.setup)
	lr.mu.Lock()
	if lr.stopping != "" {
		reason, detail = lr.stopping, lr.stopDetail+"; "+homeDetail(end)
	}
	stopping := lr.stopping != ""
	said := lr.speaks.String()
	calls := append([]string(nil), lr.calls...)
	lr.mu.Unlock()
	if reason == "" {
		reason = cli.ReasonError
	}
	detail = trim(lr.redact.Text(detail), 2000)
	for i, c := range calls {
		calls[i] = trim(lr.redact.Text(c), 120)
	}
	idle := reason == cli.ReasonOK && !stopping
	var u cli.Usage
	response := ""
	if end.Result != nil {
		u = a.Usage(end.Result)
		response = a.Outcome(cli.TurnOutput{Result: end.Result, Exit: end.Exit.Exit}).Response
	}
	if strings.TrimSpace(response) == "" {
		response = said
	}
	response = lr.redact.Text(response)
	ev.Payload = map[string]any{
		"status":          "ended",
		"reason":          reason,
		"detail":          detail,
		"exit":            end.Exit.Exit,
		"tokens_reported": u.HasTokens,
	}
	// The CLI's files are redacted, compressed and sealed before the transaction, which holds the
	// conversation's row: a large file takes seconds. A turn that did not end well or for the operator
	// may have written an error step into its conversation file, which agy replays on --conversation
	// (BUG-0006): its files are not kept, and any kept from an earlier turn of the conversation go too,
	// so the next sandbox rebuilds from the turns.
	var files map[string]keptFile
	dropFiles := reason != cli.ReasonOK && reason != ReasonOperator
	if !dropFiles {
		files, dropFiles = d.sealCLIFiles(lr, end.Files)
	}
	keepDays := 0
	if len(files) > 0 {
		keepDays = d.keepCLIFilesDays(ctx, lr.conversation)
	}
	// The work folder too, before the transaction: it may be 200 MB (D17).
	// It is read once: an end tried again finds its file removed and takes what was read.
	lr.mu.Lock()
	if lr.folderTurn != ev.Turn {
		lr.mu.Unlock()
		f, note := d.keepFolder(lr, end)
		lr.mu.Lock()
		lr.folder, lr.folderNote, lr.folderTurn = f, note, ev.Turn
	}
	folder, folderNote := lr.folder, lr.folderNote
	lr.mu.Unlock()
	err := pgx.BeginFunc(ctx, d.Pool, func(tx pgx.Tx) error {
		if err := d.insertEvent(ctx, tx, lr, ev); err != nil {
			return err
		}
		var conv string
		var totalsRaw []byte
		var cliID *string
		if err := tx.QueryRow(ctx, `SELECT c.id, c.cli_totals, c.cli_id FROM conversations c JOIN runs r ON r.conversation = c.id
			WHERE r.id = $1 FOR UPDATE OF c`, lr.id).Scan(&conv, &totalsRaw, &cliID); err != nil {
			return err
		}
		id := ""
		if cli.SafeID(end.Conversation) {
			id = end.Conversation
		}
		if cliID != nil && *cliID != "" {
			id = *cliID
		}
		var prev cliTotals
		json.Unmarshal(totalsRaw, &prev)
		if prev.CLIID != id {
			prev = cliTotals{CLIID: id}
		}
		in, out, cached := u.TokensIn, u.TokensOut, u.TokensCached
		next := prev
		prevCached := int64(0)
		if prev.TokensCached != nil {
			prevCached = *prev.TokensCached
		}
		if !u.HasTokens {
			in, out, cached = 0, 0, 0
		} else if u.TokensTotal {
			in, out = max(0, u.TokensIn-prev.TokensIn), max(0, u.TokensOut-prev.TokensOut)
			cached = max(0, u.TokensCached-prevCached)
			// Totals from before Crew kept the cache's tokens have none: this reading is the
			// baseline, not one turn's use (0.44).
			if prev.TokensCached == nil && (prev.TokensIn > 0 || prev.TokensOut > 0) {
				cached = 0
			}
			next.TokensIn, next.TokensOut = u.TokensIn, u.TokensOut
			next.TokensCached = &u.TokensCached
		} else {
			next.TokensIn, next.TokensOut = prev.TokensIn+u.TokensIn, prev.TokensOut+u.TokensOut
			sum := prevCached + u.TokensCached
			next.TokensCached = &sum
		}
		nt, _ := json.Marshal(next)
		wip := ""
		if end.WIP != "" {
			wip = "refs/crew/wip/" + lr.id
		}
		// A run records its tokens, and no cost: Crew estimates none (12.7, since 0.53). Its last turn's
		// step of crew-run's own is kept for an eval, which reads it in place of the detail (since 1.01).
		if _, err := tx.Exec(ctx, `UPDATE runs SET tokens_in = tokens_in + $2, tokens_out = tokens_out + $3,
				tokens_cached = tokens_cached + $6, exit_code = $4, wip_ref = coalesce(nullif($5, ''), wip_ref), end_step = nullif($7, '')
			WHERE id = $1`, lr.id, in, out, end.Exit.Exit, wip, cached, endStep(end)); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `UPDATE conversations SET cli_totals = $2, cli_id = coalesce(cli_id, nullif($3, '')) WHERE id = $1`,
			conv, nt, id); err != nil {
			return err
		}
		// A turn whose CLI said and did nothing, such as one that stopped for its login before it
		// began, is no turn of the conversation.
		if end.Result != nil || strings.TrimSpace(response) != "" || len(calls) > 0 {
			if err := d.insertTurn(ctx, tx, conv, lr.id, "agent", response, calls, in+out, false); err != nil {
				return err
			}
		}
		if dropFiles {
			if _, err := tx.Exec(ctx, `DELETE FROM cli_files WHERE conversation = $1`, conv); err != nil {
				return err
			}
		}
		for p, f := range files {
			if _, err := tx.Exec(ctx, `INSERT INTO cli_files (conversation, path, gzip, sha256, keep_until)
				VALUES ($1, $2, $3, $4, now() + $5::interval)
				ON CONFLICT (conversation, path) DO UPDATE SET gzip = $3, sha256 = $4, saved_at = now(), keep_until = now() + $5::interval`,
				conv, p, f.gz, f.sum, fmt.Sprintf("%d days", keepDays)); err != nil {
				return err
			}
		}
		// A turn's files are the CLI's whole state: one of another path, as the file of a CLI
		// conversation a rebuilt turn replaced, or one that does not open with this key file, would put
		// back none of them at every continue until it expired (review of 6751b80, finding 1). A turn
		// that ended well or for the operator and kept none changes nothing: its file could not be read.
		if len(files) > 0 {
			paths := make([]string, 0, len(files))
			for p := range files {
				paths = append(paths, p)
			}
			if _, err := tx.Exec(ctx, `DELETE FROM cli_files WHERE conversation = $1 AND NOT (path = ANY($2))`, conv, paths); err != nil {
				return err
			}
		}
		if _, err := tx.Exec(ctx, `UPDATE run_spools SET done = true WHERE run = $1 AND turn = $2`, lr.id, ev.Turn); err != nil {
			return err
		}
		if err := storeFolder(ctx, tx, conv, folder, folderNote); err != nil {
			return err
		}
		if !idle {
			return nil
		}
		// In the same transaction, so a turn's end is written once even when this fails and the
		// whole end is tried again.
		if _, err := tx.Exec(ctx, `UPDATE runs SET state = 'idle' WHERE id = $1 AND state <> 'ended'`, lr.id); err != nil {
			return err
		}
		// A turn that ended well counts the automatic continues from nothing again.
		_, err := tx.Exec(ctx, `UPDATE subtasks SET state = 'idle', wait_for = NULL, auto_continues = 0, updated_at = now() WHERE id = $1`, lr.subtask)
		return err
	})
	if err != nil {
		return false, err
	}
	d.logOwnError(lr, ev.Turn, end)
	lr.mu.Lock()
	lr.speaks.Reset()
	lr.calls = nil
	lr.mu.Unlock()
	if idle {
		// The next turn is held to the project's numbers as they are then (8.2).
		d.arm(ctx, lr)
		d.Log.Info("turn ended", "run", lr.id, "turn", ev.Turn, "reason", reason)
		return false, nil
	}
	d.endRun(ctx, lr, reason, detail, &end.Exit.Exit, true)
	return true, nil
}
