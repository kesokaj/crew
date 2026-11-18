package settings_test

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/kesokaj/crew/internal/db"
	"github.com/kesokaj/crew/internal/db/dbtest"
	"github.com/kesokaj/crew/internal/settings"
)

// A project's defaults are the listing of board.yaml in section 14.1, as it is there.
func TestProjectDefaultsAreTheDesignsListing(t *testing.T) {
	_, here, _, _ := runtime.Caller(0)
	tdd, err := os.ReadFile(filepath.Join(filepath.Dir(here), "..", "..", "docs", "plan", "tdd", "14-settings.md"))
	if err != nil {
		t.Fatal(err)
	}
	const head = "`board.yaml`:\n\n```yaml\n"
	a := strings.Index(string(tdd), head)
	if a < 0 {
		t.Fatal("the design has no board.yaml listing")
	}
	listing := string(tdd)[a+len(head):]
	listing = listing[:strings.Index(listing, "```")]
	ours, err := os.ReadFile("board.yaml")
	if err != nil {
		t.Fatal(err)
	}
	if string(ours) != listing {
		t.Fatal("internal/settings/board.yaml differs from the listing in section 14.1 of the design")
	}
}

func TestProjectDefaultsValidate(t *testing.T) {
	d, err := settings.ProjectDefaults("shop", "SF")
	if err != nil {
		t.Fatal(err)
	}
	if _, budget := d["budget_usd"]; d["project"] != "shop" || d["key"] != "SF" || budget || len(d["agents"].(settings.Doc)) != 0 {
		t.Fatalf("defaults = %v %v %v %v", d["project"], d["key"], d["budget_usd"], d["agents"])
	}
	if err := settings.ValidateProject(d, d); err != nil {
		t.Fatalf("the defaults do not validate: %v", err)
	}
	stages := settings.ParseStages(d)
	var on []string
	for _, s := range stages {
		if s.On {
			on = append(on, s.ID)
		}
	}
	if got := strings.Join(on, " "); got != "planning backlog analysis in_progress review test done" {
		t.Fatalf("stages on = %s", got)
	}
}

// replaceStages gives the defaults another list of stages, written as a file would.
func withStages(t *testing.T, stages string) settings.Doc {
	t.Helper()
	d, err := settings.Parse([]byte("version: 1\nstages:\n" + stages))
	if err != nil {
		t.Fatal(err)
	}
	return d
}

const fixedHead = `  - id: planning
  - id: backlog
  - {id: analysis, role: lead, wip: 2, exit: [has_why_what_goal, has_check, analysis_approved]}
  - {id: in_progress, role: builder, wip: 3, enter: [dependencies_done], exit: [branch_pushed]}
`
const optionalTail = `  - {id: review, optional: true, on: true, role: reviewer, wip: 2, exit: [review_verdict_approve]}
  - {id: test, optional: true, on: false, role: tester, wip: 2, exit: [test_result_pass]}
  - {id: e2e, optional: true, on: false, role: e2e, wip: 1, exit: [e2e_result_pass]}
  - {id: verification, optional: true, on: false, role: verifier, wip: 1, exit: [verify_pass]}
  - {id: done, enter: [merges_and_passes_on_main]}
`

// ownStage is a project's own stage, as a settings file holds it.
const ownStage = `  - {id: security, title: Security, optional: true, on: true, role: security, wip: 1, agent: .agents/agents/security.md, prompt: Find secrets., exit: [stage_verdict_pass, "cmd: make audit", "judge: No secret is logged."]}
`

