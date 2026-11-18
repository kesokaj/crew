//go:build talk

// Package talktier is the real tier of conversations (plan M20 D24, E2E 1, 2, 3 and 6's chat half):
// real agy turns in real microVMs, through a throwaway stack (stack.sh). make test-talk runs it on the
// stack's network; it is not part of make check. It sends five messages, so at most five turns of
// agy run, within the six D24 allows, and it sends none after a check failed in its scenario.
package talktier

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/big"
	"net/http"
	"os"
	"path"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/docker/docker/client"
	"github.com/docker/docker/errdefs"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/kesokaj/crew/internal/assistant"
	"github.com/kesokaj/crew/internal/credstore"
	"github.com/kesokaj/crew/internal/httpapi"
	"github.com/kesokaj/crew/internal/models/modelstest"
)

const (
	server  = "http://t20-server:8080"
	admin   = "admin@example.test"
	maxSent = 5 // the messages this test sends; D24 allows six turns
	turnFor = 12 * time.Minute
)

var (
	token      = os.Getenv("T20_INGRESS_TOKEN")
	markMain   = os.Getenv("T20_MARK_MAIN")
	markBranch = os.Getenv("T20_MARK_BRANCH")
)

// api calls the server as the ingress passes the operator's request on, signed in just now.
func api(t *testing.T, method, p, body string) (int, string) {
	t.Helper()
	req, _ := http.NewRequest(method, server+p, strings.NewReader(body))
	req.Header.Set(httpapi.HeaderIngress, token)
	req.Header.Set(httpapi.HeaderEmail, admin)
	req.Header.Set(httpapi.HeaderAuthTime, strconv.FormatInt(time.Now().Unix(), 10))
	req.Header.Set("Origin", "https://crew.test")
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return 0, err.Error()
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(b)
}

func asJSON(v any) string {
	b, _ := json.Marshal(v)
	return string(b)
}

type tier struct {
	pool   *pgxpool.Pool
	docker *client.Client
	sent   chan struct{} // one token per message this test may send
}

func open(t *testing.T) *tier {
	t.Helper()
	if token == "" || markMain == "" || markBranch == "" {
		t.Fatal("run by stack.sh test: the stack's values are not set")
	}
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, "postgres://postgres:"+os.Getenv("T20_DB_PASSWORD")+"@t20-db:5432/crew?sslmode=disable")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	dc, err := client.NewClientWithOpts(client.FromEnv, client.WithAPIVersionNegotiation())
	if err != nil {
		t.Fatal(err)
	}
	k := &tier{pool: pool, docker: dc, sent: make(chan struct{}, maxSent)}
	for range maxSent {
		k.sent <- struct{}{}
	}
	return k
}

// spend takes one message of the five, and checks first that nothing but the assistant ran: no
// pipeline run, no planner, no improver, no summary writer.
func (k *tier) spend(t *testing.T) {
	t.Helper()
	select {
	case <-k.sent:
	default:
		t.Fatal("the test would send a sixth message")
	}
	if n := k.count(t, `SELECT count(*) FROM runs r JOIN subtasks s ON s.id = r.subtask WHERE s.role <> 'assistant'`); n != 0 {
		t.Fatalf("%d runs of another role ran on the stack", n)
	}
}

