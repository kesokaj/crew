package hosting

import (
	"context"
	"net"
	"slices"
	"strconv"
	"sync"
	"time"
)

// Health (19.3, plan M19 D11): every HealthEvery the server asks each serving project's health
// address; after HealthMisses misses in a row it restarts the sandbox and waits HealthGrace; after as
// many more the project is failing, which opens one episode and its news, and it is restarted again
// every FailingRestartEvery. An answer ends the episode.
var (
	HealthEvery         = 15 * time.Second
	HealthMisses        = 3
	HealthGrace         = 60 * time.Second
	FailingRestartEvery = 10 * time.Minute
)

// health is what the server remembers of a project's answers since its last one.
type health struct {
	mu sync.Mutex
	by map[string]*projectHealth
}

type projectHealth struct {
	sandbox   string
	misses    int
	restarted bool // restarted once in this episode
	grace     time.Time
	last      time.Time // the last restart
}

func (s *Service) now() time.Time {
	if s.Now != nil {
		return s.Now()
	}
	return time.Now()
}

// RunHealth asks the health addresses every HealthEvery until ctx ends.
func (s *Service) RunHealth(ctx context.Context) {
	t := time.NewTicker(HealthEvery)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
		if err := s.HealthRound(ctx); err != nil && ctx.Err() == nil {
			s.Log.Warn("asking the hosted projects' health", "error", err)
		}
	}
}

// HealthRound asks each serving project once. A project whose deploy runs is not asked: its deploy
// pauses what serves for the data's copy, or stops it for a roll back with data, and a restart would
// start it again on the data being copied or replaced (review of M19, finding 2); its count starts
// again after.
func (s *Service) HealthRound(ctx context.Context) error {
	rows, err := s.Pool.Query(ctx, `SELECT p.id, p.name, p.key, h.sandbox, host(h.address), h.state, coalesce(h.port, 0), coalesce(h.health, '')
		FROM hosting h JOIN projects p ON p.id = h.project
		WHERE h.state IN ('live', 'failing') AND h.sandbox IS NOT NULL AND h.address IS NOT NULL
		  AND NOT EXISTS (SELECT 1 FROM deploys d WHERE d.project = h.project AND d.state IN ('building', 'starting', 'checking'))`)
	if err != nil {
		return err
	}
	type serving struct {
		id, name, key, sandbox, addr, state, health string
		port                                        int
	}
	var all []serving
	for rows.Next() {
		var v serving
		if err := rows.Scan(&v.id, &v.name, &v.key, &v.sandbox, &v.addr, &v.state, &v.port, &v.health); err != nil {
			rows.Close()
			return err
		}
		all = append(all, v)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}
	s.hl.mu.Lock()
	for id := range s.hl.by {
		if !slices.ContainsFunc(all, func(v serving) bool { return v.id == id }) {
			delete(s.hl.by, id)
		}
	}
	s.hl.mu.Unlock()
	for _, v := range all {
		st, err := settingsOf(ctx, s.Pool, v.id, v.name, v.key)
		if err != nil {
			return err
		}
		// What serves is asked where it was checked; a change of settings holds with its deploy.
		port, path := v.port, v.health
		if port == 0 || path == "" {
			port, path = st.Port, st.Health
		}
		url := "http://" + net.JoinHostPort(v.addr, strconv.Itoa(port)) + path
		s.judge(ctx, v.id, v.name, v.sandbox, v.state, s.answers(ctx, url))
	}
	return nil
}

// judge takes one answer, or its absence, of a project's health address.
func (s *Service) judge(ctx context.Context, projectID, name, sandbox, state string, ok bool) {
	s.hl.mu.Lock()
	if s.hl.by == nil {
		s.hl.by = map[string]*projectHealth{}
	}
	h := s.hl.by[projectID]
	if h == nil || h.sandbox != sandbox {
		h = &projectHealth{sandbox: sandbox}
		s.hl.by[projectID] = h
	}
	now := s.now()
	var restart, failing, back bool
	switch {
	case ok:
		back = state == "failing" || h.misses > 0 || h.restarted
		*h = projectHealth{sandbox: sandbox}
	case now.Before(h.grace):
	case state == "failing":
		if now.Sub(h.last) >= FailingRestartEvery {
			restart, h.last, h.grace = true, now, now.Add(HealthGrace)
		}
	default:
		h.misses++
		if h.misses >= HealthMisses {
			if !h.restarted {
				restart, h.restarted, h.misses, h.last, h.grace = true, true, 0, now, now.Add(HealthGrace)
			} else {
				failing, h.last = true, now
			}
		}
	}
	s.hl.mu.Unlock()
	switch {
	case back:
		if _, err := s.Pool.Exec(ctx, `UPDATE hosting SET state = 'live', down_since = NULL, updated_at = now() WHERE project = $1 AND sandbox = $2`, projectID, sandbox); err != nil {
			s.Log.Warn("recording a hosted project that answers again", "project", name, "error", err)
		}
		if state == "failing" {
			s.logLine(projectID, "%s answers again", sandbox)
		}
	case failing:
		// One episode, one time stamp: the news of it is sent once (18.1).
		if _, err := s.Pool.Exec(ctx, `UPDATE hosting SET state = 'failing', down_since = coalesce(down_since, $3), updated_at = now()
			WHERE project = $1 AND sandbox = $2`, projectID, sandbox, now); err != nil {
			s.Log.Warn("recording a hosted project that stopped answering", "project", name, "error", err)
		}
		s.logLine(projectID, "%s stopped answering", sandbox)
		s.Log.Warn("a hosted project stopped answering", "project", name, "sandbox", sandbox)
	}
	if restart {
		s.logLine(projectID, "%s did not answer: restarted", sandbox)
		if err := s.Runtime.Restart(ctx, sandbox); err != nil {
			s.Log.Warn("restarting a hosted sandbox", "sandbox", sandbox, "error", err)
		}
		if _, err := s.Pool.Exec(ctx, `UPDATE hosting SET restarts = restarts + 1, updated_at = now() WHERE project = $1 AND sandbox = $2`, projectID, sandbox); err != nil {
			s.Log.Warn("counting a restart", "project", name, "error", err)
		}
	}
}
