package board

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"

	"github.com/kesokaj/crew/internal/db"
	"github.com/kesokaj/crew/internal/settings"
)

// Record is an entry on a ticket (6.3): a note, a review, a test entry, or a comment. A comment with
// no ticket is on the project's plan (11.6).
type Record struct {
	ID           int64     `json:"id"`
	Ticket       *string   `json:"ticket"`
	Kind         string    `json:"kind"`
	Author       string    `json:"author"`
	Run          *string   `json:"run"`
	Subtask      *int64    `json:"subtask"`
	Commit       *string   `json:"commit"`
	Verdict      *string   `json:"verdict"`
	Result       *string   `json:"result"`
	Body         string    `json:"body"`
	InReplyTo    *int64    `json:"inReplyTo"`
	Resolved     bool      `json:"resolved"`
	DeliveredRun *string   `json:"deliveredRun"`
	CreatedAt    time.Time `json:"createdAt"`
	// Stage is the project's own stage a stage entry is about (7.4).
	Stage *string `json:"stage,omitempty"`
	// Quote is the text the operator marked in the task view that a comment of theirs is about, and
	// QuoteSource where it was marked: data, which an agent reads inside a fence (11.9; plan M20 D27).
	Quote       *string      `json:"quote,omitempty"`
	QuoteSource *QuoteSource `json:"quoteSource,omitempty"`
}

// The kinds of record, and the verdicts and results (6.3).
const (
	RecordNote    = "note"
	RecordReview  = "review"
	RecordTest    = "test"
	RecordComment = "comment"
	// RecordE2E is the E2E tester's entry and RecordVerify the verifier's (7.4, from M10), each with a
	// result.
	RecordE2E    = "e2e"
	RecordVerify = "verify"
	// RecordStage is the verdict of a project's own stage's agent, about that stage (7.4).
	RecordStage = "stage"

	VerdictApprove = "approve"
	VerdictChanges = "changes_requested"
)

// RecordFiles are the images kept with an entry, without their data.
func (b *Board) RecordFiles(ctx context.Context, record int64) ([]RecordFile, error) {
	rows, err := b.Pool.Query(ctx, `SELECT name, type FROM record_files WHERE record = $1 ORDER BY id`, record)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(r pgx.CollectableRow) (RecordFile, error) {
		var f RecordFile
		err := r.Scan(&f.Name, &f.Type)
		return f, err
	})
}

// RecordFileData is one image of an entry of a ticket of the project, or NotFound.
func (b *Board) RecordFileData(ctx context.Context, p *Project, record int64, name string) (RecordFile, error) {
	f := RecordFile{Name: name}
	err := b.Pool.QueryRow(ctx, `SELECT f.type, f.data FROM record_files f JOIN records r ON r.id = f.record
		WHERE f.record = $1 AND f.name = $2 AND r.project = $3`, record, name, p.ID).Scan(&f.Type, &f.Data)
	if errors.Is(err, pgx.ErrNoRows) {
		return f, errf(NotFound, "no such file")
	}
	return f, err
}

// MaxRecord is the longest entry, in characters: a review that holds every finding of a large change.
const MaxRecord = 20000

// NewRecord is an entry an agent writes on its own ticket, through the Crew MCP.
type NewRecord struct {
	Kind      string
	Author    string // the agent's role
	Run       string
	Subtask   int64
	Commit    string // the ticket's head the entry is about: a review, a test entry
	Verdict   string
	Result    string
	Body      string
	InReplyTo int64
	// Files are images kept with the entry: the E2E tester's screenshots (7.4).
	Files []RecordFile
	// Stage is the project's own stage a stage entry is about.
	Stage string
}

// RecordFile is an image kept with an entry.
type RecordFile struct {
	Name string `json:"name"`
	Type string `json:"type"`
	Data []byte `json:"-"`
}

const recordCols = `r.id, CASE WHEN r.ticket IS NULL THEN NULL ELSE p.key || '-' || t.number END, r.kind, r.author, r.run::text,
	r.subtask, r.commit, r.verdict, r.result, r.body, r.in_reply_to, r.resolved, r.delivered_run::text, r.created_at, r.stage, r.quote, r.quote_source`

