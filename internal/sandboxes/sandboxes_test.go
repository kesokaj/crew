package sandboxes_test

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/kesokaj/crew/internal/db/dbtest"
	"github.com/kesokaj/crew/internal/githubapp"
	"github.com/kesokaj/crew/internal/orchestrator"
	"github.com/kesokaj/crew/internal/sandboxes"
)

// fakeRuntime gives sandbox n the door 10.9.n.254 and the address 10.9.n.2.
type fakeRuntime struct {
	mu      sync.Mutex
	n       int
	doors   map[string][2]string // id → door, address
	running map[string]bool
	failOn  string
	removed []string
	adopted map[string][2]string
	listErr error
	specs   []orchestrator.SandboxSpec
}

func newFake() *fakeRuntime {
	return &fakeRuntime{doors: map[string][2]string{}, running: map[string]bool{}}
}

func (f *fakeRuntime) StartSandbox(_ context.Context, s orchestrator.SandboxSpec) (orchestrator.Sandbox, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.failOn == s.Kind {
		return orchestrator.Sandbox{}, errors.New("did not start")
	}
	f.n++
	f.specs = append(f.specs, s)
	door, ip := "10.9."+string(rune('0'+f.n))+".254", "10.9."+string(rune('0'+f.n))+".2"
	f.doors[s.ID] = [2]string{door, ip}
	f.running[s.ID] = true
	return orchestrator.Sandbox{ID: s.ID, BrokerIP: door, IP: ip}, nil
}

func (f *fakeRuntime) CutSandbox(_ context.Context, id string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	delete(f.doors, id)
	return nil
}

func (f *fakeRuntime) RemoveSandbox(ctx context.Context, id string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return err // as Docker's delete does
	}
	delete(f.doors, id)
	delete(f.running, id)
	f.removed = append(f.removed, id)
	return nil
}

// AdoptSandbox gives a sandbox the door of its network, as Docker reports it: adopted holds what a
// test says Docker has.
func (f *fakeRuntime) AdoptSandbox(_ context.Context, id string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if d, ok := f.adopted[id]; ok {
		for other, od := range f.doors {
			if od[0] == d[0] {
				delete(f.doors, other)
			}
		}
		f.doors[id] = d
	}
	return nil
}

func (f *fakeRuntime) SandboxAt(local, remote string) (string, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for id, d := range f.doors {
		if strings.HasPrefix(local, d[0]+":") && strings.HasPrefix(remote, d[1]+":") {
			return id, true
		}
	}
	return "", false
}

func (f *fakeRuntime) IsSandboxDoor(local string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, d := range f.doors {
		if strings.HasPrefix(local, d[0]+":") {
			return true
		}
	}
	return false
}

func (f *fakeRuntime) Sandboxes(context.Context) ([]orchestrator.SandboxInfo, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.listErr != nil {
		return nil, f.listErr
	}
	var out []orchestrator.SandboxInfo
	for id := range f.running {
		out = append(out, orchestrator.SandboxInfo{ID: id, Running: true})
	}
	return out, nil
}

func manager(t *testing.T) (*sandboxes.Manager, *fakeRuntime) {
	d := dbtest.New(t, false)
	f := newFake()
	return &sandboxes.Manager{Runtime: f, Pool: d.Server, Dir: t.TempDir(), Image: "crew-runtime:1", Log: slog.New(slog.DiscardHandler)}, f
}

func token(t *testing.T, m *sandboxes.Manager, id string) string {
	b, err := os.ReadFile(filepath.Join(m.Home(id), ".crew", "token"))
	if err != nil {
		t.Fatal(err)
	}
	return strings.TrimSpace(string(b))
}

