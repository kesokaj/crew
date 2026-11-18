package assistant

import (
	"context"
	"errors"
	"regexp"
	"strings"

	"github.com/jackc/pgx/v5"
)

// Close ends the conversation's sandbox: a turn that works is stopped, an idle one ended. Its
// conversation stays, and its next message opens it again (11.8, D25).
func (s *Service) Close(ctx context.Context, id string) error {
	run, state, err := s.latestRun(ctx, id)
	if err != nil {
		return err
	}
	if err := s.reachesRuns(state); err != nil {
		return err
	}
	if _, err := s.Pool.Exec(ctx, `UPDATE conversations SET closed_at = now() WHERE id = $1`, id); err != nil {
		return err
	}
	switch state {
	case "starting", "working":
		return s.Runs.Stop(ctx, run)
	case "idle":
		return s.Runs.End(ctx, run, "ok", "closed by the operator")
	}
	return nil
}

// Save marks or unmarks a conversation of the operator's as saved (11.8). Saving a conversation
// whose sandbox is idle ends the sandbox to free RAM while keeping all turns, CLI files, and the
// work folder in the database.
func (s *Service) Save(ctx context.Context, id string, saved bool) (View, error) {
	if !isUUID(id) {
		return View{}, ErrNotFound
	}
	var purpose string
	err := s.Pool.QueryRow(ctx, `SELECT purpose FROM conversations WHERE id = $1 AND purpose IN ('operator', 'chat')`, id).Scan(&purpose)
	if errors.Is(err, pgx.ErrNoRows) {
		return View{}, ErrNotFound
	}
	if err != nil {
		return View{}, err
	}
	if purpose != "operator" {
		return View{}, refused("only a conversation of the operator's can be saved")
	}
	run, state, err := s.latestRun(ctx, id)
	if err != nil {
		return View{}, err
	}
	if saved {
		if state == "idle" {
			if err := s.reachesRuns(state); err != nil {
				return View{}, err
			}
		}
		if _, err := s.Pool.Exec(ctx, `UPDATE conversations SET saved_at = coalesce(saved_at, now()) WHERE id = $1`, id); err != nil {
			return View{}, err
		}
		if state == "idle" && run != "" {
			if err := s.Runs.End(ctx, run, "ok", "saved by the operator"); err != nil {
				return View{}, err
			}
		}
	} else {
		if _, err := s.Pool.Exec(ctx, `UPDATE conversations SET saved_at = NULL WHERE id = $1`, id); err != nil {
			return View{}, err
		}
	}
	return s.Get(ctx, id)
}

// Delete removes (discards) a conversation of the operator's with everything of it in one step
// (11.8, D22): a turn that works or an idle sandbox is ended at once, and a ticket's chat is refused.
func (s *Service) Delete(ctx context.Context, id string) error {
	if !isUUID(id) {
		return ErrNotFound
	}
	var purpose string
	err := s.Pool.QueryRow(ctx, `SELECT purpose FROM conversations WHERE id = $1 AND purpose IN ('operator', 'chat')`, id).Scan(&purpose)
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrNotFound
	}
	if err != nil {
		return err
	}
	if purpose != "operator" {
		return refused("a ticket's chat is deleted with its project")
	}
	run, state, err := s.latestRun(ctx, id)
	if err != nil {
		return err
	}
	if err := s.reachesRuns(state); err != nil {
		return err
	}
	switch state {
	case "starting", "working":
		if err := s.Runs.EndNow(ctx, run, "operator", "discarded by the operator"); err != nil {
			return err
		}
	case "idle":
		if err := s.Runs.End(ctx, run, "ok", "closed by the operator"); err != nil {
			return err
		}
	}
	_, err = s.Pool.Exec(ctx, `SELECT crew_delete_conversation($1)`, id)
	return err
}

// reachesRuns refuses to end a run that is not ended when the service has no runs: a server without
// Docker reads the conversations and keeps the operator's messages, and starts and ends no turn.
func (s *Service) reachesRuns(state string) error {
	if s.Runs == nil && state != "" && state != "ended" {
		return refused("the runs cannot be reached: this server starts and ends no turn")
	}
	return nil
}

// latestRun is the newest run of a conversation, and its state; "" for one with none.
func (s *Service) latestRun(ctx context.Context, id string) (string, string, error) {
	if !isUUID(id) {
		return "", "", ErrNotFound
	}
	var run, state string
	var known bool
	err := s.Pool.QueryRow(ctx, `SELECT true, coalesce((SELECT r.id::text FROM runs r WHERE r.subtask = c.subtask ORDER BY r.number DESC LIMIT 1), ''),
			coalesce((SELECT r.state FROM runs r WHERE r.subtask = c.subtask ORDER BY r.number DESC LIMIT 1), '')
		FROM conversations c WHERE c.id = $1 AND c.purpose IN ('operator', 'chat')`, id).Scan(&known, &run, &state)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", "", ErrNotFound
	}
	return run, state, err
}

// Pick sets the model and the effort the operator picked for the conversation's next turns; empty
// is the assistant's own list (12.3).
func (s *Service) Pick(ctx context.Context, id, model, effort string) error {
	if !isUUID(id) {
		return ErrNotFound
	}
	return pgx.BeginFunc(ctx, s.Pool, func(tx pgx.Tx) error {
		var n int
		if err := tx.QueryRow(ctx, `SELECT count(*) FROM conversations WHERE id = $1 AND purpose IN ('operator', 'chat')`, id).Scan(&n); err != nil {
			return err
		}
		if n == 0 {
			return ErrNotFound
		}
		return s.pick(ctx, tx, id, model, effort)
	})
}

func (s *Service) pick(ctx context.Context, tx pgx.Tx, id, model, effort string) error {
	model, effort = strings.TrimSpace(model), strings.TrimSpace(effort)
	if (model != "" && !entry.MatchString(model)) || (effort != "" && !word.MatchString(effort)) {
		return invalid("model: a CLI's model as cli/model, and an effort")
	}
	_, err := tx.Exec(ctx, `UPDATE conversations SET pick_model = nullif($2, ''), pick_effort = nullif($3, '') WHERE id = $1`, id, model, effort)
	return err
}

// entry is a model as cli/model, and word an effort.
var (
	entry = regexp.MustCompile(`^[a-z][a-z0-9-]{0,19}/[A-Za-z0-9][A-Za-z0-9._-]{0,99}$`)
	word  = regexp.MustCompile(`^[a-z]{1,20}$`)
)
