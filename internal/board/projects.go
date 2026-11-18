package board

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"net/url"
	"slices"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/kesokaj/crew/internal/config"
	"github.com/kesokaj/crew/internal/db"
	"github.com/kesokaj/crew/internal/github"
	"github.com/kesokaj/crew/internal/gitstore"
	"github.com/kesokaj/crew/internal/settings"
)

// NewProject is what a project is created from (7.2, step 2; 15.4): a name, a key, the plan, the
// optional stages the plan asks for, and how its repository is made.
type NewProject struct {
	Name string `json:"name"`
	Key  string `json:"key"`
	Goal string `json:"goal"`
	Plan string `json:"plan"`
	// Stages are the optional stages that are on. nil keeps the defaults (Review and Test).
	Stages []string `json:"stages"`
	Repo   Repo     `json:"repository"`
	// Draft is the plan draft the project is made from (7.2, steps 2 and 3): its newest plan is the
	// original plan, and its front matter gives the stages not given here.
	Draft string `json:"draft,omitempty"`
	// own is a project of Crew's own (16.2): made running, with no breakdown and nothing to push,
	// in the one transaction that makes it. hidden is the gate's, which no list shows; protected is
	// Crew's own source project, which is always protected (7.13).
	own, hidden, protected bool
}

// Repo is the choice of how a project's repository is made (6.6): new on GitHub, new on the machine,
// or an existing one cloned over https.
type Repo struct {
	Kind string `json:"kind"` // github | local | existing
	// URL is the address of an existing repository; for github, the SSH address of an empty
	// repository the operator made, when gh is not signed in.
	URL string `json:"url,omitempty"`
	// Name is the name of the repository Crew creates on GitHub; the project's name when empty.
	Name string `json:"name,omitempty"`
}

// The largest plan a project takes.
const maxPlan = 1 << 20

func (n *NewProject) check() error {
	n.Name, n.Key, n.Goal = strings.TrimSpace(n.Name), strings.TrimSpace(n.Key), strings.TrimSpace(n.Goal)
	switch {
	case !settings.ProjectName.MatchString(n.Name) || settings.UUIDShaped(n.Name):
		return errf(Invalid, "name: small letters, digits, and dashes, at most 63, such as storefront-api, and not shaped as a project's id")
	case !settings.ProjectKey.MatchString(n.Key):
		return errf(Invalid, "key: a capital letter, then at most nine capital letters or digits, such as SF")
	case strings.TrimSpace(n.Plan) == "":
		return errf(Invalid, "plan: a project is created from its plan")
	case len(n.Plan) > maxPlan:
		return errf(Invalid, "plan: at most 1 MB")
	case len(n.Goal) > 2000:
		return errf(Invalid, "goal: at most 2000 characters")
	}
	switch n.Repo.Kind {
	case "local":
		if n.Repo.URL != "" {
			return errf(Invalid, "repository: a new local repository has no address")
		}
	case "existing":
		u, err := url.Parse(n.Repo.URL)
		if err != nil || u.Scheme != "https" || u.User != nil || u.Host == "" || u.RawQuery != "" || u.Fragment != "" {
			return errf(Invalid, "repository.url: an https address with no credentials in it")
		}
	case "github":
		if n.Repo.URL != "" && !github.SSHURL.MatchString(n.Repo.URL) {
			return errf(Invalid, "repository.url: the SSH address of an empty repository on GitHub, such as git@github.com:you/shop.git")
		}
		if n.Repo.URL != "" && n.Repo.Name != "" {
			return errf(Invalid, "repository: a name for a repository Crew creates, or the address of one you made, not both")
		}
		if n.Repo.Name != "" && !github.RepoName.MatchString(n.Repo.Name) {
			return errf(Invalid, "repository.name: letters, digits, '.', '-', and '_', at most 100")
		}
	default:
		return errf(Invalid, "repository.kind: github, local, or existing")
	}
	if n.Repo.Kind != "github" && n.Repo.Name != "" {
		return errf(Invalid, "repository.name: only for a repository Crew creates on GitHub")
	}
	for _, s := range n.Stages {
		if !slices.Contains(settings.OptionalStages, s) {
			return errf(Invalid, "stages: %q is not an optional stage: review, test, e2e, verification", s)
		}
	}
	return nil
}

