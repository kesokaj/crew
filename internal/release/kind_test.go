package release_test

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/kesokaj/crew/internal/release"
)

func manifest(files map[string]string, agents map[string]string, schema, readBy, setup int) release.Manifest {
	return release.Manifest{Files: files, Agents: agents, Schema: schema, ReadBy: readBy, MachineSetup: setup,
		Images: map[string]release.Image{"postgres": {Tag: "postgres:18.6"}}}
}

// The kind of a release (16.3) is decided from the paths it changes, by the running version.
func TestTheKindOfAReleaseComesFromThePathsItChanges(t *testing.T) {
	running := manifest(map[string]string{
		"baseline/agents/builder.md":    "a1",
		"baseline/stages/build.md":      "a2",
		"harness/skills/x/SKILL.md":     "a3",
		"harness/hooks/check.md":        "a4",
		"harness/skills/x/run.py":       "a5",
		"internal/board/board.go":       "a6",
		"deploy/crew-ctl":               "a7",
		"internal/settings/settings.go": "a8",
	}, map[string]string{"baseline/agents/builder.md": "model: one"}, 51, 0, 2)
	change := func(edit func(m map[string]string)) map[string]string {
		m := map[string]string{}
		for k, v := range running.Files {
			m[k] = v
		}
		edit(m)
		return m
	}
	cases := []struct {
		name      string
		next      release.Manifest
		setup     int
		kind      string
		protected string
	}{
		{"prompts and skills", manifest(change(func(m map[string]string) {
			m["baseline/stages/build.md"] = "b"
			m["harness/skills/x/SKILL.md"] = "b"
			m["harness/skills/y/SKILL.md"] = "new"
		}), running.Agents, 51, 0, 2), 2, release.KindText, ""},
		{"a removed skill is text", manifest(change(func(m map[string]string) { delete(m, "harness/skills/x/SKILL.md") }), running.Agents, 51, 0, 2), 2, release.KindText, ""},
		{"Markdown in hooks is code", manifest(change(func(m map[string]string) { m["harness/hooks/check.md"] = "b" }), running.Agents, 51, 0, 2), 2, release.KindCode, ""},
		{"a script under harness is code", manifest(change(func(m map[string]string) { m["harness/skills/x/run.py"] = "b" }), running.Agents, 51, 0, 2), 2, release.KindCode, ""},
		{"the server is code", manifest(change(func(m map[string]string) { m["internal/board/board.go"] = "b" }), running.Agents, 51, 0, 2), 2, release.KindCode, ""},
		{"the installer is protected", manifest(change(func(m map[string]string) { m["deploy/crew-ctl"] = "b" }), running.Agents, 51, 0, 2), 2, release.KindCode, "deploy/crew-ctl"},
		{"an agent's model is a setting", manifest(change(func(m map[string]string) { m["baseline/agents/builder.md"] = "b" }),
			map[string]string{"baseline/agents/builder.md": "model: two"}, 51, 0, 2), 2, release.KindCode, "baseline/agents/builder.md: the model or the effort"},
		{"a schema step is code", manifest(running.Files, running.Agents, 52, 0, 2), 2, release.KindCode, ""},
		// A schema step changes stored data, whatever its header says older versions read: it is
		// protected (the security review of dba3540; design 0.58).
		{"a schema step's file is protected", manifest(change(func(m map[string]string) { m["internal/db/migrations/0052_x.sql"] = "new" }), running.Agents, 52, 0, 2),
			2, release.KindCode, "internal/db/migrations/0052_x.sql"},
		{"the code that applies the steps is protected", manifest(change(func(m map[string]string) { m["internal/db/migrate.go"] = "new" }), running.Agents, 51, 0, 2),
			2, release.KindCode, "internal/db/migrate.go"},
		// The build's and the gate's sandboxes, the choice of a run's image, and crew-gate's settings
		// are the gate and the code that chooses what a sandbox gets (review of M16, F3; design 0.59).
		{"the build's and the check sandbox's code is protected", manifest(change(func(m map[string]string) { m["internal/harness/kata.go"] = "new" }), running.Agents, 51, 0, 2),
			2, release.KindCode, "internal/harness/kata.go"},
		{"the code that chooses a run's image is protected", manifest(change(func(m map[string]string) { m["internal/runs/driver.go"] = "new" }), running.Agents, 51, 0, 2),
			2, release.KindCode, "internal/runs/driver.go"},
		{"crew-gate's settings are protected", manifest(change(func(m map[string]string) { m["internal/board/crewproject.go"] = "new" }), running.Agents, 51, 0, 2),
			2, release.KindCode, "internal/board/crewproject.go"},
		// The server's command is protected whole, so neither main.go, which runs before serve, nor a
		// new file of it can set what the ingress's door reads (security review of 15e9dc4..ae44ea3,
		// finding 2; design 0.87).
		{"the server's main.go is protected", manifest(change(func(m map[string]string) { m["cmd/crew/main.go"] = "new" }), running.Agents, 51, 0, 2),
			2, release.KindCode, "cmd/crew/main.go"},
		{"a new file of the server's command is protected", manifest(change(func(m map[string]string) { m["cmd/crew/env.go"] = "new" }), running.Agents, 51, 0, 2),
			2, release.KindCode, "cmd/crew/env.go"},
		{"a command beside it is not", manifest(change(func(m map[string]string) { m["cmd/crew-run/main.go"] = "new" }), running.Agents, 51, 0, 2),
			2, release.KindCode, ""},
		{"the rest of the runs is code", manifest(change(func(m map[string]string) { m["internal/runs/other.go"] = "new" }), running.Agents, 51, 0, 2),
			2, release.KindCode, ""},
		{"a schema the running version cannot read is protected", manifest(running.Files, running.Agents, 53, 53, 2), 2, release.KindCode, "a schema the running version cannot read"},
		{"a newer machine setup", manifest(change(func(m map[string]string) { m["baseline/stages/build.md"] = "b" }), running.Agents, 51, 0, 3), 2, release.KindSetup, ""},
		{"a machine that has the setup", manifest(change(func(m map[string]string) { m["baseline/stages/build.md"] = "b" }), running.Agents, 51, 0, 3), 3, release.KindText, ""},
	}
	for _, c := range cases {
		k := release.Classify(running, c.next, c.setup)
		if k.Kind != c.kind {
			t.Errorf("%s: kind %s, want %s (%v)", c.name, k.Kind, c.kind, k.Changed)
		}
		if got := strings.Join(k.Protected, ","); got != c.protected {
			t.Errorf("%s: protected %q, want %q", c.name, got, c.protected)
		}
	}
	// What a release says about itself does not count: a code change named as text in its notes is
	// still code, since only the paths decide.
	next := manifest(change(func(m map[string]string) { m["internal/board/board.go"] = "b" }), running.Agents, 51, 0, 2)
	if release.Classify(running, next, 2).Kind != release.KindCode {
		t.Fatal("code passed as text")
	}
}

