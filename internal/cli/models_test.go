package cli

import (
	"context"
	"os"
	"slices"
	"strings"
	"testing"
	"time"
)

// agy's list is read as the spike recorded it on the operator's account (item 47): every name ends in
// its effort, and gpt-oss-120b has the one effort medium.
func TestAgysModelsAreSplitIntoModelAndEffort(t *testing.T) {
	run, _ := runner(t, map[string]answer{"agy models": {file: "agy/models.txt"}})
	got, err := Agy{}.ReadModels(context.Background(), run, nil)
	if err != nil {
		t.Fatal(err)
	}
	by := map[string]Model{}
	for _, m := range got {
		by[m.Name] = m
	}
	for name, want := range map[string][2]string{
		"gemini-3.8-flash-high":  {"gemini-3.8-flash", "high"},
		"claude-opus-5-5-low":    {"claude-opus-5-5", "low"},
		"claude-sonnet-5-5-high": {"claude-sonnet-5-5", "high"},
		"gpt-oss-120b-medium":    {"gpt-oss-120b", "medium"},
	} {
		m := by[name]
		if m.Model != want[0] || !slices.Equal(m.Efforts, []string{want[1]}) || !m.InName {
			t.Errorf("%s: %+v", name, m)
		}
	}
	if by["gemini-3.8-flash-high"].Display != "Gemini 3.8 Flash (High)" || len(got) != 10 {
		t.Fatalf("%+v", got)
	}

	// A last part two names differ in is an effort too; any other last part is the model's own.
	split := splitAgy([]string{"m-x-fast", "n-q1", "n-q2", "o-preview", "p"}, nil)
	want := []string{"m-x fast", "n q1", "n q2", "o-preview ", "p "}
	for i, m := range split {
		if got := m.Model + " " + strings.Join(m.Efforts, ","); got != want[i] {
			t.Errorf("%s: %q, want %q", m.Name, got, want[i])
		}
	}

	run, _ = runner(t, map[string]answer{"agy models": {file: "agy/refused-login-models.txt", code: 1}})
	if _, err := (Agy{}).ReadModels(context.Background(), run, nil); err == nil {
		t.Fatal("a refused list was read")
	}
	if (Agy{}).ShortList() != nil {
		t.Fatal("agy has no short list: no run starts on it before its list was read (12.3)")
	}
}

// fakeAsker answers with a recording, and records what was written and that standard input was
// closed after the line done accepted.
func fakeAsker(t *testing.T, file string) (Asker, *string) {
	var wrote string
	return func(_ context.Context, in []byte, done func([]byte) bool, name string, args ...string) ([]byte, []byte, int, error) {
		wrote = strings.Join(append([]string{name}, args...), " ") + "\n" + string(in)
		b, err := os.ReadFile("../../testdata/" + file)
		if err != nil {
			t.Fatal(err)
		}
		closed := false
		for _, l := range strings.Split(string(b), "\n") {
			if done([]byte(l)) {
				closed = true
			}
		}
		if !closed {
			t.Error("the answer was never accepted, so standard input would stay open")
		}
		return b, nil, 0, nil
	}, &wrote
}

// The groups of models whose limits each model shares (12.7, spike item 50).
func TestTheGroupsOfLimits(t *testing.T) {
	for _, c := range []struct {
		a            Adapter
		model, group string
		own          string
	}{
		{Agy{}, "gemini-3.8-flash", "gemini", ""},
		{Agy{}, "gemini-3.1-pro", "gemini", ""},
		{Agy{}, "claude-opus-5-5", "3p", ""},
		{Agy{}, "gpt-oss-120b", "3p", ""},
		{Agy{}, "gemini-4-argon", "gemini", ""},
	} {
		if g, own := c.a.LimitGroup(c.model), c.a.ModelLimit(c.model); g != c.group || own != c.own {
			t.Errorf("%s %s: %s %s", c.a.Name(), c.model, g, own)
		}
	}
}

// agy's /usage in JSON: one bucket for each window of each group (spike item 50).
func TestAgysLimitsAreReadFromUsage(t *testing.T) {
	run, _ := runner(t, map[string]answer{"agy -p /usage --output-format json": {file: "agy/usage-json.json"}})
	got, err := Agy{}.ReadLimits(context.Background(), run)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 4 || got[0].Group != "gemini" || got[0].Window != "weekly" || got[0].Used < 0.05 || got[0].Used > 0.07 ||
		got[0].ResetsAt.Format(time.RFC3339) != "2026-10-19T08:25:47Z" || got[2].Group != "3p" || got[1].Reached {
		t.Fatalf("%+v", got)
	}
	reached, err := agyLimits([]byte(`{"command":{"data":{"groups":[{"buckets":[{"id":"gemini-5h","window":"5h","remaining_fraction":0,"reset_time":"2026-10-13T16:31:48Z"}]}]}}}`))
	if err != nil || len(reached) != 1 || !reached[0].Reached || reached[0].Used != 1 {
		t.Fatalf("at the limit: %+v %v", reached, err)
	}
	if _, err := agyLimits([]byte("Gemini Models\tWeekly\t94%")); err == nil {
		t.Fatal("text was read as JSON")
	}
}
