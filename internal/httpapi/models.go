package httpapi

import (
	"context"
	"errors"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/kesokaj/crew/internal/cli"
	"github.com/kesokaj/crew/internal/models"
	"github.com/kesokaj/crew/internal/planning"
	"github.com/kesokaj/crew/internal/settings"
)

// The Models screen's routes (12.3, 15.4): the models each CLI offers, the agents with what each runs
// on, Crew's choices and the comparisons, the limits of each group, and the lead slots.
func (s *Server) modelRoutes(api *routes) {
	api.HandleFunc("GET /api/crew/models", s.listModels)
	api.HandleFunc("GET /api/crew/models/{cli}/{model}", s.getModel)
	api.HandleFunc("POST /api/crew/models", s.addModel)
	api.HandleFunc("POST /api/crew/models/refresh", s.refreshModels)
	api.HandleFunc("PUT /api/crew/agents/{role}", s.setAgent)
	api.HandleFunc("PUT /api/crew/agents/{role}/choice", s.setAgentChoice)
	api.HandleFunc("POST /api/crew/model-choices/{id}/undo", s.modelChoice("undo"))
	api.HandleFunc("POST /api/crew/model-choices/{id}/use", s.modelChoice("use"))
	api.HandleFunc("POST /api/crew/model-choices/{id}/dismiss", s.modelChoice("dismiss"))
	api.HandleFunc("GET /api/crew/comparisons", s.listComparisons)
	api.HandleFunc("POST /api/crew/comparisons", s.startComparison)
	api.HandleFunc("DELETE /api/crew/comparisons/{id}", s.stopComparison)
	api.HandleFunc("GET /api/crew/quota", s.quota)
	api.HandleFunc("PUT /api/capacity/lead-slots", s.setLeadSlots)
	api.HandleFunc("PUT /api/capacity/active-tickets", s.setActiveTickets)
	api.HandleFunc("GET /api/projects/{p}/tasks/{t}/agents", s.ticketAgents)
	api.HandleFunc("GET /api/projects/{p}/agents", s.projectAgents)
}

// CLIModels is one CLI's list as Crew read it.
type CLIModels struct {
	CLI      string `json:"cli"`
	SignedIn bool   `json:"signedIn"`
	InUse    bool   `json:"inUse"`
	// Read is when the list was last read; Short says it was never read, and the adapter's short
	// list stands in (12.3).
	Read     *time.Time    `json:"readAt,omitempty"`
	Short    bool          `json:"short"`
	Failures int           `json:"failures"`
	Error    string        `json:"error,omitempty"`
	Models   []ModelRow    `json:"models"`
	Windows  []QuotaWindow `json:"windows"`
}

// ModelRow is one name a CLI lists, or listed.
type ModelRow struct {
	Name      string     `json:"name"`
	Model     string     `json:"model"`
	Efforts   []string   `json:"efforts"`
	InName    bool       `json:"inName"`
	Display   string     `json:"display,omitempty"`
	Resolved  string     `json:"resolved,omitempty"`
	Family    string     `json:"family"`
	Group     string     `json:"group"`
	GroupName string     `json:"groupName"`
	Offered   bool       `json:"offered"`
	Refused   bool       `json:"refused"`
	AddedBy   string     `json:"addedBy"`
	FirstSeen time.Time  `json:"firstSeen"`
	GoneAt    *time.Time `json:"goneAt,omitempty"`
}

// QuotaWindow is one window of a group of models, as the quota guard keeps it (12.7).
type QuotaWindow struct {
	CLI          string     `json:"cli"`
	Group        string     `json:"group"`
	GroupName    string     `json:"groupName"`
	Window       string     `json:"window"`
	Used         float64    `json:"used"`
	ResetsAt     *time.Time `json:"resetsAt,omitempty"`
	ReachedUntil *time.Time `json:"reachedUntil,omitempty"`
	Source       string     `json:"source"`
	ReadAt       time.Time  `json:"readAt"`
	Close        bool       `json:"close"`
	Running      int        `json:"running"`
}

