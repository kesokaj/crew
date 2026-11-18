package board

import (
	"context"
	"fmt"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/kesokaj/crew/internal/config"
	"github.com/kesokaj/crew/internal/db"
	"github.com/kesokaj/crew/internal/settings"
)

// NewTicket is a ticket as it is added (7.1, 7.12): by the operator in the app, or from the breakdown.
type NewTicket struct {
	Title      string      `json:"title"`
	Kind       string      `json:"kind"`
	Why        string      `json:"why"`
	What       string      `json:"what"`
	Goal       string      `json:"goal"`
	Part       *string     `json:"part"`
	Source     *Source     `json:"source"`
	Checks     []CheckSpec `json:"checks"`
	Docs       []string    `json:"docs"`
	DocsReason *string     `json:"docsReason"`
	DependsOn  []string    `json:"dependsOn"`
	Priority   *int        `json:"priority"`
	Seen       *string     `json:"seen"`
	// Approved says a ticket that is not the plan's or the operator's is approved by being added: one a
	// personal token with "may answer" created (11.7). It is still analysed like an agent's in all
	// else: its may_change waits for the operator's yes, and it cannot skip Review.
	Approved bool `json:"-"`
	// Conversation is the conversation of the operator's, or the chat, a ticket of origin conversation
	// or chat came from, and ConversationRun the run of its assistant whose create_ticket made it
	// ("" for one the operator's Make a ticket of this added) (11.8; plan M20 D19).
	Conversation    string `json:"-"`
	ConversationRun string `json:"-"`
	SpecRef         string `json:"specRef,omitempty"`
	MaxTries        int    `json:"maxTries,omitempty"`
	AssignedAgent   string `json:"assignedAgent,omitempty"`
	BudgetTokens    int64  `json:"budgetTokens,omitempty"`
}

// Origins of a ticket (6.3).
const (
	OriginPlan     = "plan"
	OriginAgent    = "agent"
	OriginOperator = "operator"
	// OriginToken is a ticket the operator's own tool created through the Crew MCP (11.7).
	OriginToken = "token"
	// OriginConversation is a ticket the assistant proposed in a conversation with the operator, or
	// one the operator made of it there; OriginChat one the operator made in the chat of a ticket
	// (11.8, 11.9).
	OriginConversation = "conversation"
	OriginChat         = "chat"
)

var origins = []string{OriginPlan, OriginAgent, OriginOperator, OriginToken, OriginConversation, OriginChat}

// Limits of a ticket's texts, so one ticket cannot fill the board.
const (
	maxTitle = 200
	maxText  = 20000
	maxNote  = 4000
	maxDeps  = 50
)

var docPath = regexp.MustCompile(`^[A-Za-z0-9_][A-Za-z0-9_./-]{0,199}$`)

func checkTexts(title, why, what, goal string) error {
	switch {
	case strings.TrimSpace(title) == "" || len(title) > maxTitle || strings.ContainsAny(title, "\n\r"):
		return errf(Invalid, "title: one line of at most %d characters", maxTitle)
	case len(why) > maxText || len(what) > maxText || len(goal) > maxText:
		return errf(Invalid, "why, what, and goal: at most %d characters each", maxText)
	}
	return nil
}

func checkChecks(cs []CheckSpec) error {
	if len(cs) > 50 {
		return errf(Invalid, "checks: at most 50")
	}
	judges := 0
	for _, c := range cs {
		if c.Kind != "cmd" && c.Kind != "judge" {
			return errf(Invalid, "checks: a check is a cmd or a judge")
		}
		if c.Kind == "judge" {
			judges++
			// A judge reads the work, which does not exist before it: it cannot pass on the base.
			if c.Keeps {
				return errf(Invalid, "checks: keeps is for a command check; a judge check is read against the work")
			}
		}
		if judges > MaxJudgeChecks {
			return errf(Invalid, "checks: at most %d judge checks, which one judge answers together", MaxJudgeChecks)
		}
		if strings.TrimSpace(c.Spec) == "" || len(c.Spec) > 2000 || strings.ContainsAny(c.Spec, "\n\r\x00") {
			return errf(Invalid, "checks: one line of at most 2000 characters")
		}
	}
	return nil
}

