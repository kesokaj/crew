package pins

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/kesokaj/crew/internal/board"
	"github.com/kesokaj/crew/internal/release"
)

// Author is who the look's tickets are by: the backend's, which gives no agent's role feedback when
// Analysis discards one (9.5).
const Author = board.Backend + " · daily look"

// keepLooks is how many looks are kept.
const keepLooks = 30

// lookFor bounds a look's reading of the publishers: each request has its 30 s, and a look makes
// about twenty.
const lookFor = 5 * time.Minute

// Look is the daily look (12.8, since 0.51; 0.56), in the running server only.
type Look struct {
	Pool    *pgxpool.Pool
	Board   *board.Board
	Sources *Sources
	Log     *slog.Logger
	// Now is the clock; time.Now when nil.
	Now func() time.Time
	// For bounds one look; lookFor when 0.
	For time.Duration
	mu  sync.Mutex
}

// Result is one look: when, what it read of each source, and the keys of the tickets it made.
type Result struct {
	At    time.Time `json:"at"`
	Lines []Line    `json:"lines"`
	Made  []string  `json:"made"`
}

func (l *Look) now() time.Time {
	if l.Now != nil {
		return l.Now()
	}
	return time.Now()
}

// Once looks now: the publishers against versions.lock of main of Crew's project, a ticket for each
// finding whose keys the project does not have, and the look stored. One look runs at a time.
func (l *Look) Once(ctx context.Context) (*Result, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	r := &Result{At: l.now(), Made: []string{}}
	name, err := board.CrewProject(ctx, l.Pool)
	if err != nil {
		return nil, err
	}
	var lock []byte
	var lockErr error
	var remote map[string]string
	var from *Line
	if name != "" {
		// A lock git cannot read, or one past the bound of a read, is no lock to look by, never a
		// main with none (since 1.04).
		if lock, lockErr = l.Board.Git.File(ctx, name, "versions.lock"); lockErr == nil && lock != nil {
			remote, from = l.remoteLock(ctx, name)
		}
	}
	switch {
	case name == "":
		r.Lines = []Line{{"Crew's project", "there is none: the look has no versions.lock to read, and makes no ticket", false}}
	case lockErr != nil:
		l.Log.Warn("reading versions.lock of Crew's project", "project", name, "error", lockErr)
		r.Lines = []Line{{"Crew's project", "main of " + name + "'s versions.lock could not be read: the look makes no ticket", false}}
	case lock == nil:
		r.Lines = []Line{{"Crew's project", "main of " + name + " has no versions.lock: the look makes no ticket", false}}
	default:
		// The publishers are read within the look's bound; the tickets and the record are not, so a
		// look whose sources ran out of time still keeps what it read.
		limit := l.For
		if limit == 0 {
			limit = lookFor
		}
		read, cancel := context.WithTimeout(ctx, limit)
		found, lines := l.Sources.Compare(read, release.ParseLock(lock))
		cancel()
		r.Lines = lines
		if from != nil {
			r.Lines = append([]Line{*from}, r.Lines...)
		}
		if remote != nil {
			kept := found[:0]
			for _, f := range found {
				if pinned(remote, f) {
					r.Lines = append(r.Lines, Line{f.Source, "the remote's main pins it already: no ticket", true})
					continue
				}
				kept = append(kept, f)
			}
			found = kept
		}
		made, more, err := l.tickets(ctx, name, found)
		if err != nil {
			return nil, err
		}
		r.Made, r.Lines = made, append(r.Lines, more...)
	}
	if err := l.store(ctx, r); err != nil {
		return nil, err
	}
	return r, nil
}

