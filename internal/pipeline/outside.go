package pipeline

import (
	"context"
	"encoding/json"
	"errors"
	"maps"
	"slices"
	"strings"

	"github.com/jackc/pgx/v5"

	"github.com/kesokaj/crew/internal/board"
	"github.com/kesokaj/crew/internal/inbox"
	"github.com/kesokaj/crew/internal/mcp"
)

// The Crew MCP for the operator's own tools (11.7): the read tools of the runs with a project named,
// list_projects, get_inbox, create_ticket, and comment; with "may answer", answer_new_feature and
// change_decision. A personal token reaches the projects it names and nothing else, and never a tool
// that gives a project its OK, installs, deploys, changes settings, signs in, or makes a token.

// outsideReads are the runs' read tools a personal token calls with a project named.
var outsideReads = []string{"get_project", "get_plan", "list_tickets", "search_tickets", "get_ticket", "get_summary", "search_summaries",
	"search_memory", "get_memory", "list_bugs", "list_feedback", "list_decisions", "search_docs", "get_doc", "list_skills"}

// Inbox lists the inbox, for get_inbox.
type Inbox func(ctx context.Context) ([]inbox.Item, error)

// RegisterOutside adds the operator's tools to outside, wrapping the read tools inside offers.
func (t *Tools) RegisterOutside(outside, inside *mcp.Server, items Inbox) error {
	if t.Inbox == nil {
		t.Inbox = items
	}
	str := func(desc string) map[string]any { return map[string]any{"type": "string", "description": desc} }
	project := str("The project's name, one this token may use.")
	for _, name := range outsideReads {
		tl := inside.Tool(name)
		if tl == nil {
			return errors.New("the Crew MCP for runs has no tool " + name)
		}
		in := maps.Clone(tl.Input)
		props := maps.Clone(in["properties"].(map[string]any))
		props["project"] = project
		in["properties"] = props
		req, _ := in["required"].([]string)
		in["required"] = append(slices.Clone(req), "project")
		if err := outside.Add(mcp.Tool{Name: name, Scope: mcp.ScopeOperator, Input: in, Handle: t.withProject(tl.Handle)}); err != nil {
			return err
		}
	}
	check := object(map[string]any{"kind": map[string]any{"type": "string", "enum": []string{"cmd", "judge"}},
		"spec": str("For cmd, a command; for judge, a criterion a judge model reads the diff against.")}, "kind", "spec")
	for _, tl := range []mcp.Tool{
		{Name: "get_crew_map", Input: object(nil), Handle: func(ctx context.Context, c mcp.Caller, args json.RawMessage) (any, error) {
			return t.getCrewMap(ctx, c, args, items)
		}},
		{Name: "list_projects", Input: object(nil), Handle: t.listProjects},
		{Name: "get_inbox", Input: object(nil), Handle: func(ctx context.Context, c mcp.Caller, args json.RawMessage) (any, error) {
			return t.getInbox(ctx, c, args, items)
		}},
		{Name: "create_ticket", Input: object(map[string]any{
			"project": project, "title": str("One line that names the work."), "why": str("Why it is needed."),
			"what": str("What is in it."), "goal": str("What can be seen when it is done."), "seen": str("What you saw, if anything."),
			"kind":   map[string]any{"type": "string", "enum": []string{"feature", "fix", "chore", "docs"}},
			"checks": map[string]any{"type": "array", "items": check},
		}, "project", "title", "why"), Handle: t.outsideTicket},
		{Name: "comment", Input: object(map[string]any{
			"project": project, "ticket": str("The ticket, such as SF-12; none for a comment on the plan."), "text": str("The comment."),
		}, "project", "text"), Handle: t.outsideComment},
		{Name: "answer_new_feature", Input: object(map[string]any{
			"project": project, "ticket": str("The new feature, such as SF-12."),
			"choice": map[string]any{"type": "string", "enum": []string{"approve_now", "discard"}}, "reason": str("Why, for discard."),
		}, "project", "ticket", "choice"), Handle: t.answerNewFeature, Answers: true},
		{Name: "change_decision", Input: object(map[string]any{
			"project": project, "decision": map[string]any{"type": "integer", "description": "The decision's id."}, "choice": str("What it is changed to."),
		}, "project", "decision", "choice"), Handle: t.changeDecision, Answers: true},
	} {
		tl.Scope = mcp.ScopeOperator
		if err := outside.Add(tl); err != nil {
			return err
		}
	}
	return nil
}

// project reads the project a call names, one the token may use.
func (t *Tools) project(ctx context.Context, c mcp.Caller, name string) (*board.Project, error) {
	p, err := t.Board.Project(ctx, strings.TrimSpace(name))
	var be *board.Error
	if errors.As(err, &be) || (err == nil && !slices.Contains(c.Projects, p.ID)) {
		return nil, mcp.Errf("this token may not use a project %q", name)
	}
	return p, err
}

