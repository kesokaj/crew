package db_test

import (
	"context"
	"encoding/json"
	"slices"
	"strings"
	"testing"

	"github.com/kesokaj/crew/internal/db"
	"github.com/kesokaj/crew/internal/db/dbtest"
)

// Step 23 takes claude out of a machine that ran it (design 0.42): its login, its login check, its
// limits, and its place in the lead queue go; its models stay, marked gone; the machine's lists keep a
// role the operator set without claude's entries and give up every other, for the baseline to fill;
// a project's agents and a ticket's pins lose claude's entries; agy's entries named after Claude
// models stay. The runs of claude stay.
func TestStep23TakesClaudeOut(t *testing.T) {
	ctx := context.Background()
	d := dbtest.New(t, true)
	conn := d.ConnectAs(t, db.RoleMigrate)
	steps, _ := db.Steps()
	if steps[22].Name != "0023_agy_only.sql" {
		t.Fatalf("step 23 is %s", steps[22].Name)
	}
	if _, err := db.MigrateSteps(ctx, conn, steps[:22]); err != nil {
		t.Fatal(err)
	}
	const project = "00000000-0000-4000-8000-0000000000e1"
	for _, q := range []string{
		`INSERT INTO credentials (id, kind, nonce, sealed) VALUES ('claude', 'value', '\x00', '\x00'), ('agy', 'file', '\x00', '\x00')`,
		`INSERT INTO cli_logins (cli, state) VALUES ('claude', 'signed_in'), ('agy', 'signed_in')`,
		`INSERT INTO quota (cli, grp, win, source) VALUES ('claude', 'claude', '5h', 'run'), ('agy', 'gemini', '5h', 'usage')`,
		`INSERT INTO lead_waits (key, owner, class, cli, what) VALUES ('a', 'p', 'analysis', 'claude', 'x'), ('b', 'p', 'analysis', 'agy', 'y')`,
		`INSERT INTO models (cli, name, model) VALUES ('claude', 'opus', 'opus'), ('agy', 'claude-opus-5-5-high', 'claude-opus-5-5')`,
		`INSERT INTO baseline_entries (role, entry) VALUES ('lead', 'claude/opus'), ('lead', 'agy/claude-opus-5-5')`,
		`INSERT INTO settings (project, doc, updated_by) VALUES (NULL, '{"lead_slots": {"agy": 1, "claude": 2},
			"agents": {"clis": ["agy", "claude"], "extra_models": {"claude": ["claude-opus-5-5"]}, "auto": {"set_by_you": ["builder"]},
			"roles": {"builder": {"model": ["agy/gemini-3.8-flash", "claude/sonnet"], "effort": "high"},
				"lead": {"model": ["claude/opus", "agy/claude-opus-5-5"], "effort": "high"}}}}', 'test')`,
		`INSERT INTO projects (id, name, key, state, repo_source) VALUES ('` + project + `', 'shop', 'SF', 'running', 'local')`,
		`INSERT INTO settings (project, doc, updated_by) VALUES ('` + project + `', '{"agents": {"reviewer": {"model": "claude/sonnet", "effort": "low"},
			"tester": {"model": "agy/claude-sonnet-5-5"}, "checks_use_another_family": false}}', 'test')`,
		`INSERT INTO tickets (project, number, title, kind, stage, origin, created_by, position, agents)
			VALUES ('` + project + `', 1, 't', 'feature', 'backlog', 'plan', 'lead', 1, '{"builder": {"model": "claude/opus", "effort": "high"}}')`,
	} {
		if _, err := conn.Exec(ctx, q); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}
	if applied, err := db.MigrateSteps(ctx, conn, steps[:23]); err != nil || !slices.Equal(applied, []string{"0023_agy_only.sql"}) {
		t.Fatalf("applied %v: %v", applied, err)
	}
	count := func(q string) int {
		t.Helper()
		var n int
		if err := conn.QueryRow(ctx, q).Scan(&n); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
		return n
	}
	for q, want := range map[string]int{
		`SELECT count(*) FROM credentials WHERE id = 'claude'`:                                     0,
		`SELECT count(*) FROM credentials WHERE id = 'agy'`:                                        1,
		`SELECT count(*) FROM cli_logins`:                                                          1,
		`SELECT count(*) FROM quota`:                                                               1,
		`SELECT count(*) FROM lead_waits`:                                                          1,
		`SELECT count(*) FROM models WHERE cli = 'claude' AND NOT offered AND gone_at IS NOT NULL`: 1,
		`SELECT count(*) FROM models WHERE cli = 'agy' AND offered`:                                1,
		`SELECT count(*) FROM baseline_entries`:                                                    1,
		`SELECT count(*) FROM changes WHERE tbl = 'credentials' AND row_key = 'claude'`:            1,
	} {
		if n := count(q); n != want {
			t.Errorf("%s: %d, want %d", q, n, want)
		}
	}
	doc := func(q string) string {
		t.Helper()
		var b []byte
		if err := conn.QueryRow(ctx, q).Scan(&b); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
		var v any
		json.Unmarshal(b, &v)
		out, _ := json.Marshal(v)
		return string(out)
	}
	if got := doc(`SELECT doc FROM settings WHERE project IS NULL`); got != `{"agents":{"auto":{"set_by_you":["builder"]},"clis":["agy"],`+
		`"extra_models":{},"roles":{"builder":{"effort":"high","model":["agy/gemini-3.8-flash"]}}},"lead_slots":{"agy":1}}` {
		t.Errorf("the machine's settings: %s", got)
	}
	if got := doc(`SELECT doc FROM settings WHERE project IS NOT NULL`); got != `{"agents":{"checks_use_another_family":false,`+
		`"reviewer":{"effort":"low"},"tester":{"model":"agy/claude-sonnet-5-5"}}}` {
		t.Errorf("the project's settings: %s", got)
	}
	if got := doc(`SELECT agents FROM tickets`); got != `{"builder":{"effort":"high"}}` {
		t.Errorf("the ticket's pins: %s", got)
	}
}

