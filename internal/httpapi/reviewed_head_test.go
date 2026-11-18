package httpapi_test

import (
	"context"
	"strings"
	"sync"
	"testing"

	"github.com/kesokaj/crew/internal/cli"
	"github.com/kesokaj/crew/internal/harness"
)

// A head is reviewed before it merges (7.4, 7.10, 13.8, since 0.90): code pushed after Review approved
// an earlier commit is read by a reviewer again, in the ticket's stage, and so is a branch that changes
// a package file, also when Analysis skipped Review. A ticket only moves to the right.

// headKit plays the agents of one ticket, SF-1 ("One", which checks test -f 1.txt):
//   - the lead approves, skipping Review when skipReview;
//   - the builder pushes 1.txt and files;
//   - a fix takes broken.txt out when the branch has it, and otherwise pushes a file of its own;
//   - the n-th reviewer turn does what review(n) says: "approve", "changes", or "silent" (ends its
//     turn without write_review).
//
// With broken.txt on the branch, Crew's run of the project's tests ("make test") fails in Test.
type headKit struct {
	*ticketKit
	skipReview bool
	files      map[string]string
	review     func(n int) string

	mu       sync.Mutex
	reviews  []string         // each reviewer turn's prompt
	firsts   map[int64]string // each reviewer subtask's first prompt, which lists the test files
	models   []string         // each reviewer turn's model
	builders []string         // each builder's and fixer's model
	broken   bool
	fixes    int
}

func newHeadKit(t *testing.T, skipReview bool, files map[string]string, review func(n int) string) *headKit {
	h := &headKit{ticketKit: newTicketKit(t), skipReview: skipReview, files: files, review: review}
	_, h.broken = files["broken.txt"]
	h.custom = h.play
	return h
}

func (h *headKit) play(kind string, spec harness.RunSpec, p string) (harness.Script, bool) {
	switch kind {
	case "analysis":
		args := map[string]any{"choice": "approve", "docsReason": "none"}
		if h.skipReview {
			args["skips"], args["skipsReason"] = []string{"review"}, "a small change"
		}
		h.tool(spec.Run, "analyse_ticket", args)
	case "build":
		h.recordBuilder(spec)
		files := map[string]string{"1.txt": "one"}
		for f, b := range h.files {
			files[f] = b
		}
		h.push(spec.Run, spec.Branch, files)
		h.tool(spec.Run, "add_note", map[string]any{"text": "Built SF-1"})
		h.tool(spec.Run, "request_move", map[string]any{})
	case "fix":
		h.recordBuilder(spec)
		h.mu.Lock()
		broken := h.broken
		h.broken = false
		h.fixes++
		n := h.fixes
		h.mu.Unlock()
		if broken {
			h.pushRemoving(spec.Branch, "broken.txt")
		} else {
			h.push(spec.Run, spec.Branch, map[string]string{"fix.txt": strings.Repeat("x", n)})
		}
		h.tool(spec.Run, "add_note", map[string]any{"text": "Fixed SF-1"})
		h.tool(spec.Run, "fix_done", map[string]any{})
	case "review":
		h.mu.Lock()
		h.reviews = append(h.reviews, p)
		h.models = append(h.models, spec.Model)
		n := len(h.reviews)
		if h.firsts == nil {
			h.firsts = map[int64]string{}
		}
		if _, ok := h.firsts[spec.Subtask]; !ok {
			h.firsts[spec.Subtask] = p
		}
		first := h.firsts[spec.Subtask]
		h.mu.Unlock()
		switch h.review(n) {
		case "approve":
			// A reminder's turn holds only its new text: the reasons come from what the run was first shown.
			h.tool(spec.Run, "write_review", reviewed(first, map[string]any{"verdict": "approve", "text": "Fine."}))
		case "changes":
			h.tool(spec.Run, "write_review", map[string]any{"verdict": "changes_requested", "text": "Name it better."})
		}
	case "test":
		h.tool(spec.Run, "add_test_result", map[string]any{"result": "pass", "text": "Ran it."})
	case "summary":
		return harness.Script{Lines: summaryLines(spec)}, true
	}
	return recording(spec), true
}

func (h *headKit) recordBuilder(spec harness.RunSpec) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.builders = append(h.builders, spec.Model)
}

// done runs the board until SF-1 is Done and returns the reviewer turns' prompts.
func (h *headKit) done(stages []string) []string {
	h.shop(stages, nil, "One")
	defer h.run()()
	h.until("SF-1 is Done", func() bool { return h.stageOf("SF-1") == "done" })
	h.mu.Lock()
	defer h.mu.Unlock()
	return append([]string(nil), h.reviews...)
}

