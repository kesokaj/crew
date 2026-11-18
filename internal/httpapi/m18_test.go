package httpapi_test

import (
	"context"
	"encoding/json"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/kesokaj/crew/internal/board"
	"github.com/kesokaj/crew/internal/httpapi"
)

type itemAnswer struct {
	Item      *inboxItem `json:"item"`
	Decisions []struct {
		Label string    `json:"label"`
		By    string    `json:"by"`
		At    time.Time `json:"at"`
	} `json:"decisions"`
	Notified []struct {
		Kind   string    `json:"kind"`
		SentAt time.Time `json:"sentAt"`
	} `json:"notified"`
}

// A link chooses, Confirm decides (18.3, D11, D18): the item a link names is read with what was
// decided about it; Confirm serves the alternative's action with the request's own session and
// records the press the action took; an item that no longer waits, a label it does not have, and a
// link's alternative decide nothing; a GET never decides; a fresh route stays fresh through it.
func TestALinkChoosesAndConfirmDecides(t *testing.T) {
	t.Parallel()
	k := newTicketKit(t)
	ctx := context.Background()
	// The project waits for its OK: its breakdown is written and its ticket is in Planning.
	k.call("POST", "/api/projects", `{"name": "shop", "key": "SF", "plan": "## Goal\n\nSell.\n", "repository": {"kind": "local"}}`, 201, nil)
	k.call("POST", "/api/projects/shop/tasks", `{"title": "One", "why": "w", "what": "w", "goal": "g", "checks": [{"kind": "cmd", "spec": "test -f 1.txt"}], "docsReason": "none"}`, 201, nil)
	if _, err := k.d.Server.Exec(ctx, `INSERT INTO plans (project, kind, number, text, created_by) SELECT id, 'breakdown', 1, 'b', 'lead' FROM projects WHERE name = 'shop'`); err != nil {
		t.Fatal(err)
	}
	item := func(id string) itemAnswer {
		t.Helper()
		var a itemAnswer
		k.call("GET", "/api/inbox/item?id="+id, "", 200, &a)
		return a
	}
	a := item("ok%2Fshop")
	if a.Item == nil || a.Item.ID != "ok/shop" || len(a.Decisions) != 0 {
		t.Fatalf("the item before: %+v", a)
	}
	// What was mailed of its open episode shows with it, and in the inbox.
	if _, err := k.d.Server.Exec(ctx, `INSERT INTO notifications (kind, item, subject, body, state, sent_at) VALUES ('needs_you', 'ok/shop', 's', 'b', 'sent', now())`); err != nil {
		t.Fatal(err)
	}
	if a := item("ok%2Fshop"); len(a.Notified) != 1 || a.Notified[0].Kind != "needs_you" {
		t.Fatalf("notified: %+v", a.Notified)
	}
	var in struct {
		Items []struct {
			ID       string
			Notified []struct{ Kind string }
		}
	}
	k.call("GET", "/api/inbox", "", 200, &in)
	if idx := slices.IndexFunc(in.Items, func(it struct {
		ID       string
		Notified []struct{ Kind string }
	}) bool {
		return it.ID == "ok/shop"
	}); idx < 0 || len(in.Items[idx].Notified) != 1 {
		t.Fatalf("the inbox: %+v", in)
	}
	decide := func(h map[string]string, item, choose string) (int, string) {
		b, _ := json.Marshal(map[string]string{"item": item, "choose": choose})
		return do(t, k.srv, "POST", "/api/inbox/decision", h, string(b))
	}
	state := func() string {
		var s string
		k.d.Server.QueryRow(ctx, `SELECT state FROM projects WHERE name = 'shop'`).Scan(&s)
		return s
	}
	decisions := func() int {
		var n int
		k.d.Server.QueryRow(ctx, `SELECT count(*) FROM inbox_decisions`).Scan(&n)
		return n
	}
	for _, c := range []struct{ choose string }{{"Read the plan"}, {"Nope"}} {
		if code, body := decide(change, "ok/shop", c.choose); code != 400 {
			t.Fatalf("%s: %d %s", c.choose, code, body)
		}
	}
	if code, body := decide(with(map[string]string{"Origin": "https://evil.example"}), "ok/shop", "Give the OK"); code != 403 {
		t.Fatalf("another site: %d %s", code, body)
	}
	// A mail's link is a GET of the page and of the item: nothing decides.
	for _, p := range []string{"/inbox?item=ok%2Fshop&choose=Give+the+OK", "/api/inbox/item?id=ok%2Fshop&choose=Give+the+OK"} {
		do(t, k.srv, "GET", p, signedIn, "")
	}
	if state() != "planning" || decisions() != 0 {
		t.Fatalf("decided before Confirm: %s, %d", state(), decisions())
	}
	if code, body := decide(change, "ok/shop", "Give the OK"); code != 200 || !strings.Contains(body, `"state":"running"`) {
		t.Fatalf("Confirm: %d %s", code, body)
	}
	if state() != "running" || decisions() != 1 {
		t.Fatalf("after Confirm: %s, %d", state(), decisions())
	}
	if code, body := decide(change, "ok/shop", "Give the OK"); code != 409 || !strings.Contains(body, "no longer waits") {
		t.Fatalf("again: %d %s", code, body)
	}
	a = item("ok%2Fshop")
	if a.Item != nil || len(a.Decisions) != 1 || a.Decisions[0].Label != "Give the OK" || a.Decisions[0].By != admin {
		t.Fatalf("the old link: %+v", a)
	}

	// A new feature: Hold and then Approve now are two presses, both recorded, the newest first.
	p, err := k.b.Project(ctx, "shop")
	if err != nil {
		t.Fatal(err)
	}
	tk, err := k.b.AddTicket(ctx, "shop", board.NewTicket{Title: "Export", Why: "w", What: "w", Goal: "g",
		Checks: []board.CheckSpec{{Kind: "cmd", Spec: "test -f x.txt"}}}, board.OriginAgent, "builder · 00000000-0000-4000-8000-0000000000d1")
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 5 && func() bool {
		var s string
		k.d.Server.QueryRow(ctx, `SELECT stage FROM tickets WHERE number = 2`).Scan(&s)
		return s != "analysis"
	}(); i++ {
		k.b.Reconcile(ctx, "shop")
	}
	if _, err := k.b.Decide(ctx, p, tk.Key, board.Decision{Choice: "notice", Reason: "The plan has no export."}, "lead · r_1"); err != nil {
		t.Fatal(err)
	}
	feature := "feature/shop/" + tk.Key
	if code, body := decide(change, feature, "Hold"); code != 200 {
		t.Fatalf("Hold: %d %s", code, body)
	}
	if code, body := decide(change, feature, "Approve now"); code != 200 {
		t.Fatalf("Approve now: %d %s", code, body)
	}
	a = item(strings.ReplaceAll(feature, "/", "%2F"))
	if a.Item != nil || len(a.Decisions) != 2 || a.Decisions[0].Label != "Approve now" || a.Decisions[1].Label != "Hold" {
		t.Fatalf("the feature's presses: %+v", a)
	}

	// Agree, whose handler asks for the fresh sign-in itself (since 0.71), asks for it through Confirm.
	rules, err := k.b.AddTicket(ctx, "shop", board.NewTicket{Title: "Rules", Why: "w", What: "w", Goal: "g",
		Checks: []board.CheckSpec{{Kind: "cmd", Spec: "test -f x.txt"}}}, board.OriginAgent, "builder · 00000000-0000-4000-8000-0000000000d2")
	if err != nil {
		t.Fatal(err)
	}
	// Analysis has room once the plan's own ticket went on.
	k.b.Decide(ctx, p, "SF-1", board.Decision{Choice: "approve"}, "lead · r_1")
	for i := 0; i < 5; i++ {
		k.b.Reconcile(ctx, "shop")
	}
	if _, err := k.b.Decide(ctx, p, rules.Key, board.Decision{Choice: "approve", MayChange: []string{"AGENTS.md"}}, "lead · r_1"); err != nil {
		t.Fatal(err)
	}
	k.b.Reconcile(ctx, "shop")
	agree := "maychange/shop/" + rules.Key
	if code, body := decide(signedInAt(stale, ""), agree, "Agree"); code != 401 || reason(body) != httpapi.ReasonFresh {
		t.Fatalf("Agree, stale: %d %s", code, body)
	}
	if code, body := decide(signedInAt(time.Minute, ""), agree, "Agree"); code != 200 {
		t.Fatalf("Agree, fresh: %d %s", code, body)
	}

	// A fresh route stays fresh through Confirm: the boot hold's Resume (20.3).
	if _, err := k.d.Server.Exec(ctx, `UPDATE crew_state SET boot_failures = 2, boot_failed = 'kata: no /dev/kvm', boot_failed_at = now() WHERE id = 1`); err != nil {
		t.Fatal(err)
	}
	before := decisions()
	if code, body := decide(signedInAt(stale, ""), "boot", "Resume"); code != 401 || reason(body) != httpapi.ReasonFresh {
		t.Fatalf("Resume, stale: %d %s", code, body)
	}
	if decisions() != before {
		t.Fatal("a refused press was recorded")
	}
	if code, body := decide(signedInAt(time.Minute, ""), "boot", "Resume"); code != 200 {
		t.Fatalf("Resume, fresh: %d %s", code, body)
	}
	if decisions() != before+1 {
		t.Fatal("the press was not recorded")
	}
}

