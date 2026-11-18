package cli

import "strings"

// UnknownFamily is in the wait reason of a check whose model, or whose builder's model, is of a
// family Crew does not know (12.3): the guard in internal/runs writes it, and internal/inbox reads
// it from the ticket's stuck line (7.8).
const UnknownFamily = "of a family Crew does not know"

// Known says whether family is one Crew knows by name (12.3): Gemini, Claude, or GPT.
func Known(family string) bool {
	switch family {
	case "gemini", "claude", "gpt":
		return true
	}
	return false
}

// Family is a model's family, from its name (12.3): gemini- is Gemini; claude-, opus, sonnet, haiku,
// and fable are Claude; gpt- is GPT; anything else is its own family, for its results (Known says
// whether Crew knows the family, for the rule for checks). The aliases are claude's, kept for the
// runs it made before 0.42, whose results still count by family.
func Family(model string) string {
	m := strings.ToLower(strings.TrimSpace(model))
	switch {
	case strings.HasPrefix(m, "gemini-"):
		return "gemini"
	case strings.HasPrefix(m, "claude-"):
		return "claude"
	case strings.HasPrefix(m, "gpt-"):
		return "gpt"
	}
	for _, f := range []string{"opus", "sonnet", "haiku", "fable"} {
		if m == f || strings.HasPrefix(m, f+"-") || strings.HasPrefix(m, f+"[") {
			return "claude"
		}
	}
	return m
}
