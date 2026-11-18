package cli

import (
	"fmt"
	"io"
	"os"
	"path"
	"slices"
	"strings"
)

const (
	// IsolationProjectWarmVM identifies the shared per-project warm Kata microVM home used by primary `agy`.
	IsolationProjectWarmVM = "project-warm-kata-vm"
	// IsolationDedicatedKataVM requires a dedicated per-CLI Kata microVM and isolated home directory
	// for any future secondary CLI (`claude`, `pi`) so credentials never co-mingle (Safety Rule 8).
	IsolationDedicatedKataVM = "dedicated-kata-vm"
)

// Capabilities declares protocol and runtime features supported by an adapter seam.
type Capabilities struct {
	ACP                 bool `json:"acp"`
	RPC                 bool `json:"rpc"`
	Resume              bool `json:"resume"`
	SystemPrompt        bool `json:"systemPrompt"`
	StreamableHTTPMCP   bool `json:"streamableHttpMcp"`
	UsageTokens         bool `json:"usageTokens"`
	SubagentsDisabledOK bool `json:"subagentsDisabledOk"`
}

// AuthSpec declares subscription-only OAuth credential custody and trust defaults (Safety Rule 11).
type AuthSpec struct {
	Method               string `json:"method"`
	OAuthFile            string `json:"oauthFile"`
	FileMode             uint32 `json:"fileMode"`
	PreserveOnRefresh    bool   `json:"preserveOnRefresh"`
	ReadOnlyProjectTrust string `json:"readOnlyProjectTrust,omitempty"`
	AllowsAPIKey         bool   `json:"allowsApiKey"`
}

// SecondarySeamSpec documents an inactive secondary CLI seam and its mandatory isolation invariants.
type SecondarySeamSpec struct {
	CLI            string       `json:"cli"`
	Active         bool         `json:"active"`
	IsolationScope string       `json:"isolationScope"`
	Command        []string     `json:"command"`
	Capabilities   Capabilities `json:"capabilities"`
	AuthSpec       AuthSpec     `json:"authSpec"`
	ForbiddenEnv   []string     `json:"forbiddenEnv"`
}

// ACPAdapter extends Adapter for Harness Runtime CLIs supporting persistent JSON-RPC 2.0 ACP
// sessions (`1 session = 1 peer agent`) with internal CLI subagents disabled (design 11.10, 12.2,
// R212). Future minimal adapters such as `pi` or `claude` implement this interface inside
// `internal/cli/` without changing the orchestrator, rooms, or WORKFLOW.md.
type ACPAdapter interface {
	Adapter
	// ACPCommand returns the command line that starts the CLI as a multi-session ACP server.
	ACPCommand() []string
	// SubagentsDisabled reports whether internal CLI-native subagent spawning is turned off so
	// every peer delegation goes through Crew's Agent API and Tasks API.
	SubagentsDisabled() bool
	// IsolationScope returns whether this CLI runs in the project's warm VM or requires a dedicated Kata VM.
	IsolationScope() string
	// Capabilities returns declarative protocol/session feature flags for the CLI.
	Capabilities() Capabilities
	// AuthSpec describes subscription OAuth credential custody (never raw API keys).
	AuthSpec() AuthSpec
}

// RuntimeDescriptor summarizes the active Harness Runtime adapter and the adapter seam open for
// future minimal CLIs (`GET /api/agents/runtime`).
type RuntimeDescriptor struct {
	ActiveCLI          string              `json:"activeCli"`
	Protocol           string              `json:"protocol"`
	Command            []string            `json:"command"`
	SubagentsDisabled  bool                `json:"subagentsDisabled"`
	DisabledTools      []string            `json:"disabledTools"`
	IsolationScope     string              `json:"isolationScope"`
	Capabilities       Capabilities        `json:"capabilities"`
	AuthSpec           AuthSpec            `json:"authSpec"`
	FutureAdapterSeams []string            `json:"futureAdapterSeams"`
	FutureSeamSpecs    []SecondarySeamSpec `json:"futureSeamSpecs"`
}

// ACPCommand returns the command line that starts `agy` as a warm Agent Client Protocol (ACP)
// JSON-RPC 2.0 server with one worker process per session (`1 session = 1 peer agent`, design 12.2,
// 11.10).
func (Agy) ACPCommand() []string {
	return []string{"agy", "acp", "--stdio", "--process-per-session"}
}

// SubagentsDisabled reports that `agy` runs with internal subagents disabled so every delegation
// is an inspectable peer session tracked in Tasks API (design 11.10, R212).
func (Agy) SubagentsDisabled() bool { return true }

// IsolationScope reports `agy`'s per-project warm Kata microVM scope.
func (Agy) IsolationScope() string { return IsolationProjectWarmVM }

// Capabilities describes `agy` ACP runtime capabilities.
func (Agy) Capabilities() Capabilities {
	return Capabilities{
		ACP:                 true,
		RPC:                 false,
		Resume:              true,
		SystemPrompt:        true,
		StreamableHTTPMCP:   true,
		UsageTokens:         true,
		SubagentsDisabledOK: true,
	}
}

// AuthSpec describes `agy`'s subscription-only credential posture.
func (Agy) AuthSpec() AuthSpec {
	return AuthSpec{
		Method:               "subscription-oauth",
		OAuthFile:            ".gemini/oauth_creds.json",
		FileMode:             0o600,
		PreserveOnRefresh:    true,
		ReadOnlyProjectTrust: "never",
		AllowsAPIKey:         false,
	}
}

