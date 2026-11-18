// Package projterm is the project terminal and the jump into a running run's microVM (design 13.11):
// a terminal in the browser for looking into things, with an edit mode for changing the code by
// hand. The project terminal is a sandbox of its own, one per project, with the project's repository
// cloned from Crew's git endpoint with every branch and main checked out, and the login of every CLI
// that is signed in. In diagnostics mode the endpoint takes no push from it; in edit mode it takes
// main (only forward), the operator's own branches under you/, and a ticket's branch while no agent
// runs on that ticket. What was typed and printed is kept 180 days, and opening, switching, and
// closing are in the project's history.
package projterm

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/kesokaj/crew/internal/cli"
	"github.com/kesokaj/crew/internal/db"
	"github.com/kesokaj/crew/internal/gitendpoint"
	"github.com/kesokaj/crew/internal/orchestrator"
	"github.com/kesokaj/crew/internal/sandboxes"
	"github.com/kesokaj/crew/internal/settings"
)

// The modes of the project terminal (13.11).
const (
	ModeDiagnostics = "diagnostics"
	ModeEdit        = "edit"
)

// Defaults: a terminal is removed after Idle without input, and its log is kept for Keep.
const (
	Idle = 30 * time.Minute
	Keep = 180 * 24 * time.Hour
)

// OwnBranches is the prefix of the branches that are the operator's own: edit mode may push them.
const OwnBranches = "refs/heads/you/"

// Workspace is where the clone is in the terminal's microVM, as in a run's (12.4).
const Workspace = "/home/" + orchestrator.GuestUser + "/work"

// Sandboxes is the part of the sandbox manager the terminals use.
type Sandboxes interface {
	Start(ctx context.Context, spec sandboxes.Spec) (sandboxes.Row, orchestrator.Sandbox, error)
	Remove(ctx context.Context, id string) error
	Authorize(ctx context.Context, local, remote, token string) (sandboxes.Row, error)
}

// Stream is a terminal's process in a microVM: what it prints, what is typed, and its size.
type Stream interface {
	Read(p []byte) (int, error)
	Write(p []byte) (int, error)
	Resize(ctx context.Context, cols, rows uint) error
	Close()
}

// Runtime runs commands in a microVM.
type Runtime interface {
	Exec(ctx context.Context, container string, spec orchestrator.ExecSpec) (int, string, error)
	Interactive(ctx context.Context, container string, spec orchestrator.ExecSpec, cols, rows uint) (Stream, error)
}

// Git is what the terminals need of the repositories: holding a pushed main for the operator's
// confirmation, taking it as the new base, or giving the git endpoint Crew's main back; what a push
// changed; and a file of main, with what its links lead to.
type Git interface {
	HoldMain(ctx context.Context, name, to, key string) error
	DropHeld(ctx context.Context, name, key string) error
	AdvanceMain(ctx context.Context, name, from, to string) error
	GiveBackMain(ctx context.Context, name string) error
	ChangedFiles(ctx context.Context, name, from, to string) ([]string, error)
	IndexFile(ctx context.Context, name, path string) ([]byte, cli.Way, error)
	Linked(ctx context.Context, name string, patterns []string) ([]string, error)
}

// Service runs the project terminals.
type Service struct {
	Pool      *pgxpool.Pool
	Sandboxes Sandboxes
	Runtime   Runtime
	Git       Git
	// Comment tells the next agent on a ticket what the operator pushed to its branch, as a comment
	// on the ticket, which reaches that agent's run (11.6).
	Comment func(ctx context.Context, project, ticket, text string) error
	// Kick asks the board to look again: a push of main to send on.
	Kick func()
	// Seal encrypts what a terminal's log keeps with the key of the credentials, which is in no
	// database and no backup (AGENTS.md rule 10): a login printed on the screen is kept sealed.
	Seal func(data []byte) ([]byte, error)
	// MaxLog is the most of one attachment the log keeps; 0 is MaxLog.
	MaxLog int
	Log    *slog.Logger
	Idle   time.Duration // 0 is Idle

	mu sync.Mutex // one open or close of a project's terminal at a time
}

