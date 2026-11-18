package board

import (
	"fmt"

	"github.com/kesokaj/crew/internal/settings"
)

// Limits are the limits of a project (7.7), as its settings give them. None is in dollars (since 0.53).
type Limits struct {
	MaxFixRounds       int
	MaxTicketFailures  int
	MaxAddedShare      float64
	MaxAddedTickets    int
	MaxAddedSubtasks   int
	MaxParallelSubtask int
}

// LimitsOf reads the limits of a project's settings.
func LimitsOf(d settings.Doc) Limits {
	n := func(path string) float64 { v, _ := settingNumber(d, path); return v }
	return Limits{
		MaxFixRounds:       int(n("limits.max_fix_rounds")),
		MaxTicketFailures:  int(n("limits.max_ticket_failures")),
		MaxAddedShare:      n("limits.max_added_share"),
		MaxAddedTickets:    int(n("limits.max_added_tickets")),
		MaxAddedSubtasks:   int(n("limits.max_added_subtasks")),
		MaxParallelSubtask: int(n("limits.max_parallel_subtasks")),
	}
}

// The answers of the limits (7.7). Each is a plain function of numbers, so a test can give it any; the
// milestone that builds what reaches a limit calls it (M6: fix rounds; M9: added subtasks; M11: the
// project's pause).

// FixRoundsReached says a ticket needs the lead to look at it again (R162).
func (l Limits) FixRoundsReached(rounds int) bool { return rounds >= l.MaxFixRounds }

// TicketFailuresReached says a ticket has failed too many times since the operator last lifted its
// hold, so Crew puts it on hold instead of spending more runs or replacing it (7.7).
func (l Limits) TicketFailuresReached(failures int) bool {
	return l.MaxTicketFailures > 0 && failures >= l.MaxTicketFailures
}

// AddedShareExceeded says the tickets agents added passed their share of the tickets of the approved
// plan, and the project pauses (R32, since 0.53 a count). A project with no tickets of its plan has no
// share to pass. Replacements of a cancelled ticket are the same work cut differently, and the caller
// does not count them.
func (l Limits) AddedShareExceeded(added, plan int) bool {
	return plan > 0 && float64(added) > l.MaxAddedShare*float64(plan)+1e-9
}

// AddedTicketsExceeded says a project with no plan tickets has more open agent-added tickets than
// limits.max_added_tickets allows (0 disables; 7.7).
func (l Limits) AddedTicketsExceeded(openAdded, plan int) bool {
	return plan == 0 && l.MaxAddedTickets > 0 && openAdded > l.MaxAddedTickets
}

// MayAddSubtask refuses create_subtask past max_added_subtasks, with the reason (7.5).
func (l Limits) MayAddSubtask(added int) error {
	if added >= l.MaxAddedSubtasks {
		return fmt.Errorf("this ticket has %d subtasks that agents added, the most it may have: finish with what you have, or create a ticket", added)
	}
	return nil
}

// ParallelRoom says how many more subtasks of one ticket may start now.
func (l Limits) ParallelRoom(running int) int {
	if r := l.MaxParallelSubtask - running; r > 0 {
		return r
	}
	return 0
}
