package cli

import (
	"bufio"
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
)

func recording(t *testing.T, name string) [][]byte {
	t.Helper()
	b, err := os.ReadFile("../../testdata/" + name)
	if err != nil {
		t.Fatal(err)
	}
	var lines [][]byte
	sc := bufio.NewScanner(bytes.NewReader(b))
	sc.Buffer(make([]byte, 1<<20), 16<<20)
	for sc.Scan() {
		if len(bytes.TrimSpace(sc.Bytes())) > 0 {
			lines = append(lines, append([]byte(nil), sc.Bytes()...))
		}
	}
	return lines
}

// replay parses a recording as the driver does: every line one event, and the last result kept.
func replay(t *testing.T, a Adapter, name string) (events []Event, conv string, result []byte) {
	t.Helper()
	for _, l := range recording(t, name) {
		e := a.Parse(l)
		events = append(events, e)
		if e.ConversationID != "" && conv == "" {
			conv = e.ConversationID
		}
		if e.Result {
			result = l
		}
	}
	return events, conv, result
}

var kinds = []string{EventMessage, EventToolCall, EventToolResult, EventStatus, EventUsage, EventError}

// Every line of every recording becomes one event of a kind of 12.5, so a trajectory drops nothing.
func TestEveryRecordedLineIsOneEventOfAKnownKind(t *testing.T) {
	for _, a := range adapters {
		files, _ := filepath.Glob("../../testdata/" + a.Name() + "/*.jsonl")
		if len(files) < 5 {
			t.Fatalf("%s: only %d recordings", a.Name(), len(files))
		}
		for _, f := range files {
			rel := a.Name() + "/" + filepath.Base(f)
			for i, l := range recording(t, rel) {
				e := a.Parse(l)
				if !slices.Contains(kinds, e.Type) || e.Payload == nil {
					t.Errorf("%s line %d: %q %v", rel, i+1, e.Type, e.Payload)
				}
			}
		}
	}
}

func TestAgyReplaysANormalRun(t *testing.T) {
	events, conv, result := replay(t, Agy{}, "agy/normal-run-flash-low.jsonl")
	if conv != "f0801283-e3db-4172-881e-9ce47672a614" {
		t.Fatalf("conversation %q", conv)
	}
	var calls, results []string
	said := ""
	for _, e := range events {
		switch e.Type {
		case EventToolCall:
			calls = append(calls, e.Tool)
		case EventToolResult:
			results = append(results, e.Tool)
		}
		said += e.Speaks
	}
	if !slices.Equal(calls, []string{"write_to_file", "run_command"}) || !slices.Equal(results, calls) {
		t.Fatalf("calls %v results %v", calls, results)
	}
	if events[0].Type != EventStatus || events[0].Payload["status"] != "started" || events[len(events)-1].Type != EventUsage {
		t.Fatalf("first %v last %v", events[0], events[len(events)-1])
	}
	o := Agy{}.Outcome(TurnOutput{Result: result})
	if o.Reason != ReasonOK || o.Response != "done\n" {
		t.Fatalf("%+v", o)
	}
	u := Agy{}.Usage(result)
	if u.TokensIn != 25366 || u.TokensOut != 391 || u.TokensCached != 37737 || u.HasCost || !u.TokensTotal {
		t.Fatalf("%+v", u)
	}
	if strings.TrimSpace(said) == "" {
		t.Fatal("the agent said nothing")
	}
}

