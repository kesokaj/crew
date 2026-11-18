package settings_test

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/kesokaj/crew/internal/db"
	"github.com/kesokaj/crew/internal/db/dbtest"
	"github.com/kesokaj/crew/internal/settings"
)

var machine = settings.Machine{Version: "1.2.3", AdminEmail: "admin@example.com", SourceRepo: "owner/crew"}

// The defaults are the listing of crew.yaml in section 14.1, as it is there: the design and the code
// do not differ.
func TestDefaultsAreTheDesignsListing(t *testing.T) {
	_, here, _, _ := runtime.Caller(0)
	tdd, err := os.ReadFile(filepath.Join(filepath.Dir(here), "..", "..", "docs", "plan", "tdd", "14-settings.md"))
	if err != nil {
		t.Fatal(err)
	}
	const head = "`crew.yaml`:\n\n```yaml\n"
	a := strings.Index(string(tdd), head)
	if a < 0 {
		t.Fatal("the design has no crew.yaml listing")
	}
	listing := string(tdd)[a+len(head):]
	listing = listing[:strings.Index(listing, "```")]
	ours, err := os.ReadFile("crew.yaml")
	if err != nil {
		t.Fatal(err)
	}
	if string(ours) != listing {
		t.Fatal("internal/settings/crew.yaml differs from the listing in section 14.1 of the design")
	}
}

func TestDefaultsFillThePlaceholders(t *testing.T) {
	d, err := settings.Defaults(machine)
	if err != nil {
		t.Fatal(err)
	}
	if d["runtime_image"] != "crew-runtime:1.2.3" {
		t.Errorf("runtime_image = %v", d["runtime_image"])
	}
	if d["notifications"].(settings.Doc)["mail_to"] != "admin@example.com" {
		t.Errorf("mail_to = %v", d["notifications"])
	}
	if d["updates"].(settings.Doc)["source_repo"] != "owner/crew" {
		t.Errorf("source_repo = %v", d["updates"])
	}
	if err := settings.Validate(d, d); err != nil {
		t.Fatalf("the defaults do not validate: %v", err)
	}
}

func TestAnExportReadsBackTheSame(t *testing.T) {
	d, _ := settings.Defaults(machine)
	out, err := settings.Format(d)
	if err != nil {
		t.Fatal(err)
	}
	back, err := settings.Parse(out)
	if err != nil {
		t.Fatal(err)
	}
	if diff := settings.Diff(back, d); len(diff) != 0 {
		t.Fatalf("an exported file reads back as other values: %v", diff)
	}
}

