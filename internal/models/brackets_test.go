package models_test

import (
	"context"
	"slices"
	"testing"
	"time"

	"github.com/kesokaj/crew/internal/cli"
	"github.com/kesokaj/crew/internal/db/dbtest"
	"github.com/kesokaj/crew/internal/models"
	"github.com/kesokaj/crew/internal/models/modelstest"
	"github.com/kesokaj/crew/internal/settings"
)

func TestClassifyBracket(t *testing.T) {
	t.Parallel()
	cases := []struct {
		model   string
		efforts []string
		want    string
	}{
		{"gemini-3.8-flash", []string{"low", "medium", "high"}, "flash"},
		{"agy/gemini-2.5-flash-lite", []string{"low", "high"}, "flash"},
		{"gemini-4-pro-lite", []string{"low", "high"}, "flash"},
		{"gpt-4o-mini", []string{"medium"}, "flash"},
		{"gemini-nano", nil, "flash"},
		{"claude-haiku-4-5", []string{"low", "high"}, "flash"},
		{"claude-sonnet-5-5", []string{"low", "medium", "high"}, "flash"},
		{"gpt-oss", []string{"medium"}, "oss"},
		{"agy/gpt-oss-120b", []string{"medium"}, "oss"},
		{"claude-opus-5-5", []string{"low", "medium", "high"}, "pro"},
		{"claude-opus-5-6", []string{"high"}, "pro"},
		{"gemini-3.1-pro", []string{"high"}, "pro"},
		{"gemini-2-ultra", []string{"high"}, "pro"},
		{"gemini-4-argon", []string{"low", "medium", "high"}, "pro"},
		{"agy/gemini-4-carbon", []string{"medium", "high"}, "pro"},
		{"gemini-4-mystery", []string{"high"}, ""},
		{"gemini-exp-1206", nil, ""},
		{"proton-1", []string{"low", "medium", "high"}, ""},
		{"nova-1", []string{"medium"}, ""},
	}
	for _, tc := range cases {
		if got := models.ClassifyBracket(tc.model, tc.efforts); got != tc.want {
			t.Errorf("ClassifyBracket(%q, %v) = %q, want %q", tc.model, tc.efforts, got, tc.want)
		}
	}
}

func TestParseModelVersionAndCompareModels(t *testing.T) {
	t.Parallel()
	verCases := []struct {
		model string
		want  models.ModelVersion
	}{
		{"gemini-3.8-flash", models.ModelVersion{Major: 3, Minor: 8}},
		{"agy/gemini-4-carbon", models.ModelVersion{Major: 4, Minor: 0}},
		{"claude-opus-5-5", models.ModelVersion{Major: 5, Minor: 5}},
		{"claude-opus-5-6", models.ModelVersion{Major: 5, Minor: 6}},
		{"claude-sonnet-4-5-20250929", models.ModelVersion{Major: 4, Minor: 5}},
		{"gpt-oss-120b", models.ModelVersion{Major: -1, Minor: -1}},
		{"gemini-exp-1206", models.ModelVersion{Major: -1, Minor: -1}},
	}
	for _, tc := range verCases {
		if got := models.ParseModelVersion(tc.model); got != tc.want {
			t.Errorf("ParseModelVersion(%q) = %+v, want %+v", tc.model, got, tc.want)
		}
	}

	t1 := time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)
	t2 := time.Date(2026, 11, 1, 12, 0, 0, 0, time.UTC)
	// Higher version wins even when an older version has a newer first_seen.
	if models.CompareModels("agy/gemini-3.8-flash", t1, 1, "agy/gemini-3.7-flash", t2, 0) >= 0 {
		t.Fatal("3.8 should rank ahead of 3.7 regardless of first_seen")
	}
	// Same major.minor: newer first_seen wins (gemini-4-carbon seen after gemini-4-argon).
	if models.CompareModels("agy/gemini-4-carbon", t2, 1, "agy/gemini-4-argon", t1, 0) >= 0 {
		t.Fatal("newer same-version codename should rank ahead")
	}
	// Same major.minor and identical first_seen (fresh-install tx): baseline order wins, then model DESC.
	if models.CompareModels("agy/gemini-4-carbon", t1, 0, "agy/gemini-4-argon", t1, 1) >= 0 {
		t.Fatal("baseline index 0 should rank ahead of index 1 on equal version and first_seen")
	}
	if models.CompareModels("agy/gemini-4-neon", t1, -1, "agy/gemini-4-boron", t1, -1) >= 0 {
		t.Fatal("equal version and first_seen outside baseline should break by model DESC")
	}
}

