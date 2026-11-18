package board

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/kesokaj/crew/internal/db"
	"github.com/kesokaj/crew/internal/version"
)

// EvalCase is a known piece of a project's work, replayed from the code as it was (9.6). It stays in
// its project.
type EvalCase struct {
	ID         int64       `json:"-"`
	Number     int         `json:"number"`
	Title      string      `json:"title"`
	Origin     string      `json:"origin"`
	Ticket     *string     `json:"ticket"`
	Bug        *int        `json:"bug"`
	BaseCommit string      `json:"baseCommit"`
	Why        string      `json:"why"`
	What       string      `json:"what"`
	Goal       string      `json:"goal"`
	Checks     []CheckSpec `json:"checks"`
	HeldOut    []string    `json:"heldOut"`
	ChosenWhy  string      `json:"chosenWhy"`
	ProposedBy string      `json:"proposedBy"`
	Status     string      `json:"status"`
	RetiredWhy *string     `json:"retiredWhy"`
	CreatedAt  time.Time   `json:"createdAt"`
	// Fit is the fit check of the case's text (9.6, since 0.51), and Revision counts the operator's
	// edits of the text.
	Fit         Fit `json:"fit"`
	Revision    int `json:"revision"`
	mergeCommit string
}

// The states of a case's fit check (9.6). Pending waits for its judge; running has one; checked has
// its list, empty when nothing is missing; unchecked waits because the project's builder's model
// cannot be read, so no judge of the other family can be chosen; error is a judge whose answer could
// not be read maxFitTries times. Only checked with an empty list makes a case active by itself.
const (
	FitPending   = "pending"
	FitRunning   = "running"
	FitChecked   = "checked"
	FitUnchecked = "unchecked"
	FitError     = "error"
)

// Fit is what a case's fit check found: what the held-out files need that the case's text does not
// state, by a judge on the other family than the project's builder.
type Fit struct {
	State string   `json:"state"`
	List  []string `json:"list"`
	// Why says why the check did not run or could not be read, such as "not checked".
	Why string `json:"why,omitempty"`
	// Builder and Judge are the models: the project's builder's when the check was chosen, and the
	// judge's.
	Builder string `json:"builder,omitempty"`
	Judge   string `json:"judge,omitempty"`
	// Partial says the judge read only part of the held-out files: its empty list makes the case active
	// only with the operator's accept.
	Partial bool `json:"partial,omitempty"`
	// Run is the judge's run, Revision the text it read, and Tries its answers that could not be read.
	Run       string     `json:"run,omitempty"`
	Revision  int        `json:"revision,omitempty"`
	Tries     int        `json:"tries,omitempty"`
	CheckedAt *time.Time `json:"checkedAt,omitempty"`
}

// NewCase is a proposed case: from a Done ticket, or from a fixed bug through its fix ticket, with
// the test files that are held out and why the case was chosen.
type NewCase struct {
	Ticket  string   `json:"ticket"`
	Bug     int      `json:"bug"`
	HeldOut []string `json:"heldOut"`
	Why     string   `json:"why"`
}

const caseCols = `c.id, c.number, c.title, c.origin, CASE WHEN t.id IS NULL THEN NULL ELSE p.key || '-' || t.number END, g.number,
	c.base_commit, c.why, c.what, c.goal, c.checks, c.held_out, c.chosen_why, c.proposed_by, c.status, c.retired_why, c.created_at, c.fit, c.revision`

const caseFrom = ` FROM eval_cases c JOIN projects p ON p.id = c.project LEFT JOIN tickets t ON t.id = c.origin_ticket LEFT JOIN bugs g ON g.id = c.origin_bug`

