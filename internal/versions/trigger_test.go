package versions_test

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/kesokaj/crew/internal/gitstore"
	"github.com/kesokaj/crew/internal/release"
	"github.com/kesokaj/crew/internal/settings"
	"github.com/kesokaj/crew/internal/updater"
	"github.com/kesokaj/crew/internal/versions"
)

const fixture = "f1c50000-0000-4000-8000-000000000001"

// history is the fixture project's main as Crew's project's: the running release's commit c0, then
// the operator's push o1, the merges of tickets 1 (m1), 2, an improvement's (m2), and 3 (m3), the
// merge of ticket 4, a second improvement's (m4), and the operator's push o2.
type history struct {
	c0, o1, m1, m2, m3, m4, o2 string
}

func (k *buildKit) history() history {
	k.t.Helper()
	ctx := context.Background()
	if err := k.git.CreateLocal(ctx, "fixture", crewTree(), "Crew", gitstore.Identity{Name: "a", Email: "a@x"}); err != nil {
		k.t.Fatal(err)
	}
	work := k.t.TempDir()
	git := func(args ...string) string {
		k.t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = work
		cmd.Env = gitstore.CleanEnv()
		out, err := cmd.CombinedOutput()
		if err != nil {
			k.t.Fatalf("git %v: %v %s", args, err, out)
		}
		return strings.TrimSpace(string(out))
	}
	git("clone", "-q", k.git.Canonical("fixture"), ".")
	var h history
	h.c0 = git("rev-parse", "HEAD")
	push := func(msg string) string {
		os.WriteFile(filepath.Join(work, "notes.txt"), []byte(msg), 0o644)
		git("add", "-A")
		git("commit", "-q", "-m", msg)
		return git("rev-parse", "HEAD")
	}
	merge := func(n int, improvement bool) string {
		branch := "t" + string(rune('0'+n))
		git("checkout", "-q", "-b", branch)
		os.WriteFile(filepath.Join(work, branch+".txt"), []byte(branch), 0o644)
		git("add", "-A")
		git("commit", "-q", "-m", "work of "+branch)
		git("checkout", "-q", "main")
		git("merge", "-q", "--no-ff", "-m", "FIX-"+branch+": merge", branch)
		c := git("rev-parse", "HEAD")
		var ticket int64
		if err := k.d.Server.QueryRow(ctx, `INSERT INTO tickets (project, number, title, kind, stage, origin, created_by, position)
			VALUES ($1, $2, 't', 'feature', 'done', 'operator', 'operator', $3) RETURNING id`, fixture, 900+n, 900+n).Scan(&ticket); err != nil {
			k.t.Fatal(err)
		}
		if _, err := k.d.Server.Exec(ctx, `INSERT INTO check_runs (project, ticket, purpose, commit, state) VALUES ($1, $2, 'merge', $3, 'passed')`,
			fixture, ticket, c); err != nil {
			k.t.Fatal(err)
		}
		if improvement {
			if _, err := k.d.Server.Exec(ctx, `INSERT INTO improvements (number, title, target, patch, measure, ticket)
				VALUES ($1, 'x', '{baseline/stages/build.md}', '{}', 'fix_rounds_per_ticket', $2)`, n, ticket); err != nil {
				k.t.Fatal(err)
			}
		}
		return c
	}
	h.o1 = push("the operator's own push")
	h.m1 = merge(1, false)
	h.m2 = merge(2, true)
	h.m3 = merge(3, false)
	merge(4, false)
	h.m4 = merge(5, true)
	h.o2 = push("another push")
	git("push", "-q", "origin", "main")
	k.runningAt(h.c0)
	return h
}

// runningAt makes the running release's commit c.
func (k *buildKit) runningAt(c string) {
	k.t.Helper()
	run := filepath.Join(k.rel, "0.1.0")
	f, _ := release.Read(run)
	if _, err := release.Write(run, release.Facts{Version: "0.1.0", Commit: c, BuiltAt: time.Now(), Images: f.Manifest.Images,
		Deploy: release.DeployFile, Schema: 51}, f.Manifest.Files, nil); err != nil {
		k.t.Fatal(err)
	}
	k.gate("0.1.0")
}

