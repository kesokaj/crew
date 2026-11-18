package harness

import "testing"

// The improver's sandbox has no repository and no credential but its CLI's login (10.4).
func TestTheImproversSpec(t *testing.T) {
	ok := RunSpec{Run: "r", Prompt: "p", Improver: true, MCP: "/mcp"}
	if err := checkSpec(ok); err != nil {
		t.Fatal(err)
	}
	for name, s := range map[string]RunSpec{
		"a repository": {Run: "r", Prompt: "p", Improver: true, Repo: "shop", Branch: "main"},
		"GitHub":       {Run: "r", Prompt: "p", Improver: true, Repos: []string{"o/r"}},
		"a judge":      {Run: "r", Prompt: "p", Improver: true, Judge: true},
	} {
		if checkSpec(s) == nil {
			t.Errorf("an improver with %s", name)
		}
	}
}
