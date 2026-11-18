package planning

import (
	"context"
	"encoding/json"
	"errors"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/kesokaj/crew/internal/board"
	"github.com/kesokaj/crew/internal/mcp"
)

// Tools are the Crew MCP's tools of M5 (11.5): the planning agent's on its draft, and the lead's
// on its project for the breakdown. Each reads what it works on from the caller's token.
type Tools struct {
	Pool  *pgxpool.Pool
	Board *board.Board
	// Kick tells the orchestrator that something changed, such as a breakdown that was written.
	Kick func()
}

// Register adds the tools to a server.
func (t *Tools) Register(s *mcp.Server) error {
	for _, tl := range []mcp.Tool{
		{Name: "get_plan_draft", Scope: mcp.ScopeDraft, Input: object(nil), Handle: t.getPlanDraft},
		{Name: "write_plan_draft", Scope: mcp.ScopeDraft, Input: object(map[string]any{
			"plan":  strSchema("The whole plan, as markdown in the form of the plan template, with its front matter."),
			"title": strSchema("The plan's title, when it has one."),
		}, "plan"), Handle: t.writePlanDraft},
		t.convRead(mcp.Tool{Name: "get_project", Scope: mcp.ScopeProject, Input: object(nil), Handle: t.getProject}),
		t.convRead(mcp.Tool{Name: "get_plan", Scope: mcp.ScopeProject, Input: object(nil), Handle: t.getPlan}),
		t.convRead(mcp.Tool{Name: "list_tickets", Scope: mcp.ScopeProject, Input: object(map[string]any{
			"stage": strSchema("Only tickets in this stage, such as planning, backlog, or closed."),
			"part":  strSchema("Only tickets of this part of the plan."),
			"kind":  strSchema("Only tickets of this kind: feature, fix, chore, or docs."),
		}), Handle: t.listTickets}),
		t.convRead(mcp.Tool{Name: "search_tickets", Scope: mcp.ScopeProject, Input: object(map[string]any{
			"query": strSchema("Words that the ticket's title, why, what, or goal hold, all of them."),
		}, "query"), Handle: t.searchTickets}),
		t.convRead(mcp.Tool{Name: "get_ticket", Scope: mcp.ScopeProject, Input: object(map[string]any{
			"key": strSchema("The ticket's number, such as SF-12."),
		}, "key"), Handle: t.getTicket}),
		{Name: "write_breakdown", Scope: mcp.ScopeProject, Input: object(map[string]any{
			"text": strSchema("The breakdown, as markdown."),
		}, "text"), Handle: t.writeBreakdown},
		{Name: "propose_tickets", Scope: mcp.ScopeProject, Input: object(map[string]any{
			"tickets": map[string]any{"type": "array", "items": ticketSchema, "description": "The tickets, in order."},
			"withdraw": map[string]any{"type": "array", "description": "Tickets you proposed before that should go, with the reason.",
				"items": object(map[string]any{"key": strSchema("The ticket's number."), "reason": strSchema("Why it goes.")}, "key", "reason")},
			"revise": map[string]any{"type": "array", "description": "Tickets you proposed before, changed in place: the number, and the whole ticket as it should be. The number and the tickets that depend on it stay.",
				"items": reviseSchema},
		}), Handle: t.proposeTickets},
	} {
		if err := s.Add(tl); err != nil {
			return err
		}
	}
	return nil
}

func strSchema(desc string) map[string]any {
	return map[string]any{"type": "string", "description": desc}
}

func object(props map[string]any, required ...string) map[string]any {
	if props == nil {
		props = map[string]any{}
	}
	o := map[string]any{"type": "object", "properties": props, "additionalProperties": false}
	if len(required) > 0 {
		o["required"] = required
	}
	return o
}

