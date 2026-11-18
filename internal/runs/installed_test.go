package runs

import (
	"context"
	"net/http"
	"net/http/httptest"
	"slices"
	"testing"

	"github.com/kesokaj/crew/internal/harness"
)

// What a tool was asked to run tells which package was installed, by the common package managers;
// a command that installs nothing names none.
func TestAnInstallIsFoundInWhatAToolRan(t *testing.T) {
	for in, want := range map[string]string{
		`{"CommandLine":"apt-get install -y ffmpeg"}`:                           "apt-get ffmpeg",
		`{"CommandLine":"sudo apt-get -qq install postgresql-client libpq5"}`:   "apt-get postgresql-client libpq5",
		`{"CommandLine":"cd web && pip install requests==2.32.0"}`:              "pip requests==2.32.0",
		`{"CommandLine":"npm install -g pnpm@9"}`:                               "npm pnpm@9",
		`{"CommandLine":"go install golang.org/x/tools/cmd/goimports@v0.30.0"}`: "go golang.org/x/tools/cmd/goimports@v0.30.0",
		`{"CommandLine":"apk add jq"}`:                                          "apk jq",
		`{"CommandLine":"npm install"}`:                                         "",
		`{"CommandLine":"make test"}`:                                           "",
		`{"CommandLine":"echo \"pip install\""}`:                                "",
		`{"path":"docs/install.md"}`:                                            "",
	} {
		if got := installed(in); got != want {
			t.Errorf("installed(%s) = %q, want %q", in, got, want)
		}
	}
}

// A run that installs a package records the signal once for each command, with what it ran on (10.2).
func TestAToolInstalledInARunIsASignalOnce(t *testing.T) {
	k := newKit(t)
	line := []byte(`{"event":"step_update","step_update":{"step_index":1,"state":"ACTIVE","step_type":"tool","tool_name":"run_command","tool_info":{"name":"run_command","parameters":{"CommandLine":"apt-get install -y ffmpeg"}}}}`)
	k.play(harness.Script{Lines: [][]byte{line, line}})
	run := k.start(StartRequest{CLI: "agy"})
	k.wait(run, "idle", "ended")
	var n int
	var cmd, cliName string
	if err := k.db.Server.QueryRow(context.Background(), `SELECT count(*), max(detail->>'command'), max(cli) FROM signals WHERE kind = 'tool_installed' AND run = $1`, run).
		Scan(&n, &cmd, &cliName); err != nil {
		t.Fatal(err)
	}
	if n != 1 || cmd != "apt-get ffmpeg" || cliName != "agy" {
		t.Fatalf("%d signals, %q on %q", n, cmd, cliName)
	}
}

// A run of an eval reads its own branch and its work in progress, not main, which holds the work it
// replays (9.6); one whose branch is no eval's gets nothing.
func TestAnEvalRunReadsOnlyItsBranch(t *testing.T) {
	k := newKit(t)
	k.play(harness.Script{Recording: agyRun, Hold: true})
	run := k.start(StartRequest{})
	v := k.wait(run, "working")
	k.d.Auth = fakeAuth{sandbox: v.Sandbox}
	ctx := context.Background()
	if _, err := k.db.Server.Exec(ctx, `UPDATE subtasks SET scope = 'eval' WHERE id = (SELECT subtask FROM runs WHERE id = $1)`, run); err != nil {
		t.Fatal(err)
	}
	if _, err := k.db.Server.Exec(ctx, `UPDATE runs SET branch = 'eval/7' WHERE id = $1`, run); err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodGet, "/git/fixture.git/info/refs", nil)
	req.Header.Set("Authorization", "Bearer the-token")
	acc, err := k.d.GitAccess(req, "fixture")
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(acc.Read, []string{"refs/heads/eval/7", "refs/crew/wip/" + run}) {
		t.Fatalf("an eval run reads %v", acc.Read)
	}
	if _, err := k.db.Server.Exec(ctx, `UPDATE runs SET branch = 'task/SF-1-x' WHERE id = $1`, run); err != nil {
		t.Fatal(err)
	}
	if _, err := k.d.GitAccess(req, "fixture"); err == nil {
		t.Fatal("an eval run on a ticket's branch got access")
	}
	k.d.Stop(ctx, run)
	k.wait(run, "ended")
}
