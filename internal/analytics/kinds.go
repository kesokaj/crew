// Package analytics computes the numbers of a project (9.7) and of the Crew level (10.7) from the
// records, the runs, and the signals, and keeps them for a while in metrics.
package analytics

import (
	"fmt"
	"strings"
	"unicode"
)

// A Row is one row of the table of signals in 10.2: what a signal can point at.
type Row string

// The rows of 10.2.
const (
	RowIntervention     Row = "intervention"
	RowDecidedAlone     Row = "decided_alone"
	RowCheckRounds      Row = "check_rounds"
	RowRework           Row = "rework"
	RowEscapedDefect    Row = "escaped_defect"
	RowAnalysisDecision Row = "analysis_decision"
	RowWatchdog         Row = "watchdog"
	RowFeedback         Row = "feedback"
	RowAskedAbout       Row = "asked_about"
	RowRepeatedLesson   Row = "repeated_lesson"
	RowOverride         Row = "override"
	RowStaleDocs        Row = "stale_docs"
	RowFlakyCheck       Row = "flaky_check"
	RowInstalledTool    Row = "installed_tool"
	RowCostAndTime      Row = "cost_and_time"
	RowRewriteChoice    Row = "rewrite_choice"
	RowSandboxStartup   Row = "sandbox_startup"
	RowEvalResult       Row = "eval_result"
	// Crew's own signals that are news or a record of a step, and no row of 10.2.
	RowOther Row = ""
)

// Kind is one kind of signal as the backend records it, with the words the screens use.
type Kind struct {
	Kind  string `json:"kind"`
	Label string `json:"label"`
	Row   Row    `json:"row"`
	// Counted is whether the Crew level counts it in its signals by kind.
	Counted bool `json:"counted"`
}

// CreatedDiscarded is not recorded as such: it is an analysis decision that discarded a created ticket,
// counted apart because the screens show it apart.
const CreatedDiscarded = "created_discarded"

// Kinds are every kind of signal Crew records, in the order the Crew level shows them. Agents send none
// (10.2): each is recorded by the backend from events and records.
var Kinds = []Kind{
	{"operator_intervention", "Operator intervention", RowIntervention, true},
	{"merged_outside", "Work merged outside Crew", RowIntervention, true},
	{"check_round", "Checks failed on a request", RowCheckRounds, true},
	{"rework", "Fix round", RowRework, true},
	{"escaped_defect", "Bug found after Done", RowEscapedDefect, true},
	{"feedback", "Feedback", RowFeedback, true},
	{"tool_installed", "Tool installed in a run", RowInstalledTool, true},
	{CreatedDiscarded, "Created ticket discarded", RowAnalysisDecision, true},
	{"docs_behind", "Documentation behind the code", RowStaleDocs, true},
	{"watchdog_warning", "Watchdog warning", RowWatchdog, true},
	{"watchdog_stop", "Watchdog stop", RowWatchdog, true},
	{"lesson_recorded", "Lesson recorded", RowRepeatedLesson, true},
	// No ticket passes a budget since 0.53: a stored one keeps its label, and is not counted.
	{"over_budget", "Ticket passed its budget (before 0.53)", RowDecidedAlone, false},
	{"look_again", "Lead looked at a ticket again", RowDecidedAlone, true},
	{"decided_alone", "Agent decided an open choice", RowDecidedAlone, true},
	{"flaky_check", "Check that passed when run again", RowFlakyCheck, true},
	{"override", "Override of the baseline", RowOverride, true},
	{"eval_result", "Eval result", RowEvalResult, true},
	{"rewrite_choice", "Choice of a rewrite", RowRewriteChoice, true},
	// A question in a ticket's chat, or a comment on marked text there (11.9); the question itself is
	// not in it.
	{"asked_about", "Asked about a ticket", RowAskedAbout, true},
	{"analysis_decision", "Created ticket analysed", RowAnalysisDecision, false},
	// The kind keeps its stored name; since 0.53 it holds a ticket's tokens and minutes, and no cost.
	{"cost_and_time", "Tokens and time of a ticket", RowCostAndTime, false},
	{"sandbox_startup", "Sandbox start-up", RowSandboxStartup, false},
	{"bug_reported", "Bug reported", RowOther, false},
	{"ticket_created", "Ticket created by an agent", RowOther, false},
	{"new_approach", "New approach", RowOther, false},
	{"cancel_and_replace", "Ticket replaced", RowOther, false},
	{"skill_requested", "Skill requested", RowOther, false},
	{"housekeeping", "Housekeeping", RowOther, false},
	{"slow_stage", "Ticket long in its stage", RowOther, false},
	{"setup_failed", "Setup failed", RowOther, false},
	{"no_github_token", "No GitHub token", RowOther, false},
	{"model_gone", "Model no longer offered", RowOther, false},
	{"check_rule_not_met", "Rule for checks not met", RowOther, false},
	{"ticket_failure_hold", "Ticket held after repeated failures", RowOther, false},
	{"base_check_not_run", "Base check not run", RowOther, false},
	{"improvement", "Improvement measured", RowOther, false},
	{"improver_note", "Note from the improver", RowOther, false},
	{"research_circuit_breaker", "Researcher circuit breaker", RowOther, false},
	{"spec_decision", "Feature spec decided", RowDecidedAlone, true},
	{"chat_hop_limit", "Chat hop limit reached", RowOther, false},
}

