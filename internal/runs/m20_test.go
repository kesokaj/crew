package runs

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"io"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/kesokaj/crew/internal/cli"
	"github.com/kesokaj/crew/internal/credstore"
	"github.com/kesokaj/crew/internal/harness"
	"github.com/kesokaj/crew/internal/runspec"
	"github.com/kesokaj/crew/internal/settings"
)

// The conversations of M20 (11.8, 11.9, 12.10): a conversation's or a chat's turns are runs of one
// subtask of the scope conversation, which the conversation's row names (plan M20 D2).

const cshop = "5c000000-0000-4000-8000-000000000020"

// convKit is a kit with the project shop, its repository, and its ticket SF-1.
func convKit(t *testing.T) (*kit, int64) {
	t.Helper()
	k := newKit(t)
	k.d.MCP = "/mcp"
	ctx := context.Background()
	for _, q := range []string{`INSERT INTO projects (id, name, key, repo_source, state) VALUES ($1, 'shop', 'SF', 'local', 'running')`,
		`INSERT INTO git_repos (name, project) VALUES ('shop', $1)`} {
		if _, err := k.db.Server.Exec(ctx, q, cshop); err != nil {
			t.Fatal(err)
		}
	}
	var id int64
	if err := k.db.Server.QueryRow(ctx, `INSERT INTO tickets (project, number, title, kind, stage, origin, created_by, position)
		VALUES ($1, 1, 'Lock', 'feature', 'in_progress', 'plan', 'lead', 1) RETURNING id`, cshop).Scan(&id); err != nil {
		t.Fatal(err)
	}
	return k, id
}

// conversation makes the row of a conversation of the operator's (project "" for none) or, with a
// ticket, the ticket's chat, as internal/assistant does.
func (k *kit) conversation(purpose, project string, ticket int64) string {
	k.t.Helper()
	id := newUUID()
	if _, err := k.db.Server.Exec(context.Background(), `INSERT INTO conversations (id, project, ticket, purpose)
		VALUES ($1, nullif($2, '')::uuid, nullif($3, 0), $4)`, id, project, ticket, purpose); err != nil {
		k.t.Fatal(err)
	}
	return id
}

func (k *kit) talk(conv string) (string, int64) {
	k.t.Helper()
	return k.startRole(RoleRun{Role: "assistant", Kind: "talk", Conversation: conv, ReadOnly: true,
		Parts: []PromptPart{{Part: "agent", Source: "baseline/agents/assistant.md@test", Text: "You are the assistant."},
			{Part: "operator", Source: "message 1", Text: "What does the shop sell?"}}})
}

