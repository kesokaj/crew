package httpapi_test

import (
	"context"
	"os/exec"
	"strings"
	"sync"
	"testing"

	"github.com/kesokaj/crew/internal/gitstore"
	"github.com/kesokaj/crew/internal/harness"
	"github.com/kesokaj/crew/internal/pipeline"
)

// One ticket's error in the merge queue does not hold the tickets behind it (7.10; the security review
// of 0.3.7, finding 4). SF-1, first in the queue, gets a branch that shares no history with main, so
// Crew cannot read what it changes: it waits with a fixed reason, and SF-2 merges. The board's error on
// SF-1, which the steps of SF-2 reconcile with, does not stop them either.
func TestAMergeErrorOfOneTicketHoldsNoOther(t *testing.T) {
	t.Parallel()
	k := newTicketKit(t)
	release := make(chan struct{})
	var closeOnce sync.Once
	unblock := func() { closeOnce.Do(func() { close(release) }) }
	t.Cleanup(unblock)
	// SF-1 changes a GitHub workflow, so it waits in the queue for the operator, its builder ended.
	k.builder(func(kind, key string, spec harness.RunSpec, p string) bool {
		if kind != "build" || key != "SF-1" {
			return false
		}
		k.pushFromRoot("task/SF-1-one", map[string]string{"1.txt": key, ".github/workflows/ci.yml": "on: push\n"})
		k.tool(spec.Run, "add_note", map[string]any{"text": "Built " + key})
		k.tool(spec.Run, "request_move", map[string]any{})
		return true
	})
	built := k.custom
	k.custom = func(kind string, spec harness.RunSpec, p string) (harness.Script, bool) {
		sc, ok := built(kind, spec, p)
		if kind == "build" && k.keyOf(spec, p) == "SF-2" {
			sc.Wait = release
		}
		return sc, ok
	}
	k.shop([]string{}, nil, "One", "Two")
	if _, err := k.d.Server.Exec(context.Background(), `UPDATE tickets SET priority = 1 WHERE number = 1`); err != nil {
		t.Fatal(err)
	}
	stop := k.run()
	defer stop()
	k.until("SF-1 waits for the operator", func() bool {
		return k.count(`SELECT count(*) FROM tickets WHERE number = 1 AND code_wait IS NOT NULL`) == 1
	})
	// Then its branch shares no history with main: Crew cannot read what it changes, on every pass, and
	// SF-2 builds behind it in the queue.
	k.orphan("task/SF-1-one")
	unblock()
	k.until("SF-2 is Done", func() bool { return k.stageOf("SF-2") == "done" })
	v := k.ticket("SF-1").Ticket
	if v.Stage != "in_progress" || deref(v.Stuck) != pipeline.CouldNotMerge {
		t.Fatalf("SF-1 in %s, stuck %q", v.Stage, deref(v.Stuck))
	}
}

// orphan writes a branch again as one commit of the same tree with no parent, in the quarantine
// repository, so it shares no history with main.
func (k *ticketKit) orphan(branch string) {
	git := func(args ...string) string {
		cmd := exec.Command("git", append([]string{"-C", k.git.Quarantine.Path("shop")}, args...)...)
		cmd.Env = gitstore.CleanEnv("GIT_AUTHOR_NAME=agent", "GIT_AUTHOR_EMAIL=agent@x",
			"GIT_COMMITTER_NAME=agent", "GIT_COMMITTER_EMAIL=agent@x")
		out, err := cmd.CombinedOutput()
		if err != nil {
			k.t.Errorf("git %v: %v %s", args, err, out)
		}
		return strings.TrimSpace(string(out))
	}
	c := git("commit-tree", "refs/heads/"+branch+"^{tree}", "-m", "rewritten")
	git("update-ref", "refs/heads/"+branch, c)
}
