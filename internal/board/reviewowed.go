package board

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"

	"github.com/jackc/pgx/v5"

	"github.com/kesokaj/crew/internal/db"
	"github.com/kesokaj/crew/internal/gitstore"
	"github.com/kesokaj/crew/internal/settings"
)

// A ticket's head is reviewed before it merges (7.4, 7.10, since 0.90): the stages a ticket passed are
// not repeated, but code pushed after Review approved an earlier commit (a fix in a later stage, the
// lead's new approach, a merge of main) is read by a reviewer again, in place, and so is a branch that
// changes a package file (13.8) also when Analysis skipped Review. The check review_covers_head holds
// the move to Done; the merge queue starts the reviewer when it fails.

// Owed says whether a ticket's head needs a review before it merges, and why: the commit Review
// approved last, and the package files its branch changes.
type Owed struct {
	Need     bool
	Approved string
	Packages []PackageFile
}

// ReviewOwed is the rule. With the project's Review off, or the ticket in Review, nothing is owed and
// nothing is read. Otherwise a review is owed when Review approved an earlier commit, or when the
// branch changes a package file.
func ReviewOwed(ctx context.Context, q db.Querier, g *gitstore.Store, pipe Pipeline, p *Project, t *Ticket, head string) (Owed, error) {
	if rv, ok := pipe.Stage(settings.StageReview); !ok || !rv.On || t.Stage == settings.StageReview {
		return Owed{}, nil
	}
	var o Owed
	var commit *string
	err := q.QueryRow(ctx, `SELECT commit FROM records WHERE ticket = $1 AND kind = $2 AND verdict = $3 ORDER BY id DESC LIMIT 1`,
		t.ID, RecordReview, VerdictApprove).Scan(&commit)
	switch {
	case errors.Is(err, pgx.ErrNoRows):
	case err != nil:
		return Owed{}, err
	case commit != nil:
		o.Approved = *commit
	}
	if head != "" {
		if g == nil {
			return Owed{}, errors.New("board: no git store to compare the branch with")
		}
		if o.Packages, err = packageFiles(ctx, g, p, head); err != nil {
			return Owed{}, err
		}
	}
	o.Need = o.Approved != "" || len(o.Packages) > 0
	return o, nil
}

// ReviewOwed reads the project's stages and applies the rule to the ticket's head.
func (b *Board) ReviewOwed(ctx context.Context, p *Project, t *Ticket, head string) (Owed, error) {
	doc, err := b.Settings(ctx, b.Pool, p)
	if err != nil {
		return Owed{}, err
	}
	return ReviewOwed(ctx, b.Pool, b.Git, PipelineOf(doc), p, t, head)
}

// reviewCoversHead holds the move to Done while a review is owed and the newest review is not an
// approve of the head. A check that cannot tell refuses (BUG-012).
func reviewCoversHead(ctx context.Context, c *CheckContext) (Result, error) {
	h, err := head(ctx, c)
	if err != nil {
		return Result{Detail: "its branch cannot be compared with main"}, nil
	}
	o, err := ReviewOwed(ctx, c.Tx, c.Git, c.Pipeline, c.Project, c.Ticket, h)
	if err != nil {
		// No error's text: an agent reads the reason through get_ticket. The merge queue's own call of
		// ReviewOwed returns the error to the pass, which logs it.
		return Result{Detail: "Crew could not tell whether a review of its head is owed"}, nil
	}
	if !o.Need {
		return Result{Passed: true, Detail: "no review is owed since Review"}, nil
	}
	r, err := newestRecord(ctx, c.Tx, c.Ticket.ID, RecordReview)
	if err != nil {
		return Result{}, err
	}
	if r != nil && h != "" && r.Commit != nil && *r.Commit == h && r.Verdict != nil && *r.Verdict == VerdictApprove {
		return Result{Passed: true, Detail: "the reviewer read its head"}, nil
	}
	if o.Approved != "" {
		return Result{Detail: "Review approved " + short(o.Approved) + "; what changed since waits for the reviewer"}, nil
	}
	return Result{Detail: "its branch changes " + packagePaths(o.Packages) + ", which a reviewer reads before it merges"}, nil
}

// maxReasonPaths is how many package files a waiting reason names.
const maxReasonPaths = 5

// packagePaths names package files for a waiting reason, which people and agents read: each quoted,
// since a path is the builder's text and may hold a comma or a newline, and at most maxReasonPaths.
func packagePaths(files []PackageFile) string {
	var paths []string
	for _, f := range files[:min(len(files), maxReasonPaths)] {
		paths = append(paths, strconv.Quote(f.Path))
	}
	s := strings.Join(paths, ", ")
	if n := len(files) - maxReasonPaths; n > 0 {
		s += fmt.Sprintf(" and %d more", n)
	}
	return s
}

// short is a commit's first 12 characters, as Crew names commits to people.
func short(commit string) string {
	if len(commit) > 12 {
		return commit[:12]
	}
	return commit
}
