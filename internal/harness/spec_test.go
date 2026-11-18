package harness

import (
	"reflect"
	"testing"

	"github.com/kesokaj/crew/internal/runspec"
)

func TestARunHasARepositoryWithItsBranchOrNeither(t *testing.T) {
	for _, c := range []struct {
		spec RunSpec
		ok   bool
	}{
		{RunSpec{Run: "r", Prompt: "p", Repo: "shop", Branch: "subtask/1"}, true},
		{RunSpec{Run: "r", Prompt: "p"}, true}, // a plan draft's run
		{RunSpec{Run: "r", Prompt: "p", Repo: "shop"}, false},
		{RunSpec{Run: "r", Prompt: "p", Branch: "subtask/1"}, false},
		{RunSpec{Prompt: "p"}, false},
		{RunSpec{Run: "r"}, false},
	} {
		if err := checkSpec(c.spec); (err == nil) != c.ok {
			t.Errorf("%+v: %v", c.spec, err)
		}
	}
}

func TestARunWithNoRepositoryHasNoGit(t *testing.T) {
	if g := RunGit("r1", "", "", ""); !reflect.DeepEqual(g, runspec.Git{}) {
		t.Fatalf("no repository: %+v", g)
	}
	g := RunGit("r1", "shop", "subtask/1", "refs/heads/main")
	if g.Path != "/git/shop.git" || g.Branch != "subtask/1" || g.Base != "refs/heads/main" || g.WIP != "refs/crew/wip/r1" {
		t.Fatalf("a repository: %+v", g)
	}
}