func checkDocs(docs []string) error {
	if len(docs) > 50 {
		return errf(Invalid, "docs: at most 50 pages")
	}
	for _, d := range docs {
		if !docPath.MatchString(d) || strings.Contains(d, "..") {
			return errf(Invalid, "docs: %q is not a page's path on main", d)
		}
	}
	return nil
}

func (n *NewTicket) check() error {
	n.Title = strings.TrimSpace(n.Title)
	if n.Kind == "" {
		n.Kind = "feature"
	}
	if !slices.Contains([]string{"feature", "fix", "chore", "docs"}, n.Kind) {
		return errf(Invalid, "kind: feature, fix, chore, or docs")
	}
	if err := checkTexts(n.Title, n.Why, n.What, n.Goal); err != nil {
		return err
	}
	if n.Priority != nil && (*n.Priority < 0 || *n.Priority > 3) {
		return errf(Invalid, "priority: 0 urgent, 1 high, 2 normal, or 3 low")
	}
	if err := checkChecks(n.Checks); err != nil {
		return err
	}
	// The texts beside why, what, and goal reach the board, an export, and later agents' prompts:
	// each has a size, as an agent may write them (security review of M5).
	if n.Part != nil && (len(*n.Part) > maxTitle || strings.ContainsAny(*n.Part, "\r\n")) {
		return errf(Invalid, "part: one line of at most %d characters", maxTitle)
	}
	if n.Source != nil && (len(n.Source.File) > maxTitle || strings.ContainsAny(n.Source.File+n.Source.Heading, "\r\n") ||
		len(n.Source.Heading) > maxTitle || len(n.Source.Quote) > maxNote) {
		return errf(Invalid, "source: a file and a heading of one line each, and a quote of at most %d characters", maxNote)
	}
	if (n.DocsReason != nil && len(*n.DocsReason) > maxNote) || (n.Seen != nil && len(*n.Seen) > maxText) {
		return errf(Invalid, "docsReason: at most %d characters; seen: at most %d", maxNote, maxText)
	}
	if len(n.DependsOn) > maxDeps {
		return errf(Invalid, "dependsOn: at most %d tickets", maxDeps)
	}
	return checkDocs(n.Docs)
}

// AddTicket adds a ticket to a project. Before the OK it goes to Planning, with the breakdown; after
// it, to Backlog, where a ticket the operator adds goes to the top (7.9). A ticket of an agent is
// analysed like any other; a ticket of the plan or of the operator is approved by being added
// (7.3), and Analysis only makes it ready. A ticket added to a Done project makes it run again (7.2).
func (b *Board) AddTicket(ctx context.Context, ref string, n NewTicket, origin, createdBy string) (*Ticket, error) {
	if err := n.check(); err != nil {
		return nil, err
	}
	if !slices.Contains(origins, origin) {
		return nil, errf(Invalid, "origin %q", origin)
	}
	var out *Ticket
	err := b.inTx(ctx, func(tx pgx.Tx) error {
		p, err := projectByRef(ctx, tx, ref, true)
		if err != nil {
			return err
		}
		if p.State == "archived" {
			return errf(Refused, "%s is archived", p.Name)
		}
		doc, err := b.Settings(ctx, tx, p)
		if err != nil {
			return err
		}
		out, err = b.addTicket(ctx, tx, p, doc, n, origin, createdBy, nil)
		return err
	})
	return out, err
}

