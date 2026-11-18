package planning

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/kesokaj/crew/internal/settings"
)

// The one lead queue (7.9, R53): by class, the operator at the keyboard first in a class, then the
// operator's priority across projects, then the project served least recently, then the time waited.
// A free slot of agy goes to the first ask.
func TestTheLeadQueueGoesByClassThenPriority(t *testing.T) {
	pl, _, d := newPlanner(t, settings.Doc{"lead_slots": settings.Doc{"agy": 1}})
	ctx := context.Background()
	doc, _ := pl.Settings(ctx)
	project := func(id, name, key string) string {
		if _, err := d.Server.Exec(ctx, `INSERT INTO projects (id, name, key, state, repo_source) VALUES ($1, $2, $3, 'running', 'local')`,
			id, name, key); err != nil {
			t.Fatal(err)
		}
		return id
	}
	served, idle := project("00000000-0000-4000-8000-0000000000a1", "served", "SV"), project("00000000-0000-4000-8000-0000000000a2", "idle", "ID")
	// The project "served" had a run a minute ago; "idle" never had one.
	var sub int64
	d.Server.QueryRow(ctx, `INSERT INTO subtasks (scope, project, number, kind, role) VALUES ('project', $1, 1, 'run', 'lead') RETURNING id`, served).Scan(&sub)
	d.Server.Exec(ctx, `INSERT INTO conversations (id, purpose, cli) VALUES ('00000000-0000-4000-8000-000000000001', 'run', 'agy')`)
	d.Server.Exec(ctx, `INSERT INTO runs (id, subtask, number, state, cli, crew_version, image, conversation, started_at)
		VALUES ('00000000-0000-4000-8000-000000000001', $1, 1, 'ended', 'agy', 't', 't', '00000000-0000-4000-8000-000000000001', now() - interval '1 minute')`, sub)

	ask := func(a LeadAsk) bool {
		t.Helper()
		a.Owner = "test"
		ok, err := TakeSlot(ctx, d.Server, doc, a)
		if err != nil {
			t.Fatal(err)
		}
		return ok
	}
	asks := []LeadAsk{
		{Key: "a-low", Class: ClassAnalysis, Priority: 3, Own: 3, Project: idle, CLI: "agy", What: "low"},
		{Key: "a-urgent-served", Class: ClassAnalysis, Priority: 0, Own: 0, Project: served, CLI: "agy", What: "urgent, served"},
		{Key: "a-urgent-idle", Class: ClassAnalysis, Priority: 0, Own: 0, Project: idle, CLI: "agy", What: "urgent, idle"},
		{Key: "breakdown", Class: ClassOperatorWaiting, Priority: NormalPriority, Own: NormalPriority, Project: idle, CLI: "agy", What: "breakdown"},
		{Key: "draft", Class: ClassOperatorWaiting, Keyboard: true, Priority: NormalPriority, Own: NormalPriority, CLI: "agy", What: "draft"},
	}
	for _, a := range asks {
		ask(a)
		time.Sleep(2 * time.Millisecond)
	}
	q, err := Queue(ctx, d.Server, doc)
	if err != nil {
		t.Fatal(err)
	}
	var keys []string
	for _, w := range q {
		keys = append(keys, w.Key)
	}
	want := []string{"draft", "breakdown", "a-urgent-idle", "a-urgent-served", "a-low"}
	if len(keys) != len(want) {
		t.Fatalf("queue %v", keys)
	}
	for i := range want {
		if keys[i] != want[i] {
			t.Fatalf("queue %v, want %v", keys, want)
		}
	}
	// agy's one slot goes to the draft, and to no other ask.
	if !ask(asks[4]) || ask(asks[3]) || ask(asks[2]) || ask(asks[0]) {
		t.Fatal("the slot did not go to the first ask alone")
	}
	// When the draft started, the breakdown is next.
	Taken(ctx, d.Server, "draft")
	if !ask(asks[3]) || ask(asks[2]) {
		t.Fatal("the next ask did not get the slot")
	}
	// An ask a pass did not make again no longer waits.
	pass := time.Now()
	ask(asks[2])
	if err := Sweep(ctx, d.Server, "test", pass); err != nil {
		t.Fatal(err)
	}
	if q, _ = Queue(ctx, d.Server, doc); len(q) != 1 || q[0].Key != "a-urgent-idle" {
		t.Fatalf("after the sweep: %+v", q)
	}
}