func TestProjectValidateRefuses(t *testing.T) {
	defaults, _ := settings.ProjectDefaults("shop", "SF")
	cases := map[string]settings.Doc{
		"a fixed stage switched off": withStages(t, strings.Replace(fixedHead, "role: lead,", "role: lead, on: false,", 1)+optionalTail),
		"done not last":              withStages(t, fixedHead+"  - {id: done}\n"+optionalTail[:strings.Index(optionalTail, "  - {id: done")]),
		"review left out":            withStages(t, fixedHead+optionalTail[strings.Index(optionalTail, "  - {id: test"):]),
		"a check that is not one":    withStages(t, fixedHead+strings.Replace(optionalTail, "review_verdict_approve", "looks_good", 1)),
		"a stage twice":              withStages(t, fixedHead+strings.Replace(optionalTail, "id: e2e", "id: review", 1)),
		"no wip":                     withStages(t, fixedHead+strings.Replace(optionalTail, "wip: 2, exit: [review", "wip: 0, exit: [review", 1)),
		"backlog with an agent":      withStages(t, strings.Replace(fixedHead, "id: backlog", "{id: backlog, role: lead}", 1)+optionalTail),
		"a custom stage not optional": withStages(t, fixedHead+strings.Replace(optionalTail, "  - {id: done",
			"  - {id: security, role: security, wip: 1, exit: [\"cmd: make audit\"]}\n  - {id: done", 1)),
	}
	// A project's own stage needs its agent on main, a prompt, and an exit Crew answers (7.4).
	for name, from := range map[string][2]string{
		"no agent":                    {`agent: .agents/agents/security.md, `, ``},
		"an agent outside its folder": {`.agents/agents/security.md`, `../security.md`},
		"an agent that is not a page": {`.agents/agents/security.md`, `.agents/hooks.json`},
		"no prompt":                   {`prompt: Find secrets., `, ``},
		"no verdict in its exit":      {`stage_verdict_pass, `, ``},
		"a check nobody answers":      {`stage_verdict_pass`, `stage_verdict_pass, looks_safe`},
		"a check of another stage":    {`stage_verdict_pass`, `stage_verdict_pass, review_verdict_approve`},
		"a run":                       {`wip: 1,`, `wip: 1, run: make audit,`},
	} {
		cases["a stage of its own: "+name] = withStages(t, fixedHead+strings.Replace(optionalTail, "  - {id: done",
			strings.Replace(ownStage, from[0], from[1], 1)+"  - {id: done", 1))
	}
	cases["a command in a stage of Crew's"] = withStages(t, fixedHead+strings.Replace(optionalTail, "exit: [review_verdict_approve]",
		`exit: [review_verdict_approve, "cmd: make lint"]`, 1))
	cases["a title in a stage of Crew's"] = withStages(t, fixedHead+strings.Replace(optionalTail, "id: review,", "id: review, title: Read,", 1))
	for name, text := range map[string]string{
		"an agent that is not one":                    "version: 1\nagents: {gardener: {effort: low}}\n",
		"a model without its CLI":                     "version: 1\nagents: {builder: {model: sonnet}}\n",
		"an effort not known":                         "version: 1\nagents: {builder: {effort: huge}}\n",
		"a share above one":                           "version: 1\nlimits: {max_added_share: 1.5}\n",
		"no fix round":                                "version: 1\nlimits: {max_fix_rounds: 0}\n",
		"no ticket failure limit":                     "version: 1\nlimits: {max_ticket_failures: 0}\n",
		"negative added tickets":                      "version: 1\nlimits: {max_added_tickets: -1}\n",
		"a word not offered":                          "version: 1\napprovals: {new_feature: soon}\n",
		"credentials, which is no setting since 0.79": "version: 1\nsandbox: {credentials: []}\n",
		"push": "version: 1\ngit: {push: [everything]}\n",
		"a budget, which is no setting since 0.53": "version: 1\nbudget_usd: 140\n",
		"a run's budget":            "version: 1\nlimits: {run_budget_usd: 5}\n",
		"zero active tickets":       "version: 1\nlimits: {max_active_tickets: 0}\n",
		"too many active tickets":   "version: 1\nlimits: {max_active_tickets: 33}\n",
		"too few sandbox cpus":      "version: 1\nsandbox: {cpus: 1}\n",
		"too many sandbox cpus":     "version: 1\nsandbox: {cpus: 64}\n",
		"too little sandbox memory": "version: 1\nsandbox: {memory_gb: 1}\n",
		"too little sandbox disk":   "version: 1\nsandbox: {disk_gb: 2}\n",
		"stop before suspend":       "version: 1\nsandbox: {suspend_after_minutes: 20, stop_after_minutes: 10}\n",
	} {
		d, err := settings.Parse([]byte(text))
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		cases[name] = d
	}
	for name, d := range cases {
		err := settings.ValidateProject(d, defaults)
		if err == nil {
			t.Errorf("%s: accepted", name)
		}
		t.Logf("%s: %v", name, err)
	}
}

