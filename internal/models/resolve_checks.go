package models

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/kesokaj/crew/internal/cli"
	"github.com/kesokaj/crew/internal/db"
	"github.com/kesokaj/crew/internal/settings"
)

// CheckRule is whether the ask's checks keep off the builder's family: agents.checks_use_another_family,
// the project's over the machine's (12.3).
func CheckRule(ask Ask) bool {
	on, _ := dig(ask.Machine, "agents", "checks_use_another_family").(bool)
	if v, ok := dig(ask.Project, "agents", "checks_use_another_family").(bool); ok {
		on = v
	}
	return on
}

func (st *state) modelBracket(cliName, model string) string {
	if cliName == "" {
		if c, m, ok := strings.Cut(model, "/"); ok {
			cliName, model = c, m
		}
	}
	if cliName != "" {
		if b := st.bracketOf[cliName+"/"+model]; b != "" {
			return b
		}
		if a, ok := cli.Get(cliName); ok {
			if rows, _ := st.rowsOf(a, model); len(rows) > 0 {
				if b := st.bracketOf[cliName+"/"+rows[0].Model]; b != "" {
					return b
				}
				var efforts []string
				for _, r := range rows {
					efforts = append(efforts, r.Efforts...)
				}
				return ClassifyBracket(rows[0].Model, efforts)
			}
		}
	} else {
		for _, c := range st.inUse {
			if b := st.bracketOf[c+"/"+model]; b != "" {
				return b
			}
		}
	}
	return ClassifyBracket(model, nil)
}

// allowedForAsk says whether cliName/model may be chosen for ask when operatorChoice is false (12.3, R130):
// Gemini models are allowed for every role; Anthropic models are allowed only on check roles (pro for
// code-ticket reviewer/verifier/stage checks, flash when ask.Bracket == "flash" or ask.Role == "judge")
// unless operatorChoice is true.
func (st *state) allowedForAsk(ask Ask, checks, operatorChoice bool, cliName, model string) bool {
	if operatorChoice {
		return true
	}
	if c, m, ok := strings.Cut(model, "/"); ok {
		if cliName == "" {
			cliName = c
		}
		model = m
	}
	fam := cli.Family(model)
	if checks || slices.Contains(CheckRoles, ask.Role) {
		if fam == "gemini" {
			return true
		}
		if fam == "claude" {
			b := st.modelBracket(cliName, model)
			if ask.Bracket == "flash" || ask.Role == "judge" {
				return b == "flash"
			}
			return b == "pro"
		}
		return false
	}
	return fam == "gemini"
}

func (st *state) hasAllowedInList(ask Ask, list []string, checks, opList bool) bool {
	for _, e := range list {
		if cliName, model, ok := strings.Cut(e, "/"); ok && st.allowedForAsk(ask, checks, opList, cliName, model) {
			return true
		}
	}
	return false
}

func (st *state) selectFirst(ask Ask, own settings.Doc, list []string, checks bool, machineFrom string) (candidate, string, []string) {
	var ignored []string
	for _, lvl := range []struct{ entry, from string }{{str(ask.Pin["model"]), FromTicket}, {str(own["model"]), FromProject}} {
		if lvl.entry == "" {
			continue
		}
		if c := st.candidate(lvl.entry); c.listed {
			return c, lvl.from, ignored
		}
		ignored = append(ignored, lvl.entry)
	}
	if ask.Prefer != "" && machineFrom != FromOperator {
		if c := st.candidate(ask.Prefer); c.hasRoom() && st.allowedForAsk(ask, checks, ask.PreferFrom == FromComparison, c.cli, c.model) && (!checks || c.otherFamily(ask.Avoid)) {
			return c, ask.PreferFrom, ignored
		}
	}
	if ask.Bracket != "" && machineFrom != FromOperator {
		if c, f := st.selectBracketFirst(ask, list, checks, machineFrom); f != "" {
			return c, f, ignored
		}
	}
	hasAllowed := st.hasAllowedInList(ask, list, checks, machineFrom == FromOperator)
	for _, e := range list {
		if c := st.candidate(e); c.listed && (!hasAllowed || st.allowedForAsk(ask, checks, machineFrom == FromOperator, c.cli, c.model)) {
			return c, machineFrom, ignored
		}
	}
	return candidate{}, "", ignored
}

