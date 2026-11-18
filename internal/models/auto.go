package models

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"hash/fnv"
	"slices"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/kesokaj/crew/internal/cli"
	"github.com/kesokaj/crew/internal/db"
	"github.com/kesokaj/crew/internal/hold"
	"github.com/kesokaj/crew/internal/settings"
)

// ChosenRoles are the agents Crew changes by itself: the builder, by its passed share, and since bugs
// are recorded (M12) the agents that check, by the defects that escaped them and the share of their
// runs that asked for a fix (12.3). Results alone decide: since 0.53 no cost.
var ChosenRoles = []string{"builder", "reviewer", "tester", "e2e", "verifier"}

// checks says a role checks another's work: a unit is one of its runs, and nothing counts as passed.
func checks(role string) bool { return role != "builder" }

// Pick is an entry with an effort: what an agent runs on.
type Pick struct {
	Entry  string `json:"entry"` // CLI/model, the model as its CLI lists it
	Effort string `json:"effort"`
}

func (p Pick) String() string { return p.Entry + " · " + p.Effort }

// Rules are the numbers of agents.auto (12.3), all protected settings.
type Rules struct {
	On             bool
	SetByYou       []string
	ExploreShare   float64
	MinRuns        int
	MinDaysBetween int
	WatchRuns      int
	RetryDays      int
	HideDays       int
	CompareRuns    int
	BetterPoints   float64
	SamePoints     float64
	WindowDays     int
}

// RulesOf reads agents.auto.
func RulesOf(doc settings.Doc) Rules {
	n := func(k string, def float64) float64 {
		switch v := dig(doc, "agents", "auto", k).(type) {
		case int:
			return float64(v)
		case float64:
			return v
		}
		return def
	}
	on, ok := dig(doc, "agents", "auto", "on").(bool)
	return Rules{
		On: on || !ok, SetByYou: strs(dig(doc, "agents", "auto", "set_by_you")),
		ExploreShare: n("explore_share", 0.1), MinRuns: int(n("min_runs", 20)), MinDaysBetween: int(n("min_days_between", 7)),
		WatchRuns: int(n("watch_runs", 20)), RetryDays: int(n("retry_days", 30)), HideDays: int(n("hide_days", 30)),
		CompareRuns: int(n("compare_runs", 20)), BetterPoints: n("better_points", 8) / 100, SamePoints: n("same_points", 5) / 100,
		WindowDays: int(n("window_days", 90)),
	}
}

// Automatic says Crew chooses for the agent itself: it is one Crew chooses, agents.auto is on, and
// the operator did not set it.
func (r Rules) Automatic(role string) bool {
	return r.On && slices.Contains(AutoRoles, role) && !slices.Contains(r.SetByYou, role)
}

// Choice row as it is stored.
type ChoiceRow struct {
	ID       int64      `json:"id"`
	Role     string     `json:"role"`
	Kind     string     `json:"kind"`
	Reason   string     `json:"reason"`
	From     Pick       `json:"from"`
	To       Pick       `json:"to"`
	Numbers  Card       `json:"numbers"`
	State    string     `json:"state"`
	Who      string     `json:"who"`
	At       time.Time  `json:"at"`
	Decided  *time.Time `json:"decidedAt,omitempty"`
	HideTill *time.Time `json:"hideUntil,omitempty"`
}

// Choices are the choices of a role, newest first, or of every role when role is "".
func Choices(ctx context.Context, q db.Querier, role string) ([]ChoiceRow, error) {
	rows, err := q.Query(ctx, `SELECT id, role, kind, reason, from_entry, from_effort, to_entry, to_effort, numbers, state, who, at,
			decided_at, hide_until FROM model_choices WHERE $1 = '' OR role = $1 ORDER BY at DESC, id DESC`, role)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(r pgx.CollectableRow) (ChoiceRow, error) {
		var c ChoiceRow
		var nums []byte
		err := r.Scan(&c.ID, &c.Role, &c.Kind, &c.Reason, &c.From.Entry, &c.From.Effort, &c.To.Entry, &c.To.Effort, &nums, &c.State, &c.Who,
			&c.At, &c.Decided, &c.HideTill)
		if err == nil {
			json.Unmarshal(nums, &c.Numbers)
		}
		return c, err
	})
}

// Chooser applies the rules of 12.3 for the agents Crew chooses for.
type Chooser struct {
	Pool    *pgxpool.Pool
	Machine settings.Machine
	Now     func() time.Time
}

func (c *Chooser) now() time.Time {
	if c.Now != nil {
		return c.Now()
	}
	return time.Now()
}

