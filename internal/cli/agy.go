package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"
)

// Agy is the adapter of agy, the Antigravity CLI. Google is how it asks whose a login is.
type Agy struct {
	Google Google
}

// AgyName is the name of the one agent CLI Crew runs (12.2, AGENTS.md Agent CLIs 1-2).
const AgyName = "agy"

func (Agy) Name() string { return AgyName }

// LoginFile is agy's token file, which it renews by itself (12.6).
func (Agy) LoginFile() string { return ".gemini/antigravity-cli/antigravity-oauth-token" }

// RealEvery: agy's real request is made once a day; the other checks are `agy models` (12.6).
func (Agy) RealEvery() time.Duration { return 24 * time.Hour }

// AgyManifest is agy's release manifest: the version that is newest, its tarball, and the tarball's
// sha512, as the vendor's install script reads them (12.8, since 0.56). versions.lock pins what it
// names, and the daily look reads it for a newer one.
const AgyManifest = "https://antigravity-cli-auto-updater-974169037036.us-central1.run.app/manifests/linux_amd64.json"

// AgyTarball is the whole shape of the tarball's address the manifest names, with the version as its
// first group: the vendor's bucket, and no other. tools/check-versions.py holds versions.lock's
// AGY_URL to the same pattern, and crew-ctl's AGY_TARBALL the download on the machine.
var AgyTarball = regexp.MustCompile(`^https://storage\.googleapis\.com/antigravity-public/antigravity-cli/([0-9]+\.[0-9]+\.[0-9]+)-[0-9]+/linux-x64/cli_linux_x64\.tar\.gz$`)

// AgyFile is the name of agy's tarball, the last part of its address, which image/Dockerfile copies
// from its build context agy (since 0.57).
const AgyFile = "cli_linux_x64.tar.gz"

// agySHA512 is the shape of the tarball's checksum in versions.lock.
var agySHA512 = regexp.MustCompile(`^[0-9a-f]{128}$`)

// AgyDownload is agy's tarball as versions.lock pins it (12.8, since 0.57): the address, which is the
// vendor's bucket with AGY_VERSION in it and no other, and its sha512, which the file must have. The
// server and crew-ctl download it, and hand it to the build, which reaches no storage.googleapis.com.
// A lock of anything else gives an error that names none of its values: a commit of Crew's project
// wrote them.
func AgyDownload(lock map[string]string) (url, sha512 string, err error) {
	m := AgyTarball.FindStringSubmatch(lock["AGY_URL"])
	if m == nil || m[1] != lock["AGY_VERSION"] {
		return "", "", errors.New("versions.lock: AGY_URL is not the address of the vendor's tarball at AGY_VERSION")
	}
	if !agySHA512.MatchString(lock["AGY_SHA512"]) {
		return "", "", errors.New("versions.lock: AGY_SHA512 is not a sha512 checksum")
	}
	return lock["AGY_URL"], lock["AGY_SHA512"], nil
}

// CheckLogin refuses a token file that is not JSON with an access token.
func (Agy) CheckLogin(login []byte) error {
	var f agyFile
	if err := json.Unmarshal(login, &f); err != nil || f.Token.AccessToken == "" {
		return errors.New("agy's token file is not JSON with an access token")
	}
	return nil
}

// Renewed takes the renewed access token and its expiry into the stored login, and keeps the stored
// refresh token and everything else. agy renews only the access token: copies of one login keep one
// refresh token (spike items 2 and 17). A sandbox that wrote another account's refresh token beside
// the operator's access token so brings nothing of it in.
func (Agy) Renewed(stored, offered []byte) ([]byte, error) {
	var base map[string]any
	if err := json.Unmarshal(stored, &base); err != nil {
		return nil, fmt.Errorf("the stored login: %w", err)
	}
	tok, ok := base["token"].(map[string]any)
	if !ok {
		return nil, errors.New("the stored login has no token")
	}
	var next struct {
		Token struct {
			AccessToken string `json:"access_token"`
			Expiry      string `json:"expiry"`
		} `json:"token"`
	}
	if err := json.Unmarshal(offered, &next); err != nil {
		return nil, fmt.Errorf("the renewed login: %w", err)
	}
	if next.Token.AccessToken == "" || next.Token.Expiry == "" {
		return nil, errors.New("the renewed login has no access token or expiry")
	}
	if _, err := time.Parse(time.RFC3339, next.Token.Expiry); err != nil {
		return nil, fmt.Errorf("the renewed login's expiry: %w", err)
	}
	tok["access_token"], tok["expiry"] = next.Token.AccessToken, next.Token.Expiry
	return json.Marshal(base)
}