var ticketSchema = object(map[string]any{
	"title": strSchema("What is done, in a few words."),
	"kind":  map[string]any{"type": "string", "enum": []string{"feature", "fix", "chore", "docs"}},
	"why":   strSchema("The reason, from the plan."),
	"what":  strSchema("What is in, and what is not."),
	"goal":  strSchema("What can be seen when it is done."),
	"part":  strSchema("The part of the plan it belongs to."),
	"source": object(map[string]any{"file": strSchema("plan/original.md"), "heading": strSchema("The heading in the plan."),
		"quote": strSchema("A short quote of the plan.")}, "file"),
	"checks": map[string]any{"type": "array", "description": "At least one command a fresh checkout runs, which passes only when the goal is met.",
		"items": object(map[string]any{"kind": map[string]any{"type": "string", "enum": []string{"cmd", "judge"}},
			"spec": strSchema("For cmd, the command. For judge, a criterion no command can prove, in one line, which a judge model reads the diff against.")}, "kind", "spec")},
	"docs":      map[string]any{"type": "array", "items": map[string]any{"type": "string"}, "description": "The pages of documentation it updates."},
	"dependsOn": map[string]any{"type": "array", "items": map[string]any{"type": "string"}, "description": "Tickets that must be done first: SF-4, or @n for the n-th ticket of this call."},
	"priority":  map[string]any{"type": "integer", "minimum": 0, "maximum": 3, "description": "0 urgent, 1 high, 2 normal, 3 low."},
}, "title", "why", "what", "goal", "checks")

// reviseSchema is a ticket of the breakdown with the number of the ticket it changes.
var reviseSchema = func() map[string]any {
	props := map[string]any{"key": strSchema("The number of the ticket to change, such as SF-3.")}
	for k, v := range ticketSchema["properties"].(map[string]any) {
		props[k] = v
	}
	return object(props, "key", "title", "why", "what", "goal", "checks")
}()

func (t *Tools) kick() {
	if t.Kick != nil {
		t.Kick()
	}
}

func (t *Tools) convRead(tl mcp.Tool) mcp.Tool {
	props := map[string]any{"project": strSchema("The project's key or id.")}
	if in, ok := tl.Input["properties"].(map[string]any); ok {
		for k, v := range in {
			props[k] = v
		}
	}
	req := []string{"project"}
	if r, ok := tl.Input["required"].([]string); ok {
		req = append(req, r...)
	}
	tl.AlsoConversation = true
	tl.ConversationInput = object(props, req...)
	orig := tl.Handle
	tl.Handle = func(ctx context.Context, c mcp.Caller, args json.RawMessage) (any, error) {
		if c.Project != "" || c.Crew {
			return orig(ctx, c, args)
		}
		var m map[string]json.RawMessage
		if err := json.Unmarshal(args, &m); err != nil {
			return nil, mcp.Errf("arguments must be a JSON object")
		}
		var proj string
		if raw, ok := m["project"]; ok {
			_ = json.Unmarshal(raw, &proj)
		}
		proj = strings.TrimSpace(proj)
		if proj == "" {
			return nil, mcp.Errf("project: give the project's key or id")
		}
		var id string
		err := t.Pool.QueryRow(ctx, `SELECT id::text FROM projects
			WHERE (id::text = $1 OR lower(key) = lower($1) OR lower(name) = lower($1)) AND state <> 'archived' AND NOT hidden`, proj).Scan(&id)
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, mcp.Errf("no project %s", proj)
		}
		if err != nil {
			return nil, err
		}
		delete(m, "project")
		rest, err := json.Marshal(m)
		if err != nil {
			return nil, err
		}
		c.Project = id
		return orig(ctx, c, rest)
	}
	return tl
}

func (t *Tools) getPlanDraft(ctx context.Context, c mcp.Caller, args json.RawMessage) (any, error) {
	if err := mcp.Decode(args, &struct{}{}); err != nil {
		return nil, err
	}
	var title, state string
	var repo *string
	if err := t.Pool.QueryRow(ctx, `SELECT title, state, repo FROM plan_drafts WHERE id = $1`, c.Draft).Scan(&title, &state, &repo); err != nil {
		return nil, err
	}
	var number int
	var plan string
	err := t.Pool.QueryRow(ctx, `SELECT number, text FROM plan_draft_versions WHERE draft = $1 ORDER BY number DESC LIMIT 1`, c.Draft).Scan(&number, &plan)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return nil, err
	}
	out := map[string]any{"title": title, "state": state, "version": number, "plan": plan}
	if repo != nil {
		out["repository"] = "https://github.com/" + *repo
	}
	return out, nil
}