// What installs by itself (16.3): text under the default, nothing protected under unprotected, and
// nothing under never. A release that needs a newer machine setup never does.
func TestWhatInstallsByItself(t *testing.T) {
	text := release.Kind{Kind: release.KindText}
	code := release.Kind{Kind: release.KindCode}
	prot := release.Kind{Kind: release.KindCode, Protected: []string{"deploy/crew-ctl"}}
	setup := release.Kind{Kind: release.KindSetup}
	for _, c := range []struct {
		k    release.Kind
		auto string
		want bool
	}{
		{text, "text", true}, {code, "text", false}, {prot, "text", false}, {setup, "text", false},
		{text, "unprotected", true}, {code, "unprotected", true}, {prot, "unprotected", false}, {setup, "unprotected", false},
		{text, "never", false}, {code, "never", false},
	} {
		if got := release.InstallsBySelf(c.k, c.auto); got != c.want {
			t.Errorf("%s with %s: %v", c.k.Kind, c.auto, got)
		}
	}
}

// A protected path that names nothing protects nothing: each is a file or a folder of the
// repository.
func TestEveryProtectedPathIsInTheRepository(t *testing.T) {
	for _, p := range release.Protected {
		if strings.HasPrefix(p, "/") || strings.Contains(p, "..") {
			t.Fatalf("%q is not a path of the repository", p)
		}
		if _, err := os.Stat(filepath.Join("..", "..", strings.TrimSuffix(p, "/"))); err != nil {
			t.Errorf("the protected path %q: %v", p, err)
		}
	}
}