// reviewersOn is how many reviewer subtasks SF-1 had while it was in a stage.
func (h *headKit) reviewersOn(stage string) int {
	return h.count(`SELECT count(*) FROM subtasks s JOIN stage_moves m ON m.ticket = s.ticket AND m.to_stage = $1
		WHERE s.kind = 'review' AND s.created_at >= m.at
		AND NOT EXISTS (SELECT 1 FROM stage_moves o WHERE o.ticket = s.ticket AND o.from_stage = $1 AND o.at <= s.created_at)`, stage)
}

// approvedCommit is the commit of SF-1's first approving review.
func (h *headKit) approvedCommit() string {
	var c string
	if err := h.d.Server.QueryRow(context.Background(), `SELECT commit FROM records WHERE kind = 'review' AND verdict = 'approve' ORDER BY id LIMIT 1`).Scan(&c); err != nil {
		h.t.Fatal(err)
	}
	return c
}

// E2E 1: a fix in Test after Review approved is reviewed again, in Test, before the merge; the
// second reviewer's prompt names the commit Review approved, and runs on another family than the
// builder's.
func TestAFixAfterReviewIsReviewedAgainBeforeItMerges(t *testing.T) {
	t.Parallel()
	h := newHeadKit(t, false, map[string]string{"broken.txt": "x"}, func(int) string { return "approve" })
	prompts := h.done([]string{"review", "test"})
	if len(prompts) != 2 {
		t.Fatalf("%d reviewer turns, want 2: one in Review, one before the merge", len(prompts))
	}
	if n := h.reviewersOn("test"); n != 1 {
		t.Fatalf("%d reviewers ran in Test, want 1", n)
	}
	approved := h.approvedCommit()
	if !strings.Contains(prompts[1], "Review approved "+approved[:12]) || !strings.Contains(prompts[1], "git diff "+approved) {
		t.Errorf("the second reviewer is not told what Review approved:\n%s", prompts[1])
	}
	if !strings.Contains(prompts[1], "git show --remerge-diff") {
		t.Errorf("the second reviewer is not told to read merge commits:\n%s", prompts[1])
	}
	if !strings.Contains(prompts[1], "broken.txt") {
		t.Errorf("the second reviewer is not shown the files changed since:\n%s", prompts[1])
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	for _, m := range h.models {
		for _, b := range h.builders {
			if cli.Family(m) == cli.Family(b) {
				t.Errorf("a reviewer ran on the builder's family: %s and %s", m, b)
			}
		}
	}
}

// E2E 2: a re-review that asks for changes is a fix round in Test, and the ticket merges only after
// a review approves its final head.
func TestAReReviewThatAsksForChangesIsAFixRound(t *testing.T) {
	t.Parallel()
	h := newHeadKit(t, false, map[string]string{"broken.txt": "x"}, func(n int) string {
		if n == 2 {
			return "changes"
		}
		return "approve"
	})
	prompts := h.done([]string{"review", "test"})
	if len(prompts) != 3 {
		t.Fatalf("%d reviewer turns, want 3", len(prompts))
	}
	if n := h.count(`SELECT count(*) FROM signals WHERE kind = 'rework' AND detail->>'reason' = 'review' AND detail->>'stage' = 'test'`); n != 1 {
		t.Fatalf("%d fix rounds for the review in Test, want 1", n)
	}
	h.headApproved(t)
}

// E2E 3: a ticket that skipped Review and changes go.mod and go.sum is reviewed before it merges, in
// Test, and its reviewer is shown the manifest's diff and the lock file by name.
func TestAPackageFileIsReviewedAlsoWhenReviewWasSkipped(t *testing.T) {
	t.Parallel()
	h := newHeadKit(t, true, map[string]string{"go.mod": "module shop\n\nrequire example.com/left v1.0.0\n",
		"go.sum": "example.com/left v1.0.0 h1:x=\nexample.com/left v1.0.0/go.mod h1:y=\n"}, func(int) string { return "approve" })
	prompts := h.done([]string{"review", "test"})
	if n := h.reviewersOn("review"); n != 0 {
		t.Fatalf("%d reviewers in Review, which the ticket skipped", n)
	}
	if len(prompts) != 1 || h.reviewersOn("test") != 1 {
		t.Fatalf("%d reviewer turns, %d in Test, want 1 in Test", len(prompts), h.reviewersOn("test"))
	}
	p := prompts[0]
	for _, want := range []string{"go.mod (manifest, changed, +3 -0)", "+require example.com/left v1.0.0", "go.sum (lock file, changed, +2 -0)",
		"Crew lists the package files it changes", "A lock file is listed without its diff: read it in your workspace"} {
		if !strings.Contains(p, want) {
			t.Errorf("the reviewer's prompt does not show %q:\n%s", want, p)
		}
	}
	if strings.Contains(p, "h1:x=") {
		t.Error("the reviewer was shown the lock file's diff")
	}
	h.headApproved(t)
}

// E2E 4: with no review owed, no reviewer runs: (a) Review skipped and no package file; (b) Review off
// and go.mod changed.
func TestNoReviewIsOwedWithoutAnApprovalOrAPackageFile(t *testing.T) {
	t.Parallel()
	t.Run("skipped", func(t *testing.T) {
		h := newHeadKit(t, true, map[string]string{"app.go": "package app\n"}, func(int) string { return "approve" })
		if prompts := h.done([]string{"review", "test"}); len(prompts) != 0 {
			t.Fatalf("%d reviewer turns", len(prompts))
		}
	})
	t.Run("review off", func(t *testing.T) {
		h := newHeadKit(t, false, map[string]string{"go.mod": "module shop\n"}, func(int) string { return "approve" })
		if prompts := h.done([]string{"test"}); len(prompts) != 0 {
			t.Fatalf("%d reviewer turns", len(prompts))
		}
	})
}

// E2E 5: a ticket that passed Review with no change after it merges with one review.
func TestAnApprovedHeadIsNotReviewedTwice(t *testing.T) {
	t.Parallel()
	h := newHeadKit(t, false, nil, func(int) string { return "approve" })
	if prompts := h.done([]string{"review", "test"}); len(prompts) != 1 {
		t.Fatalf("%d reviewer turns, want 1", len(prompts))
	}
	if n := h.count(`SELECT count(*) FROM subtasks WHERE kind = 'review'`); n != 1 {
		t.Fatalf("%d reviewer subtasks, want 1", n)
	}
}

// E2E 6: a reviewer in Test that ends its turn without write_review is reminded, as in Review, and
// the ticket goes on when it answers.
func TestAReReviewWithoutAVerdictIsReminded(t *testing.T) {
	t.Parallel()
	h := newHeadKit(t, false, map[string]string{"broken.txt": "x"}, func(n int) string {
		if n == 2 {
			return "silent"
		}
		return "approve"
	})
	prompts := h.done([]string{"review", "test"})
	if len(prompts) != 3 || !strings.Contains(prompts[2], "You ended your turn without calling write_review") {
		t.Fatalf("%d reviewer turns, the last not a reminder", len(prompts))
	}
	if n := h.reviewersOn("test"); n != 1 {
		t.Fatalf("%d reviewers in Test, want 1 reminded", n)
	}
}

// E2E 7: with In progress as the ticket's last stage (Review on and skipped), a branch that changes
// go.mod is reviewed in In progress before it merges, and its builder is never told that the review
// failed its build.
func TestAPackageFileIsReviewedWhenInProgressIsTheLastStage(t *testing.T) {
	t.Parallel()
	h := newHeadKit(t, true, map[string]string{"go.mod": "module shop\n"}, func(int) string { return "approve" })
	prompts := h.done([]string{"review"})
	if len(prompts) != 1 || h.reviewersOn("in_progress") != 1 {
		t.Fatalf("%d reviewer turns, %d in In progress, want 1", len(prompts), h.reviewersOn("in_progress"))
	}
	h.ticketKit.mu.Lock()
	told := map[string]bool{}
	for kind, ps := range h.prompts {
		for _, p := range ps {
			told[kind] = told[kind] || kind != "review" && strings.Contains(p, "review_covers_head")
		}
	}
	h.ticketKit.mu.Unlock()
	for kind, ok := range told {
		if ok {
			t.Errorf("a %s turn was told about review_covers_head", kind)
		}
	}
	h.headApproved(t)
}

// headApproved fails the test unless SF-1's newest review approves the head it merged.
func (h *headKit) headApproved(t *testing.T) {
	t.Helper()
	if n := h.count(`SELECT count(*) FROM records r JOIN tickets t ON t.id = r.ticket
		WHERE r.kind = 'review' AND r.verdict = 'approve' AND r.id = (SELECT max(id) FROM records WHERE kind = 'review')
		AND EXISTS (SELECT 1 FROM check_runs c WHERE c.ticket = t.id AND c.purpose = 'merge' AND c.state = 'passed' AND c.head = r.commit)`); n != 1 {
		t.Fatal("the merged head is not the head the newest review approved")
	}
}
