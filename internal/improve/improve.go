package improve

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/kesokaj/crew/internal/board"
	"github.com/kesokaj/crew/internal/measure"
	"github.com/kesokaj/crew/internal/settings"
)

// The states of an improvement (10.5).
const (
	Proposed   = "proposed"
	Refused    = "refused"
	Testing    = "testing"
	Passed     = "passed"
	Applied    = "applied"
	Removed    = "removed"
	RolledBack = "rolled_back"
)

// Who a passed or proposed improvement waits for.
const (
	WaitsOperator    = "operator"
	WaitsCrewProject = "crew_project"
	WaitsCases       = "cases"
)

// Improvement is a proposed change to the baseline and what became of it (10.5).
type Improvement struct {
	ID          int64             `json:"-"`
	Number      int               `json:"number"`
	Title       string            `json:"title"`
	Status      string            `json:"status"`
	WaitsFor    *string           `json:"waitsFor"`
	Target      []string          `json:"target"`
	Patch       map[string]string `json:"patch"`
	Evidence    json.RawMessage   `json:"evidence"`
	Body        string            `json:"body"`
	Expected    string            `json:"expected"`
	Measure     string            `json:"measure"`
	RefusedWhy  *string           `json:"refusedWhy"`
	Evals       json.RawMessage   `json:"evals"`
	DecidedBy   *string           `json:"decidedBy"`
	Ticket      *string           `json:"ticket"`
	ReleaseFrom *string           `json:"releaseFrom"`
	ReleaseTo   *string           `json:"releaseTo"`
	Result      json.RawMessage   `json:"result"`
	CreatedAt   time.Time         `json:"createdAt"`
	DecidedAt   *time.Time        `json:"decidedAt"`
	AppliedAt   *time.Time        `json:"appliedAt"`
	MeasuredAt  *time.Time        `json:"measuredAt"`
	EndedAt     *time.Time        `json:"endedAt"`
	Examples    json.RawMessage   `json:"examples"`
}

// Proposal is what the improver proposes: the evidence, whole new texts of files, the expected effect,
// and the metric that will show it.
type Proposal struct {
	Title    string            `json:"title"`
	Patch    map[string]string `json:"patch"`
	Evidence Evidence          `json:"evidence"`
	Expected string            `json:"expected"`
	Measure  string            `json:"measure"`
	Body     string            `json:"body"`
}

// Evidence is what the signals show. It stays in the improvement's row, which never enters a project.
type Evidence struct {
	Signal   string   `json:"signal"`
	Window   string   `json:"window"`
	Projects string   `json:"projects"`
	Tasks    string   `json:"tasks"`
	Tickets  []string `json:"tickets"`
	Text     string   `json:"text"`
}

// Service is the Crew level's side of improvements.
type Service struct {
	Pool  *pgxpool.Pool
	Board *board.Board
	// Settings are the machine's settings as they apply.
	Settings func(ctx context.Context) (settings.Doc, error)
	// Running is the Crew version that runs now.
	Running string
	// Rollback asks the updater for the release before, by Crew itself (10.5, step 7); nil when there
	// is no updater, and then a worse result becomes a revert ticket.
	Rollback func(ctx context.Context, version string) error
}

const cols = `i.id, i.number, i.title, i.status, i.waits_for, i.target, i.patch, i.evidence, i.body, i.expected, i.measure, i.refused_why,
	i.evals, i.decided_by, CASE WHEN t.id IS NULL THEN NULL ELSE p.key || '-' || t.number END, i.release_from, i.release_to, i.result,
	i.created_at, i.decided_at, i.applied_at, i.measured_at, i.ended_at, i.examples`

const from = ` FROM improvements i LEFT JOIN tickets t ON t.id = i.ticket LEFT JOIN projects p ON p.id = t.project`