func (st *state) selectBracketFirst(ask Ask, list []string, checks bool, machineFrom string) (candidate, string) {
	skippedSameFamily := false
	for _, e := range list {
		c := st.candidate(e)
		if !c.listed || !st.allowedForAsk(ask, checks, false, c.cli, c.model) || st.modelBracket(c.cli, c.model) != ask.Bracket {
			continue
		}
		if checks && !c.otherFamily(ask.Avoid) {
			if c.runnable {
				skippedSameFamily = true
			}
			continue
		}
		if c.hasRoom() {
			if skippedSameFamily {
				return c, FromCheckRule
			}
			return c, machineFrom
		}
	}
	if checks {
		for _, e := range list {
			c := st.candidate(e)
			if c.listed && st.allowedForAsk(ask, true, false, c.cli, c.model) && st.modelBracket(c.cli, c.model) == ask.Bracket {
				return c, machineFrom
			}
		}
	}
	return candidate{}, ""
}

// bracketCandidates returns the other listed candidates in first's bracket: first any entry of the
// role's own list that belongs to the bracket, in the role's order, then any remaining entry of the
// bracket in agents.brackets allowed for ask (or all listed bracket entries when allowAll is true for
// operator wait messages) (12.3, 12.7).
func (st *state) bracketCandidates(ask Ask, first candidate, roleList []string, allowAll bool) []candidate {
	firstEntry := first.entry()
	bname := st.bracketOf[firstEntry]
	if bname == "" {
		return nil
	}
	seen := map[string]bool{firstEntry: true}
	var out []candidate
	add := func(e string) {
		c := st.candidate(e)
		if !c.listed {
			return
		}
		ce := c.entry()
		if seen[ce] || st.bracketOf[ce] != bname {
			return
		}
		seen[ce] = true
		out = append(out, c)
	}
	for _, e := range roleList {
		add(e)
	}
	isCheck := slices.Contains(CheckRoles, ask.Role)
	for _, e := range st.brackets[bname] {
		if allowAll || st.allowedForAsk(ask, isCheck, false, "", e) {
			add(e)
		}
	}
	return out
}

func (st *state) operatorWait(ask Ask, first candidate, inBracket []candidate, checks bool) error {
	for _, alt := range inBracket {
		if alt.hasRoom() && (!checks || cli.Known(alt.family())) {
			return &WaitError{
				Reason: fmt.Sprintf("the %s waits for %s/%s (%s), %s%s/%s", ask.Role, first.cli, first.model, first.why, WaitBracketHasRoom, alt.cli, alt.model),
				Until:  first.until,
			}
		}
	}
	return &WaitError{Reason: fmt.Sprintf("the %s waits for %s/%s: %s", ask.Role, first.cli, first.model, first.why), Until: first.until}
}

func (st *state) allowedInCheckList(ask Ask, opList, hasAllowed bool, c candidate, list []string) bool {
	if !c.listed {
		return false
	}
	if slices.Contains(list, c.entry()) {
		return !hasAllowed || st.allowedForAsk(ask, true, opList, c.cli, c.model)
	}
	return st.allowedForAsk(ask, true, false, c.cli, c.model)
}

func (st *state) chooseCheck(ask Ask, first candidate, from, machineFrom string, operatorChoice bool, atLimit string, inBracket []candidate, list []string) (candidate, string, bool, error) {
	if from == FromTicket && !cli.Known(first.family()) {
		if !first.runnable {
			return candidate{}, "", false, st.operatorWait(ask, first, inBracket, true)
		}
		return first, from, false, nil
	}
	if operatorChoice && first.otherFamily(ask.Avoid) {
		if !first.runnable {
			return candidate{}, "", false, st.operatorWait(ask, first, inBracket, true)
		}
		return first, from, false, nil
	}
	if !first.runnable && atLimit == "wait" {
		return candidate{}, "", false, &WaitError{Reason: fmt.Sprintf("the %s waits for %s/%s: %s", ask.Role, first.cli, first.model, first.why), Until: first.until}
	}
	opList := machineFrom == FromOperator
	hasAllowed := st.hasAllowedInList(ask, list, true, opList)
	if alt, f, ok := st.pickOtherFamily(ask, first, from, opList, hasAllowed, atLimit, inBracket, list); ok {
		return alt, f, false, nil
	}
	return st.fallbackSameFamily(ask, first, from, opList, hasAllowed, atLimit, inBracket, list)
}

