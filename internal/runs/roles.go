package runs

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/kesokaj/crew/internal/board"
	"github.com/kesokaj/crew/internal/capacity"
	"github.com/kesokaj/crew/internal/cli"
	"github.com/kesokaj/crew/internal/config"
	"github.com/kesokaj/crew/internal/gitendpoint"
	"github.com/kesokaj/crew/internal/harness"
	"github.com/kesokaj/crew/internal/hold"
	"github.com/kesokaj/crew/internal/mcp"
	"github.com/kesokaj/crew/internal/sandboxes"
	"github.com/kesokaj/crew/internal/settings"
)

// buildRoles are the roles whose runs build: every other run gets a workspace without the files a CLI
// loads from a repository by itself (13.8), since what it reads there is data.
var buildRoles = map[string]bool{"builder": true}

// mcpRoles are the roles whose runs get the Crew MCP (11.5). The summary writer is a judge, with no
// tools.
var mcpRoles = map[string]bool{"planner": true, "coordinator": true, "researcher": true, "dreamer": true, "curator": true, "lead": true, "builder": true, "reviewer": true, "security": true, "tester": true, "e2e": true, "verifier": true, "stage": true,
	"improver": true, "assistant": true}

// dockerRoles are the roles whose microVM has Docker inside: builders and testers run the project's
// services, and the assistant of a conversation or a chat builds and runs them to find out (11.8,
// 11.9); the lead and reviewers start without it and boot faster (13.3).
var dockerRoles = map[string]bool{"builder": true, "tester": true, "e2e": true, "assistant": true}

// setupRoles are the roles whose new sandbox runs the project's setup before the CLI: the ones that
// build or run the project's code (11.2, since 0.47). The roles that only read do not.
var setupRoles = map[string]bool{"builder": true, "tester": true, "e2e": true}

// RoleRun is a run of an agent on a plan draft, on a project, or on one of its tickets (11.2): the
// planning agent, the lead, and from M6 the agents of a ticket's stages and its summary writer.
type RoleRun struct {
	Role, Kind, Title string
	// Project is the project's id, or Draft the plan draft's; never both.
	Project, Draft string
	// Ticket is the ticket's id for a run on a ticket: its subtask is numbered under the ticket.
	Ticket int64
	// Branch is the branch the run clones: the ticket's, which its builder pushes. "" is main.
	Branch string
	// Base is where Branch starts when it is not pushed yet, and a branch the run may read: the
	// ticket's branch for a parallel subtask (7.11). "" is main.
	Base string
	// Subtask is a queued subtask of the ticket this run works on, which an agent created (7.5); 0 is a
	// new subtask.
	Subtask int64
	// ReadOnly says the run reads its branch and pushes nothing (6.1): the lead, a reviewer, a tester.
	ReadOnly bool
	// Judge starts the run in a judge sandbox: one call with no tools, no repository, no harness (13.3).
	Judge bool
	// Continues is a run whose conversation this run continues, in a new subtask: a builder's fix
	// round continues its build (12.10). "" is a new conversation.
	Continues string
	// Model is the model as its CLI lists it, without the effort, and Name what the CLI is given:
	// for agy the name that holds the effort, such as gemini-3.8-flash-high (12.3). "" is Model.
	CLI, Model, Name, Effort string
	// From and EffortFrom say where the model and the effort came from (12.3), such as "list".
	From, EffortFrom string
	Parts            []PromptPart
	Timeout          time.Duration
	// MaxTurns is the CLI's own limit of tool steps.
	MaxTurns int
	// Record runs in the transaction that records the run, so what the run takes is marked taken
	// exactly when the run exists, such as the operator's messages it is given.
	Record func(ctx context.Context, tx pgx.Tx, run string) error
	// Owner is the loop that starts the run, which takes its own asks for room out of the queue when
	// it no longer asks (capacity.Gate.Sweep); "" leaves them to go after a minute.
	Owner string
	// Eval says the run is one of an eval of the project (9.6): its subtask has the scope eval, it gets
	// no Crew MCP and not main's instructions, and it reads only its own branch, so it cannot see the
	// work it replays.
	Eval bool
	// Image is the runtime image an eval's run starts from, by its id: the candidate's, for a release's
	// suite (16.2 step 5, since 0.52). Its subtask keeps it, so a run that continues the subtask starts
	// from it too. Only an eval's run names one.
	Image string
	// Improver says the run is the improver's (10.4): no project and no draft, the subtask scope
	// improver, its own sandbox, Model only, and the Crew MCP with the Crew level's tools only.
	Improver bool
	// Conversation is a conversation of the operator's or a ticket's chat, its row made by
	// internal/assistant, whose first turn this run takes (11.8, 11.9): the run is the assistant's,
	// read-only, of a new subtask of the scope conversation, with the conversation's project or none.
	Conversation string
	// AllowMCP and ForceDocker override default role maps for custom WORKFLOW.md specialist agents (11.10).
	AllowMCP    bool
	ForceDocker bool
}

