package board

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5"

	"github.com/kesokaj/crew/internal/cli"
	"github.com/kesokaj/crew/internal/db"
	"github.com/kesokaj/crew/internal/github"
	"github.com/kesokaj/crew/internal/settings"
)

// WatchPulls reads the project's open pull requests on GitHub, with the server's own gh login, after
// a fetch (21, 7.12, since 0.47). One merged there by someone else makes its ticket Done with a note;
// Crew then runs the ticket's checks on main, once the merge is in it. One closed there without a
// merge gets a note, and nothing moves. A branch that moved there is taken when it follows Crew's, and
// is not taken as checked; one that went another way is not taken, and the inbox says so.
func (b *Board) WatchPulls(ctx context.Context, p *Project) error {
	if b.Pulls == nil || p.Remote == nil || !b.Pulls.SignedIn(ctx) {
		return nil
	}
	rows, err := b.Pool.Query(ctx, `SELECT r.ticket, r.repo, r.branch, r.number, coalesce(r.pushed, ''), coalesce(r.diverged, ''), coalesce(r.diverged_kept, '')
		FROM pull_requests r JOIN tickets t ON t.id = r.ticket
		WHERE r.project = $1 AND r.state = 'open' AND r.number IS NOT NULL AND t.stage NOT IN ('done', 'closed')`, p.ID)
	if err != nil {
		return err
	}
	type pr struct {
		ticket           int64
		repo, branch     string
		number           int
		pushed, diverged string
		kept             string
	}
	list, err := pgx.CollectRows(rows, func(r pgx.CollectableRow) (pr, error) {
		var x pr
		return x, r.Scan(&x.ticket, &x.repo, &x.branch, &x.number, &x.pushed, &x.diverged, &x.kept)
	})
	if err != nil {
		return err
	}
	var errs []error
	for _, x := range list {
		got, err := b.Pulls.GetPull(ctx, x.repo, x.number)
		if err != nil {
			errs = append(errs, fmt.Errorf("pull request #%d: %w", x.number, err))
			continue
		}
		t, err := ticketByID(ctx, b.Pool, x.ticket, false)
		if err != nil {
			return err
		}
		switch {
		case got.Merged:
			err = b.mergedOutside(ctx, p, t, got)
		case got.State == "closed":
			err = b.closedOutside(ctx, p, t, got)
		case got.Head.SHA != "" && got.Head.SHA != x.pushed && got.Head.SHA != x.diverged && got.Head.SHA != x.kept:
			err = b.branchOutside(ctx, p, t, x.branch, got.Head.SHA)
		}
		if err != nil {
			errs = append(errs, fmt.Errorf("%s: %w", t.Key, err))
		}
	}
	return errors.Join(errs...)
}

// mergedOutside makes a ticket whose pull request someone merged on GitHub Done, with a note, without
// Crew's own merge: the merge is on the remote's main, which the fetch takes as the new base.
func (b *Board) mergedOutside(ctx context.Context, p *Project, t *Ticket, got github.Pull) error {
	by := got.MergedBy.Login
	if by == "" {
		by = "someone"
	}
	reason := "merged on GitHub by " + by
	moved := false
	err := b.inTx(ctx, func(tx pgx.Tx) error {
		if _, err := projectByRef(ctx, tx, p.ID, true); err != nil {
			return err
		}
		t, err := ticketByID(ctx, tx, t.ID, true)
		if err != nil || t.Stage == settings.StageDone || t.Stage == settings.StageClosed {
			return err
		}
		doc, err := b.Settings(ctx, tx, p)
		if err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `UPDATE pull_requests SET state = 'merged', outside = 'merged', outside_by = $2, outside_head = nullif($3, ''),
				updated_at = now() WHERE ticket = $1`, t.ID, by, got.Head.SHA); err != nil {
			return err
		}
		moved = true
		return b.moveWith(ctx, tx, p, PipelineOf(doc), t, settings.StageDone, Backend, reason, nil, true)
	})
	if err != nil || !moved {
		return err
	}
	b.Kick()
	if _, err := b.AddRecord(ctx, p, t.Key, NewRecord{Kind: RecordNote, Author: "crew", Body: fmt.Sprintf(
		"Its pull request #%d was merged on GitHub by %s, not by Crew: the ticket is Done. Crew takes the remote's main as the new base, "+
			"and runs this ticket's checks on it; a fix ticket follows if they fail. Work that came in from outside is not taken as checked.", got.Number, by)}); err != nil {
		return err
	}
	return RecordSignal(ctx, b.Pool, Signal{Project: p.ID, Ticket: t.ID, Kind: "merged_outside",
		Detail: map[string]any{"text": fmt.Sprintf("%s was merged on GitHub by %s: it is Done, and its checks run on main", t.Key, by)}})
}

