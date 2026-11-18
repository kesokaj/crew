package models

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/kesokaj/crew/internal/cli"
	"github.com/kesokaj/crew/internal/db"
	"github.com/kesokaj/crew/internal/settings"
)

// Where a run's model and effort came from (15.3, the run record).
const (
	FromTicket     = "ticket"     // a pin on the ticket
	FromProject    = "project"    // the project's setting for the role
	FromOperator   = "operator"   // the machine's entry, for an agent the operator set
	FromAutomatic  = "automatic"  // the machine's entry, Crew's automatic choice
	FromList       = "list"       // the machine's entry, for an agent that follows its list
	FromTry        = "try"        // one run in ten of an automatic agent
	FromComparison = "comparison" // a comparison the operator started
	FromCheckRule  = "check_rule" // the rule for checks moved a check off the builder's family
	FromLimit      = "limit"      // the first choice's CLI was signed out or its group at or close to a limit
	FromNearest    = "nearest"    // the model does not offer the effort that was asked for
)

// WaitBracketHasRoom is the phrase in a wait's reason, and on a ticket's stuck line, when an operator's
// choice of a model waits on its limit group while another model in its bracket has room (12.7, 7.8).
const WaitBracketHasRoom = "while its bracket has room on "

// AutoRoles are the agents whose work Crew can measure, and chooses for (12.3). The others follow
// their list.
var AutoRoles = []string{"builder", "reviewer", "tester", "e2e", "verifier"}

// CheckRoles run on another family than the builder whose work they check (12.3, R191).
var CheckRoles = []string{"reviewer", "verifier", "judge"}

// Ask is a run to choose for.
type Ask struct {
	Role             string
	Machine, Project settings.Doc
	// Pin is the ticket's own entry for the role: a model with its CLI, an effort, or both.
	Pin settings.Doc
	// Avoid is the family of the builder's last run on the ticket, for a role that checks.
	Avoid string
	// Prefer, when set, is an entry Crew wants this run on instead of the machine's first, such as a
	// try or a comparison, with where it came from. It applies only at the machine's level, and only
	// when it can run.
	Prefer, PreferFrom string
	// PreferEffort is the effort that goes with Prefer, unless a pin or the project sets one.
	PreferEffort string
	// Bracket, when set, prefers the first listed entry of that bracket on the role's list that has
	// room, at the machine's level when the operator did not set the role (such as flash for smaller
	// builder tasks with distinct rules, 12.3).
	Bracket string
}

// Choice is the CLI, the model, and the effort a run starts on, and where they came from.
type Choice struct {
	CLI   string
	Model string // as the CLI lists it, without the effort
	// Effort is the effort the run gets; "" for a model with no levels.
	Effort string
	// Name is passed to the CLI as its model, exactly as the CLI lists it.
	Name string
	// Flag says the effort is passed as a flag; agy has it in the name (12.3).
	Flag                  bool
	ModelFrom, EffortFrom string
	// Bracket is the name of the chosen model's bracket in agents.brackets, or the CLI/model entry
	// itself when the model is in no bracket (12.3).
	Bracket string
	// Off says the rule for checks could not be met: no model of another known family is offered or can
	// run now, so the check runs on the builder's family and Crew records that notice (12.3, 12.7).
	Off bool
	// Ignored are entries of a pin or a project's setting that name a model no CLI lists now: the
	// machine's entry ran in their place, and the operator gets news (12.3).
	Ignored []string
	// Short says the CLI's list was never read, and its model comes from the adapter's short list.
	Short bool
}

// Entry is the CLI and model of a choice, as the settings write it.
func (c Choice) Entry() string { return c.CLI + "/" + c.Model }

// WaitError says a run cannot start now and waits: why, and until when when that is known.
type WaitError struct {
	Reason string
	Until  time.Time
}

func (e *WaitError) Error() string { return e.Reason }

// IsWait says err is a WaitError.
func IsWait(err error) (*WaitError, bool) {
	var w *WaitError
	ok := errors.As(err, &w)
	return w, ok
}