// Account asks Google whose the login is, with its access token (spike items 27 and 56).
func (a Agy) Account(ctx context.Context, login []byte) (string, error) {
	return a.Google.agyAccount(ctx, login)
}

// LoginCheck runs `agy models`: it needs a working login, makes no model request, costs no quota,
// and fails in under a second when the login is refused (spike items 11 and 47). The real check
// also reads `agy -p /credits`, which keeps the sign-in in use and shows whether paid credits are
// left (12.2).
func (a Agy) LoginCheck(ctx context.Context, run Runner, real bool) LoginResult {
	r := LoginResult{CLI: a.Name(), Real: real}
	out, errb, code, err := run(ctx, "agy", "models")
	if err != nil {
		r.State, r.Detail = LoginError, "agy could not be run: "+err.Error()
		return r
	}
	text := string(out) + string(errb)
	switch {
	case code == 0:
		r.Models = agyModels(string(out))
		if len(r.Models) == 0 {
			r.State, r.Detail = LoginError, "agy models listed no model"
			r.Models = nil
			return r
		}
		r.State, r.Detail = LoginOK, "agy models listed the models"
	case strings.Contains(text, "sign in") || strings.Contains(text, "authentication"):
		r.State, r.Detail = LoginRefused, "agy says: "+lastLine([]byte(text))
		return r
	default:
		r.State, r.Detail = LoginError, "agy models ended with an unknown error: "+lastLine([]byte(text))
		return r
	}
	if real {
		out, errb, code, err := run(ctx, "agy", "-p", "/credits")
		text := string(out) + string(errb)
		switch {
		case err != nil:
			// Could not tell, such as a timeout: that changes no state.
			r.State, r.Detail = LoginError, "agy -p /credits could not be run: "+err.Error()
			return r
		case code != 0 && (strings.Contains(text, "sign in") || strings.Contains(text, "authentication")):
			r.State, r.Detail = LoginRefused, "agy -p /credits says: "+lastLine([]byte(text))
			return r
		case code != 0:
			r.State, r.Detail = LoginError, "agy -p /credits ended with an unknown error: "+lastLine([]byte(text))
			return r
		}
		r.Credits = agyCredits(string(out))
	}
	return r
}

// agyCredits reads the value of "Remaining credits" (spike item 54).
func agyCredits(out string) string {
	for _, line := range splitLines(out) {
		if v, ok := strings.CutPrefix(line, "Remaining credits"); ok {
			return strings.TrimSpace(v)
		}
	}
	return ""
}

func splitLines(s string) []string {
	var out []string
	for _, l := range strings.Split(strings.ReplaceAll(s, "\r", ""), "\n") {
		if l = strings.TrimRight(l, " "); strings.TrimSpace(l) != "" {
			out = append(out, l)
		}
	}
	return out
}

// WorkspaceConfig: agy loads a workspace's hooks and MCP servers, also when the workspace is not in
// trustedWorkspaces (spike items 28 and 41; tried again in M5), and those of every plugin under
// .agents/plugins/ and of every plugin .agents/plugins.json names, wherever it is (tried in M6). A
// skill's or an agent's front matter, a nested .agents/, and a file written during a turn load nothing
// in that turn; a plugin written then loads in the next, so these go before every turn.
func (Agy) WorkspaceConfig() []string {
	return []string{".agents/hooks.json", ".agents/mcp_config.json", ".agents/plugins", ".agents/plugins.json"}
}

// WorkspaceInstructions: agy reads AGENTS.md and GEMINI.md at every level, and the rules, skills, and
// agents of .agents/ with the files that list more of them anywhere (13.8, its own guide).
func (Agy) WorkspaceInstructions() []string {
	return []string{"AGENTS.md", "GEMINI.md", "WORKFLOW.md", ".agents/rules", ".agents/skills", ".agents/agents",
		".agents/rules.json", ".agents/skills.json", ".agents/agents.json"}
}

