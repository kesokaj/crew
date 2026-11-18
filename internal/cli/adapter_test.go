package cli

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"testing"
	"time"
)

func TestAgySubagentsDisabledInSettingsAndACP(t *testing.T) {
	home := t.TempDir()
	a := Agy{}
	if !a.SubagentsDisabled() {
		t.Fatal("expected Agy.SubagentsDisabled() == true")
	}
	if cmd := a.ACPCommand(); !slices.Equal(cmd, []string{"agy", "acp", "--stdio", "--process-per-session"}) {
		t.Fatalf("unexpected ACPCommand: %v", cmd)
	}
	if err := a.PrepareHome(home); err != nil {
		t.Fatalf("PrepareHome: %v", err)
	}
	checkSettings := func(phase string, wantAllowLen int) {
		t.Helper()
		b, err := os.ReadFile(filepath.Join(home, agySettings))
		if err != nil {
			t.Fatalf("%s read settings.json: %v", phase, err)
		}
		var doc map[string]any
		if err := json.Unmarshal(b, &doc); err != nil {
			t.Fatalf("%s decode settings.json: %v", phase, err)
		}
		if doc["subagentsEnabled"] != false {
			t.Fatalf("%s subagentsEnabled = %v, want false", phase, doc["subagentsEnabled"])
		}
		perms, _ := doc["permissions"].(map[string]any)
		allow, _ := perms["allow"].([]any)
		if len(allow) != wantAllowLen {
			t.Fatalf("%s allow = %v, want len %d", phase, allow, wantAllowLen)
		}
		deny, _ := perms["deny"].([]any)
		if len(deny) != len(agyDenySubagents) {
			t.Fatalf("%s deny = %v, want %v", phase, deny, agyDenySubagents)
		}
		for _, want := range agyDenySubagents {
			if !slices.Contains(deny, any(want)) {
				t.Fatalf("%s missing %q in permissions.deny: %v", phase, want, deny)
			}
		}
	}
	checkSettings("PrepareHome", 0)

	if err := SanitizeHome(home, "/home/agent/worktrees/run-1", true); err != nil {
		t.Fatalf("SanitizeHome: %v", err)
	}
	checkSettings("SanitizeHome", len(agyAllow))

	desc := DescribeRuntime()
	if desc.ActiveCLI != "agy" || !desc.SubagentsDisabled || !slices.Contains(desc.FutureAdapterSeams, "pi") || !slices.Contains(desc.FutureAdapterSeams, "claude") {
		t.Fatalf("unexpected DescribeRuntime: %+v", desc)
	}
	if !IsDisabledSubagentTool("invoke_subagent") || !IsDisabledSubagentTool("swarm(*)") || IsDisabledSubagentTool("run_command") {
		t.Fatal("unexpected IsDisabledSubagentTool classification")
	}
	if !ForbiddenEnv("ANTHROPIC_API_KEY") {
		t.Fatal("expected ANTHROPIC_API_KEY forbidden")
	}
}

// stubPiAdapter proves a future minimal CLI such as `pi` satisfies ACPAdapter cleanly inside
// `internal/cli/` without modifying the orchestrator or adding it to active `adapters` until ready.
type stubPiAdapter struct{ Agy }

func (stubPiAdapter) Name() string            { return "pi" }
func (stubPiAdapter) ACPCommand() []string    { return []string{"pi", "--mode", "rpc"} }
func (stubPiAdapter) SubagentsDisabled() bool { return true }
func (stubPiAdapter) IsolationScope() string  { return IsolationDedicatedKataVM }
func (stubPiAdapter) Capabilities() Capabilities {
	return Capabilities{RPC: true, Resume: true, SystemPrompt: true, StreamableHTTPMCP: true, UsageTokens: true, SubagentsDisabledOK: true}
}
func (stubPiAdapter) AuthSpec() AuthSpec {
	return AuthSpec{Method: "subscription-oauth", OAuthFile: ".pi/agent/auth.json", FileMode: 0o600, PreserveOnRefresh: true, ReadOnlyProjectTrust: "never"}
}
func (stubPiAdapter) ForbiddenEnv(name string) bool {
	return MultiProviderForbiddenEnv(name)
}
func (stubPiAdapter) LoginCheck(context.Context, Runner, bool) LoginResult {
	return LoginResult{CLI: "pi", State: LoginOK}
}
func (stubPiAdapter) RealEvery() time.Duration { return time.Hour }

type unsafeSharedVMSecondary struct{ stubPiAdapter }

func (unsafeSharedVMSecondary) IsolationScope() string { return IsolationProjectWarmVM }

func TestFutureAdapterSeamCompilesCleanly(t *testing.T) {
	var seam ACPAdapter = stubPiAdapter{}
	if seam.Name() != "pi" || !seam.SubagentsDisabled() || seam.IsolationScope() != IsolationDedicatedKataVM {
		t.Fatalf("unexpected seam behavior: name=%s subagentsDisabled=%v scope=%s", seam.Name(), seam.SubagentsDisabled(), seam.IsolationScope())
	}
	if err := ValidateSecondaryAdapter(seam); err != nil {
		t.Fatalf("ValidateSecondaryAdapter(stubPiAdapter): %v", err)
	}
	if err := ValidateSecondaryAdapter(unsafeSharedVMSecondary{}); err == nil {
		t.Fatal("expected ValidateSecondaryAdapter to reject shared project warm VM isolation")
	}
	if len(seam.ACPCommand()) == 0 {
		t.Fatal("expected ACPCommand on seam")
	}
	for _, k := range []string{"ANTHROPIC_API_KEY", "OPENAI_API_KEY", "OPENROUTER_API_KEY", "DEEPSEEK_API_KEY"} {
		if !ForbiddenEnv(k) || !seam.ForbiddenEnv(k) {
			t.Fatalf("expected %s blocked by ForbiddenEnv", k)
		}
	}
	if _, ok := Get("pi"); ok {
		t.Fatal("stub pi must not be registered in active adapters before its milestone")
	}
	if !slices.Equal(Names(), []string{"agy"}) {
		t.Fatalf("active adapters changed unexpectedly: %v", Names())
	}

	home := t.TempDir()
	written, err := WriteOAuthCredentialFile(home, ".pi/agent/auth.json", []byte(`{"token":"initial-oauth"}`), true)
	if err != nil || !written {
		t.Fatalf("first WriteOAuthCredentialFile: written=%v err=%v", written, err)
	}
	fi, err := os.Stat(filepath.Join(home, ".pi", "agent", "auth.json"))
	if err != nil || fi.Mode().Perm() != 0o600 {
		t.Fatalf("expected 0600 oauth file, got mode=%v err=%v", fi.Mode().Perm(), err)
	}
	written2, err := WriteOAuthCredentialFile(home, ".pi/agent/auth.json", []byte(`{"token":"clobber"}`), true)
	if err != nil || written2 {
		t.Fatalf("expected PreserveOnRefresh to skip overwrite, got written=%v err=%v", written2, err)
	}
	gotBytes, _ := os.ReadFile(filepath.Join(home, ".pi", "agent", "auth.json"))
	if !stringContains(string(gotBytes), "initial-oauth") {
		t.Fatalf("OAuth file was clobbered on refresh: %q", gotBytes)
	}
}

func stringContains(s, sub string) bool {
	return len(s) >= len(sub) && (s == sub || indexStr(s, sub) >= 0)
}

func indexStr(s, sub string) int {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return i
		}
	}
	return -1
}
