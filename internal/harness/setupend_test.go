package harness

import (
	"slices"
	"strings"
	"testing"
)

// A setup that fails in a check with no command is a result of its own, named for the setup, with the
// setup's end and what it printed; a setup that passes there gives no result, as before (13.9, since
// 1.11). The fake plays the same.
func TestASetupThatFailsWithNoCommandIsAResult(t *testing.T) {
	k, _, rt := kata(t)
	fails := true
	rt.answer = func(line string) (int, string, error) {
		if strings.HasSuffix(line, "make setup") && fails {
			return 2, "no rule", nil
		}
		return 0, "", nil
	}
	spec := CheckSpec{Tree: []byte("x"), Setup: "make setup"}
	want := []CheckResult{{Name: SetupCheck + "make setup", Code: 2, Error: SetupError + " ended with 2", Output: "no rule"}}
	res, err := k.ExecFresh(t.Context(), spec)
	if err != nil || !slices.Equal(res, want) {
		t.Fatalf("Kata: %+v %v", res, err)
	}
	fails = false
	if res, err := k.ExecFresh(t.Context(), spec); err != nil || len(res) != 0 {
		t.Fatalf("Kata, a setup that passes: %+v %v", res, err)
	}
	f := &Fake{CheckSetup: func(CheckSpec) CheckResult {
		return CheckResult{Name: "ignored", Passed: true, Code: 2, Error: SetupError + " ended with 2", Output: "no rule"}
	}}
	if res, err := f.ExecFresh(t.Context(), spec); err != nil || !slices.Equal(res, want) {
		t.Fatalf("the fake: %+v %v", res, err)
	}
	spec.Cmds = []CheckCmd{{"a", "true"}, {"b", "true"}}
	res, err = f.ExecFresh(t.Context(), spec)
	if err != nil || len(res) != 2 || res[0].Name != "a" || res[1].Name != "b" || res[0].Passed || res[1].Error != SetupError+" ended with 2" {
		t.Fatalf("the fake with commands: %+v %v", res, err)
	}
}
