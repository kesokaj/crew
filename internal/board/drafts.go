package board

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"
	"go.yaml.in/yaml/v3"

	"github.com/kesokaj/crew/internal/db"
	"github.com/kesokaj/crew/internal/github"
	"github.com/kesokaj/crew/internal/settings"
)

// GitHub is what the board needs of GitHub (6.6): a new repository under the gh login's account,
// and the environment of a git command that reaches GitHub over SSH with the key from Setup.
type GitHub interface {
	CreateRepo(ctx context.Context, name, description string) (github.Repo, error)
	SSHEnv(ctx context.Context) (env []string, done func(), err error)
}

// fromDraft fills a new project from its plan draft: the newest version of its plan is the original
// plan, and the plan's front matter gives the optional stages, and its Goal section
// the goal, where the request gives none. It returns the version taken, which takeDraft checks is
// still the newest.
func (b *Board) fromDraft(ctx context.Context, n *NewProject) (int, error) {
	if strings.TrimSpace(n.Plan) != "" {
		return 0, errf(Invalid, "plan: a project made from a draft takes its plan from the draft")
	}
	var state string
	var number *int
	var text *string
	err := b.Pool.QueryRow(ctx, `SELECT d.state, v.number, v.text FROM plan_drafts d
		LEFT JOIN LATERAL (SELECT number, text FROM plan_draft_versions WHERE draft = d.id ORDER BY number DESC LIMIT 1) v ON true
		WHERE d.id::text = $1`, n.Draft).Scan(&state, &number, &text)
	if errors.Is(err, pgx.ErrNoRows) {
		return 0, errf(NotFound, "no plan draft %s", n.Draft)
	}
	if err != nil {
		return 0, err
	}
	if state != "open" {
		return 0, errf(Refused, "the plan draft is %s: a draft becomes one project", state)
	}
	if number == nil {
		return 0, errf(Refused, "the plan draft has no plan yet: the planning agent writes it first")
	}
	n.Plan = *text
	// The plan is the planning agent's text, which is data (AGENTS.md, rule 14): its front matter may
	// switch optional stages on beside the defaults, never off. The operator's request decides them
	// (security review of M5; since 0.53 a plan has no budget).
	if fm := planFrontMatter(*text); n.Stages == nil && len(fm.Stages) > 0 {
		n.Stages = append([]string{}, settings.DefaultStagesOn()...)
		for _, st := range fm.Stages {
			if slices.Contains(settings.OptionalStages, st) && !slices.Contains(n.Stages, st) {
				n.Stages = append(n.Stages, st)
			}
		}
	}
	if strings.TrimSpace(n.Goal) == "" {
		n.Goal = section(*text, "Goal", 2000)
	}
	return *number, nil
}

// frontMatter is what the board reads of a plan's front matter (6.3).
type frontMatter struct {
	Title  string   `yaml:"title"`
	Stages []string `yaml:"stages"`
}

// planFrontMatter reads a plan's front matter; a plan without one, or with one that does not read,
// gives nothing.
func planFrontMatter(text string) frontMatter {
	var fm frontMatter
	rest, ok := strings.CutPrefix(strings.TrimLeft(text, "\n"), "---\n")
	if !ok {
		return fm
	}
	head, _, ok := strings.Cut(rest, "\n---")
	if !ok || yaml.Unmarshal([]byte(head), &fm) != nil {
		return frontMatter{}
	}
	return fm
}

// section is the text under a heading of the second level, up to the next heading, on one line.
func section(text, heading string, max int) string {
	var b []string
	in := false
	for _, line := range strings.Split(text, "\n") {
		t := strings.TrimSpace(line)
		if strings.HasPrefix(t, "#") {
			if in {
				break
			}
			in = strings.TrimSpace(strings.TrimLeft(t, "#")) == heading && strings.HasPrefix(t, "## ")
			continue
		}
		if in && t != "" {
			b = append(b, t)
		}
	}
	return strings.TrimSpace(cutText(strings.Join(b, " "), max))
}

