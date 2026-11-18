package models_test

import (
	"context"
	"reflect"
	"slices"
	"testing"

	"github.com/kesokaj/crew/baseline"
	"github.com/kesokaj/crew/internal/db/dbtest"
	"github.com/kesokaj/crew/internal/models"
	"github.com/kesokaj/crew/internal/settings"
)

func entries(t *testing.T, d *dbtest.DB, role string) []string {
	t.Helper()
	own, err := settings.Load(context.Background(), d.Server)
	if err != nil {
		t.Fatal(err)
	}
	r, _ := own["agents"].(settings.Doc)["roles"].(settings.Doc)[role].(settings.Doc)
	var out []string
	for _, e := range r["model"].([]any) {
		out = append(out, e.(string))
	}
	return out
}

// Install copies the baseline's lists into the machine's settings; from then on they are the
// machine's own, and a release adds only an entry that is new to the machine, after its own (12.3).
func TestInstallFillsTheAgentsFromTheBaseline(t *testing.T) {
	ctx := context.Background()
	d := dbtest.New(t, false)
	m := settings.Machine{Version: "test"}
	changed, err := models.FillRoles(ctx, d.Server, m, "crew test")
	if err != nil || !changed {
		t.Fatalf("%v %v", changed, err)
	}
	base, _ := baseline.Roles()
	if !slices.Equal(base["judge"].Model, []string{"agy/claude-sonnet-5-5", "agy/gemini-3.8-flash", "agy/gemini-4-carbon", "agy/gemini-4-argon"}) {
		t.Fatalf("judge baseline = %v", base["judge"].Model)
	}
	for name, r := range base {
		if got := entries(t, d, name); !slices.Equal(got, r.Model) {
			t.Errorf("%s: %v, not %v", name, got, r.Model)
		}
	}
	if changed, err = models.FillRoles(ctx, d.Server, m, "crew test"); err != nil || changed {
		t.Fatalf("a second start changed the settings: %v %v", changed, err)
	}
	// The operator orders the builder's list and takes an entry out: no start changes it.
	doc, _ := settings.Effective(ctx, d.Server, m)
	doc["agents"].(settings.Doc)["roles"].(settings.Doc)["builder"] = settings.Doc{"model": []any{"agy/claude-sonnet-5-5"}, "effort": "medium"}
	if err := settings.Save(ctx, d.Server, m, doc, "op"); err != nil {
		t.Fatal(err)
	}
	if changed, _ = models.FillRoles(ctx, d.Server, m, "crew test"); changed || !slices.Equal(entries(t, d, "builder"), []string{"agy/claude-sonnet-5-5"}) {
		t.Fatalf("the operator's list changed: %v", entries(t, d, "builder"))
	}
	// A release whose baseline has an entry new to this machine adds it after the agent's own.
	d.Server.Exec(ctx, `DELETE FROM baseline_entries WHERE role = 'builder' AND entry = 'agy/gemini-4-argon'`)
	if changed, _ = models.FillRoles(ctx, d.Server, m, "crew test"); !changed || !slices.Equal(entries(t, d, "builder"), []string{"agy/claude-sonnet-5-5", "agy/gemini-4-argon"}) {
		t.Fatalf("a new entry: %v", entries(t, d, "builder"))
	}
	// The listing's own lists, which a release may change, do not change the machine's entries.
	def, _ := settings.Defaults(m)
	def["agents"].(settings.Doc)["roles"].(settings.Doc)["builder"] = settings.Doc{"model": []any{"agy/other"}, "effort": "low"}
	own, _ := settings.Load(ctx, d.Server)
	if eff := settings.Merge(def, own); !reflect.DeepEqual(eff["agents"].(settings.Doc)["roles"].(settings.Doc)["builder"].(settings.Doc)["model"], []any{"agy/claude-sonnet-5-5", "agy/gemini-4-argon"}) {
		t.Fatalf("a listing changed the machine's builder: %v", eff["agents"])
	}
}

// The listing of 14.1 shows the baseline's lists.
func TestTheListingShowsTheBaselinesLists(t *testing.T) {
	def, err := settings.Defaults(settings.Machine{Version: "test"})
	if err != nil {
		t.Fatal(err)
	}
	base, _ := baseline.Roles()
	roles := def["agents"].(settings.Doc)["roles"].(settings.Doc)
	if len(roles) != len(base) {
		t.Fatalf("%d roles in the listing, %d in the baseline", len(roles), len(base))
	}
	for name, r := range base {
		got, _ := roles[name].(settings.Doc)
		var list []string
		for _, e := range got["model"].([]any) {
			list = append(list, e.(string))
		}
		if !slices.Equal(list, r.Model) || got["effort"] != r.Effort {
			t.Errorf("%s: %v %v, the baseline %v %s", name, list, got["effort"], r.Model, r.Effort)
		}
	}
}