// Terminal is a project's open terminal.
type Terminal struct {
	Project  string    `json:"project"`
	Sandbox  string    `json:"sandbox"`
	Mode     string    `json:"mode"`
	Branch   string    `json:"branch"`
	OpenedAt time.Time `json:"openedAt"`
	InputAt  time.Time `json:"inputAt"`
	Logins   []string  `json:"logins"`
	Network  string    `json:"network"`
}

// Error is a refusal, with a reason the operator can read.
type Error struct{ Msg string }

func (e *Error) Error() string { return e.Msg }

func refuse(format string, a ...any) error { return &Error{Msg: fmt.Sprintf(format, a...)} }

// ErrNotOpen says a project has no open terminal.
var ErrNotOpen = errors.New("the project terminal is not open")

func (s *Service) idle() time.Duration {
	if s.Idle > 0 {
		return s.Idle
	}
	return Idle
}

// project is what a terminal needs of its project.
type project struct {
	id, name, state string
}

func (s *Service) project(ctx context.Context, q db.Querier, ref string) (project, error) {
	var p project
	// A hidden project is Crew's own, the gate's crew-gate, whose sandboxes may start from a candidate's
	// runtime image (16.2 step 5): it has no terminal.
	err := q.QueryRow(ctx, `SELECT id::text, name, state FROM projects WHERE (name = $1 OR id::text = $1) AND NOT hidden`, ref).Scan(&p.id, &p.name, &p.state)
	if errors.Is(err, pgx.ErrNoRows) {
		return p, refuse("no such project")
	}
	return p, err
}

// Get returns a project's open terminal.
func (s *Service) Get(ctx context.Context, ref string) (Terminal, error) {
	p, err := s.project(ctx, s.Pool, ref)
	if err != nil {
		return Terminal{}, err
	}
	var t Terminal
	err = s.Pool.QueryRow(ctx, `SELECT x.sandbox, x.mode, x.opened_at, x.input_at, coalesce(b.network, '') FROM project_terminals x
		JOIN sandboxes b ON b.id = x.sandbox WHERE x.project = $1 AND b.removed_at IS NULL`, p.id).Scan(&t.Sandbox, &t.Mode, &t.OpenedAt, &t.InputAt, &t.Network)
	if errors.Is(err, pgx.ErrNoRows) {
		return Terminal{}, ErrNotOpen
	}
	if err != nil {
		return Terminal{}, err
	}
	t.Project = p.name
	t.Logins = s.logins(ctx)
	t.Branch = s.branchOf(ctx, t.Sandbox)
	return t, nil
}

func (s *Service) logins(ctx context.Context) []string {
	rows, err := s.Pool.Query(ctx, `SELECT cli FROM cli_logins WHERE state = 'signed_in' ORDER BY cli`)
	if err != nil {
		return []string{}
	}
	out, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil || out == nil {
		return []string{}
	}
	return out
}

// branchOf reads which branch the terminal's clone has checked out.
func (s *Service) branchOf(ctx context.Context, sandbox string) string {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	code, out, err := s.Runtime.Exec(ctx, orchestrator.SandboxName(sandbox), orchestrator.ExecSpec{
		Cmd: []string{"git", "-C", Workspace, "branch", "--show-current"}, User: orchestrator.GuestUser,
	})
	if err != nil || code != 0 {
		return ""
	}
	return strings.TrimSpace(out)
}