// closedOutside notes a pull request someone closed on GitHub without a merge: nothing moves, and Crew
// does not open it again.
func (b *Board) closedOutside(ctx context.Context, p *Project, t *Ticket, got github.Pull) error {
	tag, err := b.Pool.Exec(ctx, `UPDATE pull_requests SET state = 'closed', outside = 'closed', updated_at = now() WHERE ticket = $1 AND state = 'open'`, t.ID)
	if err != nil || tag.RowsAffected() == 0 {
		return err
	}
	_, err = b.AddRecord(ctx, p, t.Key, NewRecord{Kind: RecordNote, Author: "crew", Body: fmt.Sprintf(
		"Its pull request #%d was closed on GitHub without a merge. The ticket goes on as it was; Crew does not open the pull request again.", got.Number)})
	return err
}

// branchOutside acts on a ticket's branch that moved on GitHub: a head that follows Crew's is fetched
// and becomes the branch's head, which is not taken as checked; one that went another way is kept out,
// for the inbox.
func (b *Board) branchOutside(ctx context.Context, p *Project, t *Ticket, branch, sha string) error {
	env, done, err := b.remoteEnv(ctx, p)
	if err != nil {
		return err
	}
	defer done()
	fetched, err := b.Git.FetchBranch(ctx, p.Name, *p.Remote, branch, env)
	if err != nil || fetched == "" {
		return err
	}
	if head, err := b.Git.Head(ctx, p.Name, branch); err != nil || head == fetched {
		return err
	}
	ff, err := b.Git.FollowsBranch(ctx, p.Name, branch, fetched)
	if err != nil {
		return err
	}
	why := ""
	if !ff {
		why = "it does not follow Crew's branch"
	} else if instr, err := b.instructionsIn(ctx, p, branch, fetched); err != nil {
		return err
	} else if len(instr) > 0 {
		// What instructs agents never comes in from outside on a ticket's branch: it would reach the
		// next builder, tester, and E2E tester as their instructions (13.8; security review of M13).
		why = "it changes files that instruct agents: " + strings.Join(instr, ", ")
	}
	if why != "" {
		_, err := b.Pool.Exec(ctx, `UPDATE pull_requests SET diverged = $2, diverged_why = $3, updated_at = now() WHERE ticket = $1`, t.ID, fetched, why)
		return err
	}
	if err := b.Git.AdvanceBranch(ctx, p.Name, branch, fetched); err != nil {
		return err
	}
	if _, err := b.Pool.Exec(ctx, `UPDATE pull_requests SET pushed = $2, diverged = NULL, updated_at = now() WHERE ticket = $1`, t.ID, fetched); err != nil {
		return err
	}
	b.Kick()
	_, err = b.AddRecord(ctx, p, t.Key, NewRecord{Kind: RecordNote, Author: "crew", Body: fmt.Sprintf(
		"Its branch moved on GitHub, to %s, which follows Crew's: Crew takes it as the branch's head. It is not taken as checked: the stage's checks run on it again.", fetched[:12])})
	return err
}