// ProjectModels is which model each agent uses in one project.
type ProjectModels struct {
	Name  string                    `json:"name"`
	Key   string                    `json:"key"`
	Roles map[string]*models.Choice `json:"roles"`
	Waits map[string]string         `json:"waits"`
	// Own are the roles the project set itself.
	Own []string `json:"own"`
}

func (s *Server) machineDoc(ctx context.Context, pool *pgxpool.Pool) (settings.Doc, error) {
	return settings.Effective(ctx, pool, s.Machine)
}

func (s *Server) listModels(w http.ResponseWriter, r *http.Request) {
	pool := s.pool(w)
	if pool == nil {
		return
	}
	ctx := r.Context()
	doc, err := s.machineDoc(ctx, pool)
	if err != nil {
		s.boardError(w, "reading the settings", err)
		return
	}
	clis, err := s.cliModels(ctx, pool, doc)
	if err != nil {
		s.boardError(w, "reading the models", err)
		return
	}
	agents, err := models.Agents(ctx, pool, doc)
	if err != nil {
		s.boardError(w, "reading the agents", err)
		return
	}
	choices, err := models.Choices(ctx, pool, "")
	if err != nil {
		s.boardError(w, "reading Crew's choices", err)
		return
	}
	cmps, err := models.Comparisons(ctx, pool)
	if err != nil {
		s.boardError(w, "reading the comparisons", err)
		return
	}
	rules := models.RulesOf(doc)
	since := time.Now().AddDate(0, 0, -rules.WindowDays)
	var results []models.Numbers
	for _, role := range []string{"builder", "reviewer", "tester"} {
		n, err := models.Results(ctx, pool, role, since)
		if err != nil {
			s.boardError(w, "reading the results", err)
			return
		}
		results = append(results, n...)
	}
	projects, err := s.projectModels(ctx, pool, doc)
	if err != nil {
		s.boardError(w, "reading the projects' models", err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"clis": clis, "agents": agents, "choices": nonNil(choices), "comparisons": nonNil(cmps), "results": nonNil(results),
		"projects": nonNil(projects), "auto": map[string]any{"on": rules.On, "setByYou": nonNil(rules.SetByYou), "windowDays": rules.WindowDays,
			"minRuns": rules.MinRuns, "exploreShare": rules.ExploreShare, "compareRuns": rules.CompareRuns},
		"chosen": models.ChosenRoles, "autoRoles": models.AutoRoles,
	})
}

func nonNil[T any](l []T) []T {
	if l == nil {
		return []T{}
	}
	return l
}

func (s *Server) cliModels(ctx context.Context, pool *pgxpool.Pool, doc settings.Doc) ([]CLIModels, error) {
	rows, err := models.Rows(ctx, pool)
	if err != nil {
		return nil, err
	}
	windows, err := s.quotaWindows(ctx, pool, doc)
	if err != nil {
		return nil, err
	}
	inUse := settingsList(doc, "agents", "clis")
	var out []CLIModels
	for _, name := range cli.Names() {
		a, _ := cli.Get(name)
		c := CLIModels{CLI: name, InUse: len(inUse) == 0 || slices.Contains(inUse, name), Models: []ModelRow{}, Windows: []QuotaWindow{}}
		var state, errText *string
		if err := pool.QueryRow(ctx, `SELECT state, models_read_at, models_failures, models_error FROM cli_logins WHERE cli = $1`, name).
			Scan(&state, &c.Read, &c.Failures, &errText); err != nil && !errors.Is(err, pgx.ErrNoRows) {
			return nil, err
		}
		c.SignedIn = state != nil && *state == "signed_in"
		if errText != nil {
			c.Error = *errText
		}
		read := false
		for _, m := range rows {
			if m.CLI != name {
				continue
			}
			read = read || m.AddedBy == "cli"
			c.Models = append(c.Models, ModelRow{Name: m.Name, Model: m.Model, Efforts: nonNil(m.Efforts), InName: m.InName, Display: m.Display,
				Resolved: m.Resolved, Family: m.Family, Group: m.Group, GroupName: a.GroupName(m.Group), Offered: m.Offered, Refused: m.Refused,
				AddedBy: m.AddedBy, FirstSeen: m.FirstSeen, GoneAt: m.GoneAt})
		}
		if !read {
			c.Short = true
			for _, m := range a.ShortList() {
				g := a.LimitGroup(m.Model)
				c.Models = append(c.Models, ModelRow{Name: m.Name, Model: m.Model, Efforts: nonNil(m.Efforts), Family: cli.Family(m.Model), Group: g,
					GroupName: a.GroupName(g), Offered: true, AddedBy: "short list"})
			}
		}
		for _, wnd := range windows {
			if wnd.CLI == name {
				c.Windows = append(c.Windows, wnd)
			}
		}
		out = append(out, c)
	}
	return out, nil
}