// Step 28 takes what step 23 left of claude: an open recommendation of a claude entry is dismissed, a
// watched change to one is kept, a comparison with one is stopped, and lead_slots and extra_models lose
// claude also on a machine whose lists name no claude model. What names agy only is left alone.
func TestStep28TakesClaudesChoicesOut(t *testing.T) {
	ctx := context.Background()
	d := dbtest.New(t, true)
	conn := d.ConnectAs(t, db.RoleMigrate)
	steps, _ := db.Steps()
	if steps[27].Name != "0028_agy_only_choices.sql" {
		t.Fatalf("step 28 is %s", steps[27].Name)
	}
	if _, err := db.MigrateSteps(ctx, conn, steps[:27]); err != nil {
		t.Fatal(err)
	}
	for _, q := range []string{
		`INSERT INTO model_choices (role, kind, reason, from_entry, from_effort, to_entry, to_effort, state) VALUES
			('reviewer', 'recommend', 'r', 'agy/gemini-4-argon', 'high', 'claude/opus', 'high', 'open'),
			('builder', 'change', 'c', 'agy/gemini-3.8-flash', 'high', 'claude/sonnet', 'high', 'watching'),
			('tester', 'recommend', 'r', 'agy/gemini-3.8-flash', 'medium', 'agy/claude-sonnet-5-5', 'medium', 'open')`,
		`INSERT INTO comparisons (role, own_entry, own_effort, other_entry, other_effort) VALUES
			('builder', 'agy/gemini-4-argon', 'high', 'claude/sonnet', 'high'), ('tester', 'agy/gemini-3.8-flash', 'medium', 'agy/gemini-4-argon', 'low')`,
		`INSERT INTO settings (project, doc, updated_by) VALUES (NULL, '{"lead_slots": {"agy": 1, "claude": 2}, "agents": {"extra_models": {"claude": [], "agy": []}}}', 'test')`,
	} {
		if _, err := conn.Exec(ctx, q); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}
	if _, err := db.MigrateSteps(ctx, conn, steps[:28]); err != nil {
		t.Fatal(err)
	}
	var states string
	conn.QueryRow(ctx, `SELECT string_agg(role || ':' || state, ' ' ORDER BY id) FROM model_choices`).Scan(&states)
	if states != "reviewer:dismissed builder:kept tester:open" {
		t.Fatalf("the choices: %s", states)
	}
	conn.QueryRow(ctx, `SELECT string_agg(role || ':' || state, ' ' ORDER BY id) FROM comparisons`).Scan(&states)
	if states != "builder:stopped tester:running" {
		t.Fatalf("the comparisons: %s", states)
	}
	var doc string
	conn.QueryRow(ctx, `SELECT doc::text FROM settings WHERE project IS NULL`).Scan(&doc)
	if strings.Contains(doc, "claude") || !strings.Contains(doc, `"agy"`) {
		t.Fatalf("the machine's settings: %s", doc)
	}
}
