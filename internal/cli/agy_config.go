package cli

import (
	"encoding/json"
	"strings"

	"github.com/kesokaj/crew/internal/homepath"
)

// agySettings is agy's settings file in a home.
const agySettings = ".gemini/antigravity-cli/settings.json"

// agyProviderKeys are the keys of settings.json that move agy off the subscription or define custom
// model providers or endpoints (12.2, safety rule 11). PrepareHome and PrepareRun drop each before
// every turn.
var agyProviderKeys = []string{
	"modelProvider",
	"customModels",
	"customModelsConfig",
	"gcp",
}

// agyConfig is agy's shared config in a home (12.2).
const agyConfig = ".gemini/config/config.json"

// agyAllow is the full access of a run inside its sandbox (12.2, spike item 9): every command,
// file, and MCP tool. A call with no tools gets none.
var agyAllow = []string{"command(*)", "read_file(*)", "write_file(*)", "mcp(*)"}

// disabledSubagentTools are the internal CLI subagent tools refused in `1 session = 1 peer agent`
// ACP sessions (design 11.10, R212) so every agent run is tracked via Crew's Agent API & Tasks API.
var disabledSubagentTools = []string{
	"invoke_subagent",
	"define_subagent",
	"manage_subagents",
	"run_workflow",
	"swarm",
	"browser_subagent",
}

// agyDenySubagents formats disabledSubagentTools into `agy` permissions.deny rules.
var agyDenySubagents = []string{
	"invoke_subagent(*)",
	"define_subagent(*)",
	"manage_subagents(*)",
	"run_workflow(*)",
	"swarm(*)",
	"browser_subagent(*)",
}

// IsDisabledSubagentTool reports whether name names an internal CLI subagent tool disabled by Crew.
func IsDisabledSubagentTool(name string) bool {
	base := strings.ToLower(strings.TrimSpace(name))
	if i := strings.IndexByte(base, '('); i >= 0 {
		base = strings.TrimSpace(base[:i])
	}
	for _, d := range disabledSubagentTools {
		if base == d {
			return true
		}
	}
	return false
}

// agyMCPFile is agy's MCP configuration in the home, where it reads its servers (spike item 10).
const agyMCPFile = ".gemini/config/mcp_config.json"

// agyForbiddenEnvExact are environment variables that must never be passed to agy or set through
// creds.env inside a sandbox (design 12.2, 13.5, AGENTS.md Safety Rule 11).
var agyForbiddenEnvExact = []string{
	"ANTHROPIC_API_KEY",
	"GEMINI_API_KEY",
	"GOOGLE_API_KEY",
	"GOOGLE_GEMINI_BASE_URL",
	"AGY_ADC_AUTH",
	"GOOGLE_APPLICATION_CREDENTIALS",
	"GOOGLE_GENAI_USE_VERTEXAI",
	"AGY_BUSINESS_PAYGO_TIER",
	"AGY_CLI_DISABLE_SAFETY_FILTERING",
}

var multiProviderForbiddenEnvExact = []string{
	"OPENAI_API_KEY",
	"OPENROUTER_API_KEY",
	"DEEPSEEK_API_KEY",
	"GROQ_API_KEY",
	"XAI_API_KEY",
	"MISTRAL_API_KEY",
	"COHERE_API_KEY",
	"TOGETHER_API_KEY",
	"FIREWORKS_API_KEY",
	"PERPLEXITY_API_KEY",
	"CEREBRAS_API_KEY",
	"SAMBANOVA_API_KEY",
	"AI21_API_KEY",
	"HUGGINGFACE_API_KEY",
	"HF_TOKEN",
	"REPLICATE_API_TOKEN",
	"AZURE_OPENAI_API_KEY",
	"AWS_BEARER_TOKEN_BEDROCK",
	"DASHSCOPE_API_KEY",
	"MOONSHOT_API_KEY",
	"ZHIPU_API_KEY",
	"VOYAGE_API_KEY",
	"ELEVENLABS_API_KEY",
}

var agyForbiddenEnvPrefixes = []string{
	"AGY_LLM_GATEWAY_",
}

// MultiProviderForbiddenEnv reports whether an environment variable name is a blocked LLM provider API key (Rule 11).
func MultiProviderForbiddenEnv(name string) bool {
	name = strings.TrimSpace(name)
	for _, k := range agyForbiddenEnvExact {
		if name == k {
			return true
		}
	}
	for _, k := range multiProviderForbiddenEnvExact {
		if name == k {
			return true
		}
	}
	return false
}

