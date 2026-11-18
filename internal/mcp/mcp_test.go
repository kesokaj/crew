package mcp_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/kesokaj/crew/internal/db/dbtest"
	"github.com/kesokaj/crew/internal/mcp"
)

const (
	fixture = "f1c50000-0000-4000-8000-000000000001"
	draft   = "d0000000-0000-4000-8000-000000000001"
)

// kit is a server with three tools, whose tokens are the callers' names.
type kit struct {
	srv   *httptest.Server
	s     *mcp.Server
	d     *dbtest.DB
	calls []mcp.Caller
}

func newKit(t *testing.T, callers map[string]mcp.Caller) *kit {
	t.Helper()
	d := dbtest.New(t, false)
	if _, err := d.Server.Exec(context.Background(), `INSERT INTO plan_drafts (id) VALUES ($1)`, draft); err != nil {
		t.Fatal(err)
	}
	k := &kit{d: d}
	s, err := mcp.New(d.Server, func(r *http.Request) (mcp.Caller, error) {
		c, ok := callers[strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")]
		if !ok {
			return mcp.Caller{}, mcp.ErrNoToken
		}
		return c, nil
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	echo := func(ctx context.Context, c mcp.Caller, args json.RawMessage) (any, error) {
		k.calls = append(k.calls, c)
		var a struct {
			Text string `json:"text"`
		}
		if err := mcp.Decode(args, &a); err != nil {
			return nil, err
		}
		switch a.Text {
		case "refuse":
			return nil, mcp.Errf("refused: %s", a.Text)
		case "fail":
			return nil, errors.New("the secret path /var/lib/crew broke")
		}
		return map[string]string{"echo": a.Text, "project": c.Project, "draft": c.Draft}, nil
	}
	input := map[string]any{"type": "object", "properties": map[string]any{"text": map[string]any{"type": "string"}}}
	for _, tl := range []mcp.Tool{
		{Name: "get_plan_draft", Scope: mcp.ScopeDraft, Input: input, Handle: echo},
		{Name: "get_project", Scope: mcp.ScopeProject, Input: input, Handle: echo},
		{Name: "write_report", Scope: mcp.ScopeProject, Input: input, Handle: echo},
	} {
		if err := s.Add(tl); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.Add(mcp.Tool{Name: "crew_echo", Input: input, Handle: echo}); err == nil {
		t.Fatal("a tool with no words in the baseline was added")
	}
	k.s = s
	k.srv = httptest.NewServer(s)
	t.Cleanup(k.srv.Close)
	return k
}

func (k *kit) post(t *testing.T, token string, body any) (int, map[string]any) {
	t.Helper()
	b, _ := json.Marshal(body)
	req, _ := http.NewRequest(http.MethodPost, k.srv.URL, bytes.NewReader(b))
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var out map[string]any
	json.NewDecoder(resp.Body).Decode(&out)
	return resp.StatusCode, out
}

func (k *kit) call(t *testing.T, token, tool string, args any) (string, bool) {
	t.Helper()
	code, out := k.post(t, token, map[string]any{"jsonrpc": "2.0", "id": 7, "method": "tools/call",
		"params": map[string]any{"name": tool, "arguments": args}})
	if code != 200 || out["result"] == nil {
		t.Fatalf("tools/call %s: %d %v", tool, code, out)
	}
	res := out["result"].(map[string]any)
	text := res["content"].([]any)[0].(map[string]any)["text"].(string)
	return text, res["isError"] != true
}

var callers = map[string]mcp.Caller{
	"planner": {Role: "planner", Draft: draft},
	"lead":    {Role: "curator", Project: fixture},
	"builder": {Role: "builder", Project: fixture},
	// A curator's token that somehow names a draft only reaches no project tool.
	"lead-of-draft": {Role: "curator", Draft: draft},
}

func TestTheProtocolAsBothCLIsSpeakIt(t *testing.T) {
	k := newKit(t, callers)
	if code, _ := k.post(t, "", map[string]any{"jsonrpc": "2.0", "id": 1, "method": "initialize"}); code != 401 {
		t.Fatalf("no token: %d", code)
	}
	if code, _ := k.post(t, "made-up", map[string]any{"jsonrpc": "2.0", "id": 1, "method": "initialize"}); code != 401 {
		t.Fatalf("an unknown token: %d", code)
	}
	code, out := k.post(t, "lead", map[string]any{"jsonrpc": "2.0", "id": 1, "method": "initialize",
		"params": map[string]any{"protocolVersion": "2025-03-26"}})
	if code != 200 || out["result"].(map[string]any)["protocolVersion"] != "2025-03-26" {
		t.Fatalf("initialize: %d %v", code, out)
	}
	if code, _ := k.post(t, "lead", map[string]any{"jsonrpc": "2.0", "method": "notifications/initialized"}); code != 202 {
		t.Fatalf("a notification: %d", code)
	}
	// claude asks server/discover before initialize (spike item 71): an unknown method is an error, not a failure.
	if _, out := k.post(t, "lead", map[string]any{"jsonrpc": "2.0", "id": 2, "method": "server/discover"}); out["error"] == nil {
		t.Fatalf("server/discover: %v", out)
	}
	resp, _ := http.Get(k.srv.URL)
	if resp.StatusCode != 405 {
		t.Fatalf("GET: %d", resp.StatusCode)
	}
}

func TestEachTokenListsOnlyItsOwnTools(t *testing.T) {
	k := newKit(t, callers)
	for token, want := range map[string][]string{
		"planner":       {"get_plan_draft"},
		"lead":          {"get_project", "write_report"},
		"builder":       {"get_project"},
		"lead-of-draft": {},
	} {
		_, out := k.post(t, token, map[string]any{"jsonrpc": "2.0", "id": 3, "method": "tools/list"})
		var got []string
		for _, x := range out["result"].(map[string]any)["tools"].([]any) {
			m := x.(map[string]any)
			if m["description"] == "" || m["inputSchema"] == nil {
				t.Errorf("%s: %v has no words or input", token, m["name"])
			}
			got = append(got, m["name"].(string))
		}
		if strings.Join(got, ",") != strings.Join(want, ",") {
			t.Errorf("%s lists %v, want %v", token, got, want)
		}
	}
}

func TestACallOutsideTheTokenIsRefusedAndRecorded(t *testing.T) {
	k := newKit(t, callers)
	for _, c := range []struct{ token, tool string }{
		{"planner", "get_project"},       // a draft's token reaches no project
		{"builder", "write_report"},      // the curator's tool
		{"lead", "get_plan_draft"},       // a project's token reaches no draft
		{"lead-of-draft", "get_project"}, // a role with project tools, on a draft
		{"lead", "no_such_tool"},
	} {
		text, ok := k.call(t, c.token, c.tool, map[string]any{"text": "x"})
		if ok {
			t.Errorf("%s called %s: %s", c.token, c.tool, text)
		}
	}
	if len(k.calls) != 0 {
		t.Fatalf("a refused call reached its tool: %v", k.calls)
	}
	var n int
	k.d.Server.QueryRow(context.Background(), `SELECT count(*) FROM mcp_calls WHERE NOT ok`).Scan(&n)
	if n != 5 {
		t.Fatalf("%d refused calls recorded, want 5", n)
	}
}

func TestTheCallerIsTheTokensNeverTheArguments(t *testing.T) {
	k := newKit(t, callers)
	text, ok := k.call(t, "lead", "get_project", map[string]any{"text": "hi"})
	if !ok || !strings.Contains(text, `"project":"`+fixture+`"`) {
		t.Fatalf("%v %s", ok, text)
	}
	// An argument the tool does not have is refused, so a project or a role cannot be passed in.
	if text, ok := k.call(t, "lead", "get_project", map[string]any{"text": "hi", "project": "another"}); ok || !strings.Contains(text, "unknown field") {
		t.Fatalf("an unknown argument: %v %s", ok, text)
	}
	if len(k.calls) != 2 || k.calls[0].Project != fixture || k.calls[0].Role != "curator" {
		t.Fatalf("callers %v", k.calls)
	}
}

func TestARefusalIsToldAndAFailureIsNot(t *testing.T) {
	k := newKit(t, callers)
	if text, ok := k.call(t, "lead", "get_project", map[string]any{"text": "refuse"}); ok || text != "refused: refuse" {
		t.Fatalf("a refusal: %v %q", ok, text)
	}
	text, ok := k.call(t, "lead", "get_project", map[string]any{"text": "fail"})
	if ok || strings.Contains(text, "/var/lib") || !strings.Contains(text, "Crew could not") {
		t.Fatalf("a failure: %v %q", ok, text)
	}
}

func TestEveryCallIsRecordedWithLongTextsShortened(t *testing.T) {
	k := newKit(t, callers)
	long := strings.Repeat("plan ", 2000)
	if _, ok := k.call(t, "planner", "get_plan_draft", map[string]any{"text": long}); !ok {
		t.Fatal("the call failed")
	}
	var role, tool, draftID string
	var args []byte
	var ok bool
	err := k.d.Server.QueryRow(context.Background(), `SELECT role, tool, draft::text, args, ok FROM mcp_calls ORDER BY id DESC LIMIT 1`).
		Scan(&role, &tool, &draftID, &args, &ok)
	if err != nil || role != "planner" || tool != "get_plan_draft" || draftID != draft || !ok {
		t.Fatalf("%v %s %s %s %v", err, role, tool, draftID, ok)
	}
	if len(args) > 400 || !strings.Contains(string(args), "(10000 characters)") {
		t.Fatalf("the arguments were kept as they came: %d bytes", len(args))
	}
}

// A run that keeps calling is refused at its limit, and adds no row more; the arguments of a call are
// stored at most at their cap, also when no single text in them is long.
func TestARunCannotFillTheDatabaseThroughTheMCP(t *testing.T) {
	ctx := context.Background()
	const run = "00000000-0000-4000-8000-0000000000a1"
	k := newKit(t, map[string]mcp.Caller{"lead": {Role: "lead", Project: fixture, Run: run}})
	for _, q := range []string{
		`INSERT INTO subtasks (id, scope, project, number, kind, role) VALUES (9001, 'project', '` + fixture + `', 900, 'breakdown', 'lead')`,
		`INSERT INTO runs (id, subtask, number, cli, crew_version, image) VALUES ('` + run + `', 9001, 1, 'claude', 't', 't')`,
	} {
		if _, err := k.d.Server.Exec(ctx, q); err != nil {
			t.Fatal(err)
		}
	}
	many := make([]string, 3000)
	for i := range many {
		many[i] = "short"
	}
	if _, ok := k.call(t, "lead", "get_project", map[string]any{"text": "x", "many": many}); ok {
		t.Fatal("an argument the tool does not have was taken")
	}
	var stored int
	k.d.Server.QueryRow(ctx, `SELECT length(args::text) FROM mcp_calls ORDER BY id DESC LIMIT 1`).Scan(&stored)
	if stored > 9000 {
		t.Fatalf("the arguments were stored as %d bytes", stored)
	}
	k.call(t, "lead", strings.Repeat("x", 5000), map[string]any{})
	var name int
	k.d.Server.QueryRow(ctx, `SELECT length(tool) FROM mcp_calls ORDER BY id DESC LIMIT 1`).Scan(&name)
	if name > 110 {
		t.Fatalf("a tool's name was stored as %d bytes", name)
	}
	if _, err := k.d.Server.Exec(ctx, `INSERT INTO mcp_calls (run, project, role, tool, ok)
		SELECT $1, $2, 'lead', 'get_project', true FROM generate_series(1, $3)`, run, fixture, mcp.MaxCallsPerRun); err != nil {
		t.Fatal(err)
	}
	var before, after int
	reached := len(k.calls)
	k.d.Server.QueryRow(ctx, `SELECT count(*) FROM mcp_calls`).Scan(&before)
	text, ok := k.call(t, "lead", "get_project", map[string]any{"text": "hi"})
	k.d.Server.QueryRow(ctx, `SELECT count(*) FROM mcp_calls`).Scan(&after)
	if ok || !strings.Contains(text, "calls to Crew's tools a run may make") || after != before || len(k.calls) != reached {
		t.Fatalf("a call past the limit: %v %q, rows %d to %d", ok, text, before, after)
	}
}

func TestEveryMethodNeedsTheToken(t *testing.T) {
	k := newKit(t, callers)
	req, _ := http.NewRequest(http.MethodDelete, k.srv.URL, nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil || resp.StatusCode != 401 {
		t.Fatalf("DELETE without a token: %v %v", resp.StatusCode, err)
	}
	req.Header.Set("Authorization", "Bearer lead")
	if resp, _ = http.DefaultClient.Do(req); resp.StatusCode != 200 {
		t.Fatalf("DELETE with a token: %d", resp.StatusCode)
	}
}

// A database that cannot count a run's calls is a failure of Crew, not a spent limit.
func TestAFailedCountIsNotALimit(t *testing.T) {
	k := newKit(t, map[string]mcp.Caller{"lead": {Role: "lead", Project: fixture, Run: "not-a-run"}})
	text, ok := k.call(t, "lead", "get_project", map[string]any{"text": "hi"})
	if ok || strings.Contains(text, "calls to Crew's tools") || !strings.Contains(text, "Crew could not") {
		t.Fatalf("%v %q", ok, text)
	}
}