func TestTicketBracket(t *testing.T) {
	t.Parallel()
	cases := []struct {
		mrole, kind, ticketKind, want string
	}{
		{"builder", "part", "feature", "flash"},
		{"builder", "build", "docs", "flash"},
		{"builder", "fix", "chore", "flash"},
		{"builder", "build", "feature", ""},
		{"reviewer", "review", "docs", "flash"},
		{"reviewer", "review", "chore", "flash"},
		{"reviewer", "review", "feature", ""},
		{"reviewer", "stage", "docs", ""},
		{"reviewer", "analysis", "docs", ""},
		{"verifier", "verify", "docs", ""},
	}
	for _, tc := range cases {
		if got := models.TicketBracket(tc.mrole, tc.kind, tc.ticketKind); got != tc.want {
			t.Errorf("TicketBracket(%q, %q, %q) = %q, want %q", tc.mrole, tc.kind, tc.ticketKind, got, tc.want)
		}
	}
}

func roleList(t *testing.T, doc settings.Doc, role string) []string {
	t.Helper()
	r := doc["agents"].(settings.Doc)["roles"].(settings.Doc)[role].(settings.Doc)
	var out []string
	for _, x := range r["model"].([]any) {
		out = append(out, x.(string))
	}
	return out
}

func bracketList(t *testing.T, doc settings.Doc, name string) []string {
	t.Helper()
	b := doc["agents"].(settings.Doc)["brackets"].(settings.Doc)
	var out []string
	for _, x := range b[name].([]any) {
		out = append(out, x.(string))
	}
	return out
}

func agyWithEfforts(base string, efforts ...string) []cli.Model {
	var out []cli.Model
	for _, e := range efforts {
		out = append(out, cli.Model{Name: base + "-" + e, Model: base, Efforts: []string{e}, InName: true})
	}
	return out
}

func pre144AgyModels() []cli.Model {
	var out []cli.Model
	for _, m := range []string{"gemini-4-argon", "claude-opus-5-5", "gemini-3.8-flash", "claude-sonnet-5-5"} {
		out = append(out, agyWithEfforts(m, "low", "medium", "high")...)
	}
	return out
}

func mustNoErr(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}