// addTicket adds one ticket in the transaction given, with the project locked. earlier are the keys
// of the tickets added before it in the same call, which a dependency names as @1, @2, and on.
func (b *Board) addTicket(ctx context.Context, tx pgx.Tx, p *Project, doc settings.Doc, n NewTicket, origin, createdBy string, earlier []string) (*Ticket, error) {
	var out *Ticket
	err := func() error {
		var err error
		stage := settings.StageBacklog
		// The breakdown's tickets wait in Planning for the OK: the plan's, or an amendment's (7.13).
		var amendment *int64
		if p.State == "planning" {
			stage = settings.StagePlanning
		} else if origin == OriginPlan {
			am, err := Amending(ctx, tx, p.ID)
			if err != nil {
				return err
			}
			if am != nil {
				stage, amendment = settings.StagePlanning, &am.ID
			}
		}
		priority := 2
		if n.Priority != nil {
			priority = *n.Priority
		}
		status := StatusWaiting
		if origin != OriginPlan && origin != OriginOperator && !n.Approved {
			status = StatusProposed
		}
		// The top of the backlog for the operator's own ticket on a running board; the end otherwise.
		posSQL := `coalesce(max(position), 0) + 1`
		if origin == OriginOperator && stage == settings.StageBacklog {
			posSQL = `coalesce(min(position), 1) - 1`
		}
		var number int
		var position, id int64
		if err := tx.QueryRow(ctx, `UPDATE projects SET next_number = next_number + 1 WHERE id = $1 RETURNING next_number - 1`, p.ID).Scan(&number); err != nil {
			return err
		}
		if err := tx.QueryRow(ctx, `SELECT `+posSQL+` FROM tickets WHERE project = $1`, p.ID).Scan(&position); err != nil {
			return err
		}
		// A priority the operator gives a ticket they add is theirs; one an agent gives is not (7.9).
		var operatorPriority *int
		if origin == OriginOperator && n.Priority != nil {
			operatorPriority = n.Priority
		}
		maxTries := n.MaxTries
		if maxTries < 1 || maxTries > 20 {
			maxTries = 3
		}
		err = tx.QueryRow(ctx, `INSERT INTO tickets (project, number, title, kind, part, source, why, what, goal, docs, docs_reason,
				stage, origin, created_by, seen, analysis, priority, position, operator_priority, spec_ref, max_tries, assigned_agent, budget_tokens)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, $16, $17, $18, $19, $20, $21, $22, $23) RETURNING id`,
			p.ID, number, n.Title, n.Kind, n.Part, n.Source, n.Why, n.What, n.Goal, nonNil(n.Docs), n.DocsReason,
			stage, origin, createdBy, n.Seen, Analysis{Status: status, Approved: n.Approved}, priority, position, operatorPriority,
			n.SpecRef, maxTries, n.AssignedAgent, max(n.BudgetTokens, int64(0))).Scan(&id)
		if err != nil {
			return err
		}
		if err := writeChecks(ctx, tx, id, n.Checks); err != nil {
			return err
		}
		if amendment != nil {
			if _, err := tx.Exec(ctx, `UPDATE tickets SET amendment = $2 WHERE id = $1`, id, *amendment); err != nil {
				return err
			}
		}
		// One the operator confirmed in the app is theirs from the start (D19).
		if n.Conversation != "" {
			if _, err := tx.Exec(ctx, `UPDATE tickets SET conversation = $2::uuid, conversation_run = nullif($3, '')::uuid,
				adopted_at = CASE WHEN $4 THEN now() END WHERE id = $1`, id, n.Conversation, n.ConversationRun, n.Approved); err != nil {
				return err
			}
		}
		t, err := ticketByID(ctx, tx, id, true)
		if err != nil {
			return err
		}
		for _, dep := range n.DependsOn {
			if rest, ok := strings.CutPrefix(dep, "@"); ok {
				i, err := strconv.Atoi(rest)
				if err != nil || i < 1 || i > len(earlier) {
					return errf(Invalid, "dependsOn: %s names no ticket before it in this call", dep)
				}
				dep = earlier[i-1]
			}
			if err := b.link(ctx, tx, p, t, dep, "depends_on"); err != nil {
				return err
			}
		}
		if t, err = ticketByID(ctx, tx, id, false); err != nil {
			return err
		}
		if err := db.RecordProjectChange(ctx, tx, p.ID, createdBy, "tickets", t.Key, nil, t); err != nil {
			return err
		}
		if p.State == "done" {
			if _, err := tx.Exec(ctx, `UPDATE projects SET state = 'running', updated_at = now() WHERE id = $1`, p.ID); err != nil {
				return err
			}
			if err := db.RecordProjectChange(ctx, tx, p.ID, createdBy, "projects", p.ID,
				map[string]any{"state": "done"}, map[string]any{"state": "running", "because": t.Key}); err != nil {
				return err
			}
		}
		out = t
		return nil
	}()
	return out, err
}

