package board

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/kesokaj/crew/internal/cli"
	"github.com/kesokaj/crew/internal/github"
	"github.com/kesokaj/crew/internal/gitstore"
	"github.com/kesokaj/crew/internal/redact"
	"github.com/kesokaj/crew/internal/settings"
)

// Pulls is GitHub's side of a pull request (7.12), with gh's login.
type Pulls interface {
	SignedIn(ctx context.Context) bool
	OpenPull(ctx context.Context, repo, branch, base, title, body string) (github.Pull, error)
	Comment(ctx context.Context, repo string, number int, body string) error
	Status(ctx context.Context, repo, sha, state, name, description string) error
	ClosePull(ctx context.Context, repo string, number int) error
	// GetPull reads one, to see what someone did to it on GitHub (21).
	GetPull(ctx context.Context, repo string, number int) (github.Pull, error)
}

// PullRequest is a ticket's pull request as Crew keeps it (7.12), for the Git screen.
type PullRequest struct {
	Number *int    `json:"number"`
	Ticket string  `json:"ticket"`
	Title  string  `json:"title"`
	State  string  `json:"state"`
	Branch string  `json:"branch"`
	URL    *string `json:"url"`
	// Held is a head that is not pushed: it changes files that are code, and waits for the operator.
	Held bool `json:"held"`
}

// PullRequests are a project's pull requests, newest first.
func (b *Board) PullRequests(ctx context.Context, p *Project) ([]PullRequest, error) {
	rows, err := b.Pool.Query(ctx, `SELECT r.number, $2 || '-' || t.number, t.title, r.state, r.branch, r.url, r.held IS NOT NULL
		FROM pull_requests r JOIN tickets t ON t.id = r.ticket WHERE r.project = $1
		ORDER BY coalesce(r.opened_at, r.created_at) DESC, r.number DESC NULLS FIRST, t.number DESC`, p.ID, p.Key)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(r pgx.CollectableRow) (PullRequest, error) {
		var x PullRequest
		err := r.Scan(&x.Number, &x.Ticket, &x.Title, &x.State, &x.Branch, &x.URL, &x.Held)
		return x, err
	})
}

// UsesPulls says the project's tickets get pull requests now.
func (b *Board) UsesPulls(ctx context.Context, p *Project) bool {
	doc, err := b.Settings(ctx, b.Pool, p)
	return err == nil && b.pullRepo(ctx, p, doc) != ""
}

// pullRepo is the GitHub repository a project's tickets get pull requests in, or "" when they get
// none (7.12, 6.6): a GitHub remote, and git.merge pull_request, or auto with gh signed in. With
// merge_commit, or a remote that is not GitHub's, a ticket ends in a merge commit alone.
func (b *Board) pullRepo(ctx context.Context, p *Project, doc settings.Doc) string {
	if p.Remote == nil {
		return ""
	}
	repo := github.RepoOf(*p.Remote)
	if repo == "" {
		return ""
	}
	switch settingString(doc, "git.merge") {
	case "merge_commit":
		return ""
	case "pull_request":
		return repo
	}
	if b.Pulls == nil || !b.Pulls.SignedIn(ctx) {
		return ""
	}
	return repo
}