func scan(row pgx.Row) (*Improvement, error) {
	var x Improvement
	var patch []byte
	if err := row.Scan(&x.ID, &x.Number, &x.Title, &x.Status, &x.WaitsFor, &x.Target, &patch, &x.Evidence, &x.Body, &x.Expected, &x.Measure,
		&x.RefusedWhy, &x.Evals, &x.DecidedBy, &x.Ticket, &x.ReleaseFrom, &x.ReleaseTo, &x.Result, &x.CreatedAt, &x.DecidedAt, &x.AppliedAt,
		&x.MeasuredAt, &x.EndedAt, &x.Examples); err != nil {
		return nil, err
	}
	if err := json.Unmarshal(patch, &x.Patch); err != nil {
		return nil, err
	}
	return &x, nil
}

// List is every improvement, newest first.
func (s *Service) List(ctx context.Context) ([]*Improvement, error) {
	rows, err := s.Pool.Query(ctx, `SELECT `+cols+from+` ORDER BY i.number DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []*Improvement{}
	for rows.Next() {
		x, err := scan(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, x)
	}
	return out, rows.Err()
}

// ErrNotFound is an improvement that is not there.
var ErrNotFound = errors.New("no such improvement")

// Get is one improvement by its number.
func (s *Service) Get(ctx context.Context, number int) (*Improvement, error) {
	x, err := scan(s.Pool.QueryRow(ctx, `SELECT `+cols+from+` WHERE i.number = $1`, number))
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	return x, err
}

// Refusal is a proposal Crew refuses, in words for the improver.
type Refusal struct{ Msg string }

func (r *Refusal) Error() string { return r.Msg }

// Propose saves the improver's proposal and checks it (10.5, steps 1 and 2): what it may touch, and the
// generic check. One that passes goes to the evals, in testing; one of the prompt writer's rules waits
// for the operator, since no eval measures a rewrite (7.2).
func (s *Service) Propose(ctx context.Context, run string, p Proposal) (*Improvement, error) {
	p.Title = strings.TrimSpace(p.Title)
	if p.Title == "" || len(p.Title) > 200 || strings.ContainsAny(p.Title, "\r\n") {
		return nil, &Refusal{"title: one line of at most 200 characters"}
	}
	if _, ok := measure.MetricOf(p.Measure); !ok {
		var names []string
		for _, m := range measure.Metrics {
			names = append(names, m.Name)
		}
		return nil, &Refusal{"measure: one of " + strings.Join(names, ", ")}
	}
	if len(p.Patch) == 0 || len(p.Patch) > 5 {
		return nil, &Refusal{"patch: the whole new text of one file, or of up to five that make one change"}
	}
	if len(p.Body) > 20000 || len(p.Expected) > 2000 || len(p.Evidence.Text) > 20000 || len(p.Evidence.Tickets) > 200 {
		return nil, &Refusal{"the evidence, the body, and the expected effect are too long"}
	}
	// What the improver read holds what agents read anywhere: a secret in it would reach Crew's repository
	// and every machine its releases go to (security review of M15).
	texts := []string{p.Title, p.Body, p.Expected, p.Evidence.Text}
	for _, t := range p.Patch {
		texts = append(texts, t)
	}
	if board.HoldsSecret(nil, texts...) {
		return nil, &Refusal{"the proposal looks like it holds a secret: leave the secret out"}
	}
	var target []string
	waits := false
	for path, next := range p.Patch {
		w, err := Allowed(path, next)
		if err != nil {
			return nil, &Refusal{err.Error()}
		}
		waits = waits || w
		target = append(target, path)
	}
	sort.Strings(target)
	ev, _ := json.Marshal(p.Evidence)
	patch, _ := json.Marshal(p.Patch)
	var id int64
	if err := s.Pool.QueryRow(ctx, `INSERT INTO improvements (number, title, target, patch, evidence, body, expected, measure, proposed_run)
		VALUES ((SELECT coalesce(max(number), 0) + 1 FROM improvements), $1, $2, $3, $4, $5, $6, $7, nullif($8, '')::uuid) RETURNING id`,
		p.Title, target, patch, ev, p.Body, p.Expected, p.Measure, run).Scan(&id); err != nil {
		return nil, err
	}
	status, why, waitsFor := Testing, "", ""
	if err := Generic(ctx, s.Pool, s.Board.Git, p.Patch, p.Evidence.Tickets); err != nil {
		status, why = Refused, "the generic check: "+err.Error()
	} else if waits {
		status, waitsFor = Proposed, WaitsOperator
	}
	if _, err := s.Pool.Exec(ctx, `UPDATE improvements SET status = $2, refused_why = nullif($3, ''), waits_for = nullif($4, ''),
		decided_by = CASE WHEN $2 = 'refused' THEN 'generic check' END, decided_at = CASE WHEN $2 = 'refused' THEN now() END,
		ended_at = CASE WHEN $2 = 'refused' THEN now() END WHERE id = $1`, id, status, why, waitsFor); err != nil {
		return nil, err
	}
	return s.byID(ctx, id)
}

func (s *Service) byID(ctx context.Context, id int64) (*Improvement, error) {
	return scan(s.Pool.QueryRow(ctx, `SELECT `+cols+from+` WHERE i.id = $1`, id))
}

// minCases is how many cases across all projects an improvement needs before its evals can tell (R167):
// the projects' default of knowledge.evals.min_cases.
func minCases() int {
	d, err := settings.ProjectDefaults("crew", "CREW")
	if err != nil {
		return 10
	}
	if ev, ok := d["knowledge"].(settings.Doc); ok {
		if e, ok := ev["evals"].(settings.Doc); ok {
			if n, ok := e["min_cases"].(int); ok && n > 0 {
				return n
			}
		}
	}
	return 10
}

// Step moves the improvements on (10.5): an improvement in testing gets a suite in every project with
// cases, and is decided when they have all ended; one that waits for more cases goes on once there are
// enough; a passed one that waits for Crew's project gets its ticket once there is one.
func (s *Service) Step(ctx context.Context) error {
	list, err := s.List(ctx)
	if err != nil {
		return err
	}
	for _, x := range list {
		switch {
		case x.Status == Testing:
			if err := s.evals(ctx, x); err != nil {
				return err
			}
		case x.Status == Passed && x.WaitsFor != nil && *x.WaitsFor == WaitsCrewProject:
			if err := s.ticket(ctx, x); err != nil {
				return err
			}
		case x.Status == Passed && x.Ticket != nil:
			if err := s.applied(ctx, x); err != nil {
				return err
			}
		}
	}
	return nil
}

// evals runs an improvement's evals inside each project that has cases, and decides when all have
// ended. Only the numbers come back (10.5, step 3).
func (s *Service) evals(ctx context.Context, x *Improvement) error {
	rows, err := s.Pool.Query(ctx, `SELECT p.id::text, count(c.id) FROM projects p JOIN eval_cases c ON c.project = p.id AND c.status = 'active'
		WHERE p.state <> 'archived' GROUP BY p.id ORDER BY p.id`)
	if err != nil {
		return err
	}
	type pc struct {
		project string
		cases   int
	}
	var projects []pc
	total := 0
	for rows.Next() {
		var p pc
		if err := rows.Scan(&p.project, &p.cases); err != nil {
			rows.Close()
			return err
		}
		projects = append(projects, p)
		total += p.cases
	}
	rows.Close()
	if total < minCases() {
		// Nothing ships on no evidence: it waits in testing until there are more (10.5).
		_, err := s.Pool.Exec(ctx, `UPDATE improvements SET waits_for = 'cases' WHERE id = $1 AND waits_for IS DISTINCT FROM 'cases'`, x.ID)
		return err
	}
	if _, err := s.Pool.Exec(ctx, `UPDATE improvements SET waits_for = NULL WHERE id = $1 AND waits_for = 'cases'`, x.ID); err != nil {
		return err
	}
	pending := false
	for _, p := range projects {
		var state string
		err := s.Pool.QueryRow(ctx, `SELECT state FROM eval_suites WHERE project = $1 AND improvement = $2 ORDER BY id DESC LIMIT 1`, p.project, x.ID).Scan(&state)
		switch {
		case errors.Is(err, pgx.ErrNoRows):
			proj, err := s.Board.Project(ctx, p.project)
			if err != nil {
				return err
			}
			if _, err := s.Board.StartSuite(ctx, proj, "improvement", 0, x.ID, "crew"); err != nil {
				var be *board.Error
				if !errors.As(err, &be) {
					return err
				}
			}
			pending = true
		case err != nil:
			return err
		case state == "queued" || state == "running":
			pending = true
		}
	}
	if pending {
		return nil
	}
	return s.decide(ctx, x)
}

// RecordEvals keeps the numbers one project's suite gave an improvement: each side's goals met by case.
func (s *Service) RecordEvals(ctx context.Context, improvement int64, project string, suite int64, baseline, change measure.Side) error {
	res, _ := json.Marshal(map[string]measure.Side{"baseline": baseline, "change": change})
	_, err := s.Pool.Exec(ctx, `INSERT INTO improvement_evals (improvement, project, suite, cases, result) VALUES ($1, $2, $3, $4, $5)
		ON CONFLICT (improvement, project) DO UPDATE SET suite = EXCLUDED.suite, cases = EXCLUDED.cases, result = EXCLUDED.result, at = now()`,
		improvement, project, suite, baseline.Cases(), res)
	return err
}

// decide decides an improvement from the evals of every project (10.5, step 4): not worse lets it go
// ahead, worse refuses it, and nobody has to approve; too few cases, or runs of the change that
// measured nothing, which counted as goals not met would make it worse, leave it in testing (since
// 1.10). Too few cases wait for more, which start its suites; runs that measured nothing wait until
// the operator removes it, since nothing runs its suites again.
func (s *Service) decide(ctx context.Context, x *Improvement) error {
	rows, err := s.Pool.Query(ctx, `SELECT result FROM improvement_evals WHERE improvement = $1`, x.ID)
	if err != nil {
		return err
	}
	all := map[string]measure.Side{"baseline": {Runs: map[int64][]bool{}}, "change": {Runs: map[int64][]bool{}}}
	projects := 0
	for rows.Next() {
		var raw []byte
		if err := rows.Scan(&raw); err != nil {
			rows.Close()
			return err
		}
		var sides map[string]measure.Side
		if err := json.Unmarshal(raw, &sides); err != nil {
			rows.Close()
			return err
		}
		projects++
		for name, side := range sides {
			all[name] = all[name].With(side)
		}
	}
	rows.Close()
	doc, err := s.Settings(ctx)
	if err != nil {
		return err
	}
	d := measure.NotWorse(all["baseline"], all["change"], minCases())
	evals, _ := json.Marshal(map[string]any{"projects": projects, "cases": d.Cases, "passedBefore": d.PassedBefore, "passedAfter": d.PassedAfter,
		"margin": d.Margin, "lost": len(d.Lost), "verdict": d.Verdict, "why": d.Why, "unmeasuredRuns": d.UnmeasuredRuns})
	switch d.Verdict {
	case measure.TooFew:
		_, err := s.Pool.Exec(ctx, `UPDATE improvements SET evals = $2, waits_for = 'cases' WHERE id = $1`, x.ID, evals)
		return err
	case measure.WorseVerdict:
		_, err := s.Pool.Exec(ctx, `UPDATE improvements SET evals = $2, status = 'refused', refused_why = $3, decided_by = 'evals', decided_at = now(),
			ended_at = now() WHERE id = $1`, x.ID, evals, "the evals were worse: "+d.Why)
		return err
	}
	waits := ""
	if str(doc, "improvements.approval") == "operator" {
		waits = WaitsOperator
	}
	if _, err := s.Pool.Exec(ctx, `UPDATE improvements SET evals = $2, status = 'passed', decided_by = 'evals', decided_at = now(), waits_for = nullif($3, '')
		WHERE id = $1`, x.ID, evals, waits); err != nil {
		return err
	}
	if waits != "" {
		return nil
	}
	x, err = s.byID(ctx, x.ID)
	if err != nil {
		return err
	}
	return s.ticket(ctx, x)
}

func str(d settings.Doc, path string) string {
	cur := any(d)
	for _, k := range strings.Split(path, ".") {
		m, ok := cur.(settings.Doc)
		if !ok {
			return ""
		}
		cur = m[k]
	}
	v, _ := cur.(string)
	return v
}

// ticket makes the ticket on Crew's own project that carries a passed improvement out (10.5, step 5;
// 16.2). The ticket holds the change, never the evidence. With no Crew project, it waits for one.
func (s *Service) ticket(ctx context.Context, x *Improvement) error {
	doc, err := s.Settings(ctx)
	if err != nil {
		return err
	}
	name := str(doc, "improvements.crew_project")
	if name == "" {
		_, err := s.Pool.Exec(ctx, `UPDATE improvements SET waits_for = 'crew_project' WHERE id = $1`, x.ID)
		return err
	}
	p, err := s.Board.Project(ctx, name)
	if err != nil {
		_, err := s.Pool.Exec(ctx, `UPDATE improvements SET waits_for = 'crew_project' WHERE id = $1`, x.ID)
		return err
	}
	var checks []board.CheckSpec
	var files []string
	for _, path := range x.Target {
		// The path goes into a command: only the shape a file of the baseline has (security review of M15).
		if !safePath.MatchString(path) {
			return fmt.Errorf("i_%04d names %q, which is no path a check command takes", x.Number, path)
		}
		sum := sha256.Sum256([]byte(x.Patch[path]))
		checks = append(checks, board.CheckSpec{Kind: "cmd", Spec: fmt.Sprintf("echo '%s  %s' | sha256sum -c -", hex.EncodeToString(sum[:]), path)})
		files = append(files, path)
	}
	reason := "The files it changes are the baseline's own: no page describes them."
	t, err := s.Board.AddTicket(ctx, p.Name, board.NewTicket{
		Title:      cut(fmt.Sprintf("Improvement i_%04d: %s", x.Number, x.Title), 200),
		Kind:       "chore",
		Why:        cut(fmt.Sprintf("Crew's improvement i_%04d of the baseline passed its evals in every project that has cases. Expected: %s", x.Number, x.Expected), 2000),
		What:       cut("Write the new text of "+strings.Join(files, ", ")+" exactly as Crew's note on this ticket gives it, and change nothing else.", 2000),
		Goal:       "The files hold the improvement's text, byte for byte.",
		Checks:     checks,
		DocsReason: &reason,
	}, board.OriginAgent, "improver")
	if err != nil {
		return err
	}
	var b strings.Builder
	fmt.Fprintf(&b, "The new text of each file of improvement i_%04d, from the line after its name to the line before the next.\n", x.Number)
	for _, path := range x.Target {
		fmt.Fprintf(&b, "\n===== %s\n%s", path, x.Patch[path])
	}
	var tid int64
	if err := s.Pool.QueryRow(ctx, `SELECT id FROM tickets WHERE project = $1 AND number = $2`, p.ID, t.Number).Scan(&tid); err != nil {
		return err
	}
	if _, err := s.Pool.Exec(ctx, `INSERT INTO records (project, ticket, kind, author, body) VALUES ($1, $2, 'note', 'improver', $3)`, p.ID, tid, b.String()); err != nil {
		return err
	}
	_, err = s.Pool.Exec(ctx, `UPDATE improvements SET ticket = $2, waits_for = NULL, release_from = coalesce(release_from, $3) WHERE id = $1`, x.ID, tid, s.Running)
	return err
}

// safePath is the shape of a path of the baseline in a check command.
var safePath = regexp.MustCompile(`^baseline/[A-Za-z0-9._/-]+\.md$`)

// applied marks a passed improvement applied once a release that holds its ticket's merge is installed
// (10.5, step 5): the first such release is the version it shipped in, and the one installed before it
// the version it is measured against.
func (s *Service) applied(ctx context.Context, x *Improvement) error {
	doc, err := s.Settings(ctx)
	if err != nil {
		return err
	}
	p, err := s.Board.Project(ctx, str(doc, "improvements.crew_project"))
	if err != nil {
		return nil // no Crew project any more: nothing ships
	}
	t, err := s.Board.Ticket(ctx, p, *x.Ticket)
	if err != nil || t.Stage != "done" {
		return err
	}
	merge, err := board.MergeOf(ctx, s.Pool, t)
	if err != nil || merge == "" {
		return err
	}
	rows, err := s.Pool.Query(ctx, `SELECT version, commit FROM releases WHERE installed_at IS NOT NULL ORDER BY installed_at`)
	if err != nil {
		return err
	}
	type rel struct{ version, commit string }
	var list []rel
	for rows.Next() {
		var r rel
		if err := rows.Scan(&r.version, &r.commit); err != nil {
			rows.Close()
			return err
		}
		list = append(list, r)
	}
	rows.Close()
	for i, r := range list {
		if !s.Board.Git.Follows(ctx, p.Name, merge, r.commit) {
			continue
		}
		from := ""
		if i > 0 {
			from = list[i-1].version
		}
		_, err := s.Pool.Exec(ctx, `UPDATE improvements SET status = 'applied', release_to = $2, release_from = coalesce(nullif($3, ''), release_from), applied_at = now()
			WHERE id = $1 AND status = 'passed'`, x.ID, r.version, from)
		return err
	}
	return nil
}

func cut(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n]
}

// Decide is the operator's answer to an improvement: approve one that waits for them, which goes ahead
// as a passed one does, or take one out that is not in an installed release (10.5, step 4).
func (s *Service) Decide(ctx context.Context, number int, choice, who string) (*Improvement, error) {
	x, err := s.Get(ctx, number)
	if err != nil {
		return nil, err
	}
	waits := x.WaitsFor != nil && *x.WaitsFor == WaitsOperator
	switch {
	case choice == "approve" && waits:
		if _, err := s.Pool.Exec(ctx, `UPDATE improvements SET status = 'passed', waits_for = NULL, decided_by = $2, decided_at = now() WHERE id = $1`, x.ID, who); err != nil {
			return nil, err
		}
		x, err = s.byID(ctx, x.ID)
		if err != nil {
			return nil, err
		}
		if err := s.ticket(ctx, x); err != nil {
			return nil, err
		}
	case choice == "take_out" && slices.Contains([]string{Proposed, Testing, Passed}, x.Status):
		if err := s.remove(ctx, x, who); err != nil {
			return nil, err
		}
	case choice == "approve" || choice == "take_out":
		return nil, &Refusal{fmt.Sprintf("i_%04d is %s", x.Number, x.Status)}
	default:
		return nil, &Refusal{"choice: approve, or take_out"}
	}
	return s.byID(ctx, x.ID)
}

// remove takes an improvement out before it is installed (10.5, 16.3): its ticket on Crew's project is
// closed with it; one that is Done is in Crew's main already, and is taken out by a revert of it (since
// 0.55). It holds the lock the release clock takes, and the improvement's row, and reads its status
// again under them before it changes anything: the clock installs no release with it meanwhile, and
// one installed meanwhile is refused with nothing made (round 2 of the security review of
// 1863a0e..f8d57b2).
func (s *Service) remove(ctx context.Context, x *Improvement, who string) error {
	return pgx.BeginFunc(ctx, s.Pool, func(tx pgx.Tx) error {
		// The same lock as versions' Clock.
		if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtext('crew.releases'))`); err != nil {
			return err
		}
		var status string
		if err := tx.QueryRow(ctx, `SELECT status FROM improvements WHERE id = $1 FOR UPDATE`, x.ID).Scan(&status); err != nil {
			return err
		}
		if !slices.Contains([]string{Proposed, Testing, Passed}, status) {
			return &Refusal{fmt.Sprintf("i_%04d is %s", x.Number, status)}
		}
		if x.Ticket != nil {
			doc, err := s.Settings(ctx)
			if err != nil {
				return err
			}
			p, err := s.Board.Project(ctx, str(doc, "improvements.crew_project"))
			if err != nil {
				return err
			}
			t, err := s.Board.Ticket(ctx, p, *x.Ticket)
			if err != nil {
				return err
			}
			// A ticket closed already, by the operator on the board, holds nothing to take out (round 2
			// of the review of M15).
			if t.Stage == "done" {
				if err := s.revertMerged(ctx, x, p, t); err != nil {
					return err
				}
			} else if t.Stage != "closed" {
				if _, err := s.Board.Discard(ctx, p, *x.Ticket, fmt.Sprintf("improvement i_%04d was taken out by the operator", x.Number), who); err != nil {
					var be *board.Error
					if errors.As(err, &be) {
						return &Refusal{fmt.Sprintf("i_%04d's ticket %s: %s", x.Number, *x.Ticket, be.Msg)}
					}
					return err
				}
			}
		}
		_, err := tx.Exec(ctx, `UPDATE improvements SET status = 'removed', waits_for = NULL, decided_by = $2, decided_at = now(), ended_at = now()
			WHERE id = $1`, x.ID, who)
		return err
	})
}

