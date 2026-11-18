package versions

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"sync/atomic"
	"time"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/kesokaj/crew/baseline"
	"github.com/kesokaj/crew/internal/board"
	"github.com/kesokaj/crew/internal/measure"
	"github.com/kesokaj/crew/internal/release"
	"github.com/kesokaj/crew/internal/updater"
	"github.com/kesokaj/crew/internal/version"
)

// The live steps of a gate on this server (16.2 step 5, since 0.51): one real ticket on the hidden
// project crew-gate, in the candidate's runtime image, with the login the broker hands any run, which
// passes when the board moves it to Done on its command checks; then the eval suites, when the release
// changes what agents see. The gate's own row holds where they are, so a restart takes them up again.

// realStep is the name of the real ticket's step, and suitesStep the eval suites', on the gate's report.
const (
	realStep   = "the real ticket on crew-gate"
	suitesStep = "the eval suites"
)

// LiveRunner runs the live steps. Its board, its kick, and who is signed in come once the server made
// them, which is after the releases' loop started.
type LiveRunner struct {
	Pool *pgxpool.Pool
	// LimitWait is how long the real ticket, or the eval suites, may wait at a limit before the step is
	// skipped, six hours; TicketTime how long the real ticket may take otherwise before it fails, eight
	// hours; SuitesTime how long the suites may take outside a limit before they are skipped, 24 hours.
	LimitWait, TicketTime, SuitesTime time.Duration
	Now                               func() time.Time
	// Running is the running release's version, whose suites a release's suites are compared with;
	// empty is this server's.
	Running string

	deps atomic.Pointer[LiveDeps]
}

// LiveDeps is what the live steps work with: the board, which moves the real ticket; Kick, which wakes
// the board and the pipeline; and SignedIn, the agent CLIs that are signed in.
type LiveDeps struct {
	Board    *board.Board
	Kick     func()
	SignedIn func(ctx context.Context) []string
}

// Use gives the live steps what they work with.
func (g *LiveRunner) Use(d LiveDeps) { g.deps.Store(&d) }

func (g *LiveRunner) now() time.Time {
	if g.Now != nil {
		return g.Now()
	}
	return time.Now()
}

func (g *LiveRunner) limitWait() time.Duration {
	if g.LimitWait > 0 {
		return g.LimitWait
	}
	return 6 * time.Hour
}

func (g *LiveRunner) ticketTime() time.Duration {
	if g.TicketTime > 0 {
		return g.TicketTime
	}
	return 8 * time.Hour
}

func (g *LiveRunner) suitesTime() time.Duration {
	if g.SuitesTime > 0 {
		return g.SuitesTime
	}
	return 24 * time.Hour
}

func (g *LiveRunner) running() string {
	if g.Running != "" {
		return g.Running
	}
	return version.Version
}

// liveRow is where a gate's live steps are.
type liveRow struct {
	ticket     string
	started    *time.Time
	limitSince *time.Time
	real       *realResult
}

// realResult is the real ticket's step as it ended: passed, failed, or skipped with why.
type realResult struct {
	Step    release.GateStep `json:"step"`
	Skipped string           `json:"skipped,omitempty"`
}

