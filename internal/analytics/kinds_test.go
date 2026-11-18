package analytics

import (
	"strings"
	"testing"
	"unicode/utf8"
)

// Every row of 10.2 has a kind that records it, and each kind is on the list once.
func TestEveryRowOfTheSignalsHasAKind(t *testing.T) {
	seen := map[string]bool{}
	rows := map[Row]bool{}
	for _, k := range Kinds {
		if seen[k.Kind] {
			t.Errorf("%s is on the list twice", k.Kind)
		}
		seen[k.Kind] = true
		if k.Label == "" {
			t.Errorf("%s has no words", k.Kind)
		}
		rows[k.Row] = true
	}
	for _, r := range []Row{RowIntervention, RowDecidedAlone, RowCheckRounds, RowRework, RowEscapedDefect, RowAnalysisDecision,
		RowWatchdog, RowFeedback, RowRepeatedLesson, RowOverride, RowStaleDocs, RowFlakyCheck, RowInstalledTool,
		RowCostAndTime, RowRewriteChoice, RowSandboxStartup, RowEvalResult, RowAskedAbout} {
		if !rows[r] {
			t.Errorf("no kind records the row %s of 10.2", r)
		}
	}
	if KindOf("nothing").Label != "nothing" || KindOf("rework").Label != "Fix round" {
		t.Error("KindOf")
	}
}

