package cli

import (
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"testing"
)

func TestAgyProviderKeysIncludeGCPAndDisableTelemetry(t *testing.T) {
	for _, want := range []string{"modelProvider", "customModels", "customModelsConfig", "gcp"} {
		if !slices.Contains(agyProviderKeys, want) {
			t.Fatalf("agyProviderKeys = %v, missing %q", agyProviderKeys, want)
		}
	}

	home := t.TempDir()
	settingsPath := filepath.Join(home, agySettings)
	if err := os.MkdirAll(filepath.Dir(settingsPath), 0o700); err != nil {
		t.Fatal(err)
	}
	seed := `{"gcp":{"project":"rogue"},"enableTelemetry":true,"useG1Credits":true}`
	if err := os.WriteFile(settingsPath, []byte(seed), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := (Agy{}).PrepareHome(home); err != nil {
		t.Fatalf("PrepareHome: %v", err)
	}
	b, err := os.ReadFile(settingsPath)
	if err != nil {
		t.Fatal(err)
	}
	var doc map[string]any
	if err := json.Unmarshal(b, &doc); err != nil {
		t.Fatal(err)
	}
	if _, hasGCP := doc["gcp"]; hasGCP {
		t.Fatalf("PrepareHome left gcp key: %v", doc)
	}
	if doc["enableTelemetry"] != false || doc["useG1Credits"] != false {
		t.Fatalf("PrepareHome telemetry/credits = %v", doc)
	}

	if err := os.WriteFile(settingsPath, []byte(seed), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := (Agy{}).PrepareRun(home, "/home/agent/work", true, nil); err != nil {
		t.Fatalf("PrepareRun: %v", err)
	}
	b, err = os.ReadFile(settingsPath)
	if err != nil {
		t.Fatal(err)
	}
	doc = nil
	if err := json.Unmarshal(b, &doc); err != nil {
		t.Fatal(err)
	}
	if _, hasGCP := doc["gcp"]; hasGCP {
		t.Fatalf("PrepareRun left gcp key: %v", doc)
	}
	if doc["enableTelemetry"] != false || doc["useG1Credits"] != false {
		t.Fatalf("PrepareRun telemetry/credits = %v", doc)
	}
}

func TestAgyPrepareRunAndSanitizeHomeRefuseSymlinkDirectories(t *testing.T) {
	home := t.TempDir()
	outside := t.TempDir()
	if err := os.Symlink(outside, filepath.Join(home, ".gemini")); err != nil {
		t.Fatal(err)
	}
	if err := (Agy{}).PrepareHome(home); err == nil {
		t.Fatal("PrepareHome succeeded through symlink .gemini")
	}
	if err := (Agy{}).PrepareRun(home, "/home/agent/work", true, nil); err == nil {
		t.Fatal("PrepareRun succeeded through symlink .gemini")
	}
	if err := SanitizeHome(home, "/home/agent/work", true); err == nil {
		t.Fatal("SanitizeHome succeeded through symlink .gemini")
	}
}

func TestAgyForbiddenEnv(t *testing.T) {
	forbidden := []string{
		"ANTHROPIC_API_KEY",
		"GEMINI_API_KEY",
		"GOOGLE_API_KEY",
		"GOOGLE_GEMINI_BASE_URL",
		"AGY_ADC_AUTH",
		"GOOGLE_APPLICATION_CREDENTIALS",
		"GOOGLE_GENAI_USE_VERTEXAI",
		"AGY_LLM_GATEWAY_URL",
		"AGY_LLM_GATEWAY_TOKEN",
		"AGY_BUSINESS_PAYGO_TIER",
		"AGY_CLI_DISABLE_SAFETY_FILTERING",
	}
	for _, k := range forbidden {
		if !(Agy{}).ForbiddenEnv(k) || !ForbiddenEnv(k) {
			t.Errorf("expected %q to be forbidden", k)
		}
	}
	for _, allowed := range []string{"PATH", "HOME", "LANG", "TERM", "GOPROXY"} {
		if (Agy{}).ForbiddenEnv(allowed) || ForbiddenEnv(allowed) {
			t.Errorf("expected %q to be allowed", allowed)
		}
	}
}

func TestAgyUsageDistinguishesReportedZeroFromMissingOrMalformedUsage(t *testing.T) {
	zeroLine := []byte(`{"event":"result","result":{"status":"SUCCESS","usage":{"input_tokens":0,"output_tokens":0,"cache_read_tokens":0}}}`)
	u := (Agy{}).Usage(zeroLine)
	if !u.HasTokens || u.TokensIn != 0 || u.TokensOut != 0 || !u.TokensTotal {
		t.Fatalf("genuine zero usage: %+v", u)
	}

	nonZeroLine := []byte(`{"event":"result","result":{"status":"SUCCESS","usage":{"input_tokens":120,"output_tokens":45,"cache_read_tokens":10}}}`)
	u = (Agy{}).Usage(nonZeroLine)
	if !u.HasTokens || u.TokensIn != 120 || u.TokensOut != 45 || u.TokensCached != 10 {
		t.Fatalf("non-zero usage: %+v", u)
	}

	for name, raw := range map[string][]byte{
		"nil":            nil,
		"not-json":       []byte(`{not json`),
		"missing-usage":  []byte(`{"event":"result","result":{"status":"SUCCESS"}}`),
		"null-usage":     []byte(`{"event":"result","result":{"status":"SUCCESS","usage":null}}`),
		"empty-usage":    []byte(`{"event":"result","result":{"status":"SUCCESS","usage":{}}}`),
		"string-tokens":  []byte(`{"event":"result","result":{"status":"SUCCESS","usage":{"input_tokens":"oops"}}}`),
		"negative-token": []byte(`{"event":"result","result":{"status":"SUCCESS","usage":{"input_tokens":-1,"output_tokens":5}}}`),
	} {
		if got := (Agy{}).Usage(raw); got.HasTokens {
			t.Errorf("%s: HasTokens = true (%+v), want false", name, got)
		}
	}
}