// Step steps the live steps of the gate of l.Version: nil until they ended, then their result.
func (g *LiveRunner) Step(ctx context.Context, l LiveStart) (*updater.LiveAsk, error) {
	d := g.deps.Load()
	if d == nil || d.Board == nil {
		return nil, errors.New("the live steps have no board yet: the server did not wire them")
	}
	var row liveRow
	var real []byte
	if err := g.Pool.QueryRow(ctx, `SELECT coalesce(ticket, ''), live_started_at, limit_since, real_step FROM gates WHERE version = $1`, l.Version).
		Scan(&row.ticket, &row.started, &row.limitSince, &real); err != nil {
		return nil, err
	}
	if real != nil {
		row.real = &realResult{}
		if err := json.Unmarshal(real, row.real); err != nil {
			return nil, err
		}
	}
	if row.real == nil {
		if l.SameRuntime && !l.Agents && row.ticket == "" {
			why := "the runtime image and what agents see are unchanged from " + g.running()
			r := &realResult{
				Step:    release.GateStep{Name: realStep, OK: true, Detail: "skipped: " + why},
				Skipped: why,
			}
			body, _ := json.Marshal(r)
			if _, err := g.Pool.Exec(ctx, `UPDATE gates SET real_step = $2, updated_at = now() WHERE version = $1`, l.Version, body); err != nil {
				return nil, err
			}
			row.real = r
		} else {
			r, err := g.realTicket(ctx, d, l, row)
			if err != nil || r == nil {
				return nil, err
			}
			body, _ := json.Marshal(r)
			if _, err := g.Pool.Exec(ctx, `UPDATE gates SET real_step = $2, updated_at = now() WHERE version = $1`, l.Version, body); err != nil {
				return nil, err
			}
			row.real = r
		}
	}
	var su *suitesResult
	if row.real.Step.OK && row.real.Skipped == "" && l.Agents {
		var err error
		if su, err = g.suites(ctx, d, l); err != nil || su == nil {
			return nil, err
		}
	}
	return g.held(l, g.result(l, row.real, su)), nil
}

// result is the live part once the real ticket's step ended: failed when it failed; skipped with why
// when it was skipped, and the suites with it; the suites otherwise, su, which hold, and are skipped
// when they did not run to their end.
func (g *LiveRunner) result(l LiveStart, r *realResult, su *suitesResult) *updater.LiveAsk {
	out := &updater.LiveAsk{Steps: []release.GateStep{r.Step}}
	switch {
	case !r.Step.OK && r.Skipped == "":
		out.Live = release.LiveFailed
		return out
	case r.Skipped != "":
		out.Live = release.LiveSkipped
		out.Skipped = []string{realStep + ": " + r.Skipped}
		if l.Agents {
			out.Skipped = append(out.Skipped, suitesStep+": "+r.Skipped)
		}
		return out
	}
	out.Live = release.LivePassed
	if !l.Agents {
		out.Steps = append(out.Steps, release.GateStep{Name: suitesStep, OK: true, Detail: "no eval: nothing the agents see changed"})
		return out
	}
	out.Steps = append(out.Steps, su.Step)
	out.Hold = su.Hold
	if su.Skipped != "" {
		out.Live = release.LiveSkipped
		out.Skipped = []string{suitesStep + ": " + su.Skipped}
	}
	return out
}

// otherCode is the hold of a release whose live steps passed in a candidate runtime image of other
// code than the running release's: the runs in that image, a reviewer's verdict among them, are what
// the steps' results rest on, so the steps cannot let it install by itself (security review of M16 P4d;
// since 0.53 no cost is reported).
const otherCode = "its live steps ran in a candidate runtime image whose code is not the running release's, and the steps rest on what its runs did and said: Install decides"

// held is result, held for Install when its image's code is not the running release's.
func (g *LiveRunner) held(l LiveStart, out *updater.LiveAsk) *updater.LiveAsk {
	if out.Live == release.LivePassed && out.Hold == "" && !l.SameRuntime {
		out.Hold = otherCode
	}
	return out
}

// suiteEntry is one project of a gate's eval suites, as the gate's row keeps it: its release suite
// once it has one, or why it is not compared.
type suiteEntry struct {
	Project string `json:"project"`
	Name    string `json:"name"`
	Suite   int64  `json:"suite,omitempty"`
	Note    string `json:"note,omitempty"`
}

// suitesResult is the eval suites' step as it ended: the step, why it was skipped, and why it holds
// the release.
type suitesResult struct {
	Step    release.GateStep `json:"step"`
	Skipped string           `json:"skipped,omitempty"`
	Hold    string           `json:"hold,omitempty"`
}