// takeDraft makes the draft the project's, in the project's transaction: it is still open and its
// newest version is the one the plan was taken from, else nothing is made. Its runs and their
// conversations belong to the project from now on, so their tokens are the project's (7.2).
func takeDraft(ctx context.Context, tx pgx.Tx, draft string, version int, project, who string) error {
	var state string
	var newest int
	err := tx.QueryRow(ctx, `SELECT state, coalesce((SELECT max(number) FROM plan_draft_versions WHERE draft = d.id), 0)
		FROM plan_drafts d WHERE id::text = $1 FOR UPDATE`, draft).Scan(&state, &newest)
	if err != nil {
		return err
	}
	if state != "open" || newest != version {
		return errf(Conflict, "the plan draft changed meanwhile: look at its newest version, and create the project again")
	}
	for _, q := range []string{
		`UPDATE plan_drafts SET state = 'project', project = $2, updated_at = now() WHERE id::text = $1`,
		`UPDATE projects SET draft = $1::uuid WHERE id = $2`,
		`UPDATE subtasks SET project = $2 WHERE draft::text = $1`,
		`UPDATE conversations SET project = $2 WHERE draft::text = $1`,
	} {
		if _, err := tx.Exec(ctx, q, draft, project); err != nil {
			return err
		}
	}
	return db.RecordProjectChange(ctx, tx, project, who, "plan_drafts", draft, map[string]any{"state": "open"},
		map[string]any{"state": "project", "project": project, "version": version})
}

// PushPending pushes what waits to be pushed to the remotes, of one project or of all when project
// is "" (21: "The remote cannot be reached, or rejects a push"). A push is claimed before it runs, so
// two loops never push the same one; one that fails waits longer each time, and nothing is forced.
func (b *Board) PushPending(ctx context.Context, project string) {
	for i := 0; i < 20; i++ {
		var id int64
		var name, remote, source, commit string
		var refs []string
		var tries int
		err := b.Pool.QueryRow(ctx, `UPDATE pushes u SET tries = u.tries + 1, not_before = now() + interval '10 minutes'
			FROM projects p
			WHERE u.id = (SELECT id FROM pushes WHERE state = 'pending' AND not_before <= now()
					AND ($1 = '' OR project::text = $1) ORDER BY id LIMIT 1 FOR UPDATE SKIP LOCKED)
				AND p.id = u.project
			RETURNING u.id, p.name, u.remote, u.refs, u.tries, u.source, coalesce(u.commit, '')`, project).Scan(&id, &name, &remote, &refs, &tries, &source, &commit)
		if errors.Is(err, pgx.ErrNoRows) {
			return
		}
		if err != nil {
			b.log().Error("reading the pushes that wait", "error", err)
			return
		}
		detail, done := "pushed", true
		switch perr := b.pushOf(ctx, name, remote, source, commit, refs); {
		case errors.Is(perr, errNoCommit):
			// From before a push named its commit (schema step 35): the branch's next head is pushed.
			detail = "dropped: it named no commit"
		case perr != nil:
			// What git and the remote printed stays in the server's log (M4); the row says what kind
			// of failure it was.
			detail, done = pushFailure(perr), false
			b.log().Warn("a push to the remote failed; it is tried again", "project", name, "remote", remote, "tries", tries, "error", perr)
		default:
			b.log().Info("pushed to the remote", "project", name, "remote", remote, "refs", refs)
		}
		wait := time.Minute << min(tries, 6)
		if _, err := b.Pool.Exec(ctx, `UPDATE pushes SET state = CASE WHEN $2 THEN 'done' ELSE 'pending' END, detail = $3,
				done_at = CASE WHEN $2 THEN now() END, not_before = now() + $4::interval WHERE id = $1`,
			id, done, cutText(detail, 2000), fmt.Sprintf("%d seconds", int(wait.Seconds()))); err != nil {
			b.log().Error("recording a push", "error", err)
		}
	}
}