// remoteLock is versions.lock of Crew's project's remote main while that follows main, and a line
// that says what the look read (12.8, since 0.58). A paused project's main is not fetched by the
// pipeline and lags what the operator pushed (on dave, behind the agy pin), so the look fetches the
// remote's main itself while the project does not run, into the project's remote ref and merged into
// nothing; a running project's is the pipeline's to fetch, and the look reads the ref as it stands.
// The remote's file only holds back a finding it pins already: every value a ticket names comes from
// main and the publishers, never from a commit Crew has not taken (security review of 002e3a3..ef7a82a).
// The line names no address of the remote.
func (l *Look) remoteLock(ctx context.Context, name string) (map[string]string, *Line) {
	p, err := l.Board.Project(ctx, name)
	if err != nil {
		return nil, &Line{"Crew's project", "could not be read; the look compares main's versions.lock alone", false}
	}
	if p.Remote == nil {
		return nil, nil
	}
	none := func(why string) (map[string]string, *Line) {
		return nil, &Line{"Crew's project", why + "; the look compares main's versions.lock alone", false}
	}
	var commit string
	if p.State == "running" {
		commit = l.Board.Git.RemoteMainAt(ctx, name)
	} else if commit, err = l.Board.FetchMain(ctx, p); err != nil {
		return none("the remote's main could not be fetched")
	}
	if commit == "" {
		return none("the remote has no main")
	}
	if !l.Board.Git.Follows(ctx, name, l.Board.Git.Main(ctx, name), commit) {
		return none("the remote's main does not follow main")
	}
	// A remote main the operator refused never reaches main (6.6): what it pins holds back nothing.
	var refused bool
	if err := l.Board.Pool.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM remote_mains WHERE project = $1 AND remote = $2 AND state = 'refused')`,
		p.ID, commit).Scan(&refused); err != nil {
		return none("the remote's main could not be read")
	} else if refused {
		return none("the operator refused the remote's main")
	}
	at, err := l.Board.Git.FileAt(ctx, name, commit, "versions.lock")
	if err != nil {
		return none("the remote's main's versions.lock could not be read")
	}
	if at == nil {
		return none("the remote's main has no versions.lock")
	}
	return release.ParseLock(at), &Line{"Crew's project", "main's versions.lock, and the remote's main, " + commit[:12] + ", for what it pins already", true}
}

// pinned says the remote pins every value a finding would set.
func pinned(remote map[string]string, f Finding) bool {
	for _, kv := range f.Set {
		if remote[kv.Key] != kv.Value {
			return false
		}
	}
	return len(f.Set) > 0
}

// tickets makes the ticket of each finding whose keys the project does not have, and none while an
// open ticket of the look changes one of its lines of versions.lock: it waits for a later look.
func (l *Look) tickets(ctx context.Context, project string, found []Finding) ([]string, []Line, error) {
	taken, open, err := l.Board.PinKeys(ctx, project)
	if err != nil {
		return nil, nil, err
	}
	made := []string{}
	var lines []Line
	for _, f := range found {
		f = untaken(f, taken)
		if len(f.Keys) == 0 {
			continue
		}
		if k := slices.IndexFunc(f.LockKeys(), func(k string) bool { return open[k] }); k >= 0 {
			lines = append(lines, Line{"tickets", fmt.Sprintf("%s waits: an open ticket of the look changes %s", title(f), f.LockKeys()[k]), true})
			continue
		}
		n, ok := ticket(f)
		if !ok {
			lines = append(lines, Line{"tickets", title(f) + ": a value is not fit for a check command, and no ticket is made", false})
			continue
		}
		t, ok, err := l.Board.AddPinTicket(ctx, project, f.Keys, f.LockKeys(), n, Author)
		if err != nil {
			// A ticket that cannot be made costs its line, not the look: the tickets made before it
			// stay, and the look is kept with what it did (review of c5eff65..5a72946). The board's
			// reason, which may be a database's, goes to the log only.
			l.Log.Error("a ticket of the daily look", "title", n.Title, "error", err)
			lines = append(lines, Line{"tickets", title(f) + ": not made: the board did not take it", false})
			continue
		}
		if !ok {
			continue // another look made it meanwhile
		}
		made = append(made, f.Keys...)
		for _, k := range f.LockKeys() {
			open[k] = true
		}
		lines = append(lines, Line{"tickets", t.Key + ": " + n.Title, true})
	}
	return made, lines, nil
}

// untaken is the finding without what has a key already: a version has one key for all its lines,
// and the images one key each.
func untaken(f Finding, taken map[string]bool) Finding {
	if f.To != "" {
		if taken[f.Keys[0]] {
			f.Keys = nil
		}
		return f
	}
	out := Finding{Tool: f.Tool, Places: f.Places}
	for i, k := range f.Keys {
		if !taken[k] {
			out.Keys, out.Set = append(out.Keys, k), append(out.Set, f.Set[i])
		}
	}
	return out
}

// title names what the ticket moves and where it comes from, which the release's notes show, so the
// operator's Install sees each pin a publisher moved (security review of c5eff65..5a72946).
func title(f Finding) string {
	if f.To != "" {
		return fmt.Sprintf("Update %s to %s, from %s", f.Tool, f.To, f.Source)
	}
	if len(f.Set) == 1 {
		return "Move the pinned image " + f.Set[0].Key + " to its tag's new digest"
	}
	t := "Move the pinned images " + strings.Join(f.LockKeys(), ", ") + " to their tags' new digests"
	if len(t) > 200 {
		t = fmt.Sprintf("Move %d pinned images to their tags' new digests", len(f.Set))
	}
	return t
}

// ticket is the finding's ticket: Crew's own sentences with the values filled in, and a check for
// each line of versions.lock. ok is false when a value could break out of the check's quotes, which
// its shape already refuses.
func ticket(f Finding) (board.NewTicket, bool) {
	var lines, checks []string
	for _, kv := range f.Set {
		if strings.ContainsAny(kv.Key+kv.Value, "'\"\\$` \t\r\n") {
			return board.NewTicket{}, false
		}
		lines = append(lines, "- "+kv.Key+"="+kv.Value)
		checks = append(checks, fmt.Sprintf("grep -qxF '%s=%s' versions.lock", kv.Key, kv.Value))
	}
	why := fmt.Sprintf("The daily look found %s %s at its publisher; versions.lock on main pins %s (design 12.8).", f.Tool, f.To, f.From)
	if f.To == "" {
		why = "The daily look found that the tags of these pinned images point at new digests: their publishers rebuilt them (design 12.8)."
	}
	what := "Set these lines of versions.lock, each exactly as it is here:\n" + strings.Join(lines, "\n") +
		"\nSet the same values where tools/check-versions.py's list names these keys: " + strings.Join(f.Places, ", ") +
		". Change nothing else: make check holds versions.lock and those files in step."
	reason := "A pin of versions.lock: the file is its own record (16.12)."
	n := board.NewTicket{
		Title: title(f), Kind: "chore", Why: why, What: what, DocsReason: &reason,
		Goal: "versions.lock and the files on check-versions.py's list name the new values, and make check passes.",
	}
	for _, c := range checks {
		n.Checks = append(n.Checks, board.CheckSpec{Kind: "cmd", Spec: c})
	}
	// It passes before the work too, so it must keep passing (7.6).
	n.Checks = append(n.Checks, board.CheckSpec{Kind: "cmd", Spec: "python3 tools/check-versions.py", Keeps: true})
	return n, true
}

