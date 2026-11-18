package planning

import (
	"context"
	"testing"

	"github.com/kesokaj/crew/internal/db/dbtest"
	"github.com/kesokaj/crew/internal/settings"
)

// Every working agent run (conversation, ticket chat, or stage run) takes a concurrent-run slot of its
// CLI while it works (7.9): FreeSlots counts working runs across all roles, and not an idle run that
// waits for the next message or a check sandbox.
func TestTheAssistantsTurnTakesALeadSlot(t *testing.T) {
	d := dbtest.New(t, false)
	ctx := context.Background()
	doc := settings.Doc{"max_concurrent_runs": 3}
	for _, q := range []string{
		`INSERT INTO subtasks (id, scope, number, kind, role) VALUES (901, 'conversation', 1, 'talk', 'assistant'),
			(902, 'conversation', 1, 'talk', 'assistant'), (903, 'project', 1, 'run', 'builder')`,
		`INSERT INTO runs (id, subtask, number, state, cli, crew_version, image) VALUES
			(gen_random_uuid(), 901, 1, 'working', 'agy', 'v', 'img'), (gen_random_uuid(), 902, 1, 'idle', 'agy', 'v', 'img'),
			(gen_random_uuid(), 903, 1, 'working', 'agy', 'v', 'img')`,
	} {
		if _, err := d.Server.Exec(ctx, q); err != nil {
			t.Fatal(err)
		}
	}
	free, err := FreeSlots(ctx, d.Server, doc)
	if err != nil {
		t.Fatal(err)
	}
	if free["agy"] != 1 {
		t.Fatalf("free: %v, want 1 of 3: the working assistant and working builder each take one, while the idle assistant takes none", free)
	}
}