// ReadNews is Activity's news lines after a time, oldest first, of every project and the Crew level
// (D27): a busy day of moves does not crowd them out, as the one cut of Activity's own answer does.
func TestReadNewsKeepsEveryLineOfNews(t *testing.T) {
	t.Parallel()
	bs := newBoardServer(t)
	ctx := context.Background()
	bs.call(t, "POST", "/api/projects", `{"name": "shop", "key": "SF", "plan": "x", "repository": {"kind": "local"}}`, 201, nil)
	bs.call(t, "POST", "/api/projects/shop/tasks", `{"title": "A ticket", "why": "w", "what": "w", "goal": "g"}`, 201, nil)
	pool := bs.b.Pool
	after := time.Now().Add(-3 * time.Hour)
	for i, at := range []time.Duration{-4 * time.Hour, -2 * time.Hour, -90 * time.Minute, -time.Hour} {
		if _, err := pool.Exec(ctx, `INSERT INTO machine_news (kind, key, text, at) VALUES ('certificate', $1, $2, now() + make_interval(secs => $3))`,
			"k"+string(rune('a'+i)), "news "+string(rune('a'+i)), at.Seconds()); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := pool.Exec(ctx, `INSERT INTO stage_moves (ticket, from_stage, to_stage, actor, at)
		SELECT t.id, 'backlog', 'analysis', 'board', now() - interval '30 minutes' + g * interval '1 second' FROM tickets t, generate_series(1, 300) g`); err != nil {
		t.Fatal(err)
	}
	lines, err := httpapi.ReadNews(ctx, pool, after)
	if err != nil {
		t.Fatal(err)
	}
	var texts []string
	for _, l := range lines {
		if l.Kind != "news" {
			t.Fatalf("a line of %s", l.Kind)
		}
		texts = append(texts, l.Text)
	}
	if strings.Join(texts, ", ") != "news b, news c, news d" {
		t.Fatalf("the news: %v", texts)
	}
	// Activity's own answer is still its newest 200 lines, where the moves crowd the news out.
	var a struct{ Lines []httpapi.ActivityLine }
	bs.call(t, "GET", "/api/activity", "", 200, &a)
	if len(a.Lines) != 200 {
		t.Fatalf("Activity: %d lines", len(a.Lines))
	}
	for _, l := range a.Lines {
		if l.Kind == "news" {
			t.Fatalf("Activity kept a line of news under 300 newer moves: %+v", l)
		}
	}
}
