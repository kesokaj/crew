package mcp

import (
	"context"
	"errors"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/kesokaj/crew/internal/db"
	"github.com/kesokaj/crew/internal/db/dbtest"
)

// The rules of a personal token (11.7, R168): shown once and kept as a hash; it names its projects; it
// runs out after its days unless renewed; it can be revoked; it makes at most its calls in a minute;
// five wrong tokens from one address block that address for an hour; a run's token is not one.
func TestThePersonalTokenRules(t *testing.T) {
	ctx := context.Background()
	d := dbtest.New(t, false)
	for _, q := range []string{`INSERT INTO projects (id, name, key, state, repo_source) VALUES ('00000000-0000-4000-8000-0000000000f1', 'shop', 'SF', 'running', 'local')`,
		`INSERT INTO projects (id, name, key, state, repo_source) VALUES ('00000000-0000-4000-8000-0000000000f2', 'other', 'OT', 'running', 'local')`} {
		if _, err := d.Server.Exec(ctx, q); err != nil {
			t.Fatal(err)
		}
	}
	now := time.Date(2026, 10, 16, 12, 0, 0, 0, time.UTC)
	perMinute := 3
	tk := &Tokens{Pool: d.Server, Now: func() time.Time { return now }, Days: func(context.Context) int { return 90 },
		PerMinute: func(context.Context) int { return perMinute }}
	plain, pt, err := tk.Create(ctx, "laptop", []string{"shop"}, false)
	if err != nil || !strings.HasPrefix(plain, "crew_pat_") || len(plain) < 40 || pt.MayAnswer || len(pt.Projects) != 1 || pt.Projects[0] != "shop" ||
		!pt.ExpiresAt.Equal(now.Add(90*24*time.Hour)) {
		t.Fatalf("%q %+v %v", plain, pt, err)
	}
	var stored int
	d.Server.QueryRow(ctx, `SELECT count(*) FROM personal_tokens WHERE hash = $1 OR hash LIKE '%' || $1 || '%'`, plain).Scan(&stored)
	if stored != 0 {
		t.Fatal("the token itself is stored")
	}
	if _, _, err := tk.Create(ctx, "laptop", []string{"shop"}, false); err == nil {
		t.Fatal("two live tokens of one name")
	}
	if _, _, err := tk.Create(ctx, "x", []string{"nothere"}, false); err == nil {
		t.Fatal("a token for a project that is not there")
	}
	req := func(token, addr string) (Caller, error) {
		r := httptest.NewRequest("POST", "/mcp", nil)
		r.Header.Set("Authorization", "Bearer "+token)
		r.Header.Set("X-Forwarded-For", addr)
		return tk.Resolve(r)
	}
	c, err := req(plain, "203.0.113.7")
	if err != nil || c.Token != pt.ID || c.TokenName != "laptop" || len(c.Projects) != 1 || c.Projects[0] != "00000000-0000-4000-8000-0000000000f1" || c.Role != "token" {
		t.Fatalf("%+v %v", c, err)
	}
	list, _ := tk.List(ctx)
	if len(list) != 1 || list[0].LastAddr == nil || *list[0].LastAddr != "203.0.113.7" || list[0].LastUsedAt == nil {
		t.Fatalf("the list: %+v", list)
	}
	// At its requests of a minute it waits: every request counts, a tools/list as much as a call.
	for i := 0; i < 2; i++ {
		if _, err := req(plain, "203.0.113.7"); err != nil {
			t.Fatalf("request %d: %v", i+2, err)
		}
	}
	if _, err := req(plain, "203.0.113.7"); !errors.Is(err, ErrTooMany) {
		t.Fatalf("past its requests of a minute: %v", err)
	}
	now = now.Add(61 * time.Second)
	if _, err := req(plain, "203.0.113.7"); err != nil {
		t.Fatalf("a minute later: %v", err)
	}
	perMinute = 100
	// It runs out, and renewing gives it its days again; revoked, it is gone.
	now = now.Add(91 * 24 * time.Hour)
	if _, err := req(plain, "203.0.113.8"); !errors.Is(err, ErrNoToken) {
		t.Fatalf("a token that ran out: %v", err)
	}
	if _, err := tk.Renew(ctx, pt.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := req(plain, "203.0.113.9"); err != nil {
		t.Fatalf("renewed: %v", err)
	}
	if _, err := tk.Revoke(ctx, pt.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := req(plain, "203.0.113.10"); !errors.Is(err, ErrNoToken) {
		t.Fatalf("revoked: %v", err)
	}
	// Five wrong tokens block the address, even for a right one, for an hour; another address is not.
	other, _, err := tk.Create(ctx, "laptop", []string{"shop", "other"}, true)
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < FailuresToBlock; i++ {
		if _, err := req("crew_pat_wrong", "198.51.100.1"); !errors.Is(err, ErrNoToken) {
			t.Fatalf("a wrong token: %v", err)
		}
	}
	// The address is blocked for wrong tokens, which add no row more; a right token is not guessed,
	// and works from there too.
	if _, err := req("crew_pat_wrong", "198.51.100.1"); !errors.Is(err, ErrTooMany) {
		t.Fatalf("a wrong token from a blocked address: %v", err)
	}
	var rows int
	d.Server.QueryRow(ctx, `SELECT count(*) FROM mcp_token_failures WHERE addr = '198.51.100.1'`).Scan(&rows)
	if rows != FailuresToBlock {
		t.Fatalf("%d rows of failures, not %d", rows, FailuresToBlock)
	}
	if c, err := req(other, "198.51.100.1"); err != nil || !c.MayAnswer || len(c.Projects) != 2 {
		t.Fatalf("a right token from a blocked address: %+v %v", c, err)
	}
	// An hour later the address is free, and the old rows are gone with the next wrong token.
	now = now.Add(BlockFor + time.Minute)
	if _, err := req("crew_pat_wrong", "198.51.100.3"); !errors.Is(err, ErrNoToken) {
		t.Fatalf("a wrong token an hour later: %v", err)
	}
	d.Server.QueryRow(ctx, `SELECT count(*) FROM mcp_token_failures`).Scan(&rows)
	if rows != 1 {
		t.Fatalf("%d rows of failures are kept, not the one of the last hour", rows)
	}
	// Every change of a token is in the history, never the token itself.
	var history string
	d.Server.QueryRow(ctx, `SELECT string_agg(after::text, ' ' ORDER BY seq) FROM changes WHERE tbl = 'personal_tokens'`).Scan(&history)
	for _, want := range []string{`"made": true`, `"renewed": true`, `"revoked": true`} {
		if !strings.Contains(history, want) {
			t.Errorf("the history lacks %s: %s", want, history)
		}
	}
	if strings.Contains(history, plain) || strings.Contains(history, other) {
		t.Fatal("the history holds a token")
	}
	// A run's token is no personal token, and a request with none is refused.
	if _, err := req("", "192.0.2.1"); !errors.Is(err, ErrNoToken) {
		t.Fatalf("no token: %v", err)
	}
}

// A token is made, and changed, together with its row in the history: when the row cannot be written,
// no token is made and none is changed (review of M9, round 2).
func TestATokensChangeAndItsHistoryAreOne(t *testing.T) {
	ctx := context.Background()
	d := dbtest.New(t, false)
	if _, err := d.Server.Exec(ctx, `INSERT INTO projects (id, name, key, state, repo_source) VALUES ('00000000-0000-4000-8000-0000000000f1', 'shop', 'SF', 'running', 'local')`); err != nil {
		t.Fatal(err)
	}
	tk := &Tokens{Pool: d.Server, Days: func(context.Context) int { return 90 }, PerMinute: func(context.Context) int { return 60 }}
	_, live, err := tk.Create(ctx, "kept", []string{"shop"}, false)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := d.ConnectAs(t, db.RoleMigrate).Exec(ctx, `ALTER TABLE changes ADD CONSTRAINT no_tokens CHECK (tbl <> 'personal_tokens') NOT VALID`); err != nil {
		t.Fatal(err)
	}
	if _, _, err := tk.Create(ctx, "lost", []string{"shop"}, false); err == nil {
		t.Fatal("a token made without its history")
	}
	if _, err := tk.Revoke(ctx, live.ID); err == nil {
		t.Fatal("a token revoked without its history")
	}
	var made, revoked int
	d.Server.QueryRow(ctx, `SELECT count(*) FILTER (WHERE name = 'lost'), count(*) FILTER (WHERE revoked_at IS NOT NULL) FROM personal_tokens`).Scan(&made, &revoked)
	if made != 0 || revoked != 0 {
		t.Fatalf("made %d, revoked %d", made, revoked)
	}
}

// The gate's crew-gate is Crew's own and hidden (16.2): no personal token names it, as no list of the
// operator's shows it (M16's fourth phase).
func TestNoTokenNamesTheGatesProject(t *testing.T) {
	ctx := context.Background()
	d := dbtest.New(t, false)
	if _, err := d.Server.Exec(ctx, `INSERT INTO projects (id, name, key, state, repo_source, hidden)
		VALUES ('00000000-0000-4000-8000-0000000000c1', 'crew-gate', 'CGATE', 'running', 'local', true)`); err != nil {
		t.Fatal(err)
	}
	tk := &Tokens{Pool: d.Server, Days: func(context.Context) int { return 90 }, PerMinute: func(context.Context) int { return 60 }}
	if _, _, err := tk.Create(ctx, "laptop", []string{"crew-gate"}, false); err == nil || !strings.Contains(err.Error(), `no project "crew-gate"`) {
		t.Fatalf("a token for the gate's project: %v", err)
	}
	if _, err := d.Server.Exec(ctx, `UPDATE projects SET hidden = false`); err != nil {
		t.Fatal(err)
	}
	if _, _, err := tk.Create(ctx, "laptop", []string{"crew-gate"}, false); err != nil {
		t.Fatalf("the same project shown: %v", err)
	}
}