// view is what one evaluation reads.
type view struct {
	role  string
	doc   settings.Doc
	rules Rules
	st    *state
	list  []string
	cur   Pick
	stats map[Pick]*stat
	units []BuilderUnit
	// checks are an agent that checks' runs, its units.
	checks []CheckerRun
	// blocked are the picks gone back from in the last retry_days.
	blocked map[Pick]bool
	// lastAt is the newest change or going back, which starts the days between changes again.
	lastAt   time.Time
	watching *ChoiceRow
}

func (c *Chooser) read(ctx context.Context, role string) (*view, error) {
	doc, err := settings.Effective(ctx, c.Pool, c.Machine)
	if err != nil {
		return nil, err
	}
	v := &view{role: role, doc: doc, rules: RulesOf(doc), blocked: map[Pick]bool{}}
	if v.st, err = load(ctx, c.Pool, doc); err != nil {
		return nil, err
	}
	entry, _ := dig(doc, "agents", "roles", role).(settings.Doc)
	v.list = strs(entry["model"])
	v.cur = Pick{Effort: str(entry["effort"])}
	for _, e := range v.list {
		if v.st.candidate(e).listed {
			v.cur.Entry = e
			break
		}
	}
	rows, err := Rows(ctx, c.Pool)
	if err != nil {
		return nil, err
	}
	since := c.now().AddDate(0, 0, -v.rules.WindowDays)
	if checks(role) {
		if v.checks, err = CheckerRunsOf(ctx, c.Pool, role, since); err != nil {
			return nil, err
		}
		v.stats = checkerStats(v.checks, realOf(rows))
	} else {
		if v.units, err = BuilderTickets(ctx, c.Pool, since); err != nil {
			return nil, err
		}
		v.stats = statsOf(v.units, realOf(rows))
	}
	choices, err := Choices(ctx, c.Pool, role)
	if err != nil {
		return nil, err
	}
	for _, ch := range choices {
		if ch.Kind == "back" && c.now().Sub(ch.At) < time.Duration(v.rules.RetryDays)*24*time.Hour {
			v.blocked[ch.From] = true
		}
		if (ch.Kind == "change" || ch.Kind == "back") && ch.At.After(v.lastAt) {
			v.lastAt = ch.At
		}
		if ch.Kind == "change" && ch.State == "watching" && v.watching == nil {
			ch := ch
			v.watching = &ch
		}
	}
	return v, nil
}

// canRun says a pick's entry is offered and can start now, and is not close to its limit.
func (v *view) canRun(p Pick) bool {
	c := v.st.candidate(p.Entry)
	return c.runnable && !c.close && !v.st.guard.Close(c.a, c.rows[0].Model)
}

// checksCanRun says another family than a builder pick's can run the checks now: the reviewer's
// list has a runnable entry of another family (12.3, the brakes). An agent that checks is moved off
// the builder's family when it runs, by the rule for checks.
func (v *view) checksCanRun(p Pick) bool {
	if checks(v.role) {
		return true
	}
	fam := v.st.candidate(p.Entry).family()
	reviewer, _ := dig(v.doc, "agents", "roles", "reviewer").(settings.Doc)
	for _, e := range strs(reviewer["model"]) {
		if c := v.st.candidate(e); c.runnable && v.st.allowedForAsk(Ask{Role: "reviewer"}, true, false, c.cli, c.model) && c.family() != fam {
			return true
		}
	}
	return false
}

// best is the pick the rules would change to from the current one, with the reason and the card, or
// false when there is none.
func (v *view) best(cur Pick) (Pick, string, Card, bool) {
	from := v.stats[cur]
	if from == nil || from.Units < v.rules.MinRuns {
		return Pick{}, "", Card{}, false
	}
	type cand struct {
		p      Pick
		reason string
		s      stat
	}
	var cs []cand
	for p, s := range v.stats {
		if p == cur || s.Units < v.rules.MinRuns || v.blocked[p] || !slices.Contains(v.list, p.Entry) ||
			!v.st.allowedForAsk(Ask{Role: v.role}, slices.Contains(CheckRoles, v.role), false, "", p.Entry) ||
			!v.canRun(p) || !v.checksCanRun(p) {
			continue
		}
		if r := v.rules.verdictFor(v.role, *from, *s); r != "" {
			cs = append(cs, cand{p, r, *s})
		}
	}
	if len(cs) == 0 {
		return Pick{}, "", Card{}, false
	}
	// The best first: for the builder the most passed, for an agent that checks the fewest escaped.
	slices.SortFunc(cs, func(a, b cand) int {
		if checks(v.role) {
			if ea, eb := a.s.escapedShare(), b.s.escapedShare(); ea != eb {
				if ea < eb {
					return -1
				}
				return 1
			}
		} else if a.s.passed() != b.s.passed() {
			if a.s.passed() > b.s.passed() {
				return -1
			}
			return 1
		}
		return strings.Compare(a.p.String(), b.p.String())
	})
	return cs[0].p, cs[0].reason, Card{From: cur, To: cs[0].p, FromStats: *from, ToStats: cs[0].s}, true
}