// openPull asks for a ticket's pull request as it leaves In progress (7.12): its row, and the post
// that opens it once its branch is on the remote. The branch is pushed by SyncPulls.
func (b *Board) openPull(ctx context.Context, tx pgx.Tx, p *Project, t *Ticket) error {
	if t.Branch == nil {
		return nil
	}
	doc, err := b.Settings(ctx, tx, p)
	if err != nil {
		return err
	}
	repo := b.pullRepo(ctx, p, doc)
	if repo == "" {
		return nil
	}
	tag, err := tx.Exec(ctx, `INSERT INTO pull_requests (ticket, project, repo, branch) VALUES ($1, $2, $3, $4) ON CONFLICT DO NOTHING`,
		t.ID, p.ID, repo, *t.Branch)
	if err != nil || tag.RowsAffected() == 0 {
		return err
	}
	body := noMentions(redact.New().Text(fmt.Sprintf("Ticket %s in Crew: %s\n\n**Why.** %s\n\n**What.** %s\n\n**Goal.** %s\n\nCrew opened this pull request when the ticket left In progress. "+
		"It posts the review, the tests, and its checks here, and merges on its own machine: a push of main marks this pull request merged. "+
		"Agents work on the branch; nothing is merged on GitHub.", t.Key, t.Title, t.Why, t.What, t.Goal)))
	if err := pullPost(ctx, tx, p.ID, t.ID, "open", map[string]any{"title": t.Key + ": " + t.Title, "body": body}); err != nil {
		return err
	}
	// The checks that let it leave In progress ran before it had a pull request: their newest runs on
	// its head are posted now.
	head, err := b.Head(ctx, p, t)
	if err != nil || head == "" {
		return err
	}
	rows, err := tx.Query(ctx, `SELECT DISTINCT ON (purpose) id FROM check_runs WHERE ticket = $1 AND commit = $2 AND state <> 'running'
		ORDER BY purpose, id DESC`, t.ID, head)
	if err != nil {
		return err
	}
	ids, err := pgx.CollectRows(rows, pgx.RowTo[int64])
	if err != nil {
		return err
	}
	for _, id := range ids {
		if err := pullStatus(ctx, tx, id); err != nil {
			return err
		}
	}
	return nil
}

// pullPost queues what Crew tells GitHub about a ticket's pull request, when it has one that is not
// merged or closed yet.
func pullPost(ctx context.Context, tx pgx.Tx, project string, ticket int64, kind string, body map[string]any) error {
	_, err := tx.Exec(ctx, `INSERT INTO github_posts (project, ticket, kind, body)
		SELECT $1, $2, $3, $4 WHERE EXISTS (SELECT 1 FROM pull_requests WHERE ticket = $2 AND state IN ('opening', 'open'))`,
		project, ticket, kind, body)
	return err
}

// pullRecord posts an agent's entry to its ticket's pull request as a comment (7.12).
func pullRecord(ctx context.Context, tx pgx.Tx, project string, ticket int64, n NewRecord) error {
	title := map[string]string{RecordReview: "Review", RecordTest: "Test result", RecordE2E: "End-to-end result",
		RecordVerify: "Verification", RecordStage: "Verdict of the stage " + n.Stage}[n.Kind]
	if title == "" {
		return nil
	}
	head := "**" + title + "** by the " + n.Author
	switch {
	case n.Verdict != "":
		head += ": " + map[string]string{VerdictApprove: "approved", VerdictChanges: "changes requested"}[n.Verdict]
	case n.Result != "":
		head += ": " + map[string]string{"pass": "passed", "fail": "failed"}[n.Result]
	}
	if n.Commit != "" && len(n.Commit) >= 12 {
		head += ", about " + n.Commit[:12]
	}
	return pullPost(ctx, tx, project, ticket, "comment", map[string]any{"body": head + "\n\n" + noMentions(n.Body)})
}

// mention is an @ that GitHub reads as a mention of a user or a team.
var mention = regexp.MustCompile(`@([A-Za-z0-9])`)

// noMentions keeps what agents wrote from mentioning anyone on GitHub in the operator's name: a
// zero-width space after each @ (review of M10).
func noMentions(s string) string { return mention.ReplaceAllString(s, "@\u200b$1") }

