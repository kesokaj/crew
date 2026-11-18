package httpapi

import (
	"context"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/kesokaj/crew/internal/board"
)

// The routes of M6 (15.4): comments on the plan or a ticket, a ticket's subtasks, the summaries, and
// the decisions agents recorded.
func (s *Server) ticketRoutes(api *routes) {
	api.HandleFunc("POST /api/projects/{p}/comments", s.comment)
	api.HandleFunc("GET /api/projects/{p}/tasks/{t}/subtasks", s.taskSubtasks)
	api.HandleFunc("GET /api/projects/{p}/tasks/{t}/summary", s.taskSummary)
	api.HandleFunc("GET /api/projects/{p}/summaries", s.projectSummaries)
	api.HandleFunc("GET /api/projects/{p}/decisions", s.projectDecisions)
	api.HandleFunc("POST /api/projects/{p}/decisions/{id}/change", s.changeDecision)
}

// changeDecision is the operator's change of a decision (7.5): a ticket carries it out.
func (s *Server) changeDecision(w http.ResponseWriter, r *http.Request) {
	b, p := s.project(w, r)
	if p == nil {
		return
	}
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		writeError(w, http.StatusNotFound, "NOT_FOUND", "no such decision")
		return
	}
	var req struct {
		Choice string `json:"choice"`
	}
	if !decode(w, r, &req) {
		return
	}
	t, err := b.ChangeDecision(r.Context(), p, id, req.Choice, board.Operator)
	if err != nil {
		s.boardError(w, "changing the decision", err)
		return
	}
	s.changed(w, b, http.StatusCreated, t)
}

// comment is the operator's comment on the plan, or on a ticket when ticket names one (11.6).
func (s *Server) comment(w http.ResponseWriter, r *http.Request) {
	b, p := s.project(w, r)
	if p == nil {
		return
	}
	var req struct {
		Ticket string `json:"ticket"`
		Text   string `json:"text"`
		// Remember keeps the comment for the project too: a memory entry of the operator's (9.5).
		Remember bool `json:"remember"`
		// Quote is the text the operator marked in the task view, and Source where: their comment is
		// about it, and the agent reads it as a quote (11.9, plan M20 D27).
		Quote  string             `json:"quote"`
		Source *board.QuoteSource `json:"source"`
	}
	if !decode(w, r, &req) {
		return
	}
	// The entry is checked first, so a comment is not saved when what it should keep is refused.
	var entry board.NewEntry
	var src board.MemorySource
	if req.Remember {
		title, body, _ := strings.Cut(strings.TrimSpace(req.Text), "\n")
		entry = board.NewEntry{Kind: "convention", Title: strings.TrimSpace(title), Body: strings.TrimSpace(body)}
		if req.Ticket != "" {
			t, err := b.Ticket(r.Context(), p, req.Ticket)
			if err != nil {
				s.boardError(w, "reading the ticket", err)
				return
			}
			src.Ticket = t.ID
		}
		if err := b.TryRemember(r.Context(), p, entry, board.Operator); err != nil {
			s.boardError(w, "keeping the comment for the project", err)
			return
		}
	}
	c, err := b.CommentWith(r.Context(), p, req.Ticket, board.NewComment{Body: req.Text, Author: board.Operator, Quote: req.Quote, QuoteSource: req.Source}, board.Operator)
	if err != nil {
		s.boardError(w, "saving the comment", err)
		return
	}
	if req.Remember {
		if _, err := b.Remember(r.Context(), p, entry, src, board.Operator); err != nil {
			s.boardError(w, "keeping the comment for the project (the comment is saved)", err)
			return
		}
	}
	s.changed(w, b, http.StatusCreated, c)
}

// Subtask is a subtask of a ticket with its runs and its steps (7.11, 20.2).
type Subtask struct {
	ID      string     `json:"id"` // SF-8.2
	Kind    string     `json:"kind"`
	Role    string     `json:"role"`
	Title   string     `json:"title"`
	State   string     `json:"state"`
	Commit  *string    `json:"commit"`
	Created time.Time  `json:"createdAt"`
	Steps   []Step     `json:"steps"`
	Runs    []TaskRun  `json:"runs"`
	Ended   *time.Time `json:"endedAt"`
	// Who created it (7.11): the pipeline, the backend, or the role and run of an agent, with the
	// subtask that run worked on; a part's branch, files, and join; a helper's parent.
	CreatedBy string     `json:"createdBy"`
	CreatedIn *string    `json:"createdIn,omitempty"`
	Branch    *string    `json:"branch,omitempty"`
	Touches   []string   `json:"touches,omitempty"`
	Joined    *time.Time `json:"joinedAt,omitempty"`
	Parent    *string    `json:"parent,omitempty"`
	Added     bool       `json:"added"`
}