// withProject calls a read tool of the runs for the project the call names, as if a run of that
// project called it, with the project taken out of the arguments.
func (t *Tools) withProject(h mcp.Handler) mcp.Handler {
	return func(ctx context.Context, c mcp.Caller, args json.RawMessage) (any, error) {
		var all map[string]json.RawMessage
		if err := json.Unmarshal(args, &all); err != nil {
			return nil, mcp.Errf("the arguments do not read: %v", err)
		}
		var name string
		if err := json.Unmarshal(all["project"], &name); err != nil || name == "" {
			return nil, mcp.Errf("project: name a project")
		}
		p, err := t.project(ctx, c, name)
		if err != nil {
			return nil, err
		}
		delete(all, "project")
		rest, _ := json.Marshal(all)
		as := mcp.Caller{Role: "token", Project: p.ID, Token: c.Token, TokenName: c.TokenName}
		return h(ctx, as, rest)
	}
}

// byToken names a personal token in the history (11.7): what it did is told apart from what the
// operator did in the app.
func byToken(c mcp.Caller) string { return "token " + c.TokenName }

func (t *Tools) listProjects(ctx context.Context, c mcp.Caller, args json.RawMessage) (any, error) {
	var a struct{}
	if err := mcp.Decode(args, &a); err != nil {
		return nil, err
	}
	rows, err := t.Pool.Query(ctx, `SELECT name, key, state, coalesce(goal, '') FROM projects WHERE id = ANY($1::uuid[]) AND state <> 'archived'
		ORDER BY name`, c.Projects)
	if err != nil {
		return nil, err
	}
	type line struct {
		Name  string `json:"name"`
		Key   string `json:"key"`
		State string `json:"state"`
		Goal  string `json:"goal,omitempty"`
	}
	list, err := pgx.CollectRows(rows, func(r pgx.CollectableRow) (line, error) {
		var l line
		return l, r.Scan(&l.Name, &l.Key, &l.State, &l.Goal)
	})
	return map[string]any{"projects": list}, err
}

// getInbox is the inbox, of the token's projects only.
func (t *Tools) getInbox(ctx context.Context, c mcp.Caller, args json.RawMessage, items Inbox) (any, error) {
	var a struct{}
	if err := mcp.Decode(args, &a); err != nil {
		return nil, err
	}
	all, err := items(ctx)
	if err != nil {
		return nil, err
	}
	names := map[string]bool{}
	rows, err := t.Pool.Query(ctx, `SELECT name FROM projects WHERE id = ANY($1::uuid[])`, c.Projects)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var n string
		if rows.Scan(&n) == nil {
			names[n] = true
		}
	}
	rows.Close()
	type line struct {
		ID      string   `json:"id"`
		Kind    string   `json:"kind"`
		Project string   `json:"project"`
		Title   string   `json:"title"`
		Body    string   `json:"body"`
		Can     []string `json:"alternatives"`
	}
	out := []line{}
	for _, it := range all {
		if !names[it.Project] {
			continue
		}
		l := line{ID: it.ID, Kind: it.Kind, Project: it.Project, Title: it.Title, Body: it.Body}
		for _, alt := range it.Alternatives {
			l.Can = append(l.Can, alt.Label)
		}
		out = append(out, l)
	}
	return map[string]any{"items": out, "note": "What needs the operator. A token answers only a new feature and a recorded decision, and only with may answer; the rest is answered in the app."}, nil
}

// The tickets a personal token may create: waiting for analysis in one project, and in a day.
const (
	maxTokenWaiting = 10
	maxTokenDaily   = 50
)