// state is what a choice reads: the CLIs that may run, what they offer, the brackets, and the quota guard.
type state struct {
	inUse    []string
	signedIn map[string]bool
	offered  map[string][]Row // by CLI: what is offered now, the operator's names with them
	// read says the CLI's own list was read once; until then its short list stands in (12.3).
	read     map[string]bool
	guard    Guard
	slowDown float64
	// working counts the runs that work now on each group, and on each model with a limit of its
	// own, by the guard's keys.
	working map[string]int
	// brackets maps a bracket name from agents.brackets to its CLI/model entries in order;
	// bracketOf maps a CLI/model entry to its bracket name.
	brackets     map[string][]string
	bracketOf    map[string]string
	opEditedRole map[string]bool
}

func load(ctx context.Context, pool db.Querier, machine settings.Doc) (*state, error) {
	st := &state{
		inUse:     strs(dig(machine, "agents", "clis")),
		signedIn:  map[string]bool{},
		offered:   map[string][]Row{},
		brackets:  map[string][]string{},
		bracketOf: map[string]string{},
	}
	if bmap, ok := dig(machine, "agents", "brackets").(settings.Doc); ok {
		names := make([]string, 0, len(bmap))
		for name := range bmap {
			names = append(names, name)
		}
		slices.Sort(names)
		for _, name := range names {
			entries := strs(bmap[name])
			st.brackets[name] = entries
			for _, e := range entries {
				if st.bracketOf[e] == "" {
					st.bracketOf[e] = name
				}
			}
		}
	}
	rows, err := pool.Query(ctx, `SELECT cli FROM cli_logins WHERE state = 'signed_in'`)
	if err != nil {
		return nil, err
	}
	// A read that fails is an error, never no CLI signed in (since 1.15).
	for rows.Next() {
		var c string
		if err := rows.Scan(&c); err != nil {
			rows.Close()
			return nil, err
		}
		st.signedIn[c] = true
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	all, err := Rows(ctx, pool)
	if err != nil {
		return nil, err
	}
	st.read = map[string]bool{}
	now := time.Now()
	for _, r := range all {
		if r.AddedBy == "cli" {
			st.read[r.CLI] = true
		}
		if r.Offered && !r.PassedOver(now) {
			st.offered[r.CLI] = append(st.offered[r.CLI], r)
		}
	}
	if st.opEditedRole, err = loadOperatorEditedRoles(ctx, pool, all, st.inUse, now); err != nil {
		return nil, err
	}
	st.slowDown, _ = dig(machine, "quota", "slow_down_at").(float64)
	if st.guard, err = LoadGuard(ctx, pool, st.slowDown); err != nil {
		return nil, err
	}
	st.working = map[string]int{}
	rows, err = pool.Query(ctx, `SELECT cli, coalesce(model_resolved, model, '') FROM runs WHERE state IN ('starting', 'working')`)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var c, m string
		if rows.Scan(&c, &m) != nil {
			continue
		}
		if a, ok := cli.Get(c); ok {
			for _, k := range keys(a, m) {
				st.working[k]++
			}
		}
	}
	rows.Close()
	return st, rows.Err()
}

// rowsOf are the offered names of one model of a CLI, or of the CLI's short list when its list was
// never read. A model may be named with its effort, as agy lists it: then only that name.
func (st *state) rowsOf(a cli.Adapter, model string) (rows []Row, short bool) {
	list := append([]Row(nil), st.offered[a.Name()]...)
	if !st.read[a.Name()] {
		short = true
		for _, m := range a.ShortList() {
			list = append(list, Row{CLI: a.Name(), Name: m.Name, Model: m.Model, Efforts: m.Efforts, InName: m.InName,
				Family: cli.Family(m.Model), Group: a.LimitGroup(m.Model), Offered: true})
		}
	}
	for _, r := range list {
		if r.Model == model {
			rows = append(rows, r)
		}
	}
	if len(rows) == 0 {
		for _, r := range list {
			if r.Name == model {
				return []Row{r}, short
			}
		}
	}
	return rows, short
}