func nonNil(s []string) []string {
	if s == nil {
		return []string{}
	}
	return s
}

func settingNumber(d settings.Doc, path string) (float64, bool) {
	cur := any(d)
	for _, k := range strings.Split(path, ".") {
		m, ok := cur.(settings.Doc)
		if !ok {
			return 0, false
		}
		cur = m[k]
	}
	switch v := cur.(type) {
	case int:
		return float64(v), true
	case float64:
		return v, true
	}
	return 0, false
}

func settingString(d settings.Doc, path string) string {
	cur := any(d)
	for _, k := range strings.Split(path, ".") {
		m, ok := cur.(settings.Doc)
		if !ok {
			return ""
		}
		cur = m[k]
	}
	s, _ := cur.(string)
	return s
}

func writeChecks(ctx context.Context, tx pgx.Tx, ticket int64, cs []CheckSpec) error {
	if _, err := tx.Exec(ctx, `DELETE FROM checks WHERE ticket = $1`, ticket); err != nil {
		return err
	}
	for i, c := range cs {
		if _, err := tx.Exec(ctx, `INSERT INTO checks (ticket, position, kind, spec, keeps) VALUES ($1, $2, $3, $4, $5)`,
			ticket, i+1, c.Kind, strings.TrimSpace(c.Spec), c.Keeps && c.Kind == "cmd"); err != nil {
			return err
		}
	}
	return nil
}

// link adds a link from t to the ticket named by key, of the same project. A depends_on that would
// close a circle is refused (7.9).
func (b *Board) link(ctx context.Context, tx pgx.Tx, p *Project, t *Ticket, key, kind string) error {
	n, err := ParseNumber(p, key)
	if err != nil {
		return errf(Invalid, "%s: %v", kind, err)
	}
	var to int64
	if err := tx.QueryRow(ctx, `SELECT id FROM tickets WHERE project = $1 AND number = $2`, p.ID, n).Scan(&to); err != nil {
		return errf(Invalid, "%s: %s has no ticket %s", kind, p.Name, key)
	}
	if to == t.ID {
		return errf(Invalid, "%s: a ticket cannot link to itself", kind)
	}
	if kind == "depends_on" {
		var circle bool
		err := tx.QueryRow(ctx, `WITH RECURSIVE deps(id) AS (
				SELECT $1::bigint
				UNION SELECT l.to_ticket FROM ticket_links l JOIN deps d ON l.from_ticket = d.id WHERE l.kind = 'depends_on')
			SELECT EXISTS (SELECT 1 FROM deps WHERE id = $2)`, to, t.ID).Scan(&circle)
		if err != nil {
			return err
		}
		if circle {
			return errf(Refused, "%s cannot depend on %s: that would close a circle", t.Key, key)
		}
	}
	_, err = tx.Exec(ctx, `INSERT INTO ticket_links (from_ticket, to_ticket, kind) VALUES ($1, $2, $3) ON CONFLICT DO NOTHING`, t.ID, to, kind)
	return err
}

// Link links two tickets of a project, with its row in changes.
func (b *Board) Link(ctx context.Context, p *Project, from, to, kind, who string) error {
	if !slices.Contains([]string{"depends_on", "created_from", "duplicates", "replaces"}, kind) {
		return errf(Invalid, "kind: depends_on, created_from, duplicates, or replaces")
	}
	n, err := ParseNumber(p, from)
	if err != nil {
		return err
	}
	return b.inTx(ctx, func(tx pgx.Tx) error {
		if _, err := projectByRef(ctx, tx, p.ID, true); err != nil {
			return err
		}
		t, err := ticketByNumber(ctx, tx, p.ID, n, true)
		if err != nil {
			return err
		}
		before := t.Links
		if err := b.link(ctx, tx, p, t, to, kind); err != nil {
			return err
		}
		after, err := ticketByID(ctx, tx, t.ID, false)
		if err != nil {
			return err
		}
		return db.RecordProjectChange(ctx, tx, p.ID, who, "tickets", t.Key, map[string]any{"links": before}, map[string]any{"links": after.Links})
	})
}

