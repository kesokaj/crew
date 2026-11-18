package db_test

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/kesokaj/crew/internal/db"
	"github.com/kesokaj/crew/internal/db/dbtest"
)

// step is the SQL of the numbered schema step.
func step(t *testing.T, n int) string {
	t.Helper()
	steps, err := db.Steps()
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range steps {
		if s.Number == n {
			return s.SQL
		}
	}
	t.Fatalf("no step %d", n)
	return ""
}

// Step 61 gives Crew's project, the one the machine's improvements.crew_project names, made before
// 0.53, the checks the operator chose for report 8: make check, no setup, check sandboxes in Sources,
// and no gcloud in its runs. It keeps the rest of its settings, records the change, and touches no other
// project: not the gate's hidden one, not one with another key, and not one that bears the key CREW
// while no project is named Crew's (review of 0.53).
func TestCrewsProjectGetsItsChecks(t *testing.T) {
	ctx := context.Background()
	d := dbtest.New(t, false)
	const crew, gate, other = "c0000000-0000-4000-8000-0000000000c1", "c0000000-0000-4000-8000-0000000000c2", "c0000000-0000-4000-8000-0000000000c3"
	for _, q := range []string{
		`INSERT INTO projects (id, name, key, state, repo_source) VALUES ('` + crew + `', 'crew', 'CREW', 'paused', 'local')`,
		`INSERT INTO projects (id, name, key, state, repo_source, hidden) VALUES ('` + gate + `', 'crew-gate', 'GATE', 'done', 'local', true)`,
		`INSERT INTO projects (id, name, key, state, repo_source) VALUES ('` + other + `', 'shop', 'SF', 'running', 'local')`,
		`INSERT INTO settings (project, doc, updated_by) VALUES ('` + crew + `', '{"limits": {"run_max_turns": 90}, "sandbox": {"github_read": ["kesokaj/x"]}}', 'operator')`,
		`INSERT INTO settings (project, doc, updated_by) VALUES ('` + gate + `', '{"sandbox": {"test": "go test ./...", "setup": "", "network": "model_only", "credentials": []}}', 'crew')`,
	} {
		if _, err := d.Server.Exec(ctx, q); err != nil {
			t.Fatal(err)
		}
	}
	own := func(project string) map[string]any {
		t.Helper()
		var raw []byte
		if err := d.Server.QueryRow(ctx, `SELECT doc FROM settings WHERE project = $1`, project).Scan(&raw); err != nil {
			return nil
		}
		var doc map[string]any
		if err := json.Unmarshal(raw, &doc); err != nil {
			t.Fatal(err)
		}
		return doc
	}
	// No project is Crew's yet: the one that bears the key CREW is left as it is.
	if _, err := d.Server.Exec(ctx, step(t, 61)); err != nil {
		t.Fatal(err)
	}
	if sb := own(crew)["sandbox"].(map[string]any); sb["test"] != nil || sb["check_network"] != nil {
		t.Fatalf("a project only bearing the key CREW was changed: %v", sb)
	}
	if _, err := d.Server.Exec(ctx, `INSERT INTO settings (project, doc, updated_by) VALUES (NULL, '{"improvements": {"crew_project": "crew"}}', 'test')
		ON CONFLICT ((coalesce(project::text, 'machine'))) DO UPDATE SET doc = EXCLUDED.doc`); err != nil {
		t.Fatal(err)
	}
	if _, err := d.Server.Exec(ctx, step(t, 61)); err != nil {
		t.Fatal(err)
	}
	doc := own(crew)
	sb, _ := doc["sandbox"].(map[string]any)
	if sb["setup"] != "" || sb["test"] != "make check" || sb["check_network"] != "sources" || len(sb["credentials"].([]any)) != 0 ||
		len(sb["github_read"].([]any)) != 1 || doc["limits"].(map[string]any)["run_max_turns"] != float64(90) {
		t.Fatalf("Crew's project after step 61: %v", doc)
	}
	if sb := own(gate)["sandbox"].(map[string]any); sb["test"] != "go test ./..." || sb["check_network"] != nil {
		t.Fatalf("the gate's project was changed: %v", sb)
	}
	if own(other) != nil {
		t.Fatal("another project got settings")
	}
	var changes int
	if err := d.Server.QueryRow(ctx, `SELECT count(*) FROM changes WHERE tbl = 'settings' AND project = $1 AND who = 'crew'
		AND before->'limits' IS NOT NULL AND after->'sandbox'->>'test' = 'make check'`, crew).Scan(&changes); err != nil || changes != 1 {
		t.Fatalf("%d changes recorded: %v", changes, err)
	}
	// A Crew's project with no settings of its own gets them too.
	if _, err := d.Server.Exec(ctx, `DELETE FROM settings WHERE project = $1`, crew); err != nil {
		t.Fatal(err)
	}
	if _, err := d.Server.Exec(ctx, step(t, 61)); err != nil {
		t.Fatal(err)
	}
	if sb := own(crew)["sandbox"].(map[string]any); sb["test"] != "make check" || sb["check_network"] != "sources" {
		t.Fatalf("Crew's project with no settings: %v", sb)
	}
}

