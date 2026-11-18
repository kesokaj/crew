package planning

import (
	"context"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/kesokaj/crew/internal/board"
	"github.com/kesokaj/crew/internal/settings"
)

// ProjectQueueHead describes one active project's top ready Backlog work identified by @coordinator.
type ProjectQueueHead struct {
	Project     string `json:"project"`
	Key         string `json:"key"`
	ReadyCount  int    `json:"readyCount"`
	NextTicket  string `json:"nextTicket,omitempty"`
	NextTitle   string `json:"nextTitle,omitempty"`
	NextSpecRef string `json:"nextSpecRef,omitempty"`
}

// QuotaWindow captures a model group's current capacity state for the Global Queue view.
type QuotaWindow struct {
	Group   string  `json:"group"`
	CLI     string  `json:"cli"`
	Window  string  `json:"window"`
	Used    float64 `json:"used"`
	AtLimit bool    `json:"atLimit"`
	Source  string  `json:"source,omitempty"`
}

// GlobalQueueSnapshot summarizes cross-project capacity, active runs, waiting scheduler asks,
// quota headroom, and per-project @coordinator Backlog heads (design 7.9, 7.14, R211).
type GlobalQueueSnapshot struct {
	MaxConcurrentRuns int                `json:"maxConcurrentRuns"`
	ActiveRuns        int                `json:"activeRuns"`
	Waiting           []LeadWait         `json:"waiting"`
	ProjectHeads      []ProjectQueueHead `json:"projectHeads"`
	Quota             []QuotaWindow      `json:"quota"`
}

// GlobalQueueView inspects machine concurrency, lead_waits, quota, and active projects' ready heads.
func GlobalQueueView(ctx context.Context, pool *pgxpool.Pool, b *board.Board) (*GlobalQueueSnapshot, error) {
	doc, err := settings.Effective(ctx, pool, settings.Machine{})
	if err != nil {
		return nil, err
	}
	slots, err := Slots(ctx, pool, doc)
	if err != nil {
		return nil, err
	}
	maxRuns := slots["agy"]
	var activeRuns int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM runs WHERE state IN ('starting', 'working')`).Scan(&activeRuns); err != nil {
		return nil, err
	}
	waiting, err := Queue(ctx, pool, doc)
	if err != nil {
		return nil, err
	}
	if waiting == nil {
		waiting = []LeadWait{}
	}
	qRows, err := pool.Query(ctx, `SELECT cli, grp, win, used, (reached_until IS NOT NULL AND reached_until > now()), coalesce(source, '')
		FROM quota ORDER BY cli, grp, win`)
	if err != nil {
		return nil, err
	}
	defer qRows.Close()
	quota := []QuotaWindow{}
	for qRows.Next() {
		var q QuotaWindow
		if err := qRows.Scan(&q.CLI, &q.Group, &q.Window, &q.Used, &q.AtLimit, &q.Source); err != nil {
			return nil, err
		}
		quota = append(quota, q)
	}
	if err := qRows.Err(); err != nil {
		return nil, err
	}
	var heads []ProjectQueueHead
	if b != nil {
		projects, perr := b.Projects(ctx)
		if perr == nil {
			for _, p := range projects {
				if p.State == "archived" {
					continue
				}
				rank, rerr := b.RankBacklog(ctx, p.ID)
				if rerr != nil {
					continue
				}
				h := ProjectQueueHead{Project: p.Name, Key: p.Key, ReadyCount: len(rank.Ready)}
				for _, item := range rank.Ordered {
					if item.Ready {
						h.NextTicket = item.Key
						h.NextTitle = item.Title
						h.NextSpecRef = item.SpecRef
						break
					}
				}
				heads = append(heads, h)
			}
		}
	}
	if heads == nil {
		heads = []ProjectQueueHead{}
	}
	return &GlobalQueueSnapshot{
		MaxConcurrentRuns: maxRuns,
		ActiveRuns:        activeRuns,
		Waiting:           waiting,
		ProjectHeads:      heads,
		Quota:             quota,
	}, nil
}

// DispatchProjectToGlobalQueue sequences a project's Backlog via @coordinator and kicks the board
// and orchestrator so newly ready root work enters Crew's Global Queue immediately.
func DispatchProjectToGlobalQueue(ctx context.Context, b *board.Board, projRef string, kickFn func()) (*board.CoordinatorRankResult, error) {
	res, err := b.RankBacklog(ctx, projRef)
	if err != nil {
		return nil, err
	}
	b.Kick()
	if kickFn != nil {
		kickFn()
	}
	return res, nil
}