// suites steps the eval suites of a gate whose real ticket passed (16.2 step 5, since 0.52): nil while
// they run, their step once it ended. The projects are fixed when they begin: each that is not hidden,
// not archived, and has an active case, by name. One release suite runs at a time, of a project the
// board says can be compared (ReleaseBaseline); a project whose own suite is open is passed over and
// taken after the others. The gate's row keeps the projects, the clock, and the step, so a restart
// takes them up. They are skipped when no agent CLI is signed in, after six hours on end at a limit,
// and after 24 hours outside a limit.
func (g *LiveRunner) suites(ctx context.Context, d *LiveDeps, l LiveStart) (*suitesResult, error) {
	var list, step []byte
	var started, limitSince *time.Time
	var limitS int
	if err := g.Pool.QueryRow(ctx, `SELECT suites, suites_started_at, suites_limit_since, suites_limit_s, suites_step FROM gates WHERE version = $1`,
		l.Version).Scan(&list, &started, &limitSince, &limitS, &step); err != nil {
		return nil, err
	}
	if step != nil {
		var r suitesResult
		return &r, json.Unmarshal(step, &r)
	}
	now := g.now()
	signedOut := d.SignedIn != nil && len(d.SignedIn(ctx)) == 0
	entries := []suiteEntry{}
	if list == nil {
		rows, err := g.Pool.Query(ctx, `SELECT p.id::text, p.name FROM projects p WHERE NOT p.hidden AND p.state <> 'archived'
			AND EXISTS (SELECT 1 FROM eval_cases c WHERE c.project = p.id AND c.status = 'active') ORDER BY p.name`)
		if err != nil {
			return nil, err
		}
		if entries, err = pgx.CollectRows(rows, func(r pgx.CollectableRow) (suiteEntry, error) {
			var e suiteEntry
			return e, r.Scan(&e.Project, &e.Name)
		}); err != nil {
			return nil, err
		}
		started = &now
		if err := g.saveSuites(ctx, l.Version, entries, now); err != nil {
			return nil, err
		}
	} else if err := json.Unmarshal(list, &entries); err != nil {
		return nil, err
	}
	if signedOut {
		return g.endSuites(ctx, l.Version, entries, "no agent CLI is signed in")
	}
	open, atLimit := false, false
	for _, e := range entries {
		if e.Suite == 0 {
			continue
		}
		var state string
		var waitFor *string
		if err := g.Pool.QueryRow(ctx, `SELECT state, wait_for FROM eval_suites WHERE id = $1`, e.Suite).Scan(&state, &waitFor); err != nil {
			return nil, err
		}
		if state == "queued" || state == "running" {
			open, atLimit = true, waitFor != nil && *waitFor == "model"
		}
	}
	// The clock: six hours on end at a limit skip the suites, as they skip the real ticket; the time at
	// a limit does not count toward the 24 hours.
	switch {
	case atLimit && limitSince == nil:
		limitSince = &now
		if _, err := g.Pool.Exec(ctx, `UPDATE gates SET suites_limit_since = $2 WHERE version = $1`, l.Version, now); err != nil {
			return nil, err
		}
	case atLimit && now.Sub(*limitSince) >= g.limitWait():
		return g.endSuites(ctx, l.Version, entries, fmt.Sprintf("it waited at a limit for %s", g.limitWait()))
	case !atLimit && limitSince != nil:
		limitS += int(now.Sub(*limitSince).Seconds())
		limitSince = nil
		if _, err := g.Pool.Exec(ctx, `UPDATE gates SET suites_limit_since = NULL, suites_limit_s = $2 WHERE version = $1`, l.Version, limitS); err != nil {
			return nil, err
		}
	}
	outside := now.Sub(*started) - time.Duration(limitS)*time.Second
	if limitSince != nil {
		outside -= now.Sub(*limitSince)
	}
	if outside >= g.suitesTime() {
		return g.endSuites(ctx, l.Version, entries, fmt.Sprintf("they ran out of time after %s", g.suitesTime()))
	}
	if open {
		return nil, nil
	}
	waiting := false
	for i := range entries {
		e := &entries[i]
		if e.Suite != 0 || e.Note != "" {
			continue
		}
		p, err := d.Board.Project(ctx, e.Project)
		if err != nil {
			return nil, err
		}
		// A suite of this gate that a stop between its start and its record left is taken.
		err = g.Pool.QueryRow(ctx, `SELECT id FROM eval_suites WHERE purpose = 'release' AND gate = $1 AND project = $2`, l.Version, p.ID).Scan(&e.Suite)
		if err == nil {
			return nil, g.saveSuites(ctx, l.Version, entries, now)
		}
		if !errors.Is(err, pgx.ErrNoRows) {
			return nil, err
		}
		c, err := d.Board.ReleaseBaseline(ctx, p, g.running())
		if err != nil {
			return nil, err
		}
		if c.Why != "" {
			e.Note = c.Why
			continue
		}
		layer, why, err := g.layer(ctx, d, l)
		if err != nil {
			return nil, err
		}
		if why != "" {
			return g.endSuites(ctx, l.Version, entries, why)
		}
		id, err := d.Board.StartReleaseSuite(ctx, p, l.Version, l.Image, c.Against, c.Cases, layer, "gate")
		var be *board.Error
		if errors.As(err, &be) && be.Kind == board.Refused {
			// A suite of the project's own waits or runs: the project is taken after the others, and
			// decided again then, since that suite may be the newer one to compare with.
			waiting = true
			continue
		}
		if err != nil {
			return nil, err
		}
		e.Suite = id
		if d.Kick != nil {
			d.Kick()
		}
		return nil, g.saveSuites(ctx, l.Version, entries, now)
	}
	if waiting {
		return nil, g.saveSuites(ctx, l.Version, entries, now)
	}
	return g.endSuites(ctx, l.Version, entries, "")
}