// Step 62 takes the settings of a budget out of the stored settings of the machine and of every
// project, and leaves the rest (since 0.53); a suite needs no budget_usd any more.
func TestTheBudgetsAreTakenOutOfTheSettings(t *testing.T) {
	ctx := context.Background()
	d := dbtest.New(t, false)
	const shop = "c0000000-0000-4000-8000-0000000000d1"
	for _, q := range []string{
		`INSERT INTO projects (id, name, key, state, repo_source) VALUES ('` + shop + `', 'shop', 'SF', 'running', 'local')`,
		`INSERT INTO settings (project, doc, updated_by) VALUES (NULL, '{"version": 1, "disk": {"clean_at": 80}, "improvements": {"budget_usd_per_week": 10, "run_every_days": 5}}', 'test')
			ON CONFLICT ((coalesce(project::text, 'machine'))) DO UPDATE SET doc = EXCLUDED.doc`,
		`INSERT INTO settings (project, doc, updated_by) VALUES ('` + shop + `', '{"budget_usd": 140, "limits": {"run_budget_usd": 5,
			"default_task_budget_usd": 3, "max_fix_rounds": 4}, "knowledge": {"housekeeping_budget_usd": 5, "evals": {"budget_usd": 10,
			"max_cost_increase": 0.2, "repeats": 2}}}', 'test')`,
	} {
		if _, err := d.Server.Exec(ctx, q); err != nil {
			t.Fatal(err)
		}
	}
	// The step's updates, as the server's role may run them; the default of budget_usd is the migrated
	// database's, which the suite below takes.
	updates := 0
	for _, line := range strings.Split(step(t, 62), "\n") {
		if strings.HasPrefix(line, "UPDATE settings") {
			if _, err := d.Server.Exec(ctx, line); err != nil {
				t.Fatal(err)
			}
			updates++
		}
	}
	if updates != 7 {
		t.Fatalf("%d updates of settings in step 62", updates)
	}
	var machine, project string
	d.Server.QueryRow(ctx, `SELECT doc::text FROM settings WHERE project IS NULL`).Scan(&machine)
	d.Server.QueryRow(ctx, `SELECT doc::text FROM settings WHERE project = $1`, shop).Scan(&project)
	if strings.Contains(machine, "budget") || !strings.Contains(machine, `"run_every_days": 5`) || !strings.Contains(machine, `"clean_at": 80`) {
		t.Fatalf("the machine's settings: %s", machine)
	}
	if strings.Contains(project, "budget") || strings.Contains(project, "max_cost_increase") || !strings.Contains(project, `"max_fix_rounds": 4`) ||
		!strings.Contains(project, `"repeats": 2`) {
		t.Fatalf("the project's settings: %s", project)
	}
	var suite int64
	if err := d.Server.QueryRow(ctx, `INSERT INTO eval_suites (project, purpose, repeats, asked_by) VALUES ($1, 'on_demand', 1, 'operator') RETURNING id`,
		shop).Scan(&suite); err != nil {
		t.Fatalf("a suite with no budget: %v", err)
	}
}

