package db_test

import (
	"context"
	"strings"
	"testing"

	"github.com/kesokaj/crew/internal/db"
	"github.com/kesokaj/crew/internal/db/dbtest"
)

// Step 76 applies on a database of step 75. A conversation is a run's, the proof's, the operator's or a
// ticket's chat; the operator's names no ticket, a chat names its project and ticket, and a ticket has
// one chat. A message waits with its text or its quote, and goes with its conversation (11.8, 11.9).
func TestStep76KeepsConversationsApart(t *testing.T) {
	ctx := context.Background()
	d := dbtest.New(t, true)
	conn := d.ConnectAs(t, db.RoleMigrate)
	steps, _ := db.Steps()
	if len(steps) < 76 || steps[75].Name != "0076_conversations.sql" {
		t.Fatalf("step 76 is not the conversations")
	}
	if _, err := db.MigrateSteps(ctx, conn, steps[:75]); err != nil {
		t.Fatal(err)
	}
	// A conversation of a purpose an older version wrote stays readable after the step.
	if _, err := conn.Exec(ctx, `INSERT INTO conversations (id, purpose) VALUES ('00000000-0000-4000-8000-0000000000a1', 'proof')`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.MigrateSteps(ctx, conn, steps[:76]); err != nil {
		t.Fatal(err)
	}
	exec := func(sql string, args ...any) error {
		_, err := conn.Exec(ctx, sql, args...)
		return err
	}
	var p string
	var tk, tk2 int64
	if err := conn.QueryRow(ctx, `INSERT INTO projects (id, name, key, state, repo_source) VALUES (gen_random_uuid(), 'shop', 'SH', 'running', 'local') RETURNING id`).Scan(&p); err != nil {
		t.Fatal(err)
	}
	for i, id := range []*int64{&tk, &tk2} {
		if err := conn.QueryRow(ctx, `INSERT INTO tickets (project, number, title, kind, why, what, goal, stage, origin, created_by, position)
			VALUES ($1, $2::int, 'T', 'feature', 'w', 'w', 'g', 'backlog', 'operator', 'operator', $2::int) RETURNING id`, p, i+1).Scan(id); err != nil {
			t.Fatal(err)
		}
	}
	if err := exec(`INSERT INTO conversations (id, purpose) VALUES (gen_random_uuid(), 'talk')`); err == nil {
		t.Fatal("a purpose that does not exist was taken")
	}
	if err := exec(`INSERT INTO conversations (id, project, ticket, purpose) VALUES (gen_random_uuid(), $1, $2, 'operator')`, p, tk); err == nil {
		t.Fatal("an operator's conversation that names a ticket was taken")
	}
	if err := exec(`INSERT INTO conversations (id, project, purpose) VALUES (gen_random_uuid(), $1, 'chat')`, p); err == nil {
		t.Fatal("a chat of no ticket was taken")
	}
	if err := exec(`INSERT INTO conversations (id, purpose) VALUES ('00000000-0000-4000-8000-0000000000a2', 'operator')`); err != nil {
		t.Fatalf("a conversation about no project: %v", err)
	}
	if err := exec(`INSERT INTO conversations (id, project, ticket, purpose) VALUES (gen_random_uuid(), $1, $2, 'chat')`, p, tk); err != nil {
		t.Fatalf("a ticket's chat: %v", err)
	}
	if err := exec(`INSERT INTO conversations (id, project, ticket, purpose) VALUES (gen_random_uuid(), $1, $2, 'chat')`, p, tk); err == nil {
		t.Fatal("a second chat of one ticket was taken")
	}
	if err := exec(`INSERT INTO conversations (id, project, ticket, purpose) VALUES (gen_random_uuid(), $1, $2, 'chat')`, p, tk2); err != nil {
		t.Fatalf("another ticket's chat: %v", err)
	}
	const c = "00000000-0000-4000-8000-0000000000a2"
	if err := exec(`INSERT INTO conversation_messages (conversation, text) VALUES ($1, '')`, c); err == nil {
		t.Fatal("a message with no text and no quote was taken")
	}
	if err := exec(`INSERT INTO conversation_messages (conversation, text, quote, source) VALUES ($1, '', 'marked', '{"part": "log"}')`, c); err != nil {
		t.Fatalf("a quote alone: %v", err)
	}
	if err := exec(`INSERT INTO work_folders (conversation, gzip, sha256, bytes, files) VALUES ($1, '\x00', 'x', 209715201, 1)`, c); err == nil {
		t.Fatal("a work folder over 200 MB was taken")
	}
	if err := exec(`INSERT INTO work_folders (conversation, gzip, sha256, bytes, files) VALUES ($1, '\x00', 'x', 10, 1)`, c); err != nil {
		t.Fatal(err)
	}
}

// Deleting an operator's conversation removes it and everything of it, and nothing else; one with a
// run that has not ended, or a chat, is refused (11.8, plan M20 D22).
func TestDeletingAConversationRemovesAllOfIt(t *testing.T) {
	ctx := context.Background()
	d := dbtest.New(t, false)
	exec := func(sql string, args ...any) {
		t.Helper()
		if _, err := d.Server.Exec(ctx, sql, args...); err != nil {
			t.Fatalf("%s: %v", sql, err)
		}
	}
	count := func(sql string, args ...any) int {
		t.Helper()
		var n int
		if err := d.Server.QueryRow(ctx, sql, args...).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}
	// Two conversations about no project, each with a subtask, a run, its turns, events, CLI files, a
	// message and a work folder; the first is deleted.
	for i, c := range []string{"00000000-0000-4000-8000-0000000000c1", "00000000-0000-4000-8000-0000000000c2"} {
		var sub int64
		if err := d.Server.QueryRow(ctx, `INSERT INTO subtasks (scope, number, kind, role, state) VALUES ('conversation', 1, 'talk', 'assistant', 'done') RETURNING id`).Scan(&sub); err != nil {
			t.Fatal(err)
		}
		run := "00000000-0000-4000-8000-0000000000d" + string(rune('1'+i))
		exec(`INSERT INTO conversations (id, purpose, subtask, cli) VALUES ($1, 'operator', $2, 'agy')`, c, sub)
		exec(`INSERT INTO runs (id, subtask, number, state, cli, model, crew_version, image, conversation) VALUES ($1, $2, 1, 'ended', 'agy', 'm', 't', 't', $3)`, run, sub, c)
		exec(`INSERT INTO turns (conversation, number, run, speaker, text) VALUES ($1, 1, $2, 'agent', 'hi')`, c, run)
		exec(`INSERT INTO events (run, conversation, seq, at, type) VALUES ($1, $2, 1, now(), 'status')`, run, c)
		exec(`INSERT INTO cli_files (conversation, path, gzip, sha256, keep_until) VALUES ($1, 'f', '\x00', 'x', now())`, c)
		exec(`INSERT INTO run_prompts (run, gzip) VALUES ($1, '\x00')`, run)
		exec(`INSERT INTO conversation_messages (conversation, text, run) VALUES ($1, 'hello', $2)`, c, run)
		exec(`INSERT INTO work_folders (conversation, gzip, sha256, bytes, files) VALUES ($1, '\x00', 'x', 1, 1)`, c)
	}
	exec(`SELECT crew_delete_conversation('00000000-0000-4000-8000-0000000000c1')`)
	for table, where := range map[string]string{
		"conversations": "id", "turns": "conversation", "events": "conversation", "cli_files": "conversation",
		"conversation_messages": "conversation", "work_folders": "conversation", "runs": "conversation",
	} {
		if n := count(`SELECT count(*) FROM ` + table + ` WHERE ` + where + ` = '00000000-0000-4000-8000-0000000000c1'`); n != 0 {
			t.Errorf("%d rows of %s are left", n, table)
		}
		if n := count(`SELECT count(*) FROM ` + table + ` WHERE ` + where + ` = '00000000-0000-4000-8000-0000000000c2'`); n == 0 {
			t.Errorf("the other conversation's %s went too", table)
		}
	}
	if n := count(`SELECT count(*) FROM subtasks WHERE scope = 'conversation'`); n != 1 {
		t.Errorf("%d conversation subtasks, want the other one", n)
	}
	if n := count(`SELECT count(*) FROM run_prompts`); n != 1 {
		t.Errorf("%d prompts, want the other run's", n)
	}
	// A run that has not ended stops it.
	exec(`UPDATE runs SET state = 'idle' WHERE conversation = '00000000-0000-4000-8000-0000000000c2'`)
	if _, err := d.Server.Exec(ctx, `SELECT crew_delete_conversation('00000000-0000-4000-8000-0000000000c2')`); err == nil || !strings.Contains(err.Error(), "close it first") {
		t.Fatalf("a conversation with a run that has not ended: %v", err)
	}
	// A chat goes with its ticket's project, not on its own.
	var p string
	var tk int64
	if err := d.Server.QueryRow(ctx, `INSERT INTO projects (id, name, key, state, repo_source) VALUES (gen_random_uuid(), 'shop', 'SH', 'archived', 'local') RETURNING id`).Scan(&p); err != nil {
		t.Fatal(err)
	}
	if err := d.Server.QueryRow(ctx, `INSERT INTO tickets (project, number, title, kind, why, what, goal, stage, origin, created_by, position)
		VALUES ($1, 1, 'T', 'feature', 'w', 'w', 'g', 'done', 'operator', 'operator', 1) RETURNING id`, p).Scan(&tk); err != nil {
		t.Fatal(err)
	}
	exec(`INSERT INTO conversations (id, project, ticket, purpose) VALUES ('00000000-0000-4000-8000-0000000000c3', $1, $2, 'chat')`, p, tk)
	exec(`INSERT INTO conversation_messages (conversation, text) VALUES ('00000000-0000-4000-8000-0000000000c3', 'why?')`)
	exec(`INSERT INTO conversations (id, project, purpose) VALUES ('00000000-0000-4000-8000-0000000000c4', $1, 'operator')`, p)
	exec(`INSERT INTO work_folders (conversation, gzip, sha256, bytes, files) VALUES ('00000000-0000-4000-8000-0000000000c4', '\x00', 'x', 1, 1)`)
	exec(`UPDATE tickets SET conversation = '00000000-0000-4000-8000-0000000000c4' WHERE id = $1`, tk)
	if _, err := d.Server.Exec(ctx, `SELECT crew_delete_conversation('00000000-0000-4000-8000-0000000000c3')`); err == nil {
		t.Fatal("a chat was deleted on its own")
	}
	exec(`SELECT crew_delete_project($1, 'operator')`, p)
	if n := count(`SELECT count(*) FROM conversations WHERE id IN ('00000000-0000-4000-8000-0000000000c3', '00000000-0000-4000-8000-0000000000c4')`); n != 0 {
		t.Errorf("%d of the project's conversations are left", n)
	}
	if n := count(`SELECT count(*) FROM conversation_messages WHERE conversation = '00000000-0000-4000-8000-0000000000c3'`) +
		count(`SELECT count(*) FROM work_folders WHERE conversation = '00000000-0000-4000-8000-0000000000c4'`); n != 0 {
		t.Errorf("%d messages and folders of the project's conversations are left", n)
	}
}

// Every table that refers to a run, a subtask or a conversation is named in the function that deletes
// a conversation, or lets go of the row by itself, so a table added later cannot be forgotten there.
func TestEveryTableOfAConversationIsNamedWhereItIsDeleted(t *testing.T) {
	ctx := context.Background()
	d := dbtest.New(t, false)
	var body string
	if err := d.Server.QueryRow(ctx, `SELECT pg_get_functiondef('crew_delete_conversation(uuid)'::regprocedure)`).Scan(&body); err != nil {
		t.Fatal(err)
	}
	rows, err := d.Server.Query(ctx, `SELECT DISTINCT c.conrelid::regclass::text FROM pg_constraint c
		WHERE c.contype = 'f' AND c.confrelid IN ('runs'::regclass, 'subtasks'::regclass, 'conversations'::regclass)
			AND c.confdeltype NOT IN ('c', 'n') ORDER BY 1`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	n := 0
	for rows.Next() {
		var table string
		if err := rows.Scan(&table); err != nil {
			t.Fatal(err)
		}
		n++
		if !strings.Contains(body, "FROM "+table+" WHERE") && !strings.Contains(body, "UPDATE "+table+" SET") {
			t.Errorf("crew_delete_conversation does not let go of the rows of %s", table)
		}
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	if n < 25 {
		t.Fatalf("found %d tables that refer to a run, a subtask or a conversation; the query finds too few", n)
	}
}

// Step 83 marks Crew's project (named by improvements.crew_project) protected, archives the M4
// fixture project when Crew's project or crew-gate exists while leaving a bare test database's
// fixture running, and updates crew_delete_project to refuse deleting Crew's project or any
// protected project (design 1.34, 7.13, 16.2).
func TestStep83ProtectsCrewsProjectArchivesFixtureAndRefusesDeletingProtected(t *testing.T) {
	ctx := context.Background()
	d := dbtest.New(t, false)
	const fixture = "f1c50000-0000-4000-8000-000000000001"
	const crew = "c0000000-0000-4000-8000-000000000083"
	const shop = "c0000000-0000-4000-8000-000000000084"
	// In a bare database without Crew's project or crew-gate, fixture stays running.
	var fixState string
	if err := d.Server.QueryRow(ctx, `SELECT state FROM projects WHERE id = $1`, fixture).Scan(&fixState); err != nil || fixState != "running" {
		t.Fatalf("bare fixture state: %q, %v", fixState, err)
	}
	for _, q := range []string{
		`INSERT INTO projects (id, name, key, state, repo_source) VALUES ('` + crew + `', 'crew', 'CREW', 'paused', 'local')`,
		`INSERT INTO projects (id, name, key, state, repo_source) VALUES ('` + shop + `', 'shop', 'SF', 'archived', 'local')`,
		`INSERT INTO settings (project, doc, updated_by) VALUES (NULL, '{"improvements": {"crew_project": "crew"}}', 'test')`,
	} {
		if _, err := d.Server.Exec(ctx, q); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := d.ConnectAs(t, db.RoleMigrate).Exec(ctx, step(t, 83)); err != nil {
		t.Fatal(err)
	}
	var crewProt, shopProt bool
	if err := d.Server.QueryRow(ctx, `SELECT (SELECT protected FROM projects WHERE id = $1), (SELECT protected FROM projects WHERE id = $2), (SELECT state FROM projects WHERE id = $3)`,
		crew, shop, fixture).Scan(&crewProt, &shopProt, &fixState); err != nil {
		t.Fatal(err)
	}
	if !crewProt || shopProt || fixState != "archived" {
		t.Fatalf("after step 83: crewProt=%v shopProt=%v fixState=%q", crewProt, shopProt, fixState)
	}
	var src string
	if err := d.Server.QueryRow(ctx, `SELECT coalesce(after->>'source', '') FROM changes WHERE tbl = 'projects' AND row_key = $1 ORDER BY seq DESC LIMIT 1`, crew).Scan(&src); err != nil || src != "schema step 83" {
		t.Fatalf("change source for crew: %q, %v", src, err)
	}
	// crew_delete_project refuses Crew's project and any protected project, even if archived.
	if _, err := d.Server.Exec(ctx, `UPDATE projects SET state = 'archived', protected = false WHERE id = $1`, crew); err != nil {
		t.Fatal(err)
	}
	if _, err := d.Server.Exec(ctx, `SELECT crew_delete_project($1, 'operator')`, crew); err == nil || !strings.Contains(err.Error(), "Crew's own") {
		t.Fatalf("crew_delete_project on Crew's project: %v", err)
	}
	if _, err := d.Server.Exec(ctx, `UPDATE projects SET protected = true WHERE id = $1`, shop); err != nil {
		t.Fatal(err)
	}
	if _, err := d.Server.Exec(ctx, `SELECT crew_delete_project($1, 'operator')`, shop); err == nil || !strings.Contains(err.Error(), "is protected") {
		t.Fatalf("crew_delete_project on protected shop: %v", err)
	}
}
