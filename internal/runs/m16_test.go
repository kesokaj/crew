package runs

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/kesokaj/crew/internal/cli"
	"github.com/kesokaj/crew/internal/harness"
)

// An eval's run of a release's suite starts from the candidate's runtime image (16.2 step 5, since
// 0.52): its subtask keeps the image, the run's spec names it, its row records it, and a run that
// continues the subtask starts from it too. A run that is not an eval's names none, nor one that is no
// id.
func TestAGateEvalRunStartsFromTheCandidatesImage(t *testing.T) {
	k := newKit(t)
	ctx := context.Background()
	const fixture = "f1c50000-0000-4000-8000-000000000001"
	img := "sha256:" + strings.Repeat("d", 64)
	evalOn(t, k, "eval/9", "b45e0000")
	k.play(harness.Script{Recording: agyRun, Hold: true})
	run, sub := k.startRole(RoleRun{Role: "builder", Kind: "eval_build", Project: fixture, Branch: "eval/9", Base: "refs/heads/eval/9", Eval: true, Image: img})
	k.wait(run, "working")
	if s := started(k); len(s) != 1 || s[0].Image != img {
		t.Fatalf("the run's spec: %+v", s)
	}
	if n := k.count(`SELECT count(*) FROM runs r JOIN subtasks s ON s.id = r.subtask WHERE r.id = $1 AND r.image = $2 AND s.image = $2`, run, img); n != 1 {
		t.Fatal("the run and its subtask do not record the candidate's image")
	}
	k.d.Stop(ctx, run)
	k.wait(run, "ended")
	k.play(harness.Script{Recording: agyRun, Hold: true})
	again, err := k.d.Continue(ctx, sub, ContinueRequest{Text: "Go on."})
	if err != nil {
		t.Fatal(err)
	}
	k.wait(again, "working")
	if s := started(k); len(s) != 2 || s[1].Image != img {
		t.Fatalf("the continued run's spec: %+v", s)
	}
	if n := k.count(`SELECT count(*) FROM runs WHERE id = $1 AND image = $2`, again, img); n != 1 {
		t.Fatal("the continued run does not record the candidate's image")
	}
	k.d.Stop(ctx, again)
	k.wait(again, "ended")

	for _, rr := range []RoleRun{
		{Role: "builder", Kind: "build", Project: fixture, Image: img},
		{Role: "builder", Kind: "eval_build", Project: fixture, Branch: "eval/9", Base: "refs/heads/eval/9", Eval: true, Image: "crew-runtime:latest"},
	} {
		rr.CLI, rr.Parts = "agy", []PromptPart{{Part: "agent", Source: "baseline/agents/builder.md@test", Text: "You build."}}
		if _, _, err := k.d.StartRole(ctx, rr); err == nil {
			t.Fatalf("a run of kind %s started from the image %q", rr.Kind, rr.Image)
		}
	}
	if s := started(k); len(s) != 2 {
		t.Fatalf("%d runs started", len(s))
	}
}

// A ticket's run names no image: only an eval's does (16.2 step 5, since 0.52).
func TestAGateEvalImageIsNoTicketRuns(t *testing.T) {
	k, ticket := ticketKit(t)
	if _, _, err := k.d.StartRole(context.Background(), RoleRun{Role: "builder", Kind: "build", Project: tshop, Ticket: ticket, Branch: "task/SF-1-lock",
		CLI: "agy", Parts: []PromptPart{{Part: "agent", Source: "baseline/agents/builder.md@test", Text: "You build."}}, Image: "sha256:" + strings.Repeat("d", 64)}); err == nil {
		t.Fatal("a ticket's run started from an image it names")
	}
	if s := started(k); len(s) != 0 {
		t.Fatalf("%d runs started", len(s))
	}
}