// Open opens a project's terminal, or returns the one that is open. With ticket, the clone is
// switched to that ticket's branch, which only works while no agent runs on the ticket: to step in
// there, jump into the run's microVM.
func (s *Service) Open(ctx context.Context, ref, ticket, who string) (Terminal, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	p, err := s.project(ctx, s.Pool, ref)
	if err != nil {
		return Terminal{}, err
	}
	if p.state == "archived" {
		return Terminal{}, refuse("%s is archived", p.name)
	}
	branch := ""
	if ticket != "" {
		var running bool
		err := s.Pool.QueryRow(ctx, `SELECT coalesce(t.branch, ''), EXISTS (SELECT 1 FROM runs r JOIN subtasks st ON st.id = r.subtask
				WHERE st.ticket = t.id AND r.state <> 'ended')
			FROM tickets t JOIN projects pr ON pr.id = t.project WHERE t.project = $1 AND pr.key || '-' || t.number = $2`, p.id, ticket).Scan(&branch, &running)
		if errors.Is(err, pgx.ErrNoRows) {
			return Terminal{}, refuse("%s has no ticket %s", p.name, ticket)
		}
		if err != nil {
			return Terminal{}, err
		}
		if running {
			return Terminal{}, refuse("an agent runs on %s: jump into its microVM instead, or put the ticket on hold first", ticket)
		}
		if branch == "" {
			return Terminal{}, refuse("%s has no branch yet: Crew cuts it when the ticket enters In progress", ticket)
		}
	}
	t, err := s.Get(ctx, p.id)
	if errors.Is(err, ErrNotOpen) {
		t, err = s.start(ctx, p, who)
	}
	if err != nil {
		return Terminal{}, err
	}
	if branch != "" && t.Branch != branch {
		if err := s.checkout(ctx, t.Sandbox, branch); err != nil {
			return t, err
		}
		t.Branch = branch
	}
	return t, nil
}

// repo is the name of the project's repository at the git endpoint.
func (s *Service) repo(ctx context.Context, projectID string) (string, error) {
	var name string
	err := s.Pool.QueryRow(ctx, `SELECT name FROM git_repos WHERE project = $1 ORDER BY name LIMIT 1`, projectID).Scan(&name)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", refuse("the project has no repository at Crew's git endpoint")
	}
	return name, err
}

// start starts the terminal's microVM, pulls its credentials, and clones the repository with every
// branch, main checked out.
func (s *Service) start(ctx context.Context, p project, who string) (Terminal, error) {
	repo, err := s.repo(ctx, p.id)
	if err != nil {
		return Terminal{}, err
	}
	if !gitendpoint.ValidName(repo) {
		return Terminal{}, refuse("%q is not a repository's name", repo)
	}
	var remote *string
	if err := s.Pool.QueryRow(ctx, `SELECT remote FROM projects WHERE id = $1`, p.id).Scan(&remote); err != nil {
		return Terminal{}, err
	}
	mode, err := settings.NetworkMode(ctx, s.Pool, p.id, 0)
	if err != nil {
		return Terminal{}, err
	}
	row, _, err := s.Sandboxes.Start(ctx, sandboxes.Spec{Kind: sandboxes.KindTerminal, Project: p.id, Repos: githubRepos(remote), Mode: mode, Life: 24 * time.Hour})
	if err != nil {
		return Terminal{}, fmt.Errorf("the terminal's microVM did not start: %w", err)
	}
	fail := func(err error) (Terminal, error) {
		if rerr := s.Sandboxes.Remove(context.WithoutCancel(ctx), row.ID); rerr != nil {
			s.Log.Warn("removing a terminal that did not start", "sandbox", row.ID, "error", rerr)
		}
		return Terminal{}, err
	}
	// The terminal's row first: the git endpoint reads it to let the clone below read the repository.
	err = pgx.BeginFunc(ctx, s.Pool, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `INSERT INTO project_terminals (project, sandbox) VALUES ($1, $2)
			ON CONFLICT (project) DO UPDATE SET sandbox = excluded.sandbox, mode = 'diagnostics', opened_at = now(), input_at = now()`,
			p.id, row.ID)
		return err
	})
	if err != nil {
		return fail(err)
	}
	script := `set -e
crew-run creds --quiet
url="${CREW_CRED_BROKER%/}/git/` + repo + `.git"
git init -q -b main "$1"
cd "$1"
git config "http.$url.extraHeader" "Authorization: Bearer $(cat ~/.crew/token)"
git remote add origin "$url"
git config remote.origin.fetch '+refs/heads/*:refs/remotes/origin/*'
git fetch -q origin
git checkout -q -B main origin/main
git branch -q --set-upstream-to=origin/main main
`
	cctx, cancel := context.WithTimeout(ctx, 3*time.Minute)
	defer cancel()
	code, out, err := s.Runtime.Exec(cctx, orchestrator.SandboxName(row.ID), orchestrator.ExecSpec{
		Cmd: []string{"bash", "-c", script, "terminal", Workspace}, User: orchestrator.GuestUser,
		Env: []string{"HOME=/home/" + orchestrator.GuestUser},
	})
	if err != nil || code != 0 {
		s.Pool.Exec(context.WithoutCancel(ctx), `DELETE FROM project_terminals WHERE project = $1 AND sandbox = $2`, p.id, row.ID)
		return fail(fmt.Errorf("the repository could not be cloned into the terminal (exit %d): %v %s", code, err, lastLine(out)))
	}
	err = pgx.BeginFunc(ctx, s.Pool, func(tx pgx.Tx) error {
		return db.RecordProjectChange(ctx, tx, p.id, who, "terminals", p.name, nil, map[string]any{"opened": row.ID, "mode": ModeDiagnostics})
	})
	if err != nil {
		return fail(err)
	}
	return s.Get(ctx, p.id)
}

