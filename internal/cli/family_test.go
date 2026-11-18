package cli

import "testing"

func TestTheFamilyComesFromTheName(t *testing.T) {
	for name, want := range map[string]string{
		"gemini-3.1-pro": "gemini", "gemini-3.8-flash-low": "gemini", "claude-opus-5-5": "claude", "opus": "claude",
		"sonnet": "claude", "haiku": "claude", "fable": "claude", "opus[1m]": "claude", "gpt-9": "gpt", "gpt-oss-120b": "gpt", "llama-4": "llama-4", "Sonnet": "claude",
		"opusx": "opusx",
	} {
		if got := Family(name); got != want {
			t.Errorf("%s: %s, not %s", name, got, want)
		}
	}
}

func TestAFamilyIsKnownOnlyForGeminiClaudeAndGPT(t *testing.T) {
	for name, want := range map[string]bool{
		"gemini-4-argon": true, "claude-opus-5-5": true, "opus": true, "gpt-oss-120b": true,
		"nova-1": false, "llama-4": false, "": false, "opusx": false,
	} {
		if got := Known(Family(name)); got != want {
			t.Errorf("%s: %v, not %v", name, got, want)
		}
	}
}

func TestAFolderAnIndexNamesIsMatchedAtTheRoot(t *testing.T) {
	named, _ := Named([]byte(`{"entries": [{"path": "tools"}, {"path": "/etc"}, {"path": "../up"}, {"path": "~/x"}], "inherits": [{"path": "./a/b"}]}`))
	if len(named) != 2 || named[0] != "tools/" || named[1] != "a/b/" {
		t.Fatalf("%v", named)
	}
	for path, want := range map[string]bool{"tools/p/hooks.json": true, "tools": true, "x/tools/p.json": false, "toolsx/a": false, "a/b/c": true, "a": true} {
		if Matches(path, named) != want {
			t.Errorf("%s: %v", path, !want)
		}
	}
}