// FutureSeamSpecs returns inactive, hardened seam contracts for future secondary CLIs.
func FutureSeamSpecs() []SecondarySeamSpec {
	env := MultiProviderForbiddenEnvNames()
	return []SecondarySeamSpec{
		{
			CLI:            "pi",
			Active:         false,
			IsolationScope: IsolationDedicatedKataVM,
			Command:        []string{"pi", "--mode", "rpc"},
			Capabilities: Capabilities{
				ACP:                 false,
				RPC:                 true,
				Resume:              true,
				SystemPrompt:        true,
				StreamableHTTPMCP:   true,
				UsageTokens:         true,
				SubagentsDisabledOK: true,
			},
			AuthSpec: AuthSpec{
				Method:               "subscription-oauth",
				OAuthFile:            ".pi/agent/auth.json",
				FileMode:             0o600,
				PreserveOnRefresh:    true,
				ReadOnlyProjectTrust: "never",
				AllowsAPIKey:         false,
			},
			ForbiddenEnv: env,
		},
		{
			CLI:            "claude",
			Active:         false,
			IsolationScope: IsolationDedicatedKataVM,
			Command:        []string{"claude", "--output-format", "stream-json"},
			Capabilities: Capabilities{
				ACP:                 false,
				RPC:                 false,
				Resume:              true,
				SystemPrompt:        true,
				StreamableHTTPMCP:   true,
				UsageTokens:         true,
				SubagentsDisabledOK: true,
			},
			AuthSpec: AuthSpec{
				Method:               "subscription-oauth",
				OAuthFile:            ".claude/.credentials.json",
				FileMode:             0o600,
				PreserveOnRefresh:    true,
				ReadOnlyProjectTrust: "never",
				AllowsAPIKey:         false,
			},
			ForbiddenEnv: env,
		},
	}
}

// DescribeRuntime returns the active Harness Runtime adapter descriptor and open adapter seams.
func DescribeRuntime() RuntimeDescriptor {
	a := Agy{}
	var acp ACPAdapter = a
	return RuntimeDescriptor{
		ActiveCLI:          acp.Name(),
		Protocol:           "acp-jsonrpc-2.0",
		Command:            acp.ACPCommand(),
		SubagentsDisabled:  acp.SubagentsDisabled(),
		DisabledTools:      DisabledSubagentTools(),
		IsolationScope:     acp.IsolationScope(),
		Capabilities:       acp.Capabilities(),
		AuthSpec:           acp.AuthSpec(),
		FutureAdapterSeams: []string{"pi", "claude"},
		FutureSeamSpecs:    FutureSeamSpecs(),
	}
}

// ValidateSecondaryAdapter enforces Safety Rules 8 and 11 on any future secondary CLI adapter.
func ValidateSecondaryAdapter(a ACPAdapter) error {
	if a == nil {
		return fmt.Errorf("adapter is nil")
	}
	if a.IsolationScope() != IsolationDedicatedKataVM {
		return fmt.Errorf("secondary adapter %q must declare %s (Safety Rule 8)", a.Name(), IsolationDedicatedKataVM)
	}
	if !a.SubagentsDisabled() {
		return fmt.Errorf("secondary adapter %q must disable internal subagents (R212)", a.Name())
	}
	auth := a.AuthSpec()
	if auth.AllowsAPIKey {
		return fmt.Errorf("secondary adapter %q must not allow API key auth (Safety Rule 11)", a.Name())
	}
	if auth.FileMode != 0o600 || strings.TrimSpace(auth.OAuthFile) == "" {
		return fmt.Errorf("secondary adapter %q must declare an explicit 0600 OAuthFile", a.Name())
	}
	if !a.ForbiddenEnv("ANTHROPIC_API_KEY") || !a.ForbiddenEnv("OPENAI_API_KEY") {
		return fmt.Errorf("secondary adapter %q must forbid API key env vars (Safety Rule 11)", a.Name())
	}
	return nil
}

// WriteOAuthCredentialFile writes an OAuth credential file under homeDir with 0600 mode using a
// confined os.Root handle, preserving existing refreshed tokens when preserveOnRefresh is true.
func WriteOAuthCredentialFile(homeDir, relPath string, content []byte, preserveOnRefresh bool) (bool, error) {
	clean := strings.TrimPrefix(path.Clean(strings.ReplaceAll(relPath, `\`, "/")), "/")
	if clean == "" || clean == "." || strings.HasPrefix(clean, "../") || strings.Contains(clean, "\x00") {
		return false, fmt.Errorf("invalid oauth file path %q", relPath)
	}
	root, err := os.OpenRoot(homeDir)
	if err != nil {
		return false, err
	}
	defer root.Close()
	if preserveOnRefresh {
		if fi, serr := root.Stat(clean); serr == nil && fi.Size() > 0 {
			return false, nil
		}
	}
	if dir := path.Dir(clean); dir != "." && dir != "" {
		parts := strings.Split(dir, "/")
		cur := ""
		for _, part := range parts {
			if cur == "" {
				cur = part
			} else {
				cur += "/" + part
			}
			_ = root.Mkdir(cur, 0o700)
		}
	}
	f, err := root.OpenFile(clean, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		return false, err
	}
	defer f.Close()
	if _, err := io.Writer(f).Write(content); err != nil {
		return false, err
	}
	_ = f.Chmod(0o600)
	return true, nil
}

// DisabledSubagentTools returns copies of the internal CLI subagent tool names disabled by Crew's
// `1 session = 1 peer agent` policy.
func DisabledSubagentTools() []string {
	return slices.Clone(disabledSubagentTools)
}