// KindOf returns a kind by its name; a kind not on the list is shown by its name.
func KindOf(name string) Kind {
	for _, k := range Kinds {
		if k.Kind == name {
			return k
		}
	}
	return Kind{Kind: name, Label: name}
}

// displayKind is the SQL that gives a signal row its kind as counted: a discard of a created ticket is
// counted apart from the other analysis decisions.
const displayKind = `CASE WHEN g.kind = 'analysis_decision' AND g.detail->>'choice' = 'discard' THEN '` + CreatedDiscarded + `' ELSE g.kind END`

// What says what a signal holds, in the words of the Crew level's Latest signals (10.7). A signal
// recorded with a text of its own says that; the others are told from their detail, because a line
// that only repeats the signal's name tells nothing. It is "" for a detail it cannot read.
func What(kind string, d map[string]any) string {
	// A fix round's reason is the stage's one word; one recorded before 0.2.7 has no text.
	if kind == "rework" && str(d, "text") == "" {
		return FixRound(str(d, "reason"), "")
	}
	for _, k := range []string{"text", "reason", "warning", "command"} {
		if v := str(d, k); v != "" {
			return v
		}
	}
	switch kind {
	case "analysis_decision", CreatedDiscarded:
		choice := map[string]string{"approve": "Approved", "notice": "Gave notice of", "skip": "Skipped", "discard": "Discarded"}[str(d, "choice")]
		origin := map[string]string{"plan": "a ticket of the plan", "agent": "a ticket an agent created", "operator": "a ticket you created"}[str(d, "origin")]
		if choice == "" {
			return ""
		}
		if origin == "" {
			origin = "a created ticket"
		}
		return choice + " " + origin
	case "check_round":
		if num(d, "round") == 0 {
			return ""
		}
		out := fmt.Sprintf("Round %d of the checks failed on a %s request", num(d, "round"), or(str(d, "kind"), "work"))
		if b, _ := d["last"].(bool); b {
			out += ": the last round"
		}
		return out
	case "flaky_check":
		out := fmt.Sprintf("The %s check passed when it was run again", or(str(d, "purpose"), "ticket's"))
		if cmds := strs(d, "cmds"); len(cmds) > 0 {
			out += ": " + strings.Join(cmds, ", ")
		}
		return out
	case "look_again":
		switch str(d, "choice") {
		case "new_approach":
			return "The lead chose a new approach"
		case "cancel_and_replace":
			return fmt.Sprintf("The lead replaced the ticket with %d tickets", num(d, "replacements"))
		}
	case "feedback":
		if n := num(d, "feedback"); n > 0 {
			what := map[string]string{"praise": "Praise for", "correction": "A correction of"}[str(d, "kind")]
			return fmt.Sprintf("%s the %s's work (feedback %04d)", or(what, "Feedback on"), or(str(d, "about"), "agent"), n)
		}
	case "decided_alone":
		// The question and the choice are an agent's words, as long as it wrote them: a line of the
		// list keeps a short, plain part of each.
		if q, c := plain(str(d, "question"), 200), plain(str(d, "choice"), 200); q != "" && c != "" {
			return fmt.Sprintf("%s The %s chose: %s", q, or(str(d, "role"), "agent"), c)
		}
		if n := num(d, "decision"); n > 0 {
			return fmt.Sprintf("Decision d_%04d", n)
		}
	case "rewrite_choice":
		return map[string]string{
			"first/rewrite":  "You sent Crew's rewrite of your first message",
			"first/edited":   "You sent Crew's rewrite of your first message, edited",
			"first/mine":     "You sent your own first message",
			"improve/use":    "You used Crew's improved text",
			"improve/edited": "You used Crew's improved text, edited",
			"improve/keep":   "You kept your own text",
			"review/seen":    "You read Crew's review of your text",
		}[str(d, "kind")+"/"+str(d, "choice")]
	case "bug_reported":
		if n := num(d, "bug"); n > 0 {
			return fmt.Sprintf("Bug %04d (%s): %s fixes it", n, or(str(d, "severity"), "no severity"), or(str(d, "fixTicket"), "a ticket"))
		}
	case "ticket_created":
		if k := str(d, "ticket"); k != "" {
			return "An agent created " + k
		}
	case "skill_requested":
		if k := str(d, "ticket"); k != "" {
			if b, _ := d["joined"].(bool); b {
				return "An agent asked for a skill: it joined " + k
			}
			return "An agent asked for a skill: " + k
		}
	case "housekeeping":
		return fmt.Sprintf("Housekeeping read %d feedback, with the index at %d of %d lines, and marked %d pages",
			num(d, "feedback"), num(d, "indexSize"), num(d, "indexCap"), num(d, "pagesMarked"))
	case "no_github_token":
		return strings.TrimSuffix(fmt.Sprintf("A %s sandbox got no GitHub token: %s", or(str(d, "kind"), "run"), plain(str(d, "error"), 200)), ": ")
	case "model_gone":
		if gone := strs(d, "ignored"); len(gone) > 0 {
			return fmt.Sprintf("%s is no longer offered: the %s ran on %s in its place", strings.Join(gone, ", "), or(str(d, "role"), "agent"), or(str(d, "ran"), "the machine's model"))
		}
	case "check_rule_not_met":
		if r, f := str(d, "role"), str(d, "family"); r != "" && f != "" {
			return fmt.Sprintf("The %s ran on the %s family too: no CLI in use offers another", r, f)
		}
	case "base_check_not_run":
		return str(d, "detail")
	}
	return ""
}