// candidate is one entry of a list, as it stands now.
type candidate struct {
	cli, model string
	a          cli.Adapter
	rows       []Row
	short      bool
	// listed says the CLI is in use and offers the model; runnable that it can start now.
	listed, runnable bool
	// why says why a listed entry cannot run now, and until when.
	why   string
	until time.Time
	// slow says its group is at or above quota.slow_down_at (12.7).
	slow bool
	// close says its group is above quota.slow_down_at while a run works on it: the guard holds new
	// runs back there when no other group in the bracket has room (12.7).
	close bool
}

func (st *state) candidate(entry string) candidate {
	c := candidate{}
	var ok bool
	c.cli, c.model, ok = strings.Cut(entry, "/")
	if !ok {
		return c
	}
	if c.a, ok = cli.Get(c.cli); !ok || (len(st.inUse) > 0 && !slices.Contains(st.inUse, c.cli)) {
		return c
	}
	c.rows, c.short = st.rowsOf(c.a, c.model)
	if len(c.rows) == 0 {
		return c
	}
	c.listed = true
	c.model = c.rows[0].Model
	switch until, limited := st.guard.Limited(c.a, c.rows[0].Model); {
	case !st.signedIn[c.cli]:
		c.why = c.cli + " is not signed in"
	case limited:
		c.why, c.until = fmt.Sprintf("%s's %s are at their limit", c.cli, groupName(c.a, c.rows[0].Model)), until
	default:
		c.runnable = true
	}
	if st.guard.Close(c.a, c.rows[0].Model) {
		c.slow = true
		for _, k := range keys(c.a, c.rows[0].Model) {
			if st.working[k] > 0 {
				c.close = true
			}
		}
	}
	return c
}

func (c candidate) entry() string { return c.cli + "/" + c.model }

func (c candidate) hasRoom() bool { return c.runnable && !c.slow }

func groupName(a cli.Adapter, model string) string {
	if m := a.ModelLimit(model); m != "" {
		return a.GroupName(a.LimitGroup(model)) + ", or " + m + " alone,"
	}
	return a.GroupName(a.LimitGroup(model))
}

func (c candidate) family() string {
	if len(c.rows) > 0 {
		return cli.Family(c.rows[0].Model)
	}
	return cli.Family(c.model)
}

func (c candidate) otherFamily(avoid string) bool {
	f := c.family()
	return cli.Known(f) && f != avoid
}

// Choose chooses the CLI, the model, and the effort of a run (12.3, 12.7). The model and the effort
// are resolved one by one: a pin on the ticket, then the project's setting for the role, then the
// machine's entry. An entry that names a model no CLI lists is ignored. The operator's choice of a
// model (a pin, a project's setting, or an agent in set_by_you / with auto.on false) is never moved
// at a limit or above quota.slow_down_at; when its group is at its limit while another model in its
// bracket has room, the wait reason says so. Otherwise, when a model's group is at its limit or at or
// above quota.slow_down_at, the run moves to a model of the same bracket in another group that has
// room. For a reviewer, a verifier, or a judge, Crew prefers a known family other than the builder's
// when that family can run; when no model of another known family is offered or can run (at its limit),
// Crew falls back to a model of the same bracket in the builder's family that has room and sets Off so
// the run records a notice.
func Choose(ctx context.Context, pool db.Querier, ask Ask) (Choice, error) {
	st, err := load(ctx, pool, ask.Machine)
	if err != nil {
		return Choice{}, err
	}
	return st.choose(ask)
}