// MultiProviderForbiddenEnvNames returns all exact forbidden API key env vars across agy, claude, and pi seams.
func MultiProviderForbiddenEnvNames() []string {
	out := append([]string(nil), agyForbiddenEnvExact...)
	return append(out, multiProviderForbiddenEnvExact...)
}

// ForbiddenEnv reports whether an environment variable name is forbidden by the CLI adapter.
func (Agy) ForbiddenEnv(name string) bool {
	name = strings.TrimSpace(name)
	if MultiProviderForbiddenEnv(name) {
		return true
	}
	for _, p := range agyForbiddenEnvPrefixes {
		if strings.HasPrefix(name, p) {
			return true
		}
	}
	return false
}

// ForbiddenEnv reports whether name is forbidden by any registered CLI adapter.
func ForbiddenEnv(name string) bool {
	for _, a := range adapters {
		if a.ForbiddenEnv(name) {
			return true
		}
	}
	return false
}

// ForbiddenEnvNames returns the exact forbidden environment variable names to unset in creds.env.
func ForbiddenEnvNames() []string {
	return MultiProviderForbiddenEnvNames()
}

// PrepareHome writes agy's paid credits and telemetry as off and strips custom provider keys before
// it first runs (12.2, 12.7).
func (Agy) PrepareHome(home string) error {
	doc := map[string]any{}
	if b, err := homepath.ReadFile(home, agySettings, 1<<20); err == nil {
		if json.Unmarshal(b, &doc) != nil || doc == nil {
			doc = map[string]any{}
		}
	}
	delete(doc, "toolPermission")
	for _, k := range agyProviderKeys {
		delete(doc, k)
	}
	doc["permissions"] = map[string]any{"allow": []string{}, "deny": agyDenySubagents}
	doc["subagentsEnabled"] = false
	doc["useG1Credits"] = false
	doc["enableTelemetry"] = false
	b, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		return err
	}
	if err := homepath.WriteFile(home, agySettings, append(b, '\n'), 0o600); err != nil {
		return err
	}
	return agyRemoteControlOff(home)
}

// PrepareRun writes agy's settings file for the run: its access, the workspace as trusted, paid
// credits and telemetry off, and custom provider keys stripped (12.2, 12.7). With Crew's MCP server
// it writes agy's MCP configuration with that one server; without, it removes it.
func (Agy) PrepareRun(home, workspace string, tools bool, mcp *MCP) error {
	if err := agyMCP(home, mcp); err != nil {
		return err
	}
	doc := map[string]any{}
	if b, err := homepath.ReadFile(home, agySettings, 1<<20); err == nil {
		if json.Unmarshal(b, &doc) != nil || doc == nil {
			doc = map[string]any{}
		}
	}
	allow := []string{}
	if tools {
		allow = agyAllow
	}
	delete(doc, "toolPermission")
	for _, k := range agyProviderKeys {
		delete(doc, k)
	}
	doc["permissions"] = map[string]any{"allow": allow, "deny": agyDenySubagents}
	doc["subagentsEnabled"] = false
	doc["trustedWorkspaces"] = []string{workspace}
	doc["useG1Credits"] = false
	doc["enableTelemetry"] = false
	b, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		return err
	}
	if err := homepath.WriteFile(home, agySettings, append(b, '\n'), 0o600); err != nil {
		return err
	}
	return agyRemoteControlOff(home)
}

func agyMCP(home string, mcp *MCP) error {
	if mcp == nil {
		return homepath.Remove(home, agyMCPFile)
	}
	b, err := json.MarshalIndent(map[string]any{"mcpServers": map[string]any{MCPServer: map[string]any{
		"serverUrl": mcp.URL, "headers": map[string]string{"Authorization": "Bearer " + mcp.Token},
	}}}, "", "  ")
	if err != nil {
		return err
	}
	return homepath.WriteFile(home, agyMCPFile, append(b, '\n'), 0o600)
}

// agyRemoteControlOff writes agy's shared config in home with remote control off and its hostnames
// dropped, and keeps every other key (12.2).
func agyRemoteControlOff(home string) error {
	doc := map[string]any{}
	if b, err := homepath.ReadFile(home, agyConfig, 1<<20); err == nil {
		if json.Unmarshal(b, &doc) != nil || doc == nil {
			doc = map[string]any{}
		}
	}
	settings, ok := doc["userSettings"].(map[string]any)
	if !ok || settings == nil {
		settings = map[string]any{}
	}
	settings["remoteControlEnabled"] = false
	delete(settings, "cliRemoteControlHostname")
	delete(settings, "remoteControlHostname")
	doc["userSettings"] = settings
	b, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		return err
	}
	return homepath.WriteFile(home, agyConfig, append(b, '\n'), 0o600)
}