func TestSyncModelsAutoBracketsAndPromotesRoles(t *testing.T) {
	ctx := context.Background()
	d := dbtest.New(t, false)
	m := settings.Machine{Version: "test"}
	if _, err := d.Server.Exec(ctx, `INSERT INTO cli_logins (cli, state, checked_at) VALUES ('agy', 'signed_in', now())`); err != nil {
		t.Fatal(err)
	}
	if _, err := models.FillRoles(ctx, d.Server, m, "crew test"); err != nil {
		t.Fatal(err)
	}

	// First CLI read: standard fixture + unclassified nova-1.
	modelstest.Recorded(t, d.Server)
	rows, err := models.Rows(ctx, d.Server)
	if err != nil {
		t.Fatal(err)
	}
	var initial []cli.Model
	for _, r := range rows {
		initial = append(initial, cli.Model{Name: r.Name, Model: r.Model, Efforts: r.Efforts, InName: r.InName})
	}
	initial = append(initial, cli.Model{Name: "nova-1-medium", Model: "nova-1", Efforts: []string{"medium"}, InName: true})
	// Reset cli_logins.models_read_at to simulate first-ever read with nova-1 included.
	d.Server.Exec(ctx, `DELETE FROM models; DELETE FROM model_choices; UPDATE cli_logins SET models_read_at = NULL`)
	if err := models.Record(ctx, d.Server, "agy", initial, ""); err != nil {
		t.Fatal(err)
	}

	eff, err := settings.Effective(ctx, d.Server, m)
	if err != nil {
		t.Fatal(err)
	}
	unb, err := models.Unbracketed(ctx, d.Server, eff)
	if err != nil || !slices.Equal(unb, []string{"agy/nova-1"}) {
		t.Fatalf("Unbracketed = %v, %v; want [agy/nova-1]", unb, err)
	}
	// Initial discovery before any active model was running creates no watching rows.
	if chs, _ := models.Choices(ctx, d.Server, ""); len(chs) != 0 {
		t.Fatalf("initial Record created %d model_choices: %+v", len(chs), chs)
	}

	// Operator places nova-1 into a custom bracket: SyncModels preserves it.
	if err := settings.Update(ctx, d.Server, m, "op@example.com", func(doc settings.Doc) error {
		doc["agents"].(settings.Doc)["brackets"].(settings.Doc)["custom"] = []any{"agy/nova-1"}
		return nil
	}); err != nil {
		t.Fatal(err)
	}

	// Next CLI refresh introduces gemini-4-carbon (same major, newer first_seen), gemini-3.9-flash (higher minor),
	// claude-opus-5-6 (higher minor), and an older re-listed gemini-3.7-flash.
	next := append([]cli.Model(nil), initial...)
	next = append(next, agyWithEfforts("gemini-4-carbon", "low", "medium", "high")...)
	next = append(next, agyWithEfforts("gemini-3.9-flash", "low", "medium", "high")...)
	next = append(next, agyWithEfforts("claude-opus-5-6", "low", "medium", "high")...)
	next = append(next, agyWithEfforts("gemini-3.7-flash", "low", "medium", "high")...)
	if err := models.Record(ctx, d.Server, "agy", next, ""); err != nil {
		t.Fatal(err)
	}

	eff, err = settings.Effective(ctx, d.Server, m)
	if err != nil {
		t.Fatal(err)
	}
	if got := bracketList(t, eff, "custom"); !slices.Equal(got, []string{"agy/nova-1"}) {
		t.Fatalf("custom bracket lost: %v", got)
	}
	pro := bracketList(t, eff, "pro")
	if !slices.Contains(pro, "agy/gemini-4-carbon") || !slices.Contains(pro, "agy/claude-opus-5-6") {
		t.Fatalf("pro bracket missing new models: %v", pro)
	}
	flash := bracketList(t, eff, "flash")
	i39 := slices.Index(flash, "agy/gemini-3.9-flash")
	i38 := slices.Index(flash, "agy/gemini-3.8-flash")
	i37 := slices.Index(flash, "agy/gemini-3.7-flash")
	if !(i39 >= 0 && i39 < i38 && i38 < i37) {
		t.Fatalf("flash bracket version order wrong: %v", flash)
	}

	for _, role := range []string{"curator", "planner", "assistant", "builder", "improver"} {
		rl := roleList(t, eff, role)
		if rl[0] != "agy/gemini-4-carbon" || rl[1] != "agy/gemini-4-argon" {
			t.Errorf("%s = %v, want carbon then argon first", role, rl)
		}
	}
	for _, role := range []string{"tester", "e2e", "summary", "prompt"} {
		rl := roleList(t, eff, role)
		if rl[0] != "agy/gemini-3.9-flash" || rl[1] != "agy/gemini-3.8-flash" {
			t.Errorf("%s = %v, want 3.9-flash then 3.8-flash first", role, rl)
		}
	}
	for _, role := range []string{"reviewer", "verifier"} {
		rl := roleList(t, eff, role)
		if rl[0] != "agy/claude-opus-5-6" || rl[1] != "agy/claude-opus-5-5" {
			t.Errorf("%s = %v, want opus-5-6 then opus-5-5 first", role, rl)
		}
	}

	// Every ChosenRole got a watching row with reason = "newer".
	for _, role := range models.ChosenRoles {
		chs, err := models.Choices(ctx, d.Server, role)
		if err != nil || len(chs) != 1 || chs[0].Kind != "change" || chs[0].Reason != "newer" || chs[0].State != "watching" {
			t.Errorf("%s choices = %+v, %v; want 1 watching newer change", role, chs, err)
		}
	}
}