// StartRole starts a run of a role with a new subtask and conversation. It returns the run and its
// subtask.
func (d *Driver) StartRole(ctx context.Context, rr RoleRun) (string, int64, error) {
	a, ok := cli.Get(rr.CLI)
	if !ok {
		return "", 0, fmt.Errorf("no CLI %q", rr.CLI)
	}
	if rr.Image != "" && !rr.Eval {
		return "", 0, errors.New("only an eval's run starts from an image it names")
	}
	var convProject string
	switch {
	case rr.Improver:
		if rr.Project != "" || rr.Draft != "" || rr.Ticket != 0 || rr.Role != "improver" || rr.Kind == "" || len(rr.Parts) == 0 || rr.Judge || rr.Eval ||
			rr.Conversation != "" {
			return "", 0, errors.New("the improver's run names no project and no draft, and is the improver's")
		}
	case rr.Conversation != "":
		// The conversation's row says its project, or none; its first turn makes its subtask (D2).
		if rr.Role != "assistant" || rr.Project != "" || rr.Draft != "" || rr.Ticket != 0 || rr.Subtask != 0 || rr.Branch != "" || rr.Base != "" ||
			rr.Kind == "" || len(rr.Parts) == 0 || !rr.ReadOnly || rr.Judge || rr.Eval || rr.Continues != "" {
			return "", 0, errors.New("a conversation's run is the assistant's, reads only, and names nothing but its conversation")
		}
		if err := d.Pool.QueryRow(ctx, `SELECT coalesce(project::text, '') FROM conversations
			WHERE id = $1 AND purpose IN ('operator', 'chat') AND subtask IS NULL`, rr.Conversation).Scan(&convProject); err != nil {
			return "", 0, fmt.Errorf("the conversation %s, which no turn took yet: %w", rr.Conversation, err)
		}
	case (rr.Project == "") == (rr.Draft == "") || rr.Role == "" || rr.Kind == "" || len(rr.Parts) == 0 || (rr.Ticket != 0 && rr.Project == "") ||
		rr.Role == "improver" || rr.Role == "assistant":
		return "", 0, errors.New("a run of a role names its role, its kind, its prompt, and one project or one plan draft")
	}
	if !rr.ReadOnly && !rr.Judge && rr.Branch == "" && rr.Ticket != 0 {
		return "", 0, errors.New("a run that pushes on a ticket names the ticket's branch")
	}
	if !d.signedIn(ctx, a.Name()) {
		return "", 0, ErrNotSignedIn
	}
	if why := hold.Held(ctx, d.Pool); why != "" {
		return "", 0, fmt.Errorf("%w: %s", ErrHeld, why)
	}
	// A run that checks a builder's work keeps off the builder's family (checkguard.go, rule 15).
	off, avoid, err := d.checkFamily(ctx, checkRun{role: rr.Role, kind: rr.Kind, project: rr.Project, branch: rr.Branch, model: rr.Model,
		name: rr.Name, ticket: rr.Ticket, eval: rr.Eval, judge: rr.Judge})
	if err != nil {
		return "", 0, err
	}
	// Room on the machine (7.9): the prompt writer is short and the operator waits for it, so it is
	// counted and never held back.
	size := capacity.SizeOf(rr.Role)
	if rr.ForceDocker {
		size = capacity.SizeOf("builder")
	}
	room := func(bool) {}
	if rr.Role != "prompt" {
		var err error
		key := fmt.Sprintf("run:%s%s:%d:%s:%d", rr.Project, rr.Draft, rr.Ticket, rr.Kind, rr.Subtask)
		if rr.Conversation != "" {
			key = "conversation:" + rr.Conversation
		}
		if room, err = d.takeRoom(ctx, d.roomAsk(ctx, key, rr.Owner, rr.Role, rr.Project+convProject, rr.Draft, rr.Ticket, rr.Title)); err != nil {
			return "", 0, err
		}
	}
	defer room(false)
	if rr.Timeout <= 0 {
		rr.Timeout = time.Hour
	}
	if rr.MaxTurns <= 0 {
		rr.MaxTurns = 200
	}
	prompt := Join(rr.Parts)
	title := strings.TrimSpace(rr.Title)
	if title == "" {
		title = rr.Kind
	}
	runID, convID := newUUID(), newUUID()
	if rr.Conversation != "" {
		convID = rr.Conversation
	}
	// A run that continues another's conversation in a new subtask takes it the second way when the
	// CLI's files are kept and the CLI is the same, else the third (12.10).
	way, cont, files := "new", "", map[string][]byte{}
	if rr.Continues != "" {
		var err error
		if convID, way, cont, files, err = d.resumeFrom(ctx, rr.Continues, a.Name()); err != nil {
			return "", 0, err
		}
		if way == WayRebuilt {
			if prompt, err = d.rebuilt(ctx, convID, prompt); err != nil {
				return "", 0, err
			}
		}
	}
	// What the run clones: a ticket's branch when it was pushed, or main when it was not (7.7); a
	// caller that gives a base, as an eval does, keeps it. A judge clones nothing.
	branch, base := rr.Branch, "refs/heads/main"
	if rr.Base != "" {
		base = rr.Base
	}
	if rr.ReadOnly && branch == "" {
		branch = "main"
	}
	var subtask int64
	var sc scope
	var commit string
	err = pgx.BeginFunc(ctx, d.Pool, func(tx pgx.Tx) error {
		var err error
		switch {
		case rr.Conversation != "":
			// The conversation's one subtask, whose runs take its turns (D2); a second first turn
			// finds the conversation taken and starts nothing.
			if err = tx.QueryRow(ctx, `INSERT INTO subtasks (scope, project, number, kind, role, state, title)
				VALUES ('conversation', nullif($1, '')::uuid, 1, $2, 'assistant', 'running', $3) RETURNING id`,
				convProject, rr.Kind, title).Scan(&subtask); err != nil {
				return err
			}
			tag, err := tx.Exec(ctx, `UPDATE conversations SET subtask = $2, cli = $3 WHERE id = $1 AND subtask IS NULL`, rr.Conversation, subtask, a.Name())
			if err != nil {
				return err
			}
			if tag.RowsAffected() != 1 {
				return board.ErrTaken
			}
		case rr.Subtask != 0 && rr.Ticket != 0:
			// A queued subtask an agent created starts: once, by the pass that takes it.
			err = tx.QueryRow(ctx, `UPDATE subtasks SET state = 'running' WHERE id = $1 AND ticket = $2 AND state = 'queued' RETURNING id`,
				rr.Subtask, rr.Ticket).Scan(&subtask)
			if errors.Is(err, pgx.ErrNoRows) {
				return board.ErrTaken
			}
		case rr.Ticket != 0:
			// A subtask's number is the next under its ticket: every writer of a ticket's subtasks
			// takes these locks before it counts (7.11).
			if err := board.LockForSubtasks(ctx, tx, rr.Ticket); err != nil {
				return err
			}
			err = tx.QueryRow(ctx, `INSERT INTO subtasks (scope, project, ticket, number, kind, role, state, title)
				VALUES ('ticket', $1, $2, (SELECT coalesce(max(number), 0) + 1 FROM subtasks WHERE ticket = $2), $3, $4, 'running', $5)
				RETURNING id`, rr.Project, rr.Ticket, rr.Kind, rr.Role, title).Scan(&subtask)
		case rr.Improver:
			err = tx.QueryRow(ctx, `INSERT INTO subtasks (scope, number, kind, role, state, title)
				VALUES ('improver', (SELECT coalesce(max(number), 0) + 1 FROM subtasks WHERE scope = 'improver'), $1, 'improver', 'running', $2)
				RETURNING id`, rr.Kind, title).Scan(&subtask)
		case rr.Draft != "":
			err = tx.QueryRow(ctx, `INSERT INTO subtasks (scope, draft, number, kind, role, state, title)
				VALUES ('crew', $1, (SELECT coalesce(max(number), 0) + 1 FROM subtasks WHERE draft = $1), $2, $3, 'running', $4)
				RETURNING id`, rr.Draft, rr.Kind, rr.Role, title).Scan(&subtask)
		default:
			sc := "project"
			if rr.Eval {
				sc = "eval"
			}
			err = tx.QueryRow(ctx, `INSERT INTO subtasks (scope, project, number, kind, role, state, title, image)
				VALUES ($5, $1, (SELECT coalesce(max(number), 0) + 1 FROM subtasks WHERE project = $1 AND ticket IS NULL), $2, $3, 'running', $4, nullif($6, ''))
				RETURNING id`, rr.Project, rr.Kind, rr.Role, title, sc, rr.Image).Scan(&subtask)
		}
		if err != nil {
			return err
		}
		if sc, err = d.scopeOf(ctx, tx, subtask); err != nil {
			return err
		}
		if sc.repo == "" || rr.Judge {
			branch, base = "", ""
		}
		// A chat reads its ticket's branch, main before it has one, or main at its merge commit (D7).
		if sc.chat != 0 && sc.repo != "" {
			branch, base, commit = sc.ref.branch, "refs/heads/"+sc.ref.branch, sc.ref.commit
		}
		if branch != "" {
			if _, err := tx.Exec(ctx, `UPDATE subtasks SET branch = $2 WHERE id = $1`, subtask, branch); err != nil {
				return err
			}
			if sc.eval {
				if sc.evalBase, err = evalBaseOf(ctx, tx, branch); err != nil {
					return err
				}
			}
		}
		if rr.Continues == "" && rr.Conversation == "" {
			if _, err := tx.Exec(ctx, `INSERT INTO conversations (id, project, draft, ticket, purpose, cli)
				VALUES ($1, nullif($2, '')::uuid, nullif($3, '')::uuid, nullif($4, 0), 'run', $5)`,
				convID, rr.Project, rr.Draft, rr.Ticket, a.Name()); err != nil {
				return err
			}
		} else if way == WayRebuilt {
			if _, err := tx.Exec(ctx, `UPDATE conversations SET cli = $2, cli_id = NULL, cli_totals = '{}' WHERE id = $1`, convID, a.Name()); err != nil {
				return err
			}
		}
		if err := d.insertRun(ctx, tx, newRun{
			id: runID, subtask: subtask, number: 1, cli: a.Name(), model: rr.Model, name: rr.Name, effort: rr.Effort,
			conversation: convID, way: way, branch: branch, base: base, timeout: rr.Timeout, continues: rr.Continues,
			maxTurns: rr.MaxTurns, tools: !rr.Judge, prompt: prompt, speaker: "crew", parts: rr.Parts, modelFrom: rr.From,
			effortFrom: rr.EffortFrom,
			readOnly:   rr.ReadOnly || rr.Judge, rebuilt: way == WayRebuilt, size: size, judge: rr.Judge, commit: commit,
		}); err != nil {
			return err
		}
		if off {
			if err := ruleNotMet(ctx, tx, rr.Project, rr.Ticket, runID, rr.Role, avoid); err != nil {
				return err
			}
		}
		if rr.Record != nil {
			return rr.Record(ctx, tx, runID)
		}
		return nil
	})
	if err != nil {
		return "", 0, fmt.Errorf("recording the run: %w", err)
	}
	room(true)
	spec := d.specFor(sc, harness.RunSpec{
		Run: runID, Subtask: subtask, CLI: a.Name(), Model: passed(rr.Name, rr.Model), Effort: rr.Effort, Prompt: prompt, Tools: !rr.Judge,
		Limits: harness.Limits{Timeout: rr.Timeout, MaxTurns: rr.MaxTurns}, ReadOnly: rr.ReadOnly || rr.Judge, Judge: rr.Judge,
	}, branch, base)
	if rr.AllowMCP && !sc.eval && !rr.Judge {
		spec.MCP = d.MCP
	}
	if rr.ForceDocker && !rr.Judge {
		spec.Minimal = false
	}
	spec.Commit = commit
	if way == WayFiles {
		spec.Continue, spec.Files = cont, files
	}
	d.sized(ctx, sc.role, &spec)
	d.start(ctx, runID, spec)
	return runID, subtask, nil
}