// Evaluate applies the rules of 12.3 for one agent: it ends a comparison that has its units, goes
// back from a change whose watched units are worse or keeps it, and changes to a better entry or
// effort when the rules and the brakes allow; for an agent the operator set it writes what Crew would
// choose. Each of these is a row of model_choices, which the news reads. Nothing here starts or stops
// a run.
func (c *Chooser) Evaluate(ctx context.Context, role string) error {
	if !slices.Contains(ChosenRoles, role) {
		return nil
	}
	// Pause all holds Crew's own changes of a model too (12.3, 7.9).
	if st, err := hold.Read(ctx, c.Pool); err != nil || st.PausedAt != nil {
		return err
	}
	v, err := c.read(ctx, role)
	if err != nil || v.cur.Entry == "" {
		return err
	}
	if err := c.endComparison(ctx, v, role); err != nil {
		return err
	}
	if v.watching != nil {
		// An agent the operator set meanwhile is theirs: Crew stops watching its change, and never
		// goes back on it (AGENTS.md rule 15, security review of M8).
		if !v.rules.Automatic(role) {
			_, err := c.Pool.Exec(ctx, `UPDATE model_choices SET state = 'superseded', decided_at = now() WHERE id = $1 AND state = 'watching'`, v.watching.ID)
			return err
		}
		return c.watch(ctx, v, role)
	}
	to, reason, card, ok := v.best(v.cur)
	if !ok {
		return nil
	}
	if !v.rules.Automatic(role) {
		return c.recommend(ctx, v, role, to, reason, card)
	}
	if !v.lastAt.IsZero() && c.now().Sub(v.lastAt) < time.Duration(v.rules.MinDaysBetween)*24*time.Hour {
		return nil
	}
	return c.change(ctx, v, role, to, reason, card, "crew")
}

// watch looks at the units on the new pick since a change: once there are watch_runs of them, a
// worse result takes Crew back, and anything else keeps the change.
func (c *Chooser) watch(ctx context.Context, v *view, role string) error {
	ch := v.watching
	var w stat
	for _, u := range v.units {
		if u.Started.Before(ch.At) || (Pick{u.CLI + "/" + u.Alias, u.Effort}) != ch.To {
			continue
		}
		w.Units++
		if u.Passed {
			w.Passed++
		}
	}
	for _, u := range v.checks {
		if u.Started.Before(ch.At) || (Pick{u.CLI + "/" + u.Alias, u.Effort}) != ch.To {
			continue
		}
		w.Units++
		if u.AskedFix {
			w.AskedFix++
		}
		w.Escaped += u.Escaped
	}
	if w.Units < v.rules.WatchRuns {
		return nil
	}
	if !v.rules.worseFor(role, ch.Reason, ch.Numbers.FromStats, w) {
		_, err := c.Pool.Exec(ctx, `UPDATE model_choices SET state = 'kept', decided_at = now() WHERE id = $1`, ch.ID)
		return err
	}
	card := ch.Numbers
	card.Watched = &w
	return c.goBack(ctx, v, role, *ch, "worse", card, "crew")
}

// GoBack is the operator's Go back on a change: the earlier pick runs again, the agent stays
// Automatic, and the pick gone back from is neither chosen nor tried for retry_days.
func (c *Chooser) GoBack(ctx context.Context, id int64, who string) error {
	var role string
	if err := c.Pool.QueryRow(ctx, `SELECT role FROM model_choices WHERE id = $1 AND kind = 'change'`, id).Scan(&role); err != nil {
		return err
	}
	v, err := c.read(ctx, role)
	if err != nil {
		return err
	}
	chs, err := Choices(ctx, c.Pool, role)
	if err != nil {
		return err
	}
	for _, ch := range chs {
		if ch.ID == id {
			if ch.State != "watching" && ch.State != "kept" {
				return errors.New("that change was gone back from already")
			}
			return c.goBack(ctx, v, role, ch, "operator", ch.Numbers, who)
		}
	}
	return pgx.ErrNoRows
}

