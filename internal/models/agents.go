package models

import (
	"context"
	"slices"
	"time"

	"github.com/kesokaj/crew/internal/cli"
	"github.com/kesokaj/crew/internal/db"
	"github.com/kesokaj/crew/internal/settings"
)

// Roles are the agents of 12.3, in the order the Models screen lists them.
var Roles = []string{"curator", "builder", "reviewer", "tester", "e2e", "verifier", "summary", "prompt", "planner", "assistant", "judge", "improver"}

// EntryView is one entry of an agent's list as it stands now.
type EntryView struct {
	Entry string `json:"entry"`
	// Offered says a CLI in use lists the model; CanRun that it can start now.
	Offered bool   `json:"offered"`
	CanRun  bool   `json:"canRun"`
	Why     string `json:"why,omitempty"`
	Family  string `json:"family,omitempty"`
	// Short says the model comes from the adapter's short list: its CLI's list was never read.
	Short bool `json:"short,omitempty"`
}

// AgentView is one agent on the Models screen: its entries, its effort, what a new run of it would
// run on now, and whether Crew chooses for it.
type AgentView struct {
	Role    string      `json:"role"`
	Entries []EntryView `json:"entries"`
	Effort  string      `json:"effort"`
	// State is automatic (Crew chooses), set_by_you, or list (it follows its list).
	State string `json:"state"`
	// RunsOn is what a new run of the agent starts on, with no ticket and no project; Waits says why
	// none can.
	RunsOn *Choice `json:"runsOn,omitempty"`
	Waits  string  `json:"waits,omitempty"`
	// CheckRule is set for an agent that checks: whether the rule for checks can be met now for a
	// build on the builder's family.
	CheckRule string `json:"checkRule,omitempty"`
}

// Agents are the machine's agents as they stand now.
func Agents(ctx context.Context, q db.Querier, doc settings.Doc) ([]AgentView, error) {
	st, err := load(ctx, q, doc)
	if err != nil {
		return nil, err
	}
	rules := RulesOf(doc)
	roles, _ := dig(doc, "agents", "roles").(settings.Doc)
	var out []AgentView
	var builderFamily string
	for _, role := range Roles {
		entry, ok := roles[role].(settings.Doc)
		if !ok {
			continue
		}
		v := AgentView{Role: role, Effort: str(entry["effort"]), State: "list", Entries: []EntryView{}}
		if slices.Contains(AutoRoles, role) {
			v.State = "set_by_you"
			if rules.Automatic(role) {
				v.State = "automatic"
			}
		}
		for _, e := range strs(entry["model"]) {
			c := st.candidate(e)
			ev := EntryView{Entry: e, Offered: c.listed, CanRun: c.runnable && !c.close, Why: c.why, Short: c.short}
			if c.listed {
				ev.Family = c.family()
				if c.close && ev.Why == "" {
					ev.Why = "close to its limit, and a run works there"
				}
			} else {
				ev.Why = "no CLI in use offers it now"
			}
			v.Entries = append(v.Entries, ev)
		}
		ch, err := st.choose(Ask{Role: role, Machine: doc})
		if w, ok := IsWait(err); ok {
			v.Waits = w.Reason
		} else if err != nil {
			return nil, err
		} else {
			v.RunsOn = &ch
			if role == "builder" {
				builderFamily = familyOf(ch.Model)
			}
		}
		out = append(out, v)
	}
	// The rule for checks, against what the builder runs on now.
	for i := range out {
		if !slices.Contains(CheckRoles, out[i].Role) || builderFamily == "" {
			continue
		}
		ask := Ask{Role: out[i].Role, Machine: doc, Avoid: builderFamily}
		ch, err := st.choose(ask)
		switch w, wait := IsWait(err); {
		case wait:
			out[i].CheckRule = "waits: " + w.Reason
		case err != nil:
			return nil, err
		case !cli.Known(builderFamily):
			out[i].CheckRule = "waits: the builder's model is " + cli.UnknownFamily
		case !cli.Known(familyOf(ch.Model)):
			out[i].CheckRule = "waits: " + ch.Model + " is " + cli.UnknownFamily
		case ch.Off && st.otherFamilyListed(ask) && st.otherFamilyRunnable(ask):
			out[i].CheckRule = "cannot be met now: another family is above slow_down_at, falls back to " + ch.Entry()
		case ch.Off && st.otherFamilyListed(ask):
			out[i].CheckRule = "cannot be met now: another family is at its limit, falls back to " + ch.Entry()
		case ch.Off:
			out[i].CheckRule = "cannot be met: no CLI in use offers another family than " + builderFamily
		case ch.ModelFrom == FromCheckRule:
			out[i].CheckRule = "moves a check of the builder's work to " + ch.Entry()
		default:
			out[i].CheckRule = "met: " + ch.Entry() + " is another family than " + builderFamily
		}
	}
	return out, nil
}