// pullStatus posts how a check run of Crew's ended as a status of the commit it is about.
func pullStatus(ctx context.Context, tx pgx.Tx, id int64) error {
	var project, purpose, commit, state string
	var ticket int64
	var head, detail *string
	err := tx.QueryRow(ctx, `SELECT project::text, ticket, purpose, commit, head, state, detail FROM check_runs WHERE id = $1`, id).
		Scan(&project, &ticket, &purpose, &commit, &head, &state, &detail)
	if err != nil {
		return err
	}
	// A merge candidate is not on the pull request: its status is about the head that was merged.
	sha := commit
	if purpose == PurposeMerge && head != nil {
		sha = *head
	}
	if purpose == PurposeBase || len(sha) != 40 {
		return nil
	}
	gh := map[string]string{CheckPassed: "success", CheckFailed: "failure", CheckError: "error"}[state]
	if gh == "" {
		return nil
	}
	what := map[string]string{PurposeExit: "the ticket's checks", PurposeTest: "the checks and the tests, in Test", PurposeMerge: "the checks on top of main",
		PurposeJudge: "a judge's answer", PurposeE2E: "the end-to-end tests", PurposeStage: "a stage's commands", PurposeStageJudge: "a judge, for a stage",
		PurposePart: "a part's check", PurposeJoin: "a join's check"}[purpose]
	// Only what ran and how it ended: what the commands printed stays on the machine (review of M10).
	desc := what + ": " + state
	return pullPost(ctx, tx, project, ticket, "status", map[string]any{"sha": sha, "state": gh, "context": "crew/" + purpose, "description": desc})
}

// SyncPulls keeps the branches of a project's open pull requests pushed (7.12): a head that changed is
// queued for a push, unless it changes files that are code and the operator has not allowed that head
// (13.8), which is held back until they do. A ticket that left the board gets its last word.
func (b *Board) SyncPulls(ctx context.Context, projectID string) error {
	rows, err := b.Pool.Query(ctx, `SELECT r.ticket, coalesce(r.pushed, ''), coalesce(r.held, '') FROM pull_requests r JOIN tickets t ON t.id = r.ticket
		WHERE r.project = $1 AND r.state IN ('opening', 'open') AND t.stage NOT IN ('done', 'closed')`, projectID)
	if err != nil {
		return err
	}
	type pr struct {
		ticket       int64
		pushed, held string
	}
	list, err := pgx.CollectRows(rows, func(r pgx.CollectableRow) (pr, error) {
		var x pr
		return x, r.Scan(&x.ticket, &x.pushed, &x.held)
	})
	if err != nil || len(list) == 0 {
		return err
	}
	p, err := b.Project(ctx, projectID)
	if err != nil {
		return err
	}
	for _, x := range list {
		t, err := ticketByID(ctx, b.Pool, x.ticket, false)
		if err != nil {
			return err
		}
		head, err := b.Head(ctx, p, t)
		if err != nil || head == "" || head == x.pushed || head == x.held {
			if err != nil {
				return err
			}
			continue
		}
		// The files of this head, the one that is pushed if it may be (review of M10).
		code, err := codeChangesAt(ctx, b.Git, p, head)
		if err != nil {
			return err
		}
		err = b.inTx(ctx, func(tx pgx.Tx) error {
			if len(code) > 0 && !CodeAllowed(t, head) {
				if _, err := tx.Exec(ctx, `UPDATE pull_requests SET held = $2, updated_at = now() WHERE ticket = $1`, t.ID, head); err != nil {
					return err
				}
				// Nothing of this ticket's branch goes out while a head waits for the operator; a head
				// that was not pushed is pushed again when it comes back (round 2 of M10).
				tag, err := tx.Exec(ctx, `UPDATE pushes SET state = 'done', done_at = now(), detail = 'held back: a newer head changes files that are code'
					WHERE ticket = $1 AND state = 'pending' AND source = 'quarantine'`, t.ID)
				if err != nil || tag.RowsAffected() == 0 {
					return err
				}
				_, err = tx.Exec(ctx, `UPDATE pull_requests SET pushed = NULL WHERE ticket = $1`, t.ID)
				return err
			}
			return queueBranchPush(ctx, tx, p, t, head)
		})
		if err != nil {
			return err
		}
	}
	return nil
}

