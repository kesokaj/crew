package pipeline

import (
	"context"
	"fmt"
	"maps"
	"slices"
	"strings"

	"github.com/kesokaj/crew/internal/board"
)

// review starts the reviewer for the branch as it is, on another family than the builder's (12.3),
// and a fix round for a review that asks for changes. In Review since is empty; before a merge in a
// later stage it is the commit Review approved, when there is one (7.4, since 0.90).
func (p *Pipeline) review(ctx context.Context, j *job, t *board.Ticket, subs []*sub, head, since string) error {
	if head == "" {
		return nil
	}
	done := on(subs, kindReview, head)
	if len(done) == 0 {
		recs, err := p.records(ctx, t)
		if err != nil {
			return err
		}
		plan, err := p.planOf(ctx, j, t)
		if err != nil {
			return err
		}
		unresolved, err := p.Board.Unresolved(ctx, t)
		if err != nil {
			return err
		}
		// Every test file the change touches, with its diff: a builder can weaken its own test (7.1, 21).
		tests, err := p.Board.TestFiles(ctx, j.proj, t, head)
		if err != nil {
			return err
		}
		var found []extra
		ctxText := "Your workspace is a clone of the ticket's branch at " + head[:12] + ", which you read and do not push." + skillReview(t)
		if len(tests) > 0 {
			found = append(found, extra{name: "The test files this change adds, changes, or deletes", who: "the builder's branch, as Crew listed it against main",
				text: testFilesText(tests)})
			ctxText += fmt.Sprintf(" The change touches %d test files, listed below with their diffs: with approve, give write_review a reason in tests for each of them.", len(tests))
		}
		// Every package file the change touches: a package an agent adds runs in the sandbox and later
		// in the hosted project (13.8).
		pkgs, err := p.Board.PackageFiles(ctx, j.proj, head)
		if err != nil {
			return err
		}
		if len(pkgs) > 0 {
			found = append(found, extra{name: "The package files this change adds, changes, or deletes", who: "the builder's branch, as Crew listed it against main",
				text: packageFilesText(pkgs)})
			ctxText += packageFilesContext(pkgs)
		}
		if since != "" {
			s := since[:min(12, len(since))]
			ctxText += " Review approved " + s + ". Since then the ticket's branch changed in " + board.StageName(t.Stage) +
				": a fix, a new approach, or a merge of main. Read what changed since with `git log " + since + "..HEAD` and `git diff " + since +
				" HEAD`. A merge of main brings files main changed, and the list of files changed since holds them too; a merge commit can also carry " +
				"changes of its own, so read each merge commit since then with `git show --remerge-diff <commit>`. Then judge the whole change as it is now, as in any review; " +
				"if your clone does not hold " + s + ", read the whole change against main."
			if changed, err := p.Board.Git.Touched(ctx, j.proj.Name, since, head); err == nil && len(changed) > 0 {
				found = append(found, extra{name: "The files changed since Review approved " + s, who: "the ticket's branch, as Crew listed it",
					text: touchedText(changed)})
			}
		}
		parts := p.ticketPrompt(ctx, promptInput{stage: "review", role: "reviewer", project: j.proj, ticket: t, plan: plan, records: recs,
			comments: unresolved, extra: found, context: ctxText})
		_, err = p.start(ctx, j, t, startSpec{kind: kindReview, role: "reviewer", title: "Review the change", parts: parts,
			branch: *t.Branch, readOnly: true, commit: head, comments: unresolved})
		return err
	}
	last := done[len(done)-1]
	rec, err := p.newest(ctx, t, board.RecordReview)
	if err != nil || rec == nil || rec.Commit == nil || *rec.Commit != head || *rec.Verdict == board.VerdictApprove || fixedAfter(t, subs, last) {
		return err
	}
	_, err = p.fix(ctx, j, t, subs, head, "review", extra{name: "The review that asks for changes", who: "the reviewer", text: rec.Body})
	return err
}

// What the reviewer is shown beside the change: the test files and the package files it touches, and
// the files changed since Review approved (7.1, 7.4, 13.8).

// packageFilesContext is what the reviewer's context says of the package files listed to it: a word on
// each, how to read a lock file when one is listed, and that a binary one cannot be read by its diff.
func packageFilesContext(pkgs []board.PackageFile) string {
	s := fmt.Sprintf(" The change touches %d package files, listed below: say in your review, for each, whether the change is needed.", len(pkgs))
	if slices.ContainsFunc(pkgs, func(f board.PackageFile) bool { return f.Lock }) {
		s += " A lock file is listed without its diff: read it in your workspace with `git diff origin/main...HEAD -- <path>`, and check that " +
			"each package it adds or moves, and each source and checksum it names, follows from a manifest's change."
	}
	if slices.ContainsFunc(pkgs, func(f board.PackageFile) bool { return f.Lock && f.Binary }) {
		s += " A file listed as binary cannot be read by its diff: unless the change also holds its text form (such as bun.lock beside " +
			"bun.lockb), or the manifests' change explains it, ask for changes."
	}
	return s
}

// packageFilesText lists package files, a manifest with its diff and a lock file by name, for the
// reviewer's prompt.
func packageFilesText(files []board.PackageFile) string {
	var b strings.Builder
	for _, f := range files {
		kind, what := "manifest", "changed"
		if f.Lock {
			kind = "lock file"
		}
		if f.Deleted {
			what = "deleted"
		}
		counts := fmt.Sprintf("+%d -%d", f.Added, f.Removed)
		switch {
		case f.Binary:
			counts = "binary"
		case f.Cut && f.Added == 0 && f.Removed == 0:
			counts = "counts not known"
		}
		fmt.Fprintf(&b, "%s (%s, %s, %s)\n", f.Path, kind, what, counts)
		if f.Cut {
			b.WriteString("(Its diff is too long to show here, or cut: read it in your workspace.)\n")
		}
		if f.Patch != "" {
			b.WriteString(f.Patch)
			if !strings.HasSuffix(f.Patch, "\n") {
				b.WriteByte('\n')
			}
		}
		b.WriteByte('\n')
	}
	return b.String()
}

// touchedText lists changed paths, one a line, a deleted one said so.
func touchedText(changed map[string]bool) string {
	var b strings.Builder
	for _, path := range slices.Sorted(maps.Keys(changed)) {
		b.WriteString(path)
		if changed[path] {
			b.WriteString(" (deleted)")
		}
		b.WriteByte('\n')
	}
	return b.String()
}

// testFilesText lists test files with their diffs, for the reviewer's prompt.
func testFilesText(tests []board.TestFile) string {
	var b strings.Builder
	for _, f := range tests {
		what := "changed"
		if f.Deleted {
			what = "deleted"
		}
		fmt.Fprintf(&b, "%s (%s, +%d -%d)\n", f.Path, what, f.Added, f.Removed)
		if f.Cut {
			b.WriteString("(Its diff is too long to show here, or cut: read it in your workspace.)\n")
		}
		if f.Patch != "" {
			b.WriteString(f.Patch)
			if !strings.HasSuffix(f.Patch, "\n") {
				b.WriteByte('\n')
			}
		}
		b.WriteByte('\n')
	}
	return b.String()
}