// CreateProject creates a project with its repositories (6.6), its original plan, and its settings.
// The repositories are made first; when the rows cannot be written they are removed again, so a
// project never exists without its repository (R39), nor a repository without its project.
func (b *Board) CreateProject(ctx context.Context, n NewProject, who string) (*Project, error) {
	draftVersion := 0
	if n.Draft != "" {
		var err error
		if draftVersion, err = b.fromDraft(ctx, &n); err != nil {
			return nil, err
		}
	}
	if err := n.check(); err != nil {
		return nil, err
	}
	var taken bool
	if err := b.Pool.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM projects WHERE name = $1 OR key = $2)`, n.Name, n.Key).Scan(&taken); err != nil {
		return nil, err
	}
	if taken || b.Git.Exists(n.Name) {
		return nil, errf(Conflict, "a project with the name %s or the key %s exists", n.Name, n.Key)
	}
	doc, err := settings.ProjectDefaults(n.Name, n.Key)
	if err != nil {
		return nil, err
	}
	if n.Stages != nil {
		on := map[string]bool{}
		for _, s := range settings.OptionalStages {
			on[s] = slices.Contains(n.Stages, s)
		}
		if doc, err = settings.SetStagesOn(doc, on); err != nil {
			return nil, err
		}
	}
	source, remote := "local", ""
	switch n.Repo.Kind {
	case "local", "github":
		if b.Identity == nil {
			return nil, errf(Refused, "no git identity: set it up in Setup first")
		}
		id, err := b.Identity(ctx)
		if err != nil {
			return nil, errf(Refused, "no git identity: set it up in Setup first (%v)", err)
		}
		if n.Repo.Kind == "github" {
			// GitHub first: when it refuses, nothing is made (21).
			if remote, err = b.githubRepo(ctx, n); err != nil {
				return nil, err
			}
			source = "github " + remote
		}
		if err := b.Git.CreateLocal(ctx, n.Name, firstFiles(n), n.Key+": the project's first commit", id); err != nil {
			return nil, madeOnGitHub(n, remote, b.gitError(err))
		}
	case "existing":
		source = "existing " + n.Repo.URL
		header, done := "", func() {}
		if b.ReadHeader != nil {
			if header, done, err = b.ReadHeader(ctx, n.Repo.URL); err != nil {
				return nil, errf(Refused, "the repository cannot be read: %v", err)
			}
		}
		err := b.Git.CreateFrom(ctx, n.Name, n.Repo.URL, header)
		done()
		if err != nil {
			return nil, b.gitError(err)
		}
	}
	p, err := b.insertProject(ctx, n, doc, source, remote, draftVersion, who)
	if err != nil {
		b.Git.Remove(n.Name)
		return nil, madeOnGitHub(n, remote, err)
	}
	// The first push, now, so its result is in the answer; a push that fails is tried again (21).
	if remote != "" {
		b.PushPending(ctx, p.ID)
		if p, err = b.Project(ctx, p.ID); err != nil {
			return nil, err
		}
	}
	_, _ = b.SyncMainWorkflow(ctx, b.Pool, p)
	return p, nil
}

// madeOnGitHub names, in the error of a project that was not made, the repository Crew made on GitHub
// for it, which is empty and is used by giving its address (21).
func madeOnGitHub(n NewProject, remote string, err error) error {
	if remote == "" || n.Repo.URL != "" {
		return err
	}
	msg := fmt.Sprintf("%v; the repository %s on GitHub was made and is empty: give it as the repository's url to use it", err, remote)
	var be *Error
	if errors.As(err, &be) {
		return &Error{Kind: be.Kind, Msg: msg}
	}
	return errors.New(msg)
}

// githubRepo is the remote of a new project on GitHub: the empty repository the operator made, or one
// Crew creates under the gh login's account (6.6).
func (b *Board) githubRepo(ctx context.Context, n NewProject) (string, error) {
	if n.Repo.URL != "" {
		return n.Repo.URL, nil
	}
	if b.GitHub == nil {
		return "", errf(Refused, "GitHub is not set up on this server")
	}
	name := n.Repo.Name
	if name == "" {
		name = n.Name
	}
	r, err := b.GitHub.CreateRepo(ctx, name, n.Goal)
	var ge *github.Error
	if errors.As(err, &ge) {
		return "", errf(Refused, "%s", ge.Msg)
	}
	if err != nil {
		b.log().Warn("creating a repository on GitHub", "error", err)
		return "", errf(Refused, "the repository could not be created on GitHub; the server's log says why")
	}
	b.log().Info("created a repository on GitHub", "repository", r.FullName)
	return r.SSHURL, nil
}

func (b *Board) gitError(err error) error {
	if errors.Is(err, gitstore.ErrExists) {
		return errf(Conflict, "a repository of that name exists on the machine")
	}
	// What git printed stays in the server's log: it names the server's own paths.
	b.log().Warn("making a project's repository", "error", err)
	if errors.Is(err, gitstore.ErrNoMain) {
		return errf(Refused, "the repository has no branch main")
	}
	return errf(Refused, "the repository could not be made or read; the server's log says why")
}

// firstFiles is the first commit of a new local repository: the overview page, the project's own
// context, a starter Makefile so default setup and test commands succeed, and the changelog the
// backend writes (6.2).
func firstFiles(n NewProject) map[string]string {
	goal := n.Goal
	if goal == "" {
		goal = "Built with Crew."
	}
	agents := "# " + n.Name + "\n\n" +
		"The project's own context, read by every run. Add what an agent must know here.\n\n" +
		"## Setup & Commands\n\n" +
		"- **Setup (`sandbox.setup`):** `make setup` — installs dependencies and prepares a fresh sandbox.\n" +
		"- **Tests (`sandbox.test`):** `make test` — runs the project's full test and typecheck suite.\n" +
		"- Keep `Makefile` targets `setup` and `test` working as you add packages, dependencies, or a frontend.\n\n" +
		"## Stack & Skills\n\n" +
		"- **Backend (Go):** `cmd/<app>/` + `internal/`, `log/slog` JSON logging, stdlib first (`golang`, `json-api-style`, `codebase-design` skills).\n" +
		"- **Frontend & UI:** TypeScript + React + Tailwind in `web/` (`frontend`, `frontend-ui-modern`, `frontend-ui-economist`, `typescript` skills).\n" +
		"- **Modularity:** keep files cohesive and under 400 lines (`code-modularity` rule); no generic `utils` or `helpers` dumping grounds.\n\n" +
		"## Documentation\n\n" +
		"- Overview in `README.md`, architecture in `docs/architecture.md`, setup in `docs/setup.md`, and decisions in `docs/decisions/`.\n"
	makefile := ".PHONY: setup test\n\n" +
		"setup:\n" +
		"\t@if [ -f go.mod ]; then go mod download; fi\n" +
		"\t@if [ -f web/package-lock.json ]; then npm --prefix web ci; elif [ -f package-lock.json ]; then npm ci; fi\n\n" +
		"test:\n" +
		"\t@if [ -f go.mod ]; then go vet ./... && go test -race ./...; fi\n" +
		"\t@if [ -f web/package.json ]; then npm --prefix web test --if-present && npm --prefix web run build --if-present; elif [ -f package.json ]; then npm test --if-present && npm run build --if-present; fi\n"
	specState := "waiting_project_ok"
	if n.own {
		specState = "approved"
	}
	return map[string]string{
		"README.md":                     "# " + n.Name + "\n\n" + goal + "\n",
		"AGENTS.md":                     agents,
		"WORKFLOW.md":                   defaultWorkflowFile(n),
		".crew/specs/000-foundation.md": renderFoundationSpecMarkdown(n.Name, n.Key, goal, n.Plan, specState),
		"Makefile":                      makefile,
		"CHANGELOG.md":                  "# Changelog\n\nOne entry per ticket that reaches Done.\n",
	}
}

func (b *Board) insertProject(ctx context.Context, n NewProject, doc settings.Doc, source, remote string, draftVersion int, who string) (*Project, error) {
	id, err := newUUID()
	if err != nil {
		return nil, err
	}
	var p *Project
	err = b.inTx(ctx, func(tx pgx.Tx) error {
		state, okAt := "planning", any(nil)
		if n.own {
			state, okAt = "running", b.now()
		}
		_, err := tx.Exec(ctx, `INSERT INTO projects (id, name, key, goal, repo_source, remote, state, ok_at, hidden, protected)
			VALUES ($1, $2, $3, $4, $5, nullif($6, ''), $7, $8, $9, $10)`, id, n.Name, n.Key, n.Goal, source, remote, state, okAt, n.hidden, n.protected)
		if err != nil {
			var pe *pgconn.PgError
			if errors.As(err, &pe) && pe.Code == "23505" {
				return errf(Conflict, "a project with the name %s or the key %s exists", n.Name, n.Key)
			}
			return err
		}
		if _, err := tx.Exec(ctx, `INSERT INTO git_repos (name, project, seeded_from) VALUES ($1, $2, $3)`,
			n.Name, id, nullIf(n.Repo.URL)); err != nil {
			return err
		}
		if p, err = projectByRef(ctx, tx, id, true); err != nil {
			return err
		}
		if n.Repo.Kind == "existing" {
			if imported, ierr := b.ImportMirror(ctx, tx, p, doc, who); ierr != nil {
				return ierr
			} else if imported {
				if p, err = projectByRef(ctx, tx, id, true); err != nil {
					return err
				}
			}
		}
		if _, err := tx.Exec(ctx, `INSERT INTO plans (project, kind, number, text, created_by) VALUES ($1, 'original', 1, $2, $3)
			ON CONFLICT DO NOTHING`, id, n.Plan, who); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `INSERT INTO chat_rooms (project_id, channel, topic) VALUES ($1, '#' || $2, $3)
			ON CONFLICT (project_id) DO NOTHING`, id, n.Name, n.Goal); err != nil {
			return err
		}
		if err := b.seedFoundationSpecTx(ctx, tx, p, n.Goal, n.Plan, n.own, who); err != nil {
			return err
		}
		if n.Draft != "" {
			if err := takeDraft(ctx, tx, n.Draft, draftVersion, id, who); err != nil {
				return err
			}
		}
		if p, err = projectByRef(ctx, tx, id, true); err != nil {
			return err
		}
		if remote != "" && !n.own {
			if _, err := tx.Exec(ctx, `INSERT INTO pushes (project, remote, refs, who) VALUES ($1, $2, '{refs/heads/main}', $3)`,
				id, remote, who); err != nil {
				return err
			}
		}
		if err := db.RecordProjectChange(ctx, tx, id, who, "projects", id, nil, p); err != nil {
			return err
		}
		return settings.SaveProject(ctx, tx, id, n.Name, n.Key, doc, who)
	})
	return p, err
}

func nullIf(s string) any {
	if s == "" {
		return nil
	}
	return s
}

func newUUID() (string, error) {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	b[6] = b[6]&0x0f | 0x40
	b[8] = b[8]&0x3f | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16]), nil
}

// Plan is a project's plan: the original, the breakdown, the amendments, and the comments the
// breakdown was sent back with (7.2).
type Plan struct {
	Original   string      `json:"original"`
	Breakdown  *string     `json:"breakdown"`
	Amendments []PlanEntry `json:"amendments"`
	SentBack   []PlanEntry `json:"sentBack"`
}

// PlanEntry is one row of a plan.
type PlanEntry struct {
	Number    int       `json:"number"`
	Text      string    `json:"text"`
	CreatedBy string    `json:"createdBy"`
	CreatedAt time.Time `json:"createdAt"`
}

// Plan returns a project's plan. The breakdown is the newest one.
func (b *Board) Plan(ctx context.Context, p *Project) (*Plan, error) {
	return readPlan(ctx, b.Pool, p.ID)
}

func readPlan(ctx context.Context, q db.Querier, project string) (*Plan, error) {
	rows, err := q.Query(ctx, `SELECT kind, number, text, created_by, created_at FROM plans WHERE project = $1 ORDER BY kind, number`, project)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := &Plan{Amendments: []PlanEntry{}, SentBack: []PlanEntry{}}
	for rows.Next() {
		var kind string
		var e PlanEntry
		if err := rows.Scan(&kind, &e.Number, &e.Text, &e.CreatedBy, &e.CreatedAt); err != nil {
			return nil, err
		}
		switch kind {
		case "original":
			out.Original = e.Text
		case "breakdown":
			t := e.Text
			out.Breakdown = &t
		case "amendment":
			out.Amendments = append(out.Amendments, e)
		case "sent_back":
			out.SentBack = append(out.SentBack, e)
		}
	}
	return out, rows.Err()
}

// SetBreakdown saves a new version of the breakdown (7.2, step 4), before the OK.
func (b *Board) SetBreakdown(ctx context.Context, p *Project, text, who string) error {
	return b.addPlanRow(ctx, p, "breakdown", text, who)
}

// maxPlanRows is how many versions of a breakdown, or comments it was sent back with, a project
// keeps: rows the server may only add, which a run that a text turned against Crew must not pile up.
const maxPlanRows = 100

func (b *Board) addPlanRow(ctx context.Context, p *Project, kind, text, who string) error {
	if strings.TrimSpace(text) == "" || len(text) > maxPlan {
		return errf(Invalid, "the text must be given, and at most 1 MB")
	}
	return b.inTx(ctx, func(tx pgx.Tx) error {
		locked, err := projectByRef(ctx, tx, p.ID, true)
		if err != nil {
			return err
		}
		// The breakdown and a comment on it belong before the OK; the state is read under the lock,
		// so neither lands after an OK made at the same moment.
		if (kind == "breakdown" || kind == "sent_back") && locked.State != "planning" {
			am, err := Amending(ctx, tx, locked.ID)
			if err != nil {
				return err
			}
			if am == nil {
				return errf(Refused, "%s is %s: the breakdown changes only before the OK, or of an amendment before its OK", locked.Name, locked.State)
			}
		}
		// One amendment is open at a time, checked under the project's lock (7.13).
		if kind == "amendment" {
			am, err := Amending(ctx, tx, locked.ID)
			if err != nil {
				return err
			}
			if am != nil {
				return errf(Refused, "amendment %d of %s waits for its OK", am.Number, locked.Name)
			}
		}
		var rows int
		if err := tx.QueryRow(ctx, `SELECT count(*) FROM plans WHERE project = $1 AND kind = $2`, p.ID, kind).Scan(&rows); err != nil {
			return err
		}
		if rows >= maxPlanRows {
			return errf(Refused, "%s has %d of these already, as many as a project keeps", locked.Name, rows)
		}
		var n int
		err = tx.QueryRow(ctx, `INSERT INTO plans (project, kind, number, text, created_by)
			SELECT $1, $2, coalesce(max(number), 0) + 1, $3, $4 FROM plans WHERE project = $1 AND kind = $2
			RETURNING number`, p.ID, kind, text, who).Scan(&n)
		if err != nil {
			return err
		}
		// A breakdown the operator sent back is feedback on the planner's work (9.5).
		if kind == "sent_back" {
			if err := b.systemFeedback(ctx, tx, locked, "planner", "The operator sent the breakdown back: "+text, "breakdown", nil); err != nil {
				return err
			}
			if err := intervention(ctx, tx, locked.ID, 0, SentBack, "Sent the breakdown back", nil); err != nil {
				return err
			}
		}
		return db.RecordProjectChange(ctx, tx, p.ID, who, "plans", fmt.Sprintf("%s/%s/%d", p.Key, kind, n), nil, map[string]any{"kind": kind, "number": n})
	})
}

// OK is the project OK (7.2, step 5): every ticket in Planning moves to Backlog, the project runs,
// and the approved versions of the plan and of the tickets are recorded with it.
func (b *Board) OK(ctx context.Context, ref, who string) (*Project, error) {
	var out *Project
	err := b.inTx(ctx, func(tx pgx.Tx) error {
		p, err := projectByRef(ctx, tx, ref, true)
		if err != nil {
			return err
		}
		if p.State != "planning" {
			// The OK of an amendment's tickets, while the project runs on (7.13).
			am, err := Amending(ctx, tx, p.ID)
			if err != nil {
				return err
			}
			if am == nil {
				return errf(Refused, "%s is %s: only a project in planning, or an amendment, waits for its OK", p.Name, p.State)
			}
			if err := b.okAmendment(ctx, tx, p, am, who); err != nil {
				return err
			}
			out, err = projectByRef(ctx, tx, p.ID, false)
			return err
		}
		doc, err := b.Settings(ctx, tx, p)
		if err != nil {
			return err
		}
		pipe := PipelineOf(doc)
		planned, err := tickets(ctx, tx, p.ID, "planning")
		if err != nil {
			return err
		}
		if len(planned) == 0 {
			return errf(Refused, "%s has no ticket in Planning to approve", p.Name)
		}
		approved := make([]map[string]any, 0, len(planned))
		for _, t := range planned {
			if err := b.move(ctx, tx, p, pipe, t, settings.StageBacklog, who, "the project OK", nil); err != nil {
				return err
			}
			approved = append(approved, map[string]any{"key": t.Key, "updated": t.UpdatedAt})
		}
		var planRows []int64
		rows, err := tx.Query(ctx, `SELECT id FROM plans WHERE project = $1 AND kind IN ('original', 'breakdown') ORDER BY id`, p.ID)
		if err != nil {
			return err
		}
		if planRows, err = pgx.CollectRows(rows, pgx.RowTo[int64]); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `UPDATE projects SET state = 'running', ok_at = $2, updated_at = now() WHERE id = $1`, p.ID, b.now()); err != nil {
			return err
		}
		if err := b.approveFoundationSpecTx(ctx, tx, p, who); err != nil {
			return err
		}
		if err := db.RecordProjectChange(ctx, tx, p.ID, who, "projects", p.ID, map[string]any{"state": p.State},
			map[string]any{"state": "running", "ok": map[string]any{"plans": planRows, "tickets": approved, "stages": doc["stages"]}}); err != nil {
			return err
		}
		out, err = projectByRef(ctx, tx, p.ID, false)
		return err
	})
	if err == nil && out != nil {
		b.mirrorFoundationSpecOnMain(ctx, out)
	}
	return out, err
}

// SendBack sends the breakdown back to the lead with a comment, before the OK (7.2, step 5).
func (b *Board) SendBack(ctx context.Context, ref, comment, who string) error {
	p, err := b.Project(ctx, ref)
	if err != nil {
		return err
	}
	if p.State != "planning" {
		if am, err := Amending(ctx, b.Pool, p.ID); err != nil || am == nil {
			if err == nil {
				err = errf(Refused, "%s is %s: the breakdown is sent back before the OK", p.Name, p.State)
			}
			return err
		}
	}
	return b.addPlanRow(ctx, p, "sent_back", comment, who)
}

// The operator's control of a project (15.4): pause, resume, and archive.
var stateMoves = map[string]struct {
	from []string
	to   string
}{
	"pause":   {[]string{"running"}, "paused"},
	"resume":  {[]string{"paused"}, "running"},
	"archive": {[]string{"planning", "running", "paused", "done"}, "archived"},
}

// SetState pauses, resumes, or archives a project. A paused project starts nothing and moves no
// ticket (15.2); resuming goes on where it stopped (7.7).
func (b *Board) SetState(ctx context.Context, ref, action, reason, who string) (*Project, error) {
	m, ok := stateMoves[action]
	if !ok {
		return nil, errf(Invalid, "%q: pause, resume, or archive", action)
	}
	var out *Project
	err := b.inTx(ctx, func(tx pgx.Tx) error {
		p, err := projectByRef(ctx, tx, ref, true)
		if err != nil {
			return err
		}
		if !slices.Contains(m.from, p.State) {
			return errf(Refused, "%s is %s, so it cannot %s", p.Name, p.State, action)
		}
		// Crew's project is not archived while improvements.crew_project names it: every save of the
		// settings would then be refused (security review of M16 P2).
		if action == "archive" {
			// With the settings' own lock, so a save that names the project waits for this, or this for it
			// (round 2 of the review of P2).
			if err := settings.LockMachine(ctx, tx); err != nil {
				return err
			}
			crew, err := CrewProject(ctx, tx)
			if err != nil {
				return err
			}
			if crew == p.Name {
				return errf(Refused, "%s is Crew's project: clear improvements.crew_project in the settings first", p.Name)
			}
			if p.Protected {
				return errf(Refused, "%s is protected: switch Protected off in its Settings first", p.Name)
			}
		}
		// A project Crew paused at a limit resumes when the limit is raised: your Resume while it is
		// still passed would only pause it again (7.7).
		if action == "resume" && who == Operator {
			crew, err := PausedByCrew(ctx, tx, p.ID)
			if err != nil {
				return err
			}
			if crew {
				over, err := b.OverLimit(ctx, tx, p)
				if err != nil {
					return err
				}
				if over != "" {
					return errf(Refused, "%s. Raise the limit in the project's Settings, and it resumes by itself", over)
				}
			}
		}
		var why any
		if m.to == "paused" {
			why = reason
		}
		if _, err := tx.Exec(ctx, `UPDATE projects SET state = $2, pause_reason = $3, updated_at = now() WHERE id = $1`, p.ID, m.to, why); err != nil {
			return err
		}
		if err := db.RecordProjectChange(ctx, tx, p.ID, who, "projects", p.ID,
			map[string]any{"state": p.State, "pauseReason": p.PauseReason}, map[string]any{"state": m.to, "pauseReason": why}); err != nil {
			return err
		}
		out, err = projectByRef(ctx, tx, p.ID, false)
		return err
	})
	return out, err
}

// SaveSettings validates and stores a project's whole settings (14.2). The guard, when there is one,
// is asked with the settings as they are and as they would be, while the project's row is held.
func (b *Board) SaveSettings(ctx context.Context, p *Project, d settings.Doc, who string, guard settings.Guard) error {
	return b.inTx(ctx, func(tx pgx.Tx) error {
		if _, err := projectByRef(ctx, tx, p.ID, true); err != nil {
			return err
		}
		return b.saveSettings(ctx, tx, p, d, who, guard)
	})
}

// PutBackSettings puts a key of the project's settings back to its value before change seq (14.2),
// saved and guarded as every change of them is: a limit it raises is recorded the same way.
func (b *Board) PutBackSettings(ctx context.Context, p *Project, seq int64, key, who string, guard settings.Guard) error {
	return b.inTx(ctx, func(tx pgx.Tx) error {
		if _, err := projectByRef(ctx, tx, p.ID, true); err != nil {
			return err
		}
		d, err := settings.ProjectPutBack(ctx, tx, p.ID, p.Name, p.Key, seq, key)
		if err != nil {
			return err
		}
		return b.saveSettings(ctx, tx, p, d, who, guard)
	})
}

// saveSettings stores the project's settings in tx, which holds the project's row, and records the
// limits the change raised (7.7).
func (b *Board) saveSettings(ctx context.Context, tx pgx.Tx, p *Project, d settings.Doc, who string, guard settings.Guard) error {
	before, err := b.Settings(ctx, tx, p)
	if err != nil {
		return err
	}
	if guard != nil {
		if err := guard(before, d); err != nil {
			return err
		}
	}
	if err := settings.SaveProject(ctx, tx, p.ID, p.Name, p.Key, d, who); err != nil {
		return err
	}
	after, err := b.Settings(ctx, tx, p)
	if err != nil {
		return err
	}
	for _, r := range raisedLimits(before, after) {
		if err := intervention(ctx, tx, p.ID, 0, LimitRaised, limitText(r), r); err != nil {
			return err
		}
	}
	return nil
}

// OwnStage is a stage a project adds to its board (7.4, R101), placed after the stage After.
type OwnStage struct {
	ID     string   `json:"id"`
	Title  string   `json:"title"`
	Role   string   `json:"role"`
	Agent  string   `json:"agent"`
	Prompt string   `json:"prompt"`
	WIP    int      `json:"wip"`
	Exit   []string `json:"exit"`
	On     *bool    `json:"on"`
	After  string   `json:"after"`
}

// SetOwnStage adds a project's own stage, or replaces the one with its id, with its row in changes.
// Validation of the whole stages block decides what is allowed: between In progress and Done, an
// agent defined on main, a prompt, and an exit list of checks Crew evaluates.
func (b *Board) SetOwnStage(ctx context.Context, p *Project, s OwnStage, who string) error {
	if slices.Contains(settings.OptionalStages, s.ID) {
		return errf(Refused, "%s is a stage of Crew's own: switch it on or off instead", s.ID)
	}
	return b.inTx(ctx, func(tx pgx.Tx) error {
		if _, err := projectByRef(ctx, tx, p.ID, true); err != nil {
			return err
		}
		doc, err := b.Settings(ctx, tx, p)
		if err != nil {
			return err
		}
		on := true
		if s.On != nil {
			on = *s.On
		}
		exit := make([]any, len(s.Exit))
		for i, e := range s.Exit {
			exit[i] = e
		}
		entry := settings.Doc{"id": s.ID, "optional": true, "on": on, "role": s.Role, "wip": s.WIP, "agent": s.Agent, "prompt": s.Prompt, "exit": exit}
		if s.Title != "" {
			entry["title"] = s.Title
		}
		list, _ := doc["stages"].([]any)
		var out []any
		placed := false
		for _, e := range list {
			m, _ := e.(settings.Doc)
			if m["id"] == s.ID {
				continue
			}
			out = append(out, e)
			if m["id"] == s.After {
				out = append(out, entry)
				placed = true
			}
		}
		if !placed {
			return errf(Invalid, "after: %q is not a stage of %s to put it after", s.After, p.Name)
		}
		n := settings.Doc{}
		for k, v := range doc {
			n[k] = v
		}
		n["stages"] = out
		return settings.SaveProject(ctx, tx, p.ID, p.Name, p.Key, n, who)
	})
}

// RemoveOwnStage takes a project's own stage off its board, with its row in changes. A stage a ticket
// is in stays until the ticket left it.
func (b *Board) RemoveOwnStage(ctx context.Context, p *Project, id, who string) error {
	if slices.Contains(settings.OptionalStages, id) {
		return errf(Refused, "%s is a stage of Crew's own: switch it off instead", id)
	}
	return b.inTx(ctx, func(tx pgx.Tx) error {
		if _, err := projectByRef(ctx, tx, p.ID, true); err != nil {
			return err
		}
		var n int
		if err := tx.QueryRow(ctx, `SELECT count(*) FROM tickets WHERE project = $1 AND stage = $2`, p.ID, id).Scan(&n); err != nil {
			return err
		}
		if n > 0 {
			return errf(Refused, "%d tickets are in %s: switch it off, and remove it once they left it", n, id)
		}
		doc, err := b.Settings(ctx, tx, p)
		if err != nil {
			return err
		}
		list, _ := doc["stages"].([]any)
		var out []any
		found := false
		for _, e := range list {
			if m, _ := e.(settings.Doc); m["id"] == id {
				found = true
				continue
			}
			out = append(out, e)
		}
		if !found {
			return errf(NotFound, "%s has no stage %s", p.Name, id)
		}
		c := settings.Doc{}
		for k, v := range doc {
			c[k] = v
		}
		c["stages"] = out
		return settings.SaveProject(ctx, tx, p.ID, p.Name, p.Key, c, who)
	})
}

// SetNetwork sets a project's network mode, `sandbox.network`, with its row in changes (13.6). It
// applies to the sandboxes that start after it. The guard is asked as SaveSettings asks it.
func (b *Board) SetNetwork(ctx context.Context, p *Project, mode, who string, guard settings.Guard) error {
	if mode != "inherit" && !slices.Contains(config.Modes, mode) {
		return errf(Invalid, "mode: inherit, internet, model_only, or lan")
	}
	return b.inTx(ctx, func(tx pgx.Tx) error {
		if _, err := projectByRef(ctx, tx, p.ID, true); err != nil {
			return err
		}
		doc, err := b.Settings(ctx, tx, p)
		if err != nil {
			return err
		}
		sb, _ := doc["sandbox"].(settings.Doc)
		if sb == nil {
			sb = settings.Doc{}
			doc["sandbox"] = sb
		}
		// A second read is the settings as they are, which the change below does not touch.
		before, err := b.Settings(ctx, tx, p)
		if err != nil {
			return err
		}
		sb["network"] = mode
		if guard != nil {
			if err := guard(before, doc); err != nil {
				return err
			}
		}
		return settings.SaveProject(ctx, tx, p.ID, p.Name, p.Key, doc, who)
	})
}

// SetStages switches optional stages on or off (7.4). A change applies to the tickets that have not
// reached that stage yet: a ticket's next stage is read at each move.
func (b *Board) SetStages(ctx context.Context, p *Project, on map[string]bool, who string) error {
	return b.inTx(ctx, func(tx pgx.Tx) error {
		if _, err := projectByRef(ctx, tx, p.ID, true); err != nil {
			return err
		}
		doc, err := b.Settings(ctx, tx, p)
		if err != nil {
			return err
		}
		if doc, err = settings.SetStagesOn(doc, on); err != nil {
			return err
		}
		return settings.SaveProject(ctx, tx, p.ID, p.Name, p.Key, doc, who)
	})
}