// revertMerged takes out an improvement whose ticket is Done, in Crew's main and in no installed
// release, with a revert of its ticket, as Crew's own (16.3, since 0.55, the operator's answer 3 of
// report 10): the next build carries the revert, and a release that holds the improvement and not its
// revert waits for Install (versions' HoldTakenOut). One in an installed release is measured there
// (10.5), and is refused. A revert that is open already, from a press that failed after it, is it.
// Refused too, with nothing changed (security review of 1863a0e..f8d57b2): while a release installs,
// which could hold it; a ticket Done with no merge Crew recorded, whose change is not known; and a
// history that cannot be read.
func (s *Service) revertMerged(ctx context.Context, x *Improvement, p *board.Project, t *board.Ticket) error {
	var installing string
	err := s.Pool.QueryRow(ctx, `SELECT version FROM releases WHERE state = 'installing' LIMIT 1`).Scan(&installing)
	if err == nil {
		return &Refusal{fmt.Sprintf("release %s installs now: take i_%04d out once it is done", installing, x.Number)}
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return err
	}
	merge, err := board.MergeOf(ctx, s.Pool, t)
	if err != nil {
		return err
	}
	if merge == "" {
		return &Refusal{fmt.Sprintf("i_%04d's ticket %s is Done with no merge Crew recorded: what it changed in main is not known, so it is not taken out", x.Number, t.Key)}
	}
	rows, err := s.Pool.Query(ctx, `SELECT version, commit FROM releases WHERE installed_at IS NOT NULL ORDER BY installed_at`)
	if err != nil {
		return err
	}
	type rel struct{ version, commit string }
	var list []rel
	for rows.Next() {
		var r rel
		if err := rows.Scan(&r.version, &r.commit); err != nil {
			rows.Close()
			return err
		}
		list = append(list, r)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}
	for _, r := range list {
		// A history that cannot be read refuses: nothing is changed for want of a read.
		has, err := s.Board.Git.IsAncestor(ctx, p.Name, merge, r.commit)
		if err != nil {
			return &Refusal{fmt.Sprintf("Crew could not read whether release %s holds i_%04d, so nothing was changed", r.version, x.Number)}
		}
		if has {
			return &Refusal{fmt.Sprintf("i_%04d is in release %s, which is installed: it is measured there, and taken out by a revert if it measures worse", x.Number, r.version)}
		}
	}
	if _, err := s.Board.Revert(ctx, p, t.Key, "crew"); err != nil {
		var be *board.Error
		if !errors.As(err, &be) {
			return err
		}
		var open int
		if err := s.Pool.QueryRow(ctx, `SELECT count(*) FROM ticket_links l JOIN tickets r ON r.id = l.from_ticket
			WHERE l.to_ticket = (SELECT id FROM tickets WHERE project = $1 AND number = $2) AND l.kind = 'reverts' AND r.stage <> 'closed'`,
			p.ID, t.Number).Scan(&open); err != nil {
			return err
		}
		if open == 0 {
			return &Refusal{fmt.Sprintf("i_%04d's ticket %s: %s", x.Number, t.Key, be.Msg)}
		}
	}
	return nil
}