func TestSyncModelsHonoursOverridesAndChooserRollback(t *testing.T) {
	k := newAutoKit(t, nil)
	m := settings.Machine{Version: "test"}

	// Seed legacy v1.43 planner/judge baseline_entries and operator overrides on curator & verifier.
	if _, err := models.FillRoles(k.ctx, k.d.Server, m, "crew test"); err != nil {
		t.Fatal(err)
	}
	k.d.Server.Exec(k.ctx, `DELETE FROM baseline_entries WHERE role = 'judge' AND entry = 'agy/claude-sonnet-5-5'`)
	k.d.Server.Exec(k.ctx, `INSERT INTO baseline_entries (role, entry) VALUES ('planner', 'agy/claude-opus-5-5'), ('judge', 'agy/claude-opus-5-5') ON CONFLICT DO NOTHING`)
	k.d.Server.Exec(k.ctx, `DELETE FROM baseline_entries WHERE role = 'builder' AND entry = 'agy/gemini-4-carbon'`)
	if err := settings.Update(k.ctx, k.d.Server, m, "crew test", func(doc settings.Doc) error {
		roles := doc["agents"].(settings.Doc)["roles"].(settings.Doc)
		roles["planner"].(settings.Doc)["model"] = []any{"agy/claude-opus-5-5", "agy/gemini-4-argon", "agy/gemini-3.8-flash"}
		roles["judge"].(settings.Doc)["model"] = []any{"agy/claude-opus-5-5", "agy/gemini-4-argon", "agy/gemini-3.8-flash"}
		roles["builder"].(settings.Doc)["model"] = []any{"agy/gemini-4-argon", "agy/gemini-3.8-flash"}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	// Operator customizes curator (non-AutoRole), puts verifier in set_by_you, and removes gemini-3.8-flash from builder.
	if err := settings.Update(k.ctx, k.d.Server, m, "op@example.com", func(doc settings.Doc) error {
		agents := doc["agents"].(settings.Doc)
		roles := agents["roles"].(settings.Doc)
		roles["curator"].(settings.Doc)["model"] = []any{"agy/gemini-3.8-flash"}
		roles["verifier"].(settings.Doc)["model"] = []any{"agy/claude-opus-5-5"}
		roles["builder"].(settings.Doc)["model"] = []any{"agy/gemini-4-argon"}
		agents["auto"].(settings.Doc)["set_by_you"] = []any{"verifier"}
		return nil
	}); err != nil {
		t.Fatal(err)
	}

	// Record 20 solid historical runs (90% pass) on gemini-4-argon before carbon arrives.
	k.units(20, 18, "agy", "gemini-4-argon", "high", "automatic", 1.0, k.now.Add(-24*time.Hour))

	rows, _ := models.Rows(k.ctx, k.d.Server)
	var list []cli.Model
	for _, r := range rows {
		list = append(list, cli.Model{Name: r.Name, Model: r.Model, Efforts: r.Efforts, InName: r.InName})
	}
	list = append(list, agyWithEfforts("gemini-4-carbon", "low", "medium", "high")...)
	list = append(list, agyWithEfforts("claude-opus-5-6", "low", "medium", "high")...)
	if err := models.Record(k.ctx, k.d.Server, "agy", list, ""); err != nil {
		t.Fatal(err)
	}

	eff, _ := settings.Effective(k.ctx, k.d.Server, m)
	// curator (operator-edited) and verifier (set_by_you) were not touched; builder did not get removed gemini-3.8-flash back.
	if got := roleList(t, eff, "curator"); !slices.Equal(got, []string{"agy/gemini-3.8-flash"}) {
		t.Fatalf("operator-edited curator changed: %v", got)
	}
	if got := roleList(t, eff, "verifier"); !slices.Equal(got, []string{"agy/claude-opus-5-5"}) {
		t.Fatalf("set_by_you verifier changed: %v", got)
	}
	bList := roleList(t, eff, "builder")
	if bList[0] != "agy/gemini-4-carbon" || slices.Contains(bList, "agy/gemini-3.8-flash") {
		t.Fatalf("builder = %v, want carbon first and removed 3.8-flash absent", bList)
	}
	// Legacy claude-opus-5-5 retired from planner and judge.
	if slices.Contains(roleList(t, eff, "planner"), "agy/claude-opus-5-5") {
		t.Fatalf("planner kept legacy claude-opus-5-5: %v", roleList(t, eff, "planner"))
	}
	if got := roleList(t, eff, "judge"); got[0] != "agy/claude-sonnet-5-5" || slices.Contains(got, "agy/claude-opus-5-5") {
		t.Fatalf("judge = %v, want claude-sonnet-5-5 first without legacy opus", got)
	}

	// Now 20 watched runs on gemini-4-carbon pass only 50% (vs argon's 90%): Chooser rolls back automatically!
	k.now = time.Now().Add(time.Minute)
	k.units(20, 10, "agy", "gemini-4-carbon", "high", "automatic", 1.0, k.now)
	k.evaluate()
	if got, _ := k.builder(); got[0] != "agy/gemini-4-argon" {
		t.Fatalf("Chooser did not roll back regressed carbon promotion: %v", got)
	}

	// Subsequent models.Record within retry_days (30d) must NOT re-promote blocked gemini-4-carbon.
	if err := models.Record(k.ctx, k.d.Server, "agy", list, ""); err != nil {
		t.Fatal(err)
	}
	if got, _ := k.builder(); got[0] != "agy/gemini-4-argon" {
		t.Fatalf("SyncModels re-promoted blocked gemini-4-carbon within retry_days: %v", got)
	}
}

func TestPlannerUsesGeminiAndFlashReviewingUsesSonnet(t *testing.T) {
	ctx := context.Background()
	d := dbtest.New(t, false)
	signIn(t, d, "signed_in", "agy")
	readRecorded(t, d, "agy")
	doc := machine(t, nil)

	// Planner chooses Gemini, never Claude, unless operator-chosen.
	c, err := models.Choose(ctx, d.Server, models.Ask{Role: "planner", Machine: doc})
	if err != nil || c.Model != "gemini-4-argon" {
		t.Fatalf("planner = %+v, %v; want gemini-4-argon", c, err)
	}
	pinned, err := models.Choose(ctx, d.Server, models.Ask{
		Role: "planner", Machine: doc,
		Project: settings.Doc{"agents": settings.Doc{"planner": settings.Doc{"model": "agy/claude-opus-5-5"}}},
	})
	if err != nil || pinned.Model != "claude-opus-5-5" || pinned.ModelFrom != models.FromProject {
		t.Fatalf("project-pinned planner = %+v, %v", pinned, err)
	}

	// Reviewer on Bracket: "flash" (docs/chore) chooses claude-sonnet-5-5 against Gemini build,
	// while code-ticket reviewer (Bracket: "") chooses claude-opus-5-5.
	codeRev, err := models.Choose(ctx, d.Server, models.Ask{Role: "reviewer", Machine: doc, Avoid: "gemini"})
	if err != nil || codeRev.Model != "claude-opus-5-5" || codeRev.Off {
		t.Fatalf("code reviewer = %+v, %v", codeRev, err)
	}
	flashRev, err := models.Choose(ctx, d.Server, models.Ask{Role: "reviewer", Machine: doc, Avoid: "gemini", Bracket: "flash"})
	if err != nil || flashRev.Model != "claude-sonnet-5-5" || flashRev.Bracket != "flash" || flashRev.Off {
		t.Fatalf("flash reviewer = %+v, %v", flashRev, err)
	}
	if cannot, err := models.RuleCannotBeMet(ctx, d.Server, models.Ask{Role: "reviewer", Machine: doc, Avoid: "gemini", Bracket: "flash"}); err != nil || cannot {
		t.Fatalf("flash reviewer RuleCannotBeMet with 3p available = %v, %v", cannot, err)
	}

	// At 3p limit, flash reviewer and judge fall back to gemini-3.8-flash (not pro) with Off: true.
	reset := time.Now().Add(2 * time.Hour)
	if err := models.RecordLimits(ctx, d.Server, "agy", []cli.Limit{{Group: "3p", Window: "5h", Used: 1, Reached: true, ResetsAt: reset}}, "login check"); err != nil {
		t.Fatal(err)
	}
	flashFallback, err := models.Choose(ctx, d.Server, models.Ask{Role: "reviewer", Machine: doc, Avoid: "gemini", Bracket: "flash"})
	if err != nil || flashFallback.Model != "gemini-3.8-flash" || !flashFallback.Off || flashFallback.ModelFrom != models.FromLimit {
		t.Fatalf("flash reviewer at 3p limit = %+v, %v; want gemini-3.8-flash Off=true", flashFallback, err)
	}
	if cannot, err := models.RuleCannotBeMet(ctx, d.Server, models.Ask{Role: "reviewer", Machine: doc, Avoid: "gemini", Bracket: "flash"}); err != nil || !cannot {
		t.Fatalf("flash reviewer RuleCannotBeMet at 3p limit = %v, %v; want true", cannot, err)
	}
	judgeFallback, err := models.Choose(ctx, d.Server, models.Ask{Role: "judge", Machine: doc, Avoid: "gemini"})
	if err != nil || judgeFallback.Model != "gemini-3.8-flash" || !judgeFallback.Off || judgeFallback.ModelFrom != models.FromLimit {
		t.Fatalf("judge at 3p limit = %+v, %v; want gemini-3.8-flash Off=true", judgeFallback, err)
	}
}

func TestSyncModelsPreservesCrossSlotChooserChoices(t *testing.T) {
	k := newAutoKit(t, nil)
	m := settings.Machine{Version: "test"}
	if _, err := models.FillRoles(k.ctx, k.d.Server, m, "crew test"); err != nil {
		t.Fatal(err)
	}

	// Empirically promote secondary-slot (gemini, pro) gemini-4-argon ahead of primary-slot (gemini, flash)
	// gemini-3.8-flash on tester under state = 'watching'.
	if err := settings.Update(k.ctx, k.d.Server, m, "crew", func(doc settings.Doc) error {
		doc["agents"].(settings.Doc)["roles"].(settings.Doc)["tester"].(settings.Doc)["model"] = []any{"agy/gemini-4-argon", "agy/gemini-3.8-flash"}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := k.d.Server.Exec(k.ctx, `INSERT INTO model_choices (role, kind, reason, from_entry, from_effort, to_entry, to_effort, numbers, state, who)
		VALUES ('tester', 'change', 'better', 'agy/gemini-3.8-flash', 'medium', 'agy/gemini-4-argon', 'medium', '{}'::jsonb, 'watching', 'crew')`); err != nil {
		t.Fatal(err)
	}

	rows, _ := models.Rows(k.ctx, k.d.Server)
	var list []cli.Model
	for _, r := range rows {
		list = append(list, cli.Model{Name: r.Name, Model: r.Model, Efforts: r.Efforts, InName: r.InName})
	}
	if err := models.Record(k.ctx, k.d.Server, "agy", list, ""); err != nil {
		t.Fatal(err)
	}
	eff, _ := settings.Effective(k.ctx, k.d.Server, m)
	if got := roleList(t, eff, "tester"); got[0] != "agy/gemini-4-argon" {
		t.Fatalf("watching cross-slot promotion overwritten by Record: %v", got)
	}

	// Transition watching -> kept, then run FillRoles and Record again: argon must remain first.
	if _, err := k.d.Server.Exec(k.ctx, `UPDATE model_choices SET state = 'kept', at = now() WHERE role = 'tester'`); err != nil {
		t.Fatal(err)
	}
	if _, err := models.FillRoles(k.ctx, k.d.Server, m, "crew"); err != nil {
		t.Fatal(err)
	}
	if err := models.Record(k.ctx, k.d.Server, "agy", list, ""); err != nil {
		t.Fatal(err)
	}
	eff, _ = settings.Effective(k.ctx, k.d.Server, m)
	if got := roleList(t, eff, "tester"); got[0] != "agy/gemini-4-argon" {
		t.Fatalf("kept cross-slot promotion overwritten by FillRoles/Record: %v", got)
	}

	// When a brand-new primary-slot model (gemini-3.9-flash, first_seen > kept.At) arrives, it promotes under watching.
	time.Sleep(5 * time.Millisecond)
	list = append(list, agyWithEfforts("gemini-3.9-flash", "low", "medium", "high")...)
	if err := models.Record(k.ctx, k.d.Server, "agy", list, ""); err != nil {
		t.Fatal(err)
	}
	eff, _ = settings.Effective(k.ctx, k.d.Server, m)
	if got := roleList(t, eff, "tester"); got[0] != "agy/gemini-3.9-flash" {
		t.Fatalf("new primary-slot model did not promote over kept choice: %v", got)
	}
}

func TestSyncModelsUpgradesPre144BracketHistoryWithoutFalseRemovals(t *testing.T) {
	ctx := context.Background()
	d := dbtest.New(t, false)
	signIn(t, d, "signed_in", "agy")
	m := settings.Machine{Version: "test"}

	// Simulate a v1.43 installation where models offered argon, opus, 3.8-flash, sonnet, and gpt-oss-120b (no carbon yet).
	v143Models := append(pre144AgyModels(), cli.Model{Name: "gpt-oss-120b (Medium)", Model: "gpt-oss-120b", Efforts: []string{"medium"}, InName: true})
	if err := models.Record(ctx, d.Server, "agy", v143Models, ""); err != nil {
		t.Fatal(err)
	}
	// Seed v1.43 stored roles (without carbon) as crew, then save an operator bracket edit in v1.43
	// reordering pro to [claude-opus-5-5, gemini-4-argon] and setting oss: [gpt-oss-120b].
	d.Server.Exec(ctx, `DELETE FROM baseline_entries WHERE entry = 'agy/gemini-4-carbon'`)
	if err := settings.Update(ctx, d.Server, m, "crew", func(doc settings.Doc) error {
		roles := doc["agents"].(settings.Doc)["roles"].(settings.Doc)
		for _, r := range roles {
			rm := r.(settings.Doc)
			rm["model"] = slices.DeleteFunc(rm["model"].([]any), func(v any) bool { return v == "agy/gemini-4-carbon" })
		}
		doc["agents"].(settings.Doc)["brackets"].(settings.Doc)["pro"] = []any{"agy/gemini-4-argon", "agy/claude-opus-5-5"}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if err := settings.Update(ctx, d.Server, m, "op@example.com", func(doc settings.Doc) error {
		b := doc["agents"].(settings.Doc)["brackets"].(settings.Doc)
		b["pro"] = []any{"agy/claude-opus-5-5", "agy/gemini-4-argon"}
		b["oss"] = []any{"agy/gpt-oss-120b"}
		return nil
	}); err != nil {
		t.Fatal(err)
	}

	// Upgrade to v1.44 and discover gemini-4-carbon after the v1.43 operator edit.
	time.Sleep(5 * time.Millisecond)
	v144Models := append(v143Models, agyWithEfforts("gemini-4-carbon", "low", "medium", "high")...)
	if err := models.Record(ctx, d.Server, "agy", v144Models, ""); err != nil {
		t.Fatal(err)
	}

	eff, err := settings.Effective(ctx, d.Server, m)
	if err != nil {
		t.Fatal(err)
	}
	pro := bracketList(t, eff, "pro")
	if !slices.Contains(pro, "agy/gemini-4-carbon") {
		t.Fatalf("gemini-4-carbon falsely treated as operator-removed from pro: %v", pro)
	}
	if slices.Index(pro, "agy/claude-opus-5-5") > slices.Index(pro, "agy/gemini-4-argon") {
		t.Fatalf("operator pro order not preserved: %v", pro)
	}
	unb, err := models.Unbracketed(ctx, d.Server, eff)
	if err != nil || len(unb) != 0 {
		t.Fatalf("unexpected unbracketed models after upgrade: %+v, %v", unb, err)
	}
	if got := roleList(t, eff, "builder"); got[0] != "agy/gemini-4-carbon" {
		t.Fatalf("builder did not promote gemini-4-carbon after upgrade: %v", got)
	}
}

func TestSyncModelsClearsEditedRoleAfterOperatorReset(t *testing.T) {
	ctx := context.Background()
	d := dbtest.New(t, false)
	signIn(t, d, "signed_in", "agy")
	m := settings.Machine{Version: "test"}

	pre144 := pre144AgyModels()
	mustNoErr(t, models.Record(ctx, d.Server, "agy", pre144, ""))
	// Customize planner + curator (seq 130), delete planner alone, then reset machine settings (seq 132, afterRoles == nil).
	mustNoErr(t, settings.Save(ctx, d.Server, m, settings.Doc{"version": 1, "agents": settings.Doc{"roles": settings.Doc{
		"planner": settings.Doc{"model": []any{"agy/gemini-3.8-flash"}, "effort": "medium"},
		"curator": settings.Doc{"model": []any{"agy/gemini-3.8-flash"}, "effort": "high"},
	}}}, "op@example.com"))
	mustNoErr(t, settings.Save(ctx, d.Server, m, settings.Doc{"version": 1, "agents": settings.Doc{"roles": settings.Doc{
		"curator": settings.Doc{"model": []any{"agy/gemini-3.8-flash"}, "effort": "high"},
	}}}, "op@example.com"))
	mustNoErr(t, settings.Save(ctx, d.Server, m, settings.Doc{"version": 1}, "op@example.com"))
	if own, err := settings.Load(ctx, d.Server); err != nil || len(own) != 0 {
		t.Fatalf("precondition: stored own after reset = %v, %v; want empty", own, err)
	}
	// Pre-1.44 system startups seed planner with claude-opus-5-5 first and curator narrowed.
	d.Server.Exec(ctx, `INSERT INTO baseline_entries (role, entry) VALUES ('planner', 'agy/claude-opus-5-5') ON CONFLICT DO NOTHING`)
	mustNoErr(t, settings.Update(ctx, d.Server, m, "crew 0.3.26", func(doc settings.Doc) error {
		roles := doc["agents"].(settings.Doc)["roles"].(settings.Doc)
		roles["planner"] = settings.Doc{"model": []any{"agy/claude-opus-5-5", "agy/gemini-4-argon", "agy/gemini-3.8-flash"}, "effort": "high"}
		roles["curator"] = settings.Doc{"model": []any{"agy/gemini-3.8-flash"}, "effort": "high"}
		return nil
	}))
	preEff, err := settings.Effective(ctx, d.Server, m)
	mustNoErr(t, err)
	if ch, err := models.Choose(ctx, d.Server, models.Ask{Role: "planner", Machine: preEff}); err != nil || ch.Model != "gemini-4-argon" || ch.ModelFrom != models.FromList {
		t.Errorf("planner Choose after reset = %+v, %v; want gemini-4-argon FromList", ch, err)
	}
	mustNoErr(t, models.Record(ctx, d.Server, "agy", append(pre144, agyWithEfforts("gemini-4-carbon", "low", "medium", "high")...), ""))
	_, err = models.FillRoles(ctx, d.Server, m, "crew 0.3.28")
	mustNoErr(t, err)
	eff, err := settings.Effective(ctx, d.Server, m)
	mustNoErr(t, err)
	for _, r := range []string{"planner", "curator"} {
		if got, want := roleList(t, eff, r), []string{"agy/gemini-4-carbon", "agy/gemini-4-argon", "agy/gemini-3.8-flash"}; !slices.Equal(got, want) {
			t.Fatalf("%s after reset + FillRoles = %v, want %v", r, got, want)
		}
	}
}

func TestSyncModelsDistinguishesBaselineChurnFromOperatorRemoval(t *testing.T) {
	ctx := context.Background()
	d := dbtest.New(t, false)
	signIn(t, d, "signed_in", "agy")
	m := settings.Machine{Version: "test"}

	pre144 := pre144AgyModels()
	mustNoErr(t, models.Record(ctx, d.Server, "agy", pre144, ""))
	_, err := models.FillRoles(ctx, d.Server, m, "crew 0.3.23")
	mustNoErr(t, err)
	d.Server.Exec(ctx, `DELETE FROM baseline_entries WHERE entry = 'agy/gemini-4-carbon'`)
	// 0.3.24 system churn narrows judge to [gemini-3.8-flash] while baseline_entries still holds sonnet and argon.
	mustNoErr(t, settings.Update(ctx, d.Server, m, "crew 0.3.24", func(doc settings.Doc) error {
		roles := doc["agents"].(settings.Doc)["roles"].(settings.Doc)
		roles["judge"].(settings.Doc)["model"] = []any{"agy/gemini-3.8-flash"}
		roles["builder"].(settings.Doc)["model"] = []any{"agy/gemini-4-argon", "agy/gemini-3.8-flash"}
		return nil
	}))
	// Operator genuinely removes gemini-3.8-flash from builder while leaving judge untouched.
	mustNoErr(t, settings.Update(ctx, d.Server, m, "op@example.com", func(doc settings.Doc) error {
		doc["agents"].(settings.Doc)["roles"].(settings.Doc)["builder"].(settings.Doc)["model"] = []any{"agy/gemini-4-argon"}
		return nil
	}))

	mustNoErr(t, models.Record(ctx, d.Server, "agy", append(pre144, agyWithEfforts("gemini-4-carbon", "low", "medium", "high")...), ""))
	_, err = models.FillRoles(ctx, d.Server, m, "crew 0.3.28")
	mustNoErr(t, err)
	eff, err := settings.Effective(ctx, d.Server, m)
	mustNoErr(t, err)
	if got, want := roleList(t, eff, "judge"), []string{"agy/claude-sonnet-5-5", "agy/gemini-3.8-flash", "agy/gemini-4-carbon", "agy/gemini-4-argon"}; !slices.Equal(got, want) {
		t.Fatalf("judge after system baseline churn = %v, want %v", got, want)
	}
	if got, want := roleList(t, eff, "builder"), []string{"agy/gemini-4-carbon", "agy/gemini-4-argon"}; !slices.Equal(got, want) {
		t.Fatalf("builder after operator entry removal = %v, want %v", got, want)
	}
}
