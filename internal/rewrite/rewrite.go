// Package rewrite is Crew's prompt writer (design 7.2): before the first message of a plan draft
// reaches the planning agent, Crew proposes a better wording of it, and on a selection the operator
// chooses Improve text or Review text. Each is one call of a CLI with no tools in a judge sandbox,
// with its own model and effort, under a limit of 45 seconds; what it answers is read in a fixed
// form. The operator always chooses, and their own text is never lost: it is sent as it is when the
// call is late, fails, or is off. Every rewrite is kept with the choice, and the choice is a signal.
package rewrite

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"strings"
	"sync/atomic"
	"time"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/kesokaj/crew/baseline"
	"github.com/kesokaj/crew/internal/board"
	"github.com/kesokaj/crew/internal/fence"
	"github.com/kesokaj/crew/internal/models"
	"github.com/kesokaj/crew/internal/planning"
	"github.com/kesokaj/crew/internal/runs"
	"github.com/kesokaj/crew/internal/settings"
)

// The kinds of rewrite.
const (
	KindFirst   = "first"   // the first message of a plan draft, by itself
	KindImprove = "improve" // Improve text, on the operator's own writing
	KindReview  = "review"  // Review text: notes, nothing changed
)

// The states of a rewrite.
const (
	StateWriting = "writing"
	StateReady   = "ready"
	StateLate    = "late"
	StateFailed  = "failed"
	StateOff     = "off"
)

// Limits of what is rewritten and answered.
const (
	MaxText      = 20000
	MaxRounds    = 3 // the first, and Rewrite again twice
	maxQuestions = 3
	maxNotes     = 5
)

// Runner is the part of the run driver the prompt writer uses.
type Runner interface {
	StartRole(ctx context.Context, rr runs.RoleRun) (string, int64, error)
	Finish(ctx context.Context, run string) error
	Stop(ctx context.Context, run string) error
}

// Service writes rewrites.
type Service struct {
	Pool *pgxpool.Pool
	Runs Runner
	// Machine is the machine's settings as they apply now.
	Machine func(ctx context.Context) (settings.Doc, error)
	// Project is a project's settings as they apply now.
	Project func(ctx context.Context, id string) (settings.Doc, error)
	// Drafts takes the text the operator chose as a draft's first message (7.2).
	Drafts  *planning.Drafts
	Version string
	Log     *slog.Logger
	// Poll is how often a writing rewrite's run is looked at; 0 is half a second.
	Poll time.Duration

	writing atomic.Int32 // rewrites being written now
}

// Answer is the operator's answer to one of the prompt writer's questions.
type Answer struct {
	Question string `json:"question"`
	Answer   string `json:"answer"`
}

// Request asks for a rewrite.
type Request struct {
	Kind    string   `json:"kind"`
	Text    string   `json:"text"`
	Draft   string   `json:"draft"`
	Project string   `json:"project"`
	Answers []Answer `json:"answers"`
	// Previous is the rewrite this one writes again, with the answers to its questions.
	Previous int64 `json:"previous"`
	// Rules, by their path in the baseline, replace the prompt writer's rules for an example of a change
	// to them (7.2); never set from a request.
	Rules map[string]string `json:"-"`
}

// Rewrite is a rewrite as the operator reads it.
type Rewrite struct {
	ID          int64      `json:"id"`
	Kind        string     `json:"kind"`
	Draft       *string    `json:"draft"`
	Project     *string    `json:"project"`
	Run         *string    `json:"run"`
	Round       int        `json:"round"`
	Original    string     `json:"original"`
	State       string     `json:"state"`
	Rewrite     *string    `json:"rewrite"`
	Changes     []string   `json:"changes"`
	Assumptions []string   `json:"assumptions"`
	Questions   []string   `json:"questions"`
	Notes       []string   `json:"notes"`
	Detail      *string    `json:"detail"`
	Choice      *string    `json:"choice"`
	Sent        *string    `json:"sent"`
	CLI         *string    `json:"cli"`
	Model       *string    `json:"model"`
	Effort      *string    `json:"effort"`
	CreatedAt   time.Time  `json:"createdAt"`
	ChosenAt    *time.Time `json:"chosenAt"`
}

// Error is a refusal, with a reason the operator can read.
type Error struct {
	NotFound bool
	Msg      string
}

func (e *Error) Error() string { return e.Msg }