// Step 63 takes the price list and cheaper_share out of the machine's stored settings, dismisses an open
// recommendation for the reason cheaper, a change made for it, still watched, keeping its state, and
// measures an improvement of cost_per_ticket by tokens_per_ticket (since 0.53).
func TestThePricesAndTheCheaperChoicesGo(t *testing.T) {
	ctx := context.Background()
	d := dbtest.New(t, false)
	for _, q := range []string{
		`INSERT INTO settings (project, doc, updated_by) VALUES (NULL, '{"version": 1, "prices": [{"pattern": "gemini-*", "in": 1, "out": 2, "cached": 0}],
			"agents": {"auto": {"cheaper_share": 0.15, "same_points": 4}}}', 'test')
			ON CONFLICT ((coalesce(project::text, 'machine'))) DO UPDATE SET doc = EXCLUDED.doc`,
		`INSERT INTO model_choices (role, kind, reason, from_entry, from_effort, to_entry, to_effort, state) VALUES
			('builder', 'recommend', 'cheaper', 'agy/a', 'high', 'agy/b', 'high', 'open'),
			('builder', 'recommend', 'better', 'agy/a', 'high', 'agy/c', 'high', 'open'),
			('reviewer', 'change', 'cheaper', 'agy/a', 'high', 'agy/b', 'high', 'watching')`,
		`INSERT INTO improvements (number, title, status, target, patch, measure) VALUES
			(1, 'x', 'applied', '{baseline/stages/in_progress.md}', '{}', 'cost_per_ticket'),
			(2, 'y', 'applied', '{baseline/stages/in_progress.md}', '{}', 'first_request_pass_rate')`,
	} {
		if _, err := d.Server.Exec(ctx, q); err != nil {
			t.Fatal(err)
		}
	}
	updates := 0
	for _, line := range strings.Split(step(t, 63), "\n") {
		if strings.HasPrefix(line, "UPDATE ") {
			if _, err := d.Server.Exec(ctx, line); err != nil {
				t.Fatal(err)
			}
			updates++
		}
	}
	if updates != 4 {
		t.Fatalf("%d updates in step 63", updates)
	}
	var measures string
	d.Server.QueryRow(ctx, `SELECT string_agg(measure, ' ' ORDER BY number) FROM improvements`).Scan(&measures)
	if measures != "tokens_per_ticket first_request_pass_rate" {
		t.Fatalf("the measures: %s", measures)
	}
	var machine string
	d.Server.QueryRow(ctx, `SELECT doc::text FROM settings WHERE project IS NULL`).Scan(&machine)
	if strings.Contains(machine, "prices") || strings.Contains(machine, "cheaper") || !strings.Contains(machine, `"same_points": 4`) {
		t.Fatalf("the machine's settings: %s", machine)
	}
	got := map[string]string{}
	rows, _ := d.Server.Query(ctx, `SELECT kind || ' ' || reason || ' ' || to_entry, state FROM model_choices`)
	for rows.Next() {
		var k, s string
		rows.Scan(&k, &s)
		got[k] = s
	}
	rows.Close()
	if got["recommend cheaper agy/b"] != "dismissed" || got["recommend better agy/c"] != "open" || got["change cheaper agy/b"] != "watching" {
		t.Fatalf("the choices: %v", got)
	}
}

// Step 64 adds bugs.in_confirmed (design 0.54): the ticket a bug the operator reported is in is the
// operator's word, confirmed; the one an agent named before is not, and no longer counts an escape.
func TestABugsTicketIsConfirmedForTheOperatorsReportsAlone(t *testing.T) {
	ctx := context.Background()
	d := dbtest.New(t, false)
	shop := "00000000-0000-4000-8000-0000000000a1"
	for _, q := range []string{
		`INSERT INTO projects (id, name, key, state, repo_source) VALUES ('` + shop + `', 'shop', 'SF', 'running', 'local')`,
		`INSERT INTO tickets (project, number, title, kind, stage, origin, created_by, position) VALUES ('` + shop + `', 1, 't', 'feature', 'done', 'plan', 'lead', 1)`,
		`INSERT INTO bugs (project, number, title, severity, found_by, in_ticket, in_confirmed) SELECT '` + shop + `', n, 'b', 'low', f, CASE WHEN n < 3 THEN t.id END, false
			FROM tickets t, (VALUES (1, 'operator'), (2, 'tester'), (3, 'operator')) AS v(n, f)`,
	} {
		if _, err := d.Server.Exec(ctx, q); err != nil {
			t.Fatal(err)
		}
	}
	sql := step(t, 64)
	if !strings.Contains(sql, "ADD COLUMN in_confirmed boolean NOT NULL DEFAULT false") || !strings.Contains(sql, "-- older versions read: yes") {
		t.Fatalf("step 64: %s", sql)
	}
	updates := 0
	for _, line := range strings.Split(sql, "\n") {
		if strings.HasPrefix(line, "UPDATE ") {
			if _, err := d.Server.Exec(ctx, line); err != nil {
				t.Fatal(err)
			}
			updates++
		}
	}
	var got string
	d.Server.QueryRow(ctx, `SELECT string_agg(number || ':' || in_confirmed, ' ' ORDER BY number) FROM bugs`).Scan(&got)
	if updates != 1 || got != "1:true 2:false 3:false" {
		t.Fatalf("%d updates; confirmed: %s", updates, got)
	}
}