// Across projects the queue compares only the priorities the operator set; an agent's priority
// orders the asks of its own project only (7.9, from 0.42), so a lead cannot put its project first
// by marking its own tickets urgent.
func TestAnAgentsPriorityOrdersOnlyItsOwnProject(t *testing.T) {
	pl, _, d := newPlanner(t, settings.Doc{"lead_slots": settings.Doc{"agy": 1}})
	ctx := context.Background()
	doc, _ := pl.Settings(ctx)
	ids := map[string]string{}
	for i, name := range []string{"loud", "quiet", "late"} {
		id := fmt.Sprintf("00000000-0000-4000-8000-0000000000b%d", i)
		if _, err := d.Server.Exec(ctx, `INSERT INTO projects (id, name, key, state, repo_source) VALUES ($1, $2, $3, 'running', 'local')`,
			id, name, strings.ToUpper(name[:2])); err != nil {
			t.Fatal(err)
		}
		ids[name] = id
	}
	// "loud" and "late" were served a minute ago, "quiet" never.
	for i, name := range []string{"loud", "late"} {
		var sub int64
		d.Server.QueryRow(ctx, `INSERT INTO subtasks (scope, project, number, kind, role) VALUES ('project', $1, 1, 'run', 'lead') RETURNING id`, ids[name]).Scan(&sub)
		conv := fmt.Sprintf("00000000-0000-4000-8000-00000000000%d", i+1)
		d.Server.Exec(ctx, `INSERT INTO conversations (id, purpose, cli) VALUES ($1, 'run', 'agy')`, conv)
		if _, err := d.Server.Exec(ctx, `INSERT INTO runs (id, subtask, number, state, cli, crew_version, image, conversation, started_at)
			VALUES ($1, $2, 1, 'ended', 'agy', 't', 't', $1, now() - interval '1 minute')`, conv, sub); err != nil {
			t.Fatal(err)
		}
	}
	for _, a := range []LeadAsk{
		// The lead of "loud" marked its tickets urgent; the operator set no priority there.
		{Key: "loud-urgent", Class: ClassAnalysis, Priority: NormalPriority, Own: 0, Project: ids["loud"], CLI: "agy", What: "loud"},
		{Key: "quiet-low", Class: ClassAnalysis, Priority: NormalPriority, Own: 3, Project: ids["quiet"], CLI: "agy", What: "quiet, low"},
		{Key: "quiet-high", Class: ClassAnalysis, Priority: NormalPriority, Own: 1, Project: ids["quiet"], CLI: "agy", What: "quiet, high"},
		// The operator marked a ticket of "late" urgent: that goes first everywhere.
		{Key: "late-operator", Class: ClassAnalysis, Priority: 0, Own: 0, Project: ids["late"], CLI: "agy", What: "late"},
	} {
		a.Owner = "test"
		if _, err := TakeSlot(ctx, d.Server, doc, a); err != nil {
			t.Fatal(err)
		}
		time.Sleep(2 * time.Millisecond)
	}
	q, err := Queue(ctx, d.Server, doc)
	if err != nil {
		t.Fatal(err)
	}
	var keys []string
	for _, w := range q {
		keys = append(keys, w.Key)
	}
	if want := "late-operator quiet-high quiet-low loud-urgent"; strings.Join(keys, " ") != want {
		t.Fatalf("queue %v, want %s", keys, want)
	}
}

// Close to a limit, the quota guard halves a CLI's lead slots, never below one (12.7).
func TestTheGuardLowersTheLeadSlotsNearALimit(t *testing.T) {
	pl, _, d := newPlanner(t, settings.Doc{"lead_slots": settings.Doc{"agy": 4}})
	ctx := context.Background()
	doc, _ := pl.Settings(ctx)
	one := settings.Merge(doc, settings.Doc{"lead_slots": settings.Doc{"agy": 1}})
	if n, _ := Slots(ctx, d.Server, doc); n["agy"] != 4 {
		t.Fatalf("%v", n)
	}
	if n, _ := Slots(ctx, d.Server, one); n["agy"] != 1 {
		t.Fatalf("%v", n)
	}
	reset := time.Now().Add(time.Hour)
	if _, err := d.Server.Exec(ctx, `INSERT INTO quota (cli, grp, win, used, resets_at, source) VALUES ('agy', 'gemini', '5h', 0.95, $1, 'run')`, reset); err != nil {
		t.Fatal(err)
	}
	if n, _ := Slots(ctx, d.Server, doc); n["agy"] != 2 {
		t.Fatalf("near the limit: %v", n)
	}
	if n, _ := Slots(ctx, d.Server, one); n["agy"] != 1 {
		t.Fatalf("near the limit, one slot: %v", n)
	}
}