func TestTheBrokerKnowsASandboxByItsDoorItsAddressAndItsToken(t *testing.T) {
	ctx := context.Background()
	m, f := manager(t)
	a, _, err := m.Start(ctx, sandboxes.Spec{Kind: sandboxes.KindRun, CLI: "claude", Repos: []string{"kesokaj/fixture"}})
	if err != nil {
		t.Fatal(err)
	}
	b, _, err := m.Start(ctx, sandboxes.Spec{Kind: sandboxes.KindLoginCheck, CLI: "agy"})
	if err != nil {
		t.Fatal(err)
	}
	ta, tb := token(t, m, a.ID), token(t, m, b.ID)
	fi, _ := os.Stat(filepath.Join(m.Home(a.ID), ".crew", "token"))
	if fi.Mode().Perm() != 0o600 {
		t.Fatalf("the token file is %v", fi.Mode().Perm())
	}
	var stored int
	m.Pool.QueryRow(ctx, `SELECT count(*) FROM sandbox_tokens WHERE sha256 = $1 OR sha256 = $2`, ta, tb).Scan(&stored)
	if stored != 0 {
		t.Fatal("a token is stored as itself, not as its hash")
	}

	da, db := f.doors[a.ID], f.doors[b.ID]
	got, err := m.Authorize(ctx, da[0]+":8091", da[1]+":5000", ta)
	if err != nil || got.ID != a.ID || got.CLI != "claude" || !slices.Equal(got.Repos, []string{"kesokaj/fixture"}) {
		t.Fatalf("Authorize = %+v, %v", got, err)
	}
	cases := map[string][3]string{
		"no token":                        {da[0] + ":8091", da[1] + ":1", ""},
		"a made-up token":                 {da[0] + ":8091", da[1] + ":1", strings.Repeat("0", 64)},
		"another sandbox's token":         {da[0] + ":8091", da[1] + ":1", tb},
		"its token from the other's door": {db[0] + ":8091", db[1] + ":1", ta},
		"its token from a stranger":       {da[0] + ":8091", "10.9.1.3:1", ta},
		"its token at the main door":      {"172.18.0.5:8080", da[1] + ":1", ta},
	}
	for name, c := range cases {
		if _, err := m.Authorize(ctx, c[0], c[1], c[2]); !errors.Is(err, sandboxes.ErrRefused) {
			t.Errorf("%s: %v", name, err)
		}
	}

	// Removed, the sandbox's token and home go with it.
	if err := m.Remove(ctx, a.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(m.Dir, a.ID)); !os.IsNotExist(err) {
		t.Fatalf("the home is still there: %v", err)
	}
	f.doors[a.ID] = da // even if the door were somehow still open
	if _, err := m.Authorize(ctx, da[0]+":8091", da[1]+":1", ta); !errors.Is(err, sandboxes.ErrRefused) {
		t.Fatalf("a removed sandbox's token still works: %v", err)
	}
}

func TestAnExpiredTokenIsRefused(t *testing.T) {
	ctx := context.Background()
	m, f := manager(t)
	a, _, err := m.Start(ctx, sandboxes.Spec{Kind: sandboxes.KindCheck})
	if err != nil {
		t.Fatal(err)
	}
	m.Pool.Exec(ctx, `UPDATE sandbox_tokens SET ends_at = now() - interval '1 second'`)
	d := f.doors[a.ID]
	if _, err := m.Authorize(ctx, d[0]+":8091", d[1]+":1", token(t, m, a.ID)); !errors.Is(err, sandboxes.ErrRefused) {
		t.Fatalf("an expired token: %v", err)
	}
}

func TestASandboxThatDoesNotStartLeavesNothing(t *testing.T) {
	ctx := context.Background()
	m, f := manager(t)
	f.failOn = sandboxes.KindSetup
	if _, _, err := m.Start(ctx, sandboxes.Spec{Kind: sandboxes.KindSetup}); err == nil {
		t.Fatal("no error")
	}
	live, _ := m.Live(ctx)
	entries, _ := os.ReadDir(m.Dir)
	var tokens int
	m.Pool.QueryRow(ctx, `SELECT count(*) FROM sandbox_tokens`).Scan(&tokens)
	if len(live) != 0 || len(entries) != 0 || tokens != 0 {
		t.Fatalf("left: %d rows, %d homes, %d tokens", len(live), len(entries), tokens)
	}
}

func TestARemovalOutlivesTheRequestThatAskedForIt(t *testing.T) {
	m, f := manager(t)
	row, _, err := m.Start(context.Background(), sandboxes.Spec{Kind: sandboxes.KindSetup})
	if err != nil {
		t.Fatal(err)
	}
	gone, cancel := context.WithCancel(context.Background())
	cancel()
	if err := m.Remove(gone, row.ID); err != nil {
		t.Fatalf("a request that went away cut the removal short: %v", err)
	}
	live, _ := m.Live(context.Background())
	if len(live) != 0 || f.running[row.ID] {
		t.Fatalf("left running: rows %d, in Docker %v", len(live), f.running[row.ID])
	}
}

func TestReconcileRemovesTheShortLivedWhenDockersListFails(t *testing.T) {
	ctx := context.Background()
	m, f := manager(t)
	setup, _, _ := m.Start(ctx, sandboxes.Spec{Kind: sandboxes.KindSetup})
	run, _, _ := m.Start(ctx, sandboxes.Spec{Kind: sandboxes.KindRun, CLI: "agy"})
	f.listErr = errors.New("docker is busy")
	m.Reconcile(ctx)
	live, _ := m.Live(ctx)
	if len(live) != 1 || live[0].ID != run.ID || !slices.Contains(f.removed, setup.ID) {
		t.Fatalf("live %+v, removed %v", live, f.removed)
	}
}