func (s *Server) quotaWindows(ctx context.Context, pool *pgxpool.Pool, doc settings.Doc) ([]QuotaWindow, error) {
	ws, err := models.Windows(ctx, pool)
	if err != nil {
		return nil, err
	}
	slow, _ := doc["quota"].(settings.Doc)["slow_down_at"].(float64)
	running := map[string]int{}
	rows, err := pool.Query(ctx, `SELECT cli, coalesce(model_resolved, model, '') FROM runs WHERE state IN ('starting', 'working')`)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var c, m string
		if rows.Scan(&c, &m) == nil {
			if a, ok := cli.Get(c); ok {
				running[c+"/"+a.LimitGroup(m)]++
				if own := a.ModelLimit(m); own != "" {
					running[c+"/model:"+own]++
				}
			}
		}
	}
	rows.Close()
	out := []QuotaWindow{}
	now := time.Now()
	for _, x := range ws {
		a, ok := cli.Get(x.CLI)
		if !ok {
			continue
		}
		name := x.Group
		if !strings.HasPrefix(x.Group, "model:") {
			name = a.GroupName(x.Group)
		} else {
			name = strings.TrimPrefix(x.Group, "model:") + " alone"
		}
		q := QuotaWindow{CLI: x.CLI, Group: x.Group, GroupName: name, Window: x.Window, Used: x.Used, ResetsAt: x.ResetsAt, Source: x.Source,
			ReadAt: x.ReadAt, Running: running[x.CLI+"/"+x.Group]}
		if x.ReachedUntil != nil && x.ReachedUntil.After(now) {
			q.ReachedUntil = x.ReachedUntil
		}
		q.Close = slow > 0 && x.Used >= slow && (x.ResetsAt == nil || x.ResetsAt.After(now))
		out = append(out, q)
	}
	return out, nil
}

func (s *Server) quota(w http.ResponseWriter, r *http.Request) {
	pool := s.pool(w)
	if pool == nil {
		return
	}
	doc, err := s.machineDoc(r.Context(), pool)
	if err != nil {
		s.boardError(w, "reading the settings", err)
		return
	}
	ws, err := s.quotaWindows(r.Context(), pool, doc)
	if err != nil {
		s.boardError(w, "reading the limits", err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"windows": ws})
}

// projectModels is which model each agent would run on in each project now.
func (s *Server) projectModels(ctx context.Context, pool *pgxpool.Pool, doc settings.Doc) ([]ProjectModels, error) {
	b := s.Board.Load()
	if b == nil {
		return nil, nil
	}
	projects, err := b.Projects(ctx)
	if err != nil {
		return nil, err
	}
	var out []ProjectModels
	for _, p := range projects {
		if p.Hidden {
			continue // the gate's own project: no list shows it (16.2)
		}
		if p.State == "archived" {
			continue
		}
		own, err := b.Settings(ctx, pool, p)
		if err != nil {
			return nil, err
		}
		pm := ProjectModels{Name: p.Name, Key: p.Key, Roles: map[string]*models.Choice{}, Waits: map[string]string{}, Own: []string{}}
		agents, _ := own["agents"].(settings.Doc)
		for _, role := range []string{"curator", "builder", "reviewer", "tester", "verifier"} {
			if _, ok := agents[role].(settings.Doc); ok {
				pm.Own = append(pm.Own, role)
			}
			c, err := models.Choose(ctx, pool, models.Ask{Role: role, Machine: doc, Project: own})
			if wt, ok := models.IsWait(err); ok {
				pm.Waits[role] = wt.Reason
				continue
			}
			if err != nil {
				return nil, err
			}
			pm.Roles[role] = &c
		}
		out = append(out, pm)
	}
	return out, nil
}

