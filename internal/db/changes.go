package db

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
)

// RecordChange writes the row in changes that goes with a change, in the same transaction (6.4).
// before and after are the values as JSON, nil for a row that is new or removed.
func RecordChange(ctx context.Context, tx pgx.Tx, who, table, rowKey string, before, after any) error {
	return RecordProjectChange(ctx, tx, "", who, table, rowKey, before, after)
}

// RecordProjectChange is RecordChange for a row that belongs to a project, which its history names.
func RecordProjectChange(ctx context.Context, tx pgx.Tx, project, who, table, rowKey string, before, after any) error {
	b, err := jsonOrNil(before)
	if err != nil {
		return err
	}
	a, err := jsonOrNil(after)
	if err != nil {
		return err
	}
	var p any
	if project != "" {
		p = project
	}
	_, err = tx.Exec(ctx, `INSERT INTO changes (project, who, tbl, row_key, before, after) VALUES ($1, $2, $3, $4, $5, $6)`,
		p, who, table, rowKey, b, a)
	if err != nil {
		return fmt.Errorf("recording the change: %w", err)
	}
	return nil
}

func jsonOrNil(v any) ([]byte, error) {
	if v == nil {
		return nil, nil
	}
	if raw, ok := v.(json.RawMessage); ok {
		return raw, nil
	}
	return json.Marshal(v)
}

// Change is one row of the history.
type Change struct {
	Seq    int64
	At     time.Time
	Who    string
	Table  string
	RowKey string
	Before json.RawMessage
	After  json.RawMessage
}

// History returns the changes of one row, oldest first.
func History(ctx context.Context, q Querier, table, rowKey string) ([]Change, error) {
	rows, err := q.Query(ctx, `SELECT seq, at, who, tbl, row_key, before, after FROM changes
		WHERE tbl = $1 AND row_key = $2 ORDER BY seq`, table, rowKey)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(r pgx.CollectableRow) (Change, error) {
		var c Change
		err := r.Scan(&c.Seq, &c.At, &c.Who, &c.Table, &c.RowKey, &c.Before, &c.After)
		return c, err
	})
}