func scanRecord(row pgx.Row) (Record, error) {
	var r Record
	err := row.Scan(&r.ID, &r.Ticket, &r.Kind, &r.Author, &r.Run, &r.Subtask, &r.Commit, &r.Verdict, &r.Result, &r.Body,
		&r.InReplyTo, &r.Resolved, &r.DeliveredRun, &r.CreatedAt, &r.Stage, &r.Quote, &r.QuoteSource)
	return r, err
}

// AddRecord writes an agent's entry on its ticket, with its row in changes. A reply resolves the
// comment it answers; a reply is to a comment on the same ticket, or on the plan.
func (b *Board) AddRecord(ctx context.Context, p *Project, ref string, n NewRecord) (Record, error) {
	var out Record
	if strings.TrimSpace(n.Body) == "" {
		return out, errf(Invalid, "body: the entry is empty")
	}
	if utf8.RuneCountInString(n.Body) > MaxRecord {
		return out, errf(Invalid, "body: at most %d characters", MaxRecord)
	}
	switch n.Kind {
	case RecordNote, RecordComment:
		n.Verdict, n.Result = "", ""
	case RecordReview:
		if n.Verdict != VerdictApprove && n.Verdict != VerdictChanges {
			return out, errf(Invalid, "verdict: approve or changes_requested")
		}
		n.Result = ""
	case RecordTest, RecordE2E, RecordVerify, RecordStage:
		if n.Result != "pass" && n.Result != "fail" {
			return out, errf(Invalid, "result: pass or fail")
		}
		n.Verdict = ""
	default:
		return out, errf(Invalid, "no kind of entry %q", n.Kind)
	}
	num, err := ParseNumber(p, ref)
	if err != nil {
		return out, err
	}
	err = b.inTx(ctx, func(tx pgx.Tx) error {
		// The project first, then the ticket: the order of every write of the board, so an entry
		// and a move of the same ticket wait for each other and never deadlock.
		if _, err := projectByRef(ctx, tx, p.ID, true); err != nil {
			return err
		}
		t, err := ticketByNumber(ctx, tx, p.ID, num, true)
		if err != nil {
			return err
		}
		var replyTo *int64
		if n.InReplyTo != 0 {
			var ok bool
			if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM records WHERE id = $1 AND project = $2 AND kind = 'comment'
					AND (ticket = $3 OR ticket IS NULL))`, n.InReplyTo, p.ID, t.ID).Scan(&ok); err != nil {
				return err
			}
			if !ok {
				return errf(NotFound, "there is no comment %d on %s or on the plan", n.InReplyTo, t.Key)
			}
			replyTo = &n.InReplyTo
		}
		var id int64
		if n.Kind == RecordStage && n.Stage == "" {
			return errf(Invalid, "a stage's verdict names its stage")
		}
		if n.Kind != RecordStage {
			n.Stage = ""
		}
		if err := tx.QueryRow(ctx, `INSERT INTO records (project, ticket, kind, author, run, subtask, commit, verdict, result, body, in_reply_to, stage)
			VALUES ($1, $2, $3, $4, nullif($5, '')::uuid, nullif($6, 0), nullif($7, ''), nullif($8, ''), nullif($9, ''), $10, $11, nullif($12, ''))
			RETURNING id`, p.ID, t.ID, n.Kind, n.Author, n.Run, n.Subtask, n.Commit, n.Verdict, n.Result, n.Body, replyTo, n.Stage).Scan(&id); err != nil {
			return err
		}
		if replyTo != nil {
			if _, err := tx.Exec(ctx, `UPDATE records SET resolved = true WHERE id = $1`, *replyTo); err != nil {
				return err
			}
		}
		if err := pullRecord(ctx, tx, p.ID, t.ID, n); err != nil {
			return err
		}
		for _, f := range n.Files {
			if _, err := tx.Exec(ctx, `INSERT INTO record_files (record, name, type, data) VALUES ($1, $2, $3, $4)`, id, f.Name, f.Type, f.Data); err != nil {
				return err
			}
		}
		after := map[string]any{"record": id, "kind": n.Kind}
		if n.Verdict != "" {
			after["verdict"] = n.Verdict
		}
		if n.Result != "" {
			after["result"] = n.Result
		}
		if err := db.RecordProjectChange(ctx, tx, p.ID, n.Author+" · "+n.Run, "records", t.Key, nil, after); err != nil {
			return err
		}
		out, err = scanRecord(tx.QueryRow(ctx, `SELECT `+recordCols+` FROM records r JOIN projects p ON p.id = r.project
			LEFT JOIN tickets t ON t.id = r.ticket WHERE r.id = $1`, id))
		return err
	})
	return out, err
}

// MaxComment is the longest comment the operator writes.
const MaxComment = 10000

// Comment saves the operator's comment on a ticket, or on the plan when ref is "" (11.6, steps 1
// and 2). A ticket that is Done, cancelled, or discarded takes no comment for an agent: a change
// there is a new ticket (step 6).
func (b *Board) Comment(ctx context.Context, p *Project, ref, body, who string) (Record, error) {
	return b.CommentBy(ctx, p, ref, body, Operator, who)
}

// TokenAuthor is the author of a comment the operator's own tool sent through a personal token
// (11.7): what that tool wrote reaches an agent as data with its author, never as the operator's
// instruction, since the tool is itself an agent.
func TokenAuthor(name string) string { return "token " + name }

// MaxTokenUnresolved is how many comments of one token wait for an answer on a ticket, or on the plan.
const MaxTokenUnresolved = 20

// CommentBy saves a comment of author: the operator's, or one a personal token sent (TokenAuthor).
func (b *Board) CommentBy(ctx context.Context, p *Project, ref, body, author, who string) (Record, error) {
	return b.CommentWith(ctx, p, ref, NewComment{Body: body, Author: author}, who)
}

// NewComment is a comment for an agent: its author's words, and, for the operator's own on a ticket,
// the text they marked in the task view with where it was marked (plan M20 D27).
type NewComment struct {
	Body, Author string
	Quote        string
	QuoteSource  *QuoteSource
}

// The authors whose comments reach an agent (11.6): the operator, a personal token (11.7), and what the
// operator sent on from the chat (11.9). commentBy is the same with records aliased r.
const (
	commentAuthors   = `(author = 'operator' OR author LIKE 'token %' OR author = 'assistant (from the chat)')`
	commentAuthorsOf = `(r.author = 'operator' OR r.author LIKE 'token %' OR r.author = 'assistant (from the chat)')`
)

// CommentWith saves a comment (CommentBy). A marked text goes only with the operator's own comment on
// a ticket, and needs their words beside it.
func (b *Board) CommentWith(ctx context.Context, p *Project, ref string, c NewComment, who string) (Record, error) {
	var out Record
	body, author := strings.TrimSpace(c.Body), c.Author
	if body == "" || utf8.RuneCountInString(body) > MaxComment {
		return out, errf(Invalid, "text: a comment of 1 to %d characters", MaxComment)
	}
	if author != Operator && author != ChatAuthor && !strings.HasPrefix(author, TokenAuthor("")) {
		return out, errf(Invalid, "author %q", author)
	}
	var quote *string
	if strings.TrimSpace(c.Quote) != "" {
		switch {
		case author != Operator || ref == "":
			return out, errf(Invalid, "quote: only with the operator's own comment on a ticket")
		case utf8.RuneCountInString(c.Quote) > MaxQuote:
			return out, errf(Invalid, "quote: at most %d characters", MaxQuote)
		}
		if err := CheckQuoteSource(c.QuoteSource); err != nil {
			return out, err
		}
		quote = &c.Quote
	} else if c.QuoteSource != nil {
		return out, errf(Invalid, "source: only with a quote")
	}
	err := b.inTx(ctx, func(tx pgx.Tx) error {
		p, err := projectByRef(ctx, tx, p.ID, true)
		if err != nil {
			return err
		}
		if p.State == "planning" || p.State == "archived" {
			return errf(Refused, "%s is %s: a comment on its work is made once it runs; the breakdown is sent back with one", p.Name, p.State)
		}
		var ticket *int64
		key := "plan"
		if ref != "" {
			n, err := ParseNumber(p, ref)
			if err != nil {
				return err
			}
			t, err := ticketByNumber(ctx, tx, p.ID, n, true)
			if err != nil {
				return err
			}
			if t.Stage == settings.StageDone || t.Stage == settings.StageClosed {
				return errf(Refused, "%s is %s: it takes no comment for an agent; a change is a new ticket", t.Key, t.Stage)
			}
			ticket, key = &t.ID, t.Key
		}
		var n int
		if err := tx.QueryRow(ctx, `SELECT count(*) FROM records WHERE project = $1 AND kind = 'comment' AND author = $3
			AND ticket IS NOT DISTINCT FROM $2`, p.ID, ticket, author).Scan(&n); err != nil {
			return err
		}
		if n >= 500 {
			return errf(Refused, "%s has 500 comments; write the change as a new ticket", key)
		}
		// Each comment waits for a run's answer and holds the stage: a token, the weaker principal, has
		// at most a few that wait (security review of M9).
		if strings.HasPrefix(author, TokenAuthor("")) {
			if err := tx.QueryRow(ctx, `SELECT count(*) FROM records WHERE project = $1 AND kind = 'comment' AND author = $3
				AND ticket IS NOT DISTINCT FROM $2 AND NOT resolved`, p.ID, ticket, author).Scan(&n); err != nil {
				return err
			}
			if n >= MaxTokenUnresolved {
				return errf(Refused, "%s has %d comments of this token that wait for an answer; write more once they are answered", key, n)
			}
		}
		var id int64
		var src *QuoteSource
		if quote != nil {
			src = c.QuoteSource
		}
		if err := tx.QueryRow(ctx, `INSERT INTO records (project, ticket, kind, author, body, quote, quote_source) VALUES ($1, $2, 'comment', $4, $3, $5, $6)
			RETURNING id`, p.ID, ticket, body, author, quote, src).Scan(&id); err != nil {
			return err
		}
		if err := db.RecordProjectChange(ctx, tx, p.ID, who, "records", key, nil, map[string]any{"record": id, "kind": "comment", "author": author}); err != nil {
			return err
		}
		out, err = scanRecord(tx.QueryRow(ctx, `SELECT `+recordCols+` FROM records r JOIN projects p ON p.id = r.project
			LEFT JOIN tickets t ON t.id = r.ticket WHERE r.id = $1`, id))
		return err
	})
	if err == nil {
		b.Kick()
	}
	return out, err
}

// Records returns a ticket's entries, oldest first, with the comments on the plan when plan is true.
func (b *Board) Records(ctx context.Context, t *Ticket, plan bool) ([]Record, error) {
	rows, err := b.Pool.Query(ctx, `SELECT `+recordCols+` FROM records r JOIN projects p ON p.id = r.project
		LEFT JOIN tickets t ON t.id = r.ticket WHERE r.ticket = $1 OR ($2 AND r.ticket IS NULL AND r.project = $3::uuid) ORDER BY r.id`,
		t.ID, plan, t.Project)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(r pgx.CollectableRow) (Record, error) { return scanRecord(r) })
}

// Waiting returns the operator's comments that no run was given yet: on a ticket, or on the plan
// when ticket is 0 (11.6, step 4).
func (b *Board) Waiting(ctx context.Context, project string, ticket int64) ([]Record, error) {
	rows, err := b.Pool.Query(ctx, `SELECT `+recordCols+` FROM records r JOIN projects p ON p.id = r.project
		LEFT JOIN tickets t ON t.id = r.ticket
		WHERE r.project = $1 AND r.kind = 'comment' AND `+commentAuthorsOf+` AND r.delivered_run IS NULL AND NOT r.resolved
			AND r.ticket IS NOT DISTINCT FROM nullif($2, 0) ORDER BY r.id`, project, ticket)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(r pgx.CollectableRow) (Record, error) { return scanRecord(r) })
}

// ErrTaken is returned when a comment was given to another run meanwhile.
var ErrTaken = errors.New("board: the comment was given to another run")

// Deliver marks comments given to run, in the transaction that records the run or its turn, so each
// is given once and none is lost.
func Deliver(ctx context.Context, tx pgx.Tx, ids []int64, run string) error {
	tag, err := tx.Exec(ctx, `UPDATE records SET delivered_run = $2 WHERE id = ANY($1) AND delivered_run IS NULL`, ids, run)
	if err == nil && tag.RowsAffected() != int64(len(ids)) {
		err = ErrTaken
	}
	return err
}

// Give marks the comments a run's prompt holds as given to it, also those an earlier run was given and
// did not answer, in the transaction that records the run: the run that has them may answer them.
func Give(ctx context.Context, tx pgx.Tx, ids []int64, run string) error {
	_, err := tx.Exec(ctx, `UPDATE records SET delivered_run = $2 WHERE id = ANY($1) AND NOT resolved`, ids, run)
	return err
}

// Unresolved returns the comments on a ticket, and on the plan, that no reply resolved: they are a
// part of the prompt of every run on the ticket until they are answered (8.6).
func (b *Board) Unresolved(ctx context.Context, t *Ticket) ([]Record, error) {
	rows, err := b.Pool.Query(ctx, `SELECT `+recordCols+` FROM records r JOIN projects p ON p.id = r.project
		LEFT JOIN tickets t ON t.id = r.ticket
		WHERE r.ticket = $1 AND r.kind = 'comment' AND `+commentAuthorsOf+` AND NOT r.resolved ORDER BY r.id`, t.ID)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(r pgx.CollectableRow) (Record, error) { return scanRecord(r) })
}

// Reply answers one of the operator's comments as an agent: one that was given to a run of the
// agent's own subtask, on its ticket or on the plan (11.6, step 5). The answer resolves it; a comment
// no run of it was given cannot be silenced by it.
func (b *Board) Reply(ctx context.Context, p *Project, ticket, comment int64, body, author, run string, subtask int64) (Record, error) {
	var out Record
	if strings.TrimSpace(body) == "" || utf8.RuneCountInString(body) > MaxRecord {
		return out, errf(Invalid, "text: an answer of 1 to %d characters", MaxRecord)
	}
	err := b.inTx(ctx, func(tx pgx.Tx) error {
		if _, err := projectByRef(ctx, tx, p.ID, true); err != nil {
			return err
		}
		var on *int64
		err := tx.QueryRow(ctx, `SELECT ticket FROM records WHERE id = $1 AND project = $2 AND kind = 'comment'
			AND `+commentAuthors+` AND (ticket IS NULL OR ticket = $3) AND delivered_run IN (SELECT id FROM runs WHERE subtask = $4) FOR UPDATE`,
			comment, p.ID, ticket, subtask).Scan(&on)
		if errors.Is(err, pgx.ErrNoRows) {
			return errf(NotFound, "there is no comment %d of the operator's that was given to you", comment)
		}
		if err != nil {
			return err
		}
		var id int64
		if err := tx.QueryRow(ctx, `INSERT INTO records (project, ticket, kind, author, run, subtask, body, in_reply_to)
			VALUES ($1, $2, 'comment', $3, nullif($4, '')::uuid, nullif($5, 0), $6, $7) RETURNING id`,
			p.ID, on, author, run, subtask, body, comment).Scan(&id); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `UPDATE records SET resolved = true WHERE id = $1`, comment); err != nil {
			return err
		}
		if err := db.RecordProjectChange(ctx, tx, p.ID, author+" · "+run, "records", fmt.Sprint(comment), nil,
			map[string]any{"record": id, "kind": "comment", "inReplyTo": comment}); err != nil {
			return err
		}
		out, err = scanRecord(tx.QueryRow(ctx, `SELECT `+recordCols+` FROM records r JOIN projects p ON p.id = r.project
			LEFT JOIN tickets t ON t.id = r.ticket WHERE r.id = $1`, id))
		return err
	})
	return out, err
}
