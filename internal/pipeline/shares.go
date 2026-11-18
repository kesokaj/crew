package pipeline

import (
	"context"
	"slices"
	"sync"
	"time"

	"github.com/kesokaj/crew/internal/cli"
	"github.com/kesokaj/crew/internal/models"
)

// wantStale is how long a project's want of a turn on a group counts when it is not asked for again.
const wantStale = time.Minute

// shares is how projects share a subscription (7.9, R164; 12.7, since 0.47): while a group of models
// is close to its limit, one new run on it starts in each pass of the pipeline, and it goes to the
// project that was served least recently on that group. While the group is free, room and the stage
// limits bound the runs; at its limit, runs move or wait (atlimit.go).
type shares struct {
	mu sync.Mutex
	// guard is the quota guard as this pass read it; nil starts no turn-taking.
	guard *models.Guard
	// started are the close groups a run started on in this pass.
	started map[string]bool
	// wants are the projects that asked for a turn on a close group, and when they first and last did.
	wants map[string]map[string]*want
}

type want struct{ since, seen time.Time }

// beginPass reads the guard for a pass, and forgets which groups a run started on.
func (sh *shares) beginPass(g *models.Guard) {
	sh.mu.Lock()
	defer sh.mu.Unlock()
	sh.guard, sh.started = g, map[string]bool{}
	if sh.wants == nil {
		sh.wants = map[string]map[string]*want{}
	}
}

// takeTurn says whether a run of project on the chosen model may start now, as far as the group's
// share goes. When it may, done is called with whether the run started; when it may not, first is the
// project whose turn it is, and group the group that is close to its limit.
func (p *Pipeline) takeTurn(ctx context.Context, project string, c models.Choice) (ok bool, group, first string, done func(started bool), err error) {
	sh := &p.shares
	none := func(bool) {}
	a, known := cli.Get(c.CLI)
	sh.mu.Lock()
	defer sh.mu.Unlock()
	if !known || sh.guard == nil || !sh.guard.Close(a, c.Model) {
		return true, "", "", none, nil
	}
	group = a.Name() + "/" + a.LimitGroup(c.Model)
	now := time.Now()
	ws := sh.wants[group]
	if ws == nil {
		ws = map[string]*want{}
		sh.wants[group] = ws
	}
	if w := ws[project]; w != nil {
		w.seen = now
	} else {
		ws[project] = &want{since: now, seen: now}
	}
	if sh.started[group] {
		return false, group, "", none, nil
	}
	// The projects that want the group, the one served least recently on it first, then the one that
	// asked first.
	type cand struct {
		project string
		served  time.Time
		since   time.Time
	}
	var cands []cand
	for pr, w := range ws {
		if now.Sub(w.seen) > wantStale {
			delete(ws, pr)
			continue
		}
		served, err := p.servedOn(ctx, pr, a, group)
		if err != nil {
			return false, group, "", none, err
		}
		cands = append(cands, cand{pr, served, w.since})
	}
	slices.SortFunc(cands, func(x, y cand) int {
		if c := x.served.Compare(y.served); c != 0 {
			return c
		}
		return x.since.Compare(y.since)
	})
	if cands[0].project != project {
		return false, group, cands[0].project, none, nil
	}
	return true, group, "", func(started bool) {
		if !started {
			return
		}
		sh.mu.Lock()
		defer sh.mu.Unlock()
		sh.started[group] = true
		delete(sh.wants[group], project)
	}, nil
}

// servedOn is when a run of project last started on a model of group; zero for never.
func (p *Pipeline) servedOn(ctx context.Context, project string, a cli.Adapter, group string) (time.Time, error) {
	rows, err := p.Pool.Query(ctx, `SELECT coalesce(r.model, ''), max(r.started_at) FROM runs r JOIN subtasks s ON s.id = r.subtask
		WHERE s.project = $1 AND r.cli = $2 AND r.started_at IS NOT NULL GROUP BY 1`, project, a.Name())
	if err != nil {
		return time.Time{}, err
	}
	defer rows.Close()
	var last time.Time
	for rows.Next() {
		var model string
		var at time.Time
		if err := rows.Scan(&model, &at); err != nil {
			return last, err
		}
		if a.Name()+"/"+a.LimitGroup(model) == group && at.After(last) {
			last = at
		}
	}
	return last, rows.Err()
}
