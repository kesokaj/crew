package pipeline

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"path"
	"regexp"
	"slices"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/kesokaj/crew/internal/board"
	"github.com/kesokaj/crew/internal/homepath"
	"github.com/kesokaj/crew/internal/improve"
	"github.com/kesokaj/crew/internal/mcp"
	"github.com/kesokaj/crew/internal/redact"
	"github.com/kesokaj/crew/internal/rooms"
	"github.com/kesokaj/crew/internal/settings"
)

// Tools are the Crew MCP's tools of M6 (11.5): the ones that ask, record, and request a move, and
// decide. A write goes to the caller's own ticket and its own subtask only; both come from its token.
type Tools struct {
	Pool  *pgxpool.Pool
	Board *board.Board
	// Kick tells the pipeline and the board that something changed.
	Kick func()
	// KickPlanner wakes the planner reconciler when a peer asks @planner on a Tier-2 feature spec.
	KickPlanner func()
	// Home is the home on the machine of a run's sandbox, which the run may write: the E2E tester's
	// screenshots are read from it (7.4). Nil takes none.
	Home func(ctx context.Context, run string) (string, error)
	// Secrets are the values a run's sandbox was handed, which an entry it writes never keeps: it is
	// stored, and posted to a pull request (8.3, review of M10). Nil removes secret shapes only.
	Secrets func(ctx context.Context, run string) []string
	// Improve is the Crew level's side of improvements, for the improver's tools (10.4); Version is
	// the running Crew version.
	Improve *improve.Service
	Version string
	// Inbox lists the operator's inbox, for a normal conversation's get_inbox and get_crew_map (11.8).
	Inbox Inbox
	// Rooms manages #<project> and feature thread chat rooms (M24, R214).
	Rooms *rooms.Service

	// caps holds a cap's count and the act it allows together, so calls at once cannot pass it
	// together (security review of M9). The server is the one writer, in one process.
	caps sync.Mutex
}

