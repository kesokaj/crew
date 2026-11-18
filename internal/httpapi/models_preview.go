package httpapi

import (
	"context"
	"errors"
	"net/http"
	"slices"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/kesokaj/crew/internal/cli"
	"github.com/kesokaj/crew/internal/models"
	"github.com/kesokaj/crew/internal/settings"
)

// OfferedModel is one model a signed-in CLI in use offers now, with its efforts, for the lists a
// model is chosen from (12.3: every list shows only what the signed-in CLIs offer now).
type OfferedModel struct {
	Entry   string   `json:"entry"`
	CLI     string   `json:"cli"`
	Model   string   `json:"model"`
	Display string   `json:"display,omitempty"`
	Family  string   `json:"family"`
	Efforts []string `json:"efforts"`
}

func (s *Server) offered(ctx context.Context, pool *pgxpool.Pool, doc settings.Doc) ([]OfferedModel, error) {
	clis, err := s.cliModels(ctx, pool, doc)
	if err != nil {
		return nil, err
	}
	out := []OfferedModel{}
	for _, c := range clis {
		if !c.SignedIn || !c.InUse {
			continue
		}
		at := map[string]int{}
		for _, m := range c.Models {
			if !m.Offered {
				continue
			}
			i, ok := at[m.Model]
			if !ok {
				display := m.Display
				if m.InName {
					// agy's name to show holds the effort: "Gemini 3.8 Flash (High)".
					if j := strings.LastIndex(display, " ("); j > 0 {
						display = display[:j]
					}
				}
				out = append(out, OfferedModel{Entry: c.CLI + "/" + m.Model, CLI: c.CLI, Model: m.Model, Display: display, Family: m.Family, Efforts: []string{}})
				i = len(out) - 1
				at[m.Model] = i
			}
			for _, e := range m.Efforts {
				if !slices.Contains(out[i].Efforts, e) {
					out[i].Efforts = append(out[i].Efforts, e)
				}
			}
		}
	}
	for i := range out {
		slices.SortFunc(out[i].Efforts, func(a, b string) int { return slices.Index(cli.EffortOrder, a) - slices.Index(cli.EffortOrder, b) })
	}
	return out, nil
}

// ticketAgents is the task view's "Agent for this ticket" (20.2, 12.3): the ticket's pins, what each
// agent that works on a ticket would run on now, with the pins and the rule for checks, and what can
// be pinned.
func (s *Server) ticketAgents(w http.ResponseWriter, r *http.Request) {
	b, p := s.project(w, r)
	if p == nil {
		return
	}
	ctx := r.Context()
	t, err := b.Ticket(ctx, p, r.PathValue("t"))
	if err != nil {
		s.boardError(w, "reading the ticket", err)
		return
	}
	doc, err := s.machineDoc(ctx, b.Pool)
	if err != nil {
		s.boardError(w, "reading the settings", err)
		return
	}
	own, err := b.Settings(ctx, b.Pool, p)
	if err != nil {
		s.boardError(w, "reading the project's settings", err)
		return
	}
	var built string
	if err := b.Pool.QueryRow(ctx, `SELECT coalesce(r.model, '') FROM runs r JOIN subtasks s ON s.id = r.subtask
		WHERE s.ticket = $1 AND s.kind IN ('build', 'fix') ORDER BY r.started_at DESC LIMIT 1`, t.ID).Scan(&built); err != nil && !errors.Is(err, pgx.ErrNoRows) {
		s.boardError(w, "reading the ticket's builder", err)
		return
	}
	roles := map[string]any{}
	var builder string
	for _, spec := range []struct{ role, kind string }{{"builder", "build"}, {"reviewer", "review"}, {"tester", "test"}} {
		role := spec.role
		pin, _ := t.Agents[role].(map[string]any)
		ask := models.Ask{Role: role, Machine: doc, Project: own, Pin: pin, Bracket: models.TicketBracket(role, spec.kind, t.Kind)}
		if role != "builder" {
			ask.Avoid = cli.Family(built)
			if ask.Avoid == "" {
				ask.Avoid = builder
			}
		}
		c, err := models.Choose(ctx, b.Pool, ask)
		if wt, ok := models.IsWait(err); ok {
			roles[role] = map[string]any{"waits": wt.Reason}
			continue
		}
		if err != nil {
			s.boardError(w, "choosing the models", err)
			return
		}
		if role == "builder" {
			builder = cli.Family(c.Model)
		}
		roles[role] = c
	}
	offered, err := s.offered(ctx, b.Pool, doc)
	if err != nil {
		s.boardError(w, "reading the models", err)
		return
	}
	pins := t.Agents
	if pins == nil {
		pins = map[string]any{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"pins": pins, "roles": roles, "offered": offered})
}

// projectAgents is the project Settings screen's agent part (12.3): what each stage's agent runs on
// in this project now, what the project set itself, what can be chosen, and the runs so far by model
// and effort, with the roles that ran them.
func (s *Server) projectAgents(w http.ResponseWriter, r *http.Request) {
	b, p := s.project(w, r)
	if p == nil {
		return
	}
	ctx := r.Context()
	doc, err := s.machineDoc(ctx, b.Pool)
	if err != nil {
		s.boardError(w, "reading the settings", err)
		return
	}
	own, err := b.Settings(ctx, b.Pool, p)
	if err != nil {
		s.boardError(w, "reading the project's settings", err)
		return
	}
	roles := map[string]any{}
	for _, role := range []string{"curator", "builder", "reviewer", "tester", "e2e", "verifier", "summary"} {
		c, err := models.Choose(ctx, b.Pool, models.Ask{Role: role, Machine: doc, Project: own})
		if wt, ok := models.IsWait(err); ok {
			roles[role] = map[string]any{"waits": wt.Reason}
			continue
		}
		if err != nil {
			s.boardError(w, "choosing the models", err)
			return
		}
		roles[role] = c
	}
	offered, err := s.offered(ctx, b.Pool, doc)
	if err != nil {
		s.boardError(w, "reading the models", err)
		return
	}
	rows, err := b.Pool.Query(ctx, `SELECT coalesce(r.model_name, r.model) || CASE WHEN r.model_name = r.model AND r.effort IS NOT NULL
			THEN ' · ' || r.effort ELSE '' END, s.role, count(*)
		FROM runs r JOIN subtasks s ON s.id = r.subtask WHERE s.project = $1 AND r.model IS NOT NULL GROUP BY 1, 2 ORDER BY 1, 2`, p.ID)
	if err != nil {
		s.boardError(w, "reading the project's runs", err)
		return
	}
	type ran struct {
		Model string         `json:"model"`
		Runs  int            `json:"runs"`
		Roles map[string]int `json:"roles"`
	}
	var so []ran
	for rows.Next() {
		var m, role string
		var n int
		if rows.Scan(&m, &role, &n) != nil {
			continue
		}
		if len(so) == 0 || so[len(so)-1].Model != m {
			so = append(so, ran{Model: m, Roles: map[string]int{}})
		}
		so[len(so)-1].Runs += n
		so[len(so)-1].Roles[role] += n
	}
	rows.Close()
	agents, _ := own["agents"].(settings.Doc)
	if agents == nil {
		agents = settings.Doc{}
	}
	checkRule, _ := doc["agents"].(settings.Doc)["checks_use_another_family"].(bool)
	if v, ok := agents["checks_use_another_family"].(bool); ok {
		checkRule = v
	}
	writeJSON(w, http.StatusOK, map[string]any{"roles": roles, "own": agents, "offered": offered, "runs": nonNil(so), "checkRule": checkRule})
}