// A build takes main's newest commit, stopped before the merge of a second improvement, and counts
// the tickets merged into it, not the operator's pushes; it gets the next patch number, or the minor
// or major asked for (16.2 step 3).
func TestABuildStopsBeforeASecondImprovement(t *testing.T) {
	k := newBuildKit(t)
	h := k.history()
	ctx := context.Background()
	next, err := k.b.Plan(ctx, "fixture", "")
	if err != nil {
		t.Fatal(err)
	}
	// m3, then the merge of ticket 4, then m4: the build stops at ticket 4's merge.
	if next.StopsBefore != h.m4 || next.Since != h.c0 || next.Tickets != 4 || next.Version != "0.1.1" {
		t.Fatalf("%+v", next)
	}
	if next.Commit == h.m4 || next.Commit == h.o2 {
		t.Fatalf("the build holds the second improvement: %+v", next)
	}
	for bump, want := range map[string]string{"patch": "0.1.1", "minor": "0.2.0", "major": "1.0.0"} {
		if n, err := k.b.Plan(ctx, "fixture", bump); err != nil || n.Version != want {
			t.Fatalf("%s: %+v %v", bump, n, err)
		}
	}
	if _, err := k.b.Plan(ctx, "fixture", "huge"); err == nil {
		t.Fatal("a bump that is none")
	}
	// The release after it starts there, and holds the second improvement.
	k.runningAt(next.Commit)
	if n, err := k.b.Plan(ctx, "fixture", ""); err != nil || n.Commit != h.o2 || n.StopsBefore != "" || n.Tickets != 1 {
		t.Fatalf("the next: %+v %v", n, err)
	}
	// A build, and a release folder of a higher number, raise the next number.
	if _, err := k.d.Server.Exec(ctx, `INSERT INTO builds (version, commit, asked_by, state) VALUES ('0.1.4', $1, 'operator', 'done'),
		('0.1.9', $1, 'operator', 'failed')`, h.o2); err != nil {
		t.Fatal(err)
	}
	if n, _ := k.b.Plan(ctx, "fixture", ""); n.Version != "0.1.5" {
		t.Fatalf("after the build of 0.1.4 and a failed 0.1.9: %s", n.Version)
	}
	if n, _ := k.b.Plan(ctx, "fixture", "minor"); n.Version != "0.2.0" {
		t.Fatalf("a minor after 0.1.4: %s", n.Version)
	}
	// main that has not moved builds nothing.
	k.runningAt(h.o2)
	if _, err := k.b.Plan(ctx, "fixture", ""); !errors.Is(err, versions.ErrNothingToBuild) {
		t.Fatalf("main unmoved: %v", err)
	}
}