func refuse(format string, a ...any) error { return &Error{Msg: fmt.Sprintf(format, a...)} }

// Check refuses a request whose text or answers are not what a rewrite takes, before anything is
// made for it, such as the draft of a first message.
func Check(req Request) error {
	if req.Kind != KindFirst && req.Kind != KindImprove && req.Kind != KindReview {
		return refuse("kind: first, improve, or review")
	}
	if req.Previous == 0 {
		if t := strings.TrimSpace(req.Text); t == "" || utf8.RuneCountInString(t) > MaxText {
			return refuse("text: 1 to %d characters", MaxText)
		}
	}
	if len(req.Answers) > maxQuestions {
		return refuse("answers: at most %d", maxQuestions)
	}
	for _, a := range req.Answers {
		if utf8.RuneCountInString(a.Question)+utf8.RuneCountInString(a.Answer) > 4000 {
			return refuse("answers: at most 4000 characters each")
		}
	}
	return nil
}

// MaxWriting is how many rewrites are written at once: each is a call on the subscriptions.
const MaxWriting = 3

// Start records a rewrite and has the prompt writer write it, in the background: the operator reads
// its state, and can send their own text from the first second. With the prompt writer off for the
// machine, a first message gets no proposal: its state is off.
func (s *Service) Start(ctx context.Context, req Request) (Rewrite, error) {
	req.Text = strings.TrimSpace(req.Text)
	if err := Check(req); err != nil {
		return Rewrite{}, err
	}
	round := 1
	if req.Previous != 0 {
		prev, err := s.Get(ctx, req.Previous)
		if err != nil {
			return Rewrite{}, err
		}
		if prev.Kind == KindReview || prev.State != StateReady {
			return Rewrite{}, refuse("only a rewrite that is ready is written again")
		}
		if prev.Choice != nil {
			return Rewrite{}, refuse("that rewrite was chosen already")
		}
		if prev.Round >= MaxRounds {
			return Rewrite{}, refuse("Rewrite again works twice: send one of the texts, or edit one")
		}
		round = prev.Round + 1
		req.Kind, req.Text = prev.Kind, prev.Original
		req.Draft, req.Project = deref(prev.Draft), deref(prev.Project)
	}
	if req.Text == "" || utf8.RuneCountInString(req.Text) > MaxText {
		return Rewrite{}, refuse("text: 1 to %d characters", MaxText)
	}
	if (req.Draft == "") == (req.Project == "") {
		return Rewrite{}, refuse("a rewrite belongs to one plan draft or one project")
	}
	if req.Kind == KindFirst && req.Draft == "" {
		return Rewrite{}, refuse("a first message belongs to a plan draft")
	}
	if err := s.checkOwner(ctx, req); err != nil {
		return Rewrite{}, err
	}
	state := StateWriting
	if req.Kind == KindFirst {
		doc, err := s.Machine(ctx)
		if err != nil {
			return Rewrite{}, err
		}
		if v, _ := dig(doc, "planning", "rewrite").(string); v == "off" {
			state = StateOff
		}
	}
	if state == StateWriting {
		if s.writing.Add(1) > MaxWriting {
			s.writing.Add(-1)
			return Rewrite{}, refuse("the prompt writer has %d texts to write already: try again in a moment, or send yours as it is", MaxWriting)
		}
	}
	answers, _ := json.Marshal(nonNil(req.Answers))
	var prev any
	if req.Previous != 0 {
		prev = req.Previous
	}
	var id int64
	err := s.Pool.QueryRow(ctx, `INSERT INTO prompt_rewrites (kind, draft, project, previous, round, original, answers, state)
		VALUES ($1, nullif($2, '')::uuid, nullif($3, '')::uuid, $4, $5, $6, $7, $8) RETURNING id`,
		req.Kind, req.Draft, req.Project, prev, round, req.Text, answers, state).Scan(&id)
	if err != nil {
		if state == StateWriting {
			s.writing.Add(-1)
		}
		return Rewrite{}, err
	}
	switch state {
	case StateWriting:
		go func() {
			defer s.writing.Add(-1)
			s.write(context.WithoutCancel(ctx), id, req)
		}()
	case StateOff:
		s.sendMine(ctx, id)
	}
	return s.Get(ctx, id)
}