// instructionsIn are the files a head from outside changes since the ticket's branch that instruct
// agents, as the git endpoint judges a push (13.8): the CLI's instruction files and what an index file
// names, on either side.
func (b *Board) instructionsIn(ctx context.Context, p *Project, branch, fetched string) ([]string, error) {
	head, err := b.Git.Head(ctx, p.Name, branch)
	if err != nil {
		return nil, err
	}
	files, err := b.Git.ChangedOnMain(ctx, p.Name, head, fetched)
	if err != nil {
		return nil, err
	}
	// An index file that cannot be read is an error, never one that names nothing (since 1.04).
	var ferr error
	read := func(rev string) func(string) ([]byte, cli.Way) {
		return func(path string) ([]byte, cli.Way) {
			out, way, err := b.Git.IndexFileAt(ctx, p.Name, rev, path)
			ferr = cmp.Or(ferr, err)
			return out, way
		}
	}
	_, namedNew, nerr := cli.Indexed(read(fetched))
	ferr = cmp.Or(ferr, nerr)
	_, namedOld, nerr := cli.Indexed(read(head))
	ferr = cmp.Or(ferr, nerr)
	// And main's as it is now, which may have moved on since the branch left it (since 1.05).
	_, namedMain, nerr := cli.Indexed(func(path string) ([]byte, cli.Way) {
		out, way, err := b.Git.IndexFile(ctx, p.Name, path)
		ferr = cmp.Or(ferr, err)
		return out, way
	})
	if ferr = cmp.Or(ferr, nerr); ferr != nil {
		return nil, ferr
	}
	instr := append(append(append(cli.InstructionFiles(), namedNew...), namedOld...), namedMain...)
	// What a link that stands for a file that instructs leads to instructs too, on either side and on
	// main (since 1.06).
	var linked []string
	for _, rev := range []string{fetched, head, ""} {
		var l []string
		if rev == "" {
			l, err = b.Git.Linked(ctx, p.Name, instr)
		} else {
			l, err = b.Git.LinkedAt(ctx, p.Name, rev, instr)
		}
		if err != nil {
			return nil, err
		}
		linked = append(linked, l...)
	}
	instr = append(instr, linked...)
	match := cli.NewMatcher(instr)
	var out []string
	for _, f := range files {
		if match.Matches(f) {
			out = append(out, f)
		}
	}
	return out, nil
}

// KeepCrewsBranch is the operator's answer to a branch that went another way on GitHub: Crew keeps its
// own, and the inbox no longer asks about that head.
func (b *Board) KeepCrewsBranch(ctx context.Context, p *Project, ref, who string) error {
	t, err := b.Ticket(ctx, p, ref)
	if err != nil {
		return err
	}
	return b.inTx(ctx, func(tx pgx.Tx) error {
		tag, err := tx.Exec(ctx, `UPDATE pull_requests SET diverged_kept = diverged, diverged = NULL, diverged_why = NULL, updated_at = now()
			WHERE ticket = $1 AND diverged IS NOT NULL`, t.ID)
		if err != nil {
			return err
		}
		if tag.RowsAffected() == 0 {
			return errf(Refused, "the branch of %s on GitHub has not gone another way", t.Key)
		}
		return db.RecordProjectChange(ctx, tx, p.ID, who, "pull_requests", t.Key, nil, map[string]any{"diverged": "kept Crew's branch"})
	})
}

// remoteEnv is how git reaches the project's remote: with the server's SSH key for GitHub.
func (b *Board) remoteEnv(ctx context.Context, p *Project) ([]string, func(), error) {
	if !github.SSHURL.MatchString(*p.Remote) {
		return nil, func() {}, nil
	}
	if b.GitHub == nil {
		return nil, nil, &github.Error{Msg: "GitHub is not set up on this server"}
	}
	return b.GitHub.SSHEnv(ctx)
}