// Register adds the tools to a server.
func (t *Tools) Register(s *mcp.Server) error {
	str := func(desc string) map[string]any { return map[string]any{"type": "string", "description": desc} }
	strs := func(desc string) map[string]any {
		return map[string]any{"type": "array", "items": map[string]any{"type": "string"}, "description": desc}
	}
	check := object(map[string]any{
		"kind": map[string]any{"type": "string", "enum": []string{"cmd", "judge"},
			"description": "cmd: a command that proves the goal. judge: a criterion no command can prove, which a judge model reads the diff against."},
		"spec":  str("For cmd, the command, run in a fresh checkout of the ticket's branch. For judge, the criterion, in one line."),
		"keeps": map[string]any{"type": "boolean", "description": "For cmd only: true for a check that must keep passing, which passes before the work too."},
	}, "kind", "spec")
	for _, tl := range append([]mcp.Tool{
		{Name: "get_summary", Scope: mcp.ScopeProject, AlsoCrew: true, Input: object(map[string]any{"key": str("The ticket's number, such as SF-12.")}, "key"), Handle: t.getSummary},
		{Name: "search_summaries", Scope: mcp.ScopeProject, AlsoCrew: true, Input: object(map[string]any{"query": str("Words the summaries hold."),
			"project": str("For the improver only: a project's name, to search one project; empty searches them all.")}, "query"), Handle: t.searchSummaries},
		{Name: "set_steps", Scope: mcp.ScopeProject, Input: object(map[string]any{"steps": strs("The steps, in order, each one line.")}, "steps"), Handle: t.setSteps},
		{Name: "tick_step", Scope: mcp.ScopeProject, Input: object(map[string]any{"step": map[string]any{"type": "integer", "minimum": 1, "description": "The step's number, from 1."}}, "step"), Handle: t.tickStep},
		{Name: "add_note", Scope: mcp.ScopeProject, Input: object(map[string]any{"text": str("The note, as markdown.")}, "text"), Handle: t.addNote},
		{Name: "reply_comment", Scope: mcp.ScopeProject, Input: object(map[string]any{
			"id": map[string]any{"type": "integer", "description": "The comment's id."}, "text": str("Your answer.")}, "id", "text"), Handle: t.replyComment},
		{Name: "write_review", Scope: mcp.ScopeProject, Input: object(map[string]any{
			"verdict": map[string]any{"type": "string", "enum": []string{board.VerdictApprove, board.VerdictChanges}},
			"text":    str("The review: what you read, and each finding with its file and what to do."),
			"tests": map[string]any{"type": "array", "description": "Each test file the change adds, changes, or deletes, as Crew listed them, with why its change is right. An approve needs every one.",
				"items": object(map[string]any{"path": str("The test file."), "why": str("Why its change is right.")}, "path", "why")}},
			"verdict", "text"), Handle: t.writeReview},
		{Name: "add_test_result", Scope: mcp.ScopeProject, Input: object(map[string]any{
			"result": map[string]any{"type": "string", "enum": []string{"pass", "fail"}},
			"text":   str("What you ran, what it printed, and what you found.")}, "result", "text"), Handle: t.addTestResult},
		{Name: "add_e2e_result", Scope: mcp.ScopeProject, Input: object(map[string]any{
			"result": map[string]any{"type": "string", "enum": []string{"pass", "fail"}},
			"text":   str("The flows you drove, what each did, and the log lines that matter."),
			"screenshots": map[string]any{"type": "array", "maxItems": maxShots, "items": map[string]any{"type": "string"},
				"description": "PNG or JPEG files you took, as paths relative to your home, such as e2e-shots/login.png: at most 10, 2 MB each."},
		}, "result", "text"), Handle: t.addE2EResult},
		{Name: "add_stage_verdict", Scope: mcp.ScopeProject, Input: object(map[string]any{
			"result": map[string]any{"type": "string", "enum": []string{"pass", "fail"}},
			"text":   str("What the stage asks, what you found, and for each finding where it is and what to do."),
		}, "result", "text"), Handle: t.addStageVerdict},
		{Name: "add_verification", Scope: mcp.ScopeProject, Input: object(map[string]any{
			"result": map[string]any{"type": "string", "enum": []string{"pass", "fail"}},
			"text":   str("What the goal and the plan ask, what the change does, and for each difference where it is."),
		}, "result", "text"), Handle: t.addVerification},
		{Name: "request_move", Scope: mcp.ScopeProject, Input: object(nil), Handle: t.reporter(kindBuild, "builder", "request_move")},
		{Name: "fix_done", Scope: mcp.ScopeProject, Input: object(nil), Handle: t.reporter(kindFix, "builder", "fix_done")},
		{Name: "analyse_ticket", Scope: mcp.ScopeProject, Input: object(map[string]any{
			"choice":      map[string]any{"type": "string", "enum": []string{"approve", "skip", "discard", "notice"}},
			"reason":      str("Why: for skip, discard, and notice it is required."),
			"lookAgain":   object(map[string]any{"kind": map[string]any{"type": "string", "enum": []string{"part_done", "ticket_done", "date", "all_done"}}, "value": str("The part, the ticket, or the date.")}, "kind"),
			"why":         str("The why, written or confirmed."),
			"what":        str("The what: what is in, and what is not."),
			"goal":        str("What can be seen when it is done."),
			"checks":      map[string]any{"type": "array", "items": check, "description": "The checks, replacing the ones the ticket has."},
			"approach":    str("A short approach for the builder."),
			"docs":        strs("The documentation pages it updates."),
			"docsReason":  str("Why it updates no page, when docs is empty."),
			"skips":       strs("Optional stages it does not need, such as e2e."),
			"skipsReason": str("Why it does not need them."),
			"mayChange":   strs("Paths of files that instruct agents that it may change, such as AGENTS.md."),
			"dependsOn":   strs("Tickets that must be done first, such as SF-4."),
			"priority":    map[string]any{"type": "integer", "minimum": 0, "maximum": 3},
			"network": map[string]any{"type": "string", "enum": []string{"model_only"},
				"description": "model_only when the ticket's runs need nothing from the internet but their model: they then reach only it and Crew."},
			"bugIn": str("The curator, approving a ticket that fixes a bug: the Done ticket whose change the defect is in, such as SF-12, " +
				"as you found it in main's history, whatever the reporter named; or none when it cannot be told. Not when the bug's ticket is settled already."),
		}, "choice"), Handle: t.analyseTicket},
		{Name: "write_report", Scope: mcp.ScopeProject, Input: object(map[string]any{
			"built":     str("What the project can do now, by the parts of the plan."),
			"skipped":   str("What was skipped, and why."),
			"cancelled": str("What was cancelled, why, and what replaced it."),
			"missing":   str("What the plan asked for that is not there, and what it would take."),
			"learned":   str("What went well, what kept failing, and what the next project should do differently."),
		}, "built", "skipped", "cancelled", "missing", "learned"), Handle: t.writeReport},
		{Name: "new_approach", Scope: mcp.ScopeProject, Input: object(map[string]any{
			"reason":   str("Why the ticket keeps failing: the approach, the goal, a check, or its size."),
			"approach": str("The approach a builder takes now: what to do differently, step by step."),
		}, "reason", "approach"), Handle: t.newApproach},
		{Name: "cancel_and_replace", Scope: mcp.ScopeProject, Input: object(map[string]any{
			"reason": str("Why the ticket cannot get through; for a ticket that replaces a cancelled one, what is missing."),
			"tickets": map[string]any{"type": "array", "maxItems": maxReplacements, "description": "The tickets that replace it.",
				"items": object(map[string]any{
					"title":  str("One line that names the work."),
					"kind":   map[string]any{"type": "string", "enum": []string{"feature", "fix", "chore", "docs"}},
					"why":    str("Why it is needed."),
					"what":   str("What is in it, and what is not."),
					"goal":   str("What can be seen when it is done."),
					"checks": map[string]any{"type": "array", "items": check, "description": "At least one command check that fails until its goal is met."},
				}, "title", "why", "what", "goal", "checks")},
		}, "reason"), Handle: t.cancelAndReplace},
		{Name: "create_ticket", Scope: mcp.ScopeProject, Input: object(map[string]any{
			"title":  str("One line that names the work."),
			"seen":   str("What you saw: the evidence, such as a failing command and what it printed, or a file and its line."),
			"why":    str("Why you think the ticket is needed."),
			"kind":   map[string]any{"type": "string", "enum": []string{"feature", "fix", "chore", "docs"}},
			"what":   str("Optionally, a first idea of what is in it."),
			"goal":   str("Optionally, a first idea of what can be seen when it is done."),
			"checks": map[string]any{"type": "array", "items": check, "description": "Optionally, a first idea of a check."},
		}, "title", "seen", "why"), Handle: t.createTicket},
		{Name: "link_tickets", Scope: mcp.ScopeProject, Input: object(map[string]any{
			"from": str("The ticket the link starts at, such as SF-12."),
			"to":   str("The ticket it points at, such as SF-4."),
			"kind": map[string]any{"type": "string", "enum": []string{"depends_on", "duplicates", "created_from"},
				"description": "from depends on to, duplicates it, or was created from it."},
		}, "from", "to", "kind"), Handle: t.linkTickets},
		{Name: "create_subtask", Scope: mcp.ScopeProject, Input: object(map[string]any{
			"title":    str("One line that names the part."),
			"what":     str("What to do, inside the ticket's what."),
			"doneWhen": str("What done looks like."),
			"check":    str("Optionally, one command that passes when this part is done, run on its branch."),
			"role":     map[string]any{"type": "string", "enum": []string{"builder", "tester"}},
			"touches":  strs("The files or folders it will change, such as internal/cart/ or web/src/cart.tsx. Two parts that name the same file do not run together."),
			"after":    map[string]any{"type": "array", "items": map[string]any{"type": "integer", "minimum": 1}, "description": "The numbers of the subtasks of this ticket it must come after, such as 2 for SF-9.2."},
		}, "title", "what", "doneWhen", "role", "touches"), Handle: t.createSubtask},
		{Name: "add_helper", Scope: mcp.ScopeProject, Input: object(map[string]any{
			"title": str("One line that names the helper's job, such as: Find every caller of the old function."),
		}, "title"), Handle: t.addHelper},
		{Name: "subtask_done", Scope: mcp.ScopeProject, Input: object(nil), Handle: t.subtaskDone},
		{Name: "decide", Scope: mcp.ScopeProject, Input: object(map[string]any{
			"question": str("The question the plan and the ticket do not settle."), "alternatives": strs("The alternatives you saw."),
			"choice": str("The one you chose."), "reason": str("Why.")}, "question", "alternatives", "choice", "reason"), Handle: t.decide},
	}, append(append(t.knowledgeTools(), t.crewTools()...), t.chatTools()...)...) {
		if err := s.Add(tl); err != nil {
			return err
		}
	}
	if err := t.registerFactoryTools(s); err != nil {
		return err
	}
	if err := t.registerSpecTools(s); err != nil {
		return err
	}
	if err := t.registerRoomTools(s); err != nil {
		return err
	}
	if err := t.registerM26Tools(s); err != nil {
		return err
	}
	return t.registerNormalConversation(s)
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

func (t *Tools) kick() {
	if t.Kick != nil {
		t.Kick()
	}
}

func (t *Tools) kickPlanner() {
	if t.KickPlanner != nil {
		t.KickPlanner()
	}
}

// who names an agent in the history: its role and its run (6.3).
func who(c mcp.Caller) string { return c.Role + " · " + c.Run }

// own is the caller's project and ticket, for a tool that writes to its own ticket.
func (t *Tools) own(ctx context.Context, c mcp.Caller) (*board.Project, *board.Ticket, error) {
	p, err := t.Board.Project(ctx, c.Project)
	if err != nil {
		return nil, nil, err
	}
	if c.Ticket == 0 {
		return p, nil, mcp.Errf("this run works on no ticket")
	}
	var key string
	if err := t.Pool.QueryRow(ctx, `SELECT p.key || '-' || t.number FROM tickets t JOIN projects p ON p.id = t.project
		WHERE t.id = $1 AND t.project = $2`, c.Ticket, p.ID).Scan(&key); err != nil {
		return nil, nil, err
	}
	tk, err := t.Board.Ticket(ctx, p, key)
	return p, tk, err
}

// subtaskOf is the kind and the commit of the caller's own subtask.
func (t *Tools) subtaskOf(ctx context.Context, c mcp.Caller) (kind, commit string, err error) {
	err = t.Pool.QueryRow(ctx, `SELECT kind, coalesce(commit, '') FROM subtasks WHERE id = $1`, c.Subtask).Scan(&kind, &commit)
	return
}

// report records what the caller's run reports with the call that ends its kind of run (11.3). Crew
// acts on it after the turn ends.
func (t *Tools) report(ctx context.Context, c mcp.Caller, r report) error {
	b, err := json.Marshal(r)
	if err != nil {
		return err
	}
	_, err = t.Pool.Exec(ctx, `UPDATE subtasks SET reported = $2 WHERE id = $1`, c.Subtask, b)
	t.kick()
	return err
}

func boardError(err error) error {
	var be *board.Error
	if errors.As(err, &be) {
		return mcp.Errf("%s", be.Msg)
	}
	return err
}

// reporter is a tool whose call only says a run's work is ready: request_move and fix_done.
func (t *Tools) reporter(kind, role, tool string) mcp.Handler {
	return func(ctx context.Context, c mcp.Caller, args json.RawMessage) (any, error) {
		var a struct{}
		if err := mcp.Decode(args, &a); err != nil {
			return nil, err
		}
		if _, _, err := t.own(ctx, c); err != nil {
			return nil, err
		}
		k, _, err := t.subtaskOf(ctx, c)
		if err != nil {
			return nil, err
		}
		if k != kind || c.Role != role {
			return nil, mcp.Errf("%s is for a %s's %s run; this run is a %s's %s", tool, role, kind, c.Role, k)
		}
		if err := t.report(ctx, c, report{Tool: tool}); err != nil {
			return nil, err
		}
		return map[string]any{"recorded": true, "next": "End your turn now. Crew runs the checks itself after your turn, and decides; " +
			"if something fails, you get it in this conversation."}, nil
	}
}

func (t *Tools) addNote(ctx context.Context, c mcp.Caller, args json.RawMessage) (any, error) {
	var a struct{ Text string }
	if err := mcp.Decode(args, &a); err != nil {
		return nil, err
	}
	if err := t.noSecret(ctx, c, a.Text); err != nil {
		return nil, err
	}
	p, tk, err := t.own(ctx, c)
	if err != nil {
		return nil, err
	}
	var notes int
	if err := t.Pool.QueryRow(ctx, `SELECT count(*) FROM records WHERE run = $1 AND kind = 'note'`, c.Run).Scan(&notes); err != nil {
		return nil, err
	}
	if notes >= maxNotes {
		return nil, mcp.Errf("this run wrote %d notes; write what is left in your last one", maxNotes)
	}
	r, err := t.Board.AddRecord(ctx, p, tk.Key, board.NewRecord{Kind: board.RecordNote, Author: c.Role, Run: c.Run, Subtask: c.Subtask, Body: a.Text})
	if err != nil {
		return nil, boardError(err)
	}
	t.kick()
	return map[string]any{"note": r.ID}, nil
}

// maxNotes is how many notes one run writes: enough for its work, and too few to push the reviews
// out of the next prompt (security review of M6).
const maxNotes = 20

func (t *Tools) replyComment(ctx context.Context, c mcp.Caller, args json.RawMessage) (any, error) {
	var a struct {
		ID   int64
		Text string
	}
	if err := mcp.Decode(args, &a); err != nil {
		return nil, err
	}
	if err := t.noSecret(ctx, c, a.Text); err != nil {
		return nil, err
	}
	p, err := t.Board.Project(ctx, c.Project)
	if err != nil {
		return nil, err
	}
	r, err := t.Board.Reply(ctx, p, c.Ticket, a.ID, a.Text, c.Role, c.Run, c.Subtask)
	if err != nil {
		return nil, boardError(err)
	}
	return map[string]any{"answer": r.ID, "resolved": a.ID}, nil
}

// entry writes a reviewer's or a tester's entry, about the commit its run judges, once per run.
func (t *Tools) entry(ctx context.Context, c mcp.Caller, n board.NewRecord, kind, role, tool string) (any, error) {
	p, tk, err := t.own(ctx, c)
	if err != nil {
		return nil, err
	}
	k, commit, err := t.subtaskOf(ctx, c)
	if err != nil {
		return nil, err
	}
	if k != kind || c.Role != role || commit == "" {
		return nil, mcp.Errf("%s is for a %s's %s run; this run is a %s's %s", tool, role, kind, c.Role, k)
	}
	var n0 int
	if err := t.Pool.QueryRow(ctx, `SELECT count(*) FROM records WHERE run = $1 AND kind = $2`, c.Run, n.Kind).Scan(&n0); err != nil {
		return nil, err
	}
	if n0 > 0 {
		return nil, mcp.Errf("this run wrote its %s already: one per run", n.Kind)
	}
	n.Author, n.Run, n.Subtask, n.Commit = c.Role, c.Run, c.Subtask, commit
	var secrets []string
	if t.Secrets != nil {
		secrets = t.Secrets(ctx, c.Run)
	}
	n.Body = redact.New(secrets...).Text(n.Body)
	r, err := t.Board.AddRecord(ctx, p, tk.Key, n)
	var pe *pgconn.PgError
	if errors.As(err, &pe) && pe.Code == "23505" {
		return nil, mcp.Errf("this run wrote its %s already: one per run", n.Kind)
	}
	if err != nil {
		return nil, boardError(err)
	}
	if err := t.report(ctx, c, report{Tool: tool}); err != nil {
		return nil, err
	}
	return map[string]any{"recorded": r.ID, "about": commit, "next": "End your turn now."}, nil
}

func (t *Tools) writeReview(ctx context.Context, c mcp.Caller, args json.RawMessage) (any, error) {
	var a struct {
		Verdict, Text string
		Tests         []struct{ Path, Why string }
	}
	if err := mcp.Decode(args, &a); err != nil {
		return nil, err
	}
	// An approve says why each test file the change touches is right (7.1, 21): a builder can weaken its
	// own test, and the reviewer is who reads it.
	if a.Verdict == board.VerdictApprove && c.Role == "reviewer" {
		p, tk, err := t.own(ctx, c)
		if err != nil {
			return nil, err
		}
		_, commit, err := t.subtaskOf(ctx, c)
		if err != nil {
			return nil, err
		}
		if commit != "" {
			files, err := t.Board.TestFiles(ctx, p, tk, commit)
			if err != nil {
				return nil, err
			}
			why := map[string]string{}
			for _, x := range a.Tests {
				why[x.Path] = strings.TrimSpace(x.Why)
			}
			var missing []string
			var lines []string
			for _, f := range files {
				if why[f.Path] == "" {
					missing = append(missing, f.Path)
					continue
				}
				lines = append(lines, "- "+f.Path+": "+why[f.Path])
			}
			if len(missing) > 0 {
				return nil, mcp.Errf("an approve gives, in tests, why each test file the change touches is right; these have none: %s", strings.Join(missing, ", "))
			}
			if len(lines) > 0 {
				a.Text += "\n\nWhy each test file the change touches is right:\n" + strings.Join(lines, "\n")
			}
		}
	}
	return t.entry(ctx, c, board.NewRecord{Kind: board.RecordReview, Verdict: a.Verdict, Body: a.Text}, kindReview, "reviewer", "write_review")
}

func (t *Tools) addTestResult(ctx context.Context, c mcp.Caller, args json.RawMessage) (any, error) {
	var a struct{ Result, Text string }
	if err := mcp.Decode(args, &a); err != nil {
		return nil, err
	}
	return t.entry(ctx, c, board.NewRecord{Kind: board.RecordTest, Result: a.Result, Body: a.Text}, kindTest, "tester", "add_test_result")
}

// maxShots and maxShot bound the screenshots of one E2E entry.
const (
	maxShots = 10
	maxShot  = 2 << 20
)

// addE2EResult records the E2E tester's entry with its screenshots, read from its run's home and
// kept only when they are PNG or JPEG images (7.4). What it says decides nothing: Crew's own run of
// the end-to-end tests does (13.9).
func (t *Tools) addE2EResult(ctx context.Context, c mcp.Caller, args json.RawMessage) (any, error) {
	var a struct {
		Result, Text string
		Screenshots  []string
	}
	if err := mcp.Decode(args, &a); err != nil {
		return nil, err
	}
	if len(a.Screenshots) > maxShots {
		return nil, mcp.Errf("screenshots: at most %d", maxShots)
	}
	// Only the E2E tester's run reads its home, and only once it may write its entry (review of M10).
	if c.Role != "e2e" {
		return nil, mcp.Errf("add_e2e_result is for the E2E tester's run; this run is a %s's", c.Role)
	}
	if k, _, err := t.subtaskOf(ctx, c); err != nil {
		return nil, err
	} else if k != kindE2E {
		return nil, mcp.Errf("add_e2e_result is for an E2E run; this run is a %s", k)
	}
	var files []board.RecordFile
	if len(a.Screenshots) > 0 {
		if t.Home == nil {
			return nil, mcp.Errf("screenshots: this Crew keeps none; leave them out")
		}
		home, err := t.Home(ctx, c.Run)
		if err != nil {
			return nil, err
		}
		seen := map[string]bool{}
		for _, p := range a.Screenshots {
			if err := homepath.Valid(p); err != nil {
				return nil, mcp.Errf("screenshots: %s", err)
			}
			data, err := homepath.ReadFile(home, p, maxShot)
			if err != nil {
				return nil, mcp.Errf("screenshots: %s cannot be read from your home: %v", p, err)
			}
			typ := http.DetectContentType(data)
			if typ != "image/png" && typ != "image/jpeg" {
				return nil, mcp.Errf("screenshots: %s is not a PNG or a JPEG image", p)
			}
			name := path.Base(p)
			if !shotName.MatchString(name) || seen[name] {
				return nil, mcp.Errf("screenshots: %s needs a name of its own of letters, digits, '.', '_', and '-'", p)
			}
			seen[name] = true
			files = append(files, board.RecordFile{Name: name, Type: typ, Data: data})
		}
	}
	out, err := t.entry(ctx, c, board.NewRecord{Kind: board.RecordE2E, Result: a.Result, Body: a.Text, Files: files}, kindE2E, "e2e", "add_e2e_result")
	return out, err
}

var shotName = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,99}$`)

// addStageVerdict records the verdict of the agent of a project's own stage, about that stage (7.4).
func (t *Tools) addStageVerdict(ctx context.Context, c mcp.Caller, args json.RawMessage) (any, error) {
	var a struct{ Result, Text string }
	if err := mcp.Decode(args, &a); err != nil {
		return nil, err
	}
	_, tk, err := t.own(ctx, c)
	if err != nil {
		return nil, err
	}
	return t.entry(ctx, c, board.NewRecord{Kind: board.RecordStage, Result: a.Result, Body: a.Text, Stage: tk.Stage}, kindStage, "stage", "add_stage_verdict")
}

// addVerification records the verifier's entry: whether the change does what the goal and the source
// plan ask (7.4).
func (t *Tools) addVerification(ctx context.Context, c mcp.Caller, args json.RawMessage) (any, error) {
	var a struct{ Result, Text string }
	if err := mcp.Decode(args, &a); err != nil {
		return nil, err
	}
	return t.entry(ctx, c, board.NewRecord{Kind: board.RecordVerify, Result: a.Result, Body: a.Text}, kindVerify, "verifier", "add_verification")
}

// analyseTicket records the decision of an analysis run. It is tried against the board at once, so a
// decision the board would refuse is told now; Crew applies it after the turn, once its command checks
// failed on main (7.6, M6).
func (t *Tools) analyseTicket(ctx context.Context, c mcp.Caller, args json.RawMessage) (any, error) {
	var d board.Decision
	if err := mcp.Decode(args, &d); err != nil {
		return nil, err
	}
	p, tk, err := t.own(ctx, c)
	if err != nil {
		return nil, err
	}
	k, _, err := t.subtaskOf(ctx, c)
	if err != nil {
		return nil, err
	}
	if k != kindAnalysis {
		return nil, mcp.Errf("analyse_ticket is for a run that analyses a ticket; this run is a %s", k)
	}
	if strings.HasSuffix(tk.CreatedBy, c.Run) {
		return nil, mcp.Errf("%s was created by this run: another agent analyses it (7.6)", tk.Key)
	}
	if d.Choice == "approve" || d.Choice == "notice" {
		checks := d.Checks
		if checks == nil {
			checks = tk.Checks
		}
		own := 0
		for _, ch := range checks {
			if ch.Kind == "cmd" && !ch.Keeps {
				own++
			}
		}
		if own == 0 {
			return nil, mcp.Errf("a ticket needs at least one command check that is not keeps: a command that fails until its goal is met")
		}
	}
	if err := t.Board.TryDecide(ctx, p, tk.Key, d, who(c)); err != nil {
		return nil, boardError(err)
	}
	if err := t.report(ctx, c, report{Tool: "analyse_ticket", Decision: &d}); err != nil {
		return nil, err
	}
	return map[string]any{"recorded": d.Choice, "next": "End your turn now. Crew runs the command checks on main first: a check " +
		"that passes there before the work is refused, and you get it back. Then it applies your decision."}, nil
}

// Limits of the subtasks one run asks for: enough to split a ticket, and too few for a run that a text
// it read turned against Crew to fill the ticket.
const (
	maxTouches = 50
	maxHelpers = 20
)

// createSubtask queues a subtask on the caller's own ticket (7.5, R151): the lead while it analyses
// the ticket, which plans its build in parts, or the builder while it builds. It takes the ticket's
// why, what, and goal, is not analysed, and counts toward limits.max_added_subtasks. It runs on a branch of its own, in parallel with the others where its
// after and its touches allow (7.11).
func (t *Tools) createSubtask(ctx context.Context, c mcp.Caller, args json.RawMessage) (any, error) {
	var a struct {
		Title, What, DoneWhen, Check, Role string
		Touches                            []string
		After                              []int
	}
	if err := mcp.Decode(args, &a); err != nil {
		return nil, err
	}
	p, tk, err := t.own(ctx, c)
	if err != nil {
		return nil, err
	}
	kind, _, err := t.subtaskOf(ctx, c)
	if err != nil {
		return nil, err
	}
	switch {
	case (c.Role == "curator" || c.Role == "lead") && kind == kindAnalysis && tk.Stage == settings.StageAnalysis:
	case c.Role == "builder" && (kind == kindBuild || kind == kindFix || kind == kindPart || kind == kindJoin):
	default:
		return nil, mcp.Errf("create_subtask is for the curator while it analyses its ticket, and for the builder while it builds it; this run is a %s's %s", c.Role, kind)
	}
	if tk.Stage == settings.StageDone || tk.Stage == settings.StageClosed {
		return nil, mcp.Errf("%s is %s", tk.Key, tk.Stage)
	}
	a.Title, a.Check = strings.TrimSpace(a.Title), strings.TrimSpace(a.Check)
	switch {
	case a.Title == "" || utf8.RuneCountInString(a.Title) > 200 || strings.ContainsAny(a.Title, "\r\n"):
		return nil, mcp.Errf("title: one line of at most 200 characters")
	case strings.TrimSpace(a.What) == "" || strings.TrimSpace(a.DoneWhen) == "" || len(a.What) > 20000 || len(a.DoneWhen) > 4000:
		return nil, mcp.Errf("what and doneWhen: say what to do, and what done looks like")
	case len(a.Check) > 2000 || strings.ContainsAny(a.Check, "\r\n\x00"):
		return nil, mcp.Errf("check: one command of at most 2000 characters")
	case a.Role != "builder" && a.Role != "tester":
		return nil, mcp.Errf("role: builder or tester; the E2E tester comes with M10")
	case len(a.Touches) == 0 || len(a.Touches) > maxTouches:
		return nil, mcp.Errf("touches: name the files or folders it changes, 1 to %d; a folder ends with /", maxTouches)
	}
	for _, f := range a.Touches {
		f = strings.TrimSpace(f)
		if f == "" || len(f) > 200 || strings.HasPrefix(f, "/") || strings.Contains(f, "..") || strings.ContainsAny(f, "\r\n\x00*?") {
			return nil, mcp.Errf("touches: %q is not a file or a folder of the repository", f)
		}
	}
	doc, err := t.Board.Settings(ctx, t.Pool, p)
	if err != nil {
		return nil, err
	}
	var key string
	err = pgx.BeginFunc(ctx, t.Pool, func(tx pgx.Tx) error {
		// The ticket is locked, so two calls count the same subtasks one after the other.
		if err := board.LockForSubtasks(ctx, tx, tk.ID); err != nil {
			return err
		}
		var added int
		if err := tx.QueryRow(ctx, `SELECT count(*) FROM subtasks WHERE ticket = $1 AND added`, tk.ID).Scan(&added); err != nil {
			return err
		}
		if err := board.LimitsOf(doc).MayAddSubtask(added); err != nil {
			return mcp.Errf("%s", err.Error())
		}
		after := []int64{}
		for _, n := range a.After {
			var id int64
			err := tx.QueryRow(ctx, `SELECT id FROM subtasks WHERE ticket = $1 AND number = $2 AND kind = $3`, tk.ID, n, kindPart).Scan(&id)
			if errors.Is(err, pgx.ErrNoRows) {
				return mcp.Errf("after: %s.%d is not a subtask agents created on this ticket", tk.Key, n)
			}
			if err != nil {
				return err
			}
			after = append(after, id)
		}
		touches := append([]string{}, a.Touches...)
		var number int
		var check *string
		if a.Check != "" {
			check = &a.Check
		}
		if err := tx.QueryRow(ctx, `INSERT INTO subtasks (scope, project, ticket, number, kind, role, state, title, what, done_when, check_cmd,
				touches, after, created_by, created_run, added)
			VALUES ('ticket', $1, $2, (SELECT coalesce(max(number), 0) + 1 FROM subtasks WHERE ticket = $2), $3, $4, 'queued', $5, $6, $7, $8,
				$9, $10, $11, $12, true) RETURNING number`,
			p.ID, tk.ID, kindPart, a.Role, a.Title, a.What, a.DoneWhen, check, touches, after, who(c), c.Run).Scan(&number); err != nil {
			return err
		}
		key = fmt.Sprintf("%s.%d", tk.Key, number)
		return nil
	})
	if err != nil {
		return nil, err
	}
	t.kick()
	next := "It is queued, and runs in a sandbox of its own on its own branch, beside the others where they touch other files. "
	if c.Role == "builder" {
		next += "Do not do its work here: carry on with your own part, and do not change the files it touches."
	} else {
		next += "It runs when the ticket is In progress."
	}
	return map[string]any{"subtask": key, "next": next}, nil
}

// addHelper records a helper the run starts with its CLI's own subagent feature, in the same sandbox
// (7.11): a subtask under the run's own, which ends with the parent's turn.
func (t *Tools) addHelper(ctx context.Context, c mcp.Caller, args json.RawMessage) (any, error) {
	var a struct{ Title string }
	if err := mcp.Decode(args, &a); err != nil {
		return nil, err
	}
	a.Title = strings.TrimSpace(a.Title)
	if a.Title == "" || utf8.RuneCountInString(a.Title) > 200 || strings.ContainsAny(a.Title, "\r\n") {
		return nil, mcp.Errf("title: one line of at most 200 characters")
	}
	if c.Ticket == 0 {
		return nil, mcp.Errf("this run works on no ticket")
	}
	var n int
	if err := t.Pool.QueryRow(ctx, `SELECT count(*) FROM subtasks WHERE parent = $1 AND kind = 'helper'`, c.Subtask).Scan(&n); err != nil {
		return nil, err
	}
	if n >= maxHelpers {
		return nil, mcp.Errf("this subtask has %d helpers; carry on without more", maxHelpers)
	}
	var number int
	err := pgx.BeginFunc(ctx, t.Pool, func(tx pgx.Tx) error {
		if err := board.LockForSubtasks(ctx, tx, c.Ticket); err != nil {
			return err
		}
		return tx.QueryRow(ctx, `INSERT INTO subtasks (scope, project, ticket, number, kind, role, state, title, parent, created_by, created_run)
			VALUES ('ticket', $1, $2, (SELECT coalesce(max(number), 0) + 1 FROM subtasks WHERE ticket = $2), 'helper', 'helper', 'running', $3, $4, $5, $6)
			RETURNING number`, c.Project, c.Ticket, a.Title, c.Subtask, who(c), c.Run).Scan(&number)
	})
	if err != nil {
		return nil, err
	}
	return map[string]any{"helper": number, "next": "Start the helper with your CLI's own subagent feature; it ends with your turn."}, nil
}

// subtaskDone says a parallel subtask, or a join, is ready: Crew runs its check on its branch and joins
// it into the ticket's branch (7.11).
func (t *Tools) subtaskDone(ctx context.Context, c mcp.Caller, args json.RawMessage) (any, error) {
	var a struct{}
	if err := mcp.Decode(args, &a); err != nil {
		return nil, err
	}
	if _, _, err := t.own(ctx, c); err != nil {
		return nil, err
	}
	k, _, err := t.subtaskOf(ctx, c)
	if err != nil {
		return nil, err
	}
	if k != kindPart && k != kindJoin {
		return nil, mcp.Errf("subtask_done is for a subtask an agent created, or a join; this run is a %s's %s: use %s", c.Role, k, toolOf[k])
	}
	if err := t.report(ctx, c, report{Tool: "subtask_done"}); err != nil {
		return nil, err
	}
	return map[string]any{"recorded": true, "next": "End your turn now. Crew runs this subtask's check on its branch, and joins it into " +
		"the ticket's branch; if something fails, you get it in this conversation."}, nil
}

// maxCreatedPerRun is how many tickets one run creates: enough to note what it saw, and too few for a
// run that a text it read turned against Crew to fill the board.
const maxCreatedPerRun = 10

// createTicket adds a ticket an agent saw the need for to the Backlog (7.5, R27): with what it saw,
// why, the role and the run that created it, and a link to the ticket that run works on. Another
// agent analyses it before anything is built; the creating run carries on with its own ticket.
func (t *Tools) createTicket(ctx context.Context, c mcp.Caller, args json.RawMessage) (any, error) {
	var a struct {
		Title, Seen, Why, Kind, What, Goal string
		Checks                             []board.CheckSpec
	}
	if err := mcp.Decode(args, &a); err != nil {
		return nil, err
	}
	if strings.TrimSpace(a.Seen) == "" || strings.TrimSpace(a.Why) == "" {
		return nil, mcp.Errf("seen and why: say what you saw and why the ticket is needed")
	}
	p, err := t.Board.Project(ctx, c.Project)
	if err != nil {
		return nil, err
	}
	if p.State != "running" && p.State != "done" {
		return nil, mcp.Errf("%s is %s: before the OK the planner proposes tickets with propose_tickets", p.Name, p.State)
	}
	// The assistant of a conversation proposes the ticket: it is the operator's once they adopt it,
	// and it carries the conversation and the run, so a turn's tickets are known (11.8; plan M20 D19).
	origin, n0 := board.OriginAgent, board.NewTicket{Title: a.Title, Kind: a.Kind, Why: a.Why, What: a.What, Goal: a.Goal, Checks: a.Checks}
	if c.Role == "assistant" {
		if c.Conversation == "" || c.ChatTicket != 0 {
			return nil, mcp.Errf("create_ticket is the assistant's in a conversation with the operator only")
		}
		origin, n0.Conversation, n0.ConversationRun = board.OriginConversation, c.Conversation, c.Run
	}
	seen := a.Seen
	n0.Seen = &seen
	// The count and the ticket it allows are one step, so calls at once cannot pass the cap together.
	t.caps.Lock()
	defer t.caps.Unlock()
	var n int
	if err := t.Pool.QueryRow(ctx, `SELECT count(*) FROM tickets WHERE project = $1 AND origin IN ('agent', 'conversation') AND created_by = $2`,
		p.ID, who(c)).Scan(&n); err != nil {
		return nil, err
	}
	if n >= maxCreatedPerRun {
		if origin == board.OriginConversation {
			return nil, mcp.Errf("this sandbox created %d tickets already; say what else you would propose in your answer", maxCreatedPerRun)
		}
		return nil, mcp.Errf("this run created %d tickets already; say what else you saw in a note", maxCreatedPerRun)
	}
	tk, err := t.Board.AddTicket(ctx, p.ID, n0, origin, who(c))
	if err != nil {
		return nil, boardError(err)
	}
	from := ""
	if c.Ticket != 0 {
		if _, own, err := t.own(ctx, c); err == nil {
			from = own.Key
			if err := t.Board.Link(ctx, p, tk.Key, own.Key, "created_from", who(c)); err != nil {
				return nil, boardError(err)
			}
		}
	}
	board.RecordSignal(ctx, t.Pool, board.Signal{Project: p.ID, Ticket: c.Ticket, Run: c.Run, Kind: "ticket_created",
		Detail: map[string]any{"ticket": tk.Key}})
	t.kick()
	if origin == board.OriginConversation {
		return map[string]any{"ticket": tk.Key, "stage": tk.Stage, "next": "It is a proposal: the operator makes it theirs with Make a ticket " +
			"of this, and until then another agent analyses it like any ticket an agent created. Say in your answer which ticket you created and why."}, nil
	}
	out := map[string]any{"ticket": tk.Key, "stage": tk.Stage, "next": "Another agent analyses it before it is built. Do not do its work " +
		"here: carry on with your own ticket."}
	if from != "" {
		out["createdFrom"] = from
	}
	return out, nil
}

// maxLinksPerRun is how many links one run adds.
const maxLinksPerRun = 20

// linkTickets links a ticket of the run's to another of its project (11.5): from its own ticket, or
// from a ticket the run created, never from another, and never from one that is done or closed; an
// agent does not change another ticket (7.12, review of M9). A link that would close a circle of
// dependencies is refused by the board.
func (t *Tools) linkTickets(ctx context.Context, c mcp.Caller, args json.RawMessage) (any, error) {
	var a struct{ From, To, Kind string }
	if err := mcp.Decode(args, &a); err != nil {
		return nil, err
	}
	if !slices.Contains([]string{"depends_on", "duplicates", "created_from"}, a.Kind) {
		return nil, mcp.Errf("kind: depends_on, duplicates, or created_from")
	}
	p, err := t.Board.Project(ctx, c.Project)
	if err != nil {
		return nil, err
	}
	from, err := t.Board.Ticket(ctx, p, a.From)
	if err != nil {
		return nil, boardError(err)
	}
	if from.ID != c.Ticket && from.CreatedBy != who(c) {
		return nil, mcp.Errf("%s is not your ticket, nor one this run created: link from one of those", from.Key)
	}
	if from.Stage == settings.StageDone || from.Stage == settings.StageClosed {
		return nil, mcp.Errf("%s is %s", from.Key, from.Stage)
	}
	t.caps.Lock()
	defer t.caps.Unlock()
	var links int
	if err := t.Pool.QueryRow(ctx, `SELECT count(*) FROM changes WHERE who = $1 AND tbl = 'tickets' AND before ? 'links'`, who(c)).Scan(&links); err != nil {
		return nil, err
	}
	if links >= maxLinksPerRun {
		return nil, mcp.Errf("this run added %d links; carry on without more", maxLinksPerRun)
	}
	if err := t.Board.Link(ctx, p, a.From, a.To, a.Kind, who(c)); err != nil {
		return nil, boardError(err)
	}
	t.kick()
	return map[string]any{"linked": a.From + " " + a.Kind + " " + a.To}, nil
}

func (t *Tools) decide(ctx context.Context, c mcp.Caller, args json.RawMessage) (any, error) {
	var a struct {
		Question, Choice, Reason string
		Alternatives             []string
	}
	if err := mcp.Decode(args, &a); err != nil {
		return nil, err
	}
	for name, v := range map[string]string{"question": a.Question, "choice": a.Choice, "reason": a.Reason} {
		if strings.TrimSpace(v) == "" || utf8.RuneCountInString(v) > 4000 {
			return nil, mcp.Errf("%s: 1 to 4000 characters", name)
		}
	}
	if len(a.Alternatives) > 20 {
		return nil, mcp.Errf("alternatives: at most 20")
	}
	var n int
	if err := t.Pool.QueryRow(ctx, `SELECT count(*) FROM decisions WHERE run = $1`, c.Run).Scan(&n); err != nil {
		return nil, err
	}
	if n >= 50 {
		return nil, mcp.Errf("this run recorded 50 decisions; carry on without more")
	}
	alts, _ := json.Marshal(append([]string{}, a.Alternatives...))
	var id int64
	err := t.Pool.QueryRow(ctx, `INSERT INTO decisions (project, ticket, run, role, question, alternatives, choice, reason)
		VALUES ($1, nullif($2, 0), $3, $4, $5, $6, $7, $8) RETURNING id`, c.Project, c.Ticket, c.Run, c.Role, a.Question, alts, a.Choice, a.Reason).Scan(&id)
	if err != nil {
		return nil, err
	}
	board.RecordSignal(ctx, t.Pool, board.Signal{Project: c.Project, Ticket: c.Ticket, Run: c.Run, Kind: "decided_alone", Detail: map[string]any{"decision": id}})
	return map[string]any{"decision": id, "next": "Carry on with your choice."}, nil
}

func (t *Tools) setSteps(ctx context.Context, c mcp.Caller, args json.RawMessage) (any, error) {
	var a struct{ Steps []string }
	if err := mcp.Decode(args, &a); err != nil {
		return nil, err
	}
	if len(a.Steps) == 0 || len(a.Steps) > 30 {
		return nil, mcp.Errf("steps: 1 to 30")
	}
	for _, s := range a.Steps {
		if strings.TrimSpace(s) == "" || utf8.RuneCountInString(s) > 200 || strings.ContainsAny(s, "\n\r") {
			return nil, mcp.Errf("steps: each one line of at most 200 characters")
		}
	}
	err := pgx.BeginFunc(ctx, t.Pool, func(tx pgx.Tx) error {
		var done int
		if err := tx.QueryRow(ctx, `SELECT coalesce(max(position), 0) FROM subtask_steps WHERE subtask = $1 AND done_at IS NOT NULL`,
			c.Subtask).Scan(&done); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `DELETE FROM subtask_steps WHERE subtask = $1 AND position > $2`, c.Subtask, done); err != nil {
			return err
		}
		for i, s := range a.Steps {
			if _, err := tx.Exec(ctx, `INSERT INTO subtask_steps (subtask, position, text) VALUES ($1, $2, $3)`, c.Subtask, done+i+1, strings.TrimSpace(s)); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return t.steps(ctx, c)
}

func (t *Tools) tickStep(ctx context.Context, c mcp.Caller, args json.RawMessage) (any, error) {
	var a struct{ Step int }
	if err := mcp.Decode(args, &a); err != nil {
		return nil, err
	}
	tag, err := t.Pool.Exec(ctx, `UPDATE subtask_steps SET done_at = coalesce(done_at, $3) WHERE subtask = $1 AND position = $2`,
		c.Subtask, a.Step, time.Now())
	if err != nil {
		return nil, err
	}
	if tag.RowsAffected() == 0 {
		return nil, mcp.Errf("there is no step %d", a.Step)
	}
	return t.steps(ctx, c)
}

func (t *Tools) steps(ctx context.Context, c mcp.Caller) (any, error) {
	rows, err := t.Pool.Query(ctx, `SELECT position, text, done_at IS NOT NULL FROM subtask_steps WHERE subtask = $1 ORDER BY position`, c.Subtask)
	if err != nil {
		return nil, err
	}
	type step struct {
		Step int    `json:"step"`
		Text string `json:"text"`
		Done bool   `json:"done"`
	}
	list, err := pgx.CollectRows(rows, func(r pgx.CollectableRow) (step, error) {
		var s step
		return s, r.Scan(&s.Step, &s.Text, &s.Done)
	})
	return map[string]any{"steps": list}, err
}

// summaryLine is a summary as an agent reads it.
type summaryLine struct {
	Ticket  string         `json:"ticket"`
	Title   string         `json:"title"`
	Outcome string         `json:"outcome"`
	Done    string         `json:"whatWasDone,omitempty"`
	How     string         `json:"howItWent,omitempty"`
	Next    string         `json:"whatToKnowNextTime,omitempty"`
	Facts   map[string]any `json:"facts,omitempty"`
}

const summaryCols = `p.key || '-' || t.number, t.title, s.outcome, coalesce(s.done, ''), coalesce(s.how, ''), coalesce(s.next, '')`

func (t *Tools) getSummary(ctx context.Context, c mcp.Caller, args json.RawMessage) (any, error) {
	var a struct{ Key string }
	if err := mcp.Decode(args, &a); err != nil {
		return nil, err
	}
	project := c.Project
	// The improver reads a summary of any project, by its ticket's number (10.4).
	if c.Crew {
		key, _, _ := strings.Cut(a.Key, "-")
		if err := t.Pool.QueryRow(ctx, `SELECT p.id::text FROM projects p WHERE p.key = $1`, key).Scan(&project); err != nil {
			return nil, mcp.Errf("%s names no project's ticket", a.Key)
		}
	}
	p, err := t.Board.Project(ctx, project)
	if err != nil {
		return nil, err
	}
	n, err := board.ParseNumber(p, a.Key)
	if err != nil {
		return nil, boardError(err)
	}
	var l summaryLine
	err = t.Pool.QueryRow(ctx, `SELECT `+summaryCols+`, s.facts FROM summaries s JOIN tickets t ON t.id = s.ticket
		JOIN projects p ON p.id = t.project WHERE t.project = $1 AND t.number = $2`, p.ID, n).Scan(&l.Ticket, &l.Title, &l.Outcome, &l.Done, &l.How, &l.Next, &l.Facts)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, mcp.Errf("%s has no summary: it is not finished, or its summary is not written yet", a.Key)
	}
	return l, err
}

func (t *Tools) searchSummaries(ctx context.Context, c mcp.Caller, args json.RawMessage) (any, error) {
	var a struct{ Query, Project string }
	if err := mcp.Decode(args, &a); err != nil {
		return nil, err
	}
	if strings.TrimSpace(a.Query) == "" || len(a.Query) > 200 {
		return nil, mcp.Errf("query: 1 to 200 characters")
	}
	// A project's run searches its own project, whatever it names; the improver every project, or the
	// one it names (10.4).
	where, arg := `s.project = $1`, any(c.Project)
	if c.Crew {
		where, arg = `($1 = '' OR p.name = $1)`, a.Project
	}
	rows, err := t.Pool.Query(ctx, `SELECT `+summaryCols+` FROM summaries s JOIN tickets t ON t.id = s.ticket JOIN projects p ON p.id = t.project
		WHERE `+where+` AND (s.search @@ websearch_to_tsquery('simple', $2) OR t.title ILIKE '%' || $2 || '%')
		ORDER BY s.created_at DESC LIMIT 20`, arg, a.Query)
	if err != nil {
		return nil, err
	}
	list, err := pgx.CollectRows(rows, func(r pgx.CollectableRow) (summaryLine, error) {
		var l summaryLine
		return l, r.Scan(&l.Ticket, &l.Title, &l.Outcome, &l.Done, &l.How, &l.Next)
	})
	if err != nil {
		return nil, err
	}
	return map[string]any{"summaries": list, "count": len(list), "note": fmt.Sprintf("the newest %d at most", 20)}, nil
}