func (k *tier) count(t *testing.T, q string, args ...any) int {
	t.Helper()
	var n int
	if err := k.pool.QueryRow(context.Background(), q, args...).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func (k *tier) view(t *testing.T, id string) assistant.View {
	t.Helper()
	code, body := api(t, "GET", "/api/conversations/"+id, "")
	if code != 200 {
		t.Fatalf("the view: %d %s", code, body)
	}
	var v assistant.View
	if err := json.Unmarshal([]byte(body), &v); err != nil {
		t.Fatal(err)
	}
	return v
}

// answer waits for the agent's answer number n of a conversation (1 is the first), and fails on a
// turn that ended badly.
func (k *tier) answer(t *testing.T, id string, n int) assistant.Entry {
	t.Helper()
	deadline := time.Now().Add(turnFor)
	for time.Now().Before(deadline) {
		v := k.view(t, id)
		var got []assistant.Entry
		for _, e := range v.Entries {
			if e.Kind == "agent" {
				got = append(got, e)
			}
		}
		if len(got) >= n && strings.TrimSpace(got[n-1].Text) != "" {
			return got[n-1]
		}
		for _, e := range got {
			if e.Ended != nil && e.Ended.Reason != "ok" {
				t.Fatalf("a turn ended %s: %s", e.Ended.Reason, e.Ended.Detail)
			}
		}
		var reason, detail string
		_ = k.pool.QueryRow(context.Background(), `SELECT coalesce(r.stop_reason, ''), coalesce(r.stop_detail, '') FROM runs r
			JOIN conversations c ON c.subtask = r.subtask WHERE c.id = $1 AND r.state = 'ended' AND r.stop_reason <> 'ok'
			ORDER BY r.started_at DESC LIMIT 1`, id).Scan(&reason, &detail)
		if reason != "" {
			t.Fatalf("the turn's run ended %s: %s", reason, detail)
		}
		time.Sleep(5 * time.Second)
	}
	t.Fatalf("no answer %d of %s in %s; it waits: %q", n, id, turnFor, k.view(t, id).Waiting)
	return assistant.Entry{}
}

// tookBefore checks E2E 1: every message of the conversation was taken by a run and is in the
// database before the agent's answer to it; a run's first message is there before the run started.
// A later message of the same run (way 1) is newer than the run, not than its answer.
func (k *tier) tookBefore(t *testing.T, id string) {
	t.Helper()
	if n := k.count(t, `SELECT count(*) FROM conversation_messages m LEFT JOIN runs r ON r.id = m.run
		WHERE m.conversation = $1 AND (r.id IS NULL
			OR NOT EXISTS (SELECT 1 FROM turns a WHERE a.run = r.id AND a.speaker = 'agent' AND a.at > m.created_at)
			OR (m.id = (SELECT min(f.id) FROM conversation_messages f WHERE f.run = r.id) AND m.created_at > r.started_at))`, id); n != 0 {
		t.Fatalf("%d messages of %s were not taken, or were answered before they were kept", n, id)
	}
}

// sandboxOf is the sandbox of a run: its row's kind, project, repositories, container, and whether
// it is removed.
type sandboxOf struct {
	kind, project, container string
	repos                    []string
	removed                  bool
}

func (k *tier) sandbox(t *testing.T, run string) sandboxOf {
	t.Helper()
	var s sandboxOf
	if err := k.pool.QueryRow(context.Background(), `SELECT s.kind, coalesce(s.project::text, ''), s.container, s.repos, s.removed_at IS NOT NULL
		FROM runs r JOIN sandboxes s ON s.id = r.sandbox WHERE r.id = $1`, run).Scan(&s.kind, &s.project, &s.container, &s.repos, &s.removed); err != nil {
		t.Fatal(err)
	}
	return s
}

// gone waits until the run ended and its microVM is no longer on the machine.
func (k *tier) gone(t *testing.T, run string, limit time.Duration) {
	t.Helper()
	deadline := time.Now().Add(limit)
	for time.Now().Before(deadline) {
		var state string
		if err := k.pool.QueryRow(context.Background(), `SELECT state FROM runs WHERE id = $1`, run).Scan(&state); err != nil {
			t.Fatal(err)
		}
		if s := k.sandbox(t, run); state == "ended" && s.removed {
			if _, err := k.docker.ContainerInspect(context.Background(), s.container); errdefs.IsNotFound(err) {
				return
			}
		}
		time.Sleep(10 * time.Second)
	}
	t.Fatalf("run %s or its microVM is still there after %s", run, limit)
}

func word(t *testing.T) string {
	n, err := rand.Int(rand.Reader, big.NewInt(9000))
	if err != nil {
		t.Fatal(err)
	}
	return fmt.Sprintf("heron-%d", 1000+n.Int64())
}

// keepFile reads keep.txt from the work folder Crew stored for a conversation, sealed with the stack's
// key file for that conversation (D41): it is no gzip that opens without the key.
func (k *tier) keepFile(t *testing.T, id string) []byte {
	t.Helper()
	var sealed []byte
	var note string
	if err := k.pool.QueryRow(context.Background(), `SELECT gzip, coalesce(note, '') FROM work_folders WHERE conversation = $1`, id).Scan(&sealed, &note); err != nil {
		t.Fatalf("the work folder of %s: %v", id, err)
	}
	if bytes.HasPrefix(sealed, []byte{0x1f, 0x8b}) {
		t.Fatal("the work folder is stored as a gzip that is not sealed")
	}
	key, err := os.ReadFile(os.Getenv("T20_KEY_FILE"))
	if err != nil {
		t.Fatal(err)
	}
	vault, err := credstore.NewVault(k.pool, key)
	if err != nil {
		t.Fatal(err)
	}
	gz, err := vault.Open("work-folder:"+id, sealed)
	if err != nil {
		t.Fatalf("the stored work folder does not open with the stack's key: %v", err)
	}
	zr, err := gzip.NewReader(bytes.NewReader(gz))
	if err != nil {
		t.Fatal(err)
	}
	tr := tar.NewReader(zr)
	for {
		h, err := tr.Next()
		if errors.Is(err, io.EOF) {
			t.Fatalf("the stored work folder holds no keep.txt (note %q)", note)
		}
		if err != nil {
			t.Fatal(err)
		}
		if path.Base(h.Name) == "keep.txt" {
			b, _ := io.ReadAll(tr)
			return b
		}
	}
}

func TestConversationsWithRealAgy(t *testing.T) {
	k := open(t)
	modelstest.Recorded(t, k.pool, "agy")
	t.Cleanup(func() {
		var runs, turns int
		_ = k.pool.QueryRow(context.Background(), `SELECT (SELECT count(*) FROM runs), (SELECT count(*) FROM turns WHERE speaker = 'agent')`).Scan(&runs, &turns)
		t.Logf("the stack ran %d runs with %d answers of agy", runs, turns)
		if runs > 6 {
			t.Errorf("%d runs of agy, more than D24's six", runs)
		}
	})

	// E2E 1 and 2: a conversation about the project, its sandbox removed when idle, and the next
	// message answered in a new sandbox with the CLI's files and the work folder put back.
	t.Run("about a project, way 2", func(t *testing.T) {
		t.Parallel()
		code := word(t)
		k.spend(t)
		st, body := api(t, "POST", "/api/conversations", asJSON(map[string]string{"project": "t20app", "text": "Please do three small things. " +
			"1) Read README.md in /home/agent/work and tell me the mark it names. " +
			"2) Run exactly this command: head -c 9 /dev/urandom | base64 > /home/agent/conversation/keep.txt " +
			"3) Remember this code word for later, and do not write it into any file: " + code + ". Answer in two short lines."}))
		if st != 201 {
			t.Fatalf("start: %d %s", st, body)
		}
		var v assistant.View
		json.Unmarshal([]byte(body), &v)
		first := k.answer(t, v.ID, 1)
		t.Logf("first answer (%s/%s, way %q): %s", first.CLI, first.Model, first.Way, first.Text)
		if !strings.Contains(first.Text, markMain) {
			t.Errorf("the first answer does not name main's mark %s: it did not read the project's copy", markMain)
		}
		k.tookBefore(t, v.ID)
		// repos are the GitHub repositories its token may read: none for a local project, whose copy of
		// main comes from Crew's git endpoint, as the mark above shows.
		if s := k.sandbox(t, first.Run); s.kind != "conversation" || s.project == "" || len(s.repos) != 0 {
			t.Errorf("the sandbox of a conversation about a project: %+v", s)
		}
		keep := k.keepFile(t, v.ID)
		sum := sha256.Sum256(keep)
		hash := hex.EncodeToString(sum[:])
		// The idle removal: conversations.idle_minutes is 5 on the stack.
		k.gone(t, first.Run, 9*time.Minute)
		k.spend(t)
		if st, body := api(t, "POST", "/api/conversations/"+v.ID+"/messages", asJSON(map[string]string{"text": "Run exactly this command and give me its whole output line: " +
			"sha256sum /home/agent/conversation/keep.txt . Then tell me the code word I gave you in my first message."})); st != 202 {
			t.Fatalf("the second message: %d %s", st, body)
		}
		second := k.answer(t, v.ID, 2)
		t.Logf("second answer (way %q, run %s): %s", second.Way, second.Run, second.Text)
		if second.Run == first.Run || second.Way != "files" {
			t.Errorf("the second turn is not a new run with the CLI's files put back: run %s after %s, way %q", second.Run, first.Run, second.Way)
		}
		if !strings.Contains(second.Text, hash[:16]) {
			t.Errorf("the second answer does not hold keep.txt's sha256 %s: the work folder did not come back", hash[:16])
		}
		if !strings.Contains(second.Text, code) {
			t.Errorf("the second answer does not hold the code word %s: the CLI's files did not come back", code)
		}
		k.tookBefore(t, v.ID)
		if st, body := api(t, "POST", "/api/conversations/"+v.ID+"/close", ""); st != 204 {
			t.Fatalf("close: %d %s", st, body)
		}
		k.gone(t, second.Run, 3*time.Minute)
	})

	// E2E 3: about no project (a normal conversation across Crew and the map, design 1.33) there is no
	// single-project repository mounted in /home/agent/work and no GitHub token, while the Crew MCP gives
	// read-only access to the Crew map and non-archived projects.
	t.Run("about no project", func(t *testing.T) {
		t.Parallel()
		k.spend(t)
		st, body := api(t, "POST", "/api/conversations", asJSON(map[string]string{"text": "Run ls -A /home/agent/work and tell me what it printed, or that it printed nothing. " +
			"Then call your get_crew_map tool and name the project key it lists. Two short lines."}))
		if st != 201 {
			t.Fatalf("start: %d %s", st, body)
		}
		var v assistant.View
		json.Unmarshal([]byte(body), &v)
		a := k.answer(t, v.ID, 1)
		t.Logf("answer: %s", a.Text)
		k.tookBefore(t, v.ID)
		if s := k.sandbox(t, a.Run); s.kind != "conversation" || s.project != "" || len(s.repos) != 0 {
			t.Errorf("the sandbox of a conversation about no project: %+v", s)
		}
		// Every run has one token, for the git endpoint and the Crew MCP; this one names no single project
		// or draft on run_tokens, while reaching the read-only Crew map tools.
		if n := k.count(t, `SELECT count(*) FROM run_tokens WHERE run = $1 AND (project IS NOT NULL OR draft IS NOT NULL)`, a.Run); n != 0 {
			t.Errorf("the token of a conversation about no project names a project or a draft (%d)", n)
		}
		if n := k.count(t, `SELECT count(*) FROM mcp_calls WHERE run = $1 AND tool = 'get_crew_map' AND ok`, a.Run); n == 0 {
			t.Errorf("a conversation about no project did not call get_crew_map")
		}
		if st, body := api(t, "POST", "/api/conversations/"+v.ID+"/close", ""); st != 204 {
			t.Fatalf("close: %d %s", st, body)
		}
		k.gone(t, a.Run, 3*time.Minute)
	})

	// E2E 6's chat half: two questions in a ticket's chat, the second in the same sandbox (way 1);
	// the chat reads the ticket's branch and its diff through the chat's own tool.
	t.Run("the chat of a ticket", func(t *testing.T) {
		t.Parallel()
		k.spend(t)
		st, body := api(t, "POST", "/api/projects/t20app/tasks/TA-1/chat", asJSON(map[string]string{"text": "What mark does NOTE.md on this ticket's branch name? " +
			"Read the file in your workspace, /home/agent/work, and answer in one line."}))
		if st != 202 {
			t.Fatalf("the first question: %d %s", st, body)
		}
		var made struct{ Conversation string }
		json.Unmarshal([]byte(body), &made)
		first := k.answer(t, made.Conversation, 1)
		t.Logf("first answer: %s", first.Text)
		if !strings.Contains(first.Text, markBranch) {
			t.Errorf("the chat's answer does not name the branch's mark %s", markBranch)
		}
		if s := k.sandbox(t, first.Run); s.kind != "chat" || s.project == "" || len(s.repos) != 0 {
			t.Errorf("the chat's sandbox: %+v", s)
		}
		k.spend(t)
		if st, body := api(t, "POST", "/api/projects/t20app/tasks/TA-1/chat", asJSON(map[string]string{"text": "Use your get_diff tool and tell me which files " +
			"this ticket changes against main. One line."})); st != 202 {
			t.Fatalf("the second question: %d %s", st, body)
		}
		second := k.answer(t, made.Conversation, 2)
		t.Logf("second answer (run %s, turn %d): %s", second.Run, second.Turn, second.Text)
		if second.Run != first.Run || second.Turn <= first.Turn {
			t.Errorf("the second question is not a turn in the same sandbox: run %s turn %d after run %s turn %d", second.Run, second.Turn, first.Run, first.Turn)
		}
		if n := k.count(t, `SELECT count(*) FROM mcp_calls WHERE run = $1 AND tool = 'get_diff' AND ok`, second.Run); n == 0 {
			t.Error("the chat's turn did not call get_diff")
		}
		if !strings.Contains(second.Text, "NOTE.md") {
			t.Error("the chat's answer does not name NOTE.md")
		}
		k.tookBefore(t, made.Conversation)
		if st, body := api(t, "POST", "/api/conversations/"+made.Conversation+"/close", ""); st != 204 {
			t.Fatalf("close: %d %s", st, body)
		}
		k.gone(t, second.Run, 3*time.Minute)
	})
}
