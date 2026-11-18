package board

import (
	"context"
	"errors"
	"fmt"
	"path"
	"slices"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/kesokaj/crew/internal/cli"
	"github.com/kesokaj/crew/internal/gitstore"
	"github.com/kesokaj/crew/internal/settings"
)

// CheckContext is what a check reads: the database in the move's transaction, the project, the
// ticket, and the project's code.
type CheckContext struct {
	Tx       pgx.Tx
	Project  *Project
	Ticket   *Ticket
	Pipeline Pipeline
	Git      *gitstore.Store
	Now      time.Time
}

// Result is the result of one check. NotYet is a check that cannot be evaluated by this version of
// Crew: it does not pass, and it is not a failure of the ticket.
type Result struct {
	Name   string `json:"name" yaml:"name"`
	Passed bool   `json:"passed" yaml:"passed"`
	Detail string `json:"detail,omitempty" yaml:"detail,omitempty"`
	NotYet bool   `json:"notYet,omitempty" yaml:"not_yet,omitempty"`
}

// Check evaluates one check of a stage. Only the backend evaluates checks: what an agent says about
// its own work moves nothing (7.1).
type Check func(ctx context.Context, c *CheckContext) (Result, error)

// builtin are the checks this version evaluates: from the ticket's rows, and from its branch in the
// quarantine repository.
var builtin = map[string]Check{
	"has_why_what_goal": func(_ context.Context, c *CheckContext) (Result, error) {
		t := c.Ticket
		for _, f := range [][2]string{{"why", t.Why}, {"what", t.What}, {"goal", t.Goal}} {
			if strings.TrimSpace(f[1]) == "" {
				return Result{Detail: "it has no " + f[0]}, nil
			}
		}
		return Result{Passed: true}, nil
	},
	"has_check": func(_ context.Context, c *CheckContext) (Result, error) {
		if len(c.Ticket.Checks) == 0 {
			return Result{Detail: "it has no check"}, nil
		}
		return Result{Passed: true, Detail: fmt.Sprintf("%d checks", len(c.Ticket.Checks))}, nil
	},
	"analysis_approved": func(_ context.Context, c *CheckContext) (Result, error) {
		if c.Ticket.Analysis.Status != StatusReady {
			return Result{Detail: "analysis: " + statusText(c.Ticket)}, nil
		}
		return Result{Passed: true, Detail: "approved by " + c.Ticket.Analysis.DecidedBy}, nil
	},
	"dependencies_done": func(ctx context.Context, c *CheckContext) (Result, error) {
		d, err := dependencies(ctx, c.Tx, c.Ticket)
		if err != nil {
			return Result{}, err
		}
		if !d.met() {
			return Result{Detail: "waits for " + strings.Join(append(d.waiting, d.skipped...), ", ")}, nil
		}
		return Result{Passed: true}, nil
	},
	"branch_pushed": func(ctx context.Context, c *CheckContext) (Result, error) {
		br, err := readBranch(ctx, c)
		if err != nil || !br.Pushed {
			return Result{Detail: "its branch is not pushed"}, err
		}
		return Result{Passed: true, Detail: *c.Ticket.Branch}, nil
	},
	"has_commits": func(ctx context.Context, c *CheckContext) (Result, error) {
		br, err := readBranch(ctx, c)
		if err != nil || br.Ahead == 0 {
			return Result{Detail: "its branch has no commit that main does not have"}, err
		}
		return Result{Passed: true, Detail: fmt.Sprintf("%d commits", br.Ahead)}, nil
	},
	// A skill from outside is pinned, with its source and licence (12.9, 21): when the branch changes
	// the project's skills, every row of their VENDORED.md is complete. Whether a skill came from
	// outside is the reviewer's to read; a change of skills waits for the operator's Allow (13.8).
	"skills_vendored": skillsVendored,
	// A branch that goes on with no review deletes no file a command check names (7.1, since 0.47).
	"named_tests_kept": namedTestsKept,
	// Every page the ticket names was changed on its branch, or it gives the reason none is (7.1).
	"docs_done": func(ctx context.Context, c *CheckContext) (Result, error) {
		t := c.Ticket
		if len(t.Docs) == 0 {
			if t.DocsReason == nil || strings.TrimSpace(*t.DocsReason) == "" {
				return Result{Detail: "it names no page to update and gives no reason"}, nil
			}
			return Result{Passed: true, Detail: "no page: " + *t.DocsReason}, nil
		}
		br, err := readBranch(ctx, c)
		if err != nil {
			return Result{}, err
		}
		var missing []string
		for _, d := range t.Docs {
			if !slices.Contains(br.Changed, d) {
				missing = append(missing, d)
			}
		}
		if len(missing) > 0 {
			return Result{Detail: "not changed on its branch: " + strings.Join(missing, ", ")}, nil
		}
		return Result{Passed: true}, nil
	},
}

