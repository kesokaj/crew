package models

import (
	"cmp"
	"strconv"
	"strings"
	"time"

	"github.com/kesokaj/crew/internal/cli"
)

// ClassifyBracket classifies a CLI model id and its offered efforts into "pro", "flash", "oss",
// or "" when the model cannot be deterministically classified (12.3, R190).
func ClassifyBracket(model string, efforts []string) string {
	if _, m, ok := strings.Cut(model, "/"); ok {
		model = m
	}
	low := strings.ToLower(strings.TrimSpace(model))
	if low == "" {
		return ""
	}
	if low == "gpt-oss" || strings.HasPrefix(low, "gpt-oss-") {
		return "oss"
	}
	tokens := splitModelTokens(low)
	for _, tok := range tokens {
		switch tok {
		case "flash", "lite", "mini", "nano", "haiku", "sonnet":
			return "flash"
		}
	}
	for _, tok := range tokens {
		switch tok {
		case "opus", "pro", "ultra":
			return "pro"
		}
	}
	if rest, ok := strings.CutPrefix(low, "gemini-"); ok && len(rest) > 0 && rest[0] >= '0' && rest[0] <= '9' {
		if distinctEfforts(efforts) >= 2 {
			return "pro"
		}
	}
	return ""
}

func splitModelTokens(low string) []string {
	return strings.FieldsFunc(low, func(r rune) bool {
		return r == '-' || r == '_' || r == '.' || r == '/'
	})
}

func distinctEfforts(efforts []string) int {
	seen := map[string]bool{}
	for _, e := range efforts {
		e = strings.TrimSpace(e)
		if e != "" {
			seen[e] = true
		}
	}
	return len(seen)
}

// ModelVersion is the parsed major.minor version of a model id (-1, -1 when unversioned).
type ModelVersion struct {
	Major int
	Minor int
}

// ParseModelVersion extracts the first 1-2 digit major(.minor) version token in model
// (e.g. "gemini-3.8-flash" -> 3.8, "gemini-4-carbon" -> 4.0, "claude-opus-5-5" -> 5.5),
// ignoring 4+ digit date stamps and parameter-size suffixes such as "120b".
func ParseModelVersion(model string) ModelVersion {
	if _, m, ok := strings.Cut(model, "/"); ok {
		model = m
	}
	parts := strings.Split(strings.ToLower(strings.TrimSpace(model)), "-")
	for i, p := range parts {
		if majStr, minStr, ok := strings.Cut(p, "."); ok {
			maj, ok1 := parseShortVersionInt(majStr)
			min, ok2 := parseShortVersionInt(minStr)
			if ok1 && ok2 {
				return ModelVersion{Major: maj, Minor: min}
			}
			continue
		}
		maj, ok := parseShortVersionInt(p)
		if !ok {
			continue
		}
		minor := 0
		if i+1 < len(parts) {
			if m, ok2 := parseShortVersionInt(parts[i+1]); ok2 {
				minor = m
			}
		}
		return ModelVersion{Major: maj, Minor: minor}
	}
	return ModelVersion{Major: -1, Minor: -1}
}

func parseShortVersionInt(s string) (int, bool) {
	if len(s) == 0 || len(s) > 2 {
		return 0, false
	}
	for i := 0; i < len(s); i++ {
		if s[i] < '0' || s[i] > '9' {
			return 0, false
		}
	}
	n, err := strconv.Atoi(s)
	return n, err == nil
}

// CompareModels orders two entries within the same (family, bracket) slot: higher major.minor version
// first, then newer min(first_seen), then baseline order, then entry name DESC.
func CompareModels(a string, aSeen time.Time, aBaseIdx int, b string, bSeen time.Time, bBaseIdx int) int {
	va, vb := ParseModelVersion(a), ParseModelVersion(b)
	if va.Major != vb.Major {
		return cmp.Compare(vb.Major, va.Major)
	}
	if va.Minor != vb.Minor {
		return cmp.Compare(vb.Minor, va.Minor)
	}
	if !aSeen.Equal(bSeen) {
		return bSeen.Compare(aSeen)
	}
	if aBaseIdx >= 0 && bBaseIdx >= 0 && aBaseIdx != bBaseIdx {
		return cmp.Compare(aBaseIdx, bBaseIdx)
	}
	if aBaseIdx >= 0 && bBaseIdx < 0 {
		return -1
	}
	if bBaseIdx >= 0 && aBaseIdx < 0 {
		return 1
	}
	return strings.Compare(b, a)
}

// TicketBracket returns "flash" for smaller builder tasks (part subtasks and docs/chore tickets) and
// for docs/chore ticket reviews (kind == "review", excluding custom "stage" and pre-build "analysis"),
// and "" otherwise (12.3, R46).
func TicketBracket(mrole, kind, ticketKind string) string {
	isDocsOrChore := ticketKind == "docs" || ticketKind == "chore"
	if mrole == "builder" && (kind == "part" || isDocsOrChore) {
		return "flash"
	}
	if mrole == "reviewer" && kind == "review" && isDocsOrChore {
		return "flash"
	}
	return ""
}

type roleSlot struct {
	family  string
	bracket string
}

func roleRecipe(role string) []roleSlot {
	switch role {
	case "curator", "lead", "planner", "coordinator", "researcher", "dreamer", "security", "assistant", "builder", "improver":
		return []roleSlot{{family: "gemini", bracket: "pro"}, {family: "gemini", bracket: "flash"}}
	case "tester", "e2e", "summary", "prompt":
		return []roleSlot{{family: "gemini", bracket: "flash"}, {family: "gemini", bracket: "pro"}}
	case "verifier":
		return []roleSlot{{family: "claude", bracket: "pro"}, {family: "gemini", bracket: "pro"}, {family: "gemini", bracket: "flash"}}
	case "reviewer":
		return []roleSlot{
			{family: "claude", bracket: "pro"},
			{family: "gemini", bracket: "pro"},
			{family: "claude", bracket: "flash"},
			{family: "gemini", bracket: "flash"},
		}
	case "judge":
		return []roleSlot{{family: "claude", bracket: "flash"}, {family: "gemini", bracket: "flash"}, {family: "gemini", bracket: "pro"}}
	default:
		return nil
	}
}

func entryFamily(entry string) string {
	if _, m, ok := strings.Cut(entry, "/"); ok {
		return cli.Family(m)
	}
	return cli.Family(entry)
}