func TestAgyTellsTheReasonsApart(t *testing.T) {
	read := func(name string) []byte {
		b, err := os.ReadFile("../../testdata/" + name)
		if err != nil {
			t.Fatal(err)
		}
		return b
	}
	cases := []struct {
		name string
		out  TurnOutput
		want string
	}{
		// A limit: exit code 3 and AGY_ERROR, read by its status and not by the text (spike item 50).
		{"limit", TurnOutput{Result: bytes.TrimSpace(read("agy/limit-reached-result.jsonl")), Stderr: read("agy/limit-reached-stderr.txt"), Exit: 3}, ReasonQuota},
		// A postResponseFailure AGY_ERROR warning on stderr when the turn itself succeeded with exit 0
		// does not override the SUCCESS result (BUG-0006).
		{"stale limit warning on success", TurnOutput{Result: []byte(`{"event":"result","result":{"status":"SUCCESS","response":"done"}}`), Stderr: read("agy/limit-reached-stderr.txt"), Exit: 0}, ReasonOK},
		{"limit with exit 3 even if status says success", TurnOutput{Result: []byte(`{"event":"result","result":{"status":"SUCCESS","response":"done"}}`), Stderr: read("agy/limit-reached-stderr.txt"), Exit: 3}, ReasonQuota},
		{"no login", TurnOutput{Result: bytes.TrimSpace(read("agy/no-login-result.jsonl")), Exit: 1}, ReasonLogin},
		{"refused login", TurnOutput{Result: bytes.TrimSpace(read("agy/refused-login-run-result.jsonl")), Exit: 1}, ReasonLogin},
		{"renewed at once by another copy", TurnOutput{Stderr: read("agy/concurrent-renewal-failed-stderr.txt"), Exit: 1}, ReasonLogin},
		// A refused tool still ends with 0 and a result; only standard error says so (12.2).
		{"refused tool", TurnOutput{Result: []byte(`{"event":"result","result":{"status":"SUCCESS","response":""}}`), Stderr: read("agy/refused-tool-headless.txt")}, ReasonRefused},
		{"denied actions", TurnOutput{Result: []byte(`{"event":"result","result":{"status":"SUCCESS","response":"x","denied_actions":["command(rm)"]}}`)}, ReasonRefused},
		{"no result", TurnOutput{Exit: 1, Stderr: []byte("panic: something")}, ReasonError},
		{"other error", TurnOutput{Result: []byte(`{"event":"result","result":{"status":"ERROR","error":"the model is gone"}}`), Exit: 1}, ReasonError},
	}
	for _, c := range cases {
		if o := (Agy{}).Outcome(c.out); o.Reason != c.want {
			t.Errorf("%s: %+v, want %s", c.name, o, c.want)
		}
	}
}

func TestTurnCommands(t *testing.T) {
	turn := Turn{Prompt: "Say \"hi\".", Model: "gemini-3.8-flash-low", Workspace: "/home/agent/work", Timeout: 280 * time.Second, Tools: true}
	c := Agy{}.TurnCommand(turn)
	if !slices.Contains(c.Args, "--print-timeout") || c.Args[slices.Index(c.Args, "--print-timeout")+1] != "280s" ||
		slices.Contains(c.Args, "--effort") || slices.Contains(c.Args, "--conversation") {
		t.Fatalf("%q", c.Args)
	}
	var msg struct {
		Event   string `json:"event"`
		Message struct {
			Content string `json:"content"`
		} `json:"message"`
	}
	if json.Unmarshal(c.Stdin, &msg) != nil || msg.Event != "user" || msg.Message.Content != turn.Prompt || !bytes.HasSuffix(c.Stdin, []byte("\n")) {
		t.Fatalf("%s", c.Stdin)
	}
	turn.Continue = "f0801283-e3db-4172-881e-9ce47672a614"
	if c := (Agy{}).TurnCommand(turn); c.Args[len(c.Args)-1] != turn.Continue {
		t.Fatalf("%q", c.Args)
	}

}

func TestConversationFiles(t *testing.T) {
	if f := (Agy{}).ConversationFiles("f0801283-e3db", "/home/agent/work"); !slices.Equal(f, []string{".gemini/antigravity-cli/conversations/f0801283-e3db.db"}) {
		t.Fatal(f)
	}
	// An id is from a sandbox's output: one that would name another path gives no file.
	for _, a := range adapters {
		for _, id := range []string{"", "../x", "a/b", "x.db", "--verbose", "-p", "_x", "--setting-sources=user,project"} {
			if f := a.ConversationFiles(id, "/home/agent/work"); len(f) != 0 {
				t.Errorf("%s %q: %v", a.Name(), id, f)
			}
		}
	}
}

