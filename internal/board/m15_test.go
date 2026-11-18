package board_test

import (
	"encoding/json"
	"slices"
	"strings"
	"testing"

	"github.com/kesokaj/crew/internal/board"
	"github.com/kesokaj/crew/internal/settings"
)

// signalsOf returns the signals of a kind in a project, each as its detail.
func (e *env) signalsOf(p *board.Project, kind string) []map[string]any {
	e.t.Helper()
	rows, err := e.db.Server.Query(e.ctx, `SELECT detail, coalesce(crew_version, '') FROM signals WHERE project = $1 AND kind = $2 ORDER BY id`, p.ID, kind)
	if err != nil {
		e.t.Fatal(err)
	}
	defer rows.Close()
	var out []map[string]any
	for rows.Next() {
		var raw []byte
		var version string
		if err := rows.Scan(&raw, &version); err != nil {
			e.t.Fatal(err)
		}
		var d map[string]any
		json.Unmarshal(raw, &d)
		if version == "" {
			e.t.Errorf("a %s signal has no Crew version", kind)
		}
		out = append(out, d)
	}
	return out
}

func whats(ds []map[string]any) []string {
	var out []string
	for _, d := range ds {
		out = append(out, d["what"].(string))
	}
	return out
}

// toDone takes a ticket from the Backlog to Done, with the checks this package cannot run passing.
func (e *env) toDone(p *board.Project, key string) {
	e.t.Helper()
	e.reconcile(p)
	e.approve(p, key, board.Decision{Checks: []board.CheckSpec{{Kind: "cmd", Spec: "make test"}}, DocsReason: ptr("No page covers it.")})
	e.reconcile(p)
	tk := e.ticket(p, key)
	e.push(p, *tk.Branch, map[string]string{key + ".go": "package x\n"})
	prev := e.b.Checks
	e.b.Checks = scripted()
	e.reconcile(p)
	e.b.Checks = prev
	if tk := e.ticket(p, key); tk.Stage != "done" {
		e.t.Fatalf("%s is in %s: %v", key, tk.Stage, deref(tk.Waiting))
	}
}

// Every way the operator steps in is a signal, which counts most (10.2, since 0.49): a breakdown sent
// back, a ticket edited before the OK, a raised limit, a new feature discarded, a decision changed. A
// lowered limit, an edit after the OK, and a discard of a ticket that is no new feature are not.
func TestTheOperatorsInterventionsAreSignals(t *testing.T) {
	e := newEnv(t)
	p := e.project("shop", "SF")
	e.add(p, ready("Sign-in"), board.OriginPlan)
	if _, err := e.b.EditTicket(e.ctx, p, "SF-1", board.Edit{Title: ptr("Sign-in with a code")}, board.Operator); err != nil {
		t.Fatal(err)
	}
	if err := e.b.SendBack(e.ctx, p.Name, "Split the sign-in in two.", board.Operator); err != nil {
		t.Fatal(err)
	}
	e.ok(p)
	if _, err := e.b.EditTicket(e.ctx, p, "SF-1", board.Edit{Title: ptr("Sign-in with a mail")}, board.Operator); err != nil {
		t.Fatal(err)
	}
	// A copy: the effective settings share their groups with the defaults.
	cur, _ := e.b.Settings(e.ctx, e.db.Server, p)
	raw, _ := json.Marshal(cur)
	doc, err := settings.ParseJSON(raw)
	if err != nil {
		t.Fatal(err)
	}
	// A budget a document from before 0.53 still names is taken out, and raises nothing.
	doc["budget_usd"] = 200
	doc["limits"].(settings.Doc)["max_added_share"] = 0.3
	if err := e.b.SaveSettings(e.ctx, p, doc, board.Operator, nil); err != nil {
		t.Fatal(err)
	}
	doc["budget_usd"] = 300
	doc["limits"].(settings.Doc)["max_fix_rounds"] = 5
	doc["limits"].(settings.Doc)["run_max_turns"] = 100
	if err := e.b.SaveSettings(e.ctx, p, doc, board.Operator, nil); err != nil {
		t.Fatal(err)
	}

	// A new feature discarded during its notice; a created ticket the operator discards before analysis
	// is not one.
	for _, title := range []string{"Dark mode", "Stray"} {
		n := ready(title)
		n.Seen = ptr("seen in the cart")
		e.add(p, n, board.OriginAgent)
	}
	e.reconcile(p)
	if _, err := e.b.Decide(e.ctx, p, "SF-2", board.Decision{Choice: "notice", Reason: "Not in the plan."}, "lead"); err != nil {
		t.Fatal(err)
	}
	if _, err := e.b.NoticeDecision(e.ctx, p, "SF-2", "discard", "", board.Operator); err != nil {
		t.Fatal(err)
	}
	if _, err := e.b.Discard(e.ctx, p, "SF-3", "not needed", board.Operator); err != nil {
		t.Fatal(err)
	}

	// A decision an agent made, changed.
	var id int64
	if err := e.db.Server.QueryRow(e.ctx, `INSERT INTO decisions (project, ticket, role, question, choice, reason)
		VALUES ($1, (SELECT id FROM tickets WHERE project = $1 AND number = 1), 'builder', 'Which hash?', 'sha1', 'fast') RETURNING id`, p.ID).Scan(&id); err != nil {
		t.Fatal(err)
	}
	if _, err := e.b.ChangeDecision(e.ctx, p, id, "argon2id", board.Operator); err != nil {
		t.Fatal(err)
	}

	got := e.signalsOf(p, "operator_intervention")
	want := []string{board.EditedBeforeOK, board.SentBack, board.LimitRaised, board.LimitRaised, board.FeatureDiscard, board.DecisionChanged}
	if w := whats(got); !slices.Equal(w, want) {
		t.Fatalf("interventions %v, want %v", w, want)
	}
	if got[2]["key"] != "limits.max_added_share" || got[2]["text"] != "Raised limits.max_added_share from 0.25 to 0.3" || got[3]["key"] != "limits.max_fix_rounds" {
		t.Fatalf("raised limits: %v %v", got[2], got[3])
	}
	if got[5]["role"] != "builder" || got[5]["text"] != "Changed a decision an agent made" {
		t.Fatalf("decision changed: %v", got[5])
	}
}

