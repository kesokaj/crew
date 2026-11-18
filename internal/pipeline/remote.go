package pipeline

import (
	"bytes"
	"cmp"
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/kesokaj/crew/internal/board"
	"github.com/kesokaj/crew/internal/cli"
	"github.com/kesokaj/crew/internal/gitstore"
	"github.com/kesokaj/crew/internal/harness"
	"github.com/kesokaj/crew/internal/settings"
)

// maxRemoteErrors is how often a remote main's check is tried when Crew could not run it.
const maxRemoteErrors = 3

// remote fetches the project's remote main (6.6, 7.12), every git.fetch_minutes and before a merge,
// and acts on what came in from outside:
//
//   - a main that follows Crew's is taken as the new base, and the project's tests run on it in a
//     check sandbox: work from outside is not taken as checked, so a failure makes a fix ticket, and
//     Crew's merges wait while that check runs or while its fix ticket is open;
//   - one that changes files that are code waits for the operator in the inbox (13.8);
//   - one that went another way than Crew's is merged with it: the merge is checked, then main moves
//     there and is pushed; a conflict or a failure makes a ticket that joins them, and Crew's merges
//     wait for it.
//
// It says whether Crew's merges must wait this pass, for a remote main or a merge of the two mains
// being checked.
func (p *Pipeline) remote(ctx context.Context, j *job, forMerge bool) (bool, error) {
	if j.proj.Remote == nil {
		return false, nil
	}
	// The fetch runs beside the pass, so a remote that hangs holds up no project (review of M10). A
	// merge waits for a fetch that is due or in flight: commits pushed there become the base first.
	if p.fetch(ctx, j, forMerge) && forMerge {
		return true, nil
	}
	// A ticket for what came in that was not made when it failed is made now (round 2 of M10).
	if err := p.missingTickets(ctx, j); err != nil {
		return false, err
	}
	remote := p.Board.Git.RemoteMainAt(ctx, j.proj.Name)
	local := p.Board.Git.Main(ctx, j.proj.Name)
	if remote != "" && remote == local {
		var id int64
		var kind, state string
		var tries int
		err := p.Pool.QueryRow(ctx, `SELECT id, kind, state, tries FROM remote_mains WHERE project = $1 AND remote = $2 ORDER BY id DESC LIMIT 1`,
			j.proj.ID, remote).Scan(&id, &kind, &state, &tries)
		if err != nil && !errors.Is(err, pgx.ErrNoRows) {
			return false, err
		}
		if err == nil && kind == "ahead" && state == "error" && tries < maxRemoteErrors {
			return true, p.checkRemote(ctx, j, id)
		}
	}
	if forMerge {
		var checking bool
		if err := p.Pool.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM remote_mains WHERE project = $1 AND state = 'checking')`, j.proj.ID).Scan(&checking); err != nil || checking {
			return checking, err
		}
	}
	if remote == "" || local == "" || remote == local || p.Board.Git.Follows(ctx, j.proj.Name, remote, local) {
		return false, nil
	}
	ahead := p.Board.Git.Follows(ctx, j.proj.Name, local, remote)
	// What the operator answers about a remote main is about its commit, whatever Crew merged since
	// (review of M10): held and left stay so, and taken is done.
	var id int64
	var state, base string
	var tries int
	var ticket *int64
	err := p.Pool.QueryRow(ctx, `SELECT id, state, base, tries, ticket FROM remote_mains WHERE project = $1 AND remote = $2 ORDER BY id DESC LIMIT 1`,
		j.proj.ID, remote).Scan(&id, &state, &base, &tries, &ticket)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return false, err
	}
	found := err == nil
	switch {
	case found && (state == "held" || state == "refused"):
		return false, nil
	case found && state == "taken" && ahead:
		return false, p.TakeRemoteMain(ctx, j.proj, local, remote, id)
	case found && state == "taken":
		// A diverged main the operator took, or one Crew merged past since: the two mains are merged,
		// the operator's yes kept. The merge's row takes the place of the taken one (round 2 of M10).
		return true, p.mergeRemote(ctx, j, local, remote, true)
	case found && base == local:
		switch {
		case state == "checking":
			return !ahead, nil
		case state == "error" && tries < maxRemoteErrors:
			return !ahead, p.checkRemote(ctx, j, id)
		case (state == "failed" || state == "conflict") && ticket == nil:
			// Its ticket was not made: it is made now.
			return false, p.ticketFor(ctx, j, id)
		}
		return false, nil
	}
	if ahead {
		return false, p.takeRemote(ctx, j, local, remote)
	}
	return true, p.mergeRemote(ctx, j, local, remote, false)
}

// missingTickets makes the tickets of remote mains that failed their check or did not join, whose
// ticket could not be made then.
func (p *Pipeline) missingTickets(ctx context.Context, j *job) error {
	rows, err := p.Pool.Query(ctx, `SELECT id FROM remote_mains WHERE project = $1 AND state IN ('failed', 'conflict') AND ticket IS NULL`, j.proj.ID)
	if err != nil {
		return err
	}
	ids, err := pgx.CollectRows(rows, pgx.RowTo[int64])
	if err != nil {
		return err
	}
	for _, id := range ids {
		if err := p.ticketFor(ctx, j, id); err != nil {
			return err
		}
	}
	return nil
}

// fetch starts a fetch of the remote's main when one is due, in the background, one a project at a
// time, and says whether one is in flight.
func (p *Pipeline) fetch(ctx context.Context, j *job, forMerge bool) bool {
	if _, busy := p.fetching.Load(j.proj.ID); busy {
		return true
	}
	if !p.Board.FetchDue(ctx, j.proj, num(j.doc, "git.fetch_minutes", 10), forMerge) {
		return false
	}
	if _, busy := p.fetching.LoadOrStore(j.proj.ID, true); busy {
		return true
	}
	proj := j.proj
	p.wg.Add(1)
	go func() {
		defer p.wg.Done()
		defer p.fetching.Delete(proj.ID)
		if _, err := p.Board.FetchMain(context.WithoutCancel(ctx), proj); err != nil {
			// The remote cannot be reached: work goes on on the machine, and the pushes wait (6.6).
			// The next pass comes on its own: a fetch that fails at once does not spin the loop.
			p.log().Warn("fetching the remote's main", "project", proj.Name, "error", err)
			return
		}
		// What someone did on GitHub to the project's pull requests (21, since 0.47).
		if err := p.Board.WatchPulls(context.WithoutCancel(ctx), proj); err != nil {
			p.log().Warn("reading the pull requests on GitHub", "project", proj.Name, "error", err)
		}
		p.Kick()
	}()
	return true
}

// codeIn are the files of a change that are code for the agent CLI, or GitHub workflows (13.8).
// An index file of main that cannot be read, by any failure, never names nothing: every file of the
// change is code, so the remote's main waits for the operator, who can take one that repairs main
// (since 1.05; an error each pass in 1.04, which no remote's main could end).
func (p *Pipeline) codeIn(ctx context.Context, j *job, files []string) ([]string, error) {
	var ferr error
	named, _, nerr := cli.Indexed(func(f string) ([]byte, cli.Way) {
		b, way, err := p.Board.Git.IndexFile(ctx, j.proj.Name, f)
		ferr = cmp.Or(ferr, err)
		return b, way
	})
	if ferr = cmp.Or(ferr, nerr); ferr != nil {
		p.log().Warn("main's index files cannot be read: every file the remote's main changes counts as code", "project", j.proj.Name, "error", ferr)
		return files, nil
	}
	code := append(cli.CodeFiles(), named...)
	// What a link of main that stands for code leads to is code too, and a link that cannot be followed
	// holds the remote's main the same way (since 1.06).
	linked, err := p.Board.Git.Linked(ctx, j.proj.Name, code)
	if err != nil {
		p.log().Warn("main's links cannot be followed: every file the remote's main changes counts as code", "project", j.proj.Name, "error", err)
		return files, nil
	}
	code = append(code, linked...)
	match := cli.NewMatcher(code)
	var out []string
	for _, f := range files {
		if match.Matches(f) {
			out = append(out, f)
		}
	}
	return out, nil
}

// takeRemote takes a remote main that follows Crew's as the new base, and checks it; one that changes
// files that are code waits for the operator instead.
func (p *Pipeline) takeRemote(ctx context.Context, j *job, local, remote string) error {
	files, err := p.Board.Git.ChangedOnMain(ctx, j.proj.Name, local, remote)
	if err != nil {
		return err
	}
	code, err := p.codeIn(ctx, j, files)
	if err != nil {
		return err
	}
	if len(code) > 0 {
		_, err := p.Pool.Exec(ctx, `INSERT INTO remote_mains (project, remote, base, kind, commit, state, files) VALUES ($1, $2, $3, 'ahead', $2, 'held', $4)
			ON CONFLICT DO NOTHING`, j.proj.ID, remote, local, code)
		if err == nil {
			p.log().Info("the remote's main changes files that are code: it waits for the operator", "project", j.proj.Name, "files", code)
		}
		return err
	}
	return p.TakeRemoteMain(ctx, j.proj, local, remote, 0)
}

// TakeRemoteMain moves Crew's main to the remote's, which follows it, and checks it there. held is the
// row of a main that waited for the operator, who took it; 0 for a new one.
func (p *Pipeline) TakeRemoteMain(ctx context.Context, proj *board.Project, local, remote string, held int64) error {
	if err := p.Board.Git.AdvanceMain(ctx, proj.Name, local, remote); err != nil {
		if errors.Is(err, gitstore.ErrMainMoved) {
			return nil // Crew merged meanwhile: the next pass finds them diverged, or joined
		}
		return err
	}
	var id int64
	var err error
	if held != 0 {
		id = held
		_, err = p.Pool.Exec(ctx, `UPDATE remote_mains SET state = 'checking', decided_at = now() WHERE id = $1`, id)
	} else {
		err = p.Pool.QueryRow(ctx, `INSERT INTO remote_mains (project, remote, base, kind, commit, state) VALUES ($1, $2, $3, 'ahead', $2, 'checking')
			RETURNING id`, proj.ID, remote, local).Scan(&id)
	}
	if err != nil {
		return err
	}
	_, _ = p.Board.SyncMainWorkflow(ctx, p.Pool, proj)
	p.log().Info("took the remote's main as the new base", "project", proj.Name, "from", local[:12], "to", remote[:12])
	doc, err := p.Board.Settings(ctx, p.Pool, proj)
	if err != nil {
		return err
	}
	return p.checkRemote(ctx, &job{doc: doc, proj: proj}, id)
}

// mergeRemote merges a remote main that went another way with Crew's, and checks the merge.
func (p *Pipeline) mergeRemote(ctx context.Context, j *job, local, remote string, taken bool) error {
	// What came in from outside is held when it changes files that are code, as a main that only
	// follows Crew's is (13.8, review of M10), unless the operator took it.
	if !taken {
		base, err := p.Board.Git.MergeBase(ctx, j.proj.Name, local, remote)
		if err != nil {
			return err
		}
		files, err := p.Board.Git.ChangedOnMain(ctx, j.proj.Name, base, remote)
		if err != nil {
			return err
		}
		code, err := p.codeIn(ctx, j, files)
		if err != nil {
			return err
		}
		if len(code) > 0 {
			_, err := p.Pool.Exec(ctx, `INSERT INTO remote_mains (project, remote, base, kind, state, files) VALUES ($1, $2, $3, 'diverged', 'held', $4)
				ON CONFLICT DO NOTHING`, j.proj.ID, remote, local, code)
			return err
		}
	}
	id, err := p.Board.Identity(ctx)
	if err != nil {
		return err
	}
	msg := fmt.Sprintf("Merge the remote's main into main\n\nmain went another way on the remote: %s there, %s here.\n", remote[:12], local[:12])
	merge, conflicts, err := p.Board.Git.MergeMains(ctx, j.proj.Name, local, remote, msg, id)
	if err != nil {
		return err
	}
	if len(conflicts) > 0 {
		var row int64
		if err := p.Pool.QueryRow(ctx, `INSERT INTO remote_mains (project, remote, base, kind, state, files, detail) VALUES ($1, $2, $3, 'diverged', 'conflict', $4,
				'the two mains conflict') ON CONFLICT (project, remote, base) DO UPDATE SET kind = 'diverged', state = 'conflict', files = EXCLUDED.files,
				detail = EXCLUDED.detail, decided_at = now() RETURNING id`, j.proj.ID, remote, local, conflicts).Scan(&row); err != nil {
			return err
		}
		return p.ticketFor(ctx, j, row)
	}
	var row int64
	if err := p.Pool.QueryRow(ctx, `INSERT INTO remote_mains (project, remote, base, kind, commit, state) VALUES ($1, $2, $3, 'diverged', $4, 'checking')
		ON CONFLICT (project, remote, base) DO UPDATE SET kind = 'diverged', commit = EXCLUDED.commit, state = 'checking', tries = 0, decided_at = now()
		RETURNING id`, j.proj.ID, remote, local, merge).Scan(&row); err != nil {
		return err
	}
	return p.checkRemote(ctx, j, row)
}

// checkRemote runs the project's setup and tests on the row's commit in a check sandbox, in the
// background, and acts on the result.
func (p *Pipeline) checkRemote(ctx context.Context, j *job, id int64) error {
	var commit, kind, local, remote string
	if err := p.Pool.QueryRow(ctx, `UPDATE remote_mains SET state = 'checking', tries = tries + 1 WHERE id = $1
		RETURNING commit, kind, base, remote`, id).Scan(&commit, &kind, &local, &remote); err != nil {
		return err
	}
	test := str(j.doc, "sandbox.test")
	proj := j.proj
	setup := str(j.doc, "sandbox.setup")
	timeout := num(j.doc, "limits.check_timeout_minutes", 30)
	sizing := p.largeSizing(ctx)
	p.wg.Add(1)
	go func() {
		defer p.wg.Done()
		defer p.Kick()
		bg := context.WithoutCancel(ctx)
		end := func(state, detail string, results []board.CmdResult) {
			if results == nil {
				results = []board.CmdResult{}
			}
			if _, err := p.Pool.Exec(bg, `UPDATE remote_mains SET state = $2, detail = nullif($3, ''), results = $4, decided_at = now() WHERE id = $1`,
				id, state, detail, results); err != nil {
				p.log().Error("recording the check of the remote's main", "error", err)
			}
		}
		if strings.TrimSpace(test) == "" {
			end("passed", "the project has no test command: nothing was run", nil)
			p.afterRemote(bg, proj, id, kind, local, commit, true, "")
			return
		}
		var tree bytes.Buffer
		if err := p.Board.Git.Archive(bg, proj.Name, commit, &tree); err != nil {
			end("error", "the tree to check: "+err.Error(), nil)
			return
		}
		mode, err := settings.CheckNetworkMode(bg, p.Pool, proj.ID, 0)
		if err != nil {
			end("error", err.Error(), nil)
			return
		}
		spec := harness.CheckSpec{Project: proj.ID, Mode: mode, Tree: tree.Bytes(), Setup: setup, Timeout: time.Duration(timeout) * time.Minute, Sizing: sizing,
			Cmds: []harness.CheckCmd{{Name: projectTests + test, Run: test}}}
		state, results, detail, _ := p.execChecks(bg, spec)
		end(state, detail, results)
		if state == board.CheckPassed || state == board.CheckFailed {
			p.afterRemote(bg, proj, id, kind, local, commit, state == board.CheckPassed, failuresText(&board.CheckRun{Results: results}))
		}
	}()
	return nil
}

// afterRemote acts on a remote main's check: a merge of two mains that passed becomes main and is
// pushed; one that failed, and a main from outside that fails its tests, make a fix ticket.
func (p *Pipeline) afterRemote(ctx context.Context, proj *board.Project, id int64, kind, local, commit string, passed bool, failed string) {
	doc, err := p.Board.Settings(ctx, p.Pool, proj)
	if err != nil {
		p.log().Error("after the check of the remote's main", "error", err)
		return
	}
	j := &job{doc: doc, proj: proj}
	switch {
	case passed && kind == "diverged":
		if err := p.Board.Git.AdvanceMain(ctx, proj.Name, local, commit); err != nil {
			// Crew merged meanwhile: the next fetch merges the two again, with that main.
			p.log().Warn("main did not move to the merge with the remote's", "project", proj.Name, "error", err)
			p.Pool.Exec(ctx, `UPDATE remote_mains SET state = 'error', detail = 'main moved meanwhile', tries = $2 WHERE id = $1`, id, maxRemoteErrors)
			return
		}
		if _, err := p.Pool.Exec(ctx, `INSERT INTO pushes (project, remote, refs, who) VALUES ($1, $2, '{refs/heads/main}', $3)`,
			proj.ID, *proj.Remote, board.Backend); err != nil {
			p.log().Error("queueing the push of main", "error", err)
		}
		_, _ = p.Board.SyncMainWorkflow(ctx, p.Pool, proj)
		p.log().Info("main joined the remote's main", "project", proj.Name, "merge", commit[:12])
	case !passed:
		// A ticket that is not made now is made on a later pass (remote).
		if err := p.ticketFor(ctx, j, id); err != nil {
			p.log().Error("making the ticket for the remote's main", "project", proj.Name, "error", err)
		}
	}
}

// ticketFor makes the ticket of a remote main that failed its tests or did not join Crew's.
func (p *Pipeline) ticketFor(ctx context.Context, j *job, id int64) error {
	var kind, state, remote string
	var files []string
	var results []board.CmdResult
	if err := p.Pool.QueryRow(ctx, `SELECT kind, state, remote, files, results FROM remote_mains WHERE id = $1`, id).
		Scan(&kind, &state, &remote, &files, &results); err != nil {
		return err
	}
	failed := failuresText(&board.CheckRun{Results: results})
	switch {
	case kind == "ahead":
		return p.fixTicket(ctx, j, id, "Fix main from the remote: its tests fail",
			"Commits came in on the remote's main from outside Crew, and Crew took them as the new base. The project's tests fail there.",
			"Make the project's tests pass on main again, as they did before those commits.",
			"What failed:\n"+failed)
	case state == "conflict":
		return p.joinTicket(ctx, j, id, remote, "The two mains conflict in: "+strings.Join(files, ", ")+".")
	}
	return p.joinTicket(ctx, j, id, remote, "The merge of the two mains fails the project's tests:\n"+failed)
}

// joinTicket makes the ticket that joins Crew's main with a remote main that went another way: its
// builder reads the remote's main as the branch remote/main. Crew's merges wait for it.
func (p *Pipeline) joinTicket(ctx context.Context, j *job, id int64, remote, why string) error {
	if err := p.Board.Git.ShareRemoteMain(ctx, j.proj.Name); err != nil {
		return err
	}
	return p.fixTicket(ctx, j, id, "Join main with the remote's main",
		"main went another way on the remote than in Crew: commits were pushed there from outside while Crew merged its own, and they do not join by themselves.",
		"Merge the branch remote/main, which holds the remote's main at "+remote[:12]+", into this ticket's branch, and make the project's tests pass on the result.",
		why)
}

// fixTicket adds a ticket for what came in from outside, linked to its row: an agent analyses it, as a
// ticket an agent created (7.6).
func (p *Pipeline) fixTicket(ctx context.Context, j *job, id int64, title, why, what, seen string) error {
	var have *int64
	if err := p.Pool.QueryRow(ctx, `SELECT ticket FROM remote_mains WHERE id = $1`, id).Scan(&have); err != nil || have != nil {
		return err
	}
	t, err := p.Board.AddTicket(ctx, j.proj.ID, board.NewTicket{Title: title, Kind: "fix", Why: why, What: what, Seen: &seen,
		Goal: "main passes the project's tests, with the remote's commits in it."}, board.OriginAgent, board.Backend+" · the fetch of the remote's main")
	if err != nil {
		return err
	}
	_, err = p.Pool.Exec(ctx, `UPDATE remote_mains SET ticket = $2 WHERE id = $1`, id, t.ID)
	if err == nil {
		p.log().Info("made a ticket for what came in from outside", "project", j.proj.Name, "ticket", t.Key)
	}
	return err
}