func TestAgyPrepareRunGivesFullAccessOnlyWithTools(t *testing.T) {
	home := t.TempDir()
	if err := (Agy{}).PrepareRun(home, "/home/agent/work", true, nil); err != nil {
		t.Fatal(err)
	}
	read := func() map[string]any {
		b, err := os.ReadFile(filepath.Join(home, agySettings))
		if err != nil {
			t.Fatal(err)
		}
		var m map[string]any
		json.Unmarshal(b, &m)
		return m
	}
	m := read()
	if allow := obj(m, "permissions")["allow"].([]any); len(allow) != 4 || m["useG1Credits"] != false ||
		m["trustedWorkspaces"].([]any)[0] != "/home/agent/work" {
		t.Fatalf("%v", m)
	}
	if err := (Agy{}).PrepareRun(home, "/home/agent/work", false, nil); err != nil {
		t.Fatal(err)
	}
	if allow := obj(read(), "permissions")["allow"].([]any); len(allow) != 0 {
		t.Fatalf("a call with no tools is allowed %v", allow)
	}
}

func TestAgyPrepareRunDropsModelProviderKeys(t *testing.T) {
	if !slices.Contains(agyProviderKeys, "modelProvider") {
		t.Fatalf("agyProviderKeys = %v, want modelProvider", agyProviderKeys)
	}
	for _, tools := range []bool{true, false} {
		home := t.TempDir()
		path := filepath.Join(home, agySettings)
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			t.Fatal(err)
		}
		seed := map[string]any{
			"toolPermission": "allow",
			"theme":          "dark",
		}
		for _, k := range agyProviderKeys {
			seed[k] = "custom-" + k
		}
		raw, err := json.Marshal(seed)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, raw, 0o600); err != nil {
			t.Fatal(err)
		}
		if err := (Agy{}).PrepareRun(home, "/home/agent/work", tools, nil); err != nil {
			t.Fatal(err)
		}
		b, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		var doc map[string]any
		if err := json.Unmarshal(b, &doc); err != nil {
			t.Fatalf("unmarshal %s: %v", b, err)
		}
		for _, k := range agyProviderKeys {
			if _, has := doc[k]; has {
				t.Fatalf("tools=%v: %s remained in %v", tools, k, doc)
			}
		}
		if _, has := doc["toolPermission"]; has {
			t.Fatalf("tools=%v: toolPermission remained in %v", tools, doc)
		}
		if doc["theme"] != "dark" || doc["useG1Credits"] != false {
			t.Fatalf("tools=%v: unexpected doc %v", tools, doc)
		}
		ws, ok := doc["trustedWorkspaces"].([]any)
		if !ok || len(ws) != 1 || ws[0] != "/home/agent/work" {
			t.Fatalf("tools=%v: trustedWorkspaces = %v", tools, doc["trustedWorkspaces"])
		}
		allow, ok := obj(doc, "permissions")["allow"].([]any)
		if !ok || (tools && len(allow) == 0) || (!tools && len(allow) != 0) {
			t.Fatalf("tools=%v: allow = %v", tools, allow)
		}
	}
}

func TestAgyGetsCrewsMCPWithTheRunsTokenOnly(t *testing.T) {
	m := &MCP{URL: "http://10.0.0.1:8080/mcp", Token: "tok-123"}
	home := t.TempDir()
	if err := (Agy{}).PrepareRun(home, "/home/agent/work", true, m); err != nil {
		t.Fatal(err)
	}
	fi, err := os.Stat(filepath.Join(home, agyMCPFile))
	b, _ := os.ReadFile(filepath.Join(home, agyMCPFile))
	if err != nil || fi.Mode().Perm() != 0o600 || !strings.Contains(string(b), `"serverUrl": "http://10.0.0.1:8080/mcp"`) ||
		!strings.Contains(string(b), `"Bearer tok-123"`) || !strings.Contains(string(b), `"crew"`) {
		t.Fatalf("agy's configuration: %s %v", b, err)
	}
	if err := (Agy{}).PrepareRun(home, "/home/agent/work", true, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(home, agyMCPFile)); err == nil {
		t.Fatal("agy's configuration stayed")
	}
}