// Step 65 pauses Crew's project, the one the machine's improvements.crew_project names, where it is
// Done (since 0.55, the operator's answer 2 of report 10: Crew's project is never Done), and records the
// pause as the operator's own, so that HoldLimits, which resumes a pause the backend made, never starts
// it. It touches no other project: not the gate's hidden one, not another Done one, and not Crew's
// project while it runs or is paused.
func TestCrewsProjectDoneIsPaused(t *testing.T) {
	ctx := context.Background()
	d := dbtest.New(t, false)
	const crew, gate, other = "c0000000-0000-4000-8000-0000000000e1", "c0000000-0000-4000-8000-0000000000e2", "c0000000-0000-4000-8000-0000000000e3"
	for _, q := range []string{
		`INSERT INTO projects (id, name, key, state, repo_source) VALUES ('` + crew + `', 'crew', 'CREW', 'done', 'local')`,
		`INSERT INTO projects (id, name, key, state, repo_source, hidden) VALUES ('` + gate + `', 'crew-gate', 'GATE', 'done', 'local', true)`,
		`INSERT INTO projects (id, name, key, state, repo_source) VALUES ('` + other + `', 'shop', 'SF', 'done', 'local')`,
		`INSERT INTO settings (project, doc, updated_by) VALUES (NULL, '{"improvements": {"crew_project": "crew"}}', 'test')`,
	} {
		if _, err := d.Server.Exec(ctx, q); err != nil {
			t.Fatal(err)
		}
	}
	state := func(id string) (s, reason string) {
		t.Helper()
		if err := d.Server.QueryRow(ctx, `SELECT state, coalesce(pause_reason, '') FROM projects WHERE id = $1`, id).Scan(&s, &reason); err != nil {
			t.Fatal(err)
		}
		return s, reason
	}
	if _, err := d.Server.Exec(ctx, step(t, 65)); err != nil {
		t.Fatal(err)
	}
	if s, reason := state(crew); s != "paused" || !strings.Contains(reason, "never Done") {
		t.Fatalf("Crew's project after step 65: %s %q", s, reason)
	}
	for _, id := range []string{gate, other} {
		if s, _ := state(id); s != "done" {
			t.Fatalf("another project was changed: %s", s)
		}
	}
	// The operator's pause, which no limit's resume undoes, made by the schema step: the record says
	// both (security review of 1863a0e..f8d57b2).
	var who, before, after, source string
	if err := d.Server.QueryRow(ctx, `SELECT who, before->>'state', after->>'state', coalesce(after->>'source', '') FROM changes WHERE tbl = 'projects' AND row_key = $1
		ORDER BY seq DESC LIMIT 1`, crew).Scan(&who, &before, &after, &source); err != nil || who != "operator" || before != "done" || after != "paused" ||
		source != "schema step 65" {
		t.Fatalf("the change recorded: %s %s %s %q %v", who, before, after, source, err)
	}
	// A Crew's project that is paused, or runs, is left as it is.
	if _, err := d.Server.Exec(ctx, `UPDATE projects SET state = 'running', pause_reason = NULL WHERE id = $1`, crew); err != nil {
		t.Fatal(err)
	}
	if _, err := d.Server.Exec(ctx, step(t, 65)); err != nil {
		t.Fatal(err)
	}
	if s, _ := state(crew); s != "running" {
		t.Fatalf("a running Crew's project was %s", s)
	}
}