func (st *state) roleInputs(ask Ask) (machine settings.Doc, list []string, machineFrom string, own settings.Doc) {
	roleKey := ask.Role
	machine, _ = dig(ask.Machine, "agents", "roles", ask.Role).(settings.Doc)
	if machine == nil && ask.Role == "curator" {
		machine, _ = dig(ask.Machine, "agents", "roles", "lead").(settings.Doc)
		if machine != nil {
			roleKey = "lead"
		}
	} else if machine == nil && ask.Role == "lead" {
		machine, _ = dig(ask.Machine, "agents", "roles", "curator").(settings.Doc)
		if machine != nil {
			roleKey = "curator"
		}
	} else if machine == nil && (ask.Role == "coordinator" || ask.Role == "researcher") {
		machine, _ = dig(ask.Machine, "agents", "roles", "planner").(settings.Doc)
		if machine != nil {
			roleKey = "planner"
		}
	} else if machine == nil && ask.Role == "dreamer" {
		machine, _ = dig(ask.Machine, "agents", "roles", "improver").(settings.Doc)
		if machine != nil {
			roleKey = "improver"
		}
	} else if machine == nil && ask.Role != "" {
		fallback := "planner"
		if ask.Bracket == "flash" {
			fallback = "tester"
		}
		machine, _ = dig(ask.Machine, "agents", "roles", fallback).(settings.Doc)
		if machine != nil {
			roleKey = fallback
		}
	}
	list = strs(machine["model"])
	machineFrom = FromList
	if slices.Contains(AutoRoles, ask.Role) {
		machineFrom = FromAutomatic
	}
	auto, autoSet := dig(ask.Machine, "agents", "auto", "on").(bool)
	setByYou := strs(dig(ask.Machine, "agents", "auto", "set_by_you"))
	if (autoSet && !auto) || slices.Contains(setByYou, ask.Role) || slices.Contains(setByYou, roleKey) || st.opEditedRole[ask.Role] || st.opEditedRole[roleKey] {
		machineFrom = FromOperator
	}
	own, _ = dig(ask.Project, "agents", ask.Role).(settings.Doc)
	if own == nil && ask.Role == "curator" {
		own, _ = dig(ask.Project, "agents", "lead").(settings.Doc)
	} else if own == nil && ask.Role == "lead" {
		own, _ = dig(ask.Project, "agents", "curator").(settings.Doc)
	}
	return machine, list, machineFrom, own
}

func (st *state) choose(ask Ask) (Choice, error) {
	machine, list, machineFrom, own := st.roleInputs(ask)
	atLimit := str(dig(ask.Machine, "agents", "at_limit"))
	if v := str(dig(ask.Project, "agents", "at_limit")); v != "" {
		atLimit = v
	}
	checkRule := CheckRule(ask)
	checks := checkRule && ask.Avoid != "" && slices.Contains(CheckRoles, ask.Role)

	var ch Choice
	var first candidate
	var from string
	first, from, ch.Ignored = st.selectFirst(ask, own, list, checks, machineFrom)
	if from == "" {
		return Choice{}, &WaitError{Reason: fmt.Sprintf("no model of the %s is offered: %v", ask.Role, list)}
	}

	// The effort, by the same three levels.
	effort, effortFrom := str(ask.Pin["effort"]), FromTicket
	if effort == "" {
		effort, effortFrom = str(own["effort"]), FromProject
	}
	if effort == "" {
		effort, effortFrom = str(machine["effort"]), machineFrom
		if from == ask.PreferFrom && ask.Prefer != "" && first.entry() == ask.Prefer {
			effortFrom = from
			if ask.PreferEffort != "" {
				effort = ask.PreferEffort
			}
		}
	}

	operatorChoice := from == FromTicket || from == FromProject || from == FromOperator
	inBracket := st.bracketCandidates(ask, first, list, operatorChoice)
	chosen := first

	if !checks {
		var err error
		chosen, from, err = st.chooseNonCheck(ask, first, from, operatorChoice, atLimit, inBracket, list)
		if err != nil {
			return Choice{}, err
		}
	} else {
		var err error
		chosen, from, ch.Off, err = st.chooseCheck(ask, first, from, machineFrom, operatorChoice, atLimit, inBracket, list)
		if err != nil {
			return Choice{}, err
		}
	}

	// Close to its limit, one run at a time works on a group: the others wait, so the window is shared
	// out instead of spent at once (12.7).
	if chosen.close {
		return Choice{}, &WaitError{Reason: fmt.Sprintf("the %s waits: %s's %s are close to their limit, and a run works there",
			ask.Role, chosen.cli, groupName(chosen.a, chosen.rows[0].Model))}
	}
	name, eff, flag, nearest := pickEffort(chosen.rows, effort)
	if nearest {
		effortFrom = FromNearest
	}
	ch.CLI, ch.Model, ch.Effort, ch.Name, ch.Flag = chosen.cli, chosen.rows[0].Model, eff, name, flag
	ch.ModelFrom, ch.EffortFrom, ch.Short = from, effortFrom, chosen.short
	ch.Bracket = st.bracketOf[chosen.entry()]
	if ch.Bracket == "" {
		ch.Bracket = chosen.entry()
	}
	return ch, nil
}

