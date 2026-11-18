package board

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/kesokaj/crew/internal/db"
	"github.com/kesokaj/crew/internal/github"
)

// FetchMain fetches the project's remote main into its canonical repository (6.6), with the key of
// Setup for a remote on GitHub, and returns its commit: "" when the remote has none yet.
func (b *Board) FetchMain(ctx context.Context, p *Project) (string, error) {
	if p.Remote == nil {
		return "", nil
	}
	commit, err := b.fetchMain(ctx, p)
	// What it found is written on every outcome, so a remote that fails at once is tried again after
	// git.fetch_minutes, not on every pass (round 2 of M10).
	detail := ""
	if err != nil {
		detail = pushFailure(err)
		var ge *github.Error
		if errors.As(err, &ge) {
			detail = ge.Msg
		}
	}
	if _, uerr := b.Pool.Exec(ctx, `UPDATE projects SET fetched_at = now(), fetch_detail = nullif($2, '') WHERE id = $1`, p.ID, detail); uerr != nil {
		return "", uerr
	}
	return commit, err
}

func (b *Board) fetchMain(ctx context.Context, p *Project) (string, error) {
	// The fetch has its time; what it found is written on the caller's context.
	fctx, cancel := context.WithTimeout(ctx, b.pushTimeout())
	defer cancel()
	var env []string
	if github.SSHURL.MatchString(*p.Remote) {
		if b.GitHub == nil {
			return "", &github.Error{Msg: "GitHub is not set up on this server"}
		}
		e, done, err := b.GitHub.SSHEnv(fctx)
		if err != nil {
			return "", err
		}
		defer done()
		env = e
	}
	return b.Git.FetchMain(fctx, p.Name, *p.Remote, env)
}

// FetchDue says the project's remote is due to be fetched: every git.fetch_minutes, and at most once a
// minute when a merge asks for it.
func (b *Board) FetchDue(ctx context.Context, p *Project, minutes float64, forMerge bool) bool {
	var at *time.Time
	if err := b.Pool.QueryRow(ctx, `SELECT fetched_at FROM projects WHERE id = $1`, p.ID).Scan(&at); err != nil || at == nil {
		return err == nil
	}
	if forMerge {
		return b.now().Sub(*at) >= time.Minute
	}
	return b.now().Sub(*at) >= time.Duration(minutes*float64(time.Minute))
}

// RemoteHold is the open ticket that joins Crew's main with a remote main that went another way, or
// fixes a taken remote main whose tests failed (7.12): Crew's merges wait for it, all but its own.
func (b *Board) RemoteHold(ctx context.Context, p *Project) (int64, string, string, error) {
	return remoteHold(ctx, b.Pool, p)
}

func remoteHold(ctx context.Context, q db.Querier, p *Project) (int64, string, string, error) {
	var id int64
	var key, kind string
	err := q.QueryRow(ctx, `SELECT t.id, $2 || '-' || t.number, m.kind FROM remote_mains m JOIN tickets t ON t.id = m.ticket
		WHERE m.project = $1 AND t.stage NOT IN ('done', 'closed')
		  AND ((m.kind = 'diverged' AND m.state IN ('conflict', 'failed'))
		    OR (m.kind = 'ahead' AND m.state = 'failed'
		        AND NOT EXISTS (SELECT 1 FROM remote_mains n WHERE n.project = m.project AND n.id > m.id AND n.state = 'passed')))
		ORDER BY m.id DESC LIMIT 1`, p.ID, p.Key).Scan(&id, &key, &kind)
	if errors.Is(err, pgx.ErrNoRows) {
		return 0, "", "", nil
	}
	if err != nil {
		return 0, "", "", err
	}
	detail := fmt.Sprintf("Crew's merges wait for %s, which joins main with the remote's main that went another way", key)
	if kind == "ahead" {
		detail = fmt.Sprintf("Crew's merges wait for %s, which fixes main from the remote, whose tests fail", key)
	}
	return id, key, detail, nil
}

// remoteHoldResult is what the merge check says while Crew's merges wait for the remote's main.
func remoteHoldResult(ctx context.Context, c *CheckContext) (*Result, error) {
	id, key, detail, err := remoteHold(ctx, c.Tx, c.Project)
	if err != nil || key == "" || id == c.Ticket.ID {
		return nil, err
	}
	return &Result{Detail: detail}, nil
}

// DecideRemoteMain records the operator's answer about a remote main that changes files that are
// code, with its row in changes: take it, which the pipeline then does and checks, or leave it.
func (b *Board) DecideRemoteMain(ctx context.Context, p *Project, id int64, take bool, who string) error {
	state := "refused"
	if take {
		state = "taken"
	}
	return b.inTx(ctx, func(tx pgx.Tx) error {
		tag, err := tx.Exec(ctx, `UPDATE remote_mains SET state = $3, decided_at = now() WHERE id = $1 AND project = $2 AND state = 'held'`, id, p.ID, state)
		if err != nil {
			return err
		}
		if tag.RowsAffected() == 0 {
			return errf(NotFound, "no main from the remote waits for you as %d", id)
		}
		return db.RecordProjectChange(ctx, tx, p.ID, who, "remote_mains", fmt.Sprint(id), map[string]any{"state": "held"}, map[string]any{"state": state})
	})
}