func (s *Server) getModel(w http.ResponseWriter, r *http.Request) {
	pool := s.pool(w)
	if pool == nil {
		return
	}
	ctx := r.Context()
	name, model := r.PathValue("cli"), r.PathValue("model")
	if _, ok := cli.Get(name); !ok {
		writeError(w, http.StatusNotFound, "NOT_FOUND", "no such CLI")
		return
	}
	doc, err := s.machineDoc(ctx, pool)
	if err != nil {
		s.boardError(w, "reading the settings", err)
		return
	}
	rows, err := models.Rows(ctx, pool)
	if err != nil {
		s.boardError(w, "reading the models", err)
		return
	}
	var names []models.Row
	for _, m := range rows {
		if m.CLI == name && m.Model == model {
			names = append(names, m)
		}
	}
	agents, err := models.Agents(ctx, pool, doc)
	if err != nil {
		s.boardError(w, "reading the agents", err)
		return
	}
	entry := name + "/" + model
	var byDefault, listed []string
	for _, a := range agents {
		if a.RunsOn != nil && a.RunsOn.Entry() == entry {
			byDefault = append(byDefault, a.Role)
		}
		for _, e := range a.Entries {
			if e.Entry == entry {
				listed = append(listed, a.Role)
			}
		}
	}
	projects, err := s.projectModels(ctx, pool, doc)
	if err != nil {
		s.boardError(w, "reading the projects' models", err)
		return
	}
	var inProjects []map[string]any
	for _, p := range projects {
		for role, c := range p.Roles {
			if c.Entry() == entry {
				inProjects = append(inProjects, map[string]any{"project": p.Name, "role": role, "own": slices.Contains(p.Own, role)})
			}
		}
	}
	uses, latest, err := models.Uses(ctx, pool, name, model)
	if err != nil {
		s.boardError(w, "reading the model's runs", err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"cli": name, "model": model, "names": nonNil(names), "byDefault": nonNil(byDefault),
		"listedBy": nonNil(listed), "projects": nonNil(inProjects), "uses": nonNil(uses), "latest": nonNil(latest)})
}