// A run the monitor ended past its idle time, its turn ended well, can be ended for a reason of Crew's
// own after it, as End ends one that waits (since 0.55): the pipeline took up its turn and found its
// rounds spent. The run records the reason, and its subtask, done, waits for its one retry with one
// stop. A run that ended otherwise, or one that still waits, is refused, and changes nothing.
func TestARunThatEndedWhileItWaitedIsEndedForItsRounds(t *testing.T) {
	k := newKit(t)
	ctx := context.Background()
	run := k.start(StartRequest{})
	k.wait(run, "idle")
	if err := k.d.EndWaited(ctx, run, ReasonRounds, "5 rounds of checks failed"); err == nil {
		t.Fatal("a run that waits was ended as one that ended")
	}
	k.mu.Lock()
	k.now = time.Now().Add(11 * time.Minute)
	k.mu.Unlock()
	k.d.Tick(ctx)
	if v := k.wait(run, "ended"); v.StopReason != cli.ReasonOK || v.SubtaskState != "done" {
		t.Fatalf("past its idle time: %+v", v)
	}
	if err := k.d.EndWaited(ctx, run, ReasonRounds, "5 rounds of checks failed"); err != nil {
		t.Fatal(err)
	}
	v := k.wait(run, "ended")
	if v.StopReason != ReasonRounds || v.SubtaskState != "waiting" || v.WaitFor != "retry" {
		t.Fatalf("ended for its rounds: %+v", v)
	}
	if n := k.count(`SELECT count(*) FROM runs r JOIN subtasks s ON s.id = r.subtask WHERE r.id = $1 AND s.stops = 1
		AND r.stop_detail = '5 rounds of checks failed'`, run); n != 1 {
		t.Fatal("the stop was not counted, or the detail not kept")
	}
	if err := k.d.EndWaited(ctx, run, ReasonRounds, "again"); err == nil {
		t.Fatal("a run was ended for its rounds twice")
	}
	// A run Crew finished is no run that waited.
	k.mu.Lock()
	k.now = time.Time{}
	k.mu.Unlock()
	other := k.start(StartRequest{})
	k.wait(other, "idle")
	if err := k.d.Finish(ctx, other); err != nil {
		t.Fatal(err)
	}
	k.wait(other, "ended")
	if err := k.d.EndWaited(ctx, other, ReasonRounds, "5 rounds of checks failed"); err == nil {
		t.Fatal("a finished run was ended for its rounds")
	}
	if n := k.count(`SELECT count(*) FROM runs r JOIN subtasks s ON s.id = r.subtask WHERE r.id = $1 AND r.stop_reason = 'ok' AND s.state = 'done' AND s.stops = 0`, other); n != 1 {
		t.Fatal("the refused end changed the finished run")
	}
}

