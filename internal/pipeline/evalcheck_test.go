package pipeline

import (
	"archive/tar"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os/exec"
	"strings"
	"testing"

	"github.com/kesokaj/crew/internal/board"
	"github.com/kesokaj/crew/internal/gitstore"
	"github.com/kesokaj/crew/internal/harness"
	"github.com/kesokaj/crew/internal/settings"
)

// checkAnswer is a check sandbox that gives every command result, or err and no result at all.
type checkAnswer struct {
	result harness.CheckResult
	err    error
}

func (a checkAnswer) ExecFresh(_ context.Context, spec harness.CheckSpec) ([]harness.CheckResult, error) {
	if a.err != nil {
		return nil, a.err
	}
	var out []harness.CheckResult
	for _, c := range spec.Cmds {
		r := a.result
		r.Name = c.Name
		out = append(out, r)
	}
	return out, nil
}

// An eval reads its case's check by where an error came from (13.9, since 1.02). After the project's
// setup began on the builder's tree, in a sandbox where the agent's code can be root, a setup that
// failed, a command past its time, and the sandbox ending under a command are the branch's: a failed
// round, then a goal not met. So is a tree larger than a check sandbox takes. A check sandbox that gave
// no result measured nothing. The builder is never told the setup's output.
func TestAnEvalReadsItsChecksEnd(t *testing.T) {
	setup := harness.CheckResult{Code: 2, Error: harness.SetupError + " ended with 2", Output: "held_out_test.go:3: undefined: Counts"}
	for _, c := range []struct {
		name   string
		answer checkAnswer
		large  bool
		rounds int
		want   string
		text   string
		kept   string
	}{
		{"passed", checkAnswer{result: harness.CheckResult{Passed: true}}, false, 0, "done/true", "the checks passed with the held-out files", ""},
		{"failed", checkAnswer{result: harness.CheckResult{Code: 1, Output: "FAIL"}}, false, 0, "fixing/<nil>", "and these failed: make test.", "$ make test (exit 1)\nFAIL"},
		{"the setup", checkAnswer{result: setup}, false, 0, "fixing/<nil>", "before the case's checks, and the setup failed.", ""},
		{"the setup, at the last round", checkAnswer{result: setup}, false, 1, "done/false", "the project's setup failed on the branch", ""},
		{"the setup the runtime could not run", checkAnswer{result: harness.CheckResult{Code: -1, Error: harness.SetupError + ": exec attach: EOF"}}, false, 0,
			"fixing/<nil>", "and the setup failed.", ""},
		{"past its time", checkAnswer{result: harness.CheckResult{Code: 124, Error: "it ran longer than 30m0s"}}, false, 0, "fixing/<nil>", "and these failed: make test.",
			"$ make test (exit 124; it ran longer than 30m0s)\n"},
		{"the sandbox ended under a command", checkAnswer{result: harness.CheckResult{Code: -1, Error: "the check sandbox: exec attach: EOF"}}, false, 1,
			"done/false", "the checks failed: make test", ""},
		{"no result", checkAnswer{err: errors.New("the check sandbox: no room")}, false, 0, "failed/<nil>", "the check sandbox could not run the checks; the error is in Crew's server log", ""},
		{"too large", checkAnswer{result: harness.CheckResult{Passed: true}}, true, 0, "fixing/<nil>", "its tree is larger than", "none"},
		{"too large, at the last round", checkAnswer{result: harness.CheckResult{Passed: true}}, true, 1, "done/false", "the branch's tree is larger than a check sandbox takes", ""},
	} {
		t.Run(c.name, func(t *testing.T) {
			if c.large {
				old := maxCheckTree
				maxCheckTree = 10
				defer func() { maxCheckTree = old }()
			}
			got, fix, why, kept := checkCase(t, c.answer, c.rounds)
			text := why
			if strings.HasPrefix(got, evalFixing) {
				text = fix
			}
			if got != c.want || !strings.Contains(text, c.text) {
				t.Fatalf("%s %q, want %s %q", got, text, c.want, c.text)
			}
			if strings.Contains(why, "no room") {
				t.Fatalf("an agent reads Crew's own error: %q", why)
			}
			if strings.Contains(fix, "undefined: Counts") {
				t.Fatalf("the builder is told the setup's output: %q", fix)
			}
			// What is kept for the operator names a command's error; a tree not checked keeps no round.
			if (c.kept == "none" && kept != "") || (c.kept != "none" && !strings.Contains(kept, c.kept)) {
				t.Fatalf("kept %q, want %q", kept, c.kept)
			}
		})
	}
}