// saveSuites keeps the gate's projects, and when its suites started.
func (g *LiveRunner) saveSuites(ctx context.Context, version string, entries []suiteEntry, now time.Time) error {
	body, err := json.Marshal(entries)
	if err != nil {
		return err
	}
	_, err = g.Pool.Exec(ctx, `UPDATE gates SET suites = $2, suites_started_at = coalesce(suites_started_at, $3), updated_at = now() WHERE version = $1`,
		version, body, now)
	return err
}

// endSuites ends the eval suites' step with each project's line: its suite's decision, or why it was
// not compared. It is skipped with why when the suites did not run to their end, and when no project
// could be compared, so the release waits for Install; it holds the release when a project measured
// worse, and when a project could not tell because runs of the release measured nothing, which may be
// the candidate's own way to leave out the cases it fails (since 1.10). The hold and the notes name no
// project, since the release's page publishes them; the detail, which names each, it does not (16.2
// step 5, since 0.52).
func (g *LiveRunner) endSuites(ctx context.Context, version string, entries []suiteEntry, skipped string) (*suitesResult, error) {
	res := &suitesResult{}
	var lines []string
	compared, worse, unmeasured := 0, 0, 0
	for _, e := range entries {
		if e.Suite == 0 {
			why := e.Note
			if why == "" {
				why = "not reached"
			}
			lines = append(lines, e.Name+": not compared: "+why)
			continue
		}
		var state string
		var raw []byte
		if err := g.Pool.QueryRow(ctx, `SELECT state, result FROM eval_suites WHERE id = $1`, e.Suite).Scan(&state, &raw); err != nil {
			return nil, err
		}
		var r struct {
			Decision *measure.Decision `json:"decision"`
		}
		if raw != nil {
			if err := json.Unmarshal(raw, &r); err != nil {
				return nil, err
			}
		}
		switch {
		case state == "queued" || state == "running":
			lines = append(lines, e.Name+": not compared: its suite did not end")
		case r.Decision == nil:
			lines = append(lines, fmt.Sprintf("%s: not compared: its suite is %s", e.Name, state))
		case r.Decision.Verdict == measure.WorseVerdict:
			compared, worse = compared+1, worse+1
			lines = append(lines, fmt.Sprintf("%s: worse: %s", e.Name, r.Decision.Why))
		case r.Decision.Verdict == measure.NotWorseVerdict:
			compared++
			lines = append(lines, fmt.Sprintf("%s: not worse: %s", e.Name, r.Decision.Why))
		case r.Decision.Verdict == measure.TooFew && r.Decision.UnmeasuredRuns > 0:
			unmeasured++
			lines = append(lines, fmt.Sprintf("%s: could not tell: %s", e.Name, r.Decision.Why))
		default:
			lines = append(lines, fmt.Sprintf("%s: not compared: %s", e.Name, r.Decision.Why))
		}
	}
	if skipped == "" && compared == 0 {
		skipped = "no project could be compared"
	}
	res.Skipped = skipped
	res.Step = release.GateStep{Name: suitesStep, OK: skipped == "", Detail: detailOf(lines)}
	if res.Step.Detail == "" {
		res.Step.Detail = "not run"
	}
	var holds []string
	if worse > 0 {
		holds = append(holds, fmt.Sprintf("the eval suites measured worse in %d of %d compared projects; the numbers are on Versions", worse, compared))
	}
	if unmeasured > 0 {
		holds = append(holds, fmt.Sprintf("the eval suites could not tell in %d projects: runs of the release measured nothing; the numbers are on Versions", unmeasured))
	}
	res.Hold = strings.Join(holds, "; ")
	list, err := json.Marshal(entries)
	if err != nil {
		return nil, err
	}
	body, err := json.Marshal(res)
	if err != nil {
		return nil, err
	}
	if _, err := g.Pool.Exec(ctx, `UPDATE gates SET suites = $2, suites_step = $3, updated_at = now() WHERE version = $1`, version, list, body); err != nil {
		return nil, err
	}
	return res, nil
}