func (b *Board) pushTimeout() time.Duration {
	if b.PushTimeout > 0 {
		return b.PushTimeout
	}
	return 2 * time.Minute
}

// pushFailure says what kind of failure a push was, in Crew's own words.
func pushFailure(err error) string {
	e := err.Error()
	switch {
	case errors.Is(err, context.DeadlineExceeded) || strings.Contains(e, "signal: killed") || strings.Contains(e, "timed out"):
		return "the remote did not answer in time"
	case strings.Contains(e, "[rejected]") || strings.Contains(e, "non-fast-forward") || strings.Contains(e, "fetch first"):
		return "the remote refused it: its main went another way, and Crew does not force a push"
	case strings.Contains(e, "Permission denied") || strings.Contains(e, "publickey"):
		return "the remote refused the SSH key: add its public half to the GitHub account in Setup"
	case strings.Contains(e, "not found") || strings.Contains(e, "does not exist"):
		return "the remote has no such repository"
	case strings.Contains(e, "Could not resolve") || strings.Contains(e, "Connection refused") || strings.Contains(e, "Network is unreachable"):
		return "the remote cannot be reached"
	}
	return "the push failed; the server's log says why"
}

// errNoCommit is a push of a ticket's branch that names no commit: it is dropped, not tried again.
var errNoCommit = errors.New("a push of a ticket's branch names no commit")

// pushOf runs one push, or says it names no commit.
func (b *Board) pushOf(ctx context.Context, name, remote, source, commit string, refs []string) error {
	if source == "quarantine" && commit == "" {
		return errNoCommit
	}
	return b.push(ctx, name, remote, source, commit, refs)
}

func (b *Board) push(ctx context.Context, name, remote, source, commit string, refs []string) error {
	// A push that stalls ends, so it is tried again later and holds nothing up (21).
	ctx, cancel := context.WithTimeout(ctx, b.pushTimeout())
	defer cancel()
	var env []string
	if github.SSHURL.MatchString(remote) {
		if b.GitHub == nil {
			return errors.New("GitHub is not set up on this server")
		}
		e, done, err := b.GitHub.SSHEnv(ctx)
		if err != nil {
			return err
		}
		defer done()
		env = e
	}
	if source == "quarantine" {
		// A ticket's branch goes out at the commit that was checked, and only that (review of M10).
		if commit == "" || len(refs) != 1 {
			return errors.New("a push of a ticket's branch names its one branch and the commit that was checked")
		}
		return b.Git.PushCommit(ctx, name, remote, commit, refs[0], env)
	}
	return b.Git.Push(ctx, name, remote, refs, env)
}

// cutText cuts s to at most n bytes, at the start of a character, so what is stored is UTF-8.
func cutText(s string, n int) string {
	if len(s) <= n {
		return s
	}
	for n > 0 && !utf8.RuneStart(s[n]) {
		n--
	}
	return s[:n]
}

// Pushes are the pushes of a project to its remote, newest first.
type Push struct {
	Remote    string     `json:"remote"`
	Refs      []string   `json:"refs"`
	State     string     `json:"state"`
	Tries     int        `json:"tries"`
	Detail    string     `json:"detail"`
	CreatedAt time.Time  `json:"createdAt"`
	DoneAt    *time.Time `json:"doneAt"`
}

// Pushes lists a project's pushes, newest first.
func (b *Board) Pushes(ctx context.Context, p *Project) ([]Push, error) {
	rows, err := b.Pool.Query(ctx, `SELECT remote, refs, state, tries, detail, created_at, done_at FROM pushes
		WHERE project = $1 ORDER BY id DESC LIMIT 50`, p.ID)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(r pgx.CollectableRow) (Push, error) {
		var x Push
		err := r.Scan(&x.Remote, &x.Refs, &x.State, &x.Tries, &x.Detail, &x.CreatedAt, &x.DoneAt)
		return x, err
	})
}