// release_after_tickets counts merged tickets, never the operator's pushes; the button builds after
// a push alone; a commit is built by itself once.
func TestReleaseAfterTicketsCountsTickets(t *testing.T) {
	k := newBuildKit(t)
	h := k.history()
	ctx := context.Background()
	k.s.Builder = k.b
	setMachine := func(doc string) {
		t.Helper()
		k.d.Server.Exec(ctx, `DELETE FROM settings WHERE project IS NULL`)
		if _, err := k.d.Server.Exec(ctx, `INSERT INTO settings (project, doc, updated_by) VALUES (NULL, $1::jsonb, 'test')`, doc); err != nil {
			t.Fatal(err)
		}
	}
	builds := func() int {
		var n int
		k.d.Server.QueryRow(ctx, `SELECT count(*) FROM builds`).Scan(&n)
		return n
	}
	setMachine(`{"version": 1, "improvements": {"crew_project": "fixture"}, "updates": {"release_after_tickets": 5}}`)
	if err := k.s.AutoBuild(ctx); err != nil || builds() != 0 {
		t.Fatalf("four tickets at five: %v, %d builds", err, builds())
	}
	setMachine(`{"version": 1, "improvements": {"crew_project": "fixture"}, "updates": {"release_after_tickets": 4}}`)
	if err := k.s.AutoBuild(ctx); err != nil || builds() != 1 {
		t.Fatalf("four tickets at four: %v, %d builds", err, builds())
	}
	k.b.Wait()
	bd, _ := k.b.Get(ctx, "0.1.1")
	if bd == nil || bd.AskedBy != versions.ByClock || bd.State != versions.BuildDone {
		t.Fatalf("%+v", bd)
	}
	// After it, main has the second improvement and a push: one ticket, which is not enough; the
	// button builds it.
	setMachine(`{"version": 1, "improvements": {"crew_project": "fixture"}, "updates": {"release_after_tickets": 2}}`)
	if err := k.s.AutoBuild(ctx); err != nil || builds() != 1 {
		t.Fatalf("one ticket at two: %v, %d builds", err, builds())
	}
	bd, next, err := k.s.BuildNow(ctx, "minor")
	if err != nil || bd.Version != "0.2.0" || next.Commit != h.o2 || bd.AskedBy != versions.ByOperator {
		t.Fatalf("%+v %+v %v", bd, next, err)
	}
	k.b.Wait()
	// Only pushes since: the clock builds nothing, at any count.
	f, _ := release.Read(filepath.Join(k.rel, "0.2.0"))
	if f == nil || f.Manifest.Commit != h.o2 {
		t.Fatalf("0.2.0 is %+v", f)
	}
	setMachine(`{"version": 1, "improvements": {"crew_project": "fixture"}, "updates": {"release_after_tickets": 1}}`)
	if err := k.s.AutoBuild(ctx); err != nil || builds() != 2 {
		t.Fatalf("no ticket since: %v, %d builds", err, builds())
	}
	if _, _, err := k.s.BuildNow(ctx, ""); !errors.Is(err, versions.ErrNothingToBuild) {
		t.Fatalf("the button with main unmoved: %v", err)
	}
	setMachine(`{"version": 1, "updates": {"release_after_tickets": 1}}`)
	if _, _, err := k.s.BuildNow(ctx, ""); !errors.Is(err, versions.ErrNoCrewProject) {
		t.Fatalf("with no Crew's project: %v", err)
	}
	_ = settings.Doc{}
}

// A commit whose build failed is not built again by itself: it waits for the press.
func TestAFailedCommitIsNotBuiltAgainByItself(t *testing.T) {
	k := newBuildKit(t)
	k.history()
	ctx := context.Background()
	k.s.Builder = k.b
	if _, err := k.d.Server.Exec(ctx, `INSERT INTO settings (project, doc, updated_by) VALUES (NULL, $1::jsonb, 'test')`,
		`{"version": 1, "improvements": {"crew_project": "fixture"}, "updates": {"release_after_tickets": 1}}`); err != nil {
		t.Fatal(err)
	}
	k.h.BuildErr = errors.New("the build ended with 2")
	if err := k.s.AutoBuild(ctx); err != nil {
		t.Fatal(err)
	}
	k.b.Wait()
	if err := k.s.AutoBuild(ctx); err != nil {
		t.Fatal(err)
	}
	k.b.Wait()
	var n int
	k.d.Server.QueryRow(ctx, `SELECT count(*) FROM builds`).Scan(&n)
	if n != 1 {
		t.Fatalf("%d builds of one commit", n)
	}
}