// sendMine sends a first message as the operator wrote it, when there is no proposal to choose: it
// was off, late, failed, or good as it is (7.2: "your text is sent as it is, and the draft says
// so"). A choice the operator made meanwhile stands.
func (s *Service) sendMine(ctx context.Context, id int64) {
	r, err := s.Get(ctx, id)
	if err != nil || r.Kind != KindFirst || r.Choice != nil {
		return
	}
	if _, err := s.Choose(ctx, id, "mine", ""); err != nil {
		var e *Error
		if !errors.As(err, &e) {
			s.Log.Error("sending the operator's own first message", "rewrite", id, "error", err)
		}
	}
}

// checkOwner makes sure the draft or the project is there, and a first message is the draft's first.
func (s *Service) checkOwner(ctx context.Context, req Request) error {
	if req.Draft != "" {
		var state string
		var messages int
		err := s.Pool.QueryRow(ctx, `SELECT state, (SELECT count(*) FROM draft_messages WHERE draft = d.id) FROM plan_drafts d WHERE id::text = $1`,
			req.Draft).Scan(&state, &messages)
		if errors.Is(err, pgx.ErrNoRows) {
			return &Error{NotFound: true, Msg: "no such draft"}
		}
		if err != nil {
			return err
		}
		if state != "open" {
			return refuse("the draft is %s", state)
		}
		if req.Kind == KindFirst && messages > 0 {
			return refuse("the draft has its first message: select text for Improve text instead")
		}
		return nil
	}
	var n int
	if err := s.Pool.QueryRow(ctx, `SELECT count(*) FROM projects WHERE id::text = $1 AND state <> 'archived'`, req.Project).Scan(&n); err != nil {
		return err
	}
	if n == 0 {
		return &Error{NotFound: true, Msg: "no such project"}
	}
	return nil
}