func TestAConversationsTurnIsARunOfItsOwnSubtask(t *testing.T) {
	k, _ := convKit(t)
	ctx := context.Background()
	conv := k.conversation("operator", cshop, 0)
	k.play(harness.Script{Recording: agyRun, Hold: true})
	run, sub := k.talk(conv)
	v := k.wait(run, "working")
	if n := k.count(`SELECT count(*) FROM conversations`); n != 1 {
		t.Fatalf("%d conversations: the run made one of its own", n)
	}
	if n := k.count(`SELECT count(*) FROM subtasks s JOIN conversations c ON c.subtask = s.id AND c.id = $2
		WHERE s.id = $1 AND s.scope = 'conversation' AND s.number = 1 AND s.project = $3 AND s.ticket IS NULL AND s.role = 'assistant'`, sub, conv, cshop); n != 1 {
		t.Fatal("the subtask is not the conversation's, of the scope conversation")
	}
	if n := k.count(`SELECT count(*) FROM runs WHERE id = $1 AND conversation = $2 AND NOT push AND size = 'large'`, run, conv); n != 1 {
		t.Fatal("the run is not of the conversation, read-only and large")
	}
	s := started(k)[0]
	// What a run of its project gets: main to read, the Crew MCP, Docker inside, the work folder.
	if !s.Conversation || s.Chat || s.Repo != "shop" || s.Branch != "main" || !s.ReadOnly || s.MCP != "/mcp" || s.Minimal ||
		s.Project != cshop || s.KeepFolder != "conversation" || !s.NoRepoConfig || !s.MainInstructions || s.Refresh {
		t.Fatalf("the spec: %+v", s)
	}
	k.d.Auth = fakeAuth{sandbox: v.Sandbox}
	c, err := k.d.MCPCaller(mcpRequest("the-token"))
	if err != nil || c.Role != "assistant" || c.Project != cshop || c.Ticket != 0 || c.Conversation != conv || c.ChatTicket != 0 {
		t.Fatalf("the caller: %+v %v", c, err)
	}
	acc, err := k.d.GitAccess(gitReq(), "shop")
	if err != nil || len(acc.Push) != 0 {
		t.Fatalf("the git access: %+v %v", acc, err)
	}
	for _, ref := range acc.Read {
		if strings.HasPrefix(ref, "refs/heads/") && ref != "refs/heads/main" {
			t.Errorf("a conversation reads %s", ref)
		}
	}
	// Only the assistant's run takes a conversation, and only one of the operator's or a chat.
	for _, rr := range []RoleRun{{Role: "lead", Kind: "talk", Conversation: conv, Project: cshop, ReadOnly: true},
		{Role: "assistant", Kind: "talk", ReadOnly: true}} {
		rr.CLI, rr.Parts = "agy", []PromptPart{{Part: "x", Text: "x"}}
		if _, _, err := k.d.StartRole(ctx, rr); err == nil {
			t.Errorf("%+v started", rr)
		}
	}
}