// outsideTicket creates a ticket through the MCP (11.7): analysed like a ticket an agent created, and
// marked as created through it. With "may answer" it is the operator's own, approved by adding it.
func (t *Tools) outsideTicket(ctx context.Context, c mcp.Caller, args json.RawMessage) (any, error) {
	var a struct {
		Project, Title, Why, What, Goal, Seen, Kind string
		Checks                                      []board.CheckSpec
	}
	if err := mcp.Decode(args, &a); err != nil {
		return nil, err
	}
	p, err := t.project(ctx, c, a.Project)
	if err != nil {
		return nil, err
	}
	if strings.TrimSpace(a.Why) == "" {
		return nil, mcp.Errf("why: say why the ticket is needed")
	}
	// Each ticket starts a lead's analysis on the operator's subscription: a token adds at most a few
	// that wait for it in a project, and a day's worth in all (security review of M9).
	t.caps.Lock()
	defer t.caps.Unlock()
	var waiting, today int
	if err := t.Pool.QueryRow(ctx, `SELECT count(*) FILTER (WHERE project = $2 AND stage IN ('backlog', 'analysis')
			AND analysis->>'status' IN ('proposed', 'waiting', 'analysing')), count(*) FILTER (WHERE created_at > now() - interval '1 day')
		FROM tickets WHERE created_by = $1`, byToken(c), p.ID).Scan(&waiting, &today); err != nil {
		return nil, err
	}
	switch {
	case waiting >= maxTokenWaiting:
		return nil, mcp.Errf("%d tickets of this token wait for analysis in %s; create more once they are analysed", waiting, p.Name)
	case today >= maxTokenDaily:
		return nil, mcp.Errf("this token created %d tickets in a day; create more tomorrow, or in the app", today)
	}
	var seen *string
	if strings.TrimSpace(a.Seen) != "" {
		seen = &a.Seen
	}
	// With may answer it is the operator's own, approved by adding it; it stays a token's ticket in all
	// else (13.8): its may_change waits for the operator, and it cannot skip Review.
	tk, err := t.Board.AddTicket(ctx, p.ID, board.NewTicket{Title: a.Title, Kind: a.Kind, Why: a.Why, What: a.What, Goal: a.Goal,
		Checks: a.Checks, Seen: seen, Approved: c.MayAnswer}, board.OriginToken, byToken(c))
	if err != nil {
		return nil, boardError(err)
	}
	t.kick()
	return map[string]any{"ticket": tk.Key, "stage": tk.Stage, "origin": tk.Origin}, nil
}

// outsideComment is a comment on a ticket or on the plan, in the operator's name, recorded with the
// token's (11.6, 11.7).
func (t *Tools) outsideComment(ctx context.Context, c mcp.Caller, args json.RawMessage) (any, error) {
	var a struct{ Project, Ticket, Text string }
	if err := mcp.Decode(args, &a); err != nil {
		return nil, err
	}
	p, err := t.project(ctx, c, a.Project)
	if err != nil {
		return nil, err
	}
	r, err := t.Board.CommentBy(ctx, p, strings.TrimSpace(a.Ticket), a.Text, board.TokenAuthor(c.TokenName), byToken(c))
	if err != nil {
		return nil, boardError(err)
	}
	return map[string]any{"comment": r.ID}, nil
}

// answerNewFeature is Approve now or Discard on a new feature during its notice: only with "may
// answer", and recorded with the token's name (11.7).
func (t *Tools) answerNewFeature(ctx context.Context, c mcp.Caller, args json.RawMessage) (any, error) {
	var a struct{ Project, Ticket, Choice, Reason string }
	if err := mcp.Decode(args, &a); err != nil {
		return nil, err
	}
	if !c.MayAnswer {
		return nil, mcp.Errf("this token may not answer: switch on may answer for it in Crew")
	}
	p, err := t.project(ctx, c, a.Project)
	if err != nil {
		return nil, err
	}
	if a.Choice != "approve_now" && a.Choice != "discard" {
		return nil, mcp.Errf("choice: approve_now or discard")
	}
	// A ticket that waits for the operator's own decision (approvals.created_task) is not a new
	// feature: a token does not answer it (11.7; security review of M13).
	if cur, err := t.Board.Ticket(ctx, p, a.Ticket); err != nil {
		return nil, boardError(err)
	} else if cur.Analysis.Status == board.StatusOperator {
		return nil, mcp.Errf("%s waits for the operator's own decision in Crew, not for an answer to a new feature", cur.Key)
	}
	tk, err := t.Board.NoticeDecision(ctx, p, a.Ticket, a.Choice, a.Reason, byToken(c))
	if err != nil {
		return nil, boardError(err)
	}
	t.kick()
	return map[string]any{"ticket": tk.Key, "stage": tk.Stage, "analysis": tk.Analysis.Status}, nil
}

// changeDecision changes a decision an agent recorded: only with "may answer" (11.7).
func (t *Tools) changeDecision(ctx context.Context, c mcp.Caller, args json.RawMessage) (any, error) {
	var a struct {
		Project  string
		Decision int64
		Choice   string
	}
	if err := mcp.Decode(args, &a); err != nil {
		return nil, err
	}
	if !c.MayAnswer {
		return nil, mcp.Errf("this token may not answer: switch on may answer for it in Crew")
	}
	p, err := t.project(ctx, c, a.Project)
	if err != nil {
		return nil, err
	}
	tk, err := t.Board.ChangeDecision(ctx, p, a.Decision, a.Choice, byToken(c))
	if err != nil {
		return nil, boardError(err)
	}
	t.kick()
	return map[string]any{"ticket": tk.Key, "next": tk.Key + " carries the change out."}, nil
}