func githubRepos(remote *string) []string {
	if remote == nil {
		return nil
	}
	r := strings.TrimSuffix(strings.TrimPrefix(*remote, "git@github.com:"), ".git")
	if r == *remote || strings.Count(r, "/") != 1 {
		return nil
	}
	return []string{r}
}

func lastLine(s string) string {
	s = strings.TrimSpace(s)
	if i := strings.LastIndexByte(s, '\n'); i >= 0 {
		s = s[i+1:]
	}
	if len(s) > 300 {
		s = s[:300]
	}
	return s
}

// checkout switches the clone to a ticket's branch, as it is at the git endpoint.
func (s *Service) checkout(ctx context.Context, sandbox, branch string) error {
	if !validBranch(branch) {
		return refuse("%q is not a branch Crew makes", branch)
	}
	ctx, cancel := context.WithTimeout(ctx, time.Minute)
	defer cancel()
	code, out, err := s.Runtime.Exec(ctx, orchestrator.SandboxName(sandbox), orchestrator.ExecSpec{
		Cmd:  []string{"bash", "-c", `cd "$1" && git fetch -q origin && git checkout -q -B "$2" "origin/$2"`, "terminal", Workspace, branch},
		User: orchestrator.GuestUser,
	})
	if err != nil || code != 0 {
		return refuse("the terminal could not switch to %s: %s", branch, lastLine(out))
	}
	return nil
}

func validBranch(b string) bool {
	if b == "" || strings.Contains(b, "..") || strings.HasPrefix(b, "-") {
		return false
	}
	for _, r := range b {
		if !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || strings.ContainsRune("/._-", r)) {
			return false
		}
	}
	return true
}

// SetMode switches the project terminal between diagnostics and edit mode. Switching is recorded.
func (s *Service) SetMode(ctx context.Context, ref, mode, who string) (Terminal, error) {
	if mode != ModeDiagnostics && mode != ModeEdit {
		return Terminal{}, refuse("mode: diagnostics or edit")
	}
	p, err := s.project(ctx, s.Pool, ref)
	if err != nil {
		return Terminal{}, err
	}
	err = pgx.BeginFunc(ctx, s.Pool, func(tx pgx.Tx) error {
		var was string
		err := tx.QueryRow(ctx, `SELECT mode FROM project_terminals WHERE project = $1 FOR UPDATE`, p.id).Scan(&was)
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrNotOpen
		}
		if err != nil || was == mode {
			return err
		}
		if _, err := tx.Exec(ctx, `UPDATE project_terminals SET mode = $2 WHERE project = $1`, p.id, mode); err != nil {
			return err
		}
		return db.RecordProjectChange(ctx, tx, p.id, who, "terminals", p.name, map[string]any{"mode": was}, map[string]any{"mode": mode})
	})
	if err != nil {
		return Terminal{}, err
	}
	return s.Get(ctx, p.id)
}