// Edit is what the operator may change on a ticket (15.4): its priority and hold at any time before it
// is done; its title, why, what, goal, and kind only before Analysis. A ticket has no budget (since 0.53).
type Edit struct {
	Priority *int    `json:"priority"`
	Hold     *bool   `json:"hold"`
	Title    *string `json:"title"`
	Kind     *string `json:"kind"`
	Why      *string `json:"why"`
	What     *string `json:"what"`
	Goal     *string `json:"goal"`
	// Agents replaces the ticket's pins: a model with its CLI, an effort, or both, by role (12.3).
	Agents map[string]any `json:"agents"`
	// Network is the ticket's own network mode, or "inherit" (13.6). It applies to the runs that start
	// after it, as a project's does.
	Network *string `json:"network"`
}

// EditTicket changes a ticket as the operator asked, with its row in changes.
func (b *Board) EditTicket(ctx context.Context, p *Project, ref string, e Edit, who string) (*Ticket, error) {
	n, err := ParseNumber(p, ref)
	if err != nil {
		return nil, err
	}
	var out *Ticket
	err = b.inTx(ctx, func(tx pgx.Tx) error {
		if _, err := projectByRef(ctx, tx, p.ID, true); err != nil {
			return err
		}
		t, err := ticketByNumber(ctx, tx, p.ID, n, true)
		if err != nil {
			return err
		}
		if t.Stage == settings.StageDone || t.Stage == settings.StageClosed {
			return errf(Refused, "%s is %s: a change is a new ticket", t.Key, t.Stage)
		}
		before, after := map[string]any{}, map[string]any{}
		set := func(col string, old, v any) {
			before[col], after[col] = old, v
		}
		if e.Priority != nil {
			if *e.Priority < 0 || *e.Priority > 3 {
				return errf(Invalid, "priority: 0 urgent, 1 high, 2 normal, or 3 low")
			}
			set("priority", t.Priority, *e.Priority)
			// Only the operator's priority orders the lead queue across projects (7.9).
			set("operator_priority", t.OperatorPriority, *e.Priority)
		}
		texts := e.Title != nil || e.Why != nil || e.What != nil || e.Goal != nil || e.Kind != nil
		if texts && t.Stage != settings.StagePlanning && t.Stage != settings.StageBacklog {
			return errf(Refused, "%s is in %s: its title, why, what, and goal change only before Analysis", t.Key, t.Stage)
		}
		title, why, what, goal := t.Title, t.Why, t.What, t.Goal
		for _, f := range []struct {
			col string
			v   *string
			dst *string
		}{{"title", e.Title, &title}, {"why", e.Why, &why}, {"what", e.What, &what}, {"goal", e.Goal, &goal}} {
			if f.v != nil {
				set(f.col, *f.dst, *f.v)
				*f.dst = *f.v
			}
		}
		if err := checkTexts(strings.TrimSpace(title), why, what, goal); err != nil {
			return err
		}
		if e.Title != nil {
			after["title"] = strings.TrimSpace(title)
		}
		if e.Kind != nil {
			if !slices.Contains([]string{"feature", "fix", "chore", "docs"}, *e.Kind) {
				return errf(Invalid, "kind: feature, fix, chore, or docs")
			}
			set("kind", t.Kind, *e.Kind)
		}
		if e.Agents != nil {
			if errs := settings.CheckTicketAgents(e.Agents); len(errs) > 0 {
				return errf(Invalid, "agents: %s", strings.Join(errs, "; "))
			}
			set("agents", t.Agents, e.Agents)
		}
		if e.Network != nil && *e.Network != t.Network {
			if *e.Network != "inherit" && !slices.Contains(config.Modes, *e.Network) {
				return errf(Invalid, "network: inherit, internet, model_only, or lan")
			}
			set("network", t.Network, *e.Network)
		}
		if e.Hold != nil && *e.Hold != t.Hold {
			if err := b.setHold(ctx, tx, t, *e.Hold, before, after); err != nil {
				return err
			}
		}
		if len(after) == 0 {
			out = t
			return nil
		}
		if err := updateTicket(ctx, tx, t.ID, after); err != nil {
			return err
		}
		if err := db.RecordProjectChange(ctx, tx, p.ID, who, "tickets", t.Key, before, after); err != nil {
			return err
		}
		if texts && t.Stage == settings.StagePlanning {
			if err := intervention(ctx, tx, p.ID, t.ID, EditedBeforeOK, "Edited "+t.Key+" before the OK", nil); err != nil {
				return err
			}
		}
		out, err = ticketByID(ctx, tx, t.ID, false)
		return err
	})
	return out, err
}