// An improvement the operator took out before it was installed holds every release that has its merge
// and not its revert's (16.3, since 0.55, the operator's answer 3 of report 10: held, not refused): the
// release is made, and waits for Install with the reason, which names the improvement by its number
// alone. A release of a commit before its merge, or after its revert merged, is not held; a revert
// that is not Done holds as no revert. A removed improvement no longer stops a build as the second
// improvement would (16.2 step 3).
func TestAReleaseThatHoldsATakenOutImprovementIsHeld(t *testing.T) {
	k := newBuildKit(t)
	h := k.history()
	ctx := context.Background()
	k.s.Builder, k.s.Git = k.b, k.git
	if _, err := k.d.Server.Exec(ctx, `INSERT INTO settings (project, doc, updated_by) VALUES (NULL, '{"version": 1, "improvements": {"crew_project": "fixture"}}', 'test')`); err != nil {
		t.Fatal(err)
	}
	// Improvement 2 (merged at m2) is taken out; its revert ticket merges at r2, after o2.
	revert := k.revertOf(h, 2)
	if _, err := k.d.Server.Exec(ctx, `UPDATE improvements SET status = 'removed' WHERE number = 2`); err != nil {
		t.Fatal(err)
	}
	// The build no longer stops before m4: improvement 2 is no improvement for the stop rule.
	if next, err := k.b.Plan(ctx, "fixture", ""); err != nil || next.StopsBefore != "" || next.Commit != revert.commit {
		t.Fatalf("a removed improvement still stops the build: %+v %v", next, err)
	}
	why := func(commit string) string {
		t.Helper()
		w, err := k.s.TakenOutHold(ctx, commit)
		if err != nil {
			t.Fatal(err)
		}
		return w
	}
	// Before the revert is Done: m3 and r2 hold i_0002, m1 does not.
	revert.stage("in_progress")
	if w := why(h.m3); !strings.Contains(w, "i_0002") || strings.Contains(w, "Crew could not read") {
		t.Fatalf("m3: %q", w)
	}
	if w := why(revert.commit); !strings.Contains(w, "i_0002") {
		t.Fatalf("the revert's commit, the revert not Done: %q", w)
	}
	if w := why(h.m1); w != "" {
		t.Fatalf("m1, before the merge: %q", w)
	}
	// The revert Done: a release of its merge holds nothing; one of m3 still holds i_0002.
	revert.stage("done")
	if w := why(revert.commit); w != "" {
		t.Fatalf("after the revert: %q", w)
	}
	if w := why(h.m3); !strings.Contains(w, "i_0002") {
		t.Fatalf("m3 after the revert merged: %q", w)
	}
	// A commit the repository does not hold cannot be read: it is held.
	if w := why(strings.Repeat("e", 40)); !strings.Contains(w, "Crew could not read") || !strings.Contains(w, "i_0002") {
		t.Fatalf("a commit not held: %q", w)
	}
	// The hold does not need the builder, which a server without its sandboxes has not, nor the
	// setting that names Crew's project: the improvement's ticket names it. With no history to read
	// it holds (security review of 1863a0e..f8d57b2).
	builder := k.s.Builder
	k.s.Builder, k.s.Git = nil, k.git
	if w := why(h.m3); !strings.Contains(w, "it holds i_0002") {
		t.Fatalf("with no builder: %q", w)
	}
	if _, err := k.d.Server.Exec(ctx, `UPDATE settings SET doc = '{"version": 1}' WHERE project IS NULL`); err != nil {
		t.Fatal(err)
	}
	if w := why(h.m3); !strings.Contains(w, "it holds i_0002") {
		t.Fatalf("with no Crew's project named: %q", w)
	}
	k.s.Git = nil
	if w := why(h.m3); !strings.Contains(w, "Crew could not read") || !strings.Contains(w, "i_0002") {
		t.Fatalf("with no history to read: %q", w)
	}
	k.s.Builder, k.s.Git = builder, k.git
	// A removed improvement whose Done ticket has no merge recorded: what it changed is not known, so it
	// holds as unread (round 2 of the security review of 1863a0e..f8d57b2).
	var lone int64
	if err := k.d.Server.QueryRow(ctx, `INSERT INTO tickets (project, number, title, kind, stage, origin, created_by, position)
		VALUES ($1, 980, 't', 'feature', 'done', 'operator', 'operator', 980) RETURNING id`, fixture).Scan(&lone); err != nil {
		t.Fatal(err)
	}
	if _, err := k.d.Server.Exec(ctx, `INSERT INTO improvements (number, title, target, patch, measure, ticket, status)
		VALUES (7, 'x', '{baseline/stages/build.md}', '{}', 'fix_rounds_per_ticket', $1, 'removed')`, lone); err != nil {
		t.Fatal(err)
	}
	if w := why(h.m1); !strings.Contains(w, "Crew could not read whether it holds i_0007") {
		t.Fatalf("a removed improvement with no merge: %q", w)
	}
	if _, err := k.d.Server.Exec(ctx, `DELETE FROM improvements WHERE number = 7`); err != nil {
		t.Fatal(err)
	}
	// At its notice a release that holds it is held, so it never installs by itself; the next, with the
	// revert, is not, and replaces it.
	k.releaseAt("0.1.1", h.m3)
	k.releaseAt("0.1.2", revert.commit)
	if err := k.s.Scan(ctx, 2); err != nil {
		t.Fatal(err)
	}
	if r := k.get("0.1.2"); r.Held || r.State != "waiting" {
		t.Fatalf("the release with the revert: %+v", r)
	}
	if r := k.get("0.1.1"); !r.Held || r.HeldWhy == nil || !strings.Contains(*r.HeldWhy, "i_0002") || r.ReplacedBy == nil {
		t.Fatalf("the release before the revert, at its notice: %+v", r)
	}
	// The press of Take it out comes after a notice: the next pass holds what waits.
	revert.stage("in_progress")
	k.pass()
	if r := k.get("0.1.2"); !r.Held || r.HeldWhy == nil || !strings.Contains(*r.HeldWhy, "i_0002") {
		t.Fatalf("a waiting release that holds it: %+v", r)
	}
}

