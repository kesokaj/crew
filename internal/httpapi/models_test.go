package httpapi_test

import (
	"context"
	"encoding/json"
	"os"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/kesokaj/crew/internal/cli"
	"github.com/kesokaj/crew/internal/models"
	"github.com/kesokaj/crew/internal/models/modelstest"
)

// recordedAgy is agy's recorded list of models, as its login check reads it.
func recordedAgy(t *testing.T) []cli.Model {
	t.Helper()
	b, err := os.ReadFile("../../testdata/agy/models-gemini-4.txt")
	if err != nil {
		t.Fatal(err)
	}
	list, err := cli.Agy{}.ReadModels(context.Background(), func(context.Context, string, ...string) ([]byte, []byte, int, error) {
		return b, nil, 0, nil
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	return list
}

// The Models screen's routes (12.3, 15.4): every model each CLI offers, the agents with what each
// runs on, giving a model to an agent, Automatic and Set by you, a name the operator adds, a
// comparison, the limits, and the lead slots with the queue on the front page.
func TestTheModelsScreenReadsAndGivesModels(t *testing.T) {
	t.Parallel()
	k := newTicketKit(t)
	ctx := context.Background()
	var m struct {
		CLIs []struct {
			CLI    string
			Short  bool
			Models []struct {
				Name, Model, Family, Group, GroupName string
				Efforts                               []string
				Offered                               bool
			}
		}
		Agents []struct {
			Role, State, Effort, CheckRule string
			Entries                        []struct {
				Entry   string
				Offered bool
				Why     string
			}
			RunsOn *struct{ CLI, Model, Name, Effort, ModelFrom string }
		}
		Auto struct{ SetByYou []string }
	}
	k.call("GET", "/api/crew/models", "", 200, &m)
	if len(m.CLIs) != 1 || m.CLIs[0].CLI != "agy" || len(m.CLIs[0].Models) != 13 || m.CLIs[0].Short {
		t.Fatalf("%+v", m.CLIs)
	}
	flash := m.CLIs[0].Models[slices.IndexFunc(m.CLIs[0].Models, func(x struct {
		Name, Model, Family, Group, GroupName string
		Efforts                               []string
		Offered                               bool
	}) bool {
		return x.Name == "gemini-3.8-flash-high"
	})]
	if flash.Model != "gemini-3.8-flash" || flash.Family != "gemini" || flash.GroupName != "Gemini models" || flash.Efforts[0] != "high" {
		t.Fatalf("%+v", flash)
	}
	agent := func(role string) int {
		for i, a := range m.Agents {
			if a.Role == role {
				return i
			}
		}
		t.Fatalf("no agent %s", role)
		return -1
	}
	// agy no longer lists the builder's first entry, gemini-4-argon: it shows so, and the next runs.
	var listed []cli.Model
	for _, x := range recordedAgy(t) {
		if x.Model != "gemini-4-argon" {
			listed = append(listed, x)
		}
	}
	if err := models.Record(ctx, k.d.Server, "agy", listed, ""); err != nil {
		t.Fatal(err)
	}
	k.call("GET", "/api/crew/models", "", 200, &m)
	b := m.Agents[agent("builder")]
	if b.State != "automatic" || b.Entries[0].Entry != "agy/gemini-4-argon" || b.Entries[0].Offered || b.RunsOn.Name != "gemini-3.8-flash-high" {
		t.Fatalf("the builder: %+v", b)
	}
	if r := m.Agents[agent("reviewer")]; !strings.HasPrefix(r.CheckRule, "met:") || m.Agents[agent("curator")].State != "list" {
		t.Fatalf("the reviewer's rule: %q", r.CheckRule)
	}

	// Giving a model to an agent puts it first and sets the agent to Set by you; a model no CLI
	// offers cannot be given.
	k.call("PUT", "/api/crew/agents/builder", `{"model": "agy/gemini-3.8-flash", "effort": "medium"}`, 204, nil)
	k.call("PUT", "/api/crew/agents/builder", `{"model": "agy/gemini-4-argon"}`, 400, nil)
	// agy lists it again.
	modelstest.Recorded(t, k.d.Server)
	k.call("GET", "/api/crew/models", "", 200, &m)
	b = m.Agents[agent("builder")]
	if b.State != "set_by_you" || b.Entries[0].Entry != "agy/gemini-3.8-flash" || b.Effort != "medium" || b.RunsOn.Name != "gemini-3.8-flash-medium" ||
		!slices.Contains(m.Auto.SetByYou, "builder") {
		t.Fatalf("after giving a model: %+v %v", b, m.Auto.SetByYou)
	}
	k.call("PUT", "/api/crew/agents/builder/choice", `{"state": "automatic"}`, 204, nil)
	k.call("PUT", "/api/crew/agents/lead/choice", `{"state": "automatic"}`, 400, nil)
	k.call("GET", "/api/crew/models", "", 200, &m)
	if m.Agents[agent("builder")].State != "automatic" {
		t.Fatal("not back to Automatic")
	}

	// A name the operator adds is offered, and shown as added.
	k.call("POST", "/api/crew/models", `{"cli": "agy", "name": "gemini-4-argon-preview-high"}`, 204, nil)
	k.call("POST", "/api/crew/models", `{"cli": "agy", "name": "--model"}`, 400, nil)
	if n := k.count(`SELECT count(*) FROM models WHERE cli = 'agy' AND name = 'gemini-4-argon-preview-high' AND added_by = 'operator' AND offered`); n != 1 {
		t.Fatal("the added name is not offered")
	}

	// A comparison for the builder, one at a time, and stopped.
	var started struct{ ID int64 }
	k.call("POST", "/api/crew/comparisons", `{"role": "builder", "model": "agy/claude-sonnet-5-5", "effort": "low"}`, 201, &started)
	k.call("POST", "/api/crew/comparisons", `{"role": "builder", "model": "agy/claude-sonnet-5-5", "effort": "high"}`, 400, nil)
	k.call("POST", "/api/crew/comparisons", `{"role": "lead", "model": "agy/claude-sonnet-5-5", "effort": "high"}`, 400, nil)
	var cmps struct{ Comparisons []struct{ State string } }
	k.call("GET", "/api/crew/comparisons", "", 200, &cmps)
	if len(cmps.Comparisons) != 1 || cmps.Comparisons[0].State != "running" {
		t.Fatalf("%+v", cmps)
	}
	k.call("DELETE", "/api/crew/comparisons/"+strconv.FormatInt(started.ID, 10), "", 204, nil)
	k.call("DELETE", "/api/crew/comparisons/"+strconv.FormatInt(started.ID, 10), "", 404, nil)

	// A limit reached, by agy's Claude and GPT models: the quota and the news say so.
	if _, err := models.Reached(ctx, k.d.Server, "agy", "3p", "weekly", time.Now().Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	var q struct {
		Windows []struct{ CLI, GroupName string }
	}
	k.call("GET", "/api/crew/quota", "", 200, &q)
	if len(q.Windows) != 1 || q.Windows[0].GroupName != "Claude and GPT models" {
		t.Fatalf("%+v", q)
	}
	var feed struct{ Lines []struct{ Kind, Text string } }
	k.call("GET", "/api/activity", "", 200, &feed)
	if !slices.ContainsFunc(feed.Lines, func(l struct{ Kind, Text string }) bool {
		return l.Kind == "news" && strings.HasPrefix(l.Text, "agy: Claude and GPT models reached a limit")
	}) {
		t.Fatalf("no news of the limit: %+v", feed.Lines)
	}

	// The lead slots, and the queue.
	k.call("PUT", "/api/capacity/lead-slots", `{"cli": "agy", "slots": 3}`, 204, nil)
	k.call("PUT", "/api/capacity/lead-slots", `{"cli": "agy", "slots": -1}`, 400, nil)
	var ov struct {
		Lead struct {
			Slots []struct {
				CLI        string
				Slots, Set int
			}
			Queue []any
		}
	}
	k.call("GET", "/api/overview", "", 200, &ov)
	if len(ov.Lead.Slots) != 1 || ov.Lead.Slots[0].CLI != "agy" || ov.Lead.Slots[0].Slots != 3 || ov.Lead.Queue == nil {
		t.Fatalf("%+v", ov.Lead)
	}

	// One model: who uses it by default, and whose list has it. Opus is at its group's limit, so the
	// planner runs on another model now; the curator is on Gemini by default.
	var one struct{ ByDefault, ListedBy []string }
	k.call("GET", "/api/crew/models/agy/claude-opus-5-5", "", 200, &one)
	if slices.Contains(one.ByDefault, "curator") || !slices.Contains(one.ListedBy, "reviewer") {
		t.Fatalf("%+v", one)
	}
	k.call("GET", "/api/crew/models/agy/gemini-4-argon", "", 200, &one)
	if !slices.Contains(one.ByDefault, "curator") {
		t.Fatalf("%+v", one)
	}
	k.call("GET", "/api/crew/models/agy/gemini-3.8-flash", "", 200, &one)
	if !slices.Contains(one.ByDefault, "prompt") {
		t.Fatalf("%+v", one)
	}
}

// A ticket's agents and a project's: what each would run on now, with the pins, the project's own
// setting, and the rule for checks; and what can be chosen (12.3, 20.2).
func TestATicketsAndAProjectsAgents(t *testing.T) {
	t.Parallel()
	k := newTicketKit(t)
	k.call("POST", "/api/projects", `{"name": "shop", "key": "SF", "plan": "## Goal\n\nSell.\n", "stages": ["review"], "repository": {"kind": "local"}}`, 201, nil)
	k.call("POST", "/api/projects/shop/tasks", `{"title": "one", "why": "w", "what": "w", "goal": "g", "checks": [{"kind": "cmd", "spec": "true"}], "docsReason": "none"}`, 201, nil)
	k.call("PATCH", "/api/projects/shop/tasks/SF-1", `{"agents": {"builder": {"model": "agy/gemini-3.8-flash", "effort": "low"}}}`, 200, nil)
	type choice struct{ CLI, Model, Name, Effort, ModelFrom, Waits string }
	var a struct {
		Pins    map[string]map[string]string
		Roles   map[string]choice
		Offered []struct {
			Entry   string
			Efforts []string
		}
	}
	k.call("GET", "/api/projects/shop/tasks/SF-1/agents", "", 200, &a)
	if a.Pins["builder"]["model"] != "agy/gemini-3.8-flash" || a.Roles["builder"].Name != "gemini-3.8-flash-low" || a.Roles["builder"].ModelFrom != "ticket" {
		t.Fatalf("the builder: %+v", a)
	}
	// The builder is on Gemini, so the reviewer stays on Claude Opus, of another family.
	if a.Roles["reviewer"].CLI != "agy" || a.Roles["reviewer"].Model != "claude-opus-5-5" || len(a.Offered) != 5 {
		t.Fatalf("the reviewer, or what is offered: %+v %d", a.Roles["reviewer"], len(a.Offered))
	}
	var doc map[string]any
	k.call("GET", "/api/projects/shop/settings", "", 200, &doc)
	doc["agents"] = map[string]any{"tester": map[string]any{"model": "agy/claude-sonnet-5-5", "effort": "low"}}
	b, _ := json.Marshal(doc)
	k.call("PUT", "/api/projects/shop/settings", string(b), 200, nil)
	var p struct {
		Roles     map[string]choice
		Own       map[string]map[string]string
		CheckRule bool
	}
	k.call("GET", "/api/projects/shop/agents", "", 200, &p)
	if p.Roles["tester"].Name != "claude-sonnet-5-5-low" || p.Roles["tester"].ModelFrom != "project" || p.Own["tester"]["effort"] != "low" || !p.CheckRule {
		t.Fatalf("%+v", p)
	}
}

// The Models screen names no price (12.7, since 0.53): Crew estimates no cost, and a model's row has
// no price and no "priced"; each model agy lists is there.
func TestTheModelsScreenNamesNoPrice(t *testing.T) {
	t.Parallel()
	k := newTicketKit(t)
	var m struct {
		CLIs []struct {
			Models []map[string]any
		}
	}
	k.call("GET", "/api/crew/models", "", 200, &m)
	if len(m.CLIs) != 1 || len(m.CLIs[0].Models) == 0 {
		t.Fatalf("%+v", m.CLIs)
	}
	for _, x := range m.CLIs[0].Models {
		if _, ok := x["price"]; ok {
			t.Errorf("%s has a price: %v", x["name"], x)
		}
		if _, ok := x["priced"]; ok {
			t.Errorf("%s says whether it is priced: %v", x["name"], x)
		}
	}
}

// A ticket past its budget and one long in its stage are news in the project's activity (7.7, 8.2).
func TestBudgetAndSlowStageAreNews(t *testing.T) {
	t.Parallel()
	k := newTicketKit(t)
	k.shop(nil, nil, "One")
	ctx := context.Background()
	if _, err := k.d.Server.Exec(ctx, `INSERT INTO signals (project, ticket, kind, detail)
		SELECT t.project, t.id, k, jsonb_build_object('text', x) FROM tickets t,
		(VALUES ('over_budget', 'SF-1 passed its budget: an estimated $4.00 of $3.00. The work carries on'),
			('slow_stage', 'SF-1 has been in Review for 2h0m0s, 4 times its usual 30m0s there')) v(k, x)`); err != nil {
		t.Fatal(err)
	}
	var feed struct {
		Lines []struct {
			Kind, Text string
			Ticket     *string
		}
	}
	k.call("GET", "/api/projects/shop/activity", "", 200, &feed)
	news := 0
	for _, l := range feed.Lines {
		if l.Kind == "news" && l.Ticket != nil && *l.Ticket == "SF-1" && strings.HasPrefix(l.Text, "SF-1 ") {
			news++
		}
	}
	if news != 2 {
		t.Fatalf("%d lines of news, want 2: %+v", news, feed.Lines)
	}
}