// queueBranchPush queues a push of the ticket's branch at head to the remote, once.
func queueBranchPush(ctx context.Context, tx pgx.Tx, p *Project, t *Ticket, head string) error {
	if p.Remote == nil || t.Branch == nil {
		return nil
	}
	tag, err := tx.Exec(ctx, `UPDATE pull_requests SET pushed = $2, held = NULL, updated_at = now() WHERE ticket = $1 AND coalesce(pushed, '') <> $2`, t.ID, head)
	if err != nil || tag.RowsAffected() == 0 {
		return err
	}
	// An older head that did not go out yet never will: the newer one follows it, and an older commit
	// is refused by the remote once the newer is there (round 2 of M10).
	if _, err := tx.Exec(ctx, `UPDATE pushes SET state = 'done', done_at = now(), detail = 'superseded by a newer head'
		WHERE ticket = $1 AND state = 'pending' AND source = 'quarantine'`, t.ID); err != nil {
		return err
	}
	_, err = tx.Exec(ctx, `INSERT INTO pushes (project, remote, refs, who, source, ticket, commit) VALUES ($1, $2, $3, $4, 'quarantine', $5, $6)`,
		p.ID, *p.Remote, []string{"refs/heads/" + *t.Branch}, Backend, t.ID, head)
	return err
}