func (c *Chooser) goBack(ctx context.Context, v *view, role string, ch ChoiceRow, reason string, card Card, who string) error {
	return notAutomaticIsNothing(pgx.BeginFunc(ctx, c.Pool, func(tx pgx.Tx) error {
		if err := c.setFirst(ctx, tx, v, role, ch.From, who); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `UPDATE model_choices SET state = 'undone', decided_at = now() WHERE id = $1`, ch.ID); err != nil {
			return err
		}
		return insertChoice(ctx, tx, role, "back", reason, ch.To, ch.From, card, "done", who)
	}))
}

// notAutomaticIsNothing makes Crew's refused change of an agent the operator set no error: nothing
// was changed, as it should be.
func notAutomaticIsNothing(err error) error {
	if errors.Is(err, errNotAutomatic) {
		return nil
	}
	return err
}

func (c *Chooser) change(ctx context.Context, v *view, role string, to Pick, reason string, card Card, who string) error {
	return notAutomaticIsNothing(pgx.BeginFunc(ctx, c.Pool, func(tx pgx.Tx) error {
		if err := c.setFirst(ctx, tx, v, role, to, who); err != nil {
			return err
		}
		return insertChoice(ctx, tx, role, "change", reason, v.cur, to, card, "watching", who)
	}))
}

// recommend writes what Crew would choose for an agent the operator set, unless the same was shown
// and hidden with Not now, or is open already.
func (c *Chooser) recommend(ctx context.Context, v *view, role string, to Pick, reason string, card Card) error {
	chs, err := Choices(ctx, c.Pool, role)
	if err != nil {
		return err
	}
	for _, ch := range chs {
		if ch.Kind != "recommend" || ch.To != to {
			continue
		}
		if ch.State == "open" || (ch.State == "dismissed" && ch.HideTill != nil && ch.HideTill.After(c.now())) {
			return nil
		}
	}
	return pgx.BeginFunc(ctx, c.Pool, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `UPDATE model_choices SET state = 'dismissed', decided_at = now() WHERE role = $1 AND kind = 'recommend' AND state = 'open'`, role); err != nil {
			return err
		}
		return insertChoice(ctx, tx, role, "recommend", reason, v.cur, to, card, "open", "crew")
	})
}

// UseIt takes the pick a recommendation names; the agent stays Set by you.
func (c *Chooser) UseIt(ctx context.Context, id int64, who string) error {
	ch, err := c.choice(ctx, id, "recommend", "open")
	if err != nil {
		return err
	}
	v, err := c.read(ctx, ch.Role)
	if err != nil {
		return err
	}
	return pgx.BeginFunc(ctx, c.Pool, func(tx pgx.Tx) error {
		if err := c.setFirst(ctx, tx, v, ch.Role, ch.To, who); err != nil {
			return err
		}
		_, err := tx.Exec(ctx, `UPDATE model_choices SET state = 'used', decided_at = now(), who = $2 WHERE id = $1`, id, who)
		return err
	})
}

// NotNow hides a recommendation for hide_days.
func (c *Chooser) NotNow(ctx context.Context, id int64, who string) error {
	ch, err := c.choice(ctx, id, "recommend", "open")
	if err != nil {
		return err
	}
	doc, err := settings.Effective(ctx, c.Pool, c.Machine)
	if err != nil {
		return err
	}
	_, err = c.Pool.Exec(ctx, `UPDATE model_choices SET state = 'dismissed', decided_at = now(), who = $2, hide_until = $3 WHERE id = $1`,
		ch.ID, who, c.now().AddDate(0, 0, RulesOf(doc).HideDays))
	return err
}

func (c *Chooser) choice(ctx context.Context, id int64, kind, state string) (ChoiceRow, error) {
	all, err := Choices(ctx, c.Pool, "")
	if err != nil {
		return ChoiceRow{}, err
	}
	for _, ch := range all {
		if ch.ID == id {
			if ch.Kind != kind || ch.State != state {
				return ChoiceRow{}, fmt.Errorf("that is a %s that is %s", ch.Kind, ch.State)
			}
			return ch, nil
		}
	}
	return ChoiceRow{}, pgx.ErrNoRows
}

func insertChoice(ctx context.Context, tx pgx.Tx, role, kind, reason string, from, to Pick, card Card, state, who string) error {
	b, err := json.Marshal(card)
	if err != nil {
		return err
	}
	_, err = tx.Exec(ctx, `INSERT INTO model_choices (role, kind, reason, from_entry, from_effort, to_entry, to_effort, numbers, state, who)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10)`, role, kind, reason, from.Entry, from.Effort, to.Entry, to.Effort, b, state, who)
	return err
}

// errNotAutomatic says the agent was set by the operator meanwhile: Crew changes nothing of it.
var errNotAutomatic = errors.New("the agent is set by the operator")