// Measure measures each applied improvement once enough tickets were done on the version it shipped
// in (10.5, step 6), and takes a worse one out again (step 7): back to the release before when nothing
// was installed since, otherwise with a revert of its ticket on Crew's project, which makes a new
// release without it. n is improvements.measure_after_tasks.
func (s *Service) Measure(ctx context.Context, n int) error {
	list, err := s.List(ctx)
	if err != nil {
		return err
	}
	for _, x := range list {
		if x.Status != Applied || x.MeasuredAt != nil || x.ReleaseFrom == nil || x.ReleaseTo == nil {
			continue
		}
		m, ok := measure.MetricOf(x.Measure)
		if !ok {
			continue
		}
		count := n
		around := measure.AroundVersions
		// A change of the prompt writer's rules is measured by what the operator did with rewrites, and no
		// conclusion is drawn from fewer than ten plans (7.2).
		if m.Name == measure.RewritesUsed {
			count, around = 10, measure.AroundRewrites
		}
		before, after, err := around(ctx, s.Pool, *x.ReleaseFrom, *x.ReleaseTo, count)
		if err != nil {
			return err
		}
		if len(after) < count {
			continue
		}
		var ev struct {
			Margin float64 `json:"margin"`
		}
		json.Unmarshal(x.Evals, &ev)
		r := measure.Compare(m, before, after, count, ev.Margin)
		// A release that also changed code or a protected part could explain the result (10.5).
		var kind string
		var protected []string
		if err := s.Pool.QueryRow(ctx, `SELECT kind, protected FROM releases WHERE version = $1`, *x.ReleaseTo).Scan(&kind, &protected); err == nil &&
			(kind != "text" || len(protected) > 0) && r.Verdict != measure.Inconclusive {
			r.Verdict, r.Why = measure.Inconclusive, r.Why+"; the release also changed code, which could explain it"
		}
		res, _ := json.Marshal(r)
		text := fmt.Sprintf("Improvement i_%04d measured %s: %s", x.Number, r.Verdict, r.Why)
		// A worse one is taken out first and counts as measured only then: a take-out that fails is
		// tried again on the next pass (review of M15).
		if r.Verdict == measure.Worse {
			how, err := s.takeOut(ctx, x)
			if err != nil {
				return fmt.Errorf("taking out i_%04d: %w", x.Number, err)
			}
			text = fmt.Sprintf("Improvement i_%04d measured worse, and Crew took it out %s: %s", x.Number, how, r.Why)
		}
		if _, err := s.Pool.Exec(ctx, `UPDATE improvements SET result = $2, measured_at = now() WHERE id = $1`, x.ID, res); err != nil {
			return err
		}
		if err := board.RecordSignal(ctx, s.Pool, board.Signal{Kind: "improvement", Detail: map[string]any{"improvement": x.Number, "verdict": r.Verdict, "text": text}}); err != nil {
			return err
		}
	}
	return nil
}

