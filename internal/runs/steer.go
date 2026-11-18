package runs

import (
	"context"
	"errors"
	"strings"

	"github.com/jackc/pgx/v5"

	"github.com/kesokaj/crew/internal/harness"
)

// Steer sends a live mid-turn steering prompt (`session/prompt` via `crew-run acp-steer`) to a run
// whose turn is currently working in a project's ACP microVM (design 1.37, 11.6).
func (d *Driver) Steer(ctx context.Context, runID, text string, record func(ctx context.Context, tx pgx.Tx, run string) error) error {
	st, ok := d.Harness.(harness.Steerer)
	if !ok {
		return errors.New("harness does not support live steering")
	}
	text = strings.TrimSpace(text)
	if text == "" {
		return errors.New("steering text is empty")
	}
	var state string
	if err := d.Pool.QueryRow(ctx, `SELECT state FROM runs WHERE id = $1`, runID).Scan(&state); err != nil {
		return err
	}
	if state != "working" {
		return ErrBusy
	}
	if _, ok := d.getLive(runID); !ok {
		return ErrEnded
	}
	if err := st.Steer(ctx, runID, text); err != nil {
		return err
	}
	if record != nil {
		return pgx.BeginFunc(ctx, d.Pool, func(tx pgx.Tx) error {
			return record(ctx, tx, runID)
		})
	}
	return nil
}