// addModel keeps a name the operator adds beside what a CLI lists (12.3, agents.extra_models).
func (s *Server) addModel(w http.ResponseWriter, r *http.Request) {
	var req struct {
		CLI  string `json:"cli"`
		Name string `json:"name"`
	}
	if !readJSON(w, r, &req) {
		return
	}
	pool := s.pool(w)
	if pool == nil {
		return
	}
	a, ok := cli.Get(req.CLI)
	req.Name = strings.TrimSpace(req.Name)
	if !ok || !models.ValidName.MatchString(req.Name) {
		writeError(w, http.StatusBadRequest, "INVALID_ARGUMENT", "a CLI and a name the CLI takes, such as gemini-4-argon")
		return
	}
	who := r.Header.Get(HeaderEmail)
	err := s.updateMachine(r.Context(), pool, who, func(doc settings.Doc) error {
		agents := doc["agents"].(settings.Doc)
		extra, _ := agents["extra_models"].(settings.Doc)
		if extra == nil {
			extra = settings.Doc{}
		}
		names := settingsList(extra, a.Name())
		if !slices.Contains(names, req.Name) {
			names = append(names, req.Name)
		}
		list := make([]any, len(names))
		for i, n := range names {
			list[i] = n
		}
		extra[a.Name()] = list
		agents["extra_models"] = extra
		return nil
	})
	if err == nil {
		err = models.Add(r.Context(), pool, a.Name(), req.Name)
	}
	if err != nil {
		s.settingsError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// refreshModels reads every CLI's list again now, and offers again a name a CLI refused that its list
// still has (12.3).
func (s *Server) refreshModels(w http.ResponseWriter, r *http.Request) {
	pool := s.pool(w)
	if pool == nil {
		return
	}
	if err := models.ClearRefused(r.Context(), pool); err != nil {
		s.boardError(w, "offering the refused names again", err)
		return
	}
	if l := s.Logins.Load(); l != nil {
		for _, name := range cli.Names() {
			l.CheckNow(r.Context(), name, false)
		}
	}
	w.WriteHeader(http.StatusAccepted)
}

// setAgent gives an agent a model, on its CLI, and an effort: that entry goes first in the machine's
// list, and an agent Crew chooses for is Set by you from then on (12.3).
func (s *Server) setAgent(w http.ResponseWriter, r *http.Request) {
	role := r.PathValue("role")
	var req struct {
		Model  string `json:"model"`
		Effort string `json:"effort"`
	}
	if !readJSON(w, r, &req) {
		return
	}
	pool := s.pool(w)
	if pool == nil {
		return
	}
	ctx := r.Context()
	doc, err := s.machineDoc(ctx, pool)
	if err != nil {
		s.boardError(w, "reading the settings", err)
		return
	}
	if req.Model != "" {
		// Only what a signed-in CLI offers can be chosen (12.3).
		agents, err := models.Agents(ctx, pool, settings.Merge(doc, settings.Doc{"agents": settings.Doc{"roles": settings.Doc{
			role: settings.Doc{"model": []any{req.Model}, "effort": "high"}}}}))
		if err != nil {
			s.boardError(w, "reading the agents", err)
			return
		}
		ok := false
		for _, a := range agents {
			if a.Role == role && len(a.Entries) > 0 && a.Entries[0].Offered {
				ok = true
			}
		}
		if !ok {
			writeError(w, http.StatusBadRequest, "INVALID_ARGUMENT", req.Model+" is not offered by a CLI in use")
			return
		}
	}
	err = s.updateMachine(ctx, pool, r.Header.Get(HeaderEmail), func(doc settings.Doc) error {
		agents := doc["agents"].(settings.Doc)
		roles := agents["roles"].(settings.Doc)
		entry, ok := roles[role].(settings.Doc)
		if !ok {
			return errNoAgent
		}
		if req.Model != "" {
			list := []any{req.Model}
			for _, e := range settingsList(entry, "model") {
				if e != req.Model {
					list = append(list, e)
				}
			}
			entry["model"] = list
		}
		if req.Effort != "" {
			entry["effort"] = req.Effort
		}
		if slices.Contains(models.AutoRoles, role) {
			setByYou(agents, role, true)
		}
		return nil
	})
	if err != nil {
		s.settingsError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

var errNoAgent = errors.New("no such agent")

// setByYou puts an agent into agents.auto.set_by_you, or takes it out.
func setByYou(agents settings.Doc, role string, on bool) {
	auto, _ := agents["auto"].(settings.Doc)
	if auto == nil {
		auto = settings.Doc{}
		agents["auto"] = auto
	}
	var list []any
	for _, x := range settingsList(auto, "set_by_you") {
		if x != role {
			list = append(list, x)
		}
	}
	if on {
		list = append(list, role)
	}
	if list == nil {
		list = []any{}
	}
	auto["set_by_you"] = list
}

// setAgentChoice sets an agent Crew chooses for to Automatic, or to Set by you (12.3).
func (s *Server) setAgentChoice(w http.ResponseWriter, r *http.Request) {
	role := r.PathValue("role")
	var req struct {
		State string `json:"state"`
	}
	if !readJSON(w, r, &req) {
		return
	}
	if !slices.Contains(models.AutoRoles, role) || (req.State != "automatic" && req.State != "set_by_you") {
		writeError(w, http.StatusBadRequest, "INVALID_ARGUMENT", "an agent Crew chooses for, and automatic or set_by_you")
		return
	}
	pool := s.pool(w)
	if pool == nil {
		return
	}
	err := s.updateMachine(r.Context(), pool, r.Header.Get(HeaderEmail), func(doc settings.Doc) error {
		setByYou(doc["agents"].(settings.Doc), role, req.State == "set_by_you")
		return nil
	})
	if err != nil {
		s.settingsError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) chooser(pool *pgxpool.Pool) *models.Chooser {
	return &models.Chooser{Pool: pool, Machine: s.Machine}
}

// modelChoice is Go back on a change, and Use it and Not now on a recommendation (12.3).
func (s *Server) modelChoice(action string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
		if err != nil {
			writeError(w, http.StatusBadRequest, "INVALID_ARGUMENT", "no such choice")
			return
		}
		pool := s.pool(w)
		if pool == nil {
			return
		}
		c, who := s.chooser(pool), r.Header.Get(HeaderEmail)
		switch action {
		case "undo":
			err = c.GoBack(r.Context(), id, who)
		case "use":
			err = c.UseIt(r.Context(), id, who)
		default:
			err = c.NotNow(r.Context(), id, who)
		}
		switch {
		case errors.Is(err, pgx.ErrNoRows):
			writeError(w, http.StatusNotFound, "NOT_FOUND", "no such choice")
		case err != nil && strings.Contains(err.Error(), "that "):
			writeError(w, http.StatusConflict, "FAILED_PRECONDITION", err.Error())
		case err != nil:
			s.settingsError(w, err)
		default:
			w.WriteHeader(http.StatusNoContent)
		}
	}
}

func (s *Server) listComparisons(w http.ResponseWriter, r *http.Request) {
	pool := s.pool(w)
	if pool == nil {
		return
	}
	cmps, err := models.Comparisons(r.Context(), pool)
	if err != nil {
		s.boardError(w, "reading the comparisons", err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"comparisons": nonNil(cmps)})
}

func (s *Server) startComparison(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Role   string `json:"role"`
		Model  string `json:"model"`
		Effort string `json:"effort"`
	}
	if !readJSON(w, r, &req) {
		return
	}
	pool := s.pool(w)
	if pool == nil {
		return
	}
	id, err := s.chooser(pool).StartComparison(r.Context(), req.Role, models.Pick{Entry: req.Model, Effort: req.Effort})
	if err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_ARGUMENT", err.Error())
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{"id": id})
}

func (s *Server) stopComparison(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_ARGUMENT", "no such comparison")
		return
	}
	pool := s.pool(w)
	if pool == nil {
		return
	}
	if err := s.chooser(pool).StopComparison(r.Context(), id); errors.Is(err, pgx.ErrNoRows) {
		writeError(w, http.StatusNotFound, "NOT_FOUND", "no running comparison of that number")
		return
	} else if err != nil {
		s.boardError(w, "stopping the comparison", err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// setLeadSlots sets max_concurrent_runs (7.9).
func (s *Server) setLeadSlots(w http.ResponseWriter, r *http.Request) {
	var req struct {
		CLI   string `json:"cli"`
		Slots int    `json:"slots"`
	}
	if !readJSON(w, r, &req) {
		return
	}
	if req.CLI != "" {
		if _, ok := cli.Get(req.CLI); !ok {
			writeError(w, http.StatusBadRequest, "INVALID_ARGUMENT", "no such CLI")
			return
		}
	}
	if req.Slots < 1 || req.Slots > 64 {
		writeError(w, http.StatusBadRequest, "INVALID_ARGUMENT", "from 1 to 64 concurrent runs")
		return
	}
	pool := s.pool(w)
	if pool == nil {
		return
	}
	err := s.updateMachine(r.Context(), pool, r.Header.Get(HeaderEmail), func(doc settings.Doc) error {
		doc["max_concurrent_runs"] = req.Slots
		delete(doc, "lead_slots")
		return nil
	})
	if err != nil {
		s.settingsError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// setActiveTickets sets max_active_tickets across all projects (7.9, R52).
func (s *Server) setActiveTickets(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Max int `json:"max"`
	}
	if !readJSON(w, r, &req) {
		return
	}
	if req.Max < 1 || req.Max > 64 {
		writeError(w, http.StatusBadRequest, "INVALID_ARGUMENT", "from 1 to 64 active tickets")
		return
	}
	pool := s.pool(w)
	if pool == nil {
		return
	}
	err := s.updateMachine(r.Context(), pool, r.Header.Get(HeaderEmail), func(doc settings.Doc) error {
		doc["max_active_tickets"] = req.Max
		return nil
	})
	if err != nil {
		s.settingsError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// updateMachine changes the machine's settings as they apply, under the settings' lock, and stores
// them with their history.
func (s *Server) updateMachine(ctx context.Context, pool *pgxpool.Pool, who string, change func(settings.Doc) error) error {
	return settings.Update(ctx, pool, s.Machine, who, change)
}

func (s *Server) settingsError(w http.ResponseWriter, err error) {
	if s.writeStale(w, err) {
		return
	}
	var inv *settings.InvalidError
	switch {
	case errors.Is(err, errNoAgent):
		writeError(w, http.StatusNotFound, "NOT_FOUND", "no such agent")
	// A key that is no setting, or a change that is not one of the key's lines (14.2).
	case errors.Is(err, settings.ErrNoSuchKey), errors.Is(err, settings.ErrNoSuchChange):
		writeError(w, http.StatusNotFound, "NOT_FOUND", err.Error())
	case errors.As(err, &inv):
		writeError(w, http.StatusBadRequest, "INVALID_ARGUMENT", inv.Error())
	default:
		s.Log.Error("changing the settings", "error", err)
		writeError(w, http.StatusServiceUnavailable, "UNAVAILABLE", "the settings could not be saved")
	}
}

// settingsList reads a list of words from a settings document by its path.
func settingsList(d settings.Doc, path ...string) []string {
	var cur any = d
	for _, k := range path {
		m, ok := cur.(settings.Doc)
		if !ok {
			return nil
		}
		cur = m[k]
	}
	var out []string
	if l, ok := cur.([]any); ok {
		for _, x := range l {
			if s, ok := x.(string); ok {
				out = append(out, s)
			}
		}
	}
	return out
}

// leadPanel is the front page's lead slots and the queue of lead runs that wait for one (7.9).
type leadPanel struct {
	Slots []leadCLI           `json:"slots"`
	Queue []planning.LeadWait `json:"queue"`
}

type leadCLI struct {
	CLI   string `json:"cli"`
	Slots int    `json:"slots"`
	// Set is lead_slots; Slots is lower while the guard holds the CLI back near a limit.
	Set  int       `json:"set"`
	Runs []leadRun `json:"runs"`
}

type leadRun struct {
	Run     string `json:"run"`
	Project string `json:"project,omitempty"`
	Ticket  string `json:"ticket,omitempty"`
	Title   string `json:"title"`
}

func (s *Server) leadPanel(ctx context.Context, pool *pgxpool.Pool, doc settings.Doc) (leadPanel, error) {
	p := leadPanel{Slots: []leadCLI{}, Queue: []planning.LeadWait{}}
	now, err := planning.Slots(ctx, pool, doc)
	if err != nil {
		return p, err
	}
	set := planning.ConfiguredSlots(doc)
	for _, name := range cli.Names() {
		lc := leadCLI{CLI: name, Slots: now[name], Set: set, Runs: []leadRun{}}
		rows, err := pool.Query(ctx, `SELECT r.id::text, coalesce(p.name, ''), coalesce(p.key || '-' || t.number, ''), coalesce(s.title, s.kind)
			FROM runs r JOIN subtasks s ON s.id = r.subtask LEFT JOIN projects p ON p.id = s.project LEFT JOIN tickets t ON t.id = s.ticket
			WHERE r.state IN ('starting', 'working') AND (s.kind <> 'helper' OR s.parent IS NULL)
			  AND r.cli = $1 ORDER BY r.started_at`, name)
		if err != nil {
			return p, err
		}
		for rows.Next() {
			var lr leadRun
			if rows.Scan(&lr.Run, &lr.Project, &lr.Ticket, &lr.Title) == nil {
				lc.Runs = append(lc.Runs, lr)
			}
		}
		rows.Close()
		p.Slots = append(p.Slots, lc)
	}
	q, err := planning.Queue(ctx, pool, doc)
	if err != nil {
		return p, err
	}
	p.Queue = nonNil(q)
	return p, nil
}