func TestProjectValidateAccepts(t *testing.T) {
	defaults, _ := settings.ProjectDefaults("shop", "SF")
	cases := map[string]settings.Doc{
		"a stage of its own": withStages(t, fixedHead+strings.Replace(optionalTail, "  - {id: done", ownStage+"  - {id: done", 1)),
		"one before review":  withStages(t, fixedHead+ownStage+optionalTail),
	}
	for name, text := range map[string]string{
		"agents":                            "version: 1\nagents: {builder: {effort: low}, reviewer: {model: claude/sonnet}, at_limit: wait}\n",
		"a remote":                          "version: 1\ngit: {remote: 'git@github.com:o/r.git'}\n",
		"the operator's":                    "version: 1\napprovals: {new_feature: operator}\n",
		"project sandbox sizing and limits": "version: 1\nlimits: {max_active_tickets: 4, max_ticket_failures: 2, max_added_tickets: 0}\nsandbox: {cpus: 8, memory_gb: 12, disk_gb: 50, suspend_after_minutes: 15, stop_after_minutes: 60, max_snapshots: 10}\n",
	} {
		d, err := settings.Parse([]byte(text))
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		cases[name] = d
	}
	for name, d := range cases {
		if err := settings.ValidateProject(d, defaults); err != nil {
			t.Errorf("%s: refused: %v", name, err)
		}
	}
}

func TestSetStagesOn(t *testing.T) {
	d, _ := settings.ProjectDefaults("shop", "SF")
	n, err := settings.SetStagesOn(d, map[string]bool{"e2e": true, "review": false})
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]bool{}
	for _, s := range settings.ParseStages(n) {
		got[s.ID] = s.On
	}
	if !got["e2e"] || got["review"] || !got["test"] {
		t.Fatalf("stages = %v", got)
	}
	if on := settings.ParseStages(d)[4].On; !on {
		t.Fatal("SetStagesOn changed the settings it was given")
	}
	for _, bad := range []map[string]bool{{"analysis": false}, {"security": true}} {
		if _, err := settings.SetStagesOn(d, bad); err == nil {
			t.Errorf("%v: accepted", bad)
		}
	}
}

// A project's settings store only what differs from its defaults, with history that names the
// project; the key of a project does not change.
func TestSaveProjectStoresTheOperatorsValues(t *testing.T) {
	ctx := context.Background()
	d := dbtest.New(t, false)
	const fixture = "f1c50000-0000-4000-8000-000000000001"
	doc, _ := settings.ProjectDefaults("fixture", "FIX")
	doc["limits"].(settings.Doc)["max_fix_rounds"] = 4
	// A budget, as a document from before 0.53 names it, is taken out as it is saved.
	doc["budget_usd"] = 12.5
	save := func(doc settings.Doc) error {
		return pgx.BeginFunc(ctx, d.Server, func(tx pgx.Tx) error {
			return settings.SaveProject(ctx, tx, fixture, "fixture", "FIX", doc, "test")
		})
	}
	if err := save(doc); err != nil {
		t.Fatal(err)
	}
	own, err := settings.LoadProject(ctx, d.Server, fixture)
	if err != nil || len(own) != 1 || own["limits"].(settings.Doc)["max_fix_rounds"] != 4 {
		t.Fatalf("stored %v (%v)", own, err)
	}
	var project string
	if err := d.Server.QueryRow(ctx, `SELECT project::text FROM changes WHERE tbl = 'settings' AND row_key = $1`, fixture).Scan(&project); err != nil || project != fixture {
		t.Fatalf("the history row names %q (%v)", project, err)
	}
	other := settings.Merge(doc, settings.Doc{"key": "OTHER"})
	if err := save(other); err == nil {
		t.Fatal("the key was changed")
	}
	bad := settings.Merge(doc, settings.Doc{"limits": settings.Doc{"max_fix_rounds": 0}})
	if err := save(bad); err == nil {
		t.Fatal("settings that do not validate were saved")
	}
	h, _ := db.History(ctx, d.Server, "settings", fixture)
	if len(h) != 1 {
		t.Fatalf("history has %d rows", len(h))
	}
	eff, err := settings.EffectiveProject(ctx, d.Server, fixture, "fixture", "FIX")
	if _, budget := eff["budget_usd"]; err != nil || budget || eff["limits"].(settings.Doc)["max_fix_rounds"] != 4 {
		t.Fatalf("effective = %v (%v)", eff, err)
	}
}