// checkCase runs an eval run's check with answer, at check round rounds of a limit of one, and says
// how the eval run is now, its fix text, its why, and what is kept of its failed rounds.
func checkCase(t *testing.T, answer checkAnswer, rounds int, heldOut ...string) (got, fix, why, kept string) {
	t.Helper()
	return checkCaseOn(t, answer, rounds, nil, heldOut...)
}

// checkCaseOn is checkCase with the eval's branch moved by move first, from the case's base commit.
func checkCaseOn(t *testing.T, answer checkAnswer, rounds int, move func(tools *Tools, branch, base string), heldOut ...string) (got, fix, why, kept string) {
	t.Helper()
	ctx := context.Background()
	tools, proj, _ := toolsKit(t)
	base := tools.Board.Git.Main(ctx, proj.Name)
	var caseID, suite, er int64
	tools.Pool.QueryRow(ctx, `INSERT INTO eval_cases (project, number, title, origin, base_commit, why, what, goal, proposed_by)
		VALUES ($1, 1, 'One', 'operator', $2, 'w', 'w', 'g', 'operator') RETURNING id`, proj.ID, base).Scan(&caseID)
	tools.Pool.QueryRow(ctx, `INSERT INTO eval_suites (project, purpose, repeats, budget_usd, state, asked_by, started_at)
		VALUES ($1, 'on_demand', 1, 10, 'running', 'operator', now()) RETURNING id`, proj.ID).Scan(&suite)
	if err := tools.Pool.QueryRow(ctx, `INSERT INTO eval_runs (suite, project, eval_case, repeat, side, state, crew_version, check_rounds)
		VALUES ($1, $2, $3, 1, 'baseline', 'checking', '1.2.0', $4) RETURNING id`, suite, proj.ID, caseID, rounds).Scan(&er); err != nil {
		t.Fatal(err)
	}
	branch := fmt.Sprintf("eval/%d", er)
	if err := tools.Board.Git.EvalBranch(ctx, proj.Name, branch, base); err != nil {
		t.Fatal(err)
	}
	tools.Pool.Exec(ctx, `UPDATE eval_runs SET branch = $2 WHERE id = $1`, er, branch)
	if move != nil {
		move(tools, branch, base)
	}
	p := &Pipeline{Pool: tools.Pool, Board: tools.Board, Checks: answer, Version: "1.2.0", Log: slog.New(slog.NewTextHandler(io.Discard, nil))}
	j := &job{doc: settings.Doc{"limits": settings.Doc{"max_fix_rounds": 1}}, proj: proj}
	e := &evalRun{id: er, state: evalChecking, branch: branch, checks: rounds, detail: map[string]any{}}
	c := &board.EvalCase{BaseCommit: base, Checks: []board.CheckSpec{{Kind: "cmd", Spec: "make test"}}, HeldOut: heldOut}
	p.checkEval(ctx, j, &evalSuite{}, e, c)
	p.wg.Wait()
	var state string
	var met *bool
	tools.Pool.QueryRow(ctx, `SELECT state, goal_met, coalesce(detail->>'fix', ''), coalesce(detail->>'why', ''), coalesce(failed_output, '')
		FROM eval_runs WHERE id = $1`, er).Scan(&state, &met, &fix, &why, &kept)
	got = state + "/<nil>"
	if met != nil {
		got = fmt.Sprintf("%s/%v", state, *met)
	}
	return got, fix, why, kept
}