// IndexFiles: the files under .agents/ that name folders anywhere for agy to read (its own guide):
// plugins and skills, which can declare hooks, and agents are code; rules are instructions.
func (Agy) IndexFiles() map[string]bool {
	return map[string]bool{".agents/plugins.json": true, ".agents/skills.json": true, ".agents/agents.json": true, ".agents/rules.json": false}
}

// CodeFiles: what agy loads as code, and skills and agents, which crew-run gives claude too (13.8).
func (Agy) CodeFiles() []string {
	return append(Agy{}.WorkspaceConfig(), ".agents/skills", ".agents/agents", ".agents/skills.json", ".agents/agents.json")
}

// TurnCommand starts agy headless with the prompt as one stream-json message on standard input,
// which is then closed: agy does not end while it is open (12.2, spike item 52). The model's name
// holds its effort, so --effort is never passed (12.3, item 48). --print-timeout takes a duration
// with its unit.
func (Agy) TurnCommand(t Turn) Command {
	args := []string{"agy", "-p", "", "--input-format", "stream-json", "--output-format", "stream-json",
		"--mode", "accept-edits"}
	if t.Timeout > 0 {
		args = append(args, "--print-timeout", fmt.Sprintf("%ds", int(t.Timeout.Seconds())))
	}
	if t.Model != "" {
		args = append(args, "--model", t.Model)
	}
	if t.Continue != "" {
		args = append(args, "--conversation", t.Continue)
	}
	msg, _ := json.Marshal(map[string]any{"event": "user", "message": map[string]any{"content": t.Prompt}})
	return Command{Args: args, Stdin: append(msg, '\n'), Env: []string{"AGY_CLI_DISABLE_AUTO_UPDATE=true"}}
}

// Parse reads one line agy printed (12.5, spike item 52): init, step_update, and result.
func (Agy) Parse(line []byte) Event {
	m := payload(line)
	if m == nil {
		return Event{Type: EventStatus, Payload: map[string]any{"kind": "text", "text": short(string(line), 2000)}}
	}
	switch str(m, "event") {
	case "init":
		return Event{Type: EventStatus, ConversationID: str(m, "conversation_id"), Payload: map[string]any{
			"status": "started", "model": str(m, "init", "model"), "cwd": str(m, "init", "cwd"),
		}}
	case "step_update":
		s := obj(m, "step_update")
		e := Event{ConversationID: str(s, "conversation_id")}
		p := map[string]any{"step": num(s, "step_index"), "state": str(s, "state"), "stepType": str(s, "step_type")}
		if sub := obj(s, "subagent_info"); sub != nil {
			p["subagent"] = sub
		}
		switch str(s, "step_type") {
		case "agent_response":
			e.Type = EventMessage
			if t := str(s, "text_delta"); t != "" {
				p["text"] = t
				e.Speaks = t
			}
			if u := obj(s, "usage"); u != nil {
				p["usage"] = u
			}
		case "tool":
			name := str(s, "tool_name")
			p["tool"] = name
			params := obj(s, "tool_info", "parameters")
			p["input"] = params
			e.Tool, e.ToolInput = name, compact(params)
			e.ToolID = fmt.Sprintf("%v", num(s, "step_index"))
			if str(s, "state") == "DONE" || str(s, "state") == "ERROR" {
				e.Type = EventToolResult
				out := obj(s, "tool_info")
				if o, ok := out["output"]; ok {
					p["output"] = o
				}
				if str(s, "state") == "ERROR" || str(s, "error") != "" {
					p["error"] = str(s, "error")
					e.ToolFailed = true
				}
				p["duration"] = num(s, "duration_seconds")
			} else {
				e.Type = EventToolCall
			}
		default:
			e.Type = EventStatus
		}
		e.Payload = p
		return e
	case "result":
		r := obj(m, "result")
		return Event{Type: EventUsage, Result: true, ConversationID: str(r, "conversation_id"), Payload: map[string]any{
			"status": str(r, "status"), "response": short(str(r, "response"), 4000), "error": str(r, "error"),
			"usage": obj(r, "usage"), "turns": num(r, "num_turns"), "duration": num(r, "duration_seconds"),
		}}
	}
	return Event{Type: EventStatus, Payload: map[string]any{"kind": str(m, "event")}}
}