func (t *Tools) writePlanDraft(ctx context.Context, c mcp.Caller, args json.RawMessage) (any, error) {
	var a struct {
		Plan  string `json:"plan"`
		Title string `json:"title"`
	}
	if err := mcp.Decode(args, &a); err != nil {
		return nil, err
	}
	a.Title = strings.TrimSpace(a.Title)
	switch {
	case strings.TrimSpace(a.Plan) == "":
		return nil, mcp.Errf("plan: give the whole plan")
	case len(a.Plan) > MaxPlan:
		return nil, mcp.Errf("plan: at most 1 MB")
	case len(a.Title) > maxTitle || strings.ContainsAny(a.Title, "\r\n"):
		return nil, mcp.Errf("title: one line of at most %d characters", maxTitle)
	}
	var number int
	err := pgx.BeginFunc(ctx, t.Pool, func(tx pgx.Tx) error {
		var state string
		if err := tx.QueryRow(ctx, `SELECT state FROM plan_drafts WHERE id = $1 FOR UPDATE`, c.Draft).Scan(&state); err != nil {
			return err
		}
		if state != "open" {
			return mcp.Errf("the draft is %s: the project was made from it, and its plan no longer changes", state)
		}
		var versions int
		if err := tx.QueryRow(ctx, `SELECT count(*) FROM plan_draft_versions WHERE draft = $1`, c.Draft).Scan(&versions); err != nil {
			return err
		}
		if versions >= MaxVersions {
			return mcp.Errf("the draft has %d versions, as many as a draft keeps: say so to the operator", versions)
		}
		if err := tx.QueryRow(ctx, `INSERT INTO plan_draft_versions (draft, number, text, run)
			SELECT $1, coalesce(max(number), 0) + 1, $2, nullif($3, '')::uuid FROM plan_draft_versions WHERE draft = $1 RETURNING number`,
			c.Draft, a.Plan, c.Run).Scan(&number); err != nil {
			return err
		}
		_, err := tx.Exec(ctx, `UPDATE plan_drafts SET title = CASE WHEN $2 = '' THEN title ELSE $2 END, updated_at = now() WHERE id = $1`, c.Draft, a.Title)
		return err
	})
	if err != nil {
		return nil, err
	}
	return map[string]any{"version": number, "saved": true}, nil
}

func (t *Tools) project(ctx context.Context, c mcp.Caller) (*board.Project, error) {
	return t.Board.Project(ctx, c.Project)
}

func (t *Tools) getProject(ctx context.Context, c mcp.Caller, args json.RawMessage) (any, error) {
	if err := mcp.Decode(args, &struct{}{}); err != nil {
		return nil, err
	}
	p, err := t.project(ctx, c)
	if err != nil {
		return nil, err
	}
	doc, err := t.Board.Settings(ctx, t.Pool, p)
	if err != nil {
		return nil, err
	}
	var on []string
	for _, s := range board.PipelineOf(doc).Columns() {
		if s.Optional {
			on = append(on, s.ID)
		}
	}
	repo := "the workspace of your run, on main"
	if p.Remote != nil {
		repo += "; on GitHub at " + *p.Remote
	}
	return map[string]any{"name": p.Name, "key": p.Key, "goal": p.Goal, "state": p.State, "optionalStagesOn": on,
		"repository": repo}, nil
}

func (t *Tools) getPlan(ctx context.Context, c mcp.Caller, args json.RawMessage) (any, error) {
	if err := mcp.Decode(args, &struct{}{}); err != nil {
		return nil, err
	}
	p, err := t.project(ctx, c)
	if err != nil {
		return nil, err
	}
	return t.Board.Plan(ctx, p)
}