// Step is one step of a subtask's checklist.
type Step struct {
	Text string `json:"text"`
	Done bool   `json:"done"`
}

// TaskRun is one run of a subtask: its CLI, its model and effort with where they came from, and how
// it went.
type TaskRun struct {
	ID         string     `json:"id"`
	State      string     `json:"state"`
	CLI        string     `json:"cli"`
	Model      *string    `json:"model"`
	Effort     *string    `json:"effort"`
	ModelFrom  *string    `json:"modelFrom"`
	Way        string     `json:"way"`
	StopReason *string    `json:"stopReason"`
	Tokens     int64      `json:"tokens"`
	Started    time.Time  `json:"startedAt"`
	Ended      *time.Time `json:"endedAt"`
}

func subtasksOf(ctx context.Context, pool *pgxpool.Pool, t *board.Ticket) ([]Subtask, error) {
	rows, err := pool.Query(ctx, `SELECT s.id, s.number, s.kind, s.role, s.title, s.state, s.commit, s.created_at, s.ended_at, s.created_by,
			(SELECT c.number FROM runs r JOIN subtasks c ON c.id = r.subtask WHERE r.id = s.created_run), s.branch, s.touches, s.joined_at,
			(SELECT x.number FROM subtasks x WHERE x.id = s.parent), s.added
		FROM subtasks s WHERE s.ticket = $1 ORDER BY s.number`, t.ID)
	if err != nil {
		return nil, err
	}
	type row struct {
		id int64
		s  Subtask
	}
	list, err := pgx.CollectRows(rows, func(r pgx.CollectableRow) (row, error) {
		var x row
		var n int
		var in, parent *int
		err := r.Scan(&x.id, &n, &x.s.Kind, &x.s.Role, &x.s.Title, &x.s.State, &x.s.Commit, &x.s.Created, &x.s.Ended, &x.s.CreatedBy,
			&in, &x.s.Branch, &x.s.Touches, &x.s.Joined, &parent, &x.s.Added)
		x.s.ID = t.Key + "." + strconv.Itoa(n)
		if in != nil {
			k := t.Key + "." + strconv.Itoa(*in)
			x.s.CreatedIn = &k
		}
		if parent != nil {
			k := t.Key + "." + strconv.Itoa(*parent)
			x.s.Parent = &k
		}
		return x, err
	})
	if err != nil {
		return nil, err
	}
	out := make([]Subtask, 0, len(list))
	for _, x := range list {
		s := x.s
		rows, err := pool.Query(ctx, `SELECT text, done_at IS NOT NULL FROM subtask_steps WHERE subtask = $1 ORDER BY position`, x.id)
		if err != nil {
			return nil, err
		}
		if s.Steps, err = pgx.CollectRows(rows, func(r pgx.CollectableRow) (Step, error) {
			var st Step
			return st, r.Scan(&st.Text, &st.Done)
		}); err != nil {
			return nil, err
		}
		rows, err = pool.Query(ctx, `SELECT id::text, state, cli, model, effort, model_from, way, stop_reason, tokens_in + tokens_out, started_at, ended_at
			FROM runs WHERE subtask = $1 ORDER BY number`, x.id)
		if err != nil {
			return nil, err
		}
		if s.Runs, err = pgx.CollectRows(rows, func(r pgx.CollectableRow) (TaskRun, error) {
			var tr TaskRun
			return tr, r.Scan(&tr.ID, &tr.State, &tr.CLI, &tr.Model, &tr.Effort, &tr.ModelFrom, &tr.Way, &tr.StopReason, &tr.Tokens, &tr.Started, &tr.Ended)
		}); err != nil {
			return nil, err
		}
		if s.Steps == nil {
			s.Steps = []Step{}
		}
		if s.Runs == nil {
			s.Runs = []TaskRun{}
		}
		out = append(out, s)
	}
	return out, nil
}

func (s *Server) taskSubtasks(w http.ResponseWriter, r *http.Request) {
	b, p := s.project(w, r)
	if p == nil {
		return
	}
	t, err := b.Ticket(r.Context(), p, r.PathValue("t"))
	if err != nil {
		s.boardError(w, "reading the ticket", err)
		return
	}
	list, err := subtasksOf(r.Context(), b.Pool, t)
	if err != nil {
		s.boardError(w, "reading the subtasks", err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"subtasks": list})
}

// Summary is a finished ticket's summary (8.4): the facts from the records, and the sections a model
// wrote, which are missing until they are written.
type Summary struct {
	Ticket    string         `json:"ticket"`
	Title     string         `json:"title"`
	Outcome   string         `json:"outcome"`
	Facts     map[string]any `json:"facts"`
	Done      *string        `json:"whatWasDone"`
	How       *string        `json:"howItWent"`
	Next      *string        `json:"whatToKnowNextTime"`
	Missing   bool           `json:"missing"`
	WrittenAt *time.Time     `json:"writtenAt"`
}

