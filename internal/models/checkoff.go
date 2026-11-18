package models

import (
	"context"

	"github.com/kesokaj/crew/internal/cli"
	"github.com/kesokaj/crew/internal/db"
)

// RuleCannotBeMet says the rule for checks cannot be met for ask: no entry of the role's list of a
// family Crew knows other than ask.Avoid is offered and runnable now, so Choose runs the check on a
// model of the builder's family and marks the choice Off (12.3, 12.7). The guard every run passes reads
// it (internal/runs/checkguard.go), so a check on the builder's family starts only when Choose itself
// would have let it (since 1.15).
func RuleCannotBeMet(ctx context.Context, pool db.Querier, ask Ask) (bool, error) {
	st, err := load(ctx, pool, ask.Machine)
	if err != nil {
		return false, err
	}
	return st.ruleCannotBeMet(ask), nil
}

// otherFamilyListed says the role's list has a listed entry allowed for ask of a family Crew knows
// other than ask.Avoid: the entries Choose's rule for checks moves to.
func (st *state) otherFamilyListed(ask Ask) bool {
	_, list, machineFrom, _ := st.roleInputs(ask)
	opList := machineFrom == FromOperator
	hasAllowed := st.hasAllowedInList(ask, list, true, opList)
	for _, e := range list {
		if c := st.candidate(e); st.allowedInCheckList(ask, opList, hasAllowed, c, list) && c.otherFamily(ask.Avoid) {
			return true
		}
	}
	return false
}

// otherFamilyRunnable says an allowed listed entry of a known family other than ask.Avoid can run now
// (even if slow above quota.slow_down_at).
func (st *state) otherFamilyRunnable(ask Ask) bool {
	_, list, machineFrom, _ := st.roleInputs(ask)
	opList := machineFrom == FromOperator
	hasAllowed := st.hasAllowedInList(ask, list, true, opList)
	for _, e := range list {
		if c := st.candidate(e); st.allowedInCheckList(ask, opList, hasAllowed, c, list) && c.otherFamily(ask.Avoid) && c.runnable {
			return true
		}
	}
	return false
}

// ruleCannotBeMet says Choose runs ask on the builder's family with Off true: no entry of a known
// family other than ask.Avoid has room now while the builder's family has room (or none is runnable at
// all), and agents.at_limit does not hold the run to wait for one (12.3, 12.7).
func (st *state) ruleCannotBeMet(ask Ask) bool {
	if !cli.Known(ask.Avoid) {
		return false
	}
	_, list, machineFrom, own := st.roleInputs(ask)
	first, from, _ := st.selectFirst(ask, own, list, true, machineFrom)
	if from == "" || (from == FromTicket && !cli.Known(first.family())) {
		return false
	}
	operatorChoice := from == FromTicket || from == FromProject || from == FromOperator
	if operatorChoice && first.otherFamily(ask.Avoid) {
		return false
	}
	if !st.otherFamilyListed(ask) {
		return true
	}
	atLimit := str(dig(ask.Machine, "agents", "at_limit"))
	if v := str(dig(ask.Project, "agents", "at_limit")); v != "" {
		atLimit = v
	}
	if atLimit == "wait" {
		return false
	}
	opList := machineFrom == FromOperator
	hasAllowed := st.hasAllowedInList(ask, list, true, opList)
	inBracket := st.bracketCandidates(ask, first, list, operatorChoice)
	if alt, _, ok := st.pickOtherFamily(ask, first, from, opList, hasAllowed, atLimit, inBracket, list); ok && alt.otherFamily(ask.Avoid) {
		return false
	}
	return true
}