func (st *state) chooseNonCheck(ask Ask, first candidate, from string, operatorChoice bool, atLimit string, inBracket []candidate, list []string) (candidate, string, error) {
	if operatorChoice {
		if !first.runnable {
			return candidate{}, "", st.operatorWait(ask, first, inBracket, false)
		}
		return first, from, nil
	}
	if first.hasRoom() {
		return first, from, nil
	}
	if atLimit == "wait" {
		if !first.runnable {
			return candidate{}, "", &WaitError{Reason: fmt.Sprintf("the %s waits for %s/%s: %s", ask.Role, first.cli, first.model, first.why), Until: first.until}
		}
		return first, from, nil
	}
	var fallbacks []candidate
	seen := map[string]bool{first.entry(): true}
	for _, alt := range inBracket {
		if st.allowedForAsk(ask, false, false, alt.cli, alt.model) {
			seen[alt.entry()] = true
			fallbacks = append(fallbacks, alt)
		}
	}
	for _, e := range list {
		if c := st.candidate(e); c.listed && !seen[c.entry()] && st.allowedForAsk(ask, false, false, c.cli, c.model) {
			seen[c.entry()] = true
			fallbacks = append(fallbacks, c)
		}
	}
	for _, alt := range fallbacks {
		if alt.hasRoom() {
			return alt, FromLimit, nil
		}
	}
	if !first.runnable {
		for _, alt := range fallbacks {
			if alt.runnable && !alt.close {
				return alt, FromLimit, nil
			}
		}
		return candidate{}, "", &WaitError{Reason: fmt.Sprintf("the %s waits: %s, and no next choice can run", ask.Role, first.why), Until: first.until}
	}
	return first, from, nil
}

// pickEffort chooses the name to pass and the effort for a model's offered names: the one that offers
// the effort asked for, else the nearest one below it, else the nearest above (12.3). A model with no
// levels runs with none.
func pickEffort(rows []Row, want string) (name, effort string, flag, nearest bool) {
	type opt struct {
		name, effort string
		flag         bool
	}
	var opts []opt
	for _, r := range rows {
		if len(r.Efforts) == 0 {
			opts = append(opts, opt{name: r.Name})
			continue
		}
		for _, e := range r.Efforts {
			opts = append(opts, opt{r.Name, e, !r.InName})
		}
	}
	for _, o := range opts {
		if o.effort == want {
			return o.name, o.effort, o.flag, false
		}
	}
	w := slices.Index(cli.EffortOrder, want)
	best, bestD := -1, 0
	for i, o := range opts {
		k := slices.Index(cli.EffortOrder, o.effort)
		if k < 0 {
			continue
		}
		// Below the wanted effort first, the nearest; then above, the nearest.
		d := w - k
		if d < 0 {
			d = 100 - d
		}
		if best < 0 || d < bestD {
			best, bestD = i, d
		}
	}
	if best < 0 {
		return opts[0].name, "", false, want != ""
	}
	o := opts[best]
	return o.name, o.effort, o.flag, true
}

func dig(d settings.Doc, path ...string) any {
	var cur any = d
	for _, k := range path {
		m, ok := cur.(settings.Doc)
		if !ok {
			return nil
		}
		cur = m[k]
	}
	return cur
}

func str(v any) string { s, _ := v.(string); return s }

func strs(v any) []string {
	var out []string
	switch l := v.(type) {
	case []any:
		for _, x := range l {
			if s, ok := x.(string); ok {
				out = append(out, s)
			}
		}
	case []string:
		out = l
	}
	return out
}