// releaseAt writes a gated release folder of text at commit.
func (k *buildKit) releaseAt(version, commit string) {
	k.t.Helper()
	k.release(version, map[string]string{"baseline/stages/build.md": "z" + version}, false)
	dir := filepath.Join(k.rel, version)
	f, err := release.Read(dir)
	if err != nil {
		k.t.Fatal(err)
	}
	if _, err := release.Write(dir, release.Facts{Version: version, Commit: commit, BuiltAt: k.now, Schema: 51, MachineSetup: 2,
		Images: f.Manifest.Images}, f.Manifest.Files, nil); err != nil {
		k.t.Fatal(err)
	}
	k.gate(version)
}

// takenOut is the revert ticket of an improvement's ticket, merged on the fixture's main at commit.
type takenOut struct {
	k      *buildKit
	id     int64
	commit string
}

func (r takenOut) stage(s string) {
	r.k.t.Helper()
	if _, err := r.k.d.Server.Exec(context.Background(), `UPDATE tickets SET stage = $2 WHERE id = $1`, r.id, s); err != nil {
		r.k.t.Fatal(err)
	}
}

// revertOf reverts the merge of history's ticket n on main, as a revert ticket's merge does (7.13), and
// records the revert ticket, linked to it, with its passed merge check.
func (k *buildKit) revertOf(h history, n int) takenOut {
	k.t.Helper()
	ctx := context.Background()
	work := k.t.TempDir()
	git := func(args ...string) string {
		k.t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = work
		cmd.Env = gitstore.CleanEnv()
		out, err := cmd.CombinedOutput()
		if err != nil {
			k.t.Fatalf("git %v: %v %s", args, err, out)
		}
		return strings.TrimSpace(string(out))
	}
	git("clone", "-q", k.git.Canonical("fixture"), ".")
	var of int64
	if err := k.d.Server.QueryRow(ctx, `SELECT id FROM tickets WHERE project = $1 AND number = $2`, fixture, 900+n).Scan(&of); err != nil {
		k.t.Fatal(err)
	}
	var merge string
	if err := k.d.Server.QueryRow(ctx, `SELECT commit FROM check_runs WHERE ticket = $1 AND purpose = 'merge'`, of).Scan(&merge); err != nil {
		k.t.Fatal(err)
	}
	git("revert", "--no-edit", "-m", "1", merge)
	c := git("rev-parse", "HEAD")
	git("push", "-q", "origin", "main")
	r := takenOut{k: k, commit: c}
	if err := k.d.Server.QueryRow(ctx, `INSERT INTO tickets (project, number, title, kind, stage, origin, created_by, position)
		VALUES ($1, 990, 'revert', 'revert', 'done', 'operator', 'operator', 990) RETURNING id`, fixture).Scan(&r.id); err != nil {
		k.t.Fatal(err)
	}
	if _, err := k.d.Server.Exec(ctx, `INSERT INTO ticket_links (from_ticket, to_ticket, kind) VALUES ($1, $2, 'reverts')`, r.id, of); err != nil {
		k.t.Fatal(err)
	}
	if _, err := k.d.Server.Exec(ctx, `INSERT INTO check_runs (project, ticket, purpose, commit, state) VALUES ($1, $2, 'merge', $3, 'passed')`,
		fixture, r.id, c); err != nil {
		k.t.Fatal(err)
	}
	return r
}