// A run in a candidate's runtime image records the agy of that release, as its release.json names it,
// not the running release's login check (the gate of 0.2.25 ran 1.4.5 and recorded 1.4.4): an eval's
// run of a release's suite, and a run of crew-gate's real ticket. A candidate no release names, or one
// whose agy is unknown, records none; a run in the running image keeps the login check's version.
func TestARunInACandidatesImageRecordsItsReleasesAgy(t *testing.T) {
	k, ticket := ticketKit(t)
	ctx := context.Background()
	const fixture = "f1c50000-0000-4000-8000-000000000001"
	known, unknown, none, gate := "sha256:"+strings.Repeat("a", 64), "sha256:"+strings.Repeat("b", 64),
		"sha256:"+strings.Repeat("c", 64), "sha256:"+strings.Repeat("e", 64)
	for v, m := range map[string]string{
		"9.0.1": `{"images": {"crew-runtime": {"id": "` + known + `"}}, "pins": {"agy": "1.4.5"}}`,
		"9.0.2": `{"images": {"crew-runtime": {"id": "` + unknown + `"}}, "pins": {"agy": "unknown"}}`,
		"9.0.3": `{"images": {"crew-runtime": {"id": "` + gate + `"}}, "pins": {"agy": "1.4.6"}}`,
	} {
		if _, err := k.db.Server.Exec(ctx, `INSERT INTO releases (version, commit, manifest_sha256, manifest, built_at, kind)
			VALUES ($1, 'c0ffee', 'feed', $2, now(), 'code')`, v, m); err != nil {
			t.Fatal(err)
		}
	}
	version := func(run string) string {
		var v *string
		if err := k.db.Server.QueryRow(ctx, `SELECT cli_version FROM runs WHERE id = $1`, run).Scan(&v); err != nil {
			t.Fatal(err)
		}
		if v == nil {
			return "<none>"
		}
		return *v
	}
	for i, c := range []struct{ image, want string }{{known, "1.4.5"}, {unknown, "<none>"}, {none, "<none>"}} {
		branch := "eval/" + string(rune('1'+i))
		evalOn(t, k, branch, "b45e0000")
		run, _ := k.startRole(RoleRun{Role: "builder", Kind: "eval_build", Project: fixture, Branch: branch, Base: "refs/heads/" + branch, Eval: true, Image: c.image})
		k.wait(run, "idle", "ended")
		if got := version(run); got != c.want {
			t.Errorf("an eval's run in %s recorded agy %q, not %q", c.image[:12], got, c.want)
		}
	}
	plain := k.start(StartRequest{})
	k.wait(plain, "idle", "ended")
	if got := version(plain); got != "1.0" {
		t.Errorf("a run in the running image recorded agy %q, not the login check's 1.0", got)
	}
	// crew-gate's real ticket runs in the image its project names.
	if _, err := k.db.Server.Exec(ctx, `UPDATE projects SET hidden = true, runtime_image = $2 WHERE id = $1`, tshop, gate); err != nil {
		t.Fatal(err)
	}
	// The builder's model, which the judge below keeps off (checkguard.go).
	run, _ := k.startRole(RoleRun{Role: "builder", Kind: "build", Project: tshop, Ticket: ticket, Branch: "task/SF-1-lock", Model: "gemini-3.8-flash"})
	k.wait(run, "idle", "ended")
	if got := version(run); got != "1.4.6" {
		t.Errorf("the gate's ticket's run recorded agy %q, not its candidate's 1.4.6", got)
	}
	if n := k.count(`SELECT count(*) FROM runs WHERE id = $1 AND image = $2`, run, gate); n != 1 {
		t.Fatal("the gate's ticket's run does not record the candidate's image")
	}
	// Continued, as a fix round is, it keeps the candidate's (review of d79d6b3).
	var runSub int64
	if err := k.db.Server.QueryRow(ctx, `SELECT subtask FROM runs WHERE id = $1`, run).Scan(&runSub); err != nil {
		t.Fatal(err)
	}
	k.d.Stop(ctx, run)
	k.wait(run, "ended")
	fix, err := k.d.Continue(ctx, runSub, ContinueRequest{Text: "Fix it."})
	if err != nil {
		t.Fatal(err)
	}
	k.wait(fix, "idle", "ended")
	if n := k.count(`SELECT count(*) FROM runs WHERE id = $1 AND image = $2`, fix, gate); n != 1 || version(fix) != "1.4.6" {
		t.Fatalf("the gate's ticket's continued run does not record the candidate's image, or agy %q", version(fix))
	}
	// A judge of crew-gate runs in the running release's image, whatever its project names, as its
	// sandbox does (13.4), and records it with the login check's version, also when it is continued
	// (GATE-7 and GATE-8 on dave recorded the candidate's).
	jrun, jsub := k.startRole(RoleRun{Role: "judge", Kind: "judge", Project: tshop, Ticket: ticket, Judge: true, Model: "claude-sonnet-5-5",
		Parts: []PromptPart{{Part: "agent", Source: "baseline/agents/judge.md@test", Text: "You judge."}}})
	k.wait(jrun, "idle", "ended")
	if s := started(k); s[len(s)-1].Image != "" {
		t.Fatalf("the judge's spec names the image %q", s[len(s)-1].Image)
	}
	if n := k.count(`SELECT count(*) FROM runs r, runs p WHERE r.id = $1 AND p.id = $2 AND r.image = p.image`, jrun, plain); n != 1 || version(jrun) != "1.0" {
		t.Fatalf("the gate's judge recorded another image than the running release's, or agy %q", version(jrun))
	}
	k.d.Stop(ctx, jrun)
	k.wait(jrun, "ended")
	again, err := k.d.Continue(ctx, jsub, ContinueRequest{Text: "Again."})
	if err != nil {
		t.Fatal(err)
	}
	k.wait(again, "idle", "ended")
	if s := started(k); !s[len(s)-1].Judge || s[len(s)-1].Image != "" {
		t.Fatalf("the continued judge's spec: judge %v, image %q", s[len(s)-1].Judge, s[len(s)-1].Image)
	}
	if n := k.count(`SELECT count(*) FROM runs r, runs p WHERE r.id = $1 AND p.id = $2 AND r.image = p.image`, again, plain); n != 1 || version(again) != "1.0" {
		t.Fatalf("the gate's continued judge recorded another image than the running release's, or agy %q", version(again))
	}
}
