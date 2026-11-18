package httpapi_test

import (
	"context"
	"strings"
	"testing"
)

// A project's Protected switch (PUT /api/projects/{p}/protected, design 1.34, 7.13, 16.2) is off by
// default, blocks both Archive and Delete while on, shows on the overview and board responses, and is
// permanently locked on for Crew's own project (while makeGate archives the M4 fixture project so it
// leaves the lists).
func TestProjectProtectedBlocksArchiveAndDeleteAndLocksCrewsProject(t *testing.T) {
	t.Parallel()
	k := newTicketKit(t)
	ctx := context.Background()
	k.crewSource()
	k.setMachine(200, func(doc map[string]any) { doc["updates"].(map[string]any)["source_repo"] = "kesokaj/crew" })

	var crew struct {
		Name      string
		Protected bool
	}
	k.call("POST", "/api/crew/project", "", 201, &crew)
	if crew.Name != "crew" || !crew.Protected {
		t.Fatalf("Crew's project: %+v", crew)
	}
	// makeGate archived the M4 fixture project so it leaves /api/overview.
	if n := k.count(`SELECT count(*) FROM projects WHERE id = 'f1c50000-0000-4000-8000-000000000001' AND state = 'archived'`); n != 1 {
		t.Fatal("makeGate did not archive the fixture project")
	}

	bad := func(method, path, body string, code int) string {
		t.Helper()
		var e struct {
			Error struct {
				Message string
			}
		}
		k.call(method, path, body, code, &e)
		return e.Error.Message
	}

	// Crew's project cannot be unprotected or deleted.
	if msg := bad("PUT", "/api/projects/crew/protected", `{"protected": false}`, 400); !strings.Contains(msg, "always protected") {
		t.Fatalf("unprotecting crew: %s", msg)
	}
	k.call("PUT", "/api/projects/crew/protected", `{"protected": true}`, 200, nil)
	k.call("PUT", "/api/projects/crew/protected", `{bad`, 400, nil)
	k.call("PUT", "/api/projects/no-such-project/protected", `{"protected": true}`, 404, nil)
	if msg := bad("DELETE", "/api/projects/crew", `{"name": "crew"}`, 400); !strings.Contains(msg, "Crew's project") {
		t.Fatalf("deleting crew: %s", msg)
	}

	// Another project starts with Protected off, can switch it on and off, and refuses Archive and
	// Delete while Protected is on.
	k.call("POST", "/api/projects", `{"name": "shop", "key": "SF", "plan": "## Goal\n\nSell.\n", "repository": {"kind": "local"}}`, 201, nil)
	var shop struct {
		Protected bool
	}
	k.call("PUT", "/api/projects/shop/protected", `{"protected": true}`, 200, &shop)
	if !shop.Protected {
		t.Fatal("shop was not marked protected")
	}
	if msg := bad("POST", "/api/projects/shop/archive", `{}`, 400); !strings.Contains(msg, "is protected") {
		t.Fatalf("archiving protected shop: %s", msg)
	}
	if msg := bad("DELETE", "/api/projects/shop", `{"name": "shop"}`, 400); !strings.Contains(msg, "is protected") {
		t.Fatalf("deleting protected shop: %s", msg)
	}

	var ov struct {
		Projects []struct {
			Name      string
			Protected bool
			Locked    bool
		}
	}
	k.call("GET", "/api/overview", "", 200, &ov)
	found := map[string]struct{ prot, locked bool }{}
	for _, p := range ov.Projects {
		found[p.Name] = struct{ prot, locked bool }{p.Protected, p.Locked}
	}
	if _, hasFixture := found["fixture"]; hasFixture {
		t.Fatal("archived fixture still appears in /api/overview")
	}
	if !found["crew"].prot || !found["crew"].locked || !found["shop"].prot || found["shop"].locked {
		t.Fatalf("overview flags: %+v", found)
	}

	var crewOv struct {
		Project struct{ Protected bool }
		Locked  bool
	}
	k.call("GET", "/api/projects/crew/overview", "", 200, &crewOv)
	if !crewOv.Project.Protected || !crewOv.Locked {
		t.Fatalf("crew overview: %+v", crewOv)
	}
	var shopBoard struct {
		Project struct{ Protected bool }
	}
	k.call("GET", "/api/projects/shop/board", "", 200, &shopBoard)
	if !shopBoard.Project.Protected {
		t.Fatalf("shop board: %+v", shopBoard)
	}

	// Switch Protected off on shop: Archive and Delete now succeed.
	k.call("PUT", "/api/projects/shop/protected", `{"protected": false}`, 200, &shop)
	if shop.Protected {
		t.Fatal("shop stayed protected")
	}
	k.call("DELETE", "/api/projects/shop", `{"name": "shop"}`, 200, nil)
	_ = ctx
}