func TestUnifiedRunQueueOrdersContinuesStageRightAndReservesInteractive(t *testing.T) {
	pl, _, d := newPlanner(t, settings.Doc{"max_concurrent_runs": 1, "conversations": settings.Doc{"max_sandboxes": 2}})
	ctx := context.Background()
	doc, _ := pl.Settings(ctx)
	projID := "00000000-0000-4000-8000-0000000000c1"
	if _, err := d.Server.Exec(ctx, `INSERT INTO projects (id, name, key, state, repo_source) VALUES ($1, 'shop', 'SF', 'running', 'local')`, projID); err != nil {
		t.Fatal(err)
	}

	// 1. Insert one working ticket run so background free slots = 0 (1 of 1 used by a ticket run).
	var subTicket int64
	if err := d.Server.QueryRow(ctx, `INSERT INTO subtasks (scope, project, number, kind, role) VALUES ('ticket', $1, 1, 'build', 'builder') RETURNING id`, projID).Scan(&subTicket); err != nil {
		t.Fatal(err)
	}
	if _, err := d.Server.Exec(ctx, `INSERT INTO runs (id, subtask, number, state, cli, crew_version, image) VALUES (gen_random_uuid(), $1, 1, 'working', 'agy', 'v', 'img')`, subTicket); err != nil {
		t.Fatal(err)
	}
	if free, err := FreeSlots(ctx, d.Server, doc); err != nil || free["agy"] != 0 {
		t.Fatalf("expected 0 free background slots while ticket builder is working, got %v (err %v)", free, err)
	}

	// 2. Enqueue background asks across stages, continues, and maintenance classes.
	asks := []LeadAsk{
		{Key: "improver", Owner: "test", Class: ClassImprovement, Priority: NormalPriority, Own: NormalPriority, CLI: "agy", What: "improver"},
		{Key: "eval", Owner: "test", Class: ClassEvals, Priority: NormalPriority, Own: NormalPriority, Project: projID, CLI: "agy", What: "eval"},
		{Key: "retro", Owner: "test", Class: ClassRetrospective, Priority: NormalPriority, Own: NormalPriority, Project: projID, CLI: "agy", What: "retro"},
		{Key: "house", Owner: "test", Class: ClassHousekeeping, Priority: NormalPriority, Own: NormalPriority, Project: projID, CLI: "agy", What: "house"},
		{Key: "report", Owner: "test", Class: ClassFinalReport, Priority: NormalPriority, Own: NormalPriority, Project: projID, CLI: "agy", What: "report"},
		{Key: "analysis-1", Owner: "test", Class: ClassAnalysis, StageRank: 2, Priority: NormalPriority, Own: NormalPriority, Project: projID, CLI: "agy", What: "analyse"},
		{Key: "build-new", Owner: "test", Class: ClassTicket, StageRank: 3, Priority: NormalPriority, Own: NormalPriority, Project: projID, CLI: "agy", What: "build"},
		{Key: "review-new", Owner: "test", Class: ClassTicket, StageRank: 4, Priority: NormalPriority, Own: NormalPriority, Project: projID, CLI: "agy", What: "review"},
		{Key: "verify-new", Owner: "test", Class: ClassTicket, StageRank: 7, Priority: NormalPriority, Own: NormalPriority, Project: projID, CLI: "agy", What: "verify"},
		{Key: "build-resume", Owner: "test", Class: ClassTicket, Continues: true, StageRank: 3, Priority: NormalPriority, Own: NormalPriority, Project: projID, CLI: "agy", What: "resume build"},
	}
	for _, a := range asks {
		ok, err := TakeSlot(ctx, d.Server, doc, a)
		if err != nil || ok {
			t.Fatalf("TakeSlot(%s) = %v, %v; want false, nil while slot is occupied", a.Key, ok, err)
		}
		time.Sleep(2 * time.Millisecond)
	}

	q, err := Queue(ctx, d.Server, doc)
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, w := range q {
		got = append(got, w.Key)
	}
	want := "build-resume verify-new review-new build-new analysis-1 report house retro eval improver"
	if strings.Join(got, " ") != want {
		t.Fatalf("queue order = %q, want %q", strings.Join(got, " "), want)
	}

	// 3. Interactive reservation: an operator_waiting Keyboard turn bypasses ticket contention immediately!
	ok, err := TakeSlot(ctx, d.Server, doc, LeadAsk{
		Key: "keyboard-chat", Owner: "test", Class: ClassOperatorWaiting, Keyboard: true,
		Priority: NormalPriority, Own: NormalPriority, Project: projID, CLI: "agy", What: "ticket chat",
	})
	if err != nil || !ok {
		t.Fatalf("expected interactive Keyboard turn to bypass ticket contention, got ok=%v err=%v", ok, err)
	}
	if err := Taken(ctx, d.Server, "keyboard-chat"); err != nil {
		t.Fatal(err)
	}

	// 4. When the working ticket run transitions to 'idle' (waiting on a check sandbox), it releases its slot
	// and the highest-priority background waiter ("build-resume") gets the freed slot.
	if _, err := d.Server.Exec(ctx, `UPDATE runs SET state = 'idle' WHERE subtask = $1`, subTicket); err != nil {
		t.Fatal(err)
	}
	if free, err := FreeSlots(ctx, d.Server, doc); err != nil || free["agy"] != 1 {
		t.Fatalf("expected idle run waiting on checks to release its slot (free=1), got %v (err %v)", free, err)
	}
	if ok, err := TakeSlot(ctx, d.Server, doc, asks[len(asks)-1]); err != nil || !ok {
		t.Fatalf("expected build-resume (continues=true) to get the freed slot first, got ok=%v err=%v", ok, err)
	}
}
