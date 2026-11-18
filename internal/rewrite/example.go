package rewrite

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/kesokaj/crew/internal/models"
	"github.com/kesokaj/crew/internal/runs"
)

// Example rewrites one of the operator's earlier first messages again, with rules in place of the
// prompt writer's own, for the operator to compare a change of the rules with (7.2): the old rules when
// rules is nil. It is a call of its own on the message's draft, and keeps no rewrite of the operator's.
func (s *Service) Example(ctx context.Context, rewriteID int64, rules map[string]string) (string, error) {
	var original string
	var draft, project *string
	if err := s.Pool.QueryRow(ctx, `SELECT original, draft::text, project::text FROM prompt_rewrites WHERE id = $1`, rewriteID).Scan(&original, &draft, &project); err != nil {
		return "", err
	}
	req := Request{Kind: KindFirst, Text: original, Rules: rules}
	if draft != nil {
		req.Draft = *draft
	} else if project != nil {
		req.Project = *project
	}
	doc, err := s.Machine(ctx)
	if err != nil {
		return "", err
	}
	c, err := models.Choose(ctx, s.Pool, models.Ask{Role: "prompt", Machine: doc})
	if err != nil {
		return "", err
	}
	parts, err := s.parts(ctx, req)
	if err != nil {
		return "", err
	}
	limit := 3 * timeout(doc)
	run, _, err := s.Runs.StartRole(ctx, runs.RoleRun{Role: "prompt", Kind: "rewrite", Title: "An example of a change to the prompt writer's rules",
		Project: req.Project, Draft: req.Draft, Judge: true, CLI: c.CLI, Model: c.Model, Name: c.Name, Effort: c.Effort,
		From: c.ModelFrom, EffortFrom: c.EffortFrom, Parts: parts, Timeout: limit})
	if err != nil {
		return "", err
	}
	deadline := time.Now().Add(limit)
	poll := s.Poll
	if poll <= 0 {
		poll = 500 * time.Millisecond
	}
	for {
		var state string
		if err := s.Pool.QueryRow(ctx, `SELECT state FROM runs WHERE id = $1`, run).Scan(&state); err != nil {
			return "", err
		}
		if state == "idle" || state == "ended" {
			break
		}
		if time.Now().After(deadline) {
			s.Runs.Stop(ctx, run)
			return "", fmt.Errorf("the prompt writer took longer than %s", limit)
		}
		select {
		case <-ctx.Done():
			return "", ctx.Err()
		case <-time.After(poll):
		}
	}
	defer s.Runs.Finish(ctx, run)
	var answer string
	if err := s.Pool.QueryRow(ctx, `SELECT text FROM turns WHERE run = $1 AND speaker = 'agent' ORDER BY number DESC LIMIT 1`, run).Scan(&answer); err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return "", err
	}
	f, ok := readRewrite(answer)
	switch {
	case !ok:
		return "", errors.New("the answer was not in its form")
	case f.good:
		return original, nil
	}
	return f.rewrite, nil
}