// Close removes the project terminal's microVM. reason is recorded.
func (s *Service) Close(ctx context.Context, ref, who, reason string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	p, err := s.project(ctx, s.Pool, ref)
	if err != nil {
		return err
	}
	var sandbox string
	err = s.Pool.QueryRow(ctx, `SELECT sandbox FROM project_terminals WHERE project = $1`, p.id).Scan(&sandbox)
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrNotOpen
	}
	if err != nil {
		return err
	}
	if err := s.Sandboxes.Remove(ctx, sandbox); err != nil {
		return err
	}
	return pgx.BeginFunc(ctx, s.Pool, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `DELETE FROM project_terminals WHERE project = $1 AND sandbox = $2`, p.id, sandbox); err != nil {
			return err
		}
		return db.RecordProjectChange(ctx, tx, p.id, who, "terminals", p.name, map[string]any{"open": sandbox}, map[string]any{"closed": reason})
	})
}

// Reap closes every terminal that had no input for the idle time, and removes what the log keeps no
// longer. The server runs it every minute.
func (s *Service) Reap(ctx context.Context) {
	rows, err := s.Pool.Query(ctx, `SELECT project::text FROM project_terminals WHERE input_at < now() - $1::interval`,
		fmt.Sprintf("%d seconds", int(s.idle().Seconds())))
	if err != nil {
		s.Log.Warn("reading the terminals", "error", err)
		return
	}
	idle, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		s.Log.Warn("reading the terminals", "error", err)
		return
	}
	for _, p := range idle {
		if err := s.Close(ctx, p, "crew", fmt.Sprintf("no input for %s", s.idle())); err != nil && !errors.Is(err, ErrNotOpen) {
			s.Log.Warn("closing an idle terminal", "project", p, "error", err)
		}
	}
	if _, err := s.Pool.Exec(ctx, `DELETE FROM terminal_log WHERE at < now() - $1::interval`, fmt.Sprintf("%d seconds", int(Keep.Seconds()))); err != nil {
		s.Log.Warn("removing old terminal logs", "error", err)
	}
}

