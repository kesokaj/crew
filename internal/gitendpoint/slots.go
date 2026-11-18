package gitendpoint

import (
	"context"
	"sync"
	"time"
)

// The requests the endpoint works on at once (6.1, since 1.08). A push or a fetch, and the advertisement
// of refs before it, takes a slot of its repository and one of the server before its body is read, and
// holds them until git has ended, its hook with it: what each may hold of the server's memory,
// processes and disk is bounded, and with the slots so is their sum. A repository, a project's one
// (git_repos_one_per_project, schema step 81), takes at most half the server's slots of a service, so another
// project's go on; pushes and fetches are counted apart, so fetches never hold up a push. A request
// with no free slot waits for one at most MaxSlotWait, with at most twice a repository's slots waiting
// beside it, else it is refused at once; its body arrives within MaxBodyTime of taking one, and its
// answer within MaxWriteTime, or git ends: a client that sends or reads slowly holds no slot.
const (
	MaxPushes      = 4
	MaxRepoPushes  = 2
	MaxFetches     = 8
	MaxRepoFetches = 4
	MaxSlotWait    = 5 * time.Minute
	MaxBodyTime    = 2 * time.Minute
)

// slots counts one service's requests, per repository and in all, and the ones that wait.
type slots struct {
	perRepo int
	all     chan struct{}
	mu      sync.Mutex
	repos   map[string]chan struct{}
	waiting map[string]int
}

func newSlots(all, perRepo int) *slots {
	return &slots{perRepo: perRepo, all: make(chan struct{}, all), repos: map[string]chan struct{}{}, waiting: map[string]int{}}
}

// take waits until wait has passed or ctx ends for a slot of repo, then one of the server; past twice a
// repository's slots waiting, it waits for none. release gives both back; ok is false when no slot was
// taken.
func (s *slots) take(ctx context.Context, repo string, wait time.Duration) (release func(), ok bool) {
	s.mu.Lock()
	own, found := s.repos[repo]
	if !found {
		own = make(chan struct{}, s.perRepo)
		s.repos[repo] = own
	}
	if s.waiting[repo] >= 2*s.perRepo {
		s.mu.Unlock()
		return nil, false
	}
	s.waiting[repo]++
	s.mu.Unlock()
	defer func() {
		s.mu.Lock()
		s.waiting[repo]--
		s.mu.Unlock()
	}()
	timer := time.NewTimer(wait)
	defer timer.Stop()
	select {
	case own <- struct{}{}:
	case <-timer.C:
		return nil, false
	case <-ctx.Done():
		return nil, false
	}
	select {
	case s.all <- struct{}{}:
	case <-timer.C:
		<-own
		return nil, false
	case <-ctx.Done():
		<-own
		return nil, false
	}
	return func() { <-s.all; <-own }, true
}

// slot takes a slot of service for repo, waiting at most SlotWait.
func (e *Endpoint) slot(ctx context.Context, service, repo string) (func(), bool) {
	wait := MaxSlotWait
	if e.SlotWait > 0 {
		wait = e.SlotWait
	}
	return e.slotsOf(service).take(ctx, repo, wait)
}

// slotsOf is the count of service: pushes, or fetches.
func (e *Endpoint) slotsOf(service string) *slots {
	e.slotsOnce.Do(func() {
		e.pushSlots = newSlots(MaxPushes, MaxRepoPushes)
		e.fetchSlots = newSlots(MaxFetches, MaxRepoFetches)
	})
	if service == "git-receive-pack" {
		return e.pushSlots
	}
	return e.fetchSlots
}

func (e *Endpoint) bodyTime() time.Duration {
	if e.BodyTime > 0 {
		return e.BodyTime
	}
	return MaxBodyTime
}

// busyText is what a request that found no slot reads.
func busyText(service string) string {
	if service == "git-receive-pack" {
		return "crew: the git endpoint works on as many pushes as it takes at once; push again in a few minutes"
	}
	return "crew: the git endpoint works on as many fetches as it takes at once; fetch again in a few minutes"
}