// maxEvalFile bounds each of the candidate's eval files a release's suite lays over the baseline.
const maxEvalFile = 256 << 10

// layer reads the candidate's eval files from the release's commit in Crew's project (9.6). why says
// the commit lacks one, git could not read one, or one is not text of at most 256 KB: the suites would
// measure the running prompts, so they are skipped.
func (g *LiveRunner) layer(ctx context.Context, d *LiveDeps, l LiveStart) (map[string]string, string, error) {
	short := l.Commit
	if len(short) > 12 {
		short = short[:12]
	}
	out := map[string]string{}
	for _, f := range baseline.EvalFiles() {
		b, err := d.Board.Git.FileAt(ctx, l.Project, l.Commit, "baseline/"+f)
		if err != nil {
			return nil, fmt.Sprintf("baseline/%s of the release's commit %s in Crew's project could not be read", f, short), nil
		}
		if b == nil {
			return nil, fmt.Sprintf("the release's commit %s in Crew's project has no baseline/%s", short, f), nil
		}
		if len(b) > maxEvalFile || !utf8.Valid(b) {
			return nil, fmt.Sprintf("the release's baseline/%s is not text of at most 256 KB", f), nil
		}
		out[f] = string(b)
	}
	return out, "", nil
}

// realTicket starts the real ticket, or reads where it is: nil while it runs, its step once it ended.
// Its image is the candidate's from the moment it starts until it ended; every way it ends takes it
// away again.
func (g *LiveRunner) realTicket(ctx context.Context, d *LiveDeps, l LiveStart, row liveRow) (*realResult, error) {
	if row.ticket == "" {
		if d.SignedIn != nil && len(d.SignedIn(ctx)) == 0 {
			return &realResult{Step: release.GateStep{Name: realStep, OK: false, Detail: "not run"}, Skipped: "no agent CLI is signed in"}, nil
		}
		return nil, g.startTicket(ctx, d, l)
	}
	var t struct {
		id                  int64
		stage               string
		closed              *string
		limit, login, stops bool
	}
	err := g.Pool.QueryRow(ctx, `SELECT t.id, t.stage, t.closed_reason,
			EXISTS (SELECT 1 FROM subtasks s WHERE s.ticket = t.id AND s.state = 'waiting' AND s.wait_for LIKE 'quota:%'),
			EXISTS (SELECT 1 FROM subtasks s WHERE s.ticket = t.id AND s.state = 'waiting' AND s.wait_for LIKE 'login:%'),
			EXISTS (SELECT 1 FROM subtasks s WHERE s.ticket = t.id AND (s.state = 'stopped' OR s.wait_for = 'operator'))
		FROM tickets t JOIN projects p ON p.id = t.project WHERE p.name = $1 AND p.key || '-' || t.number = $2 AND t.created_by = 'gate'`,
		board.GateProject, row.ticket).Scan(&t.id, &t.stage, &t.closed, &t.limit, &t.login, &t.stops)
	if errors.Is(err, pgx.ErrNoRows) {
		// The ticket's key is the gate's before the ticket is added: a stop between the two left it to
		// be added now (review of M16 P4c).
		return nil, g.addTicket(ctx, d, l, row.ticket)
	}
	if err != nil {
		return nil, err
	}
	step := func(ok bool, detail string) *realResult {
		return &realResult{Step: release.GateStep{Name: realStep, OK: ok, Detail: detail}}
	}
	now := g.now()
	switch {
	case t.stage == "done":
		return g.end(ctx, d, l.Image, row.ticket, false, step(true, row.ticket+" reached Done on its checks"))
	case t.closed != nil:
		return g.end(ctx, d, l.Image, row.ticket, false, step(false, fmt.Sprintf("%s was closed: %s", row.ticket, *t.closed)))
	case t.stops:
		return g.end(ctx, d, l.Image, row.ticket, true, step(false, "a run of "+row.ticket+" was stopped"))
	case t.login || (d.SignedIn != nil && len(d.SignedIn(ctx)) == 0):
		r := step(false, "not run to its end")
		r.Skipped = "no agent CLI is signed in"
		return g.end(ctx, d, l.Image, row.ticket, true, r)
	}
	if t.limit {
		if row.limitSince == nil {
			_, err := g.Pool.Exec(ctx, `UPDATE gates SET limit_since = $2 WHERE version = $1`, l.Version, now)
			return nil, err
		}
		if now.Sub(*row.limitSince) >= g.limitWait() {
			r := step(false, "not run to its end")
			r.Skipped = fmt.Sprintf("it waited at a limit for %s", g.limitWait())
			return g.end(ctx, d, l.Image, row.ticket, true, r)
		}
		return nil, nil
	}
	if row.limitSince != nil {
		if _, err := g.Pool.Exec(ctx, `UPDATE gates SET limit_since = NULL WHERE version = $1`, l.Version); err != nil {
			return nil, err
		}
	}
	if row.started != nil && now.Sub(*row.started) >= g.ticketTime() {
		return g.end(ctx, d, l.Image, row.ticket, true, step(false, fmt.Sprintf("%s did not reach Done in %s", row.ticket, g.ticketTime())))
	}
	return nil, nil
}

