package setup

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/kesokaj/crew/internal/orchestrator"
	"github.com/kesokaj/crew/internal/sandboxes"
)

// fakeBoxes starts and removes setup microVMs in memory. failRemove makes the next removals fail.
type fakeBoxes struct {
	mu         sync.Mutex
	n          int
	live       []string
	removed    []string
	failRemove int
	starting   chan struct{} // when set, Start waits for it
}

func (f *fakeBoxes) Start(ctx context.Context, spec sandboxes.Spec) (sandboxes.Row, orchestrator.Sandbox, error) {
	if f.starting != nil {
		<-f.starting
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.n++
	id := fmt.Sprintf("setup-%d", f.n)
	f.live = append(f.live, id)
	return sandboxes.Row{ID: id, Kind: spec.Kind}, orchestrator.Sandbox{ID: id}, nil
}

func (f *fakeBoxes) Remove(_ context.Context, id string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.failRemove > 0 {
		f.failRemove--
		return errors.New("docker said no")
	}
	f.live = slices.DeleteFunc(f.live, func(v string) bool { return v == id })
	f.removed = append(f.removed, id)
	return nil
}

func (f *fakeBoxes) Home(id string) string { return "/homes/" + id }

func (f *fakeBoxes) isLive(id string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Contains(f.live, id)
}

func lifeService() (*Service, *fakeBoxes) {
	f := &fakeBoxes{}
	return &Service{Sandboxes: f, Log: slog.New(slog.DiscardHandler)}, f
}

func nop() {}

// Found on dave: after Stop, the terminal of the old step ended late and removed the microVM of the
// step the operator had started next, which then showed nothing.
func TestALateTerminalDoesNotRemoveTheNextStepsMicroVM(t *testing.T) {
	s, f := lifeService()
	ctx := context.Background()
	git, gen, err := s.open(ctx, "git", nop)
	if err != nil {
		t.Fatal(err)
	}
	_ = s.Close(ctx) // Stop
	gcloud, _, err := s.open(ctx, "gcloud", nop)
	if err != nil {
		t.Fatal(err)
	}
	s.end(git.id, gen) // the git terminal's late last word
	if !f.isLive(gcloud.id) || s.Running() != "gcloud" {
		t.Fatalf("the gcloud microVM was removed by the git terminal: live %v, running %q", f.live, s.Running())
	}
}

// A reloaded page takes the step over: the first terminal is ended, and its end leaves the
// microVM to the second.
func TestAReloadedPageTakesTheStepOver(t *testing.T) {
	s, f := lifeService()
	ctx := context.Background()
	first, cancelFirst := context.WithCancel(ctx)
	a, genA, _ := s.open(ctx, "agy", cancelFirst)
	b, genB, _ := s.open(ctx, "agy", nop)
	if a.id != b.id || genA == genB {
		t.Fatalf("a reload started another microVM or kept the attachment: %s/%d %s/%d", a.id, genA, b.id, genB)
	}
	if first.Err() == nil {
		t.Fatal("the first terminal was not ended")
	}
	s.end(a.id, genA)
	if !f.isLive(a.id) {
		t.Fatal("the first terminal removed the microVM the second uses")
	}
	s.end(b.id, genB)
	if f.isLive(b.id) || s.Running() != "" {
		t.Fatalf("the step ended and its microVM stayed: %v", f.live)
	}
}

func TestAFailedRemovalIsTriedAgain(t *testing.T) {
	s, f := lifeService()
	ctx := context.Background()
	sess, gen, _ := s.open(ctx, "gh", nop)
	f.failRemove = 2
	s.end(sess.id, gen)
	if !f.isLive(sess.id) || s.Running() != "gh" {
		t.Fatal("a microVM whose removal failed was forgotten")
	}
	if _, _, err := s.open(ctx, "gcloud", nop); err == nil {
		t.Fatal("a second microVM started while the first could not be removed")
	}
	f.failRemove = 1
	if err := s.Close(ctx); err == nil {
		t.Fatal("Stop said the microVM was removed when it was not")
	}
	s.Reap(ctx)
	if f.isLive(sess.id) || s.Running() != "" {
		t.Fatalf("Reap did not remove it: live %v", f.live)
	}
}

func TestStopEndsTheTerminalAndRunningDoesNotWaitForAStart(t *testing.T) {
	s, f := lifeService()
	ctx := context.Background()
	term, cancel := context.WithCancel(ctx)
	s.open(ctx, "git", cancel)
	_ = s.Close(ctx)
	if term.Err() == nil {
		t.Fatal("Stop did not end the terminal")
	}

	f.starting = make(chan struct{})
	go s.open(ctx, "agy", nop)
	deadline := time.Now().Add(2 * time.Second)
	for s.Running() != "agy" {
		if time.Now().After(deadline) {
			t.Fatal("Running waited for the start, or did not say agy")
		}
		time.Sleep(10 * time.Millisecond)
	}
	close(f.starting)
}