// ticketLine is a ticket as a list shows it.
type ticketLine struct {
	Key       string  `json:"key"`
	Title     string  `json:"title"`
	Kind      string  `json:"kind"`
	Stage     string  `json:"stage"`
	Part      *string `json:"part"`
	CreatedBy string  `json:"createdBy"`
}

func lines(ts []*board.Ticket, keep func(*board.Ticket) bool) []ticketLine {
	out := []ticketLine{}
	for _, x := range ts {
		if keep(x) {
			out = append(out, ticketLine{x.Key, x.Title, x.Kind, x.Stage, x.Part, x.CreatedBy})
		}
	}
	return out
}

func (t *Tools) listTickets(ctx context.Context, c mcp.Caller, args json.RawMessage) (any, error) {
	var a struct{ Stage, Part, Kind string }
	if err := mcp.Decode(args, &a); err != nil {
		return nil, err
	}
	p, err := t.project(ctx, c)
	if err != nil {
		return nil, err
	}
	ts, err := t.Board.Tickets(ctx, p)
	if err != nil {
		return nil, err
	}
	return lines(ts, func(x *board.Ticket) bool {
		return (a.Stage == "" || x.Stage == a.Stage) && (a.Kind == "" || x.Kind == a.Kind) &&
			(a.Part == "" || (x.Part != nil && strings.EqualFold(*x.Part, a.Part)))
	}), nil
}

func (t *Tools) searchTickets(ctx context.Context, c mcp.Caller, args json.RawMessage) (any, error) {
	var a struct{ Query string }
	if err := mcp.Decode(args, &a); err != nil {
		return nil, err
	}
	words := strings.Fields(strings.ToLower(a.Query))
	if len(words) == 0 || len(a.Query) > 500 {
		return nil, mcp.Errf("query: give a few words")
	}
	p, err := t.project(ctx, c)
	if err != nil {
		return nil, err
	}
	ts, err := t.Board.Tickets(ctx, p)
	if err != nil {
		return nil, err
	}
	return lines(ts, func(x *board.Ticket) bool {
		text := strings.ToLower(strings.Join([]string{x.Title, x.Why, x.What, x.Goal}, " "))
		for _, w := range words {
			if !strings.Contains(text, w) {
				return false
			}
		}
		return true
	}), nil
}

func (t *Tools) getTicket(ctx context.Context, c mcp.Caller, args json.RawMessage) (any, error) {
	var a struct{ Key string }
	if err := mcp.Decode(args, &a); err != nil {
		return nil, err
	}
	p, err := t.project(ctx, c)
	if err != nil {
		return nil, err
	}
	x, err := t.Board.Ticket(ctx, p, a.Key)
	var be *board.Error
	if errors.As(err, &be) {
		return nil, mcp.Errf("%s", be.Msg)
	}
	return x, err
}

// who is how a change made by an agent is named in the history: its role and its run (6.3).
func who(c mcp.Caller) string { return c.Role + " · " + c.Run }