// An eval run that measured nothing for an error of Crew's own step gives its readers a fixed sentence,
// never the error, which can name the server's paths or relay git's text: it goes to the server's log
// (13.9, since 1.02).
func TestAnEvalsOwnErrorIsAFixedSentence(t *testing.T) {
	got, _, why, _ := checkCase(t, checkAnswer{result: harness.CheckResult{Passed: true}}, 0, "gone_test.go")
	if got != "failed/<nil>" || why != "Crew could not copy the held-out files in; the error is in Crew's server log" {
		t.Fatalf("a held-out copy that failed: %s %q", got, why)
	}
}

// An eval's archive stops at what a check sandbox takes: nothing past the bound reaches the buffer, so
// a branch's large files do not fill the server's memory (since 1.02).
func TestAnEvalsTreeStopsAtTheBound(t *testing.T) {
	var buf strings.Builder
	c := &treeCap{w: &buf, left: 8}
	if n, err := c.Write([]byte("12345")); n != 5 || err != nil || c.over {
		t.Fatalf("within the bound: %d %v %v", n, err, c.over)
	}
	if n, err := c.Write([]byte("6789")); n != 0 || err == nil || !c.over {
		t.Fatalf("past the bound: %d %v %v", n, err, c.over)
	}
	if buf.String() != "12345" {
		t.Fatalf("the buffer took %q", buf.String())
	}
}

// A branch whose tree lists more paths than a check sandbox takes, from a pack of a few hundred bytes,
// is the branch's tree too large: Archive stops at the bound, and the eval reads it as the size it
// measures itself (13.9, since 1.03).
func TestAnEvalsBranchPastTheTreesBoundIsTooLarge(t *testing.T) {
	bomb := func(tools *Tools, branch, base string) {
		q := tools.Board.Git.Quarantine.Path("shop")
		run := func(stdin string, args ...string) string {
			cmd := exec.Command("git", args...)
			cmd.Dir, cmd.Stdin = q, strings.NewReader(stdin)
			cmd.Env = gitstore.CleanEnv()
			out, err := cmd.Output()
			if err != nil {
				t.Fatalf("git %v: %v", args, err)
			}
			return strings.TrimSpace(string(out))
		}
		obj, mode, typ := run("x\n", "hash-object", "-w", "--stdin"), "100644", "blob"
		for range 5 {
			var list strings.Builder
			for i := range 16 {
				fmt.Fprintf(&list, "%s %s %s\t%x\n", mode, typ, obj, i)
			}
			obj, mode, typ = run(list.String(), "mktree"), "040000", "tree"
		}
		run("", "update-ref", "refs/heads/"+branch, run("", "commit-tree", obj, "-p", base, "-m", "bomb"))
	}
	got, fix, _, _ := checkCaseOn(t, checkAnswer{result: harness.CheckResult{Passed: true}}, 0, bomb)
	if got != "fixing/<nil>" || !strings.Contains(fix, "its tree is larger than") {
		t.Fatalf("a branch past the tree's bound: %s %q", got, fix)
	}
	if !(&treeCap{}).tooLarge(fmt.Errorf("x: %w", gitstore.ErrTreeTooLarge)) || (&treeCap{}).tooLarge(errors.New("x")) {
		t.Fatal("the bound's error is not read as a tree too large")
	}
}

func tarFiles(t *testing.T, archive []byte) map[string]string {
	t.Helper()
	out := map[string]string{}
	tr := tar.NewReader(bytes.NewReader(archive))
	for {
		hdr, err := tr.Next()
		if errors.Is(err, io.EOF) {
			return out
		}
		if err != nil {
			t.Fatal(err)
		}
		if hdr.Typeflag != tar.TypeReg && hdr.Typeflag != tar.TypeRegA {
			continue
		}
		b, err := io.ReadAll(tr)
		if err != nil {
			t.Fatal(err)
		}
		out[hdr.Name] = string(b)
	}
}