// resumeFrom is how a new subtask continues the conversation of run on cliName: its conversation,
// the way, the CLI's own id of it, and its files for the second way (12.10).
func (d *Driver) resumeFrom(ctx context.Context, run, cliName string) (conv, way, cliID string, files map[string][]byte, err error) {
	var convCLI string
	var id, stopReason *string
	if err = d.Pool.QueryRow(ctx, `SELECT c.id, coalesce(c.cli, r.cli), c.cli_id, r.stop_reason FROM runs r JOIN conversations c ON c.id = r.conversation
		WHERE r.id = $1`, run).Scan(&conv, &convCLI, &id, &stopReason); err != nil {
		return "", "", "", nil, fmt.Errorf("the run to continue: %w", err)
	}
	if convCLI != cliName || id == nil || !cli.SafeID(*id) ||
		(stopReason != nil && *stopReason != cli.ReasonOK && *stopReason != ReasonOperator) {
		return conv, WayRebuilt, "", nil, nil
	}
	if files, err = d.cliFiles(ctx, conv); err != nil {
		return "", "", "", nil, err
	}
	if len(files) == 0 {
		return conv, WayRebuilt, "", nil, nil
	}
	return conv, WayFiles, *id, files, nil
}

// cliFiles are the CLI's own files of a conversation that are still kept (12.10), opened with the key
// file. They are put back all or none: when one does not open, as one sealed with another key file or
// moved to another path, or does not unzip, there are none, and the conversation is rebuilt from its
// turns (plan of 2026-10-26, D6, D10).
func (d *Driver) cliFiles(ctx context.Context, conv string) (map[string][]byte, error) {
	files := map[string][]byte{}
	rows, err := d.Pool.Query(ctx, `SELECT path, gzip FROM cli_files WHERE conversation = $1 AND keep_until > now()`, conv)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	bad := ""
	for rows.Next() {
		var p string
		var sealed []byte
		if err := rows.Scan(&p, &sealed); err != nil {
			return nil, err
		}
		if d.Seal == nil {
			d.Log.Warn("the CLI's files are not put back: Crew has no key file to open them with", "conversation", conv)
			return map[string][]byte{}, nil
		}
		gz, err := d.Seal.Open(cliFilePurpose(conv, p), sealed)
		if err == nil {
			var b []byte
			if b, err = gunzip(gz); err == nil {
				files[p] = b
				continue
			}
		}
		if bad == "" {
			bad = p
		}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if bad != "" {
		d.Log.Warn("the CLI's files are not put back: one does not open with this key file", "conversation", conv, "path", bad)
		return map[string][]byte{}, nil
	}
	return files, nil
}

// scope is what a subtask's runs belong to, and so what their token names.
type scope struct {
	role, project, draft, repo string
	readRepos                  []string
	// mode is the network mode of the run's sandbox (13.6): its ticket's, its project's, or the
	// machine's. A judge's sandbox is Model only whatever it says (sandboxes.ModeFor).
	mode string
	// ownInstructions says the run's ticket may change files that instruct agents: its may_change
	// names one, and it counts (13.8).
	ownInstructions bool
	// setup is the project's setup command for a role that runs it, with its time limit; setupMayFail
	// says the run is the builder of the ticket that fixes it (13.5).
	setup        string
	setupTimeout time.Duration
	setupMayFail bool
	// eval says the run is one of an eval (9.6), and improver the improver's (10.4).
	eval, improver bool
	// image is the runtime image the subtask's runs start from: the candidate's, for an eval's run of a
	// release's suite (16.2 step 5, since 0.52); "" is the running release's.
	image string
	// evalBase is the base commit of the case a run of an eval with a branch replays: a run that judges
	// takes its instructions from it, since main holds the work it replays (9.6, 13.8).
	evalBase string
	// conversation says the subtask is a conversation's (11.8, 11.9): chat is its ticket when it is a
	// ticket's chat, and ref what that chat reads now.
	conversation bool
	chat         int64
	ref          chatRef
}

// chatRef is what a ticket's chat reads (11.9, D7): the ticket's branch, main before it has one, or
// main at the ticket's merge commit once it is Done; and the branches of its subtasks.
type chatRef struct {
	branch, commit string
	also           []string
}

// chatRefOf reads the chat's ref of a ticket now. The merge commit is the ticket's newest merge
// candidate whose checks passed, as board.MergeOf reads it.
func chatRefOf(ctx context.Context, q interface {
	QueryRow(context.Context, string, ...any) pgx.Row
}, ticket int64) (chatRef, error) {
	var r chatRef
	var done bool
	err := q.QueryRow(ctx, `SELECT coalesce(t.branch, ''), t.stage = 'done' AND t.closed_reason IS NULL,
			coalesce((SELECT commit FROM check_runs WHERE ticket = t.id AND purpose = 'merge' AND state = 'passed' ORDER BY id DESC LIMIT 1), ''),
			coalesce(ARRAY(SELECT DISTINCT s.branch FROM subtasks s WHERE s.ticket = t.id AND s.branch IS NOT NULL
				AND s.branch <> coalesce(t.branch, '') ORDER BY s.branch), '{}')
		FROM tickets t WHERE t.id = $1`, ticket).Scan(&r.branch, &done, &r.commit, &r.also)
	if err != nil {
		return r, err
	}
	if !done || r.commit == "" {
		r.commit = ""
	}
	if done && r.commit != "" || r.branch == "" {
		r.branch = "main"
	}
	return r, nil
}

// evalBaseOf is the base commit of the case whose eval run works on branch. A run of an eval whose
// case cannot be found does not start: it would have no instructions but the branch's (review of M15
// after the live checks).
func evalBaseOf(ctx context.Context, q interface {
	QueryRow(context.Context, string, ...any) pgx.Row
}, branch string) (string, error) {
	var base string
	err := q.QueryRow(ctx, `SELECT c.base_commit FROM eval_runs e JOIN eval_cases c ON c.id = e.eval_case WHERE e.branch = $1`, branch).Scan(&base)
	if err != nil {
		return "", fmt.Errorf("the eval case of branch %s: %w", branch, err)
	}
	return base, nil
}

// namesInstructions says a may_change list names a file that instructs agents, or a folder of them.
func namesInstructions(mayChange []string) bool {
	// The files a run reads as instructions or code; a repository's workflows are not read by a run.
	var pats []string
	for _, f := range cli.InstructionFiles() {
		if !strings.HasPrefix(f, ".github/") {
			pats = append(pats, f)
		}
	}
	for _, m := range mayChange {
		m = strings.TrimRight(strings.TrimSuffix(strings.TrimSuffix(m, "**"), "*"), "/")
		if m != "" && cli.Matches(m, pats) {
			return true
		}
	}
	return false
}

// scopeOf reads a subtask's scope: its role, its project with the project's repository at the git
// endpoint, or its plan draft with the GitHub repository the draft names for reading.
func (d *Driver) scopeOf(ctx context.Context, q interface {
	QueryRow(context.Context, string, ...any) pgx.Row
}, subtask int64) (scope, error) {
	var sc scope
	var readRepo string
	var ticket int64
	var mayChange []string
	var mayList bool
	var branch, convMode string
	err := q.QueryRow(ctx, `SELECT s.role, s.scope = 'eval', s.scope = 'improver', s.scope = 'conversation',
			coalesce((SELECT c.ticket FROM conversations c WHERE c.subtask = s.id AND c.purpose = 'chat'), 0),
			coalesce((SELECT c.network_mode FROM conversations c WHERE c.subtask = s.id AND c.purpose = 'operator'), ''),
			coalesce(s.project::text, ''), coalesce(s.draft::text, ''), coalesce(s.branch, ''),
			coalesce(s.image, ''),
			coalesce((SELECT name FROM git_repos g WHERE g.project = s.project ORDER BY name LIMIT 1), ''),
			coalesce((SELECT repo FROM plan_drafts pd WHERE pd.id = s.draft), ''), coalesce(s.ticket, 0),
			coalesce(ARRAY(SELECT jsonb_array_elements_text(CASE WHEN jsonb_typeof(t.analysis->'mayChange') = 'array'
				THEN t.analysis->'mayChange' ELSE '[]' END)), '{}'),
			coalesce(t.origin IN ('plan', 'operator') OR (t.analysis->>'mayChangeAgreed')::boolean, false)
		FROM subtasks s LEFT JOIN tickets t ON t.id = s.ticket WHERE s.id = $1`, subtask).Scan(&sc.role, &sc.eval, &sc.improver, &sc.conversation, &sc.chat,
		&convMode, &sc.project, &sc.draft, &branch, &sc.image, &sc.repo, &readRepo, &ticket,
		&mayChange, &mayList)
	if err != nil {
		return sc, err
	}
	if sc.improver {
		// The improver sees every project and has no way out: Model only, always (10.4, 13.6).
		if sc.project != "" || sc.draft != "" || sc.role != "improver" {
			return sc, fmt.Errorf("subtask %d is the improver's and names a project or a draft", subtask)
		}
		sc.mode = config.ModeModelOnly
		return sc, nil
	}
	if sc.conversation {
		// A conversation is the assistant's, about its project or none; a chat has the mode its
		// ticket's runs have, a ticket's own mode its ceiling (13.6).
		if sc.role != "assistant" || sc.draft != "" || ticket != 0 || (sc.chat != 0 && sc.project == "") {
			return sc, fmt.Errorf("subtask %d is a conversation's and is not the assistant's of its project or none", subtask)
		}
		if sc.chat != 0 {
			if sc.ref, err = chatRefOf(ctx, q, sc.chat); err != nil {
				return sc, err
			}
		}
		sc.mode, err = settings.NetworkMode(ctx, q, sc.project, sc.chat)
		if err == nil && sc.chat == 0 && convMode == string(config.ModeModelOnly) {
			sc.mode = config.ModeModelOnly
		}
		return sc, err
	}
	if sc.project == "" && sc.draft == "" {
		return sc, fmt.Errorf("subtask %d belongs to no project and no plan draft", subtask)
	}
	sc.ownInstructions = mayList && namesInstructions(mayChange)
	if sc.eval && branch != "" {
		if sc.evalBase, err = evalBaseOf(ctx, q, branch); err != nil {
			return sc, err
		}
	}
	// A draft's subtask keeps its draft's scope after the draft became a project, which then counts
	// its tokens: its runs never get the project's repository or the project's tools.
	if sc.draft != "" {
		sc.project, sc.repo = "", ""
	}
	if readRepo != "" {
		sc.readRepos = []string{readRepo}
	}
	// A plan draft has no project yet: its planning agent has the machine's mode.
	if sc.mode, err = settings.NetworkMode(ctx, q, sc.project, ticket); err != nil {
		return sc, err
	}
	if setupRoles[sc.role] && sc.project != "" {
		var name, key string
		if err := q.QueryRow(ctx, `SELECT name, key FROM projects WHERE id = $1`, sc.project).Scan(&name, &key); err != nil {
			return sc, err
		}
		doc, err := settings.EffectiveProject(ctx, d.Pool, sc.project, name, key)
		if err != nil {
			return sc, err
		}
		sandbox, _ := doc["sandbox"].(settings.Doc)
		sc.setup, _ = sandbox["setup"].(string)
		minutes := 30
		if l, ok := doc["limits"].(settings.Doc); ok {
			if n, ok := l["check_timeout_minutes"].(int); ok && n > 0 {
				minutes = n
			}
		}
		sc.setupTimeout = time.Duration(minutes) * time.Minute
		if sc.role == "builder" && ticket != 0 {
			if err := q.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM setup_checks WHERE ticket = $1)`, ticket).Scan(&sc.setupMayFail); err != nil {
				return sc, err
			}
		}
	}
	return sc, nil
}

// specFor fills a run's spec from its scope: the project and its repository, the GitHub repositories
// its token may read, the Crew MCP for a role that has its tools, and Docker for a role that runs the
// project's services. A judge gets none of it.
func (d *Driver) specFor(sc scope, spec harness.RunSpec, branch, base string) harness.RunSpec {
	spec.Project, spec.Repo, spec.Repos = sc.project, sc.repo, sc.readRepos
	spec.Branch, spec.Base = "", ""
	spec.Improver = sc.improver
	spec.OwnBranchOnly, spec.InstructionsAt = sc.eval, sc.evalBase
	spec.Minimal = !dockerRoles[sc.role]
	spec.Mode = sc.mode
	spec.Setup, spec.SetupTimeout, spec.SetupMayFail = sc.setup, sc.setupTimeout, sc.setupMayFail
	spec.Image = sc.image
	if spec.Judge {
		spec.Repo, spec.Repos, spec.Tools = "", nil, false
		return spec
	}
	if sc.repo != "" {
		spec.Branch, spec.Base = branch, base
	}
	// Every role in mcpRoles (including a normal conversation outside a project, 11.8) gets the Crew
	// MCP unless it is an eval replay.
	if mcpRoles[sc.role] && !sc.eval {
		spec.MCP = d.MCP
	}
	if sc.conversation {
		spec.Conversation, spec.Chat, spec.KeepFolder = sc.chat == 0, sc.chat != 0, harness.ConversationFolder
		if sc.chat != 0 && sc.repo != "" {
			spec.Refresh, spec.Also = true, sc.ref.also
		}
	}
	spec.NoRepoConfig = !buildRoles[sc.role]
	// Every run reads main's skills and context, also a builder on an older branch, but for the builder
	// of a ticket that may change them (12.9, since 0.46), and a run of an eval, which main would show
	// the work it replays (9.6).
	spec.MainInstructions = (!buildRoles[sc.role] || !sc.ownInstructions) && !sc.eval
	return spec
}

// liveRunOf is the live run of a sandbox that a request at its door proved to be with its token.
func (d *Driver) liveRunOf(r *http.Request, token string) (string, error) {
	if d.Auth == nil || token == "" {
		return "", errors.New("no token")
	}
	local := ""
	if a, ok := r.Context().Value(http.LocalAddrContextKey).(net.Addr); ok {
		local = a.String()
	}
	row, err := d.Auth.Authorize(r.Context(), local, r.RemoteAddr, token)
	if err != nil {
		return "", err
	}
	var id string
	err = d.Pool.QueryRow(r.Context(), `SELECT r.id FROM runs r
		LEFT JOIN run_tokens rt ON rt.run = r.id AND rt.sha256 = $2 AND rt.ends_at > now()
		WHERE r.sandbox = $1 AND r.state <> 'ended'
		ORDER BY (rt.sha256 IS NOT NULL) DESC, r.started_at DESC LIMIT 1`,
		row.ID, sandboxes.Hash(token)).Scan(&id)
	return id, err
}

// turnRuns says whether a run's turn runs now: the Crew MCP answers only then, so what a turn left
// running in its sandbox cannot write between turns (security review of M5).
func (d *Driver) turnRuns(ctx context.Context, run string) bool {
	var state string
	err := d.Pool.QueryRow(ctx, `SELECT state FROM runs WHERE id = $1`, run).Scan(&state)
	return err == nil && (state == "starting" || state == "working")
}

// MCPCaller is the Crew MCP's rule for who calls (11.5): a request at a run's own door, from its
// address, with its token, while a turn of that run runs, is that run, with the role and the one
// project or plan draft its token row names while it lasts. Nothing the request says otherwise is
// read.
func (d *Driver) MCPCaller(r *http.Request) (mcp.Caller, error) {
	token, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
	if !ok {
		return mcp.Caller{}, mcp.ErrNoToken
	}
	run, err := d.liveRunOf(r, strings.TrimSpace(token))
	if err != nil || !d.turnRuns(r.Context(), run) {
		return mcp.Caller{}, mcp.ErrNoToken
	}
	c := mcp.Caller{Run: run}
	err = d.Pool.QueryRow(r.Context(), `SELECT t.role, coalesce(t.project::text, ''), coalesce(t.draft::text, ''), s.id, coalesce(s.ticket, 0),
			s.scope = 'improver' AND t.role = 'improver' AND t.project IS NULL AND t.draft IS NULL,
			coalesce(c.id::text, ''), coalesce(c.ticket, 0)
		FROM run_tokens t JOIN runs r ON r.id = t.run JOIN subtasks s ON s.id = r.subtask
		LEFT JOIN conversations c ON c.subtask = s.id AND s.scope = 'conversation' AND c.purpose IN ('operator', 'chat')
		WHERE t.run = $1 AND t.ends_at > now() ORDER BY t.ends_at DESC LIMIT 1`, run).Scan(&c.Role, &c.Project, &c.Draft, &c.Subtask, &c.Ticket, &c.Crew,
		&c.Conversation, &c.ChatTicket)
	if err != nil {
		return mcp.Caller{}, mcp.ErrNoToken
	}
	// A run on a ticket writes to that ticket only (11.5); a draft's run belongs to no ticket.
	if c.Project == "" {
		c.Ticket = 0
	}
	return c, nil
}

// GitAccess is the git endpoint's rule for runs (6.1): a request at a run's own door, from its
// address, with its token, reads main, its branch, and the work in progress it continues from, and
// pushes only its branch and its own wip ref, in its own project's repository only.
func (d *Driver) GitAccess(r *http.Request, repo string) (gitendpoint.Access, error) {
	id, err := d.liveRunOf(r, gitendpoint.Token(r))
	if err != nil {
		return gitendpoint.Access{}, gitendpoint.ErrRefused
	}
	var own, branch, base, ticketBranch string
	var push, onTicket, mayList, eval bool
	var mayChange, touches, chatReads []string
	// A ticket's chat reads, beside main, its ticket's branch and its subtasks' as they are now (D7).
	err = d.Pool.QueryRow(r.Context(), `SELECT coalesce(g.name, ''), coalesce(r.branch, ''), coalesce(r.base_ref, ''), r.push,
			t.id IS NOT NULL, coalesce(t.branch, ''), s.touches,
			coalesce(ARRAY(SELECT jsonb_array_elements_text(CASE WHEN jsonb_typeof(t.analysis->'mayChange') = 'array'
				THEN t.analysis->'mayChange' ELSE '[]' END)), '{}'),
			coalesce(t.origin IN ('plan', 'operator') OR (t.analysis->>'mayChangeAgreed')::boolean, false), s.scope = 'eval',
			coalesce(ARRAY(SELECT b FROM conversations c JOIN tickets ct ON ct.id = c.ticket,
				LATERAL (SELECT ct.branch AS b UNION SELECT cs.branch FROM subtasks cs WHERE cs.ticket = ct.id) bs
				WHERE c.subtask = s.id AND c.purpose = 'chat' AND s.scope = 'conversation' AND b IS NOT NULL), '{}')
		FROM runs r JOIN subtasks s ON s.id = r.subtask LEFT JOIN git_repos g ON g.project = s.project AND g.name = $2
		LEFT JOIN tickets t ON t.id = s.ticket
		WHERE r.id = $1`, id, repo).Scan(&own, &branch, &base, &push, &onTicket, &ticketBranch, &touches, &mayChange, &mayList, &eval, &chatReads)
	if err == nil && own == "" {
		// A normal conversation outside a project reads main of any non-archived, non-hidden project's
		// repository, and pushes nothing (11.8, 13.3).
		var normalOK bool
		if qerr := d.Pool.QueryRow(r.Context(), `SELECT EXISTS (
			SELECT 1 FROM runs r JOIN subtasks s ON s.id = r.subtask
			JOIN conversations c ON c.subtask = s.id
			JOIN git_repos g ON g.name = $2 JOIN projects p ON p.id = g.project
			WHERE r.id = $1 AND s.scope = 'conversation' AND c.purpose = 'operator' AND c.project IS NULL
				AND p.state <> 'archived' AND NOT p.hidden
		)`, id, repo).Scan(&normalOK); qerr == nil && normalOK {
			return gitendpoint.Access{Repo: repo, Who: "run " + id, Run: id, Read: []string{"refs/heads/main"}}, nil
		}
	}
	if err != nil || own == "" || own != repo || branch == "" {
		return gitendpoint.Access{}, gitendpoint.ErrRefused
	}
	wip := "refs/crew/wip/" + id
	read := []string{"refs/heads/main", "refs/heads/" + branch, wip}
	if base != "" && base != "refs/heads/main" {
		read = append(read, base)
	}
	for _, b := range chatReads {
		if !push {
			read = append(read, "refs/heads/"+b)
		}
	}
	// A run of an eval reads its own branch only: main holds the work it replays (9.6).
	if eval {
		if !strings.HasPrefix(branch, "eval/") {
			return gitendpoint.Access{}, gitendpoint.ErrRefused
		}
		read = []string{"refs/heads/" + branch, wip}
	}
	// A run that analyses or judges reads and pushes nothing (6.1), not even work in progress.
	if !push {
		return gitendpoint.Access{Repo: repo, Who: "run " + id, Run: id, Read: read}, nil
	}
	acc := gitendpoint.Access{Repo: repo, Who: "run " + id, Run: id, Read: read, Push: []string{"refs/heads/" + branch, wip}}
	if onTicket {
		// The work in progress has the branch's rule: the next run of the subtask starts from it
		// (security review of M9).
		// Its files are a part's aid to running beside others, not a boundary of the work in
		// progress: a stray file there must not lose a stopped run's work.
		rule := pathRule(branch, ticketBranch, mayChange, mayList, touches)
		saved := rule
		saved.Touches = nil
		acc.Paths = map[string]gitendpoint.PathRule{"refs/heads/" + branch: rule, wip: saved}
	}
	return acc, nil
}

// pathRule is what a run on a ticket may change with a push to its branch (13.8, 7.11): every file
// that does not instruct agents, and those its ticket's may_change names, measured from main; a
// subtask on a branch of its own, measured from the ticket's branch, only inside the files it said it
// touches. The paths a ticket an agent created names count only once the operator agreed (13.8).
func pathRule(branch, ticketBranch string, mayChange []string, mayList bool, touches []string) gitendpoint.PathRule {
	rule := gitendpoint.PathRule{Base: "refs/heads/main", Allow: []string{gitendpoint.Everything}}
	if mayList {
		rule.Allow = append(rule.Allow, mayChange...)
	}
	if ticketBranch != "" && branch != ticketBranch {
		rule.Base = "refs/heads/" + ticketBranch
	}
	if len(touches) > 0 {
		rule.Touches = touches
	}
	return rule
}

// RecordPush records a ref a push through the git endpoint changed (6.7): which run pushed which
// commits. Work in progress is not recorded: it is the run's own, and the next commit replaces it.
func (d *Driver) RecordPush(acc gitendpoint.Access, ref, old, new string) {
	// A ref the endpoint could not read, which it names with no value, is no push to record (since 1.08).
	if strings.HasPrefix(ref, "refs/crew/wip/") || new == "" {
		return
	}
	var run any
	if acc.Run != "" {
		run = acc.Run
	}
	if _, err := d.Pool.Exec(context.Background(), `INSERT INTO ref_pushes (repo, run, who, ref, old, new) VALUES ($1, $2, $3, $4, nullif($5, ''), $6)`,
		acc.Repo, run, acc.Who, ref, old, new); err != nil {
		d.Log.Error("recording a push", "repo", acc.Repo, "ref", ref, "error", err)
	}
}

// passed is the name a CLI is given as its model: name, or the model when it has none.
func passed(name, model string) string {
	if name != "" {
		return name
	}
	return model
}