// startTicket gives crew-gate the candidate's runtime image, records the real ticket's key, and adds
// it, in a package of its own for this gate. The key is recorded first, so a stop between the two
// adds no second ticket: the next round adds this one (review of M16 P4c).
func (g *LiveRunner) startTicket(ctx context.Context, d *LiveDeps, l LiveStart) error {
	var n int
	if err := g.Pool.QueryRow(ctx, `SELECT next_number FROM projects WHERE name = $1 AND hidden`, board.GateProject).Scan(&n); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return fmt.Errorf("the gate's project %s is not made: make Crew's project first", board.GateProject)
		}
		return err
	}
	key := fmt.Sprintf("%s-%d", board.GateKey, n)
	if _, err := g.Pool.Exec(ctx, `UPDATE gates SET ticket = $2, live_started_at = $3, updated_at = now() WHERE version = $1`,
		l.Version, key, g.now()); err != nil {
		return err
	}
	return g.addTicket(ctx, d, l, key)
}

// addTicket gives crew-gate the candidate's image and adds the ticket whose key the gate recorded.
func (g *LiveRunner) addTicket(ctx context.Context, d *LiveDeps, l LiveStart, key string) error {
	if !imageID.MatchString(l.Image) {
		return fmt.Errorf("the candidate's runtime image is named %q, not by its id", l.Image)
	}
	var n int
	if _, err := fmt.Sscanf(key, board.GateKey+"-%d", &n); err != nil {
		return fmt.Errorf("the gate's ticket %q is no key of %s", key, board.GateProject)
	}
	if _, err := g.Pool.Exec(ctx, `UPDATE projects SET runtime_image = $2 WHERE name = $1 AND hidden`, board.GateProject, l.Image); err != nil {
		return err
	}
	t, err := d.Board.AddTicket(ctx, board.GateProject, board.GateTicket(n), board.OriginOperator, "gate")
	if err != nil {
		g.clear(context.WithoutCancel(ctx), l.Image)
		return fmt.Errorf("the real ticket: %w", err)
	}
	if t.Key != key {
		if _, err := g.Pool.Exec(ctx, `UPDATE gates SET ticket = $2 WHERE version = $1`, l.Version, t.Key); err != nil {
			return err
		}
	}
	if d.Kick != nil {
		d.Kick()
	}
	return nil
}