// A bug in a ticket that is Done is an escaped defect: the review and the test both missed it (10.2),
// once its ticket is confirmed: the operator's word is, an agent's waits for the analysis of its fix
// ticket (since 0.54; bugticket_test.go). A lesson in memory is a signal, and other kinds of entry are
// not; documentation that falls behind the code is one, once until it is current again.
func TestEscapedDefectsLessonsAndPagesBehindAreSignals(t *testing.T) {
	e := newEnv(t)
	p := e.running("shop", "SF")
	e.toDone(p, "SF-1")
	e.add(p, ready("Second"), board.OriginOperator)
	if _, _, err := e.b.ReportBug(e.ctx, p, board.NewBug{Title: "Receipts lose the cents", Severity: "high", Happens: "1.5 shows as 1",
		InTicket: "SF-1"}, board.BugFound{By: board.Operator}, board.OriginOperator, board.Operator); err != nil {
		t.Fatal(err)
	}
	if _, _, err := e.b.ReportBug(e.ctx, p, board.NewBug{Title: "A bug of nothing Done", Severity: "low", Happens: "x"},
		board.BugFound{By: "tester"}, board.OriginAgent, "tester"); err != nil {
		t.Fatal(err)
	}
	esc := e.signalsOf(p, "escaped_defect")
	if len(esc) != 1 || esc[0]["severity"] != "high" || esc[0]["foundBy"] != board.Operator {
		t.Fatalf("escaped defects %v", esc)
	}
	var ticket int64
	e.db.Server.QueryRow(e.ctx, `SELECT ticket FROM signals WHERE kind = 'escaped_defect'`).Scan(&ticket)
	if ticket != e.ticket(p, "SF-1").ID {
		t.Fatal("the escaped defect does not name the Done ticket")
	}

	for _, n := range []board.NewEntry{{Kind: "lesson", Title: "Money is cents", Keywords: []string{"money"}}, {Kind: "fact", Title: "A fake mailer"}} {
		if _, err := e.b.Remember(e.ctx, p, n, board.MemorySource{}, "builder"); err != nil {
			t.Fatal(err)
		}
	}
	if l := e.signalsOf(p, "lesson_recorded"); len(l) != 1 || l[0]["text"] != "Money is cents" {
		t.Fatalf("lessons %v", l)
	}

	e.commit(p, "docs/mail.md", "---\ncovers: [internal/mail/**]\n---\n# Mail\n", "SF-1")
	e.b.IndexDocs(e.ctx, p)
	e.commit(p, "internal/mail/send.go", "package mail\n", "SF-2")
	e.b.IndexDocs(e.ctx, p)
	e.commit(p, "internal/mail/more.go", "package mail\n", "SF-2")
	e.b.IndexDocs(e.ctx, p)
	behind := e.signalsOf(p, "docs_behind")
	if len(behind) != 1 || behind[0]["text"] != "docs/mail.md is behind SF-2" {
		t.Fatalf("pages behind %v", behind)
	}
}

