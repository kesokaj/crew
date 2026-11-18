package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"
)

// Model is one name a CLI lists (12.3): the name exactly as the CLI takes it, the model without its
// effort, and the efforts it offers. agy puts the effort into the name (gemini-3.8-flash-high), so
// each of its names offers one effort, or none for a model with one fixed effort.
type Model struct {
	Name    string   `json:"name"`
	Model   string   `json:"model"`
	Efforts []string `json:"efforts,omitempty"`
	// InName says the effort is part of the name, and is not passed as a flag.
	InName  bool   `json:"inName,omitempty"`
	Display string `json:"display,omitempty"`
	// Resolved is the full name an alias points at now, where the CLI says it. agy has no aliases;
	// the runs of claude before 0.42 keep theirs.
	Resolved string `json:"resolved,omitempty"`
}

// Asker runs a command, writes in to its standard input, and closes that once done says a line of
// standard output was the last one needed, or the command ended. err is set only when the command
// could not be run at all.
type Asker func(ctx context.Context, in []byte, done func(line []byte) bool, name string, args ...string) (stdout, stderr []byte, exit int, err error)

// EffortOrder is every effort a CLI names, from the least to the most (12.3).
var EffortOrder = []string{"minimal", "fast", "low", "medium", "high", "xhigh", "max"}

// agyEfforts are the last parts of an agy name that are an effort by themselves (12.3).
var agyEfforts = []string{"fast", "low", "medium", "high", "max"}

// splitAgy splits agy's names into their models and efforts (12.3): a last part of fast, low,
// medium, high, or max is the effort, and so is any last part in which two or more names differ and
// are otherwise the same. A name that ends in anything else is a model with one fixed effort.
func splitAgy(names, displays []string) []Model {
	stems := map[string]int{}
	for _, n := range names {
		if i := strings.LastIndex(n, "-"); i > 0 {
			stems[n[:i]]++
		}
	}
	out := make([]Model, 0, len(names))
	for k, n := range names {
		m := Model{Name: n, Model: n, InName: true}
		if k < len(displays) {
			m.Display = displays[k]
		}
		if i := strings.LastIndex(n, "-"); i > 0 && (slices.Contains(agyEfforts, n[i+1:]) || stems[n[:i]] >= 2) {
			m.Model, m.Efforts = n[:i], []string{n[i+1:]}
		}
		out = append(out, m)
	}
	return out
}

// agyModels reads `agy models`: one model on each line, its name and the name to show separated by
// a tab (spike item 47).
func agyModels(out string) []Model {
	var names, displays []string
	for _, line := range splitLines(out) {
		name, display, ok := strings.Cut(line, "\t")
		if ok && name != "" && !strings.Contains(name, " ") {
			names, displays = append(names, name), append(displays, strings.TrimSpace(display))
		}
	}
	return splitAgy(names, displays)
}

// ReadModels runs `agy models` (spike item 47); the login check reads the same list.
func (Agy) ReadModels(ctx context.Context, run Runner, _ Asker) ([]Model, error) {
	out, errb, code, err := run(ctx, "agy", "models")
	if err != nil {
		return nil, fmt.Errorf("agy could not be run: %w", err)
	}
	if code != 0 {
		return nil, fmt.Errorf("agy models ended with %d: %s", code, lastLine(append(out, errb...)))
	}
	m := agyModels(string(out))
	if len(m) == 0 {
		return nil, errors.New("agy models listed no model Crew can read")
	}
	return m, nil
}

// Named is a name the operator added beside agy's list (12.3), read by the same rule.
func (Agy) Named(name string) Model { return splitAgy([]string{name}, nil)[0] }

// ShortList is empty: agy has no aliases, so until its list was read once no run starts on it (12.3).
func (Agy) ShortList() []Model { return nil }

// LimitGroup is the group of agy's models whose limits a model shares (12.7, spike item 50): the
// Gemini models, and the Claude and GPT models.
func (Agy) LimitGroup(model string) string {
	if Family(model) == "gemini" {
		return "gemini"
	}
	return "3p"
}

// ModelLimit is "": agy's limits are its groups' only.
func (Agy) ModelLimit(string) string { return "" }

// Reached is the group a run at its limit marks: its model's group (spike item 50).
func (a Agy) Reached(model, _ string) string { return a.LimitGroup(model) }

// GroupName is how the vendor names a group of models in /usage.
func (Agy) GroupName(group string) string {
	if group == "gemini" {
		return "Gemini models"
	}
	return "Claude and GPT models"
}

// Limit is what a CLI said about one window of a group of models, or of one model (12.7).
type Limit struct {
	// Group is the group of models, or model:<name> for a model with a limit of its own.
	Group  string `json:"group"`
	Window string `json:"window"` // 5h or weekly
	// Used is the share of the window that is used, from 0 to 1.
	Used     float64   `json:"used"`
	ResetsAt time.Time `json:"resetsAt,omitempty"`
	// Reached says the CLI refuses work in this window until it resets.
	Reached bool `json:"reached,omitempty"`
}

// ReadLimits reads `agy -p /usage --output-format json` (spike item 50): no model request, one
// bucket for each window of each group, with what is left and when it resets.
func (Agy) ReadLimits(ctx context.Context, run Runner) ([]Limit, error) {
	out, errb, code, err := run(ctx, "agy", "-p", "/usage", "--output-format", "json")
	if err != nil {
		return nil, fmt.Errorf("agy could not be run: %w", err)
	}
	if code != 0 {
		return nil, fmt.Errorf("agy -p /usage ended with %d: %s", code, lastLine(append(out, errb...)))
	}
	return agyLimits(out)
}

func agyLimits(out []byte) ([]Limit, error) {
	var r struct {
		Command struct {
			Data struct {
				Groups []struct {
					Name    string `json:"name"`
					Buckets []struct {
						ID        string   `json:"id"`
						Window    string   `json:"window"`
						Remaining *float64 `json:"remaining_fraction"`
						Reset     string   `json:"reset_time"`
					} `json:"buckets"`
				} `json:"groups"`
			} `json:"data"`
		} `json:"command"`
	}
	if err := json.Unmarshal(bytes.TrimSpace(lastJSON(out)), &r); err != nil {
		return nil, fmt.Errorf("reading agy's /usage: %w", err)
	}
	var out2 []Limit
	for _, g := range r.Command.Data.Groups {
		for _, b := range g.Buckets {
			group, _, _ := strings.Cut(b.ID, "-")
			if group == "" || b.Remaining == nil {
				continue
			}
			l := Limit{Group: group, Window: b.Window, Used: 1 - *b.Remaining, Reached: *b.Remaining <= 0}
			if t, err := time.Parse(time.RFC3339, b.Reset); err == nil {
				l.ResetsAt = t
			}
			out2 = append(out2, l)
		}
	}
	if len(out2) == 0 {
		return nil, errors.New("agy's /usage named no limit")
	}
	return out2, nil
}

// lastJSON is the last line of out that is a JSON object, or out.
func lastJSON(out []byte) []byte {
	lines := bytes.Split(bytes.TrimSpace(out), []byte("\n"))
	for i := len(lines) - 1; i >= 0; i-- {
		if l := bytes.TrimSpace(lines[i]); len(l) > 0 && l[0] == '{' {
			return l
		}
	}
	return out
}