// maxDetail bounds the eval suites' detail, as the updater bounds each text of a gate.
const maxDetail = 2000

// detailOf is the eval suites' detail: each project's line, cut at a whole line to maxDetail.
func detailOf(lines []string) string {
	out := strings.Join(lines, "; ")
	if len(out) <= maxDetail {
		return out
	}
	for n := len(lines) - 1; n >= 0; n-- {
		tail := fmt.Sprintf("and %d more: see each project's Evals tab", len(lines)-n)
		if n > 0 {
			tail = "; " + tail
		}
		if kept := strings.Join(lines[:n], "; "); len(kept)+len(tail) <= maxDetail {
			return kept + tail
		}
	}
	return ""
}

// imageID is a runtime image's id, the only way the gate names one.
var imageID = regexp.MustCompile(`^sha256:[0-9a-f]{64}$`)

// clear takes image from crew-gate, and only that one: another gate's is not this gate's to take. An
// empty image, of a gate whose folder cannot be read and whose ticket runs, takes whatever crew-gate
// names, which is that gate's, since one real ticket runs at a time.
func (g *LiveRunner) clear(ctx context.Context, image string) error {
	_, err := g.Pool.Exec(ctx, `UPDATE projects SET runtime_image = NULL WHERE name = $1 AND (runtime_image = $2 OR $2 = '')`, board.GateProject, image)
	return err
}

// end ends the real ticket's step: the candidate's image is taken from crew-gate, and a ticket that
// did not reach Done is cancelled, so that nothing of it runs on after its gate.
func (g *LiveRunner) end(ctx context.Context, d *LiveDeps, image, ticket string, cancel bool, r *realResult) (*realResult, error) {
	if err := g.clear(ctx, image); err != nil {
		return nil, err
	}
	p, err := d.Board.Project(ctx, board.GateProject)
	if err != nil {
		return nil, err
	}
	// Every other ticket of crew-gate that is open, one an agent created or a lead's replacement, goes
	// with it: nothing of a gate runs on in a later gate's image (security review of M16 P4c).
	rows, err := g.Pool.Query(ctx, `SELECT p.key || '-' || t.number FROM tickets t JOIN projects p ON p.id = t.project
		WHERE p.name = $1 AND t.stage NOT IN ('done', 'closed') AND (p.key || '-' || t.number <> $2 OR $3)`, board.GateProject, ticket, cancel)
	if err != nil {
		return nil, err
	}
	keys, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		return nil, err
	}
	for _, k := range keys {
		var be *board.Error
		if _, err := d.Board.Cancel(ctx, p, k, "the gate's real ticket ended: "+r.Step.Detail, "gate"); err != nil &&
			!(errors.As(err, &be) && be.Kind == board.Refused) {
			return nil, err
		}
	}
	return r, nil
}

// Abandon ends a gate's real ticket that is still on its way when its gate leaves its live part
// another way: its folder was gated, or is no longer pending, or the release that runs no longer
// shares its runtime inputs and the operator did not press. The ticket is cancelled and the image
// taken (review of M16 P4c).
func (g *LiveRunner) Abandon(ctx context.Context, version, image, why string) error {
	var ticket string
	var real []byte
	err := g.Pool.QueryRow(ctx, `SELECT coalesce(ticket, ''), real_step FROM gates WHERE version = $1`, version).Scan(&ticket, &real)
	if err != nil {
		return err
	}
	d := g.deps.Load()
	// Its eval suites, once its real ticket ended, are the pipeline's to stop, which this wakes (16.2
	// step 5, since 0.52).
	if real != nil && d != nil && d.Kick != nil {
		d.Kick()
	}
	// A gate with no ticket on its way set no image, or took its own already: it has nothing to end.
	if ticket == "" || real != nil {
		return nil
	}
	if d == nil || d.Board == nil {
		return errors.New("the live steps have no board yet: the server did not wire them")
	}
	_, err = g.end(ctx, d, image, ticket, true, &realResult{Step: release.GateStep{Name: realStep, Detail: why}})
	return err
}