// setFirst puts a pick first in the agent's list and sets its effort, in the machine's settings as
// they are under the settings' lock. Crew's own change of an agent the operator set meanwhile is
// refused (AGENTS.md rule 15).
func (c *Chooser) setFirst(ctx context.Context, tx pgx.Tx, _ *view, role string, p Pick, who string) error {
	return settings.UpdateTx(ctx, tx, c.Machine, who, func(doc settings.Doc) error {
		if who == "crew" && !RulesOf(doc).Automatic(role) {
			return errNotAutomatic
		}
		agents, _ := doc["agents"].(settings.Doc)
		roles, _ := agents["roles"].(settings.Doc)
		entry, ok := roles[role].(settings.Doc)
		if !ok {
			return fmt.Errorf("no agent %s", role)
		}
		list := []any{p.Entry}
		for _, e := range strs(entry["model"]) {
			if e != p.Entry {
				list = append(list, e)
			}
		}
		entry["model"], entry["effort"] = list, p.Effort
		return nil
	})
}

// Steer is where a new unit of an agent goes, when Crew sends it elsewhere than the agent's own
// pick: a try, or the other pick of a comparison. key names the unit (a ticket for the builder), so
// the same unit is always steered the same way.
func (c *Chooser) Steer(ctx context.Context, role, key string, pinned bool) (Pick, string, error) {
	if !slices.Contains(AutoRoles, role) || pinned {
		return Pick{}, "", nil
	}
	v, err := c.read(ctx, role)
	if err != nil || v.cur.Entry == "" {
		return Pick{}, "", err
	}
	// A comparison: every second unit goes to the other pick, while it can run.
	if cmp, err := running(ctx, c.Pool, role); err != nil {
		return Pick{}, "", err
	} else if cmp != nil {
		// A unit is a ticket for the builder, and a run for an agent that checks.
		var n int
		q := `SELECT count(DISTINCT s.ticket) FROM subtasks s JOIN runs r ON r.subtask = s.id
			WHERE s.kind = 'build' AND s.role = $1 AND r.started_at >= $2`
		if checks(role) {
			q = `SELECT count(*) FROM subtasks s JOIN runs r ON r.subtask = s.id WHERE s.role = $1 AND s.ticket IS NOT NULL AND r.started_at >= $2`
		}
		if err := c.Pool.QueryRow(ctx, q, role, cmp.Started).Scan(&n); err != nil {
			return Pick{}, "", err
		}
		if n%2 == 0 && v.canRun(cmp.Other) {
			return cmp.Other, FromComparison, nil
		}
		return Pick{}, "", nil
	}
	// A try: one unit in explore_share of an Automatic agent, never while a change is watched.
	if !slices.Contains(ChosenRoles, role) || !v.rules.Automatic(role) || v.watching != nil || v.rules.ExploreShare <= 0 {
		return Pick{}, "", nil
	}
	h := fnv.New32a()
	h.Write([]byte(role + "/" + key))
	if float64(h.Sum32()%1000) >= v.rules.ExploreShare*1000 {
		return Pick{}, "", nil
	}
	tries := 0
	for _, u := range v.units {
		if u.From == FromTry {
			tries++
		}
	}
	for _, u := range v.checks {
		if u.From == FromTry {
			tries++
		}
	}
	// In turn: another entry of the list at the agent's effort, and the own entry at the next lower
	// effort.
	var others []Pick
	for _, e := range v.list {
		if p := (Pick{e, v.cur.Effort}); e != v.cur.Entry && v.st.allowedForAsk(Ask{Role: role}, slices.Contains(CheckRoles, role), false, "", e) && !v.blocked[p] && v.canRun(p) && v.checksCanRun(p) {
			others = append(others, p)
		}
	}
	var lower []Pick
	if c := v.st.candidate(v.cur.Entry); c.listed && v.st.allowedForAsk(Ask{Role: role}, slices.Contains(CheckRoles, role), false, c.cli, c.model) {
		at := slices.Index(cli.EffortOrder, v.cur.Effort)
		for i := at - 1; i >= 0; i-- {
			name, eff, _, nearest := pickEffort(c.rows, cli.EffortOrder[i])
			if !nearest && name != "" {
				if p := (Pick{v.cur.Entry, eff}); !v.blocked[p] {
					lower = append(lower, p)
				}
				break
			}
		}
	}
	if tries%2 == 1 && len(lower) > 0 || len(others) == 0 && len(lower) > 0 {
		return lower[0], FromTry, nil
	}
	if len(others) > 0 {
		return others[(tries/2)%len(others)], FromTry, nil
	}
	return Pick{}, "", nil
}