func TestAgyPrepareRunKeepsRemoteControlOff(t *testing.T) {
	read := func(home string) map[string]any {
		t.Helper()
		p := filepath.Join(home, agyConfig)
		fi, err := os.Stat(p)
		if err != nil {
			t.Fatal(err)
		}
		if fi.Mode().Perm() != 0o600 {
			t.Fatalf("mode %o, want 0600", fi.Mode().Perm())
		}
		b, err := os.ReadFile(p)
		if err != nil {
			t.Fatal(err)
		}
		var m map[string]any
		if err := json.Unmarshal(b, &m); err != nil {
			t.Fatalf("unmarshal %s: %v", b, err)
		}
		return m
	}
	for _, tools := range []bool{true, false} {
		home := t.TempDir()
		if err := (Agy{}).PrepareRun(home, "/home/agent/work", tools, nil); err != nil {
			t.Fatal(err)
		}
		if us := obj(read(home), "userSettings"); us["remoteControlEnabled"] != false || len(us) != 1 {
			t.Fatalf("tools=%v fresh home: %v", tools, us)
		}

		p := filepath.Join(home, agyConfig)
		seed := `{"userSettings":{"remoteControlEnabled":true,"cliRemoteControlHostname":"cli-box","remoteControlHostname":"rc-box","themePreference":"dark"},"plugins":{"p1":{"enabled":true}}}`
		if err := os.WriteFile(p, []byte(seed), 0o644); err != nil {
			t.Fatal(err)
		}
		if err := (Agy{}).PrepareRun(home, "/home/agent/work", tools, nil); err != nil {
			t.Fatal(err)
		}
		m := read(home)
		us := obj(m, "userSettings")
		_, hasCLIHost := us["cliRemoteControlHostname"]
		_, hasHost := us["remoteControlHostname"]
		if us["remoteControlEnabled"] != false || hasCLIHost || hasHost || us["themePreference"] != "dark" ||
			obj(m, "plugins", "p1")["enabled"] != true {
			t.Fatalf("tools=%v seeded home: %v", tools, m)
		}

		for _, bad := range []string{"{not json", "null", `{"userSettings":"not-an-object"}`, `{"userSettings":null}`} {
			if err := os.WriteFile(p, []byte(bad), 0o644); err != nil {
				t.Fatal(err)
			}
			if err := (Agy{}).PrepareRun(home, "/home/agent/work", tools, nil); err != nil {
				t.Fatal(err)
			}
			if us := obj(read(home), "userSettings"); us["remoteControlEnabled"] != false || len(us) != 1 {
				t.Fatalf("tools=%v bad=%q: %v", tools, bad, us)
			}
		}

		cmd := (Agy{}).TurnCommand(Turn{Prompt: "hi", Model: "gemini-3.8-flash-low", Workspace: "/home/agent/work", Tools: tools})
		for _, arg := range cmd.Args {
			if strings.Contains(arg, "remote-control") {
				t.Fatalf("TurnCommand passed remote-control arg %q", arg)
			}
		}
	}
}

func TestAgyNamesWhatItLoadsFromAWorkspace(t *testing.T) {
	if got := strings.Join((Agy{}).WorkspaceConfig(), " "); got != ".agents/hooks.json .agents/mcp_config.json .agents/plugins .agents/plugins.json" {
		t.Fatalf("agy: %q", got)
	}
}

// What a run at its limit marks: agy's group of the model (12.7).
func TestARunAtItsLimitMarksItsGroup(t *testing.T) {
	for _, c := range []struct {
		a             Adapter
		model, detail string
		want          string
	}{
		{Agy{}, "gemini-3.8-flash", "agy reached a limit: Individual quota reached.", "gemini"},
		{Agy{}, "claude-opus-5-5", "", "3p"},
		{Agy{}, "gemini-4-argon-high", "", "gemini"},
	} {
		if got := c.a.Reached(c.model, c.detail); got != c.want {
			t.Errorf("%s %s %q: %s, want %s", c.a.Name(), c.model, c.detail, got, c.want)
		}
	}
}