func deref(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

func nonNil[T any](v []T) []T {
	if v == nil {
		return []T{}
	}
	return v
}

func dig(d settings.Doc, path ...string) any {
	var v any = d
	for _, p := range path {
		m, ok := v.(settings.Doc)
		if !ok {
			return nil
		}
		v = m[p]
	}
	return v
}

// timeout is the machine's planning.rewrite_timeout_seconds, 45 when it is not set.
func timeout(doc settings.Doc) time.Duration {
	switch v := dig(doc, "planning", "rewrite_timeout_seconds").(type) {
	case int:
		if v > 0 {
			return time.Duration(v) * time.Second
		}
	case float64:
		if v > 0 {
			return time.Duration(v * float64(time.Second))
		}
	}
	return 45 * time.Second
}

// write has the prompt writer write the rewrite, and records what came of it.
func (s *Service) write(ctx context.Context, id int64, req Request) {
	fail := func(state, detail string) {
		if _, err := s.Pool.Exec(ctx, `UPDATE prompt_rewrites SET state = $2, detail = $3 WHERE id = $1 AND state = 'writing'`, id, state, detail); err != nil {
			s.Log.Error("recording a rewrite", "rewrite", id, "error", err)
		}
		s.sendMine(ctx, id)
	}
	doc, err := s.Machine(ctx)
	if err != nil {
		fail(StateFailed, "the settings could not be read")
		return
	}
	var project settings.Doc
	if req.Project != "" && s.Project != nil {
		if project, err = s.Project(ctx, req.Project); err != nil {
			fail(StateFailed, "the project's settings could not be read")
			return
		}
	}
	limit := timeout(doc)
	deadline := time.Now().Add(limit)
	c, err := models.Choose(ctx, s.Pool, models.Ask{Role: "prompt", Machine: doc, Project: project})
	if err != nil {
		// At a limit or signed out with no next choice: the operator's own text is sent (7.2).
		fail(StateFailed, "no CLI can write it now: "+err.Error())
		return
	}
	parts, err := s.parts(ctx, req)
	if err != nil {
		fail(StateFailed, "its prompt could not be made")
		s.Log.Error("a rewrite's prompt", "rewrite", id, "error", err)
		return
	}
	title := map[string]string{KindFirst: "Rewrite the first message", KindImprove: "Improve text", KindReview: "Review text"}[req.Kind]
	run, _, err := s.Runs.StartRole(ctx, runs.RoleRun{
		Role: "prompt", Kind: "rewrite", Title: title, Project: req.Project, Draft: req.Draft, Judge: true,
		CLI: c.CLI, Model: c.Model, Name: c.Name, Effort: c.Effort, From: c.ModelFrom, EffortFrom: c.EffortFrom, Parts: parts, Timeout: limit,
		Record: func(ctx context.Context, tx pgx.Tx, run string) error {
			_, err := tx.Exec(ctx, `UPDATE prompt_rewrites SET run = $2 WHERE id = $1`, id, run)
			return err
		},
	})
	if err != nil {
		fail(StateFailed, "the prompt writer did not start: "+err.Error())
		return
	}
	answer, state, err := s.await(ctx, id, run, deadline)
	switch {
	case errors.Is(err, errChosen):
		// The operator sent their own text meanwhile: the run is stopped, and nothing is waited for.
		s.Runs.Stop(ctx, run)
		return
	case err != nil:
		fail(StateFailed, err.Error())
		return
	case state == StateLate:
		s.Runs.Stop(ctx, run)
		fail(StateLate, fmt.Sprintf("the prompt writer took longer than %s", limit))
		return
	}
	defer s.Runs.Finish(ctx, run)
	if req.Kind == KindReview {
		notes, ok := readNotes(answer)
		if !ok {
			fail(StateFailed, "the answer was not in its form")
			return
		}
		b, _ := json.Marshal(notes)
		s.Pool.Exec(ctx, `UPDATE prompt_rewrites SET state = 'ready', notes = $2 WHERE id = $1 AND state = 'writing'`, id, b)
		return
	}
	f, ok := readRewrite(answer)
	switch {
	case !ok:
		fail(StateFailed, "the answer was not in its form")
	case f.good:
		s.Pool.Exec(ctx, `UPDATE prompt_rewrites SET state = 'good' WHERE id = $1 AND state = 'writing'`, id)
		s.sendMine(ctx, id)
	default:
		ch, _ := json.Marshal(f.changes)
		as, _ := json.Marshal(f.assumptions)
		qs, _ := json.Marshal(f.questions)
		s.Pool.Exec(ctx, `UPDATE prompt_rewrites SET state = 'ready', rewrite = $2, changes = $3, assumptions = $4, questions = $5
			WHERE id = $1 AND state = 'writing'`, id, f.rewrite, ch, as, qs)
	}
}

// errChosen says the operator chose while the rewrite was written.
var errChosen = errors.New("the operator chose already")

// await waits for the run's one turn: its answer once it has answered, late at the deadline, an
// error when the run stopped without one, or errChosen once the operator chose without it.
func (s *Service) await(ctx context.Context, id int64, run string, deadline time.Time) (string, string, error) {
	poll := s.Poll
	if poll == 0 {
		poll = 500 * time.Millisecond
	}
	for {
		var state string
		var reason *string
		var mine string
		if err := s.Pool.QueryRow(ctx, `SELECT r.state, r.stop_reason, x.state FROM runs r, prompt_rewrites x WHERE r.id = $1 AND x.id = $2`,
			run, id).Scan(&state, &reason, &mine); err != nil {
			return "", "", err
		}
		if mine != StateWriting {
			return "", "", errChosen
		}
		if state == "idle" || (state == "ended" && reason != nil && *reason == "ok") {
			var answer string
			err := s.Pool.QueryRow(ctx, `SELECT text FROM turns WHERE run = $1 AND speaker = 'agent' ORDER BY number DESC LIMIT 1`, run).Scan(&answer)
			if errors.Is(err, pgx.ErrNoRows) {
				return "", "", errors.New("the prompt writer answered nothing")
			}
			return answer, StateReady, err
		}
		if state == "ended" {
			return "", "", fmt.Errorf("the prompt writer stopped: %s", deref(reason))
		}
		if time.Now().After(deadline) {
			return "", StateLate, nil
		}
		time.Sleep(poll)
	}
}

// parts are the prompt writer's prompt: its rules, its stage, the text fenced as data, and in a
// project the project's goal, the titles of its plan's parts, and at most five earlier rewrites the
// operator changed in that project, of no other (10.6).
func (s *Service) parts(ctx context.Context, req Request) ([]runs.PromptPart, error) {
	stage := "stages/rewrite.md"
	if req.Kind == KindReview {
		stage = "stages/review-text.md"
	}
	// The prompt writer's rules as they apply: a project's override of them, which the operator agreed
	// to, replaces them for its texts (10.3, 7.2).
	file := func(path string) (string, string) {
		if t, ok := req.Rules[path]; ok {
			return t, "the change under test, of baseline/" + path + "@" + s.Version
		}
		text, source, err := board.BaselineText(ctx, s.Pool, req.Project, path, s.Version)
		if err != nil {
			s.Log.Error("reading a project's override", "project", req.Project, "path", path, "error", err)
			return baseline.MustText(path), "baseline/" + path + "@" + s.Version + " (its override could not be read)"
		}
		return text, source
	}
	agentText, agentSource := file("agents/prompt-writer.md")
	stageText, stageSource := file(stage)
	var answers strings.Builder
	for _, a := range req.Answers {
		fmt.Fprintf(&answers, "Q: %s\nA: %s\n\n", strings.TrimSpace(a.Question), strings.TrimSpace(a.Answer))
	}
	var context strings.Builder
	if req.Project != "" {
		var goal, plan string
		if err := s.Pool.QueryRow(ctx, `SELECT p.goal, coalesce((SELECT text FROM plans WHERE project = p.id AND kind = 'original'), '')
			FROM projects p WHERE p.id::text = $1`, req.Project).Scan(&goal, &plan); err != nil {
			return nil, err
		}
		fmt.Fprintf(&context, "The project's goal: %s\n", strings.TrimSpace(goal))
		if titles := partTitles(plan); len(titles) > 0 {
			fmt.Fprintf(&context, "The parts of its plan: %s\n", strings.Join(titles, "; "))
		}
		rows, err := s.Pool.Query(ctx, `SELECT original, rewrite, sent FROM prompt_rewrites WHERE project::text = $1 AND choice = 'edited'
			ORDER BY id DESC LIMIT 5`, req.Project)
		if err != nil {
			return nil, err
		}
		for rows.Next() {
			var o, r, sent string
			var rp, sp *string
			if err := rows.Scan(&o, &rp, &sp); err != nil {
				rows.Close()
				return nil, err
			}
			r, sent = deref(rp), deref(sp)
			fmt.Fprintf(&context, "\nAn earlier text of the operator:\n%s\nYour rewrite of it:\n%s\nWhat the operator made of your rewrite and sent:\n%s\n", o, r, sent)
		}
		rows.Close()
	}
	f := fence.New(req.Text, answers.String(), context.String())
	out := []runs.PromptPart{
		{Part: "agent", Source: agentSource, Text: baseline.Body(agentText)},
		{Part: "stage", Source: stageSource, Text: stageText},
	}
	if context.Len() > 0 {
		out = append(out, runs.PromptPart{Part: "context", Source: "project " + req.Project, Text: f.Data("The project", "Crew and the operator", context.String())})
	}
	out = append(out, runs.PromptPart{Part: "text", Source: "the operator", Text: f.Data("The operator's text", "the operator", req.Text)})
	if answers.Len() > 0 {
		out = append(out, runs.PromptPart{Part: "answers", Source: "the operator", Text: f.Data("The operator's answers to your questions", "the operator", answers.String())})
	}
	return out, nil
}

// partTitles are the headings under "## Parts" of a plan.
func partTitles(plan string) []string {
	var out []string
	in := false
	for _, l := range strings.Split(plan, "\n") {
		switch {
		case strings.HasPrefix(l, "## "):
			in = strings.TrimSpace(strings.TrimPrefix(l, "## ")) == "Parts"
		case in && strings.HasPrefix(l, "### "):
			out = append(out, strings.TrimSpace(strings.TrimPrefix(l, "### ")))
		}
	}
	return out
}

// Get reads one rewrite.
func (s *Service) Get(ctx context.Context, id int64) (Rewrite, error) {
	var r Rewrite
	var changes, assumptions, questions, notes []byte
	err := s.Pool.QueryRow(ctx, `SELECT x.id, x.kind, x.draft::text, x.project::text, x.run::text, x.round, x.original, x.state, x.rewrite,
			x.changes, x.assumptions, x.questions, x.notes, x.detail, x.choice, x.sent, r.cli, r.model, r.effort, x.created_at, x.chosen_at
		FROM prompt_rewrites x LEFT JOIN runs r ON r.id = x.run WHERE x.id = $1`, id).Scan(&r.ID, &r.Kind, &r.Draft, &r.Project, &r.Run,
		&r.Round, &r.Original, &r.State, &r.Rewrite, &changes, &assumptions, &questions, &notes, &r.Detail, &r.Choice, &r.Sent,
		&r.CLI, &r.Model, &r.Effort, &r.CreatedAt, &r.ChosenAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return r, &Error{NotFound: true, Msg: "no such rewrite"}
	}
	if err != nil {
		return r, err
	}
	for _, x := range []struct {
		raw []byte
		to  *[]string
	}{{changes, &r.Changes}, {assumptions, &r.Assumptions}, {questions, &r.Questions}, {notes, &r.Notes}} {
		json.Unmarshal(x.raw, x.to)
		*x.to = nonNil(*x.to)
	}
	return r, nil
}

// The choices, and which kind takes each.
var choices = map[string][]string{
	KindFirst:   {"rewrite", "edited", "mine"},
	KindImprove: {"use", "edited", "keep"},
	KindReview:  {"seen"},
}

// Choose records the operator's choice and the text it gives (7.2). A first message is then sent to
// the planning agent: the text chosen, and nothing about the other. Improve text sends nothing: the
// page puts the text in the place of the selection. The choice is a signal (10.2). Send mine works at
// any time, also while the rewrite is written; that rewrite is then no longer waited for.
func (s *Service) Choose(ctx context.Context, id int64, choice, text string) (Rewrite, error) {
	r, err := s.Get(ctx, id)
	if err != nil {
		return r, err
	}
	allowed := choices[r.Kind]
	if !slices.Contains(allowed, choice) {
		return r, refuse("choice: %s", strings.Join(allowed, ", "))
	}
	if r.Choice != nil {
		return r, refuse("this rewrite was chosen already")
	}
	mine := choice == "mine" || choice == "keep"
	if !mine && choice != "seen" && r.State != StateReady {
		return r, refuse("there is no rewrite to %s: send your own text", choice)
	}
	switch choice {
	case "rewrite", "use":
		text = deref(r.Rewrite)
	case "mine", "keep":
		text = r.Original
	case "seen":
		text = ""
	}
	text = strings.TrimSpace(text)
	if choice != "seen" && (text == "" || utf8.RuneCountInString(text) > MaxText) {
		return r, refuse("text: 1 to %d characters", MaxText)
	}
	detail, _ := json.Marshal(map[string]any{"rewrite": id, "kind": r.Kind, "choice": choice, "state": r.State, "round": r.Round})
	err = pgx.BeginFunc(ctx, s.Pool, func(tx pgx.Tx) error {
		if r.Kind == KindFirst {
			// One first message a draft: a proposal of an earlier round, or a page that is out of
			// date, cannot add another once one was sent.
			var sent int
			if err := tx.QueryRow(ctx, `SELECT (SELECT count(*) FROM draft_messages WHERE draft = d.id) FROM plan_drafts d
				WHERE d.id::text = $1 FOR UPDATE`, deref(r.Draft)).Scan(&sent); err != nil {
				return err
			}
			if sent > 0 {
				return refuse("the draft has its first message already")
			}
		}
		tag, err := tx.Exec(ctx, `UPDATE prompt_rewrites SET choice = $2, sent = nullif($3, ''), chosen_at = now(),
			state = CASE WHEN state = 'writing' THEN 'late' ELSE state END,
			detail = CASE WHEN state = 'writing' THEN 'you sent your own text before the rewrite was written' ELSE detail END
			WHERE id = $1 AND choice IS NULL`, id, choice, text)
		if err != nil {
			return err
		}
		if tag.RowsAffected() == 0 {
			return refuse("this rewrite was chosen already")
		}
		// The choice is a signal (10.2): which text was sent, of which rewrite, on which model.
		if _, err := tx.Exec(ctx, `INSERT INTO signals (project, run, kind, role, cli, model, effort, crew_version, detail)
			SELECT x.project, x.run, 'rewrite_choice', 'prompt', r.cli, r.model, r.effort, $2, $3
			FROM prompt_rewrites x LEFT JOIN runs r ON r.id = x.run WHERE x.id = $1`, id, s.Version, detail); err != nil {
			return err
		}
		if r.Kind == KindFirst {
			return s.Drafts.AddMessage(ctx, tx, deref(r.Draft), planning.Said{Text: text, Rewrite: id})
		}
		return nil
	})
	if err != nil {
		return r, err
	}
	if r.Kind == KindFirst {
		s.Drafts.Kicked()
	}
	if r.State == StateWriting && r.Run != nil {
		s.Runs.Stop(context.WithoutCancel(ctx), *r.Run)
	}
	return s.Get(ctx, id)
}