// The clock reads the hold again for the release it is about to install (16.3, since 0.55): an
// improvement taken out after the round held what waited is never installed by the clock with it
// (security review of 1863a0e..f8d57b2).
func TestTheClockInstallsNoReleaseOfAnImprovementTakenOut(t *testing.T) {
	k := newBuildKit(t)
	h := k.history()
	ctx := context.Background()
	k.s.Git = k.git
	if _, err := k.d.Server.Exec(ctx, `INSERT INTO settings (project, doc, updated_by) VALUES (NULL, '{"version": 1, "improvements": {"crew_project": "fixture"}}', 'test')`); err != nil {
		t.Fatal(err)
	}
	k.releaseAt("0.1.1", h.m3)
	if err := k.s.Scan(ctx, 2); err != nil {
		t.Fatal(err)
	}
	if r := k.get("0.1.1"); r.Held || r.State != "waiting" {
		t.Fatalf("before the take-out: %+v", r)
	}
	// The clock waits for the take-out's lock: while one holds it, nothing is installed.
	takeOut, err := k.d.Server.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer takeOut.Rollback(ctx)
	if _, err := takeOut.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtext('crew.releases'))`); err != nil {
		t.Fatal(err)
	}
	if _, err := k.d.Server.Exec(ctx, `UPDATE releases SET install_after = $1 WHERE version = '0.1.1'`, k.now.Add(-time.Hour)); err != nil {
		t.Fatal(err)
	}
	short, cancel := context.WithTimeout(ctx, 300*time.Millisecond)
	err = k.s.Clock(short, updater.Status{})
	cancel()
	if r := k.get("0.1.1"); err == nil || r.State != "waiting" {
		t.Fatalf("the clock did not wait for the take-out: %v %+v", err, r)
	}
	takeOut.Rollback(ctx)
	// Taken out now, and due: the clock holds it instead of asking the updater.
	if _, err := k.d.Server.Exec(ctx, `UPDATE improvements SET status = 'removed' WHERE number = 2`); err != nil {
		t.Fatal(err)
	}
	if _, err := k.d.Server.Exec(ctx, `UPDATE releases SET install_after = $1 WHERE version = '0.1.1'`, k.now.Add(-time.Hour)); err != nil {
		t.Fatal(err)
	}
	if err := k.s.Clock(ctx, updater.Status{}); err != nil {
		t.Fatal(err)
	}
	// Asked of the updater, it would be installing.
	if r := k.get("0.1.1"); !r.Held || r.State != "waiting" || r.HeldWhy == nil || !strings.Contains(*r.HeldWhy, "i_0002") {
		t.Fatalf("the clock installed it: %+v", r)
	}
}