func (st *state) pickOtherFamily(ask Ask, first candidate, from string, opList, hasAllowed bool, atLimit string, inBracket []candidate, list []string) (candidate, string, bool) {
	if first.otherFamily(ask.Avoid) && first.hasRoom() {
		return first, from, true
	}
	var otherPool []candidate
	seen := map[string]bool{first.entry(): true}
	addOther := func(c candidate) {
		if seen[c.entry()] || !st.allowedInCheckList(ask, opList, hasAllowed, c, list) {
			return
		}
		seen[c.entry()] = true
		otherPool = append(otherPool, c)
	}
	if first.otherFamily(ask.Avoid) {
		if atLimit != "wait" {
			for _, c := range inBracket {
				addOther(c)
			}
			for _, e := range list {
				addOther(st.candidate(e))
			}
		}
	} else {
		if ask.Bracket != "" {
			for _, c := range inBracket {
				if slices.Contains(list, c.entry()) {
					addOther(c)
				}
			}
		}
		for _, e := range list {
			addOther(st.candidate(e))
		}
	}
	moveFrom := FromLimit
	if !first.otherFamily(ask.Avoid) && first.runnable {
		moveFrom = FromCheckRule
	}
	for _, alt := range otherPool {
		if alt.otherFamily(ask.Avoid) && alt.hasRoom() {
			return alt, moveFrom, true
		}
	}
	// When every offered model of another family is at or above quota.slow_down_at, yield to a model of
	// the builder's family in the same bracket when one has room (safety rule 15, 12.7); if the builder's
	// family has no model with room either, keep the runnable cross-family model below.
	if atLimit != "wait" && st.sameFamilyHasRoom(ask, first, opList, hasAllowed, inBracket, list) {
		return candidate{}, "", false
	}
	if first.otherFamily(ask.Avoid) && first.runnable && !first.close {
		return first, from, true
	}
	for _, alt := range otherPool {
		if alt.otherFamily(ask.Avoid) && alt.runnable && !alt.close {
			return alt, moveFrom, true
		}
	}
	if first.otherFamily(ask.Avoid) && first.runnable {
		return first, from, true
	}
	for _, alt := range otherPool {
		if alt.otherFamily(ask.Avoid) && alt.runnable {
			return alt, moveFrom, true
		}
	}
	return candidate{}, "", false
}

func (st *state) fallbackSameFamily(ask Ask, first candidate, from string, opList, hasAllowed bool, atLimit string, inBracket []candidate, list []string) (candidate, string, bool, error) {
	var waitOther *candidate
	for _, e := range list {
		if c := st.candidate(e); st.allowedInCheckList(ask, opList, hasAllowed, c, list) && c.otherFamily(ask.Avoid) {
			waitOther = &c
			break
		}
	}
	if waitOther != nil && atLimit == "wait" {
		return candidate{}, "", false, &WaitError{
			Reason: fmt.Sprintf("the %s waits for %s/%s, of another family than the builder's: %s", ask.Role, waitOther.cli, waitOther.model, waitOther.why),
			Until:  waitOther.until,
		}
	}
	if !cli.Known(ask.Avoid) {
		if !first.runnable {
			return candidate{}, "", false, &WaitError{Reason: fmt.Sprintf("the %s waits: %s, and no next choice can run", ask.Role, first.why), Until: first.until}
		}
		return first, from, false, nil
	}
	fallbackFrom := from
	if waitOther != nil {
		fallbackFrom = FromLimit
	}
	if first.family() == ask.Avoid && first.hasRoom() {
		return first, fallbackFrom, true, nil
	}
	samePool := st.sameFamilyPool(ask, opList, hasAllowed, inBracket, list)
	for _, alt := range samePool {
		if alt.family() == ask.Avoid && alt.hasRoom() {
			return alt, fallbackOrigin(alt, first, fallbackFrom), true, nil
		}
	}
	if first.family() == ask.Avoid && first.runnable && !first.close {
		return first, fallbackFrom, true, nil
	}
	for _, alt := range samePool {
		if alt.family() == ask.Avoid && alt.runnable && !alt.close {
			return alt, fallbackOrigin(alt, first, fallbackFrom), true, nil
		}
	}
	if waitOther != nil && !first.otherFamily(ask.Avoid) {
		return candidate{}, "", false, &WaitError{
			Reason: fmt.Sprintf("the %s waits for %s/%s, of another family than the builder's: %s", ask.Role, waitOther.cli, waitOther.model, waitOther.why),
			Until:  waitOther.until,
		}
	}
	if !first.runnable {
		return candidate{}, "", false, &WaitError{Reason: fmt.Sprintf("the %s waits: %s, and no next choice can run", ask.Role, first.why), Until: first.until}
	}
	return first, from, false, nil
}