// FixRound is the line of a fix round (7.4): what sent the work back, by the reason its recorder
// gives, and the first line of what was found. The finding is an agent's words or a command's output,
// so it is one plain line, cut short.
func FixRound(reason, found string) string {
	what := map[string]string{
		"review":           "The review asked for changes",
		"verification":     "The verification found that it does not do what was asked",
		"test":             "The tests failed",
		"end-to-end tests": "The end-to-end tests failed",
		"checks":           "The checks failed",
		"merge":            "The merge with main failed",
		"operator":         "You refused a change",
	}[reason]
	if what == "" {
		what = "The " + or(reason, "work") + " failed"
	}
	// The first line of a check's failure is its command ("$ go test"), and of a judge's a list item:
	// one marker is taken off its front. A record's heading ("## Findings") says less than the line
	// after it, so it is used only when the record has nothing else.
	heading := ""
	for _, line := range strings.Split(found, "\n") {
		line = plain(line, 200)
		if h := strings.TrimSpace(strings.TrimLeft(line, "#")); h != line {
			if heading == "" {
				heading = h
			}
			continue
		}
		for _, mark := range []string{"$ ", "- ", "* ", "> "} {
			if strings.HasPrefix(line, mark) {
				line = strings.TrimSpace(strings.TrimPrefix(line, mark))
				break
			}
		}
		// A line of markers only, such as a rule ("---"), says nothing.
		if strings.ContainsFunc(line, func(r rune) bool { return unicode.IsLetter(r) || unicode.IsDigit(r) }) {
			return what + ": " + line
		}
	}
	if heading != "" {
		return what + ": " + heading
	}
	return what
}

func str(d map[string]any, k string) string {
	s, _ := d[k].(string)
	return strings.TrimSpace(s)
}

// num is a whole number of a detail; JSON gives every number as a float.
func num(d map[string]any, k string) int {
	f, _ := d[k].(float64)
	return int(f)
}

func strs(d map[string]any, k string) []string {
	l, _ := d[k].([]any)
	out := []string{}
	for _, v := range l {
		if s, ok := v.(string); ok && s != "" {
			out = append(out, s)
		}
	}
	return out
}

// plain is s on one line, without control, invisible, or direction-changing characters, cut to at
// most n characters.
func plain(s string, n int) string {
	var b strings.Builder
	count := 0
	for _, r := range s {
		if unicode.IsControl(r) || unicode.In(r, unicode.Bidi_Control, unicode.Cf, unicode.Zl, unicode.Zp) {
			r = ' '
		}
		if count == n {
			return strings.TrimSpace(b.String()) + "…"
		}
		b.WriteRune(r)
		count++
	}
	return strings.TrimSpace(b.String())
}

func or(s, otherwise string) string {
	if s == "" {
		return otherwise
	}
	return s
}