func (b *Board) evaluate(ctx context.Context, c *CheckContext, name string) (Result, error) {
	f := b.Checks[name]
	if f == nil {
		f = builtin[name]
	}
	if f == nil {
		f = m6Checks[name]
	}
	if f == nil {
		f = ownStageCheck(name)
	}
	if f == nil {
		// Validation refuses a name no check answers; one that reached here waits, and says so.
		return Result{Name: name, NotYet: true, Detail: name + " is not a check this version of Crew evaluates"}, nil
	}
	r, err := f(ctx, c)
	r.Name = name
	return r, err
}

func readBranch(ctx context.Context, c *CheckContext) (gitstore.Branch, error) {
	if c.Ticket.Branch == nil {
		return gitstore.Branch{}, nil
	}
	return c.Git.ReadBranch(ctx, c.Project.Name, *c.Ticket.Branch)
}

// deps is the state of what a ticket depends on (7.9).
type deps struct {
	waiting []string // not done yet
	skipped []string // skipped: the ticket's plan no longer holds, so it is analysed again
}

func (d deps) met() bool { return len(d.waiting) == 0 && len(d.skipped) == 0 }

// dependencies reads what t waits for. A dependency that was discarded counts as met; a cancelled one
// passes its place to the tickets that replace it; a skipped one is reported apart.
func dependencies(ctx context.Context, q pgx.Tx, t *Ticket) (deps, error) {
	var d deps
	rows, err := q.Query(ctx, `SELECT o.id, p.key || '-' || o.number, o.stage, coalesce(o.closed_reason, ''), o.analysis->>'status'
		FROM ticket_links l JOIN tickets o ON o.id = l.to_ticket JOIN projects p ON p.id = o.project
		WHERE l.from_ticket = $1 AND l.kind = 'depends_on' ORDER BY o.number`, t.ID)
	if err != nil {
		return d, err
	}
	type dep struct {
		id                         int64
		key, stage, closed, status string
	}
	var list []dep
	for rows.Next() {
		var x dep
		var status *string
		if err := rows.Scan(&x.id, &x.key, &x.stage, &x.closed, &status); err != nil {
			rows.Close()
			return d, err
		}
		if status != nil {
			x.status = *status
		}
		list = append(list, x)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return d, err
	}
	for _, x := range list {
		switch {
		case x.stage == settings.StageDone, x.closed == "discarded":
		case x.closed == "cancelled":
			var open []string
			r, err := q.Query(ctx, `SELECT p.key || '-' || n.number FROM ticket_links l JOIN tickets n ON n.id = l.from_ticket
				JOIN projects p ON p.id = n.project
				WHERE l.to_ticket = $1 AND l.kind = 'replaces' AND n.stage <> 'done' AND coalesce(n.closed_reason, '') <> 'discarded'`, x.id)
			if err != nil {
				return d, err
			}
			if open, err = pgx.CollectRows(r, pgx.RowTo[string]); err != nil {
				return d, err
			}
			d.waiting = append(d.waiting, open...)
		case x.stage == settings.StageAnalysis && x.status == StatusSkipped:
			d.skipped = append(d.skipped, x.key+" (skipped)")
		default:
			d.waiting = append(d.waiting, x.key)
		}
	}
	return d, nil
}

