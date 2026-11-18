package sessions_test

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/kesokaj/crew/internal/db/dbtest"
	"github.com/kesokaj/crew/internal/sessions"
)

// A handover is the same person's: a nonce handed in with another email's sign-in, as after the
// admin email was changed, takes nothing over, and is used up (D13).
func TestAHandoverOfAnotherEmailTakesNothingOver(t *testing.T) {
	ctx := context.Background()
	pool := dbtest.New(t, false).Server
	var st sessions.Store
	now := time.Now()
	id, _, err := st.Seen(ctx, pool, "old-id", "old@example.com", now.Add(-time.Hour), "a browser")
	if err != nil {
		t.Fatal(err)
	}
	nonce, err := sessions.Handover(ctx, pool, id)
	if err != nil {
		t.Fatal(err)
	}
	moved, err := sessions.TakeOver(ctx, pool, nonce, "new-id", "new@example.com", now)
	if err != nil || moved {
		t.Fatalf("another email: moved %v, %v", moved, err)
	}
	var n int
	pool.QueryRow(ctx, `SELECT count(*) FROM session_handovers`).Scan(&n)
	if n != 0 {
		t.Fatalf("the nonce is used up: %d left", n)
	}
	// The same email in another case is the same person.
	nonce, _ = sessions.Handover(ctx, pool, id)
	if moved, err := sessions.TakeOver(ctx, pool, nonce, "new-id", "OLD@example.com", now); err != nil || !moved {
		t.Fatalf("the same email: moved %v, %v", moved, err)
	}
}

// A device signed out cannot be handed a sign-in, and its ids stay refused.
func TestASignedOutDeviceTakesNoHandover(t *testing.T) {
	ctx := context.Background()
	pool := dbtest.New(t, false).Server
	var st sessions.Store
	id, _, _ := st.Seen(ctx, pool, "dev", "a@example.com", time.Now(), "")
	nonce, _ := sessions.Handover(ctx, pool, id)
	tx, _ := pool.Begin(ctx)
	if err := sessions.SignOut(ctx, tx, id, "a@example.com"); err != nil {
		t.Fatal(err)
	}
	tx.Commit(ctx)
	if moved, err := sessions.TakeOver(ctx, pool, nonce, "dev2", "a@example.com", time.Now()); err != nil || moved {
		t.Fatalf("a signed-out device: moved %v, %v", moved, err)
	}
	if _, out, _ := st.Seen(ctx, pool, "dev", "a@example.com", time.Now(), ""); !out {
		t.Fatal("the signed-out device's id passes")
	}
}

// The browser's User-Agent is kept to 400 bytes, cut on a rune's edge.
func TestALongUserAgentIsCut(t *testing.T) {
	ctx := context.Background()
	pool := dbtest.New(t, false).Server
	var st sessions.Store
	agent := strings.Repeat("a", 399) + "é" + strings.Repeat("b", 100)
	if _, _, err := st.Seen(ctx, pool, "dev", "a@example.com", time.Now(), agent); err != nil {
		t.Fatal(err)
	}
	var got string
	pool.QueryRow(ctx, `SELECT user_agent FROM sessions`).Scan(&got)
	if got != strings.Repeat("a", 399) {
		t.Fatalf("%d bytes: %q", len(got), got[len(got)-3:])
	}
}

// A device whose newest sign-in is older than the ingress's 30 days and one goes, with its ids and
// its handovers; a younger one stays, and so does a handover still good.
func TestPruneTakesTheDevicesPastTheirDays(t *testing.T) {
	ctx := context.Background()
	pool := dbtest.New(t, false).Server
	// Made as rows, since a new device's own Prune would take the old one at once.
	var old, young int64
	for _, d := range []struct {
		days int
		id   *int64
	}{{32, &old}, {29, &young}} {
		pool.QueryRow(ctx, `INSERT INTO sessions (email, auth_at) VALUES ('a@example.com', now() - make_interval(days => $1)) RETURNING id`, d.days).Scan(d.id)
		pool.Exec(ctx, `INSERT INTO session_ids (jti, session) VALUES ($1, $2)`, "id-"+strings.Repeat("x", d.days), *d.id)
	}
	sessions.Handover(ctx, pool, old)
	sessions.Handover(ctx, pool, young)
	pool.Exec(ctx, `INSERT INTO session_handovers (nonce_sha256, session, until) VALUES (repeat('0', 64), $1, now() - interval '1 minute')`, young)
	n, err := sessions.Prune(ctx, pool)
	if err != nil || n != 1 {
		t.Fatalf("pruned %d, %v", n, err)
	}
	var ids, handovers int
	pool.QueryRow(ctx, `SELECT count(*) FROM session_ids`).Scan(&ids)
	pool.QueryRow(ctx, `SELECT count(*) FROM session_handovers`).Scan(&handovers)
	if ids != 1 || handovers != 1 {
		t.Fatalf("left: %d ids, %d handovers", ids, handovers)
	}
}

// A device signed out is kept for good, however old its sign-in: Prune takes it not.
func TestPruneKeepsADeviceSignedOut(t *testing.T) {
	ctx := context.Background()
	pool := dbtest.New(t, false).Server
	pool.Exec(ctx, `INSERT INTO sessions (email, auth_at, signed_out_at) VALUES ('a@example.com', now() - interval '90 days', now() - interval '89 days')`)
	if n, err := sessions.Prune(ctx, pool); err != nil || n != 0 {
		t.Fatalf("pruned %d, %v", n, err)
	}
}