func commitTree(t *testing.T, dir, parent string, files map[string]string) string {
	t.Helper()
	git := func(stdin string, args ...string) string {
		cmd := exec.Command("git", args...)
		cmd.Dir, cmd.Stdin = dir, strings.NewReader(stdin)
		cmd.Env = gitstore.CleanEnv()
		out, err := cmd.Output()
		if err != nil {
			t.Fatalf("git %v: %v", args, err)
		}
		return strings.TrimSpace(string(out))
	}
	tree := git("", "rev-parse", parent+"^{tree}")
	for path, body := range files {
		blob := git(body, "hash-object", "-w", "--stdin")
		parts := strings.Split(path, "/")
		leaf := parts[len(parts)-1]
		sub := fmt.Sprintf("100644 blob %s\t%s\n", blob, leaf)
		for i := len(parts) - 2; i >= 0; i-- {
			subTree := git(sub, "mktree")
			sub = fmt.Sprintf("040000 tree %s\t%s\n", subTree, parts[i])
		}
		// Replace the top-level entry in tree.
		top := parts[0]
		var kept strings.Builder
		for _, line := range strings.Split(git("", "ls-tree", tree), "\n") {
			if line != "" && !strings.HasSuffix(line, "\t"+top) {
				kept.WriteString(line + "\n")
			}
		}
		kept.WriteString(sub)
		tree = git(kept.String(), "mktree")
	}
	return git("", "commit-tree", tree, "-p", parent, "-m", "commit")
}

