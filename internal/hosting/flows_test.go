package hosting

import (
	"context"
	"os"
	"regexp"
	"slices"
	"testing"
	"time"

	"github.com/kesokaj/crew/baseline"
	"github.com/kesokaj/crew/internal/board"
)

const head2 = "2222222222222222222222222222222222222222"
const head3 = "3333333333333333333333333333333333333333"

// done records a ticket of the project that reached Done at a time.
func (h *hosted) done(number int, at time.Time) {
	h.t.Helper()
	var id int64
	if err := h.db.Server.QueryRow(h.ctx, `INSERT INTO tickets (project, number, title, kind, stage, origin, created_by, position)
		VALUES ($1, $2, 'T', 'fix', 'done', 'operator', 'operator', $3) RETURNING id`, h.id, number, number).Scan(&id); err != nil {
		h.t.Fatal(err)
	}
	if _, err := h.db.Server.Exec(h.ctx, `INSERT INTO stage_moves (ticket, from_stage, to_stage, actor, at) VALUES ($1, 'verification', 'done', 'backend', $2)`, id, at); err != nil {
		h.t.Fatal(err)
	}
}

func (h *hosted) setHead(c string) {
	h.git.mu.Lock()
	h.git.head = c
	h.git.mu.Unlock()
}

func (h *hosted) follow() {
	h.t.Helper()
	if err := h.s.FollowRound(h.ctx); err != nil {
		h.t.Fatal(err)
	}
	h.s.Wait()
}

func (h *hosted) count() int {
	var n int
	h.db.Server.QueryRow(h.ctx, `SELECT count(*) FROM deploys WHERE project = $1`, h.id).Scan(&n)
	return n
}

// Follow main (19.5, plan M19 D16): two tickets Done close together give one deploy of main's head,
// listing both; a head that failed is not tried again until a newer one; off gives none.
func TestFollowMainDeploysTheHeadOnce(t *testing.T) {
	h := newHosted(t, map[string]any{"follow_main": true})
	h.deploy(asked)
	h.follow()
	if h.count() != 1 {
		t.Fatal("a deploy with no ticket Done")
	}
	h.done(1, time.Now())
	h.done(2, time.Now())
	h.follow()
	if h.count() != 1 {
		t.Fatal("a deploy of the commit that serves")
	}
	h.setHead(head2)
	h.follow()
	d, _ := h.s.deployByNumber(h.ctx, h.id, 2)
	if d.State != "live" || d.Why != "follow_main" || d.Commit != head2 || !slices.Equal(d.Tickets, []string{"SH-1", "SH-2"}) {
		t.Fatalf("the deploy: %+v", d)
	}
	h.follow()
	if h.count() != 2 {
		t.Fatal("a second deploy of the same head")
	}
	h.done(3, time.Now().Add(time.Second))
	h.setHead(head3)
	h.build.ImageErr = os.ErrInvalid
	h.follow()
	h.follow()
	h.follow()
	if h.count() != 3 {
		t.Fatalf("a failed head was tried again: %d deploys", h.count())
	}
	h.build.ImageErr = nil
	h.setHead("4444444444444444444444444444444444444444")
	h.follow()
	if d, _ := h.s.deployByNumber(h.ctx, h.id, 4); d.State != "live" {
		t.Fatalf("a newer head after a failed one: %+v", d)
	}
}

func TestFollowMainOffDeploysNothing(t *testing.T) {
	h := newHosted(t, nil)
	h.deploy(asked)
	h.done(1, time.Now())
	h.setHead(head2)
	h.follow()
	if h.count() != 1 {
		t.Fatal("Follow main off deployed")
	}
}

// A project that waits for its Dockerfile (19.2 step 2): the ticket is made once, and hosting goes on
// by itself once main has the Dockerfile.
func TestHostingGoesOnWhenMainHasTheDockerfile(t *testing.T) {
	h := newHosted(t, nil)
	var made []board.NewTicket
	h.s.Tickets = func(_ context.Context, _ string, n board.NewTicket) (TicketMade, error) {
		made = append(made, n)
		var id int64
		err := h.db.Server.QueryRow(h.ctx, `INSERT INTO tickets (project, number, title, kind, stage, origin, created_by, position)
			VALUES ($1, 9, $2, 'chore', 'backlog', 'operator', 'operator', 9) RETURNING id`, h.id, n.Title).Scan(&id)
		return TicketMade{ID: id, Key: "SH-9"}, err
	}
	h.git.files = map[string][]byte{}
	for range 2 {
		if key, err := h.s.Hostable(h.ctx, h.id); err != nil || key != "SH-9" {
			t.Fatalf("%q %v", key, err)
		}
	}
	if len(made) != 1 || made[0].Kind != "chore" || len(made[0].Checks) != 3 || made[0].Checks[1].Spec != "docker build -f 'Dockerfile' ." {
		t.Fatalf("the tickets: %+v", made)
	}
	h.follow()
	if h.count() != 0 {
		t.Fatal("deployed with no Dockerfile")
	}
	h.git.files = map[string][]byte{"Dockerfile": []byte("FROM scratch\n")}
	h.setHead(head2)
	h.follow()
	d, _ := h.s.deployByNumber(h.ctx, h.id, 1)
	if d.State != "live" || d.Why != "hostable" || d.Commit != head2 {
		t.Fatalf("%+v", d)
	}
}