// setHold holds a ticket or lets it go on. A hold lets a run that is going finish and starts nothing
// new (7.9). On a new feature during its notice, a hold stops its clock, and letting it go starts the
// clock again with what was left (7.3).
func (b *Board) setHold(ctx context.Context, tx pgx.Tx, t *Ticket, hold bool, before, after map[string]any) error {
	before["hold"], after["hold"] = t.Hold, hold
	if t.Analysis.Status != StatusNotice {
		return nil
	}
	a := t.Analysis
	if hold && t.NoticeUntil != nil {
		left := t.NoticeUntil.Sub(b.now())
		if left < 0 {
			left = 0
		}
		// Whole seconds, rounded up, so what was left is never lost to the rounding.
		a.NoticeLeftSeconds, a.NoticePaused = int64((left+time.Second-1)/time.Second), true
		before["notice_until"], after["notice_until"] = t.NoticeUntil, nil
	}
	if !hold && t.NoticeUntil == nil && a.NoticePaused {
		// A notice that had passed when it was held goes ahead at once: until is now.
		until := b.now().Add(time.Duration(a.NoticeLeftSeconds) * time.Second)
		a.NoticeLeftSeconds, a.NoticePaused = 0, false
		before["notice_until"], after["notice_until"] = nil, until
	}
	before["analysis"], after["analysis"] = t.Analysis, a
	return nil
}

