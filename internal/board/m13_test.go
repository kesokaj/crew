package board_test

import (
	"sync"
	"testing"

	"github.com/kesokaj/crew/internal/board"
)

// Amendments sent at the same moment open one, not several: the open one is checked again under the
// project's lock, so each that passed the first look before another was written is refused there
// (7.13: one amendment is open at a time). The first look refuses most of them, so the check under
// the lock is also called on its own, as a second caller that passed the first look reaches it.
func TestAmendmentsAtTheSameMomentOpenOne(t *testing.T) {
	e := newEnv(t)
	p := e.project("shop", "SF")
	e.add(p, ready("Sign-in"), board.OriginPlan)
	e.ok(p)
	const n = 8
	var (
		start = make(chan struct{})
		wg    sync.WaitGroup
		mu    sync.Mutex
		made  int
	)
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			if _, err := e.b.AddAmendment(e.ctx, "shop", "Add sign-out.", board.Operator); err == nil {
				mu.Lock()
				made++
				mu.Unlock()
			} else if kindOf(err) != board.Refused {
				t.Error(err)
			}
		}()
	}
	close(start)
	wg.Wait()
	var rows int
	if err := e.db.Server.QueryRow(e.ctx, `SELECT count(*) FROM plans WHERE project = $1 AND kind = 'amendment'`, p.ID).Scan(&rows); err != nil {
		t.Fatal(err)
	}
	if made != 1 || rows != 1 {
		t.Fatalf("%d of %d amendments were made, and the plan has %d rows of them; want one", made, n, rows)
	}
	if err := e.b.AddPlanRowForTest(e.ctx, "shop", "amendment", "Add sign-out.", board.Operator); kindOf(err) != board.Refused {
		t.Fatalf("a second amendment past the first look: %v, want it refused under the lock", err)
	}
}

// What a test file is (7.1, since 0.47): by its name, by its folder, and by a command check that names
// it; nothing else.
func TestWhatATestFileIs(t *testing.T) {
	checks := []board.CheckSpec{{Kind: "cmd", Spec: "bash scripts/lockout.sh && test -f out/report.txt"}, {Kind: "judge", Spec: "docs/guide.md reads well"},
		{Kind: "cmd", Spec: "pytest ./auth/lockout.py::test_five"}, {Kind: "cmd", Spec: "npx playwright test --config=web/pw.config.mjs web/flows.mjs:12"}}
	for p, want := range map[string]bool{
		"internal/lock/lock_test.go": true, "auth/test_lockout.py": true, "auth/lockout_test.py": true,
		"web/src/login.test.tsx": true, "web/e2e/login.spec.ts": true, "tests/fixtures/users.json": true,
		"pkg/__tests__/a.js": true, "e2e/run.sh": true, "testdata/agy/run.jsonl": true,
		"scripts/lockout.sh": true, "out/report.txt": true,
		"auth/lockout.py":   true,
		"web/pw.config.mjs": true, "web/flows.mjs": true,
		"internal/lock/lock.go": false, "docs/guide.md": false, "testing.md": false, "contest/main.go": false, "latest.go": false,
		// A word that only ends with a path names another file.
		"report.txt": false, "lockout.sh": false,
	} {
		if got := board.IsTestFile(p, checks); got != want {
			t.Errorf("%s: %v, want %v", p, got, want)
		}
	}
}
