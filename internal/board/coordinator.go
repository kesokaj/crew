package board

import (
	"context"
	"sort"

	"github.com/jackc/pgx/v5"

	"github.com/kesokaj/crew/internal/settings"
)

// CoordinatorTicket describes one Backlog ticket's deterministic sequencing score and readiness.
type CoordinatorTicket struct {
	Key              string   `json:"key"`
	Title            string   `json:"title"`
	SpecRef          string   `json:"specRef,omitempty"`
	Ready            bool     `json:"ready"`
	Hold             bool     `json:"hold"`
	OperatorPriority *int     `json:"operatorPriority,omitempty"`
	Priority         int      `json:"priority"`
	TriesUsed        int      `json:"triesUsed"`
	UnblockedCount   int      `json:"unblockedCount"`
	UnmetDeps        []string `json:"unmetDeps,omitempty"`
	Position         int64    `json:"position"`
}

// CoordinatorRankResult reports the ordered Backlog after @coordinator sequencing.
type CoordinatorRankResult struct {
	Project string              `json:"project"`
	Ordered []CoordinatorTicket `json:"ordered"`
	Ready   []string            `json:"ready"`
	Blocked []string            `json:"blocked"`
}

// RankBacklog sequences a project's Backlog tickets by dependency readiness, operator priority,
// spec order, downstream unblocks count, ticket priority, tries used, and ticket number.
// Only tickets.position is updated; ticket stages remain untouched (Rule 3).
func (b *Board) RankBacklog(ctx context.Context, projRef string) (*CoordinatorRankResult, error) {
	p, err := b.Project(ctx, projRef)
	if err != nil {
		return nil, err
	}
	all, err := tickets(ctx, b.Pool, p.ID, "")
	if err != nil {
		return nil, err
	}
	stageOf := make(map[string]string, len(all))
	var backlog []*Ticket
	backlogSet := map[string]bool{}
	for _, t := range all {
		stageOf[t.Key] = t.Stage
		if t.Stage == settings.StageBacklog {
			backlog = append(backlog, t)
			backlogSet[t.Key] = true
		}
	}
	revDeps := map[string][]string{}
	for _, t := range backlog {
		for _, l := range t.Links {
			if l.Kind == "depends_on" && backlogSet[l.To] {
				revDeps[l.To] = append(revDeps[l.To], t.Key)
			}
		}
	}
	candidates := buildCoordinatorCandidates(backlog, stageOf, revDeps)
	sortCoordinatorCandidates(candidates)

	ready, blocked := []string{}, []string{}
	if err := b.inTx(ctx, func(tx pgx.Tx) error {
		for i := range candidates {
			pos := int64((i + 1) * 1000)
			candidates[i].Position = pos
			if _, err := tx.Exec(ctx, `UPDATE tickets SET position = $2, updated_at = now()
				WHERE project = $1::uuid AND number = $3`, p.ID, pos, mustParseNum(candidates[i].Key)); err != nil {
				return err
			}
			if candidates[i].Ready {
				ready = append(ready, candidates[i].Key)
			} else {
				blocked = append(blocked, candidates[i].Key)
			}
		}
		return nil
	}); err != nil {
		return nil, err
	}
	b.Kick()
	return &CoordinatorRankResult{Project: p.Name, Ordered: candidates, Ready: ready, Blocked: blocked}, nil
}

func buildCoordinatorCandidates(backlog []*Ticket, stageOf map[string]string, revDeps map[string][]string) []CoordinatorTicket {
	out := make([]CoordinatorTicket, 0, len(backlog))
	for _, t := range backlog {
		var unmet []string
		for _, l := range t.Links {
			if l.Kind == "depends_on" && stageOf[l.To] != settings.StageDone {
				unmet = append(unmet, l.To)
			}
		}
		unblocks := countTransitiveDependents(t.Key, revDeps)
		out = append(out, CoordinatorTicket{
			Key: t.Key, Title: t.Title, SpecRef: t.SpecRef,
			Ready: !t.Hold && len(unmet) == 0, Hold: t.Hold,
			OperatorPriority: t.OperatorPriority, Priority: t.Priority,
			TriesUsed: t.TriesUsed, UnblockedCount: unblocks, UnmetDeps: unmet,
			Position: t.Position,
		})
	}
	return out
}

func countTransitiveDependents(root string, revDeps map[string][]string) int {
	seen := map[string]bool{root: true}
	queue := append([]string(nil), revDeps[root]...)
	count := 0
	for len(queue) > 0 {
		cur := queue[0]
		queue = queue[1:]
		if seen[cur] {
			continue
		}
		seen[cur] = true
		count++
		queue = append(queue, revDeps[cur]...)
	}
	return count
}

func sortCoordinatorCandidates(items []CoordinatorTicket) {
	sort.SliceStable(items, func(i, j int) bool {
		a, b := items[i], items[j]
		if a.Hold != b.Hold {
			return !a.Hold
		}
		if (a.OperatorPriority != nil) != (b.OperatorPriority != nil) {
			return a.OperatorPriority != nil
		}
		if a.OperatorPriority != nil && b.OperatorPriority != nil && *a.OperatorPriority != *b.OperatorPriority {
			return *a.OperatorPriority < *b.OperatorPriority
		}
		if a.Ready != b.Ready {
			return a.Ready
		}
		if (a.SpecRef != "") != (b.SpecRef != "") {
			return a.SpecRef != ""
		}
		if a.SpecRef != b.SpecRef {
			return a.SpecRef < b.SpecRef
		}
		if a.UnblockedCount != b.UnblockedCount {
			return a.UnblockedCount > b.UnblockedCount
		}
		if a.Priority != b.Priority {
			return a.Priority < b.Priority
		}
		if a.TriesUsed != b.TriesUsed {
			return a.TriesUsed < b.TriesUsed
		}
		return mustParseNum(a.Key) < mustParseNum(b.Key)
	})
}

func mustParseNum(key string) int {
	if _, numStr, ok := splitKey(key); ok {
		n := 0
		for _, ch := range numStr {
			if ch >= '0' && ch <= '9' {
				n = n*10 + int(ch-'0')
			}
		}
		return n
	}
	return 0
}

func splitKey(key string) (string, string, bool) {
	for i := len(key) - 1; i >= 0; i-- {
		if key[i] == '-' {
			return key[:i], key[i+1:], true
		}
	}
	return "", "", false
}