const summarySelect = `SELECT p.key || '-' || t.number, t.title, s.outcome, s.facts, s.done, s.how, s.next, s.missing, s.written_at
	FROM summaries s JOIN tickets t ON t.id = s.ticket JOIN projects p ON p.id = t.project`

func scanSummary(r pgx.Row) (Summary, error) {
	var x Summary
	err := r.Scan(&x.Ticket, &x.Title, &x.Outcome, &x.Facts, &x.Done, &x.How, &x.Next, &x.Missing, &x.WrittenAt)
	return x, err
}

func (s *Server) taskSummary(w http.ResponseWriter, r *http.Request) {
	b, p := s.project(w, r)
	if p == nil {
		return
	}
	t, err := b.Ticket(r.Context(), p, r.PathValue("t"))
	if err != nil {
		s.boardError(w, "reading the ticket", err)
		return
	}
	x, err := scanSummary(b.Pool.QueryRow(r.Context(), summarySelect+` WHERE s.ticket = $1`, t.ID))
	if errors.Is(err, pgx.ErrNoRows) {
		writeError(w, http.StatusNotFound, "NOT_FOUND", t.Key+" has no summary yet")
		return
	}
	if err != nil {
		s.boardError(w, "reading the summary", err)
		return
	}
	writeJSON(w, http.StatusOK, x)
}

// projectSummaries lists a project's summaries, newest first; q searches their written sections.
func (s *Server) projectSummaries(w http.ResponseWriter, r *http.Request) {
	b, p := s.project(w, r)
	if p == nil {
		return
	}
	q := r.URL.Query().Get("q")
	if len(q) > 200 {
		writeError(w, http.StatusBadRequest, "INVALID_ARGUMENT", "q: at most 200 characters")
		return
	}
	rows, err := b.Pool.Query(r.Context(), summarySelect+` WHERE s.project = $1
		AND ($2 = '' OR s.search @@ websearch_to_tsquery('simple', $2) OR t.title ILIKE '%' || $2 || '%') ORDER BY s.created_at DESC LIMIT 500`, p.ID, q)
	if err != nil {
		s.boardError(w, "reading the summaries", err)
		return
	}
	list, err := pgx.CollectRows(rows, func(r pgx.CollectableRow) (Summary, error) { return scanSummary(r) })
	if err != nil {
		s.boardError(w, "reading the summaries", err)
		return
	}
	if list == nil {
		list = []Summary{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"summaries": list})
}

// Decision is a choice an agent recorded with decide (7.5).
type Decision struct {
	ID           int64     `json:"id"`
	Ticket       *string   `json:"ticket"`
	Role         string    `json:"role"`
	Run          *string   `json:"run"`
	Question     string    `json:"question"`
	Alternatives []string  `json:"alternatives"`
	Choice       string    `json:"choice"`
	Reason       string    `json:"reason"`
	At           time.Time `json:"createdAt"`
	// ChangedTo is what the operator changed it to, and ChangeTicket the ticket that carries it out.
	ChangedTo    *string    `json:"changedTo"`
	ChangeTicket *string    `json:"changeTicket"`
	ChangedAt    *time.Time `json:"changedAt"`
}

func (s *Server) projectDecisions(w http.ResponseWriter, r *http.Request) {
	b, p := s.project(w, r)
	if p == nil {
		return
	}
	rows, err := b.Pool.Query(r.Context(), `SELECT d.id, CASE WHEN d.ticket IS NULL THEN NULL ELSE p.key || '-' || t.number END, d.role,
			d.run::text, d.question, d.alternatives, d.choice, d.reason, d.created_at, d.changed_to,
			CASE WHEN c.id IS NULL THEN NULL ELSE p.key || '-' || c.number END, d.changed_at
		FROM decisions d JOIN projects p ON p.id = d.project LEFT JOIN tickets t ON t.id = d.ticket
		LEFT JOIN tickets c ON c.id = d.change_ticket
		WHERE d.project = $1 ORDER BY d.id DESC LIMIT 1000`, p.ID)
	if err != nil {
		s.boardError(w, "reading the decisions", err)
		return
	}
	list, err := pgx.CollectRows(rows, func(r pgx.CollectableRow) (Decision, error) {
		var d Decision
		return d, r.Scan(&d.ID, &d.Ticket, &d.Role, &d.Run, &d.Question, &d.Alternatives, &d.Choice, &d.Reason, &d.At,
			&d.ChangedTo, &d.ChangeTicket, &d.ChangedAt)
	})
	if err != nil {
		s.boardError(w, "reading the decisions", err)
		return
	}
	if list == nil {
		list = []Decision{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"decisions": list})
}