// Delete forgets the hosting: the sandbox, the images, the data and the values go.
func TestForgetRemovesWhatHostingHeld(t *testing.T) {
	h := newHosted(t, nil)
	h.SetValues(t)
	h.deploy(asked)
	h.deploy(asked)
	h.write("shop.db", "x")
	if err := h.s.Forget(h.ctx, h.id); err != nil {
		t.Fatal(err)
	}
	if len(h.rt.images) != 0 || h.rt.running[SandboxID(h.id, 2)] {
		t.Fatalf("kept: %v", h.rt.images)
	}
	if _, err := os.Stat(h.s.projectDir(h.id)); !os.IsNotExist(err) {
		t.Fatal("the project's folder was kept")
	}
	if v, _ := h.s.values(h.ctx, h.id); len(v) != 0 {
		t.Fatal("the values were kept")
	}
}

func (h *hosted) SetValues(t *testing.T) {
	if err := h.s.SetValues(h.ctx, h.id, map[string]string{"A": "1"}, "test"); err != nil {
		t.Fatal(err)
	}
}

// Agents cannot deploy (19.5, rule 12): no tool of the Crew MCP, of the runs' or of the operator's own
// tools, deploys, rolls back, stops hosting, or writes settings or values.
func TestNoMCPToolDeploysOrWritesSettings(t *testing.T) {
	tools, err := baseline.Tools()
	if err != nil {
		t.Fatal(err)
	}
	bad := regexp.MustCompile(`deploy|host|roll|setting|value|secret|stop`)
	if len(tools) < 50 {
		t.Fatalf("%d tools", len(tools))
	}
	for name := range tools {
		if bad.MatchString(name) {
			t.Errorf("the tool %s", name)
		}
	}
}

// The ticket that makes a project hostable is cancelled (21, recovery row 67): hosting still waits,
// the view names the ticket and its stage, and when a replacement puts the Dockerfile on main, hosting
// goes on by itself.
func TestACancelledHostableTicketLeavesHostingWaiting(t *testing.T) {
	h := newHosted(t, nil)
	h.s.Tickets = func(_ context.Context, _ string, n board.NewTicket) (TicketMade, error) {
		var id int64
		err := h.db.Server.QueryRow(h.ctx, `INSERT INTO tickets (project, number, title, kind, stage, origin, created_by, position)
			VALUES ($1, 9, $2, 'chore', 'backlog', 'operator', 'operator', 9) RETURNING id`, h.id, n.Title).Scan(&id)
		return TicketMade{ID: id, Key: "SH-9"}, err
	}
	h.git.files = map[string][]byte{}
	if _, err := h.s.Hostable(h.ctx, h.id); err != nil {
		t.Fatal(err)
	}
	h.db.Server.Exec(h.ctx, `UPDATE tickets SET stage = 'cancelled' WHERE number = 9`)
	v, err := h.s.View(h.ctx, h.id, "shop")
	if err != nil || v.State != "waiting_dockerfile" || v.Ticket == nil || v.Ticket.Key != "SH-9" || v.Ticket.Stage != "cancelled" {
		t.Fatalf("the view: %+v %v", v, err)
	}
	h.git.files = map[string][]byte{"Dockerfile": []byte("FROM scratch\n")}
	h.setHead(head2)
	h.follow()
	if d, _ := h.s.deployByNumber(h.ctx, h.id, 1); d.State != "live" || d.Why != "hostable" {
		t.Fatalf("%+v", d)
	}
}

// After a roll back, Follow main does not deploy again the head it rolled away from, until a newer one
// (review of M19, finding 7).
func TestFollowMainLeavesTheHeadARollBackLeft(t *testing.T) {
	h := newHosted(t, map[string]any{"follow_main": true})
	h.deploy(asked)
	h.done(1, time.Now())
	h.setHead(head2)
	h.follow()
	h.deploy(Ask{Why: "rollback", By: "operator", From: 1})
	h.done(2, time.Now().Add(time.Second))
	h.follow()
	if n := h.count(); n != 3 {
		t.Fatalf("the head the roll back left was deployed again: %d deploys", n)
	}
	h.setHead(head3)
	h.follow()
	if d, _ := h.s.deployByNumber(h.ctx, h.id, 4); d.Commit != head3 || d.State != "live" {
		t.Fatalf("a newer head: %+v", d)
	}
}