// agyError is the line agy writes on standard error before it ends at a limit (spike item 50).
type agyError struct {
	Status    string `json:"status"`
	ErrorCode int    `json:"error_code"`
	Short     string `json:"short_error"`
}

func agyErrorLine(stderr []byte) (agyError, bool) {
	var found agyError
	ok := false
	for _, l := range splitLines(string(stderr)) {
		if rest, has := strings.CutPrefix(strings.TrimSpace(l), "AGY_ERROR:"); has {
			var e agyError
			if json.Unmarshal([]byte(strings.TrimSpace(rest)), &e) == nil {
				found, ok = e, true
			}
		}
	}
	return found, ok
}

// Outcome reads why an agy turn ended (12.2): a limit by AGY_ERROR's status with exit code 3, not by
// the text; a login by the result's error or standard error; a refused tool by denied_actions or
// the line on standard error, because the run still ends with 0.
func (Agy) Outcome(o TurnOutput) Outcome {
	var r struct {
		Status        string `json:"status"`
		Response      string `json:"response"`
		Error         string `json:"error"`
		DeniedActions []any  `json:"denied_actions"`
	}
	have := false
	if o.Result != nil {
		var line struct {
			Result json.RawMessage `json:"result"`
		}
		have = json.Unmarshal(o.Result, &line) == nil && json.Unmarshal(line.Result, &r) == nil
	}
	errText := string(o.Stderr)
	// In a multi-turn or resumed stream-json session, agy's postResponseFailure can write an older
	// turn's AGY_ERROR to standard error as a warning while the turn itself ends with exit 0 and a
	// SUCCESS result (BUG-0006).
	succeeded := have && r.Status == "SUCCESS" && r.Error == "" && o.Exit == 0
	if e, ok := agyErrorLine(o.Stderr); ok && !succeeded && (e.Status == "RESOURCE_EXHAUSTED" || e.ErrorCode == 429) {
		return Outcome{Reason: ReasonQuota, Detail: "agy reached a limit: " + short(e.Short, 300), Response: r.Response}
	}
	login := func(s string) bool {
		s = strings.ToLower(s)
		return strings.Contains(s, "authentication failed") || strings.Contains(s, "authentication required") ||
			strings.Contains(s, "sign in")
	}
	switch {
	case have && login(r.Error), !have && login(errText):
		return Outcome{Reason: ReasonLogin, Detail: "agy is not signed in: " + short(firstNonEmpty(r.Error, lastLine(o.Stderr)), 300)}
	case !have:
		return Outcome{Reason: ReasonError, Detail: fmt.Sprintf("agy printed no result (exit %d): %s", o.Exit, short(lastLine(o.Stderr), 300))}
	case len(r.DeniedActions) > 0 || strings.Contains(errText, "auto-denied"):
		return Outcome{Reason: ReasonRefused, Detail: "agy refused a tool: " + short(lastLine(o.Stderr), 300), Response: r.Response}
	case r.Status == "SUCCESS" && o.Exit == 0:
		if strings.TrimSpace(r.Response) == "" && strings.Contains(errText, "no output produced") {
			return Outcome{Reason: ReasonRefused, Detail: "agy produced no output: " + short(lastLine(o.Stderr), 300)}
		}
		return Outcome{Reason: ReasonOK, Detail: "agy answered", Response: r.Response}
	case strings.Contains(strings.ToLower(r.Error), "quota"):
		return Outcome{Reason: ReasonQuota, Detail: "agy reached a limit: " + short(r.Error, 300), Response: r.Response}
	}
	return Outcome{Reason: ReasonError, Detail: fmt.Sprintf("agy ended with %s (exit %d): %s", r.Status, o.Exit, short(firstNonEmpty(r.Error, lastLine(o.Stderr)), 300)), Response: r.Response}
}

// ConversationFiles is the one file a new sandbox needs to continue (spike items 38 and 45).
func (Agy) ConversationFiles(id, _ string) []string {
	if !SafeID(id) {
		return nil
	}
	return []string{".gemini/antigravity-cli/conversations/" + id + ".db"}
}

func firstNonEmpty(s ...string) string {
	for _, v := range s {
		if strings.TrimSpace(v) != "" {
			return v
		}
	}
	return ""
}