// Each protected part 16.8 names has its paths on the list, so a part of the design whose path was
// left off (round 1 and 2 of the reviews of M16) fails here, and so does a part the design no longer
// names in these words.
func TestEachProtectedPartOf168HasItsPaths(t *testing.T) {
	body, err := os.ReadFile(filepath.Join("..", "..", "docs", "plan", "tdd", "16-versions-self-update-and-rollback.md"))
	if err != nil {
		t.Fatal(err)
	}
	var row string
	for _, line := range strings.Split(string(body), "\n") {
		if strings.HasPrefix(line, "| Protected parts, which matter when") {
			row = line
		}
	}
	parts := map[string][]string{
		"The updater":                                      {"cmd/crew-updater/", "internal/updater/"},
		"the roll back path":                               {"internal/updater/", "deploy/"},
		"the credential broker":                            {"internal/credbroker/", "internal/credstore/"},
		"the rules of the git endpoint":                    {"internal/gitendpoint/"},
		"the rules of the MCP tokens":                      {"internal/mcp/tokens.go", "internal/mcp/mcp.go"},
		"the database container with its roles":            {"deploy/db/"},
		"every schema step and the code that applies them": {"internal/db/"},
		"the improver's own definition":                    {"improver.md", "improver.go", "internal/improve/"},
		"the ingress and the sign-in":                      {"ingress/", "internal/httpapi/server.go", "cmd/crew/"},
		"the code that chooses a sandbox's network mode and credentials": {"internal/sandboxes/", "internal/egress/", "internal/fence/",
			"internal/runs/roles.go", "internal/orchestrator/", "internal/config/", "cmd/crew/", "internal/githubapp/",
			"internal/projterm/", "internal/setup/"},
		"the image a run starts from": {"internal/runs/driver.go", "internal/runs/continue.go"},
		"the removal of secrets":      {"internal/redact/", "cmd/crew/"},
		"the code that holds the operator's logins or hands them on": {"internal/github/", "internal/setup/", "internal/logins/",
			"internal/cli/", "internal/notify/", "internal/mail/", "internal/homepath/"},
		"the secrets of hosted projects":                                    {"internal/hosting/"},
		"the gate and the build with their scripts":                         {"internal/versions/", "deploy/", "tools/", "Makefile"},
		"the Go code of their sandboxes":                                    {"internal/harness/"},
		"the settings of `crew-gate`":                                       {"internal/board/crewproject.go"},
		"the press of Run the live gate and the diff it shows":              {"internal/httpapi/versions.go", "web/src/lib/gate.ts", "web/src/components/gate.tsx"},
		"the list of what Crew depends on":                                  {"go.mod", "go.sum", "versions.lock", "image/", "web/package.json", "web/package-lock.json"},
		"the settings keys for approvals":                                   {"internal/settings/"},
		"the code that chooses a model and the numbers it chooses by":       {"internal/models/"},
		"the code that decides whether a release is protected":              {"internal/release/"},
		"the eval's reading of which runs measured their case":              {"internal/pipeline/evalend.go", "internal/pipeline/evals.go", "internal/measure/"},
		"the bound of a tree a push brings and a check lists":               {"internal/gitendpoint/", "internal/gitstore/listtree.go"},
		"the bound of every other read of git's output":                     {"internal/gitstore/readbound.go"},
		"the cuts of a history's fields":                                    {"internal/gitstore/history.go"},
		"the store's reading of an index file through the links on its way": {"internal/gitstore/indexfile.go"},
		"the code that keeps a check off the builder's family":              {"internal/pipeline/checkfamily.go", "internal/pipeline/atlimit.go"},
	}
	for part, paths := range parts {
		if !strings.Contains(row, part) {
			t.Errorf("16.8 does not name %q", part)
		}
		for _, p := range paths {
			if !strings.Contains(row, "`"+p+"`") {
				t.Errorf("%s: 16.8 does not name %s", part, p)
			}
			if !slices.Contains(release.Protected, p) {
				t.Errorf("%s: %s is not protected", part, p)
			}
		}
	}
	// Every directory 16.8's row names in backticks is read from the row itself and checked against
	// release.Protected, apart from the four the row explicitly says are not protected whole
	// (register row 76).
	notProtectedDirs := []string{"internal/pipeline/", "internal/gitstore/", "internal/board/", "cmd/crew-run/"}
	rest := row
	for {
		_, after, ok := strings.Cut(rest, "`")
		if !ok {
			break
		}
		tok, tail, ok := strings.Cut(after, "`")
		if !ok {
			break
		}
		rest = tail
		if strings.HasSuffix(tok, "/") && !slices.Contains(notProtectedDirs, tok) && !slices.Contains(release.Protected, tok) {
			t.Errorf("16.8 names directory %s, which is not in release.Protected", tok)
		}
	}
}

// Each path on the list is named in 16.8's row, so the design says which files and directories a
// part holds and a path protected beyond the design's words is found (security review of M16's
// close, finding 2; register row 76).
func TestEachProtectedFileIsNamedIn168(t *testing.T) {
	body, err := os.ReadFile(filepath.Join("..", "..", "docs", "plan", "tdd", "16-versions-self-update-and-rollback.md"))
	if err != nil {
		t.Fatal(err)
	}
	var row string
	for _, line := range strings.Split(string(body), "\n") {
		if strings.HasPrefix(line, "| Protected parts, which matter when") {
			row = line
		}
	}
	for _, p := range release.Protected {
		if !strings.Contains(row, "`"+p+"`") {
			t.Errorf("16.8 does not name %s", p)
		}
	}
}

// PostgreSQL's image is the same only by its tag and its id: a release that keeps the tag and brings
// another image under it changes code (security review of M16 P3).
func TestAnotherPostgresUnderTheSameTagIsCode(t *testing.T) {
	files := map[string]string{"baseline/stages/build.md": "a"}
	running := release.Manifest{Files: files, Images: map[string]release.Image{"postgres": {Tag: "postgres:18.6", ID: "sha256:a"}}}
	next := release.Manifest{Files: files, Images: map[string]release.Image{"postgres": {Tag: "postgres:18.6", ID: "sha256:b"}}}
	if k := release.Classify(running, next, 2); k.Kind != release.KindCode {
		t.Fatalf("%+v", k)
	}
	next.Images["postgres"] = release.Image{Tag: "postgres:18.6", ID: "sha256:a"}
	if k := release.Classify(running, next, 2); k.Kind != release.KindText {
		t.Fatalf("%+v", k)
	}
}
