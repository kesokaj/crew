package cli

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// fakeRun answers each command line with a recording, or with what the test gives.
type answer struct {
	file   string
	stdout string
	code   int
	err    error
}

func runner(t *testing.T, answers map[string]answer) (Runner, *[]string) {
	var asked []string
	return func(_ context.Context, name string, args ...string) ([]byte, []byte, int, error) {
		line := strings.Join(append([]string{name}, args...), " ")
		asked = append(asked, line)
		a, ok := answers[line]
		if !ok {
			t.Fatalf("unexpected command %q", line)
		}
		if a.err != nil {
			return nil, nil, -1, a.err
		}
		out := []byte(a.stdout)
		if a.file != "" {
			b, err := os.ReadFile("../../testdata/" + a.file)
			if err != nil {
				t.Fatal(err)
			}
			out = b
		}
		return out, nil, a.code, nil
	}, &asked
}

func TestAgyLoginCheck(t *testing.T) {
	ctx := context.Background()
	run, asked := runner(t, map[string]answer{"agy models": {file: "agy/models.txt"}, "agy -p /credits": {file: "agy/credits.txt"}})
	r := Agy{}.LoginCheck(ctx, run, false)
	if r.State != LoginOK || len(r.Models) != 10 || r.Models[0].Name != "gemini-3.8-flash-high" || r.Models[0].Model != "gemini-3.8-flash" {
		t.Fatalf("%+v", r)
	}
	if len(*asked) != 1 {
		t.Fatalf("the check that is not real ran %v", *asked)
	}
	r = Agy{}.LoginCheck(ctx, run, true)
	if r.State != LoginOK || r.Credits != "0" || !r.Real {
		t.Fatalf("real: %+v", r)
	}

	run, _ = runner(t, map[string]answer{"agy models": {file: "agy/refused-login-models.txt", code: 1}})
	if r := (Agy{}).LoginCheck(ctx, run, true); r.State != LoginRefused || !strings.Contains(r.Detail, "sign in") {
		t.Fatalf("refused: %+v", r)
	}
	run, _ = runner(t, map[string]answer{"agy models": {stdout: "panic: something else", code: 2}})
	if r := (Agy{}).LoginCheck(ctx, run, false); r.State != LoginError {
		t.Fatalf("another fault counted as a refused login: %+v", r)
	}
	run, _ = runner(t, map[string]answer{"agy models": {err: errors.New("no such file")}})
	if r := (Agy{}).LoginCheck(ctx, run, false); r.State != LoginError {
		t.Fatalf("a missing agy: %+v", r)
	}
	// The real check's /credits: only a sign-in error is a refusal; a timeout or another fault
	// could not tell, and changes no state.
	models := answer{file: "agy/models.txt"}
	for name, c := range map[string]struct {
		credits answer
		want    string
	}{
		"timeout":    {answer{err: errors.New("context deadline exceeded")}, LoginError},
		"network":    {answer{stdout: "error: dial tcp: i/o timeout", code: 1}, LoginError},
		"signed out": {answer{stdout: "Please sign in to continue", code: 1}, LoginRefused},
	} {
		run, _ = runner(t, map[string]answer{"agy models": models, "agy -p /credits": c.credits})
		if r := (Agy{}).LoginCheck(ctx, run, true); r.State != c.want {
			t.Errorf("/credits %s: %+v, want %s", name, r, c.want)
		}
	}
}

func TestTheAdapters(t *testing.T) {
	if !slices.Equal(Names(), []string{"agy"}) {
		t.Fatal(Names())
	}
	if a, ok := Get("agy"); !ok || a.LoginFile() == "" {
		t.Fatal("agy renews its own login file")
	}
	// agy is the only CLI since 0.42: claude is taken out, and no other is built.
	for _, other := range []string{"claude", "codex"} {
		if _, ok := Get(other); ok {
			t.Fatalf("%s is built", other)
		}
	}
}

// No non-test Go file outside internal/cli/ names the literal "agy" (AGENTS.md Agent CLIs 2,
// register row 55, CREW-42).
func TestNoGoFileOutsideInternalCLINamesAgy(t *testing.T) {
	root := filepath.Join("..", "..")
	var found []string
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		rel = filepath.ToSlash(rel)
		if d.IsDir() {
			if strings.HasPrefix(rel, ".") || rel == "internal/cli" || rel == "web" || rel == "testdata" {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(rel, ".go") || strings.HasSuffix(rel, "_test.go") {
			return nil
		}
		b, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		if strings.Contains(string(b), `"agy"`) {
			found = append(found, rel)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(found) != 0 {
		t.Fatalf("non-test Go files outside internal/cli/ naming \"agy\": %v", found)
	}
}
