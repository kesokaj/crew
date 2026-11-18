// Package redact removes secrets from what Crew stores from a sandbox (design 8.3): events, turns,
// the CLI's own files, and the output of commands. It removes by value, every secret Crew handed
// that sandbox, wherever it appears, and by shape, a whole line that holds a needle and a whole
// key block. A JSON line keeps its form: only the strings in it that change are written again, so a
// stored line still parses, and is otherwise the line the CLI printed.
package redact

import (
	"bytes"
	"encoding/json"
	"regexp"
	"slices"
	"strings"
	"unicode/utf8"
)

// Marker is what replaces a removed value.
const Marker = "[redacted]"

// minValue is the shortest value removed by value: a shorter one would match ordinary text.
const minValue = 8

// Redactor removes the secrets of one sandbox.
type Redactor struct {
	values []string
}

// New returns a Redactor that removes values, and every secret shape. Values shorter than eight
// characters are not removed by value; a secret is longer.
func New(values ...string) *Redactor {
	r := &Redactor{}
	for _, v := range values {
		v = strings.TrimSpace(v)
		if len(v) >= minValue && !slices.Contains(r.values, v) {
			r.values = append(r.values, v)
		}
	}
	// The longest first, so a value inside another goes with the longer one.
	slices.SortFunc(r.values, func(a, b string) int { return len(b) - len(a) })
	return r
}

// Text removes secrets from s: each value, then each line that holds a needle, then each key block.
func (r *Redactor) Text(s string) string {
	for _, v := range r.values {
		s = strings.ReplaceAll(s, v, Marker)
	}
	lines := strings.SplitAfter(s, "\n")
	var b strings.Builder
	changed := false
	for i := 0; i < len(lines); i++ {
		raw := lines[i]
		body := strings.TrimSuffix(raw, "\n")
		if blockOpen.MatchString(body) {
			end := i
			for end < len(lines) && !blockClose.MatchString(lines[end]) {
				end++
			}
			if end == len(lines) {
				end = len(lines) - 1
			}
			b.WriteString("[redacted: a key]")
			b.WriteString(lines[end][len(strings.TrimSuffix(lines[end], "\n")):])
			i, changed = end, true
			continue
		}
		if !mayHold(body) {
			b.WriteString(raw)
			continue
		}
		if n, ok := needleIn(body); ok {
			b.WriteString("[redacted: " + n + "]")
			b.WriteString(raw[len(body):])
			changed = true
			continue
		}
		b.WriteString(raw)
	}
	if !changed {
		return s
	}
	return b.String()
}

// jsonString matches a string literal of JSON.
var jsonString = regexp.MustCompile(`"(?:[^"\\]|\\.)*"`)

// Line removes secrets from one line a CLI printed. A JSON line keeps its form: each string in it
// is decoded, so a value behind an escape is found, and written again only when it changed. A line
// that is not JSON is redacted as text.
func (r *Redactor) Line(line []byte) []byte {
	if !json.Valid(line) {
		return []byte(r.Text(string(line)))
	}
	changed := false
	out := jsonString.ReplaceAllFunc(line, func(lit []byte) []byte {
		var s string
		if json.Unmarshal(lit, &s) != nil {
			return lit
		}
		red := r.Text(s)
		if red == s {
			return lit
		}
		changed = true
		b, _ := json.Marshal(red)
		return b
	})
	if !changed {
		return line
	}
	if !json.Valid(out) {
		// A secret whose removal broke the line: what is kept is only that it was there.
		return []byte(`{"redacted":"the line held a secret and is not kept"}`)
	}
	return out
}

// Value removes secrets from a decoded value: every string in it, at any depth.
func (r *Redactor) Value(v any) any {
	switch t := v.(type) {
	case string:
		return r.Text(t)
	case map[string]any:
		out := make(map[string]any, len(t))
		for k, x := range t {
			out[r.Text(k)] = r.Value(x)
		}
		return out
	case []any:
		out := make([]any, len(t))
		for i, x := range t {
			out[i] = r.Value(x)
		}
		return out
	}
	return v
}

// File removes secrets from a file of a conversation's work folder (11.8, plan M20 D33): a file that
// is valid UTF-8 as Text, any other by value only, so the line rules never cut a binary file.
func (r *Redactor) File(b []byte) []byte {
	if utf8.Valid(b) {
		return []byte(r.Text(string(b)))
	}
	for _, v := range r.values {
		b = bytes.ReplaceAll(b, []byte(v), []byte(Marker))
	}
	return b
}

// Bytes removes secrets from the content of a file: as lines of JSON when every line is one, and
// as text otherwise. Binary content is checked by value only, since a needle in it says nothing.
func (r *Redactor) Bytes(b []byte) []byte {
	if bytes.IndexByte(b, 0) >= 0 {
		for _, v := range r.values {
			b = bytes.ReplaceAll(b, []byte(v), bytes.Repeat([]byte("x"), len(v)))
		}
		return b
	}
	lines := bytes.SplitAfter(b, []byte("\n"))
	out := make([]byte, 0, len(b))
	for _, l := range lines {
		body := bytes.TrimSuffix(l, []byte("\n"))
		if len(body) > 0 && json.Valid(body) {
			out = append(out, r.Line(body)...)
			out = append(out, l[len(body):]...)
			continue
		}
		return []byte(r.Text(string(b)))
	}
	return out
}