func TestValidateRefuses(t *testing.T) {
	defaults, _ := settings.Defaults(machine)
	cases := map[string]string{
		"unknown key":         "version: 1\nlead_slot: {agy: 1}\n",
		"unknown nested key":  "version: 1\ndisk: {clean_at: 80, full_at: 99}\n",
		"wrong kind":          "version: 1\ndisk: {clean_at: high}\n",
		"a word not offered":  "version: 1\nnetwork: {mode: everywhere}\n",
		"an effort not known": "version: 1\nagents: {roles: {lead: {model: [claude/opus], effort: huge}}}\n",
		"bounds":              "version: 1\ndisk: {clean_at: 96, stop_at: 95}\n",
		"no version":          "max_concurrent_runs: 8\n",
		"a list of numbers":   "version: 1\nnetwork: {model_only_hosts: [1, 2]}\n",
		"a null that cannot":  "version: 1\nsessions: {days: null}\n",
		"negative slots":      "version: 1\nmax_concurrent_runs: -1\n",
		"zero slots":          "version: 1\nmax_concurrent_runs: 0\n",
		"too many slots":      "version: 1\nmax_concurrent_runs: 65\n",
		"wrong nullable kind": "version: 1\nkeep_trajectories_days: many\n",
		"prices, which are no setting since 0.53":         "version: 1\nprices: [{pattern: \"gemini-*\", in: 1, out: 1, cached: 1}]\n",
		"an improver after no ticket":                     "version: 1\nimprovements: {run_after_tasks: 0}\n",
		"an improver every no day":                        "version: 1\nimprovements: {run_every_days: 0}\n",
		"a measure after no ticket":                       "version: 1\nimprovements: {measure_after_tasks: 0}\n",
		"the share of as well, for less":                  "version: 1\nagents: {auto: {cheaper_share: 0.15}}\n",
		"the build's size as a word":                      "version: 1\nsandbox_sizes: {build: {memory_gb: lots}}\n",
		"restart hours that are no range":                 "version: 1\nmachine_care: {restart_hours: \"2-5\"}\n",
		"restart hours past 23":                           "version: 1\nmachine_care: {restart_hours: \"22:00-24:00\"}\n",
		"restart hours of no length":                      "version: 1\nmachine_care: {restart_hours: \"03:00-03:00\"}\n",
		"restart hours as a number":                       "version: 1\nmachine_care: {restart_hours: 3}\n",
		"quiet hours that are no range":                   "version: 1\nnotifications: {quiet_hours: \"22-07\"}\n",
		"a reminder after no hour":                        "version: 1\nnotifications: {remind_after_hours: 0}\n",
		"a mail server without a port":                    "version: 1\nnotifications: {mail_smtp: smtp.gmail.com}\n",
		"a mail server with no host":                      "version: 1\nnotifications: {mail_smtp: \":587\"}\n",
		"web push before the phone":                       "version: 1\nnotifications: {web_push: true}\n",
		"a conversation idle for a minute":                "version: 1\nconversations: {idle_minutes: 1}\n",
		"a conversation idle for a day":                   "version: 1\nconversations: {idle_minutes: 1440}\n",
		"no conversation sandbox":                         "version: 1\nconversations: {max_sandboxes: 0}\n",
		"a summary every turn":                            "version: 1\nconversations: {summary_every_turns: 1}\n",
		"conversation credentials, no setting since 0.79": "version: 1\nconversations: {credentials: []}\n",
		"zero active tickets":                             "version: 1\nmax_active_tickets: 0\n",
		"too many active tickets":                         "version: 1\nmax_active_tickets: 65\n",
		"stop before suspend":                             "version: 1\nsandbox: {suspend_after_minutes: 30, stop_after_minutes: 10}\n",
	}
	for name, text := range cases {
		d, err := settings.Parse([]byte(text))
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if err := settings.Validate(d, defaults); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}

func TestValidateAccepts(t *testing.T) {
	defaults, _ := settings.Defaults(machine)
	for name, text := range map[string]string{
		"concurrent runs within bounds": "version: 1\nmax_concurrent_runs: 16\n",
		"a whole number for a float":    "version: 1\ncpu_overcommit: 3\n",
		"a set nullable":                "version: 1\nkeep_trajectories_days: 30\nupdates: {backup_copy_to: /mnt/x}\n",
		"a new role entry":              "version: 1\nagents: {roles: {curator: {model: [claude/sonnet], effort: max}}}\n",
		"hosts":                         "version: 1\nnetwork: {mode: model_only, model_only_hosts: [api.anthropic.com]}\n",
		"the build's size (0.60)":       "version: 1\nsandbox_sizes: {build: {memory_gb: 8}}\n",
		"the mail's own server":         "version: 1\nnotifications: {mail_smtp: \"mail.example.com:2525\", quiet_hours: \"23:00-06:30\", remind_after_hours: 1}\n",
		"conversations within bounds":   "version: 1\nconversations: {idle_minutes: 5, max_sandboxes: 20, summary_every_turns: 100}\n",
		"active tickets within bounds":  "version: 1\nmax_active_tickets: 16\nsandbox: {suspend_after_minutes: 10, stop_after_minutes: 60, max_snapshots: 10}\n",
	} {
		d, err := settings.Parse([]byte(text))
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if err := settings.Validate(d, defaults); err != nil {
			t.Errorf("%s: refused: %v", name, err)
		}
	}
}

// What is stored is only what the operator set, so a default that changes later reaches every value
// the operator did not set (14.2), and the agents' entries. Every change has its row in changes, and a set that does not
// validate changes nothing.
func TestSaveStoresTheOperatorsValuesWithHistory(t *testing.T) {
	ctx := context.Background()
	d := dbtest.New(t, false)
	defaults, _ := settings.Defaults(machine)

	file, _ := settings.Format(settings.Merge(defaults, settings.Doc{"disk": settings.Doc{"clean_at": 80}}))
	doc, err := settings.Parse(file)
	if err != nil {
		t.Fatal(err)
	}
	if err := settings.Save(ctx, d.Server, machine, doc, "test"); err != nil {
		t.Fatal(err)
	}
	own, err := settings.Load(ctx, d.Server)
	if err != nil {
		t.Fatal(err)
	}
	// The agents' entries are stored whole: they are the machine's own from install on (12.3).
	roles, _ := own["agents"].(settings.Doc)["roles"].(settings.Doc)
	if len(own) != 2 || own["disk"].(settings.Doc)["clean_at"] != 80 || len(own["disk"].(settings.Doc)) != 1 ||
		len(own["agents"].(settings.Doc)) != 1 || len(roles) != 12 {
		t.Fatalf("stored %v, want only disk.clean_at and agents.roles", own)
	}
	eff, _ := settings.Effective(ctx, d.Server, machine)
	if eff["disk"].(settings.Doc)["stop_at"] != 95 || eff["disk"].(settings.Doc)["clean_at"] != 80 {
		t.Fatalf("effective disk = %v", eff["disk"])
	}

	// The same file again changes nothing, and writes no history.
	if err := settings.Save(ctx, d.Server, machine, doc, "test"); err != nil {
		t.Fatal(err)
	}
	h, _ := db.History(ctx, d.Server, "settings", "machine")
	if len(h) != 1 {
		t.Fatalf("%d history rows, want 1", len(h))
	}

	// A file from before 0.53 names the improver's weekly budget, the prices, and the share of "for less":
	// they are taken out, and the rest is saved.
	old := settings.Merge(defaults, settings.Doc{"disk": settings.Doc{"clean_at": 81}, "improvements": settings.Doc{"budget_usd_per_week": 10},
		"prices": []any{settings.Doc{"pattern": "gemini-*", "in": 1.0, "out": 2.0, "cached": 0.0}},
		"agents": settings.Doc{"auto": settings.Doc{"cheaper_share": 0.15}}})
	if err := settings.Save(ctx, d.Server, machine, old, "test"); err != nil {
		t.Fatalf("a file from before 0.53: %v", err)
	}
	if got, _ := settings.Load(ctx, d.Server); got["disk"].(settings.Doc)["clean_at"] != 81 || got["improvements"] != nil || got["prices"] != nil ||
		strings.Contains(fmt.Sprint(got), "cheaper") {
		t.Fatalf("stored %v", got)
	}
	if err := settings.Save(ctx, d.Server, machine, doc, "test"); err != nil {
		t.Fatal(err)
	}

	bad, _ := settings.Parse([]byte("version: 1\ndisk: {clean_at: 99}\n"))
	if err := settings.Save(ctx, d.Server, machine, bad, "test"); err == nil {
		t.Fatal("an invalid set was saved")
	}
	after, _ := settings.Load(ctx, d.Server)
	if after["disk"].(settings.Doc)["clean_at"] != 80 {
		t.Fatalf("a refused set changed the stored values: %v", after)
	}
}

// Two changes at once, each made from what it read, both stay: each reads the settings under the
// settings' lock (reviews of M8).
func TestTwoChangesAtOnceBothStay(t *testing.T) {
	ctx := context.Background()
	d := dbtest.New(t, false)
	var wg sync.WaitGroup
	for _, c := range []string{"agy", "claude", "codex"} {
		wg.Add(1)
		go func() {
			defer wg.Done()
			err := settings.Update(ctx, d.Server, machine, c, func(doc settings.Doc) error {
				time.Sleep(50 * time.Millisecond)
				doc["agents"].(settings.Doc)["extra_models"].(settings.Doc)[c] = []any{c + "/m1"}
				return nil
			})
			if err != nil {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	eff, _ := settings.Effective(ctx, d.Server, machine)
	for _, c := range []string{"agy", "claude", "codex"} {
		if len(eff["agents"].(settings.Doc)["extra_models"].(settings.Doc)[c].([]any)) != 1 {
			t.Fatalf("a change was lost: %v", eff["agents"].(settings.Doc)["extra_models"])
		}
	}
}

// agents.brackets is an open map of bracket names to CLI/model lists: a new bracket with valid entries
// is accepted, while a non-map, a non-list, an empty list, a bare model name without a CLI, a duplicate
// within one bracket, or a model placed in two brackets is refused (12.3, 14.1).
func TestBracketsAreValidatedLikeRoles(t *testing.T) {
	defaults, err := settings.Defaults(machine)
	if err != nil {
		t.Fatal(err)
	}
	okText := "version: 1\nagents:\n  brackets:\n    pro: [agy/claude-opus-5-5, agy/gemini-4-argon]\n    flash: [agy/gemini-3.8-flash, agy/claude-sonnet-5-5]\n    reasoning: [agy/deepseek-r1]\n"
	okDoc, err := settings.Parse([]byte(okText))
	if err != nil {
		t.Fatal(err)
	}
	if err := settings.Validate(okDoc, defaults); err != nil {
		t.Fatalf("valid brackets refused: %v", err)
	}
	for name, text := range map[string]string{
		"brackets not a map":    "version: 1\nagents: {brackets: [agy/claude-opus-5-5]}\n",
		"bracket not a list":    "version: 1\nagents: {brackets: {pro: agy/claude-opus-5-5}}\n",
		"empty bracket":         "version: 1\nagents: {brackets: {pro: []}}\n",
		"number in bracket":     "version: 1\nagents: {brackets: {pro: [123]}}\n",
		"model without CLI":     "version: 1\nagents: {brackets: {pro: [claude-opus-5-5]}}\n",
		"duplicate in bracket":  "version: 1\nagents: {brackets: {pro: [agy/claude-opus-5-5, agy/claude-opus-5-5]}}\n",
		"model in two brackets": "version: 1\nagents: {brackets: {pro: [agy/claude-opus-5-5], flash: [agy/claude-opus-5-5, agy/gemini-3.8-flash]}}\n",
	} {
		d, err := settings.Parse([]byte(text))
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if err := settings.Validate(d, defaults); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}

	// MachineHistory and PutBack accept both default brackets (pro, flash) and newly added brackets under agents.brackets.
	ctx := context.Background()
	db := dbtest.New(t, false)
	if err := settings.Save(ctx, db.Server, machine, settings.Merge(defaults, okDoc), "op"); err != nil {
		t.Fatal(err)
	}
	h, err := settings.MachineHistory(ctx, db.Server, machine, "agents.brackets.reasoning")
	if err != nil || len(h.Changes) != 1 || h.Default != nil {
		t.Fatalf("new bracket history: %+v %v", h, err)
	}
	if err := settings.PutBack(ctx, db.Server, machine, h.Changes[0].Seq, "agents.brackets.reasoning", "op", nil); err != nil {
		t.Fatalf("putBack new bracket: %v", err)
	}
	eff, _ := settings.Effective(ctx, db.Server, machine)
	if _, exists := eff["agents"].(settings.Doc)["brackets"].(settings.Doc)["reasoning"]; exists {
		t.Fatalf("new bracket remained after PutBack: %v", eff["agents"].(settings.Doc)["brackets"])
	}
}