// Every kind Latest signals lists says what it holds, from the detail its recorder writes, and a kind
// on the list with no recorded detail here fails (dave, 0.2.5: 23 of 30 lines repeated the signal's
// name; the review of the fix: feedback still did).
func TestEveryKindSaysWhatItHolds(t *testing.T) {
	// The detail each recorder writes, as it writes it, and the line it gives.
	recorded := map[string]struct {
		detail map[string]any
		want   string
	}{
		"operator_intervention": {map[string]any{"what": "limit_raised", "text": "You raised the budget"}, "You raised the budget"},
		"merged_outside":        {map[string]any{"text": "ML-1 was merged on GitHub by ann"}, "ML-1 was merged on GitHub by ann"},
		"check_round":           {map[string]any{"kind": "build", "round": 3.0, "last": true}, "Round 3 of the checks failed on a build request: the last round"},
		"rework": {map[string]any{"reason": "merge", "stage": "review", "text": "The merge with main failed: The ticket's branch conflicts with main in: a.txt"},
			"The merge with main failed: The ticket's branch conflicts with main in: a.txt"},
		"escaped_defect":           {map[string]any{"bug": 3.0, "severity": "high", "foundBy": "tester", "fixTicket": "WC-9", "text": "The count is off by one"}, "The count is off by one"},
		"feedback":                 {map[string]any{"feedback": 3.0, "about": "builder", "kind": "correction"}, "A correction of the builder's work (feedback 0003)"},
		"tool_installed":           {map[string]any{"command": "apt-get install jq", "text": "apt-get install jq"}, "apt-get install jq"},
		CreatedDiscarded:           {map[string]any{"choice": "discard", "origin": "agent"}, "Discarded a ticket an agent created"},
		"docs_behind":              {map[string]any{"page": "docs/a.md", "by": "WC-2", "text": "docs/a.md is behind WC-2"}, "docs/a.md is behind WC-2"},
		"watchdog_warning":         {map[string]any{"warning": "the same command ran five times"}, "the same command ran five times"},
		"watchdog_stop":            {map[string]any{"reason": "no progress", "detail": "…"}, "no progress"},
		"lesson_recorded":          {map[string]any{"entry": 4.0, "appliesTo": []any{"builder"}, "keywords": []any{}, "text": "Run go vet"}, "Run go vet"},
		"over_budget":              {map[string]any{"spentUsd": 2.5, "budgetUsd": 2.0, "text": "WC-3 passed its budget"}, "WC-3 passed its budget"},
		"look_again":               {map[string]any{"choice": "cancel_and_replace", "replacements": 2.0}, "The lead replaced the ticket with 2 tickets"},
		"decided_alone":            {map[string]any{"decision": 2.0}, "Decision d_0002"},
		"flaky_check":              {map[string]any{"checkRun": 4.0, "purpose": "merge", "cmds": []any{"go test ./..."}}, "The merge check passed when it was run again: go test ./..."},
		"override":                 {map[string]any{"what": "applied", "override": 1.0, "path": "AGENTS.md", "text": "shop applies o_1 to AGENTS.md"}, "shop applies o_1 to AGENTS.md"},
		"eval_result":              {map[string]any{"suite": 1.0, "purpose": "on_demand", "verdict": "pass", "text": "shop: 2 of 2 cases"}, "shop: 2 of 2 cases"},
		"rewrite_choice":           {map[string]any{"kind": "improve", "round": 1.0, "state": "ready", "choice": "keep", "rewrite": 3.0}, "You kept your own text"},
		"analysis_decision":        {map[string]any{"choice": "approve", "origin": "plan"}, "Approved a ticket of the plan"},
		"bug_reported":             {map[string]any{"bug": 3.0, "fixTicket": "WC-9", "severity": "high"}, "Bug 0003 (high): WC-9 fixes it"},
		"ticket_created":           {map[string]any{"ticket": "WC-13"}, "An agent created WC-13"},
		"new_approach":             {map[string]any{"reason": "stuck", "text": "The lead takes WC-4 another way"}, "The lead takes WC-4 another way"},
		"cancel_and_replace":       {map[string]any{"reason": "too big", "replacements": []any{"WC-5"}, "missing": false, "text": "WC-4 is replaced by WC-5"}, "WC-4 is replaced by WC-5"},
		"skill_requested":          {map[string]any{"ticket": "WC-14", "joined": true}, "An agent asked for a skill: it joined WC-14"},
		"housekeeping":             {map[string]any{"feedback": 2.0, "indexSize": 40.0, "indexCap": 60.0, "pagesMarked": 1.0}, "Housekeeping read 2 feedback, with the index at 40 of 60 lines, and marked 1 pages"},
		"slow_stage":               {map[string]any{"stage": "review", "usualSeconds": 60.0, "text": "WC-6 has been in Review for 2h"}, "WC-6 has been in Review for 2h"},
		"setup_failed":             {map[string]any{"since": "2026-10-19T12:00:00Z", "text": "Setup fails on main"}, "Setup fails on main"},
		"no_github_token":          {map[string]any{"sandbox": "s1", "kind": "run", "error": "the App is not installed"}, "A run sandbox got no GitHub token: the App is not installed"},
		"model_gone":               {map[string]any{"role": "builder", "ignored": []any{"agy/gemini-2"}, "ran": "agy/gemini-3-pro"}, "agy/gemini-2 is no longer offered: the builder ran on agy/gemini-3-pro in its place"},
		"check_rule_not_met":       {map[string]any{"role": "reviewer", "family": "claude"}, "The reviewer ran on the claude family too: no CLI in use offers another"},
		"ticket_failure_hold":      {map[string]any{"failures": 4.0, "limit": 4.0, "text": "WC-4 was held after 4 failures"}, "WC-4 was held after 4 failures"},
		"base_check_not_run":       {map[string]any{"detail": "main has no go.mod"}, "main has no go.mod"},
		"improvement":              {map[string]any{"improvement": 2.0, "verdict": "better", "text": "i_0002 measured better"}, "i_0002 measured better"},
		"improver_note":            {map[string]any{"text": "The improver: look at the reviews"}, "The improver: look at the reviews"},
		"research_circuit_breaker": {map[string]any{"text": "FC-1 handed off to @researcher after 2/2 tries"}, "FC-1 handed off to @researcher after 2/2 tries"},
		"spec_decision":            {map[string]any{"text": "SPEC-001 decided autonomously (DEC-001)"}, "SPEC-001 decided autonomously (DEC-001)"},
		"chat_hop_limit":           {map[string]any{"text": "Stopped @researcher reply in feat/001-cache at hop 4/3"}, "Stopped @researcher reply in feat/001-cache at hop 4/3"},
		"asked_about":              {map[string]any{"ticket": "WC-3", "stage": "done", "marked": true, "text": "You asked about WC-3, Done, on marked text"}, "You asked about WC-3, Done, on marked text"},
	}
	// Measurements, which Latest signals leaves out.
	left := map[string]bool{"sandbox_startup": true, "cost_and_time": true}
	for _, k := range Kinds {
		if left[k.Kind] {
			continue
		}
		r, ok := recorded[k.Kind]
		if !ok {
			t.Errorf("%s: no recorded detail in this test", k.Kind)
			continue
		}
		if got := What(k.Kind, r.detail); got != r.want || got == k.Label {
			t.Errorf("%s: What %q, want %q", k.Kind, got, r.want)
		}
	}
	if got := What("decided_alone", map[string]any{"decision": 2.0, "question": "Which parser?", "choice": "stdlib", "role": "builder"}); got != "Which parser? The builder chose: stdlib" {
		t.Errorf("decided_alone with its decision: %q", got)
	}
	// An agent's question is its own words: one plain line, cut short (review of the fix).
	long := "Which\u202e parser?\n" + strings.Repeat("x", 300)
	if got := What("decided_alone", map[string]any{"question": long, "choice": "stdlib", "role": "builder"}); strings.ContainsAny(got, "\u202e\n") ||
		utf8.RuneCountInString(got) > 240 || !strings.HasPrefix(got, "Which  parser? x") {
		t.Errorf("decided_alone with a long question: %q", got)
	}
	// Invisible characters and line separators too (round 2 of the review).
	if got := What("decided_alone", map[string]any{"question": "Which\u200b\ufeff\u2028\u2029parser?", "choice": "stdlib", "role": "builder"}); got != "Which    parser? The builder chose: stdlib" {
		t.Errorf("decided_alone with invisible characters: %q", got)
	}
	// A fix round recorded before 0.2.7 has its stage's word only (dave, 0.2.6: "merge"): its line says
	// what sent the work back.
	for reason, want := range map[string]string{
		"merge": "The merge with main failed", "review": "The review asked for changes", "test": "The tests failed",
		"end-to-end tests": "The end-to-end tests failed", "checks": "The checks failed", "operator": "You refused a change",
		"verification": "The verification found that it does not do what was asked", "stage docs": "The stage docs failed",
	} {
		if got := What("rework", map[string]any{"reason": reason, "stage": "review"}); got != want {
			t.Errorf("rework for %s, recorded before 0.2.7: %q, want %q", reason, got, want)
		}
	}
	if got := What("flaky_check", map[string]any{"purpose": "merge"}); got != "The merge check passed when it was run again" {
		t.Errorf("flaky_check with no commands: %q", got)
	}
}