// takeOut takes a worse improvement out (10.5, step 7) and says how.
func (s *Service) takeOut(ctx context.Context, x *Improvement) (string, error) {
	var newer int
	if err := s.Pool.QueryRow(ctx, `SELECT count(*) FROM releases r, releases t WHERE t.version = $1 AND r.installed_at > t.installed_at`, *x.ReleaseTo).Scan(&newer); err != nil {
		return "", err
	}
	how := ""
	if s.Rollback != nil && s.Running == *x.ReleaseTo && newer == 0 {
		if err := s.Rollback(ctx, *x.ReleaseFrom); err == nil {
			how = "by going back to " + *x.ReleaseFrom
		}
	}
	if how == "" {
		if x.Ticket == nil {
			return "", errors.New("an improvement to take out has no ticket")
		}
		doc, err := s.Settings(ctx)
		if err != nil {
			return "", err
		}
		p, err := s.Board.Project(ctx, str(doc, "improvements.crew_project"))
		if err != nil {
			return "", err
		}
		r, err := s.Board.Revert(ctx, p, *x.Ticket, "crew")
		if err != nil {
			return "", err
		}
		how = "with " + r.Key + ", which reverts it in the next release"
	}
	_, err := s.Pool.Exec(ctx, `UPDATE improvements SET status = 'rolled_back', ended_at = now() WHERE id = $1`, x.ID)
	return how, err
}
