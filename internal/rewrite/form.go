package rewrite

import (
	"strings"
	"unicode/utf8"
)

// form is a rewrite read from the prompt writer's answer in its fixed form (baseline/stages/rewrite.md).
type form struct {
	good                            bool
	rewrite                         string
	changes, assumptions, questions []string
}

var rewriteHeadings = []string{"## Rewrite", "## Changes", "## Assumptions", "## Questions"}

// readRewrite reads an answer: GOOD AS IT IS alone, or the four headings in their order and nothing
// before them. Anything else is not the form, and the operator's own text stays as it is.
func readRewrite(answer string) (form, bool) {
	a := strings.TrimSpace(answer)
	if a == "GOOD AS IT IS" {
		return form{good: true}, true
	}
	secs, ok := sectionsOf(a, rewriteHeadings)
	if !ok || secs[0] == "" || utf8.RuneCountInString(secs[0]) > MaxText {
		return form{}, false
	}
	f := form{rewrite: secs[0], changes: list(secs[1]), assumptions: list(secs[2]), questions: list(secs[3])}
	if len(f.questions) > maxQuestions {
		return form{}, false
	}
	// An assumption is a sentence of the rewrite: one that is not is not marked, and so not listed.
	kept := f.assumptions[:0]
	for _, x := range f.assumptions {
		if strings.Contains(f.rewrite, x) {
			kept = append(kept, x)
		}
	}
	f.assumptions = kept
	return f, true
}

// readNotes reads a review: the one heading, and at most five notes under it.
func readNotes(answer string) ([]string, bool) {
	secs, ok := sectionsOf(strings.TrimSpace(answer), []string{"## Notes"})
	if !ok {
		return nil, false
	}
	notes := list(secs[0])
	if len(notes) == 0 || len(notes) > maxNotes {
		return nil, false
	}
	return notes, true
}

// sectionsOf cuts text at its headings, which must come in their order, each once, as a whole line,
// the first at the start of the text. A line that only begins like a heading, such as "## Changes to
// make" inside a rewrite, is text of its section.
func sectionsOf(text string, headings []string) ([]string, bool) {
	lines := strings.Split(text, "\n")
	at := make([]int, len(headings))
	next := 0
	for i, l := range lines {
		if next < len(headings) && strings.TrimRight(l, " \t\r") == headings[next] {
			if next == 0 && i != 0 {
				return nil, false
			}
			at[next] = i
			next++
		}
	}
	if next != len(headings) {
		return nil, false
	}
	out := make([]string, len(headings))
	for k := range headings {
		end := len(lines)
		if k+1 < len(headings) {
			end = at[k+1]
		}
		out[k] = strings.TrimSpace(strings.Join(lines[at[k]+1:end], "\n"))
	}
	return out, true
}

// list reads the lines of a section that begin with "- "; a section that says none has none.
func list(sec string) []string {
	out := []string{}
	if strings.EqualFold(strings.TrimSpace(sec), "none") {
		return out
	}
	for _, l := range strings.Split(sec, "\n") {
		l = strings.TrimSpace(l)
		if item, ok := strings.CutPrefix(l, "- "); ok && strings.TrimSpace(item) != "" && !strings.EqualFold(strings.TrimSpace(item), "none") {
			out = append(out, strings.TrimSpace(item))
		}
	}
	return out
}