// Step 67 gives a release its measure (10.7, since 0.57): a release that stopped running before the
// step makes no news when it is measured, since its past is not today's news; the running one, and one
// not installed yet, will (the cold read of the thirteenth session's plan).
func TestStep67MakesNoNewsOfThePast(t *testing.T) {
	ctx := context.Background()
	d := dbtest.New(t, true)
	conn := d.ConnectAs(t, db.RoleMigrate)
	steps, _ := db.Steps()
	if len(steps) < 67 || steps[66].Name != "0067_release_measure.sql" {
		t.Fatalf("step 67 is not the release's measure")
	}
	if _, err := db.MigrateSteps(ctx, conn, steps[:66]); err != nil {
		t.Fatal(err)
	}
	for _, r := range []struct{ version, state, installed string }{
		{"0.2.0", "kept", "now()"}, {"0.2.1", "removed", "now()"}, {"0.2.2", "running", "now()"}, {"0.2.3", "waiting", "NULL"}, {"0.2.4", "failed", "NULL"},
	} {
		if _, err := conn.Exec(ctx, `INSERT INTO releases (version, commit, manifest_sha256, manifest, built_at, kind, state, installed_at)
			VALUES ($1, 'c', 'm', '{}', now(), 'code', $2, `+r.installed+`)`, r.version, r.state); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := db.MigrateSteps(ctx, conn, steps[:67]); err != nil {
		t.Fatal(err)
	}
	rows, err := conn.Query(ctx, `SELECT version FROM releases WHERE measure_news AND measure IS NULL AND measured_at IS NULL ORDER BY version`)
	if err != nil {
		t.Fatal(err)
	}
	var news []string
	for rows.Next() {
		var v string
		rows.Scan(&v)
		news = append(news, v)
	}
	if strings.Join(news, " ") != "0.2.2 0.2.3 0.2.4" {
		t.Fatalf("the releases whose measure is news: %v", news)
	}
}

// Step 68 undoes the "not compared" that 0.2.26's first pass stored for itself before its install's
// request was done (design 0.57): a release whose first done install request names another release
// before it is measured again; one with no such request keeps its measure, and so does every other verdict.
func TestStep68MeasuresAgainAReleaseWronglyNotCompared(t *testing.T) {
	ctx := context.Background()
	d := dbtest.New(t, false)
	for _, q := range []string{
		`INSERT INTO releases (version, commit, manifest_sha256, manifest, built_at, kind, state, installed_at, measure, measured_at) VALUES
			('0.2.26', 'c', 'm', '{}', now(), 'code', 'running', now(), '{"verdict": "not_compared", "why": "Crew does not know what ran before it"}', now()),
			('0.2.13', 'c', 'm', '{}', now(), 'code', 'removed', now(), '{"verdict": "not_compared", "why": "Crew does not know what ran before it"}', now()),
			('0.2.12', 'c', 'm', '{}', now(), 'code', 'removed', now(), '{"verdict": "not_compared", "why": "Crew does not know what ran before it"}', now()),
			('0.2.25', 'c', 'm', '{}', now(), 'code', 'kept', now(), '{"verdict": "inconclusive", "before": "0.2.23"}', now()),
			('0.2.11', 'c', 'm', '{}', now(), 'code', 'removed', now(), '{"verdict": "not_compared"}', now()),
			('0.2.9', 'c', 'm', '{}', now(), 'code', 'removed', now(), '{"verdict": "not_compared"}', now()),
			('0.2.7', 'c', 'm', '{}', now(), 'code', 'removed', now(), '{"verdict": "not_compared"}', now()),
			('0.2.6', 'c', 'm', '{}', now(), 'code', 'removed', now(), '{"verdict": "not_compared"}', now())`,
		`INSERT INTO update_requests (id, version, from_version, action, asked_by, state, asked_at) VALUES
			(38, '0.2.26', '0.2.25', 'install', 'crew-ctl', 'done', now()), (12, '0.2.12', '0.2.12', 'install', 'crew-ctl', 'done', now()),
			(39, '0.2.25', '0.2.23', 'install', 'crew-ctl', 'done', now()),
			(5, '0.2.11', NULL, 'install', 'root', 'done', now()), (6, '0.2.11', '0.2.10', 'install', 'crew-ctl', 'done', now()),
			(7, '0.2.9', '0.2.8', 'install', 'crew-ctl', 'running', now()), (8, '0.2.7', '0.2.6', 'gate', 'crew-ctl', 'done', now()),
			(4, '0.2.6', '', 'install', 'root', 'done', now())`,
	} {
		if _, err := d.Server.Exec(ctx, q); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := d.Server.Exec(ctx, step(t, 68)); err != nil {
		t.Fatal(err)
	}
	rows, err := d.Server.Query(ctx, `SELECT version, measure IS NULL AND measured_at IS NULL FROM releases ORDER BY version`)
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]bool{}
	for rows.Next() {
		var v string
		var cleared bool
		rows.Scan(&v, &cleared)
		got[v] = cleared
	}
	// The first done install is what Measure reads (review of dba3540): 0.2.11's named none. A request
	// that is not done, or a gate's, names no release before.
	if !got["0.2.26"] || got["0.2.13"] || got["0.2.12"] || got["0.2.25"] || got["0.2.11"] || got["0.2.9"] || got["0.2.7"] || got["0.2.6"] {
		t.Fatalf("the measures cleared: %v", got)
	}
}