// store keeps the look, and the newest keepLooks of them.
func (l *Look) store(ctx context.Context, r *Result) error {
	lines, err := json.Marshal(r.Lines)
	if err != nil {
		return err
	}
	return pgx.BeginFunc(ctx, l.Pool, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `INSERT INTO pin_looks (at, lines, made) VALUES ($1, $2, $3)`, r.At, lines, r.Made); err != nil {
			return err
		}
		_, err := tx.Exec(ctx, `DELETE FROM pin_looks WHERE id NOT IN (SELECT id FROM pin_looks ORDER BY at DESC, id DESC LIMIT $1)`, keepLooks)
		return err
	})
}

// Last is the newest look, nil before the first.
func Last(ctx context.Context, q interface {
	QueryRow(context.Context, string, ...any) pgx.Row
}) (*Result, error) {
	r := &Result{}
	var lines []byte
	err := q.QueryRow(ctx, `SELECT at, lines, made FROM pin_looks ORDER BY at DESC, id DESC LIMIT 1`).Scan(&r.At, &lines, &r.Made)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if err := json.Unmarshal(lines, &r.Lines); err != nil {
		return nil, err
	}
	return r, nil
}

// Run looks once a day, at the time next gives after now, until ctx ends. It makes no look when it
// starts: an install would otherwise make tickets that day, which the operator did not ask for. A look
// that fails is logged, and the next day's looks again.
func (l *Look) Run(ctx context.Context, next func(time.Time) time.Time) {
	for {
		select {
		case <-ctx.Done():
			return
		case <-time.After(time.Until(next(l.now()))):
		}
		if r, err := l.Once(ctx); err != nil {
			l.Log.Error("the daily look", "error", err)
		} else {
			l.Log.Info("the daily look", "tickets", r.Made)
		}
	}
}