// A project's override of the baseline (9.6, 10.3, since 0.49): the lead's goes to the evals, a checking
// role's or the prompt writer's waits for the operator, the operator's applies at once and replaces
// the one before, a newer proposal withdraws one not yet decided, and the text applies in the project
// only, with its source named.
func TestOverridesOfTheBaseline(t *testing.T) {
	e := newEnv(t)
	p := e.running("shop", "SF")
	other := e.running("mail", "ML")
	lead := "lead · 00000000-0000-4000-8000-000000000001"
	for _, bad := range []struct{ path, text string }{{"tools.yaml", "x"}, {"agents/nobody.md", "x"}, {"stages/in_progress.md", " "}} {
		if _, err := e.b.ProposeOverride(e.ctx, p, bad.path, bad.text, "", lead, ""); kindOf(err) != board.Invalid {
			t.Errorf("%s: %v", bad.path, err)
		}
	}
	cur, _, _ := board.BaselineText(e.ctx, e.db.Server, p.ID, "stages/in_progress.md", "1.0.0")
	if _, err := e.b.ProposeOverride(e.ctx, p, "stages/in_progress.md", cur, "", lead, ""); kindOf(err) != board.Invalid {
		t.Errorf("the text that applies already: %v", err)
	}
	o1, err := e.b.ProposeOverride(e.ctx, p, "stages/in_progress.md", "Run the checks first.\n", "Many check rounds.", lead, "")
	if err != nil || o1.Status != board.OverrideTesting || o1.Version != 1 || o1.Number != 1 {
		t.Fatalf("the lead's: %+v %v", o1, err)
	}
	o2, err := e.b.ProposeOverride(e.ctx, p, "stages/in_progress.md", "Run the checks before asking.\n", "", lead, "")
	if err != nil || o2.Version != 2 {
		t.Fatalf("a newer version: %+v %v", o2, err)
	}
	if o, _ := e.b.Override(e.ctx, p, 1); o.Status != board.OverrideWithdrawn {
		t.Fatalf("the older proposal is %s", o.Status)
	}
	rev, err := e.b.ProposeOverride(e.ctx, p, "agents/reviewer.md", "Review harder.\n", "", lead, "")
	if err != nil || rev.Status != board.OverrideWaiting || !rev.Waits {
		t.Fatalf("a checking role's: %+v %v", rev, err)
	}
	pw, _ := e.b.ProposeOverride(e.ctx, p, "agents/prompt-writer.md", "Shorter.\n", "", lead, "")
	if pw.Status != board.OverrideWaiting {
		t.Fatalf("the prompt writer's: %s", pw.Status)
	}
	// Until something applies, a run reads the baseline.
	if text, source, _ := board.BaselineText(e.ctx, e.db.Server, p.ID, "stages/in_progress.md", "1.0.0"); text != cur || source != "baseline/stages/in_progress.md@1.0.0" {
		t.Fatalf("before: %q", source)
	}
	if err := e.b.DecideByEvals(e.ctx, o2.ID, 0, "too_few", map[string]any{"verdict": "too_few"}); err != nil {
		t.Fatal(err)
	}
	if o, _ := e.b.Override(e.ctx, p, o2.Number); o.Status != board.OverrideTesting {
		t.Fatalf("too few cases: %s", o.Status)
	}
	if err := e.b.DecideByEvals(e.ctx, o2.ID, 0, "not_worse", map[string]any{"verdict": "not_worse", "margin": 0.1}); err != nil {
		t.Fatal(err)
	}
	o, _ := e.b.Override(e.ctx, p, o2.Number)
	if o.Status != board.OverrideApplied || o.AppliedAt == nil || deref(o.DecidedBy) != "evals" || !strings.Contains(string(o.Eval), "not_worse") {
		t.Fatalf("not worse: %+v", o)
	}
	text, source, _ := board.BaselineText(e.ctx, e.db.Server, p.ID, "stages/in_progress.md", "1.0.0")
	if text != "Run the checks before asking.\n" || source != "override o_2 v2 of baseline/stages/in_progress.md@1.0.0" {
		t.Fatalf("after: %q %q", text, source)
	}
	if text, _, _ := board.BaselineText(e.ctx, e.db.Server, other.ID, "stages/in_progress.md", "1.0.0"); text != cur {
		t.Fatal("another project reads the override")
	}
	// The operator's own applies at once, in place of the one before; and can be taken back.
	mine, err := e.b.ProposeOverride(e.ctx, p, "stages/in_progress.md", "My own words.\n", "", board.Operator, "")
	if err != nil || mine.Status != board.OverrideApplied {
		t.Fatalf("the operator's: %+v %v", mine, err)
	}
	if o, _ := e.b.Override(e.ctx, p, o2.Number); o.Status != board.OverrideReplaced {
		t.Fatalf("the one before is %s", o.Status)
	}
	if _, err := e.b.DecideOverride(e.ctx, p, mine.Number, "refuse", "", board.Operator); kindOf(err) != board.Refused {
		t.Fatalf("refusing an applied one: %v", err)
	}
	if o, err := e.b.DecideOverride(e.ctx, p, mine.Number, "take_back", "", board.Operator); err != nil || o.Status != board.OverrideTakenBack {
		t.Fatalf("take back: %+v %v", o, err)
	}
	if text, _, _ := board.BaselineText(e.ctx, e.db.Server, p.ID, "stages/in_progress.md", "1.0.0"); text != cur {
		t.Fatal("a taken back override still applies")
	}
	// The operator answers one that waits.
	if o, err := e.b.DecideOverride(e.ctx, p, rev.Number, "apply", "", board.Operator); err != nil || o.Status != board.OverrideApplied {
		t.Fatalf("apply: %+v %v", o, err)
	}
	if o, err := e.b.DecideOverride(e.ctx, p, pw.Number, "refuse", "not now", board.Operator); err != nil || o.Status != board.OverrideRefused || deref(o.DecidedWhy) != "not now" {
		t.Fatalf("refuse: %+v %v", o, err)
	}
	// Crew takes back one that measured worse, with the measure kept.
	if err := e.b.TakeBack(e.ctx, rev.ID, "it measured worse", map[string]any{"verdict": "worse"}); err != nil {
		t.Fatal(err)
	}
	if o, _ := e.b.Override(e.ctx, p, rev.Number); o.Status != board.OverrideTakenBack || deref(o.DecidedBy) != "crew" || !strings.Contains(string(o.Measure), "worse") {
		t.Fatalf("taken back by Crew: %+v", o)
	}
	var whats []string
	for _, d := range e.signalsOf(p, "override") {
		whats = append(whats, d["what"].(string))
	}
	if !slices.Equal(whats, []string{"applied", "applied", "taken_back", "applied", "taken_back"}) {
		t.Fatalf("override signals %v", whats)
	}
	// A file no eval runs, and the E2E tester's, wait for the operator: the evals cannot measure them
	// (security review of M15).
	for _, path := range []string{"guides/planning.md", "agents/curator.md", "stages/analysis.md", "agents/e2e.md"} {
		if o, err := e.b.ProposeOverride(e.ctx, p, path, "Changed.\n", "", lead, ""); err != nil || o.Status != board.OverrideWaiting {
			t.Errorf("%s: %+v %v", path, o, err)
		}
	}
	// With approvals.agent_change operator every override waits.
	raw, _ := json.Marshal(func() settings.Doc { d, _ := e.b.Settings(e.ctx, e.db.Server, other); return d }())
	doc, _ := settings.ParseJSON(raw)
	doc["approvals"].(settings.Doc)["agent_change"] = "operator"
	if err := e.b.SaveSettings(e.ctx, other, doc, board.Operator, nil); err != nil {
		t.Fatal(err)
	}
	if o, _ := e.b.ProposeOverride(e.ctx, other, "guides/breakdown.md", "Smaller tickets.\n", "", lead, ""); o == nil || o.Status != board.OverrideWaiting {
		t.Fatalf("with agent_change operator: %+v", o)
	}
}
