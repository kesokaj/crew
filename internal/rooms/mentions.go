package rooms

import (
	"errors"
	"regexp"
	"strings"
)

// MaxMentionRecipients bounds fan-out per message to prevent runaway peer wakeups.
const MaxMentionRecipients = 10

// ErrTooManyMentions is returned when a message addresses more than MaxMentionRecipients handles.
var ErrTooManyMentions = errors.New("message exceeds maximum of 10 @mention recipients")

var mentionRe = regexp.MustCompile(`(?:^|[^A-Za-z0-9._])@([A-Za-z][A-Za-z0-9_-]{0,31})`)

// ExtractProseMentions extracts unique normalized @handles from CommonMark prose only,
// ignoring fenced code blocks (backticks/tildes with <=3 leading spaces), inline code spans,
// and '>' blockquote lines. Returns ErrTooManyMentions when unique handles exceed 10.
func ExtractProseMentions(markdown string) ([]string, error) {
	prose := extractProseText(markdown)
	matches := mentionRe.FindAllStringSubmatch(prose, -1)
	seen := map[string]bool{}
	out := make([]string, 0, len(matches))
	for _, m := range matches {
		if len(m) < 2 {
			continue
		}
		h := strings.ToLower(strings.TrimSpace(m[1]))
		if h == "" || seen[h] {
			continue
		}
		seen[h] = true
		out = append(out, h)
	}
	if len(out) > MaxMentionRecipients {
		return out, ErrTooManyMentions
	}
	return out, nil
}

// ParseMentions returns unique normalized @handles in order of appearance from prose only.
func ParseMentions(body string) []string {
	out, _ := ExtractProseMentions(body)
	if out == nil {
		return []string{}
	}
	return out
}

func extractProseText(markdown string) string {
	norm := strings.ReplaceAll(strings.ReplaceAll(markdown, "\r\n", "\n"), "\r", "\n")
	lines := strings.Split(norm, "\n")
	var kept []string

	var fenceChar byte
	fenceLen := 0
	for _, line := range lines {
		indent, rest := leadingIndent(line)
		if fenceLen > 0 {
			if indent <= 3 && isClosingFence(rest, fenceChar, fenceLen) {
				fenceLen = 0
				fenceChar = 0
			}
			continue
		}
		if indent <= 3 {
			if fc, fl, ok := parseOpeningFence(rest); ok {
				fenceChar, fenceLen = fc, fl
				continue
			}
			if strings.HasPrefix(rest, ">") {
				continue
			}
		}
		kept = append(kept, stripInlineCodeSpans(line))
	}
	return strings.Join(kept, "\n")
}

func leadingIndent(line string) (cols int, rest string) {
	i := 0
	for i < len(line) {
		if line[i] == ' ' {
			cols++
			i++
		} else if line[i] == '\t' {
			cols += 4 - (cols % 4)
			i++
		} else {
			break
		}
	}
	return cols, line[i:]
}

func parseOpeningFence(rest string) (byte, int, bool) {
	if len(rest) < 3 || (rest[0] != '`' && rest[0] != '~') {
		return 0, 0, false
	}
	ch := rest[0]
	n := 0
	for n < len(rest) && rest[n] == ch {
		n++
	}
	if n < 3 {
		return 0, 0, false
	}
	info := rest[n:]
	if ch == '`' && strings.ContainsRune(info, '`') {
		return 0, 0, false
	}
	return ch, n, true
}

func isClosingFence(rest string, ch byte, minLen int) bool {
	n := 0
	for n < len(rest) && rest[n] == ch {
		n++
	}
	if n < minLen {
		return false
	}
	return strings.Trim(rest[n:], " \t") == ""
}

func stripInlineCodeSpans(line string) string {
	var b strings.Builder
	b.Grow(len(line))
	i := 0
	for i < len(line) {
		if line[i] != '`' {
			b.WriteByte(line[i])
			i++
			continue
		}
		runLen := 0
		for i+runLen < len(line) && line[i+runLen] == '`' {
			runLen++
		}
		j := i + runLen
		found := -1
		for j < len(line) {
			if line[j] != '`' {
				j++
				continue
			}
			closeLen := 0
			for j+closeLen < len(line) && line[j+closeLen] == '`' {
				closeLen++
			}
			if closeLen == runLen {
				found = j + closeLen
				break
			}
			j += closeLen
		}
		if found >= 0 {
			b.WriteByte(' ')
			i = found
		} else {
			b.WriteString(line[i : i+runLen])
			i += runLen
		}
	}
	return b.String()
}
