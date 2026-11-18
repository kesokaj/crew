package mcp

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/kesokaj/crew/baseline"
)

// The improver's token reaches the Crew level's tools and the three a project's run shares with it, and
// nothing else of a project's; no project's run, nor a personal token, reaches a Crew level tool
// (10.4, 10.6).
func TestTheCrewLevelIsTheImproversOnly(t *testing.T) {
	words := map[string]baseline.Tool{
		"list_signals": {Description: "x", Roles: []string{"improver"}},
		"get_metrics":  {Description: "x", Roles: []string{"all", "improver"}},
		"add_note":     {Description: "x", Roles: []string{"all"}},
		"get_project":  {Description: "x", Roles: []string{"all", "token"}},
	}
	s := &Server{tools: map[string]*Tool{}, words: words}
	h := func(context.Context, Caller, json.RawMessage) (any, error) { return nil, nil }
	for _, tl := range []Tool{
		{Name: "list_signals", Scope: ScopeCrew, Handle: h, Input: map[string]any{}},
		{Name: "get_metrics", Scope: ScopeProject, AlsoCrew: true, Handle: h, Input: map[string]any{}},
		{Name: "add_note", Scope: ScopeProject, Handle: h, Input: map[string]any{}},
		{Name: "get_project", Scope: ScopeProject, Handle: h, Input: map[string]any{}},
	} {
		if err := s.Add(tl); err != nil {
			t.Fatal(err)
		}
	}
	improver := Caller{Run: "r", Role: "improver", Crew: true}
	builder := Caller{Run: "r", Role: "builder", Project: "p"}
	lead := Caller{Run: "r", Role: "lead", Project: "p"}
	for _, tc := range []struct {
		name string
		c    Caller
		tool string
		want bool
	}{
		{"the improver's own tool", improver, "list_signals", true},
		{"a shared tool, at the Crew level", improver, "get_metrics", true},
		{"a project's tool", improver, "add_note", false},
		{"a project's read tool", improver, "get_project", false},
		{"a builder at the Crew level", builder, "list_signals", false},
		{"a lead at the Crew level", lead, "list_signals", false},
		{"a builder's shared tool", builder, "get_metrics", true},
		{"a token's Crew flag with a project", Caller{Run: "r", Role: "improver", Crew: true, Project: "p"}, "list_signals", false},
		{"a role named improver without the flag", Caller{Run: "r", Role: "improver"}, "list_signals", false},
		{"a personal token", Caller{Token: 1, Role: "token", Projects: []string{"p"}}, "list_signals", false},
		{"the flag on another role", Caller{Run: "r", Role: "lead", Crew: true}, "list_signals", false},
	} {
		if got := s.Allowed(tc.c, tc.tool); got != tc.want {
			t.Errorf("%s: %v", tc.name, got)
		}
	}
}