func statusText(t *Ticket) string {
	switch t.Analysis.Status {
	case StatusWaiting, StatusProposed:
		return "waiting for analysis"
	case StatusAnalysing:
		return "being analysed"
	case StatusSkipped:
		return "skipped: " + t.Analysis.Reason
	case StatusNotice:
		if t.Hold {
			return "a new feature, held"
		}
		if t.NoticeUntil == nil {
			return "a new feature, waiting for you"
		}
		return "a new feature, goes ahead at " + t.NoticeUntil.UTC().Format(time.RFC3339)
	case StatusReady:
		return "ready"
	case StatusOperator:
		if p := t.Analysis.Proposal; p != nil {
			return "waits for you: the analysis proposes to " + p.Choice + " it"
		}
		return "waits for you"
	}
	return t.Analysis.Status
}

// errVendoredTooLarge is main's VENDORED.md past the bound the store reads an index file within, which
// the check follows main's file through (since 1.07, after its security review).
var errVendoredTooLarge = fmt.Errorf("main's .agents/skills/VENDORED.md is longer than %d bytes, more than Crew reads", cli.MaxIndexBytes)

// skillsVendored is the check skills_vendored. The skills are also where a link of .agents/skills, or
// of a skill's folder, leads, the branch's and main's, which may have come since the branch left it,
// and a link to the root makes every file a skill; VENDORED.md is read through such links as the CLI
// opens it, where the branch leads to it and where main does (since 1.06, and after its review and
// security review, the operator's popup answer of session 73, "AllPaths and main's links").
func skillsVendored(ctx context.Context, c *CheckContext) (Result, error) {
	br, err := readBranch(ctx, c)
	if err != nil {
		return Result{}, err
	}
	if len(br.Changed) == 0 {
		return Result{Passed: true, Detail: "it changes no skill"}, nil
	}
	const vendored = ".agents/skills/VENDORED.md"
	skills, err := c.Git.BranchLinked(ctx, c.Project.Name, *c.Ticket.Branch, []string{".agents/skills/"})
	if err != nil {
		return Result{}, err
	}
	onMain, err := c.Git.Linked(ctx, c.Project.Name, []string{".agents/skills/"})
	if err != nil {
		return Result{}, err
	}
	skills = append(skills, onMain...)
	touches := slices.Contains(skills, cli.AllPaths)
	for _, f := range br.Changed {
		touches = touches || strings.HasPrefix(f, ".agents/skills/") || cli.Below(f, skills)
	}
	if !touches {
		return Result{Passed: true, Detail: "it changes no skill"}, nil
	}
	paths := []string{vendored}
	_, way, err := c.Git.IndexFile(ctx, c.Project.Name, vendored)
	if errors.Is(err, gitstore.ErrIndexFileTooLarge) {
		return Result{}, errVendoredTooLarge
	}
	if err != nil {
		return Result{}, err
	}
	// Where a link of main leads it: the file it reaches, whatever its name, or where it would be.
	switch {
	case len(way.Links) == 0:
	case way.File != "" && way.File != vendored:
		paths = append(paths, way.File)
	case way.File == "" && path.Base(way.End) == "VENDORED.md":
		paths = append(paths, way.End)
	}
	for _, p := range paths {
		r, err := vendoredRowsAt(ctx, c, p)
		if err != nil {
			return Result{}, err
		}
		if r != nil {
			return *r, nil
		}
	}
	return Result{Passed: true, Detail: "every row of .agents/skills/VENDORED.md has its source, pin, and licence"}, nil
}

// vendoredRowsAt is what the check says of the VENDORED.md at p, or nil when it holds.
func vendoredRowsAt(ctx context.Context, c *CheckContext, p string) (*Result, error) {
	md, err := c.Git.BranchFile(ctx, c.Project.Name, *c.Ticket.Branch, p)
	if err != nil {
		return nil, err
	}
	// The record of where skills came from is not taken away by a branch: no row its base has is gone
	// from it, the file deleted or emptied (security review of M12, review round 2).
	base, err := c.Git.BaseFile(ctx, c.Project.Name, *c.Ticket.Branch, p)
	if err != nil {
		return nil, err
	}
	if gone := RemovedRows(string(base), string(md)); len(gone) > 0 {
		return &Result{Detail: "the branch takes rows out of " + p + ": " + strings.Join(gone, ", ")}, nil
	}
	if problems := VendoredRows(string(md)); len(problems) > 0 {
		return &Result{Detail: p + ": " + strings.Join(problems, "; ")}, nil
	}
	return nil, nil
}