// codeChangesAt are the files that are code (13.8) that a head of a ticket's branch changes since it
// left main.
func codeChangesAt(ctx context.Context, g *gitstore.Store, p *Project, head string) ([]string, error) {
	files, err := g.ChangedSince(ctx, p.Name, head)
	if err != nil {
		return nil, err
	}
	// An index file that cannot be read is an error, as in codeChanges (since 1.04).
	var ferr error
	named, _, nerr := cli.Indexed(func(f string) ([]byte, cli.Way) {
		b, way, err := g.IndexFile(ctx, p.Name, f)
		ferr = cmp.Or(ferr, err)
		return b, way
	})
	if ferr = cmp.Or(ferr, nerr); ferr != nil {
		return nil, ferr
	}
	code := append(cli.CodeFiles(), named...)
	// And what a link of main that stands for code leads to (since 1.06).
	linked, err := g.Linked(ctx, p.Name, code)
	if err != nil {
		return nil, err
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

// PullsPending tells GitHub what waits for it, in order, of every project (7.12): a post waits for its
// branch's pushes and for its pull request to be opened; one that fails is tried again later, as a
// push is. Nothing here holds up a ticket.
func (b *Board) PullsPending(ctx context.Context) {
	if b.Pulls == nil {
		return
	}
	for i := 0; i < 20; i++ {
		var id, ticket int64
		var kind, repo, branch string
		var body map[string]any
		var number *int
		var tries int
		err := b.Pool.QueryRow(ctx, `UPDATE github_posts g SET tries = g.tries + 1, not_before = now() + interval '10 minutes'
			FROM pull_requests r
			WHERE g.id = (SELECT id FROM github_posts WHERE state = 'pending' AND not_before <= now() ORDER BY id LIMIT 1 FOR UPDATE SKIP LOCKED)
				AND r.ticket = g.ticket
			RETURNING g.id, g.ticket, g.kind, g.body, r.repo, r.branch, r.number, g.tries`).Scan(&id, &ticket, &kind, &body, &repo, &branch, &number, &tries)
		if errors.Is(err, pgx.ErrNoRows) {
			return
		}
		if err != nil {
			b.log().Error("reading what waits for GitHub", "error", err)
			return
		}
		detail, done, wait := b.post(ctx, id, ticket, kind, body, repo, branch, number)
		if !done && wait == 0 {
			wait = time.Minute << min(tries, 6)
		}
		// A post that waits for what comes before it counts no try.
		waiting := !done && detail == waitingDetail
		if _, err := b.Pool.Exec(ctx, `UPDATE github_posts SET state = CASE WHEN $2 THEN 'done' ELSE 'pending' END, detail = $3,
				done_at = CASE WHEN $2 THEN now() END, not_before = now() + $4::interval,
				tries = CASE WHEN $5 THEN tries - 1 ELSE tries END WHERE id = $1`,
			id, done, cutText(detail, 2000), fmt.Sprintf("%d seconds", int(wait.Seconds())), waiting); err != nil {
			b.log().Error("recording a post to GitHub", "error", err)
		}
	}
}

// waitingDetail is a post that waits for what comes before it, which is no failure.
const waitingDetail = "waits for its branch on the remote and its pull request"

// post sends one post. A post that waits for its branch or its pull request comes back soon and
// counts no try.
func (b *Board) post(ctx context.Context, id, ticket int64, kind string, body map[string]any, repo, branch string, number *int) (string, bool, time.Duration) {
	// What a post is about is on the remote first: the branch's pushes, and the pull request itself.
	var before bool
	if err := b.Pool.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM pushes WHERE ticket = $1 AND state = 'pending')
			OR EXISTS (SELECT 1 FROM github_posts WHERE ticket = $1 AND state = 'pending' AND id < $2 AND kind = 'open')`, ticket, id).Scan(&before); err != nil {
		return err.Error(), false, 0
	}
	str := func(k string) string { s, _ := body[k].(string); return s }
	switch {
	case kind == "open" && before:
		return waitingDetail, false, 5 * time.Second
	case kind == "open":
		var pushed bool
		if err := b.Pool.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM pushes WHERE ticket = $1 AND state = 'done')`, ticket).Scan(&pushed); err != nil {
			return err.Error(), false, 0
		}
		if !pushed {
			return waitingDetail, false, 5 * time.Second
		}
		pr, err := b.Pulls.OpenPull(ctx, repo, branch, "main", str("title"), str("body"))
		if err != nil {
			b.log().Warn("GitHub did not open a pull request; it is tried again", "ticket", ticket, "error", err)
			return pullFailure(err), false, 0
		}
		// The page links it: only GitHub's own address is kept (review of M10).
		url := pr.URL
		if !strings.HasPrefix(url, "https://github.com/") {
			url = ""
		}
		if _, err := b.Pool.Exec(ctx, `UPDATE pull_requests SET number = $2, url = nullif($3, ''), state = CASE WHEN state = 'opening' THEN 'open' ELSE state END,
				opened_at = now(), updated_at = now() WHERE ticket = $1`, ticket, pr.Number, url); err != nil {
			return err.Error(), false, 0
		}
		if _, err := b.Pool.Exec(ctx, `UPDATE tickets SET pull_request = $2 WHERE id = $1`, ticket, pr.Number); err != nil {
			return err.Error(), false, 0
		}
		b.log().Info("opened a pull request", "ticket", ticket, "number", pr.Number)
		return fmt.Sprintf("opened #%d", pr.Number), true, 0
	case number == nil || before:
		return waitingDetail, false, 5 * time.Second
	}
	var err error
	switch kind {
	case "comment":
		err = b.Pulls.Comment(ctx, repo, *number, str("body"))
	case "status":
		err = b.Pulls.Status(ctx, repo, str("sha"), str("state"), str("context"), str("description"))
	case "close":
		err = b.Pulls.ClosePull(ctx, repo, *number)
	}
	if err != nil {
		b.log().Warn("a post to GitHub failed; it is tried again", "ticket", ticket, "kind", kind, "error", err)
		return pullFailure(err), false, 0
	}
	return "posted", true, 0
}

// pullFailure says what kind of failure a post was, in Crew's words; GitHub's own are in the log.
func pullFailure(err error) string {
	var ge *github.Error
	if errors.As(err, &ge) {
		if strings.Contains(ge.Msg, "cannot be reached") {
			return "GitHub cannot be reached"
		}
		return ge.Msg
	}
	return "the post failed; the server's log says why"
}