// An eval case's held-out check starts in the project's check mode with the branch's tree alone for
// the setup, asks for its route out to be cut after the setup, and carries the held-out files
// separately to be placed only after the cut; a ticket's own check of the same project keeps the
// project's check mode with no held-out part and no cut (9.6, 13.6, 13.9, register row 22).
func TestAnEvalsHeldOutFilesComeAfterTheSetupWithNoRouteOut(t *testing.T) {
	ctx := context.Background()
	tools, proj, _ := toolsKit(t)
	if _, err := tools.Pool.Exec(ctx, `INSERT INTO settings (project, doc, updated_by) VALUES ($1, '{"sandbox": {"check_network": "sources"}}', 'operator')`, proj.ID); err != nil {
		t.Fatal(err)
	}
	base := tools.Board.Git.Main(ctx, proj.Name)
	canon := tools.Board.Git.Canonical(proj.Name)
	merged := commitTree(t, canon, base, map[string]string{
		"held_test.go":       "held-out test\n",
		"held_only_test.go":  "held-out only\n",
		"testdata/cases.txt": "held-out cases\n",
	})
	tk, err := tools.Board.Ticket(ctx, proj, "SF-1")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := tools.Pool.Exec(ctx, `INSERT INTO check_runs (project, ticket, purpose, commit, state) VALUES ($1, $2, 'merge', $3, 'passed')`, proj.ID, tk.ID, merged); err != nil {
		t.Fatal(err)
	}

	var caseID, suite, er int64
	if err := tools.Pool.QueryRow(ctx, `INSERT INTO eval_cases (project, number, title, origin, origin_ticket, base_commit, why, what, goal, checks, held_out, proposed_by)
		VALUES ($1, 1, 'One', 'ticket', $2, $3, 'w', 'w', 'g', '[{"kind":"cmd","spec":"go test ./..."}]', $4, 'operator') RETURNING id`,
		proj.ID, tk.ID, base, []string{"held_test.go", "held_only_test.go", "testdata/cases.txt"}).Scan(&caseID); err != nil {
		t.Fatal(err)
	}
	c, err := tools.Board.Case(ctx, tools.Pool, proj.ID, caseID)
	if err != nil {
		t.Fatal(err)
	}
	tools.Pool.QueryRow(ctx, `INSERT INTO eval_suites (project, purpose, repeats, budget_usd, state, asked_by, started_at)
		VALUES ($1, 'on_demand', 1, 10, 'running', 'operator', now()) RETURNING id`, proj.ID).Scan(&suite)
	if err := tools.Pool.QueryRow(ctx, `INSERT INTO eval_runs (suite, project, eval_case, repeat, side, state, crew_version)
		VALUES ($1, $2, $3, 1, 'baseline', 'checking', '1.2.0') RETURNING id`, suite, proj.ID, caseID).Scan(&er); err != nil {
		t.Fatal(err)
	}
	branch := fmt.Sprintf("eval/%d", er)
	if err := tools.Board.Git.EvalBranch(ctx, proj.Name, branch, base); err != nil {
		t.Fatal(err)
	}
	tools.Pool.Exec(ctx, `UPDATE eval_runs SET branch = $2 WHERE id = $1`, er, branch)
	q := tools.Board.Git.Quarantine.Path(proj.Name)
	head := commitTree(t, q, base, map[string]string{
		"count.go":     "branch code\n",
		"held_test.go": "branch test\n",
		"testdata":     "branch file\n",
	})
	quarantineGit(t, tools, proj.Name)("update-ref", "refs/heads/"+branch, head)

	fake := &harness.Fake{}
	p := &Pipeline{Pool: tools.Pool, Board: tools.Board, Checks: fake, Version: "1.2.0", Log: slog.New(slog.NewTextHandler(io.Discard, nil))}
	j := &job{doc: settings.Doc{"sandbox": settings.Doc{"setup": "make setup"}, "limits": settings.Doc{"max_fix_rounds": 1}}, proj: proj}
	e := &evalRun{id: er, state: evalChecking, branch: branch, detail: map[string]any{}}
	p.checkEval(ctx, j, &evalSuite{}, e, c)
	p.wg.Wait()

	if _, err := p.runChecks(ctx, j, tk, board.PurposeStage, base, "", "in_progress", checkCmds(tk.Checks, "")); err != nil {
		t.Fatal(err)
	}
	p.wg.Wait()

	if len(fake.Checks) != 2 {
		t.Fatalf("%d check runs, want 2", len(fake.Checks))
	}
	evalSpec, ticketSpec := fake.Checks[0], fake.Checks[1]
	if evalSpec.Mode != "sources" || !evalSpec.Cut || evalSpec.Setup != "make setup" {
		t.Fatalf("eval check spec: mode=%q cut=%v setup=%q", evalSpec.Mode, evalSpec.Cut, evalSpec.Setup)
	}
	treeFiles := tarFiles(t, evalSpec.Tree)
	if treeFiles["count.go"] != "branch code\n" || treeFiles["held_test.go"] != "branch test\n" || treeFiles["testdata"] != "branch file\n" {
		t.Fatalf("eval tree files: %v", treeFiles)
	}
	if _, ok := treeFiles["held_only_test.go"]; ok {
		t.Fatalf("held_only_test.go is in the setup tree: %v", treeFiles)
	}
	if _, ok := treeFiles["testdata/cases.txt"]; ok {
		t.Fatalf("testdata/cases.txt is in the setup tree: %v", treeFiles)
	}
	heldFiles := tarFiles(t, evalSpec.HeldOut)
	if len(heldFiles) != 3 || heldFiles["held_test.go"] != "held-out test\n" ||
		heldFiles["held_only_test.go"] != "held-out only\n" || heldFiles["testdata/cases.txt"] != "held-out cases\n" {
		t.Fatalf("eval held-out files: %v", heldFiles)
	}
	if ticketSpec.Mode != "sources" || ticketSpec.Cut || len(ticketSpec.HeldOut) != 0 {
		t.Fatalf("ticket check spec: mode=%q cut=%v heldOut=%d bytes", ticketSpec.Mode, ticketSpec.Cut, len(ticketSpec.HeldOut))
	}
}