// The line of a fix round says what failed with the first line of what was found, as one plain line
// of at most 200 characters of the finding: the stage's word alone said nothing (dave, 0.2.6).
func TestAFixRoundSaysWhatFailed(t *testing.T) {
	for _, c := range []struct{ reason, found, want string }{
		{"merge", "The ticket's branch conflicts with main in: a.txt. Merge main into the branch.\nmore", "The merge with main failed: The ticket's branch conflicts with main in: a.txt. Merge main into the branch."},
		{"test", "$ go test ./...\n--- FAIL: TestCount\nexit code 1\n\n", "The tests failed: go test ./..."},
		{"review", "\n\n## Changes requested\n\nThe count is off by one.", "The review asked for changes: The count is off by one."},
		{"review", "# Review\n## Findings\n", "The review asked for changes: Review"},
		{"test", "$ go test -race ./...\nexit code 1", "The tests failed: go test -race ./..."},
		{"review", "**Verdict**: changes", "The review asked for changes: **Verdict**: changes"},
		{"checks", "$ -race-check\nexit code 1", "The checks failed: -race-check"},
		// A line of markers only says nothing, and one marker is taken off, not one of each (round 2).
		{"review", "---\n>\n-\n$\n* * *\nThe count is off by one.", "The review asked for changes: The count is off by one."},
		{"review", "- > quoted", "The review asked for changes: > quoted"},
		{"stage docs", "- the README says how to run it\n  The judge: it does not", "The stage docs failed: the README says how to run it"},
		{"checks", "", "The checks failed"},
		{"review", "\u202e\u200bfine", "The review asked for changes: fine"},
	} {
		if got := FixRound(c.reason, c.found); got != c.want {
			t.Errorf("FixRound(%q, %q) = %q, want %q", c.reason, c.found, got, c.want)
		}
	}
	got := FixRound("merge", strings.Repeat("x", 300))
	if n := utf8.RuneCountInString(strings.TrimPrefix(got, "The merge with main failed: ")); n != 201 || !strings.HasSuffix(got, "…") {
		t.Errorf("a long finding: %d characters, %q", n, got)
	}
}