// Run reaps every minute until ctx ends.
func (s *Service) Run(ctx context.Context) {
	t := time.NewTicker(time.Minute)
	defer t.Stop()
	for {
		s.Reap(ctx)
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

// GitAccess is what a request from a project terminal may do at the git endpoint: read every branch
// and tag of its own project; in edit mode also push main, its own branches, and a ticket's branch
// while no agent runs on that ticket. A request that is not from a terminal is refused, for the next
// rule to look at.
func (s *Service) GitAccess(r *http.Request, repo string) (gitendpoint.Access, error) {
	token := gitendpoint.Token(r)
	local := ""
	if a, ok := r.Context().Value(http.LocalAddrContextKey).(net.Addr); ok {
		local = a.String()
	}
	if token == "" {
		return gitendpoint.Access{}, gitendpoint.ErrRefused
	}
	row, err := s.Sandboxes.Authorize(r.Context(), local, r.RemoteAddr, token)
	if err != nil || row.Kind != sandboxes.KindTerminal || row.Project == "" {
		return gitendpoint.Access{}, gitendpoint.ErrRefused
	}
	return s.access(r.Context(), row.ID, row.Project, repo)
}

func (s *Service) access(ctx context.Context, sandbox, projectID, repo string) (gitendpoint.Access, error) {
	var mode string
	err := s.Pool.QueryRow(ctx, `SELECT x.mode FROM project_terminals x JOIN git_repos g ON g.project = x.project
		WHERE x.project = $1 AND x.sandbox = $2 AND g.name = $3`, projectID, sandbox, repo).Scan(&mode)
	if err != nil {
		return gitendpoint.Access{}, gitendpoint.ErrRefused
	}
	acc := gitendpoint.Access{Repo: repo, Who: "terminal " + sandbox, Read: []string{"refs/heads/", "refs/tags/"}}
	if mode != ModeEdit {
		return acc, nil
	}
	acc.Push = []string{"refs/heads/main", OwnBranches}
	rows, err := s.Pool.Query(ctx, `SELECT t.branch FROM tickets t WHERE t.project = $1 AND t.branch IS NOT NULL
		AND t.stage NOT IN ('done', 'closed')
		AND NOT EXISTS (SELECT 1 FROM runs r JOIN subtasks st ON st.id = r.subtask WHERE st.ticket = t.id AND r.state <> 'ended')`, projectID)
	if err != nil {
		return gitendpoint.Access{}, gitendpoint.ErrRefused
	}
	branches, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		return gitendpoint.Access{}, gitendpoint.ErrRefused
	}
	for _, b := range branches {
		acc.Push = append(acc.Push, "refs/heads/"+b)
	}
	return acc, nil
}

// Pushed acts on a push from a project terminal, after the endpoint recorded it: a main that goes on
// from Crew's becomes the new base and is sent on to the remote; a ticket's branch tells the next
// agent on it what changed. A push from anything else is not a terminal's.
func (s *Service) Pushed(acc gitendpoint.Access, ref, old, new string) {
	sandbox, ok := strings.CutPrefix(acc.Who, "terminal ")
	if !ok || acc.Run != "" {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	var projectID, name, key string
	var remote *string
	if err := s.Pool.QueryRow(ctx, `SELECT p.id::text, p.name, p.key, p.remote FROM project_terminals x JOIN projects p ON p.id = x.project
		WHERE x.sandbox = $1`, sandbox).Scan(&projectID, &name, &key, &remote); err != nil {
		s.Log.Warn("a push from a terminal that is not open", "sandbox", sandbox, "ref", ref)
		return
	}
	// notTaken records, in the project's history, a main the terminal pushed that Crew did not take,
	// so the operator's git push that said it worked is answered.
	notTaken := func(why string) {
		s.Log.Warn("main from the terminal was not taken", "project", name, "why", why)
		err := pgx.BeginFunc(ctx, s.Pool, func(tx pgx.Tx) error {
			return db.RecordProjectChange(ctx, tx, projectID, "crew", "terminals", name, map[string]any{"main": old},
				map[string]any{"notTaken": new, "why": why})
		})
		if err != nil {
			s.Log.Error("recording main from the terminal", "project", name, "error", err)
		}
	}
	switch {
	case ref == "refs/heads/main" && new == "":
		// The git endpoint could not read the refs of the push: whatever it brought to main is not
		// taken, and Crew's main is given back (since 1.08).
		why := "the refs of its push could not be read"
		if gerr := s.Git.GiveBackMain(ctx, acc.Repo); gerr != nil {
			s.Log.Error("giving main back to the git endpoint", "project", name, "error", gerr)
			why += "; and the git endpoint may still serve that main, as Crew's could not be given back to it: " + gerr.Error()
		}
		notTaken(why)
		return
	case ref == "refs/heads/main":
		// What it changed is read first: a main whose changes cannot be read is not taken. A file that
		// is code for the agent CLI, or a workflow, never merges by itself (13.8, AGENTS.md rule 13):
		// whatever runs in the terminal could have pushed it, so the held main names those files.
		files, err := s.Git.ChangedFiles(ctx, acc.Repo, old, new)
		if err != nil {
			files = nil
			s.Log.Warn("reading what a push to main changed", "project", name, "error", err)
		}
		// An index file of main that cannot be read, by any failure, is held with every file the push
		// changes counted as code, never read as one that names nothing, so a main that repairs it can
		// still be confirmed (since 1.05; in 1.04 such a main was not taken).
		whole := false
		named, _, nerr := cli.Indexed(func(f string) ([]byte, cli.Way) {
			b, way, ferr := s.Git.IndexFile(ctx, acc.Repo, f)
			if ferr != nil {
				whole = true
				s.Log.Warn("main's index files cannot be read: every file the push changes counts as code", "project", name, "error", ferr)
			}
			return b, way
		})
		if nerr != nil {
			whole = true
			s.Log.Warn("main's index files cannot be read: every file the push changes counts as code", "project", name, "error", nerr)
		}
		code := append(cli.CodeFiles(), named...)
		// And what a link of main that stands for code leads to; one that cannot be followed holds the
		// main the same way (since 1.06).
		linked, lerr := s.Git.Linked(ctx, acc.Repo, code)
		if lerr != nil {
			whole = true
			s.Log.Warn("main's links cannot be followed: every file the push changes counts as code", "project", name, "error", lerr)
		}
		code = append(code, linked...)
		match := cli.NewMatcher(code)
		var codeChanged []string
		for _, f := range files {
			if whole || match.Matches(f) {
				codeChanged = append(codeChanged, f)
			}
		}
		if err != nil {
			why := "what it changed could not be read"
			if gerr := s.Git.GiveBackMain(ctx, acc.Repo); gerr != nil {
				s.Log.Error("giving main back to the git endpoint", "project", name, "error", gerr)
				why += "; and the git endpoint still serves that main, as Crew's could not be given back to it: " + gerr.Error()
			}
			notTaken(why)
			return
		}
		// Anything that runs in the terminal while edit mode is on could have pushed it: it waits for
		// the operator's confirmation, with its diff, before it becomes the base (13.11, M8).
		var id int64
		if files == nil {
			files = []string{}
		}
		if codeChanged == nil {
			codeChanged = []string{}
		}
		err = pgx.BeginFunc(ctx, s.Pool, func(tx pgx.Tx) error {
			if err := tx.QueryRow(ctx, `INSERT INTO terminal_mains (project, repo, old, new, files, code, pushed_by) VALUES ($1, $2, $3, $4, $5, $6, $7)
				RETURNING id`, projectID, acc.Repo, old, new, files, codeChanged, acc.Who).Scan(&id); err != nil {
				return err
			}
			return db.RecordProjectChange(ctx, tx, projectID, "operator", "terminals", name, map[string]any{"main": old},
				map[string]any{"held": new, "from": "the terminal, in edit mode", "waits": "for your confirmation", "code": codeChanged})
		})
		if err != nil {
			s.Log.Error("recording main from the terminal", "project", name, "error", err)
			if gerr := s.Git.GiveBackMain(ctx, acc.Repo); gerr != nil {
				s.Log.Error("giving main back to the git endpoint", "project", name, "error", gerr)
			}
			return
		}
		if err := s.Git.HoldMain(ctx, acc.Repo, new, strconv.FormatInt(id, 10)); err != nil {
			s.Pool.Exec(ctx, `UPDATE terminal_mains SET state = 'failed', detail = $2, decided_at = now() WHERE id = $1`, id, err.Error())
			notTaken(err.Error())
		}
	case strings.HasPrefix(ref, "refs/heads/") && !strings.HasPrefix(ref, OwnBranches):
		var ticket string
		err := s.Pool.QueryRow(ctx, `SELECT $2 || '-' || number FROM tickets WHERE project = $1 AND branch = $3`,
			projectID, key, strings.TrimPrefix(ref, "refs/heads/")).Scan(&ticket)
		if err != nil || s.Comment == nil {
			return
		}
		text := fmt.Sprintf("I pushed to this ticket's branch myself, from the project terminal: %s is now at %s (it was at %s). "+
			"Read what I changed before you go on.", strings.TrimPrefix(ref, "refs/heads/"), short(new), short(old))
		if err := s.Comment(ctx, projectID, ticket, text); err != nil {
			s.Log.Warn("telling the next agent of a push", "ticket", ticket, "error", err)
		}
	}
}

// HeldMain is a main the terminal pushed that waits for the operator's confirmation.
type HeldMain struct {
	ID       int64     `json:"id"`
	Project  string    `json:"project"`
	Old      string    `json:"old"`
	New      string    `json:"new"`
	Files    []string  `json:"files"`
	PushedBy string    `json:"pushedBy"`
	PushedAt time.Time `json:"pushedAt"`
}

// Held are the mains of a project, or of every project when ref is "", that wait for the operator.
func Held(ctx context.Context, q db.Querier, projectID string) ([]HeldMain, error) {
	rows, err := q.Query(ctx, `SELECT m.id, p.name, m.old, m.new, m.files, m.pushed_by, m.pushed_at FROM terminal_mains m
		JOIN projects p ON p.id = m.project WHERE `+waitingMain+` AND ($1 = '' OR m.project::text = $1) ORDER BY m.id`, projectID)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(r pgx.CollectableRow) (HeldMain, error) {
		var h HeldMain
		err := r.Scan(&h.ID, &h.Project, &h.Old, &h.New, &h.Files, &h.PushedBy, &h.PushedAt)
		return h, err
	})
}

// waitingMain is a held main that waits for the operator: also one whose decision began long ago and
// never ended, as when the server stopped in the middle of it (round two of the review of M8).
const waitingMain = `(m.state = 'waiting' OR (m.state = 'deciding' AND m.decided_at < now() - interval '10 minutes'))`

// ErrNotHeld says no main of that number waits.
var ErrNotHeld = errors.New("no main of that number waits for you")

// DecideMain is the operator's answer to a held main: take makes it the new base and sends it on to
// the remote, as long as Crew's main is still the one it went on from; otherwise it is refused, and
// forgotten.
func (s *Service) DecideMain(ctx context.Context, ref string, id int64, take bool, who string) error {
	p, err := s.project(ctx, s.Pool, ref)
	if err != nil {
		return err
	}
	// The decision claims the row first: a second click, another tab, or a Refuse at the same time
	// finds it decided (reviews of M8).
	var repo, old, new string
	var remote *string
	err = s.Pool.QueryRow(ctx, `UPDATE terminal_mains m SET state = 'deciding', decided_at = now() FROM projects x
		WHERE m.id = $1 AND m.project = $2 AND `+waitingMain+` AND x.id = m.project
		RETURNING m.repo, m.old, m.new, x.remote`, id, p.id).Scan(&repo, &old, &new, &remote)
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrNotHeld
	}
	if err != nil {
		return err
	}
	key := strconv.FormatInt(id, 10)
	state, detail := "refused", "refused by the operator"
	if take {
		state, detail = "taken", "taken as main"
		if err := s.Git.AdvanceMain(ctx, repo, old, new); err != nil {
			state, detail = "failed", "it could not become main: "+err.Error()
		}
	}
	if err := s.Git.DropHeld(ctx, repo, key); err != nil {
		s.Log.Warn("forgetting a held main", "project", p.name, "error", err)
	}
	err = pgx.BeginFunc(ctx, s.Pool, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `UPDATE terminal_mains SET state = $2, detail = $3, decided_by = $4, decided_at = now() WHERE id = $1 AND state = 'deciding'`,
			id, state, detail, who); err != nil {
			return err
		}
		after := map[string]any{"held": new, "decided": state, "why": detail}
		if state == "taken" {
			after = map[string]any{"main": new, "from": "the terminal, in edit mode, confirmed"}
			if remote != nil {
				if _, err := tx.Exec(ctx, `INSERT INTO pushes (project, remote, refs, who) VALUES ($1, $2, '{refs/heads/main}', 'operator')`,
					p.id, *remote); err != nil {
					return err
				}
			}
		}
		return db.RecordProjectChange(ctx, tx, p.id, who, "terminals", p.name, map[string]any{"main": old}, after)
	})
	if err != nil {
		// What happened to main stands; the row says so, without its history.
		s.Pool.Exec(context.WithoutCancel(ctx), `UPDATE terminal_mains SET state = $2, detail = $3, decided_by = $4, decided_at = now()
			WHERE id = $1 AND state = 'deciding'`, id, state, detail+"; its history could not be written: "+err.Error(), who)
		return err
	}
	if state == "taken" && s.Kick != nil {
		s.Kick()
	}
	if state == "failed" {
		return errors.New(detail)
	}
	return nil
}

func short(c string) string {
	if len(c) > 7 {
		return c[:7]
	}
	if c == "" {
		return "nothing"
	}
	return c
}
