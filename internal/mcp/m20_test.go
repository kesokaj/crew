package mcp

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/kesokaj/crew/baseline"
)

// The assistant of a conversation is matched against the word assistant, and of a ticket's chat
// against the word chat; the word all reaches neither, and a conversation about no project reaches
// ScopeConversation tools and ScopeProject tools marked AlsoConversation (11.8, 11.9; design 1.33).
func TestTheAssistantReachesOnlyTheToolsThatNameIt(t *testing.T) {
	words := map[string]baseline.Tool{
		"get_project":   {Description: "x", Roles: []string{"all", "assistant", "chat"}},
		"get_plan":      {Description: "x", Roles: []string{"all", "assistant", "chat"}},
		"get_crew_map":  {Description: "x", Roles: []string{"assistant", "token"}},
		"add_note":      {Description: "x", Roles: []string{"all"}},
		"create_ticket": {Description: "x", Roles: []string{"all", "assistant"}},
		"get_diff":      {Description: "x", Roles: []string{"conversation", "chat"}},
	}
	s := &Server{tools: map[string]*Tool{}, words: words}
	h := func(context.Context, Caller, json.RawMessage) (any, error) { return nil, nil }
	for _, name := range []string{"get_project", "add_note", "create_ticket", "get_diff"} {
		if err := s.Add(Tool{Name: name, Scope: ScopeProject, Handle: h, Input: map[string]any{}}); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.Add(Tool{Name: "get_plan", Scope: ScopeProject, AlsoConversation: true, Handle: h, Input: map[string]any{}}); err != nil {
		t.Fatal(err)
	}
	if err := s.Add(Tool{Name: "get_crew_map", Scope: ScopeConversation, Handle: h, Input: map[string]any{}}); err != nil {
		t.Fatal(err)
	}
	talk := Caller{Run: "r", Role: "assistant", Project: "p", Conversation: "c"}
	chat := Caller{Run: "r", Role: "assistant", Project: "p", Conversation: "c", ChatTicket: 7}
	none := Caller{Run: "r", Role: "assistant", Conversation: "c"}
	for _, tc := range []struct {
		name string
		c    Caller
		tool string
		want bool
	}{
		{"a conversation reads", talk, "get_project", true},
		{"a conversation proposes a ticket", talk, "create_ticket", true},
		{"a conversation writes no note", talk, "add_note", false},
		{"a conversation has no chat's tool", talk, "get_diff", false},
		{"a project conversation has no crew map tool", talk, "get_crew_map", false},
		{"a chat reads", chat, "get_project", true},
		{"a chat has its own tool", chat, "get_diff", true},
		{"a chat creates no ticket", chat, "create_ticket", false},
		{"a chat writes no note", chat, "add_note", false},
		{"no project reaches no plain project tool", none, "get_project", false},
		{"normal conversation reaches AlsoConversation read tool", none, "get_plan", true},
		{"normal conversation reaches ScopeConversation tool", none, "get_crew_map", true},
		{"normal conversation creates no ticket", none, "create_ticket", false},
		{"the assistant with no conversation", Caller{Run: "r", Role: "assistant", Project: "p"}, "get_project", false},
	} {
		if got := s.Allowed(tc.c, tc.tool); got != tc.want {
			t.Errorf("%s: %v", tc.name, got)
		}
	}
}
