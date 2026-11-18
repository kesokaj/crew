package sandboxes_test

import (
	"context"
	"strings"
	"testing"

	"github.com/kesokaj/crew/internal/sandboxes"
)

// A sandbox of a project gets a GitHub token only for the repository of its own project (safety
// rule 7, 12.6, 13.11): Start refuses any repository that is not the project's GitHub remote, and
// starts no microVM.
func TestASandboxRefusesARepositoryThatIsNotItsProjectsRemote(t *testing.T) {
	ctx := context.Background()
	m, rt := manager(t)
	const shop, local = "00000000-0000-4000-8000-0000000000d1", "00000000-0000-4000-8000-0000000000d2"
	for _, q := range []string{
		`INSERT INTO projects (id, name, key, state, repo_source, remote) VALUES ('` + shop + `', 'shop', 'SF', 'running', 'github', 'git@github.com:kesokaj/shop.git')`,
		`INSERT INTO projects (id, name, key, state, repo_source) VALUES ('` + local + `', 'local', 'LC', 'running', 'local')`,
	} {
		if _, err := m.Pool.Exec(ctx, q); err != nil {
			t.Fatal(err)
		}
	}
	for _, tc := range []struct {
		name    string
		project string
		repos   []string
	}{
		{"another repository", shop, []string{"kesokaj/other"}},
		{"its own and another", shop, []string{"kesokaj/shop", "kesokaj/other"}},
		{"a project with no remote", local, []string{"kesokaj/local"}},
	} {
		if _, _, err := m.Start(ctx, sandboxes.Spec{Kind: sandboxes.KindTerminal, Project: tc.project, Repos: tc.repos}); err == nil ||
			!strings.Contains(err.Error(), "is not this project's remote") {
			t.Fatalf("%s: %v", tc.name, err)
		}
	}
	if len(rt.specs) != 0 {
		t.Fatalf("a refused start started %d microVMs", len(rt.specs))
	}
	row, _, err := m.Start(ctx, sandboxes.Spec{Kind: sandboxes.KindTerminal, Project: shop, Repos: []string{"kesokaj/shop"}})
	if err != nil || len(row.Repos) != 1 || row.Repos[0] != "kesokaj/shop" {
		t.Fatalf("its own remote: %+v, %v", row, err)
	}
}