// updateTicket writes the columns of cols, which only the board's own code names.
func updateTicket(ctx context.Context, tx pgx.Tx, id int64, cols map[string]any) error {
	keys := make([]string, 0, len(cols))
	for k := range cols {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	sets := make([]string, 0, len(keys)+1)
	args := []any{id}
	for _, k := range keys {
		if !ticketColumn[k] {
			return fmt.Errorf("board: %q is not a column a change may set", k)
		}
		args = append(args, cols[k])
		sets = append(sets, fmt.Sprintf("%s = $%d", k, len(args)))
	}
	sets = append(sets, "updated_at = now()")
	_, err := tx.Exec(ctx, `UPDATE tickets SET `+strings.Join(sets, ", ")+` WHERE id = $1`, args...)
	return err
}

var ticketColumn = map[string]bool{
	"priority": true, "operator_priority": true, "title": true, "kind": true, "why": true, "what": true, "goal": true,
	"hold": true, "notice_until": true, "analysis": true, "approach": true, "docs": true, "docs_reason": true,
	"skips": true, "skips_reason": true, "branch": true, "agents": true, "network": true,
	"spec_ref": true, "max_tries": true, "tries_used": true, "assigned_agent": true, "budget_tokens": true,
}

// Discard closes a ticket that is not done as discarded, with the reason (7.4, 7.6). The operator
// can discard any ticket that is not done.
func (b *Board) Discard(ctx context.Context, p *Project, ref, reason, who string) (*Ticket, error) {
	if strings.TrimSpace(reason) == "" {
		return nil, errf(Invalid, "reason: say why the ticket is discarded")
	}
	n, err := ParseNumber(p, ref)
	if err != nil {
		return nil, err
	}
	var out *Ticket
	err = b.inTx(ctx, func(tx pgx.Tx) error {
		if _, err := projectByRef(ctx, tx, p.ID, true); err != nil {
			return err
		}
		t, err := ticketByNumber(ctx, tx, p.ID, n, true)
		if err != nil {
			return err
		}
		doc, err := b.Settings(ctx, tx, p)
		if err != nil {
			return err
		}
		feature := t.Analysis.Status == StatusNotice
		if err := b.close(ctx, tx, p, PipelineOf(doc), t, "discarded", reason, who); err != nil {
			return err
		}
		if feature {
			if err := intervention(ctx, tx, p.ID, t.ID, FeatureDiscard, "Discarded a new feature", nil); err != nil {
				return err
			}
		}
		out, err = ticketByID(ctx, tx, t.ID, false)
		return err
	})
	return out, err
}

func (b *Board) close(ctx context.Context, tx pgx.Tx, p *Project, pipe Pipeline, t *Ticket, why, reason, who string) error {
	switch t.Stage {
	case settings.StageClosed:
		return errf(Refused, "%s is closed already", t.Key)
	case settings.StageDone:
		return errf(Refused, "%s is done: a change is a new ticket", t.Key)
	}
	a := t.Analysis
	a.Reason = reason
	if _, err := tx.Exec(ctx, `UPDATE tickets SET analysis = $2, notice_until = NULL WHERE id = $1`, t.ID, a); err != nil {
		return err
	}
	t.ClosedReason = &why
	return b.move(ctx, tx, p, pipe, t, settings.StageClosed, who, why+": "+reason, nil)
}

// NoticeDecision is the operator's answer to a new feature during its notice (7.3): Approve now,
// Discard, Hold, or Lift the hold (release), which starts its clock again with what was left.
func (b *Board) NoticeDecision(ctx context.Context, p *Project, ref, choice, reason, who string) (*Ticket, error) {
	t, err := b.Ticket(ctx, p, ref)
	if err != nil {
		return nil, err
	}
	waits := t.Analysis.Status == StatusNotice || t.Analysis.Status == StatusOperator
	if !waits || t.Stage != settings.StageAnalysis {
		return nil, errf(Refused, "%s is not a new feature waiting for its notice, nor a ticket waiting for your decision", t.Key)
	}
	switch choice {
	case "approve_now":
		var out *Ticket
		err := b.inTx(ctx, func(tx pgx.Tx) error {
			if _, err := projectByRef(ctx, tx, p.ID, true); err != nil {
				return err
			}
			t, err := ticketByID(ctx, tx, t.ID, true)
			if err != nil {
				return err
			}
			if (t.Analysis.Status != StatusNotice && t.Analysis.Status != StatusOperator) || t.Stage != settings.StageAnalysis {
				return errf(Refused, "%s is no longer waiting for its notice or your decision", t.Key)
			}
			// A ticket the analysis proposed to skip or discard may have no check yet: it is not made
			// ready without its why, what, goal, and a check.
			if err := approvable(t, nil, t.Checks); err != nil {
				return err
			}
			if err := b.markReady(ctx, tx, p, t, who, "approved now"); err != nil {
				return err
			}
			out, err = ticketByID(ctx, tx, t.ID, false)
			return err
		})
		return out, err
	case "discard":
		if strings.TrimSpace(reason) == "" {
			reason = "discarded during its notice"
			if t.Analysis.Status == StatusOperator {
				reason = "discarded: the operator decided"
			}
		}
		return b.Discard(ctx, p, ref, reason, who)
	case "hold", "release":
		h := choice == "hold"
		return b.EditTicket(ctx, p, ref, Edit{Hold: &h}, who)
	}
	return nil, errf(Invalid, "choice: approve_now, discard, hold, or release")
}

// NeedsAgreement says a ticket waits for the operator to agree that it may change the files that
// instruct agents its analysis named (13.8): a ticket an agent created, or one that came through a
// token, a conversation, or a chat. A ticket of the plan or of the operator needs no yes.
func NeedsAgreement(t *Ticket) bool {
	return ((t.Origin != OriginPlan && t.Origin != OriginOperator) || (t.SpecRef != "" && t.CreatedBy != Operator)) &&
		len(t.Analysis.MayChange) > 0 && !t.Analysis.MayChangeAgreed
}

// AgreementWait is why such a ticket waits.
func AgreementWait(t *Ticket) string {
	return "it may change files that instruct agents (" + strings.Join(t.Analysis.MayChange, ", ") + "): it waits for you to agree (13.8)"
}

// MayChangeAnswer is the operator's answer to a ticket that waits for their agreement (13.8): agree
// lets it change the files its may_change names; refuse takes them out of may_change, so the ticket
// goes on without them and a push that changes one is refused.
func (b *Board) MayChangeAnswer(ctx context.Context, p *Project, ref, choice, who string) (*Ticket, error) {
	n, err := ParseNumber(p, ref)
	if err != nil {
		return nil, err
	}
	if choice != "agree" && choice != "refuse" {
		return nil, errf(Invalid, "choice: agree or refuse")
	}
	var out *Ticket
	err = b.inTx(ctx, func(tx pgx.Tx) error {
		if _, err := projectByRef(ctx, tx, p.ID, true); err != nil {
			return err
		}
		t, err := ticketByNumber(ctx, tx, p.ID, n, true)
		if err != nil {
			return err
		}
		if t.Stage == settings.StageDone || t.Stage == settings.StageClosed {
			return errf(Refused, "%s is %s", t.Key, t.Stage)
		}
		if !NeedsAgreement(t) {
			return errf(Refused, "%s does not wait for your agreement", t.Key)
		}
		a := t.Analysis
		if choice == "agree" {
			a.MayChangeAgreed = true
		} else {
			a.MayChange = nil
		}
		if err := updateTicket(ctx, tx, t.ID, map[string]any{"analysis": a}); err != nil {
			return err
		}
		if err := db.RecordProjectChange(ctx, tx, p.ID, who, "tickets", t.Key, map[string]any{"analysis": t.Analysis},
			map[string]any{"analysis": a, "mayChange": choice}); err != nil {
			return err
		}
		out, err = ticketByID(ctx, tx, t.ID, false)
		return err
	})
	if err == nil {
		b.Kick()
	}
	return out, err
}

// markReady makes a ticket in Analysis ready, as its decision or its notice says.
func (b *Board) markReady(ctx context.Context, tx pgx.Tx, p *Project, t *Ticket, who, why string) error {
	a := t.Analysis
	a.Status, a.NoticeLeftSeconds, a.NoticePaused = StatusReady, 0, false
	if who != Backend {
		a.DecidedBy = who
	}
	if err := updateTicket(ctx, tx, t.ID, map[string]any{"analysis": a, "notice_until": nil}); err != nil {
		return err
	}
	return db.RecordProjectChange(ctx, tx, p.ID, who, "tickets", t.Key,
		map[string]any{"analysis": t.Analysis, "notice_until": t.NoticeUntil},
		map[string]any{"analysis": a, "notice_until": nil, "why": why})
}

// LockForSubtasks takes the locks a writer of a ticket's subtasks holds before it counts their numbers:
// its project's row first, as the board's own changes take it, and then the ticket's, so two writers
// never give two subtasks one number and never wait for each other in a circle (7.11).
func LockForSubtasks(ctx context.Context, tx pgx.Tx, ticket int64) error {
	if _, err := tx.Exec(ctx, `SELECT 1 FROM projects p JOIN tickets t ON t.project = p.id WHERE t.id = $1 FOR KEY SHARE OF p`, ticket); err != nil {
		return err
	}
	_, err := tx.Exec(ctx, `SELECT 1 FROM tickets WHERE id = $1 FOR UPDATE`, ticket)
	return err
}