// What ends with a sandbox, such as its GitHub token, ends after the broker refuses the sandbox, so
// nothing new is made for it in between; and it ends even when Docker could not remove it yet.
func TestOnRemoveRunsAfterTheBrokerRefusesTheSandbox(t *testing.T) {
	ctx := context.Background()
	m, f := manager(t)
	row, sb, _ := m.Start(ctx, sandboxes.Spec{Kind: sandboxes.KindRun, CLI: "agy"})
	tok := token(t, m, row.ID)
	var refusedAtRevoke bool
	m.OnRemove = func(ctx context.Context, id string) {
		_, err := m.Authorize(ctx, sb.BrokerIP+":8091", sb.IP+":4000", tok)
		refusedAtRevoke = err != nil
	}
	f.failOn = ""
	if err := m.Remove(ctx, row.ID); err != nil {
		t.Fatal(err)
	}
	if !refusedAtRevoke {
		t.Fatal("the sandbox could still ask the broker when its token was revoked")
	}
}

func TestReconcileRemovesTheShortLivedAndTheOrphans(t *testing.T) {
	ctx := context.Background()
	m, f := manager(t)
	setup, _, _ := m.Start(ctx, sandboxes.Spec{Kind: sandboxes.KindSetup})
	check, _, _ := m.Start(ctx, sandboxes.Spec{Kind: sandboxes.KindCheck})
	// A build's sandbox cannot go on without the server either: its build is failed (review of M16 P3).
	build, _, _ := m.Start(ctx, sandboxes.Spec{Kind: sandboxes.KindBuild})
	// A project's build of its image to host is short-lived too, in its checks' mode, Sources among them;
	// a hosted project's sandbox, which hosting records itself, lives on and is adopted (since 0.72).
	hostBuild, _, err := m.Start(ctx, sandboxes.Spec{Kind: sandboxes.KindHostBuild, Mode: "sources"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := m.Pool.Exec(ctx, `INSERT INTO sandboxes (id, kind, container, network, image, size, mode)
		VALUES ('host-0123456789ab-1', 'hosted', 'crew-host-0123456789ab-1', 'crew-host-0123456789ab-1', 'sha256:x', 'small', 'hosted_internet')`); err != nil {
		t.Fatal(err)
	}
	f.running["host-0123456789ab-1"] = true
	run, _, _ := m.Start(ctx, sandboxes.Spec{Kind: sandboxes.KindRun, CLI: "agy"})
	gone, _, _ := m.Start(ctx, sandboxes.Spec{Kind: sandboxes.KindRun, CLI: "agy"})
	delete(f.running, gone.ID)          // its container is gone
	f.running["orphan-0000000a"] = true // Docker has it, the database does not

	m.Reconcile(ctx)
	live, _ := m.Live(ctx)
	if len(live) != 2 || live[0].ID != "host-0123456789ab-1" || live[1].ID != run.ID || slices.Contains(f.removed, "host-0123456789ab-1") {
		t.Fatalf("live after reconcile: %+v", live)
	}
	for _, id := range []string{setup.ID, check.ID, build.ID, hostBuild.ID, gone.ID, "orphan-0000000a"} {
		if !slices.Contains(f.removed, id) {
			t.Errorf("%s was not removed", id)
		}
	}
}

// The table of 12.6: what each sandbox gets from the broker.
func TestGrantIsTheTableOfTheDesign(t *testing.T) {
	signedIn := []string{"agy"}
	cases := []struct {
		kind, cli string
		gcloud    bool
		want      sandboxes.Grant
	}{
		// No run and no conversation gets gcloud, whatever its row says (AGENTS.md safety rule 8, 0.79);
		// the terminal still takes it as its row says.
		{sandboxes.KindRun, "agy", true, sandboxes.Grant{CLIs: []string{"agy"}, GitIdentity: true, GitHubToken: true}},
		{sandboxes.KindRun, "agy", false, sandboxes.Grant{CLIs: []string{"agy"}, GitIdentity: true, GitHubToken: true}},
		{sandboxes.KindTerminal, "", true, sandboxes.Grant{CLIs: signedIn, GitIdentity: true, GitHubToken: true, Gcloud: true}},
		{sandboxes.KindImprover, "agy", true, sandboxes.Grant{CLIs: []string{"agy"}}},
		{sandboxes.KindJudge, "agy", true, sandboxes.Grant{CLIs: []string{"agy"}}},
		{sandboxes.KindLoginCheck, "agy", true, sandboxes.Grant{CLIs: []string{"agy"}}},
		{sandboxes.KindChat, "agy", true, sandboxes.Grant{CLIs: []string{"agy"}, GitHubToken: true}},
		// A conversation gets what a run of its project gets (11.8, 13.3).
		{sandboxes.KindConversation, "agy", true, sandboxes.Grant{CLIs: []string{"agy"}, GitIdentity: true, GitHubToken: true}},
		{sandboxes.KindConversation, "agy", false, sandboxes.Grant{CLIs: []string{"agy"}, GitIdentity: true, GitHubToken: true}},
		{sandboxes.KindCheck, "", true, sandboxes.Grant{GitHubToken: true}},
		{sandboxes.KindSetup, "", true, sandboxes.Grant{}},
		{sandboxes.KindHosted, "", true, sandboxes.Grant{}},
		// The build of a release gets nothing: no login, no token, no git identity (13.3, since 0.51).
		{sandboxes.KindBuild, "agy", true, sandboxes.Grant{}},
		// A CLI Crew does not run gets no login, claude included since 0.42.
		{sandboxes.KindRun, "claude", true, sandboxes.Grant{GitIdentity: true, GitHubToken: true}},
		{sandboxes.KindRun, "codex", true, sandboxes.Grant{GitIdentity: true, GitHubToken: true}},
	}
	for _, c := range cases {
		got := sandboxes.GrantFor(c.kind, c.cli, signedIn, c.gcloud)
		if !slices.Equal(got.CLIs, c.want.CLIs) || got.GitIdentity != c.want.GitIdentity ||
			got.GitHubToken != c.want.GitHubToken || got.Gcloud != c.want.Gcloud {
			t.Errorf("%s/%s: %+v, want %+v", c.kind, c.cli, got, c.want)
		}
	}
	// A project terminal gets only the CLIs that are signed in, and only those Crew runs.
	if got := sandboxes.GrantFor(sandboxes.KindTerminal, "", nil, false); len(got.CLIs) != 0 {
		t.Fatalf("terminal with no CLI signed in: %v", got.CLIs)
	}
	if got := sandboxes.GrantFor(sandboxes.KindTerminal, "", []string{"agy", "claude"}, false); !slices.Equal(got.CLIs, []string{"agy"}) {
		t.Fatalf("terminal with a login left from claude: %v", got.CLIs)
	}
}

// Docker gives a subnet again once its network is gone. When another process removed the sandbox
// that had it and started one on it, the broker learns the new sandbox, and the old one's token
// still does not work.
func TestASubnetGivenAgainIsLearned(t *testing.T) {
	ctx := context.Background()
	m, f := manager(t)
	old, _, _ := m.Start(ctx, sandboxes.Spec{Kind: sandboxes.KindLoginCheck, CLI: "claude"})
	door := f.doors[old.ID]
	oldTok := token(t, m, old.ID)
	// Another process removes it and starts a new one on the same subnet; this one never heard.
	m.Pool.Exec(ctx, `UPDATE sandboxes SET removed_at = now() WHERE id = $1`, old.ID)
	other := &sandboxes.Manager{Runtime: newFake(), Pool: m.Pool, Dir: m.Dir, Image: "crew-runtime:1", Log: m.Log}
	fresh, _, err := other.Start(ctx, sandboxes.Spec{Kind: sandboxes.KindLoginCheck, CLI: "claude"})
	if err != nil {
		t.Fatal(err)
	}
	f.adopted = map[string][2]string{fresh.ID: door}
	got, err := m.Authorize(ctx, door[0]+":8091", door[1]+":1", token(t, m, fresh.ID))
	if err != nil || got.ID != fresh.ID {
		t.Fatalf("the new sandbox: %+v, %v", got, err)
	}
	if _, err := m.Authorize(ctx, door[0]+":8091", door[1]+":1", oldTok); err == nil {
		t.Fatal("the removed sandbox's token works")
	}
}

// Prepare runs with the sandbox's row, home, and token in place, before its microVM starts; an error
// removes the sandbox again and starts no microVM (12.4).
func TestPrepareRunsBeforeTheMicroVMStarts(t *testing.T) {
	ctx := context.Background()
	m, f := manager(t)
	var sawID, sawHash string
	row, _, err := m.Start(ctx, sandboxes.Spec{Kind: sandboxes.KindRun, CLI: "claude", Prepare: func(id, home, hash string) error {
		sawID, sawHash = id, hash
		if _, err := m.Get(ctx, id); err != nil {
			t.Errorf("the row is not there yet: %v", err)
		}
		if f.running[id] {
			t.Error("the microVM started before Prepare")
		}
		tok, err := os.ReadFile(filepath.Join(home, ".crew", "token"))
		if err != nil || sandboxes.Hash(strings.TrimSpace(string(tok))) != hash {
			t.Errorf("the token and its hash: %v", err)
		}
		return os.WriteFile(filepath.Join(home, ".crew", "run.json"), []byte("{}"), 0o644)
	}})
	if err != nil || sawID != row.ID || sawHash == "" {
		t.Fatalf("%v %q %q", err, sawID, sawHash)
	}
	if _, err := os.Stat(filepath.Join(m.Home(row.ID), ".crew", "run.json")); err != nil {
		t.Fatal(err)
	}
	_, _, err = m.Start(ctx, sandboxes.Spec{Kind: sandboxes.KindRun, Prepare: func(id, home, hash string) error {
		sawID = id
		return errors.New("no")
	}})
	if err == nil || f.running[sawID] {
		t.Fatalf("%v", err)
	}
	if _, err := m.Get(ctx, sawID); err == nil {
		t.Fatal("a sandbox that was not prepared is still live")
	}
}

// The kinds that only call their model are Model only whatever is asked, Setup has the internet, and
// the others have what their project or ticket asks for (13.6).
func TestModeFor(t *testing.T) {
	for _, tc := range []struct{ kind, asked, want string }{
		{sandboxes.KindJudge, "", "model_only"}, {sandboxes.KindJudge, "lan", "model_only"}, {sandboxes.KindJudge, "internet", "model_only"},
		{sandboxes.KindLoginCheck, "lan", "model_only"}, {sandboxes.KindImprover, "", "model_only"},
		{sandboxes.KindSetup, "model_only", "internet"},
		{sandboxes.KindRun, "", "internet"}, {sandboxes.KindRun, "model_only", "model_only"}, {sandboxes.KindRun, "lan", "lan"},
		{sandboxes.KindCheck, "model_only", "model_only"}, {sandboxes.KindTerminal, "lan", "lan"}, {sandboxes.KindChat, "", "internet"},
		{sandboxes.KindConversation, "lan", "lan"}, {sandboxes.KindChat, "model_only", "model_only"},
		// The build of a release is Sources, whatever is asked (13.6, since 0.51).
		{sandboxes.KindBuild, "", "sources"}, {sandboxes.KindBuild, "lan", "sources"}, {sandboxes.KindBuild, "internet", "sources"},
	} {
		if got := sandboxes.ModeFor(tc.kind, tc.asked); got != tc.want {
			t.Errorf("ModeFor(%s, %q) = %q, want %q", tc.kind, tc.asked, got, tc.want)
		}
	}
}

// GitHub gives no token for a sandbox (21): the signal names the sandbox, its project, and its run.
func TestNoGitHubTokenIsASignal(t *testing.T) {
	ctx := context.Background()
	m, _ := manager(t)
	const project, run = "f1c50000-0000-4000-8000-000000000001", "00000000-0000-4000-8000-0000000000b1"
	if _, err := m.Pool.Exec(ctx, `UPDATE projects SET remote = 'git@github.com:kesokaj/fixture.git' WHERE id = $1`, project); err != nil {
		t.Fatal(err)
	}
	row, _, err := m.Start(ctx, sandboxes.Spec{Kind: sandboxes.KindRun, CLI: "agy", Project: project, Repos: []string{"kesokaj/fixture"}})
	if err != nil {
		t.Fatal(err)
	}
	for _, q := range []string{
		`INSERT INTO subtasks (id, scope, project, number, kind, role) VALUES (9001, 'project', '` + project + `', 900, 'run', 'builder')`,
		`INSERT INTO runs (id, subtask, number, cli, crew_version, image, sandbox) VALUES ('` + run + `', 9001, 1, 'agy', 't', 't', '` + row.ID + `')`,
	} {
		if _, err := m.Pool.Exec(ctx, q); err != nil {
			t.Fatal(err)
		}
	}
	m.NoGitHubToken(ctx, row, errors.New("github: 503 Service Unavailable"))
	// A sandbox that asks again and again gets one signal.
	m.NoGitHubToken(ctx, row, errors.New("github: 503 Service Unavailable"))
	other, _, err := m.Start(ctx, sandboxes.Spec{Kind: sandboxes.KindRun, CLI: "agy", Repos: []string{"kesokaj/fixture"}})
	if err != nil {
		t.Fatal(err)
	}
	// A signal that could not be written is written at the next request.
	m.NoGitHubToken(ctx, sandboxes.Row{ID: other.ID, Project: "not-a-project"}, errors.New("github: 503"))
	m.NoGitHubToken(ctx, other, errors.New("github: 503"))
	var n int
	m.Pool.QueryRow(ctx, `SELECT count(*) FROM signals WHERE kind = 'no_github_token'`).Scan(&n)
	if n != 2 {
		t.Fatalf("%d signals", n)
	}
	var kind, gotProject, gotRun, sandbox, why string
	if err := m.Pool.QueryRow(ctx, `SELECT kind, project::text, run::text, detail->>'sandbox', detail->>'error' FROM signals WHERE detail->>'sandbox' = $1`, row.ID).
		Scan(&kind, &gotProject, &gotRun, &sandbox, &why); err != nil {
		t.Fatal(err)
	}
	if kind != "no_github_token" || gotProject != project || gotRun != run || sandbox != row.ID || why != githubapp.NoTokenOther {
		t.Fatalf("%s %s %s %s %s", kind, gotProject, gotRun, sandbox, why)
	}
}

// The signal keeps a fixed sentence for its cause, never the error's text: the improver reads it
// through list_signals and get_metrics, and the error of the database, of the network, or GitHub's
// own message can name the server's addresses (13.9).
func TestTheNoGitHubTokenSignalHoldsNoCrewError(t *testing.T) {
	ctx := context.Background()
	m, _ := manager(t)
	const addr = "172.16.9.3:5432"
	for _, tc := range []struct {
		cause error
		want  string
	}{
		{fmt.Errorf("failed to connect to `user=crew database=crew`: %s: connection refused", addr), githubapp.NoTokenOther},
		{fmt.Errorf("GitHub: Post \"https://api.github.com/app/installations/7/access_tokens\": dial tcp %s: i/o timeout", addr), githubapp.NoTokenOther},
		{errors.New("GitHub POST /app/installations/7/access_tokens: 422 Unprocessable Entity " + addr), githubapp.NoTokenOther},
		{fmt.Errorf("the token: %w", githubapp.ErrNotSetUp), githubapp.NoTokenNotSetUp},
		{fmt.Errorf("%w on kesokaj at %s", githubapp.ErrNotInstalled, addr), githubapp.NoTokenNotInstalled},
	} {
		row, _, err := m.Start(ctx, sandboxes.Spec{Kind: sandboxes.KindRun, CLI: "agy", Repos: []string{"kesokaj/fixture"}})
		if err != nil {
			t.Fatal(err)
		}
		m.NoGitHubToken(ctx, row, tc.cause)
		var why string
		if err := m.Pool.QueryRow(ctx, `SELECT detail->>'error' FROM signals WHERE kind = 'no_github_token' AND detail->>'sandbox' = $1`, row.ID).Scan(&why); err != nil {
			t.Fatal(err)
		}
		if why != tc.want {
			t.Errorf("%v: the signal holds %q, not %q", tc.cause, why, tc.want)
		}
	}
	var n int
	m.Pool.QueryRow(ctx, `SELECT count(*) FROM signals WHERE detail::text LIKE '%' || $1 || '%'`, addr).Scan(&n)
	if n != 0 {
		t.Fatalf("%d signals hold the address", n)
	}
}

// The build of a release starts in Sources, with its own Docker daemon, whatever its spec asks, and
// its row says so (13.6, since 0.51).
func TestABuildSandboxIsSources(t *testing.T) {
	ctx := context.Background()
	m, f := manager(t)
	row, _, err := m.Start(ctx, sandboxes.Spec{Kind: sandboxes.KindBuild, Mode: "lan", Repos: nil})
	if err != nil {
		t.Fatal(err)
	}
	if row.Mode != "sources" {
		t.Fatalf("row mode %q", row.Mode)
	}
	var stored string
	if err := m.Pool.QueryRow(ctx, `SELECT mode FROM sandboxes WHERE id = $1`, row.ID).Scan(&stored); err != nil || stored != "sources" {
		t.Fatalf("stored mode %q (%v)", stored, err)
	}
	got := f.specs[len(f.specs)-1]
	if got.Mode != "sources" || got.Minimal || len(got.Env) != 0 {
		t.Fatalf("the microVM's spec: %+v", got)
	}
	// A project or a ticket cannot ask for Sources for a sandbox that runs an agent; a check, which runs
	// none, may have it (since 0.53: Crew's own project checks so), with its own Docker daemon.
	for _, kind := range []string{sandboxes.KindRun, sandboxes.KindChat, sandboxes.KindTerminal} {
		if _, _, err := m.Start(ctx, sandboxes.Spec{Kind: kind, Mode: "sources"}); err == nil {
			t.Fatalf("a %s started in Sources", kind)
		}
	}
	check, _, err := m.Start(ctx, sandboxes.Spec{Kind: sandboxes.KindCheck, Mode: "sources"})
	if err != nil || check.Mode != "sources" {
		t.Fatalf("a check in Sources: %+v %v", check, err)
	}
	if got := f.specs[len(f.specs)-1]; got.Mode != "sources" || got.Minimal {
		t.Fatalf("the check's microVM: %+v", got)
	}
}

// A run of the gate's project starts from the runtime image the project names while a gate's live
// steps run, the candidate's by its id (16.2 step 5); its checks, a run of any other project, and one
// of none, start from the running release's. Only a hidden project names one.
func TestTheGatesProjectRunsInTheImageItNames(t *testing.T) {
	m, f := manager(t)
	ctx := context.Background()
	img := "sha256:" + strings.Repeat("a", 64)
	for _, q := range []string{
		`INSERT INTO projects (id, name, key, state, repo_source, hidden) VALUES ('00000000-0000-4000-8000-0000000000c1', 'crew-gate', 'GATE', 'running', 'local', true)`,
		`INSERT INTO projects (id, name, key, state, repo_source) VALUES ('00000000-0000-4000-8000-0000000000c2', 'shop', 'SF', 'running', 'local')`,
		`UPDATE projects SET runtime_image = '` + img + `' WHERE name = 'crew-gate'`,
	} {
		if _, err := m.Pool.Exec(ctx, q); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := m.Pool.Exec(ctx, `UPDATE projects SET runtime_image = $1 WHERE name = 'shop'`, img); err == nil {
		t.Fatal("a project that is not hidden named a runtime image")
	}
	if _, err := m.Pool.Exec(ctx, `UPDATE projects SET runtime_image = 'crew-runtime:latest' WHERE name = 'crew-gate'`); err == nil {
		t.Fatal("a runtime image that is no id was taken")
	}
	for _, project := range []string{"00000000-0000-4000-8000-0000000000c1", "00000000-0000-4000-8000-0000000000c2", ""} {
		if _, _, err := m.Start(ctx, sandboxes.Spec{Kind: sandboxes.KindRun, Project: project}); err != nil {
			t.Fatal(err)
		}
	}
	// The gate's checks, which decide whether its ticket passed, run in the running release's image.
	if _, _, err := m.Start(ctx, sandboxes.Spec{Kind: sandboxes.KindCheck, Project: "00000000-0000-4000-8000-0000000000c1"}); err != nil {
		t.Fatal(err)
	}
	if len(f.specs) != 4 || f.specs[0].Image != img || f.specs[1].Image != "" || f.specs[2].Image != "" || f.specs[3].Image != "" {
		t.Fatalf("images: %q %q %q %q", f.specs[0].Image, f.specs[1].Image, f.specs[2].Image, f.specs[3].Image)
	}
	var recorded string
	m.Pool.QueryRow(ctx, `SELECT image FROM sandboxes WHERE project = '00000000-0000-4000-8000-0000000000c1'`).Scan(&recorded)
	if recorded != img {
		t.Fatalf("the sandbox's row names %q", recorded)
	}
}

// An agent run of a release's eval suite starts from the candidate's runtime image the run names, by
// its id, and its row records it (16.2 step 5, since 0.52). Only a run's sandbox takes one: a check
// decides with the running release's image, and a judge and the improver never take one. An image that
// is no id is refused, and so is a run that names one in a project that names its own. The image is a
// run's only while a release suite of its project from that image is open and its gate runs its live
// part: a run that continues the subtask after that is refused it (security review of M16 P4d).
func TestAGateEvalRunStartsInTheImageItNames(t *testing.T) {
	m, f := manager(t)
	ctx := context.Background()
	img := "sha256:" + strings.Repeat("b", 64)
	for _, q := range []string{
		`INSERT INTO projects (id, name, key, state, repo_source) VALUES ('00000000-0000-4000-8000-0000000000c2', 'shop', 'SF', 'running', 'local')`,
		`INSERT INTO projects (id, name, key, state, repo_source, hidden, runtime_image) VALUES ('00000000-0000-4000-8000-0000000000c1', 'crew-gate', 'GATE', 'running', 'local', true, 'sha256:` + strings.Repeat("a", 64) + `')`,
		`INSERT INTO gates (version, state, suites) VALUES ('0.3.0', 'live', '[{"project": "00000000-0000-4000-8000-0000000000c2", "suite": 2}]')`,
		`INSERT INTO eval_suites (id, project, purpose, repeats, budget_usd, state, asked_by) VALUES (1, '00000000-0000-4000-8000-0000000000c2', 'on_demand', 1, 10, 'done', 'operator')`,
		`INSERT INTO eval_suites (id, project, purpose, repeats, budget_usd, state, asked_by, gate, image, compare_to, layer)
			VALUES (2, '00000000-0000-4000-8000-0000000000c2', 'release', 1, 10, 'running', 'gate', '0.3.0', '` + img + `', 1, '{}')`,
	} {
		if _, err := m.Pool.Exec(ctx, q); err != nil {
			t.Fatal(err)
		}
	}
	const shop = "00000000-0000-4000-8000-0000000000c2"
	if _, _, err := m.Start(ctx, sandboxes.Spec{Kind: sandboxes.KindRun, Project: shop, Image: img}); err != nil {
		t.Fatal(err)
	}
	if len(f.specs) != 1 || f.specs[0].Image != img {
		t.Fatalf("the run's sandbox started from %+v", f.specs)
	}
	var recorded string
	m.Pool.QueryRow(ctx, `SELECT image FROM sandboxes WHERE project = $1`, shop).Scan(&recorded)
	if recorded != img {
		t.Fatalf("the sandbox's row names %q", recorded)
	}
	for _, s := range []sandboxes.Spec{
		{Kind: sandboxes.KindCheck, Project: shop, Image: img},
		{Kind: sandboxes.KindJudge, Project: shop, Image: img},
		{Kind: sandboxes.KindImprover, Image: img},
		{Kind: sandboxes.KindRun, Project: shop, Image: "crew-runtime:latest"},
		{Kind: sandboxes.KindRun, Project: "00000000-0000-4000-8000-0000000000c1", Image: img},
	} {
		if _, _, err := m.Start(ctx, s); err == nil {
			t.Fatalf("a %s sandbox of %q started from the image %q", s.Kind, s.Project, s.Image)
		}
	}
	if len(f.specs) != 1 {
		t.Fatalf("%d sandboxes started", len(f.specs))
	}
	// Another image than its suite's, its suite ended, its gate's suites step ended, its gate naming its
	// project with another suite, its suite another project's, and its gate no longer live: refused.
	for _, q := range []string{
		`UPDATE eval_suites SET image = 'sha256:` + strings.Repeat("c", 64) + `' WHERE id = 2`,
		`UPDATE eval_suites SET image = '` + img + `', state = 'stopped' WHERE id = 2`,
		`UPDATE eval_suites SET state = 'queued' WHERE id = 2; UPDATE gates SET suites_step = '{}'`,
		`UPDATE gates SET suites_step = NULL, suites = '[{"project": "00000000-0000-4000-8000-0000000000c2", "suite": 9}]'`,
		`UPDATE gates SET suites = '[{"project": "00000000-0000-4000-8000-0000000000c1", "suite": 2}]'; UPDATE eval_suites SET project = '00000000-0000-4000-8000-0000000000c1' WHERE id = 2`,
		`UPDATE gates SET state = 'done', suites = '[{"project": "00000000-0000-4000-8000-0000000000c2", "suite": 2}]'; UPDATE eval_suites SET project = '00000000-0000-4000-8000-0000000000c2' WHERE id = 2`,
	} {
		if _, err := m.Pool.Exec(ctx, q); err != nil {
			t.Fatal(err)
		}
		if _, _, err := m.Start(ctx, sandboxes.Spec{Kind: sandboxes.KindRun, Project: shop, Image: img}); err == nil {
			t.Fatalf("a run started from the image after %s", q)
		}
	}
	if len(f.specs) != 1 {
		t.Fatalf("%d sandboxes started", len(f.specs))
	}
}

// Only a terminal's sandbox may say it holds gcloud (AGENTS.md safety rule 8, 0.79): a row of another
// kind that said so would give gcloud again after a roll back to an older server (security review of
// 8543b5a).
func TestOnlyATerminalIsStartedWithGcloud(t *testing.T) {
	m, f := manager(t)
	ctx := context.Background()
	for _, kind := range []string{sandboxes.KindRun, sandboxes.KindConversation, sandboxes.KindJudge, sandboxes.KindCheck} {
		if _, _, err := m.Start(ctx, sandboxes.Spec{Kind: kind, Gcloud: true}); err == nil {
			t.Errorf("a %s sandbox started with gcloud", kind)
		}
	}
	if len(f.specs) != 0 {
		t.Fatalf("%d sandboxes started", len(f.specs))
	}
	row, _, err := m.Start(ctx, sandboxes.Spec{Kind: sandboxes.KindTerminal, Gcloud: true})
	if err != nil || !row.Gcloud {
		t.Fatalf("a terminal with gcloud: %+v %v", row, err)
	}
}