// amendmentRun says whether the caller is the breakdown run of the project's open amendment (7.13):
// after the OK only that run writes a breakdown or proposes tickets, not a lead that analyses a ticket,
// looks again, or writes the report (security review of M11).
func (t *Tools) amendmentRun(ctx context.Context, c mcp.Caller, p *board.Project) error {
	am, err := board.Amending(ctx, t.Pool, p.ID)
	if err != nil {
		return err
	}
	if am == nil {
		return mcp.Errf("%s is %s: the breakdown is written before the OK, or of an amendment before its OK", p.Name, p.State)
	}
	var ok bool
	if err := t.Pool.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM subtasks WHERE id = $1 AND project = $2 AND kind = 'breakdown' AND created_at > $3)`,
		c.Subtask, p.ID, am.At).Scan(&ok); err != nil {
		return err
	}
	if !ok {
		return mcp.Errf("only the run that breaks down amendment %d of %s writes its breakdown and proposes its tickets", am.Number, p.Name)
	}
	return nil
}

func (t *Tools) writeBreakdown(ctx context.Context, c mcp.Caller, args json.RawMessage) (any, error) {
	var a struct{ Text string }
	if err := mcp.Decode(args, &a); err != nil {
		return nil, err
	}
	p, err := t.project(ctx, c)
	if err != nil {
		return nil, err
	}
	if p.State != "planning" {
		if err := t.amendmentRun(ctx, c, p); err != nil {
			return nil, err
		}
	}
	if err := t.Board.SetBreakdown(ctx, p, a.Text, who(c)); err != nil {
		return nil, boardError(err)
	}
	pl, err := t.Board.Plan(ctx, p)
	if err != nil {
		return nil, err
	}
	t.kick()
	return map[string]any{"saved": true, "sentBackComments": len(pl.SentBack)}, nil
}

func (t *Tools) proposeTickets(ctx context.Context, c mcp.Caller, args json.RawMessage) (any, error) {
	var a struct {
		Tickets  []board.NewTicket  `json:"tickets"`
		Withdraw []board.Withdrawal `json:"withdraw"`
		Revise   []board.Revision   `json:"revise"`
	}
	if err := mcp.Decode(args, &a); err != nil {
		return nil, err
	}
	// What only an agent that saw something gives (7.5); the priority is the lead's in the breakdown
	// (7.9), so it is kept.
	for i := range a.Tickets {
		a.Tickets[i].Seen = nil
	}
	for i := range a.Revise {
		a.Revise[i].Seen = nil
	}
	p, err := t.project(ctx, c)
	if err != nil {
		return nil, err
	}
	if p.State != "planning" {
		if err := t.amendmentRun(ctx, c, p); err != nil {
			return nil, err
		}
	}
	ts, err := t.Board.ProposeTickets(ctx, c.Project, a.Tickets, a.Withdraw, a.Revise, who(c))
	if err != nil {
		return nil, boardError(err)
	}
	t.kick()
	proposed, revised := []map[string]string{}, []map[string]string{}
	for i, x := range ts {
		line := map[string]string{"key": x.Key, "title": x.Title}
		if i < len(a.Tickets) {
			proposed = append(proposed, line)
		} else {
			revised = append(revised, line)
		}
	}
	out := map[string]any{"proposed": proposed, "revised": revised, "withdrawn": len(a.Withdraw)}
	if warn := dagLinearWarning(a.Tickets); warn != "" {
		out["warning"] = warn
	}
	return out, nil
}

// dagLinearWarning returns a hint when 4 or more non-docs tickets form a width-1 linear chain with no
// parallel fan-out wave.
func dagLinearWarning(ts []board.NewTicket) string {
	code, withDeps := 0, 0
	for _, t := range ts {
		if t.Kind == "docs" {
			continue
		}
		code++
		if len(t.DependsOn) > 0 {
			withDeps++
		}
	}
	if code >= 4 && withDeps >= code-1 {
		targets := map[string]int{}
		for _, t := range ts {
			if t.Kind == "docs" {
				continue
			}
			for _, d := range t.DependsOn {
				targets[d]++
			}
		}
		fanout := false
		for _, n := range targets {
			if n >= 2 {
				fanout = true
				break
			}
		}
		if !fanout {
			return "All code tickets form a strictly linear dependency chain (width 1), which forces 1 builder at a time. Consider revising dependencies into a 3-Wave fan-out DAG (Wave 1: 1 foundation ticket; Wave 2: 2-4 parallel feature tickets that each depend only on Wave 1; Wave 3: final integration) so multiple builders run in parallel."
		}
	}
	return ""
}

// boardError gives the board's refusal to the agent, and keeps any other error for the server's log.
func boardError(err error) error {
	var be *board.Error
	if errors.As(err, &be) {
		return mcp.Errf("%s", be.Msg)
	}
	return err
}