func TestAConversationAboutNoProjectHasNoRepositoryAndNoMCP(t *testing.T) {
	k, _ := convKit(t)
	ctx := context.Background()
	conv := k.conversation("operator", "", 0)
	k.play(harness.Script{Recording: agyRun, Hold: true})
	run, sub := k.talk(conv)
	v := k.wait(run, "working")
	s := started(k)[0]
	if !s.Conversation || s.Repo != "" || s.Branch != "" || s.MCP != "/mcp" || s.Project != "" || len(s.Repos) != 0 || s.Mode != "internet" ||
		s.KeepFolder != "conversation" {
		t.Fatalf("the spec: %+v", s)
	}
	if n := k.count(`SELECT count(*) FROM subtasks WHERE id = $1 AND scope = 'conversation' AND project IS NULL AND draft IS NULL`, sub); n != 1 {
		t.Fatal("the subtask names a project")
	}
	k.d.Auth = fakeAuth{sandbox: v.Sandbox}
	acc, err := k.d.GitAccess(gitReq(), "shop")
	if err != nil || len(acc.Push) != 0 || !slices.Equal(acc.Read, []string{"refs/heads/main"}) {
		t.Fatalf("normal conversation git access to shop: %+v, %v", acc, err)
	}
	// An archived project's repository is refused.
	other := newUUID()
	for _, q := range []string{
		`INSERT INTO projects (id, name, key, repo_source, state) VALUES ($1, 'old', 'OLD', 'local', 'archived')`,
		`INSERT INTO git_repos (name, project) VALUES ('old', $1)`,
	} {
		if _, err := k.db.Server.Exec(ctx, q, other); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := k.d.GitAccess(gitReq(), "old"); err == nil {
		t.Fatal("a normal conversation reached an archived project's repository")
	}
	// A normal conversation with network_mode = 'model_only' starts in model_only mode.
	convModel := k.conversation("operator", "", 0)
	if _, err := k.db.Server.Exec(ctx, `UPDATE conversations SET network_mode = 'model_only' WHERE id = $1`, convModel); err != nil {
		t.Fatal(err)
	}
	k.play(harness.Script{Recording: agyRun, Hold: true})
	runModel, _ := k.talk(convModel)
	k.wait(runModel, "working")
	if sm := started(k)[1]; sm.Mode != "model_only" {
		t.Fatalf("model_only conversation mode: %+v", sm)
	}
}

// A ticket's chat reads the ticket's branch, its subtasks' branches and main, fetched again at every
// turn, and nothing else (11.9, D7).
func TestAChatReadsItsTicketsBranchesAndNoOther(t *testing.T) {
	k, ticket := convKit(t)
	ctx := context.Background()
	for _, q := range []string{
		`UPDATE tickets SET branch = 'task/SF-1-lock' WHERE id = $1 AND project = $2`,
		`INSERT INTO subtasks (scope, project, ticket, number, kind, role, branch) VALUES ('ticket', $2, $1, 1, 'build', 'builder', 'task/SF-1-lock'),
			('ticket', $2, $1, 2, 'build', 'builder', 'sub/SF-1.2')`,
		`INSERT INTO tickets (project, number, title, kind, stage, origin, created_by, position, branch)
			SELECT $2, 2, 'Other', 'feature', 'in_progress', 'plan', 'lead', 2, 'task/SF-2-other' WHERE $1::bigint > 0`,
	} {
		if _, err := k.db.Server.Exec(ctx, q, ticket, cshop); err != nil {
			t.Fatal(err)
		}
	}
	conv := k.conversation("chat", cshop, ticket)
	k.play(harness.Script{Recording: agyRun, Hold: true})
	run, _ := k.talk(conv)
	v := k.wait(run, "working")
	s := started(k)[0]
	if !s.Chat || s.Conversation || s.Branch != "task/SF-1-lock" || !s.Refresh || !slices.Equal(s.Also, []string{"sub/SF-1.2"}) ||
		s.Commit != "" || !s.ReadOnly || s.MCP != "/mcp" || s.KeepFolder != "conversation" || !s.NoRepoConfig || !s.MainInstructions {
		t.Fatalf("the spec: %+v", s)
	}
	k.d.Auth = fakeAuth{sandbox: v.Sandbox}
	c, err := k.d.MCPCaller(mcpRequest("the-token"))
	if err != nil || c.Role != "assistant" || c.Ticket != 0 || c.ChatTicket != ticket || c.Conversation != conv {
		t.Fatalf("the caller: %+v %v", c, err)
	}
	acc, err := k.d.GitAccess(gitReq(), "shop")
	if err != nil || len(acc.Push) != 0 {
		t.Fatalf("the git access: %+v %v", acc, err)
	}
	for _, want := range []string{"refs/heads/main", "refs/heads/task/SF-1-lock", "refs/heads/sub/SF-1.2"} {
		if !slices.Contains(acc.Read, want) {
			t.Errorf("the chat cannot read %s: %v", want, acc.Read)
		}
	}
	if slices.Contains(acc.Read, "refs/heads/task/SF-2-other") {
		t.Errorf("the chat reads another ticket's branch: %v", acc.Read)
	}
}

// A chat before its ticket has a branch reads main; once Done, main at the merge commit; a ref that
// changed between two turns is a new run with the new ref (D7).
func TestAChatsRefFollowsItsTicket(t *testing.T) {
	k, ticket := convKit(t)
	ctx := context.Background()
	conv := k.conversation("chat", cshop, ticket)
	run, sub := k.talk(conv)
	k.wait(run, "idle")
	if s := started(k)[0]; s.Branch != "main" || s.Commit != "" || !s.Refresh {
		t.Fatalf("before a branch: %+v", s)
	}
	// The same ref: the next turn is in the same microVM.
	same, err := k.d.Continue(ctx, sub, ContinueRequest{Text: "And now?"})
	if err != nil || same != run {
		t.Fatalf("the same ref continued in %s, %v", same, err)
	}
	k.wait(run, "idle")
	if _, err := k.db.Server.Exec(ctx, `UPDATE tickets SET branch = 'task/SF-1-lock' WHERE id = $1`, ticket); err != nil {
		t.Fatal(err)
	}
	next, err := k.d.Continue(ctx, sub, ContinueRequest{Text: "And with the branch?"})
	if err != nil || next == run {
		t.Fatalf("a new ref continued in %s, %v", next, err)
	}
	k.wait(next, "idle")
	if v := k.wait(run, "ended"); v.StopReason != cli.ReasonOK {
		t.Fatalf("the run before: %+v", v)
	}
	if s := started(k)[1]; s.Branch != "task/SF-1-lock" || !s.Chat || s.Base != "refs/heads/task/SF-1-lock" {
		t.Fatalf("with a branch: %+v", s)
	}
	merge := strings.Repeat("e", 40)
	for _, q := range []string{`UPDATE tickets SET stage = 'done' WHERE id = $1 AND project = $2`,
		`INSERT INTO check_runs (project, ticket, purpose, commit, state) VALUES ($2, $1, 'merge', '` + merge + `', 'passed')`} {
		if _, err := k.db.Server.Exec(ctx, q, ticket, cshop); err != nil {
			t.Fatal(err)
		}
	}
	done, err := k.d.Continue(ctx, sub, ContinueRequest{Text: "And once Done?"})
	if err != nil || done == next {
		t.Fatalf("a Done ticket continued in %s, %v", done, err)
	}
	k.wait(done, "idle")
	if s := started(k)[2]; s.Branch != "main" || s.Commit != merge {
		t.Fatalf("once Done: %+v", s)
	}
	if n := k.count(`SELECT count(*) FROM runs WHERE id = $1 AND branch = 'main' AND git_commit = $2`, done, merge); n != 1 {
		t.Fatal("the run does not record the commit it reads")
	}
}

// Nothing but the operator's next message continues a conversation (D23): a turn that ended for a
// restart, an error, a sign-in or a limit leaves its subtask stopped, and the monitor starts nothing.
func TestCrewNeverContinuesAConversationByItself(t *testing.T) {
	for _, reason := range []string{ReasonRestart, cli.ReasonError, cli.ReasonLogin, cli.ReasonQuota} {
		t.Run(reason, func(t *testing.T) {
			k, _ := convKit(t)
			ctx := context.Background()
			conv := k.conversation("operator", cshop, 0)
			if reason == ReasonRestart {
				k.play(harness.Script{Recording: agyRun, Hold: true})
			} else {
				k.play(harness.Script{Recording: agyRun, Exit: runspec.Exit{Exit: 1, Reason: reason, Detail: "made up"}})
			}
			run, sub := k.talk(conv)
			if reason == ReasonRestart {
				k.wait(run, "working")
				lr, _ := k.d.getLive(run)
				k.d.endRun(ctx, lr, ReasonRestart, "its microVM did not survive the restart", nil, true)
			}
			if v := k.wait(run, "ended"); v.StopReason != reason {
				t.Fatalf("ended %+v", v)
			}
			if n := k.count(`SELECT count(*) FROM subtasks WHERE id = $1 AND state = 'stopped' AND wait_for IS NULL`, sub); n != 1 {
				var state, wait string
				k.db.Server.QueryRow(ctx, `SELECT state, coalesce(wait_for, '') FROM subtasks WHERE id = $1`, sub).Scan(&state, &wait)
				t.Fatalf("the subtask is %s, waiting for %q", state, wait)
			}
			// The CLI's login and the limit are there again: nothing it waits for is missing.
			k.db.Server.Exec(ctx, `UPDATE cli_logins SET checked_at = now() + interval '1 minute'`)
			k.db.Server.Exec(ctx, `UPDATE subtasks SET wait_until = now() - interval '1 minute' WHERE id = $1`, sub)
			k.d.Tick(ctx)
			k.d.Tick(ctx)
			if n := k.count(`SELECT count(*) FROM runs WHERE subtask = $1`, sub); n != 1 {
				t.Fatalf("%d runs of the conversation after the monitor", n)
			}
			// The operator's next message does continue it.
			if _, err := k.d.Continue(ctx, sub, ContinueRequest{Text: "Again, please."}); err != nil {
				t.Fatal(err)
			}
		})
	}
}

// A conversation's run waits conversations.idle_minutes for the next message, read when it starts
// (12.10, 14.1); every other run waits the driver's IdleFor.
func TestAConversationIdlesForIdleMinutes(t *testing.T) {
	k, _ := convKit(t)
	ctx := context.Background()
	def, _ := settings.Defaults(settings.Machine{Version: "1.0.0"})
	doc := settings.Merge(def, settings.Doc{"conversations": settings.Doc{"idle_minutes": 7}})
	if err := settings.Save(ctx, k.db.Server, settings.Machine{Version: "1.0.0"}, doc, "test"); err != nil {
		t.Fatal(err)
	}
	conv := k.conversation("operator", cshop, 0)
	run, sub := k.talk(conv)
	k.wait(run, "idle")
	plain := k.start(StartRequest{})
	k.wait(plain, "idle")
	at := func(d time.Duration) {
		k.mu.Lock()
		k.now = time.Now().Add(d)
		k.mu.Unlock()
		k.d.Tick(ctx)
	}
	at(6*time.Minute + 30*time.Second)
	if v := k.wait(run, "idle", "ended"); v.State != "idle" {
		t.Fatalf("ended after 6.5 minutes of 7: %+v", v)
	}
	at(7*time.Minute + 30*time.Second)
	if v := k.wait(run, "ended"); v.StopReason != cli.ReasonOK || v.StopDetail != IdleEnded {
		t.Fatalf("after 7.5 minutes: %+v", v)
	}
	if n := k.count(`SELECT count(*) FROM subtasks WHERE id = $1 AND state = 'done'`, sub); n != 1 {
		t.Fatal("an idle end left the subtask other than done")
	}
	// The run of the fixture still waits: 7.5 minutes is less than its 10.
	if v := k.wait(plain, "idle", "ended"); v.State != "idle" {
		t.Fatalf("the plain run: %+v", v)
	}
}

// The CLI's files of a conversation of the operator's are kept conversations.keep_cli_files_days, not
// the machine's keep_cli_files_days (12.10, D13).
func TestAConversationsCLIFilesAreKeptByItsSetting(t *testing.T) {
	k, _ := convKit(t)
	ctx := context.Background()
	def, _ := settings.Defaults(settings.Machine{Version: "1.0.0"})
	doc := settings.Merge(def, settings.Doc{"keep_cli_files_days": 3, "conversations": settings.Doc{"keep_cli_files_days": 12}})
	if err := settings.Save(ctx, k.db.Server, settings.Machine{Version: "1.0.0"}, doc, "test"); err != nil {
		t.Fatal(err)
	}
	file := ".gemini/antigravity-cli/conversations/" + agyConv + ".db"
	for purpose, days := range map[string]float64{"operator": 12, "chat": 12, "run": 3} {
		var run string
		if purpose == "run" {
			k.play(harness.Script{Recording: agyRun, Files: map[string][]byte{file: []byte("{}\n")}})
			run = k.start(StartRequest{})
		} else {
			var ticket int64
			if purpose == "chat" {
				k.db.Server.QueryRow(ctx, `SELECT id FROM tickets WHERE project = $1`, cshop).Scan(&ticket)
			}
			k.play(harness.Script{Recording: agyRun, Files: map[string][]byte{file: []byte("{}\n")}})
			run, _ = k.talk(k.conversation(purpose, cshop, ticket))
		}
		k.wait(run, "idle")
		var got float64
		if err := k.db.Server.QueryRow(ctx, `SELECT extract(epoch FROM f.keep_until - f.saved_at) / 86400 FROM cli_files f
			JOIN runs r ON r.conversation = f.conversation WHERE r.id = $1`, run).Scan(&got); err != nil {
			t.Fatal(err)
		}
		if got < days-0.01 || got > days+0.01 {
			t.Errorf("%s: kept %.2f days, want %v", purpose, got, days)
		}
	}
}

// vaultOf is a vault of a key of its own, as a key file gives one (D41).
func vaultOf(t *testing.T, k *kit) *credstore.Vault {
	t.Helper()
	key := make([]byte, credstore.KeySize)
	if _, err := rand.Read(key); err != nil {
		t.Fatal(err)
	}
	v, err := credstore.NewVault(k.db.Server, key)
	if err != nil {
		t.Fatal(err)
	}
	return v
}

// storedFolder is a conversation's work folder as the database holds it, and opened with the driver's
// key; what is stored is no gzip anyone can read without the key (D41).
func (k *kit) storedFolder(conv string) (map[string]string, string) {
	k.t.Helper()
	var sealed []byte
	var note string
	if err := k.db.Server.QueryRow(context.Background(), `SELECT gzip, coalesce(note, '') FROM work_folders WHERE conversation = $1`, conv).Scan(&sealed, &note); err != nil {
		k.t.Fatal(err)
	}
	if bytes.HasPrefix(sealed, []byte{0x1f, 0x8b}) {
		k.t.Fatal("the work folder is stored as a gzip that is not sealed")
	}
	gz, err := k.d.Seal.Open("work-folder:"+conv, sealed)
	if err != nil {
		k.t.Fatalf("the stored work folder does not open: %v", err)
	}
	return folderFiles(k.t, gz), note
}

// folderFiles reads a stored work folder into its files.
func folderFiles(t *testing.T, gz []byte) map[string]string {
	t.Helper()
	zr, err := gzip.NewReader(bytes.NewReader(gz))
	if err != nil {
		t.Fatal(err)
	}
	tr := tar.NewReader(zr)
	out := map[string]string{}
	for {
		h, err := tr.Next()
		if errors.Is(err, io.EOF) {
			return out
		}
		if err != nil {
			t.Fatal(err)
		}
		b, _ := io.ReadAll(tr)
		out[h.Name] = string(b)
	}
}

// A conversation's work folder is saved after a turn with the secrets the sandbox was given taken out
// (D18), kept while it does not change, and put back in the next sandbox (11.8, D17).
func TestAWorkFolderIsSavedScrubbedAndPutBack(t *testing.T) {
	k, _ := convKit(t)
	k.f.Dir = t.TempDir()
	ctx := context.Background()
	conv := k.conversation("operator", cshop, 0)
	k.play(harness.Script{Recording: agyRun, Folder: map[string][]byte{
		"notes.md":     []byte("the login was " + secret + "\nand more\n"),
		"data/out.bin": append([]byte{0xff, 0x00}, []byte(secret)...),
	}})
	run, sub := k.talk(conv)
	k.wait(run, "idle")
	var files int
	if err := k.db.Server.QueryRow(ctx, `SELECT files FROM work_folders WHERE conversation = $1`, conv).Scan(&files); err != nil {
		t.Fatal(err)
	}
	got, note := k.storedFolder(conv)
	if files != 2 || note != "" || !strings.Contains(got["notes.md"], "and more") || strings.Contains(got["notes.md"], secret) ||
		strings.Contains(got["data/out.bin"], secret) || got["data/out.bin"][0] != 0xff {
		t.Fatalf("stored %d files, note %q: %q", files, note, got)
	}
	// The harness's file beside the home is gone once it is stored.
	if left, _ := filepath.Glob(filepath.Join(k.f.Dir, "*", harness.FolderFile)); len(left) != 0 {
		t.Fatalf("left %v", left)
	}
	// A turn that changes nothing keeps the row as it was.
	var saved time.Time
	k.db.Server.QueryRow(ctx, `SELECT saved_at FROM work_folders WHERE conversation = $1`, conv).Scan(&saved)
	k.play(harness.Script{Recording: agyRun, Folder: map[string][]byte{
		"notes.md":     []byte("the login was " + secret + "\nand more\n"),
		"data/out.bin": append([]byte{0xff, 0x00}, []byte(secret)...),
	}})
	if _, err := k.d.Continue(ctx, sub, ContinueRequest{Text: "Look again."}); err != nil {
		t.Fatal(err)
	}
	k.wait(run, "idle")
	if n := k.count(`SELECT count(*) FROM work_folders WHERE conversation = $1 AND saved_at = $2`, conv, saved); n != 1 {
		t.Fatal("an unchanged folder was written again")
	}
	// The sandbox goes idle and is removed; the next message puts the folder back.
	k.mu.Lock()
	k.now = time.Now().Add(31 * time.Minute)
	k.mu.Unlock()
	k.d.Tick(ctx)
	k.wait(run, "ended")
	next, err := k.d.Continue(ctx, sub, ContinueRequest{Text: "And now?"})
	if err != nil {
		t.Fatal(err)
	}
	k.wait(next, "idle", "ended")
	s := started(k)[1]
	if s.Run != next || s.KeepFolder != harness.ConversationFolder || folderFiles(t, s.Folder)["notes.md"] != got["notes.md"] {
		t.Fatalf("put back: %s %q %q", s.Run, s.KeepFolder, folderFiles(t, s.Folder))
	}
}

// A work folder over its limits is not saved: the last one saved stays, with the note why (D17).
func TestAWorkFolderOverItsLimitsKeepsTheLastWithANote(t *testing.T) {
	k, _ := convKit(t)
	k.f.Dir = t.TempDir()
	ctx := context.Background()
	conv := k.conversation("operator", cshop, 0)
	k.play(harness.Script{Recording: agyRun, Folder: map[string][]byte{"a.txt": []byte("a\n")}})
	run, sub := k.talk(conv)
	k.wait(run, "idle")
	many := map[string][]byte{}
	for i := range harness.FolderMaxEntries + 1 {
		many[fmt.Sprintf("f%d", i)] = nil
	}
	k.play(harness.Script{Recording: agyRun, Folder: many})
	if _, err := k.d.Continue(ctx, sub, ContinueRequest{Text: "Make many files."}); err != nil {
		t.Fatal(err)
	}
	k.wait(run, "idle")
	kept, note := k.storedFolder(conv)
	if kept["a.txt"] != "a\n" || !strings.Contains(note, "not saved") || !strings.Contains(note, "20000") {
		t.Fatalf("kept %q, note %q", kept, note)
	}
}

// A work folder sealed with another key file is not put back: the new sandbox starts without it and
// the Sandbox card says why. A server with no key file stores none (D41).
func TestAWorkFolderOfAnotherKeyIsNotPutBack(t *testing.T) {
	k, _ := convKit(t)
	k.f.Dir = t.TempDir()
	ctx := context.Background()
	conv := k.conversation("operator", cshop, 0)
	k.play(harness.Script{Recording: agyRun, Folder: map[string][]byte{"a.txt": []byte("a\n")}})
	run, sub := k.talk(conv)
	k.wait(run, "idle")
	k.d.Seal = vaultOf(t, k) // the key file changed
	k.mu.Lock()
	k.now = time.Now().Add(31 * time.Minute)
	k.mu.Unlock()
	k.d.Tick(ctx)
	k.wait(run, "ended")
	// Read while the new turn works: its end stores what that sandbox's folder holds then.
	k.play(harness.Script{Recording: agyRun, Hold: true})
	next, err := k.d.Continue(ctx, sub, ContinueRequest{Text: "Is it there?"})
	if err != nil {
		t.Fatal(err)
	}
	k.wait(next, "working")
	var note string
	k.db.Server.QueryRow(ctx, `SELECT coalesce(note, '') FROM work_folders WHERE conversation = $1`, conv).Scan(&note)
	if s := started(k)[1]; s.Run != next || s.Folder != nil || !strings.Contains(note, "does not open with this key file") {
		t.Fatalf("put back %d bytes, note %q", len(s.Folder), note)
	}
	// With no key file nothing is stored, and the note says why.
	other := k.conversation("operator", cshop, 0)
	k.d.Seal = nil
	k.play(harness.Script{Recording: agyRun, Folder: map[string][]byte{"b.txt": []byte("b\n")}})
	r2, _ := k.talk(other)
	k.wait(r2, "idle")
	var files int
	k.db.Server.QueryRow(ctx, `SELECT files, coalesce(note, '') FROM work_folders WHERE conversation = $1`, other).Scan(&files, &note)
	if files != 0 || !strings.Contains(note, "no key file") {
		t.Fatalf("with no key: %d files, note %q", files, note)
	}
}