// ModelRun is one run on a model, for the Models screen.
type ModelRun struct {
	ID      string    `json:"id"`
	Role    string    `json:"role"`
	Effort  string    `json:"effort"`
	Project string    `json:"project,omitempty"`
	Ticket  string    `json:"ticket,omitempty"`
	Title   string    `json:"title"`
	State   string    `json:"state"`
	Started time.Time `json:"startedAt"`
}

// ModelUse is what one model did for one agent and effort: its runs, how many failed, and their
// tokens in and out (since 0.53 no cost).
type ModelUse struct {
	Role   string `json:"role"`
	Effort string `json:"effort"`
	Runs   int    `json:"runs"`
	Failed int    `json:"failed"`
	Tokens int64  `json:"tokens"`
}

// Uses are the runs on one model of a CLI by agent and effort, and its latest runs.
func Uses(ctx context.Context, q db.Querier, cliName, model string) ([]ModelUse, []ModelRun, error) {
	rows, err := q.Query(ctx, `SELECT s.role, coalesce(r.effort, ''), count(*),
			count(*) FILTER (WHERE r.state = 'ended' AND coalesce(r.stop_reason, 'ok') <> 'ok'), coalesce(sum(r.tokens_in + r.tokens_out), 0)::bigint
		FROM runs r JOIN subtasks s ON s.id = r.subtask WHERE r.cli = $1 AND r.model = $2
		GROUP BY s.role, r.effort ORDER BY count(*) DESC`, cliName, model)
	if err != nil {
		return nil, nil, err
	}
	var uses []ModelUse
	for rows.Next() {
		var u ModelUse
		if err := rows.Scan(&u.Role, &u.Effort, &u.Runs, &u.Failed, &u.Tokens); err != nil {
			rows.Close()
			return nil, nil, err
		}
		uses = append(uses, u)
	}
	rows.Close()
	rows, err = q.Query(ctx, `SELECT r.id::text, s.role, coalesce(r.effort, ''), coalesce(p.name, ''), coalesce(p.key || '-' || t.number, ''),
			coalesce(s.title, ''), r.state, r.started_at
		FROM runs r JOIN subtasks s ON s.id = r.subtask LEFT JOIN projects p ON p.id = s.project LEFT JOIN tickets t ON t.id = s.ticket
		WHERE r.cli = $1 AND r.model = $2 ORDER BY r.started_at DESC LIMIT 10`, cliName, model)
	if err != nil {
		return nil, nil, err
	}
	var latest []ModelRun
	for rows.Next() {
		var m ModelRun
		if err := rows.Scan(&m.ID, &m.Role, &m.Effort, &m.Project, &m.Ticket, &m.Title, &m.State, &m.Started); err != nil {
			rows.Close()
			return nil, nil, err
		}
		latest = append(latest, m)
	}
	rows.Close()
	return uses, latest, rows.Err()
}
