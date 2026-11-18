package hosting

import (
	"context"
	"slices"
	"testing"

	"github.com/kesokaj/crew/internal/db/dbtest"
)

// The ingress is joined again to every hosted bridge that serves, at the server's start and each
// round: an install makes the ingress a new container, which Docker joins to no hosted bridge (plan
// M19 D29). A project that is stopped has no bridge to join.
func TestTheIngressIsJoinedToEveryServingBridge(t *testing.T) {
	ctx := context.Background()
	d := dbtest.New(t, false)
	rt := newFakeRuntime()
	s := &Service{Pool: d.Server, Runtime: rt}
	for i, row := range []struct{ name, state, sandbox string }{
		{"shop", "live", "host-a-1"}, {"club", "failing", "host-b-3"}, {"site", "stopped", ""},
	} {
		var p string
		if err := d.Server.QueryRow(ctx, `INSERT INTO projects (id, name, key, state, repo_source) VALUES (gen_random_uuid(), $1, $2, 'running', 'local') RETURNING id`,
			row.name, string(rune('A'+i))+"K").Scan(&p); err != nil {
			t.Fatal(err)
		}
		if _, err := d.Server.Exec(ctx, `INSERT INTO hosting (project, state, sandbox) VALUES ($1, $2, nullif($3, ''))`, p, row.state, row.sandbox); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.Rejoin(ctx); err != nil {
		t.Fatal(err)
	}
	slices.Sort(rt.joined)
	if !slices.Equal(rt.joined, []string{"host-a-1", "host-b-3"}) {
		t.Fatalf("joined: %v", rt.joined)
	}
}