func scanCase(row pgx.Row) (*EvalCase, error) {
	var c EvalCase
	var checks, fit []byte
	if err := row.Scan(&c.ID, &c.Number, &c.Title, &c.Origin, &c.Ticket, &c.Bug, &c.BaseCommit, &c.Why, &c.What, &c.Goal, &checks,
		&c.HeldOut, &c.ChosenWhy, &c.ProposedBy, &c.Status, &c.RetiredWhy, &c.CreatedAt, &fit, &c.Revision); err != nil {
		return nil, err
	}
	if err := json.Unmarshal(checks, &c.Checks); err != nil {
		return nil, err
	}
	if err := json.Unmarshal(fit, &c.Fit); err != nil {
		return nil, err
	}
	if c.Fit.List == nil {
		c.Fit.List = []string{}
	}
	return &c, nil
}

// Cases lists a project's eval cases, newest first; active only when asked.
func (b *Board) Cases(ctx context.Context, p *Project, activeOnly bool) ([]*EvalCase, error) {
	rows, err := b.Pool.Query(ctx, `SELECT `+caseCols+caseFrom+` WHERE c.project = $1 AND (NOT $2 OR c.status = 'active') ORDER BY c.number DESC`, p.ID, activeOnly)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []*EvalCase{}
	for rows.Next() {
		c, err := scanCase(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// Case is one of a project's cases by its number, with the merge commit its held-out files come from.
func (b *Board) Case(ctx context.Context, q db.Querier, project string, id int64) (*EvalCase, error) {
	c, err := scanCase(q.QueryRow(ctx, `SELECT `+caseCols+caseFrom+` WHERE c.project = $1 AND c.id = $2`, project, id))
	if err != nil {
		return nil, err
	}
	if c.Ticket != nil {
		var tid int64
		if err := q.QueryRow(ctx, `SELECT origin_ticket FROM eval_cases WHERE id = $1`, id).Scan(&tid); err != nil {
			return nil, err
		}
		t, err := ticketByID(ctx, q, tid, false)
		if err != nil {
			return nil, err
		}
		if c.mergeCommit, err = MergeOf(ctx, q, t); err != nil {
			return nil, err
		}
	}
	return c, nil
}

// MergeCommit is the merge the case's ticket ended in: where its held-out files are read.
func (c *EvalCase) MergeCommit() string { return c.mergeCommit }

// ProposeCase makes an eval case from a Done ticket or a fixed bug (9.6). Its code is main before the
// ticket's merge; its checks are the ticket's command checks; its held-out files are test files the
// ticket added or changed. A case waits for its fit check, whoever proposed it (since 0.51): it joins
// the suite when the check finds nothing missing, or when the operator accepts it.
func (b *Board) ProposeCase(ctx context.Context, p *Project, n NewCase, by, run string) (*EvalCase, error) {
	if (n.Ticket == "") == (n.Bug == 0) {
		return nil, errf(Invalid, "name a Done ticket, or a fixed bug")
	}
	if len(n.Why) > 1000 || len(n.HeldOut) > 50 {
		return nil, errf(Invalid, "why: at most 1000 characters; heldOut: at most 50 files")
	}
	var t *Ticket
	var bugID int64
	origin := "ticket"
	if n.Bug != 0 {
		origin = "bug"
		var fix *int64
		var status string
		err := b.Pool.QueryRow(ctx, `SELECT id, fix_ticket, status FROM bugs WHERE project = $1 AND number = $2`, p.ID, n.Bug).Scan(&bugID, &fix, &status)
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, errf(NotFound, "no bug %04d", n.Bug)
		}
		if err != nil {
			return nil, err
		}
		if status != "fixed" || fix == nil {
			return nil, errf(Refused, "bug %04d is %s: a case comes from a bug that was fixed", n.Bug, status)
		}
		var err2 error
		if t, err2 = ticketByID(ctx, b.Pool, *fix, false); err2 != nil {
			return nil, err2
		}
	} else {
		var err error
		if t, err = b.Ticket(ctx, p, n.Ticket); err != nil {
			return nil, err
		}
	}
	if by == Operator && origin == "ticket" {
		origin = "operator"
	}
	if t.Stage != "done" {
		return nil, errf(Refused, "%s is %s: a case comes from a ticket that is Done", t.Key, t.Stage)
	}
	merge, err := MergeOf(ctx, b.Pool, t)
	if err != nil {
		return nil, err
	}
	if merge == "" {
		return nil, errf(Refused, "%s merged nothing: there is no work to replay", t.Key)
	}
	base, err := b.Git.FirstParent(ctx, p.Name, merge)
	if err != nil || base == "" {
		return nil, fmt.Errorf("the commit before %s's merge: %w", t.Key, err)
	}
	var checks []CheckSpec
	for _, c := range t.Checks {
		if c.Kind == "cmd" {
			checks = append(checks, CheckSpec{Kind: "cmd", Spec: c.Spec})
		}
	}
	if len(checks) == 0 {
		return nil, errf(Refused, "%s has no command check: a case is decided by commands", t.Key)
	}
	changed, err := b.Git.ChangedFiles(ctx, p.Name, base, merge)
	if err != nil {
		return nil, err
	}
	held := []string{}
	for _, f := range n.HeldOut {
		f = strings.TrimSpace(f)
		switch {
		case !slices.Contains(changed, f):
			return nil, errf(Invalid, "heldOut: %s is no file %s added or changed", f, t.Key)
		case !IsTestFile(f, t.Checks):
			return nil, errf(Invalid, "heldOut: %s is not a test file; only tests are held out", f)
		}
		if !slices.Contains(held, f) {
			held = append(held, f)
		}
	}
	raw, _ := json.Marshal(checks)
	var out *EvalCase
	err = b.inTx(ctx, func(tx pgx.Tx) error {
		locked, err := projectByRef(ctx, tx, p.ID, true)
		if err != nil {
			return err
		}
		var dup int
		if err := tx.QueryRow(ctx, `SELECT count(*) FROM eval_cases WHERE project = $1 AND origin_ticket = $2 AND status IN ('active', 'waiting')`, locked.ID, t.ID).Scan(&dup); err != nil {
			return err
		}
		if dup > 0 {
			return errf(Refused, "%s is a case already", t.Key)
		}
		var id int64
		if err := tx.QueryRow(ctx, `INSERT INTO eval_cases (project, number, title, origin, origin_ticket, origin_bug, base_commit, why, what, goal, checks,
				held_out, chosen_why, proposed_by, proposed_run, status, fit)
			VALUES ($1, (SELECT coalesce(max(number), 0) + 1 FROM eval_cases WHERE project = $1), $2, $3, $4, nullif($5, 0), $6, $7, $8, $9, $10, $11, $12, $13,
				nullif($14, '')::uuid, 'waiting', '{"state": "pending"}') RETURNING id`,
			locked.ID, t.Title, origin, t.ID, bugID, base, t.Why, t.What, t.Goal, raw, held, n.Why, by, run).Scan(&id); err != nil {
			return err
		}
		if out, err = scanCase(tx.QueryRow(ctx, `SELECT `+caseCols+caseFrom+` WHERE c.id = $1`, id)); err != nil {
			return err
		}
		return db.RecordProjectChange(ctx, tx, locked.ID, by, "eval_cases", fmt.Sprintf("%04d", out.Number), nil,
			map[string]any{"ticket": t.Key, "heldOut": held})
	})
	return out, err
}

// RetireCase takes a case out of the suite, or out of waiting, with the reason; it stays in the record.
func (b *Board) RetireCase(ctx context.Context, p *Project, number int, why, who string) error {
	if strings.TrimSpace(why) == "" {
		return errf(Invalid, "why: say why the case is retired")
	}
	return b.inTx(ctx, func(tx pgx.Tx) error {
		var was string
		err := tx.QueryRow(ctx, `UPDATE eval_cases c SET status = 'retired', retired_why = $3 FROM eval_cases o
			WHERE o.id = c.id AND c.project = $1 AND c.number = $2 AND c.status IN ('active', 'waiting') RETURNING o.status`, p.ID, number, why).Scan(&was)
		if errors.Is(err, pgx.ErrNoRows) {
			return errf(NotFound, "no active or waiting case %04d", number)
		}
		if err != nil {
			return err
		}
		return db.RecordProjectChange(ctx, tx, p.ID, who, "eval_cases", fmt.Sprintf("%04d", number), map[string]any{"status": was}, map[string]any{"status": "retired", "why": why})
	})
}

// AcceptCase makes a waiting case active on the operator's word, whatever its fit check found or
// whether it ran (9.6): the operator's choice wins.
func (b *Board) AcceptCase(ctx context.Context, p *Project, number int, who string) (*EvalCase, error) {
	var out *EvalCase
	err := b.inTx(ctx, func(tx pgx.Tx) error {
		var id int64
		err := tx.QueryRow(ctx, `UPDATE eval_cases SET status = 'active' WHERE project = $1 AND number = $2 AND status = 'waiting' RETURNING id`, p.ID, number).Scan(&id)
		if errors.Is(err, pgx.ErrNoRows) {
			return errf(NotFound, "no waiting case %04d", number)
		}
		if err != nil {
			return err
		}
		if out, err = scanCase(tx.QueryRow(ctx, `SELECT `+caseCols+caseFrom+` WHERE c.id = $1`, id)); err != nil {
			return err
		}
		return db.RecordProjectChange(ctx, tx, p.ID, who, "eval_cases", fmt.Sprintf("%04d", number), map[string]any{"status": "waiting"},
			map[string]any{"status": "active", "fit": out.Fit.State, "list": out.Fit.List})
	})
	return out, err
}

// CaseText is the text of a case the operator can change: what the agents are given of it. Its code,
// its checks, and its held-out files stay.
type CaseText struct {
	Title string `json:"title"`
	Why   string `json:"why"`
	What  string `json:"what"`
	Goal  string `json:"goal"`
}

// EditCase changes the text of a waiting case, and its fit check runs again on the new text (9.6). A
// case that is active or retired is not changed: its results were measured on its text, so a new text
// is a new case, made again from its ticket after the old one is retired.
func (b *Board) EditCase(ctx context.Context, p *Project, number int, t CaseText, who string) (*EvalCase, error) {
	if err := checkTexts(t.Title, t.Why, t.What, t.Goal); err != nil {
		return nil, err
	}
	if strings.TrimSpace(t.Goal) == "" {
		return nil, errf(Invalid, "goal: say what the case must achieve")
	}
	var out *EvalCase
	err := b.inTx(ctx, func(tx pgx.Tx) error {
		before, err := scanCase(tx.QueryRow(ctx, `SELECT `+caseCols+caseFrom+` WHERE c.project = $1 AND c.number = $2 FOR UPDATE OF c`, p.ID, number))
		if errors.Is(err, pgx.ErrNoRows) {
			return errf(NotFound, "no case %04d", number)
		}
		if err != nil {
			return err
		}
		if before.Status != "waiting" {
			return errf(Refused, "case %04d is %s: only a waiting case's text is changed; retire it and make the case again", number, before.Status)
		}
		if _, err := tx.Exec(ctx, `UPDATE eval_cases SET title = $2, why = $3, what = $4, goal = $5, revision = revision + 1,
				fit = jsonb_build_object('state', 'pending') WHERE id = $1`, before.ID, t.Title, t.Why, t.What, t.Goal); err != nil {
			return err
		}
		if out, err = scanCase(tx.QueryRow(ctx, `SELECT `+caseCols+caseFrom+` WHERE c.id = $1`, before.ID)); err != nil {
			return err
		}
		return db.RecordProjectChange(ctx, tx, p.ID, who, "eval_cases", fmt.Sprintf("%04d", number),
			map[string]any{"title": before.Title, "why": before.Why, "what": before.What, "goal": before.Goal, "revision": before.Revision},
			map[string]any{"title": out.Title, "why": out.Why, "what": out.What, "goal": out.Goal, "revision": out.Revision})
	})
	return out, err
}

// EvalSuite is one run of a project's suite (10.5).
type EvalSuite struct {
	ID          int64  `json:"id"`
	Purpose     string `json:"purpose"`
	Override    *int   `json:"override"`
	Improvement *int   `json:"improvement"`
	// Gate is the version whose gate a release's suite is of (16.2 step 5, since 0.52).
	Gate       *string         `json:"gate"`
	Repeats    int             `json:"repeats"`
	State      string          `json:"state"`
	StoppedWhy *string         `json:"stoppedWhy"`
	Result     json.RawMessage `json:"result"`
	AskedBy    string          `json:"askedBy"`
	CreatedAt  time.Time       `json:"createdAt"`
	StartedAt  *time.Time      `json:"startedAt"`
	EndedAt    *time.Time      `json:"endedAt"`
	Runs       int             `json:"runs"`
	RunsDone   int             `json:"runsDone"`
	// Unmeasured says why an ended suite measured no case, so that it does not read as goals met by
	// none: the why of its last run that measured nothing, else why the suite stopped. It is nil for
	// a suite that measured a case, and for one still going (M15 on dave).
	Unmeasured *string `json:"unmeasured"`
}

// unmeasuredWhy is why the suite the expression names measured no case: the why of its last run that
// measured nothing, else why it stopped. Both arguments are SQL of the caller's own, never input.
func unmeasuredWhy(suite, stopped string) string {
	return `coalesce((SELECT r.detail->>'why' FROM eval_runs r WHERE r.suite = ` + suite + ` AND r.goal_met IS NULL AND r.detail->>'why' <> ''
		ORDER BY r.id DESC LIMIT 1), ` + stopped + `, 'no run of it measured a result')`
}

// UnmeasuredWhy is why a suite that is ending with no case measured says so, as Suites will say it once
// it has ended: stopped is why it stops, if it does (M15 on dave: the eval_result signal said 0%).
func UnmeasuredWhy(ctx context.Context, q db.Querier, suite int64, stopped string) (string, error) {
	var why string
	err := q.QueryRow(ctx, `SELECT `+unmeasuredWhy("$1", "nullif($2, '')"), suite, stopped).Scan(&why)
	return why, err
}

// Suites lists a project's suites, newest first; release says whether a release's gate's suites are
// among them, which measured a candidate, not the project as it runs (16.2 step 5, since 0.52).
func (b *Board) Suites(ctx context.Context, p *Project, limit int, release bool) ([]*EvalSuite, error) {
	rows, err := b.Pool.Query(ctx, `SELECT s.id, s.purpose, o.number, i.number, s.gate, s.repeats, s.state, s.stopped_why,
			s.result, s.asked_by, s.created_at, s.started_at, s.ended_at,
			(SELECT count(*) FROM eval_runs r WHERE r.suite = s.id), (SELECT count(*) FROM eval_runs r WHERE r.suite = s.id AND r.state IN ('done', 'failed', 'stopped')),
			CASE WHEN s.state IN ('done', 'stopped') AND coalesce((s.result->'baseline'->>'cases')::int, 0) = 0 THEN `+unmeasuredWhy("s.id", "s.stopped_why")+` END
		FROM eval_suites s LEFT JOIN overrides o ON o.id = s.override LEFT JOIN improvements i ON i.id = s.improvement
		WHERE s.project = $1 AND (s.purpose <> 'release' OR $3) ORDER BY s.id DESC LIMIT $2`, p.ID, limit, release)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []*EvalSuite{}
	for rows.Next() {
		var s EvalSuite
		if err := rows.Scan(&s.ID, &s.Purpose, &s.Override, &s.Improvement, &s.Gate, &s.Repeats, &s.State, &s.StoppedWhy,
			&s.Result, &s.AskedBy, &s.CreatedAt, &s.StartedAt, &s.EndedAt, &s.Runs, &s.RunsDone, &s.Unmeasured); err != nil {
			return nil, err
		}
		out = append(out, &s)
	}
	return out, rows.Err()
}

// EvalRunView is one eval run of a suite as the Evals tab shows it: what it measured, why it ended,
// and the output of its failed held-out checks, which only the operator sees (9.6, since 0.51).
type EvalRunView struct {
	ID           int64   `json:"id"`
	Case         int     `json:"case"`
	Repeat       int     `json:"repeat"`
	Side         string  `json:"side"`
	State        string  `json:"state"`
	GoalMet      *bool   `json:"goalMet"`
	CheckRounds  int     `json:"checkRounds"`
	Rework       int     `json:"rework"`
	Model        *string `json:"model"`
	Why          string  `json:"why"`
	FailedOutput *string `json:"failedOutput"`
}

// EvalRuns lists the eval runs of one of a project's suites, in the order they ran.
func (b *Board) EvalRuns(ctx context.Context, p *Project, suite int64) ([]*EvalRunView, error) {
	var n int
	if err := b.Pool.QueryRow(ctx, `SELECT count(*) FROM eval_suites WHERE id = $1 AND project = $2`, suite, p.ID).Scan(&n); err != nil {
		return nil, err
	}
	if n == 0 {
		return nil, errf(NotFound, "no suite %d", suite)
	}
	rows, err := b.Pool.Query(ctx, `SELECT r.id, c.number, r.repeat, r.side, r.state, r.goal_met, r.check_rounds, r.rework, r.model,
			coalesce(r.detail->>'why', ''), r.failed_output
		FROM eval_runs r JOIN eval_cases c ON c.id = r.eval_case WHERE r.suite = $1 AND r.project = $2 ORDER BY r.id`, suite, p.ID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []*EvalRunView{}
	for rows.Next() {
		var v EvalRunView
		if err := rows.Scan(&v.ID, &v.Case, &v.Repeat, &v.Side, &v.State, &v.GoalMet, &v.CheckRounds, &v.Rework, &v.Model, &v.Why, &v.FailedOutput); err != nil {
			return nil, err
		}
		out = append(out, &v)
	}
	return out, rows.Err()
}

// ReleaseCompare is how a release's gate compares a project (16.2 step 5, since 0.52): the earlier
// suite and the cases to run, or why the project is not compared.
type ReleaseCompare struct {
	Against int64
	Cases   []int64
	Why     string
}

// ReleaseBaseline decides how a release's gate compares a project (16.2 step 5, since 0.52), before
// anything of it runs. It is not compared, with why, when it is paused; when it has fewer active
// cases than knowledge.evals.min_cases; when no done suite, not a release suite, measured a case on
// its baseline side on running, the running release's version; and when the cases that suite
// measured and that are still active are too few. Otherwise it is compared with the newest such suite,
// on those cases.
func (b *Board) ReleaseBaseline(ctx context.Context, p *Project, running string) (ReleaseCompare, error) {
	doc, err := b.Settings(ctx, b.Pool, p)
	if err != nil {
		return ReleaseCompare{}, err
	}
	minCases := 10.0
	if v, ok := settingNumber(doc, "knowledge.evals.min_cases"); ok {
		minCases = max(1, v)
	}
	if p.State == "paused" {
		return ReleaseCompare{Why: "the project is paused"}, nil
	}
	var active int
	if err := b.Pool.QueryRow(ctx, `SELECT count(*) FROM eval_cases WHERE project = $1 AND status = 'active'`, p.ID).Scan(&active); err != nil {
		return ReleaseCompare{}, err
	}
	if float64(active) < minCases {
		return ReleaseCompare{Why: fmt.Sprintf("%d active cases, and at least %d are needed to tell", active, int(minCases))}, nil
	}
	var against int64
	// A release suite has no baseline side; a run that met its goal or not is done.
	err = b.Pool.QueryRow(ctx, `SELECT s.id FROM eval_suites s WHERE s.project = $1 AND s.state = 'done'
		AND EXISTS (SELECT 1 FROM eval_runs r WHERE r.suite = s.id AND r.side = 'baseline' AND r.crew_version = $2 AND r.goal_met IS NOT NULL)
		ORDER BY s.id DESC LIMIT 1`, p.ID, running).Scan(&against)
	if errors.Is(err, pgx.ErrNoRows) {
		return ReleaseCompare{Why: "no suite on the running release " + running}, nil
	}
	if err != nil {
		return ReleaseCompare{}, err
	}
	rows, err := b.Pool.Query(ctx, `SELECT DISTINCT r.eval_case FROM eval_runs r JOIN eval_cases c ON c.id = r.eval_case
		WHERE r.suite = $1 AND r.side = 'baseline' AND r.goal_met IS NOT NULL AND c.status = 'active' ORDER BY 1`, against)
	if err != nil {
		return ReleaseCompare{}, err
	}
	cases, err := pgx.CollectRows(rows, pgx.RowTo[int64])
	if err != nil {
		return ReleaseCompare{}, err
	}
	if float64(len(cases)) < minCases {
		return ReleaseCompare{Why: fmt.Sprintf("%d cases measured on the running release are still active, and at least %d are needed to tell", len(cases), int(minCases))}, nil
	}
	return ReleaseCompare{Against: against, Cases: cases}, nil
}

// StartReleaseSuite queues the suite of a release's gate on a project (16.2 step 5, since 0.52): the
// cases given, once each, on side release, from the candidate's runtime image (image, by its id) with
// the candidate's eval files (layer) laid over the baseline, to be compared with the earlier suite
// against. Its runs record the gate's version. It is refused for a gate that is not live, a project
// with a suite open, and cases none of which is active; the pipeline stops one on a project archived
// or paused, as any suite.
func (b *Board) StartReleaseSuite(ctx context.Context, p *Project, gate, image string, against int64, cases []int64, layer map[string]string, by string) (int64, error) {
	// The image by its id, and the suite compared with, are the table's own checks.
	if layer == nil {
		layer = map[string]string{}
	}
	var id int64
	err := b.inTx(ctx, func(tx pgx.Tx) error {
		locked, err := projectByRef(ctx, tx, p.ID, true)
		if err != nil {
			return err
		}
		var live int
		if err := tx.QueryRow(ctx, `SELECT count(*) FROM gates WHERE version = $1 AND state = 'live'`, gate).Scan(&live); err != nil {
			return err
		}
		if live == 0 {
			return errf(Refused, "the gate of %s does not run its live steps", gate)
		}
		var open int
		if err := tx.QueryRow(ctx, `SELECT count(*) FROM eval_suites WHERE project = $1 AND state IN ('queued', 'running')`, locked.ID).Scan(&open); err != nil {
			return err
		}
		if open > 0 {
			return errf(Refused, "a suite of %s waits or runs already", locked.Name)
		}
		rows, err := tx.Query(ctx, `SELECT id FROM eval_cases WHERE project = $1 AND status = 'active' AND id = ANY($2) ORDER BY number`, locked.ID, cases)
		if err != nil {
			return err
		}
		active, err := pgx.CollectRows(rows, pgx.RowTo[int64])
		if err != nil {
			return err
		}
		if len(active) == 0 {
			return errf(Refused, "none of the cases is an active case of %s", locked.Name)
		}
		if err := tx.QueryRow(ctx, `INSERT INTO eval_suites (project, purpose, repeats, asked_by, gate, image, compare_to, layer)
			VALUES ($1, 'release', 1, $2, $3, $4, $5, $6) RETURNING id`, locked.ID, by, gate, image, against, layer).Scan(&id); err != nil {
			return err
		}
		for _, c := range active {
			if _, err := tx.Exec(ctx, `INSERT INTO eval_runs (suite, project, eval_case, repeat, side, crew_version) VALUES ($1, $2, $3, 1, 'release', $4)`,
				id, locked.ID, c, gate); err != nil {
				return err
			}
		}
		return db.RecordProjectChange(ctx, tx, locked.ID, by, "eval_suites", fmt.Sprint(id), nil,
			map[string]any{"purpose": "release", "gate": gate, "cases": len(active), "against": against})
	})
	return id, err
}

// StartSuite queues a run of a project's suite: every active case, knowledge.evals.repeats times, on
// the baseline, and with the change too for an override or an improvement. One suite of a project
// waits or runs at a time.
func (b *Board) StartSuite(ctx context.Context, p *Project, purpose string, override, improvement int64, by string) (int64, error) {
	if !slices.Contains([]string{"on_demand", "override", "improvement"}, purpose) || (purpose == "override") != (override != 0) ||
		(purpose == "improvement") != (improvement != 0) {
		return 0, errf(Invalid, "a suite runs on demand, for an override, or for an improvement")
	}
	var id int64
	err := b.inTx(ctx, func(tx pgx.Tx) error {
		locked, err := projectByRef(ctx, tx, p.ID, true)
		if err != nil {
			return err
		}
		if locked.State == "archived" {
			return errf(Refused, "%s is archived", locked.Name)
		}
		var open int
		if err := tx.QueryRow(ctx, `SELECT count(*) FROM eval_suites WHERE project = $1 AND state IN ('queued', 'running')`, locked.ID).Scan(&open); err != nil {
			return err
		}
		if open > 0 {
			return errf(Refused, "a suite of %s waits or runs already", locked.Name)
		}
		var cases []int64
		rows, err := tx.Query(ctx, `SELECT id FROM eval_cases WHERE project = $1 AND status = 'active' ORDER BY number`, locked.ID)
		if err != nil {
			return err
		}
		for rows.Next() {
			var c int64
			if err := rows.Scan(&c); err != nil {
				rows.Close()
				return err
			}
			cases = append(cases, c)
		}
		rows.Close()
		if len(cases) == 0 {
			return errf(Refused, "%s has no eval case yet", locked.Name)
		}
		doc, err := b.Settings(ctx, tx, locked)
		if err != nil {
			return err
		}
		repeats, _ := settingNumber(doc, "knowledge.evals.repeats")
		if repeats < 1 {
			repeats = 1
		}
		if err := tx.QueryRow(ctx, `INSERT INTO eval_suites (project, purpose, override, improvement, repeats, asked_by)
			VALUES ($1, $2, nullif($3, 0), nullif($4, 0), $5, $6) RETURNING id`, locked.ID, purpose, override, improvement, int(repeats), by).Scan(&id); err != nil {
			return err
		}
		sides := []string{"baseline"}
		if purpose != "on_demand" {
			sides = append(sides, "change")
		}
		// Repeats go round the cases, so a suite that is stopped has measured each case as often as it could.
		for r := 1; r <= int(repeats); r++ {
			for _, c := range cases {
				for _, side := range sides {
					if _, err := tx.Exec(ctx, `INSERT INTO eval_runs (suite, project, eval_case, repeat, side, crew_version) VALUES ($1, $2, $3, $4, $5, $6)`,
						id, locked.ID, c, r, side, version.Version); err != nil {
						return err
					}
				}
			}
		}
		return db.RecordProjectChange(ctx, tx, locked.ID, by, "eval_suites", fmt.Sprint(id), nil,
			map[string]any{"purpose": purpose, "cases": len(cases), "repeats": int(repeats)})
	})
	return id, err
}
