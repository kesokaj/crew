package runs

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/kesokaj/crew/internal/db/dbtest"
	"github.com/kesokaj/crew/internal/gitendpoint"
	"github.com/kesokaj/crew/internal/harness"
)

// TestMain lets the test program test a push's paths for the git endpoint's hook, as the server does,
// and starts tools/check-db.sh when go test ./internal/runs runs outside make test-go.
func TestMain(m *testing.M) {
	if len(os.Args) > 1 && os.Args[1] == gitendpoint.HookCommand {
		os.Exit(gitendpoint.PreReceive(os.Stdin, os.Stderr))
	}
	stopDB := startTestDB()
	code := m.Run()
	stopDB()
	os.Exit(code)
}

func startTestDB() func() {
	if os.Getenv(dbtest.EnvHost) != "" {
		return func() {}
	}
	root, err := filepath.Abs("../..")
	if err != nil {
		return func() {}
	}
	script := filepath.Join(root, "tools", "check-db.sh")
	if _, err := os.Stat(script); err != nil {
		return func() {}
	}
	docker := "sudo docker"
	inspectArgs := []string{"sudo", "docker", "inspect", "-f", "{{range .NetworkSettings.Networks}}{{.IPAddress}}{{end}}", "crew-check-db"}
	if os.Geteuid() == 0 {
		docker = "docker"
		inspectArgs = inspectArgs[1:]
	}
	run := func(arg string) error {
		cmd := exec.Command("bash", script, arg)
		cmd.Dir = root
		cmd.Env = append(os.Environ(), "DOCKER="+docker)
		cmd.Stdout = os.Stderr
		cmd.Stderr = os.Stderr
		return cmd.Run()
	}
	if err := run("start"); err != nil {
		return func() {}
	}
	out, err := exec.Command(inspectArgs[0], inspectArgs[1:]...).Output()
	ip := strings.TrimSpace(string(out))
	if err != nil || ip == "" {
		_ = run("stop")
		return func() {}
	}
	_ = os.Setenv(dbtest.EnvHost, ip)
	_ = os.Setenv("CREW_DB_SECRETS_DIR", filepath.Join(root, ".check", "secrets"))
	return func() { _ = run("stop") }
}

// A parallel subtask's push is measured from its ticket's branch and stays inside the files it named
// (7.11); a run on the ticket's branch itself is measured from main and names no files.
func TestAPartsPushStaysInsideItsFiles(t *testing.T) {
	r := pathRule("sub/SF-1.2", "task/SF-1-x", []string{"AGENTS.md"}, false, []string{"a/"})
	if r.Base != "refs/heads/task/SF-1-x" || len(r.Touches) != 1 || r.Touches[0] != "a/" || len(r.Allow) != 1 || r.Allow[0] != gitendpoint.Everything {
		t.Fatalf("a part's rule: %+v", r)
	}
	r = pathRule("task/SF-1-x", "task/SF-1-x", []string{"AGENTS.md"}, true, nil)
	if r.Base != "refs/heads/main" || r.Touches != nil || len(r.Allow) != 2 {
		t.Fatalf("the ticket's rule: %+v", r)
	}
}

// Every run reads main's skills and context but the builder of a ticket that may change them, once
// that counts (12.9, 13.8).
func TestOnlyTheBuilderOfATicketThatChangesThemKeepsItsOwnInstructions(t *testing.T) {
	for _, c := range []struct {
		may  []string
		want bool
	}{
		{nil, false}, {[]string{"internal/**"}, false}, {[]string{"AGENTS.md"}, true}, {[]string{".agents/skills/**"}, true},
		{[]string{".agents/skills/flows/SKILL.md"}, true}, {[]string{".agents/"}, true}, {[]string{"docs/AGENTS.md"}, true},
		// A repository's workflows are not read by a run (review of M12).
		{[]string{".github/workflows/ci.yml"}, false},
	} {
		if got := namesInstructions(c.may); got != c.want {
			t.Errorf("%v: %v", c.may, got)
		}
	}
	d := &Driver{}
	for _, c := range []struct {
		role string
		own  bool
		want bool
	}{{"builder", false, true}, {"builder", true, false}, {"reviewer", true, true}, {"lead", false, true}} {
		spec := d.specFor(scope{role: c.role, project: "p", repo: "p", ownInstructions: c.own}, harness.RunSpec{}, "task/x", "")
		if spec.MainInstructions != c.want {
			t.Errorf("%s, own %v: main's instructions %v", c.role, c.own, spec.MainInstructions)
		}
	}
}