func (st *state) sameFamilyHasRoom(ask Ask, first candidate, opList, hasAllowed bool, inBracket []candidate, list []string) bool {
	if !cli.Known(ask.Avoid) {
		return false
	}
	if first.family() == ask.Avoid && first.hasRoom() {
		return true
	}
	for _, alt := range st.sameFamilyPool(ask, opList, hasAllowed, inBracket, list) {
		if alt.family() == ask.Avoid && alt.hasRoom() {
			return true
		}
	}
	return false
}

func (st *state) sameFamilyPool(ask Ask, opList, hasAllowed bool, inBracket []candidate, list []string) []candidate {
	var samePool []candidate
	seenSame := map[string]bool{}
	for _, c := range inBracket {
		if slices.Contains(list, c.entry()) && st.allowedInCheckList(ask, opList, hasAllowed, c, list) {
			seenSame[c.entry()] = true
			samePool = append(samePool, c)
		}
	}
	for _, e := range list {
		if c := st.candidate(e); !seenSame[c.entry()] && st.allowedInCheckList(ask, opList, hasAllowed, c, list) {
			seenSame[c.entry()] = true
			samePool = append(samePool, c)
		}
	}
	return samePool
}

func fallbackOrigin(alt, first candidate, def string) string {
	if alt.entry() != first.entry() {
		return FromLimit
	}
	return def
}

// GroupSlowOrLimited says the limit group of cliName/model is at its limit, signed out, or at or above
// quota.slow_down_at: the condition under which a subtask between turns moves within its bracket (12.7).
func GroupSlowOrLimited(ctx context.Context, pool db.Querier, machine settings.Doc, cliName, model string) (bool, error) {
	st, err := load(ctx, pool, machine)
	if err != nil {
		return false, err
	}
	c := st.candidate(cliName + "/" + model)
	if !c.listed {
		return false, nil
	}
	return !c.hasRoom(), nil
}

// Unbracketed lists the offered CLI/model entries of the CLIs in use that appear in no bracket of
// agents.brackets, sorted (12.3, 7.8).
func Unbracketed(ctx context.Context, pool db.Querier, machine settings.Doc) ([]string, error) {
	st, err := load(ctx, pool, machine)
	if err != nil {
		return nil, err
	}
	seen := map[string]bool{}
	var out []string
	for _, cliName := range st.inUse {
		for _, r := range st.offered[cliName] {
			entry := r.CLI + "/" + r.Model
			if seen[entry] {
				continue
			}
			seen[entry] = true
			if st.bracketOf[entry] == "" {
				out = append(out, entry)
			}
		}
	}
	slices.Sort(out)
	return out, nil
}

// Unoffered is an agent none of whose entries is offered now by a CLI in use: its runs wait, and the
// inbox says so (12.3, 7.8).
type Unoffered struct {
	Role    string
	Entries []string
	// Since is when the last of its entries went, or zero when none was ever offered.
	Since time.Time
}

// NoModel lists the agents of the machine's agents.roles that have no entry a CLI in use offers.
func NoModel(ctx context.Context, q db.Querier, machine settings.Doc) ([]Unoffered, error) {
	st, err := load(ctx, q, machine)
	if err != nil {
		return nil, err
	}
	roles, _ := dig(machine, "agents", "roles").(settings.Doc)
	names := make([]string, 0, len(roles))
	for n := range roles {
		names = append(names, n)
	}
	slices.Sort(names)
	var out []Unoffered
	for _, name := range names {
		entry, _ := roles[name].(settings.Doc)
		list := strs(entry["model"])
		listed := false
		for _, e := range list {
			if st.candidate(e).listed {
				listed = true
				break
			}
		}
		if listed {
			continue
		}
		u := Unoffered{Role: name, Entries: list}
		if err := q.QueryRow(ctx, `SELECT coalesce(max(gone_at), 'epoch') FROM models WHERE cli || '/' || model = ANY($1)`, list).Scan(&u.Since); err != nil {
			return nil, err
		}
		out = append(out, u)
	}
	return out, nil
}
