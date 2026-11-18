package pipeline

import (
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"

	"github.com/kesokaj/crew/internal/board"
	"github.com/kesokaj/crew/internal/harness"
)

// The chat's get_checks gives an errored check run's detail and its commands' errors as CouldNotCheck,
// and a setup's own end as it is (13.9, the security review of session 57, finding 1).
func TestTheChatReadsNoCheckRunsError(t *testing.T) {
	const crews = "the check sandbox: open /var/lib/crew/sandboxes/t37-sb/home: permission denied"
	k := newChatKit(t)
	if _, err := k.tools.Pool.Exec(k.ctx, `INSERT INTO check_runs (project, ticket, purpose, commit, state, detail, results) VALUES
		($1, $2, 'test', 'ddddddd', 'error', $3, jsonb_build_array(jsonb_build_object('name', 'make test', 'code', -1, 'passed', false, 'error', $3::text))),
		($1, $2, 'part', 'eeeeeee', 'error', $4, jsonb_build_array(jsonb_build_object('name', 'make', 'code', 2, 'passed', false, 'error', $4::text, 'output', 'npm ERR!')))`,
		k.p.ID, k.one.ID, crews, "the project's setup ended with 2"); err != nil {
		t.Fatal(err)
	}
	ch, err := k.call(k.tools.getChecks, k.chat, `{}`)
	if err != nil {
		t.Fatal(err)
	}
	b, _ := json.Marshal(ch)
	if strings.Contains(string(b), "/var/lib/crew") {
		t.Fatalf("get_checks holds Crew's error: %s", b)
	}
	if strings.Count(string(b), board.CouldNotCheck) != 2 || strings.Count(string(b), "the project's setup ended with 2") != 2 || !strings.Contains(string(b), "npm ERR!") {
		t.Fatalf("get_checks: %s", b)
	}
	var kept int
	if err := k.tools.Pool.QueryRow(k.ctx, `SELECT count(*) FROM check_runs WHERE detail = $1`, crews).Scan(&kept); err != nil || kept != 1 {
		t.Fatalf("the run keeps its detail for the operator: %d %v", kept, err)
	}
}

// A fix round's text gives a setup the runtime could not run, which the pipeline counts as a failure,
// as CouldNotCheck, and a setup's own end and the output as they are (13.9).
func TestFailuresTextHoldsNoCrewError(t *testing.T) {
	run := &board.CheckRun{State: board.CheckFailed, Results: []board.CmdResult{
		{Name: "make test", Code: -1, Error: harness.SetupError + ": exec: /var/lib/docker/t37: no such file", Output: "npm ERR! one"},
		{Name: "make e2e", Code: 2, Error: harness.SetupError + " ended with 2", Output: "npm ERR! two"},
	}}
	text := failuresText(run)
	if strings.Contains(text, "/var/lib/docker") || !strings.Contains(text, "("+board.CouldNotCheck+")") ||
		!strings.Contains(text, "("+harness.SetupError+" ended with 2)") || !strings.Contains(text, "npm ERR! one") {
		t.Fatalf("the text: %s", text)
	}
}

// detailUse is any read or write of a field named Detail: a check run's, a check's result's, a
// signal's, a draft's.
var detailUse = regexp.MustCompile(`\b\w+(\[[^\]]*\])?\.Detail\b`)

// No code of the board or the pipeline hands a check run's detail on as it is: the reasons, run ends,
// signals and texts built from it, which agents read, go through board.PublicDetail (13.9). Its
// twelve Wait sites have no scenario that reaches them, so this reads the source. Every use of a
// Detail in the two packages is listed: the helper itself, a failed merge's detail (Crew's list of
// the files that conflict), the scans into a run, and the Details of other types. A new one fails
// here until it is read and listed. A read through a method, or by another name than Detail, is not
// seen.
func TestNoCheckRunsDetailIsHandedOnRaw(t *testing.T) {
	allowed := map[string][]string{
		"checkerror.go": {"d := deref(run.Detail)", "run.Detail = &d", "run.Detail = nil"},
		"merge.go":      {"text = derefS(last.Detail)"},
		"checks.go":     {"FROM check_runs WHERE id = $1`, id).Scan(&c.ID, &c.Purpose, &c.Commit, &c.Main, &c.Head, &c.State, &c.Results, &c.Detail,"},
		"checkruns.go": {"err := row.Scan(&c.ID, &c.Purpose, &c.Commit, &c.Main, &c.Head, &c.State, &c.Results, &c.Detail, &c.Flaky, &c.StartedAt, &c.EndedAt, &c.Stage, &c.Criteria)",
			"detail, err := json.Marshal(s.Detail)", "if err != nil || s.Detail == nil {"},
		"stages.go":    {`failed = append(failed, "- "+x.Name+": "+x.Detail)`},
		"reconcile.go": {"t.ID, moveID, t.Stage, r.Name, r.Passed, r.Detail, b.now()); err != nil {", "return wait(r.Detail)"},
		"drafts.go":    {"err := r.Scan(&x.Remote, &x.Refs, &x.State, &x.Tries, &x.Detail, &x.CreatedAt, &x.DoneAt)"},
	}
	for _, dir := range []string{".", "../board"} {
		files, err := filepath.Glob(filepath.Join(dir, "*.go"))
		if err != nil || len(files) == 0 {
			t.Fatalf("%s: %v", dir, err)
		}
		for _, f := range files {
			if strings.HasSuffix(f, "_test.go") {
				continue
			}
			b, err := os.ReadFile(f)
			if err != nil {
				t.Fatal(err)
			}
			for i, line := range strings.Split(string(b), "\n") {
				if detailUse.MatchString(line) && !slices.Contains(allowed[filepath.Base(f)], strings.TrimSpace(line)) {
					t.Errorf("%s:%d uses a Detail this test has not read: %s", f, i+1, strings.TrimSpace(line))
				}
			}
		}
	}
}
