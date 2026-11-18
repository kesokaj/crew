package models

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/kesokaj/crew/internal/db"
)

// Comparison is one comparison the operator started.
type Comparison struct {
	ID      int64      `json:"id"`
	Role    string     `json:"role"`
	Own     Pick       `json:"own"`
	Other   Pick       `json:"other"`
	State   string     `json:"state"`
	Numbers Card       `json:"numbers"`
	Started time.Time  `json:"startedAt"`
	Ended   *time.Time `json:"endedAt,omitempty"`
}

// Comparisons are every comparison, the running ones first, then the newest.
func Comparisons(ctx context.Context, q db.Querier) ([]Comparison, error) {
	rows, err := q.Query(ctx, `SELECT id, role, own_entry, own_effort, other_entry, other_effort, state, numbers, started_at, ended_at
		FROM comparisons ORDER BY state = 'running' DESC, started_at DESC`)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(r pgx.CollectableRow) (Comparison, error) {
		var c Comparison
		var nums []byte
		err := r.Scan(&c.ID, &c.Role, &c.Own.Entry, &c.Own.Effort, &c.Other.Entry, &c.Other.Effort, &c.State, &nums, &c.Started, &c.Ended)
		if err == nil {
			json.Unmarshal(nums, &c.Numbers)
		}
		return c, err
	})
}

func running(ctx context.Context, q db.Querier, role string) (*Comparison, error) {
	all, err := Comparisons(ctx, q)
	if err != nil {
		return nil, err
	}
	for _, c := range all {
		if c.Role == role && c.State == "running" {
			return &c, nil
		}
	}
	return nil, nil
}

// endComparison ends a running comparison once each pick has compare_runs units, keeps its numbers,
// and judges them by the same rules: Crew changes for an Automatic agent if they say so and the days
// between changes allow it, and recommends for an agent the operator set.
func (c *Chooser) endComparison(ctx context.Context, v *view, role string) error {
	cmp, err := running(ctx, c.Pool, role)
	if err != nil || cmp == nil {
		return err
	}
	own, other := c.comparisonStats(v, cmp)
	if own.Units < v.rules.CompareRuns || other.Units < v.rules.CompareRuns {
		return nil
	}
	card := Card{From: cmp.Own, To: cmp.Other, FromStats: own, ToStats: other}
	b, _ := json.Marshal(card)
	if _, err := c.Pool.Exec(ctx, `UPDATE comparisons SET state = 'ended', ended_at = now(), numbers = $2 WHERE id = $1`, cmp.ID, b); err != nil {
		return err
	}
	reason := v.rules.verdictFor(role, own, other)
	if reason == "" || v.watching != nil || !v.canRun(cmp.Other) || !v.checksCanRun(cmp.Other) {
		return nil
	}
	if !v.rules.Automatic(role) || !v.st.allowedForAsk(Ask{Role: role}, slices.Contains(CheckRoles, role), false, "", cmp.Other.Entry) {
		return c.recommend(ctx, v, role, cmp.Other, reason, card)
	}
	if !v.lastAt.IsZero() && c.now().Sub(v.lastAt) < time.Duration(v.rules.MinDaysBetween)*24*time.Hour {
		return nil
	}
	return c.change(ctx, v, role, cmp.Other, reason, card, "crew")
}

// comparisonStats are the units of a comparison: the other pick's are the runs it sent there, the own
// pick's every unit on it since the comparison started. A unit that moved at a limit counts for
// neither.
func (c *Chooser) comparisonStats(v *view, cmp *Comparison) (own, other stat) {
	for _, u := range v.units {
		if u.Started.Before(cmp.Started) || u.From == FromLimit {
			continue
		}
		p := Pick{u.CLI + "/" + u.Alias, u.Effort}
		var s *stat
		switch {
		case p == cmp.Other && u.From == FromComparison:
			s = &other
		case p == cmp.Own && u.From != FromComparison && u.From != FromTry:
			s = &own
		default:
			continue
		}
		n := float64(s.Units)
		s.Units++
		if u.Passed {
			s.Passed++
		}
		s.FixRounds = (s.FixRounds*n + float64(u.FixRounds)) / (n + 1)
	}
	for _, u := range v.checks {
		if u.Started.Before(cmp.Started) || u.From == FromLimit {
			continue
		}
		p := Pick{u.CLI + "/" + u.Alias, u.Effort}
		var s *stat
		switch {
		case p == cmp.Other && u.From == FromComparison:
			s = &other
		case p == cmp.Own && u.From != FromComparison && u.From != FromTry:
			s = &own
		default:
			continue
		}
		s.Units++
		if u.AskedFix {
			s.AskedFix++
		}
		s.Escaped += u.Escaped
	}
	return own, other
}

// StartComparison starts a comparison of an agent's own pick with another, for the agents Crew
// chooses for (12.3).
func (c *Chooser) StartComparison(ctx context.Context, role string, other Pick) (int64, error) {
	if !slices.Contains(AutoRoles, role) {
		return 0, fmt.Errorf("a comparison is for the agents Crew chooses for: %s", strings.Join(AutoRoles, ", "))
	}
	v, err := c.read(ctx, role)
	if err != nil {
		return 0, err
	}
	if v.cur.Entry == "" {
		return 0, fmt.Errorf("the %s has no entry that is offered", role)
	}
	if other == v.cur {
		return 0, errors.New("a comparison needs another entry or effort than the one the agent runs on")
	}
	if !v.st.candidate(other.Entry).listed {
		return 0, fmt.Errorf("no CLI in use offers %s", other.Entry)
	}
	var id int64
	err = c.Pool.QueryRow(ctx, `INSERT INTO comparisons (role, own_entry, own_effort, other_entry, other_effort) VALUES ($1, $2, $3, $4, $5)
		RETURNING id`, role, v.cur.Entry, v.cur.Effort, other.Entry, other.Effort).Scan(&id)
	return id, err
}

// StopComparison stops a running comparison.
func (c *Chooser) StopComparison(ctx context.Context, id int64) error {
	tag, err := c.Pool.Exec(ctx, `UPDATE comparisons SET state = 'stopped', ended_at = now() WHERE id = $1 AND state = 'running'`, id)
	if err == nil && tag.RowsAffected() == 0 {
		err = pgx.ErrNoRows
	}
	return err
}
