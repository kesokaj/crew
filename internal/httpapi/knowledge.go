package httpapi

import (
	"net/http"
	"strconv"

	harnesstree "github.com/kesokaj/crew/harness"
	"github.com/kesokaj/crew/internal/board"
)

// The routes of M12 for a project's knowledge (9, 15.4): memory, bugs, and feedback, each read, and
// written by the operator.
func (s *Server) knowledgeRoutes(api *routes) {
	api.HandleFunc("GET /api/projects/{p}/memory", s.listMemory)
	api.HandleFunc("POST /api/projects/{p}/memory", s.addMemory)
	api.HandleFunc("PATCH /api/projects/{p}/memory/{n}", s.patchMemory)
	api.HandleFunc("GET /api/projects/{p}/bugs", s.listBugs)
	api.HandleFunc("POST /api/projects/{p}/bugs", s.addBug)
	api.HandleFunc("POST /api/projects/{p}/bugs/{n}/close", s.closeBug)
	api.HandleFunc("GET /api/projects/{p}/feedback", s.listFeedback)
	api.HandleFunc("POST /api/projects/{p}/feedback", s.addFeedback)
	api.HandleFunc("POST /api/projects/{p}/feedback/{n}/resolve", s.resolveFeedback)
	api.HandleFunc("GET /api/projects/{p}/docs", s.listDocs)
	api.HandleFunc("GET /api/projects/{p}/docs/page", s.getDoc)
	api.HandleFunc("PUT /api/projects/{p}/docs/page", s.editDoc)
	api.HandleFunc("GET /api/projects/{p}/docs/search", s.searchDocs)
	api.HandleFunc("GET /api/projects/{p}/skills", s.listSkills)
	api.HandleFunc("GET /api/projects/{p}/skills/file", s.getProjectSkillFile)
	api.HandleFunc("PUT /api/projects/{p}/skills/file", s.saveProjectSkillFile)
	api.HandleFunc("DELETE /api/projects/{p}/skills/file", s.deleteProjectSkillFile)
	api.HandleFunc("POST /api/projects/{p}/skills/request", s.requestProjectSkill)
}

// number is the {n} of a route, or a 404.
func number(w http.ResponseWriter, r *http.Request, what string) (int, bool) {
	n, err := strconv.Atoi(r.PathValue("n"))
	if err != nil || n < 1 {
		writeError(w, http.StatusNotFound, "NOT_FOUND", "no such "+what)
		return 0, false
	}
	return n, true
}

func (s *Server) listMemory(w http.ResponseWriter, r *http.Request) {
	b, p := s.project(w, r)
	if p == nil {
		return
	}
	list, err := b.Memory(r.Context(), p, true)
	if err != nil {
		s.boardError(w, "reading the memory", err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"entries": orEmpty(list)})
}

// addMemory is an entry the operator writes: active at once, and no agent changes it (9.3).
func (s *Server) addMemory(w http.ResponseWriter, r *http.Request) {
	b, p := s.project(w, r)
	if p == nil {
		return
	}
	var req board.NewEntry
	if !decode(w, r, &req) {
		return
	}
	e, err := b.Remember(r.Context(), p, req, board.MemorySource{}, board.Operator)
	if err != nil {
		s.boardError(w, "saving the entry", err)
		return
	}
	s.changed(w, b, http.StatusCreated, e)
}

// patchMemory is the operator's edit of an entry, or its retire with a reason (9.3, 21).
func (s *Server) patchMemory(w http.ResponseWriter, r *http.Request) {
	b, p := s.project(w, r)
	if p == nil {
		return
	}
	n, ok := number(w, r, "memory entry")
	if !ok {
		return
	}
	var req board.MemoryChange
	if !decode(w, r, &req) {
		return
	}
	e, err := b.ChangeMemory(r.Context(), p, n, req, board.Operator)
	if err != nil {
		s.boardError(w, "changing the entry", err)
		return
	}
	s.changed(w, b, http.StatusOK, e)
}

func (s *Server) listBugs(w http.ResponseWriter, r *http.Request) {
	b, p := s.project(w, r)
	if p == nil {
		return
	}
	list, err := b.Bugs(r.Context(), p)
	if err != nil {
		s.boardError(w, "reading the bugs", err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"bugs": orEmpty(list)})
}

// addBug is the operator's report: the bug and its fix ticket, which Analysis makes ready (9.4).
func (s *Server) addBug(w http.ResponseWriter, r *http.Request) {
	b, p := s.project(w, r)
	if p == nil {
		return
	}
	var req board.NewBug
	if !decode(w, r, &req) {
		return
	}
	bug, fix, err := b.ReportBug(r.Context(), p, req, board.BugFound{By: board.Operator}, board.OriginOperator, board.Operator)
	if err != nil {
		s.boardError(w, "reporting the bug", err)
		return
	}
	s.changed(w, b, http.StatusCreated, map[string]any{"bug": bug, "fixTicket": fix.Key})
}

func (s *Server) closeBug(w http.ResponseWriter, r *http.Request) {
	b, p := s.project(w, r)
	if p == nil {
		return
	}
	n, ok := number(w, r, "bug")
	if !ok {
		return
	}
	var req struct {
		Reason string `json:"reason"`
		Note   string `json:"note"`
	}
	if !decode(w, r, &req) {
		return
	}
	bug, err := b.CloseBug(r.Context(), p, n, req.Reason, req.Note, board.Operator)
	if err != nil {
		s.boardError(w, "closing the bug", err)
		return
	}
	s.changed(w, b, http.StatusOK, bug)
}

func (s *Server) listFeedback(w http.ResponseWriter, r *http.Request) {
	b, p := s.project(w, r)
	if p == nil {
		return
	}
	list, err := b.FeedbackList(r.Context(), p, false)
	if err != nil {
		s.boardError(w, "reading the feedback", err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"feedback": orEmpty(list)})
}

// addFeedback is the operator's feedback (9.5); a rule is a memory entry at once.
func (s *Server) addFeedback(w http.ResponseWriter, r *http.Request) {
	b, p := s.project(w, r)
	if p == nil {
		return
	}
	var req board.NewFeedback
	if !decode(w, r, &req) {
		return
	}
	f, err := b.GiveFeedback(r.Context(), p, req, board.FeedbackFrom{Role: board.Operator})
	if err != nil {
		s.boardError(w, "saving the feedback", err)
		return
	}
	s.changed(w, b, http.StatusCreated, f)
}

// resolveFeedback is the operator's answer to a piece of feedback: applied as a memory entry or a
// ticket, or dismissed with a reason, also feedback about the lead.
func (s *Server) resolveFeedback(w http.ResponseWriter, r *http.Request) {
	b, p := s.project(w, r)
	if p == nil {
		return
	}
	n, ok := number(w, r, "feedback")
	if !ok {
		return
	}
	var req struct {
		Outcome string `json:"outcome"`
		Reason  string `json:"reason"`
	}
	if !decode(w, r, &req) {
		return
	}
	f, err := b.ResolveFeedback(r.Context(), p, n, req.Outcome, req.Reason, board.Operator)
	if err != nil {
		s.boardError(w, "resolving the feedback", err)
		return
	}
	s.changed(w, b, http.StatusOK, f)
}

func orEmpty[T any](l []T) []T {
	if l == nil {
		return []T{}
	}
	return l
}

// indexed indexes a project's pages when main moved, and says which main they are of.
func (s *Server) indexed(w http.ResponseWriter, r *http.Request, b *board.Board, p *board.Project) (string, bool) {
	if err := b.IndexDocs(r.Context(), p); err != nil {
		s.boardError(w, "indexing the documentation", err)
		return "", false
	}
	var main *string
	if err := b.Pool.QueryRow(r.Context(), `SELECT docs_indexed FROM projects WHERE id = $1`, p.ID).Scan(&main); err != nil {
		s.boardError(w, "reading the documentation", err)
		return "", false
	}
	if main == nil {
		return "", true
	}
	return *main, true
}

// listDocs is the Documentation screen's tree (9.9): every page with its kind, what it covers, its last
// change, and whether it is current.
func (s *Server) listDocs(w http.ResponseWriter, r *http.Request) {
	b, p := s.project(w, r)
	if p == nil {
		return
	}
	main, ok := s.indexed(w, r, b, p)
	if !ok {
		return
	}
	pages, err := b.Docs(r.Context(), p)
	if err != nil {
		s.boardError(w, "reading the documentation", err)
		return
	}
	current := 0
	for _, d := range pages {
		if d.Current() {
			current++
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"main": main, "pages": orEmpty(pages), "current": current})
}

func (s *Server) getDoc(w http.ResponseWriter, r *http.Request) {
	b, p := s.project(w, r)
	if p == nil {
		return
	}
	main, ok := s.indexed(w, r, b, p)
	if !ok {
		return
	}
	d, err := b.Doc(r.Context(), p, r.URL.Query().Get("path"))
	if err != nil {
		s.boardError(w, "reading the page", err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"main": main, "page": d})
}

// editDoc is the operator's edit of a page: a commit on main with the operator as its author, from
// the main the page was read at (9.9, 21).
func (s *Server) editDoc(w http.ResponseWriter, r *http.Request) {
	b, p := s.project(w, r)
	if p == nil {
		return
	}
	var req struct {
		Path    string `json:"path"`
		Base    string `json:"base"`
		Content string `json:"content"`
	}
	if !decode(w, r, &req) {
		return
	}
	if b.Identity == nil {
		writeError(w, http.StatusServiceUnavailable, "UNAVAILABLE", "the git identity is not set up: set it up in Setup")
		return
	}
	id, err := b.Identity(r.Context())
	if err != nil {
		writeError(w, http.StatusServiceUnavailable, "UNAVAILABLE", "the git identity is not set up: set it up in Setup")
		return
	}
	commit, err := b.EditPage(r.Context(), p, req.Path, req.Base, req.Content, id)
	if err != nil {
		s.boardError(w, "saving the page", err)
		return
	}
	d, err := b.Doc(r.Context(), p, req.Path)
	if err != nil {
		s.boardError(w, "reading the page", err)
		return
	}
	s.changed(w, b, http.StatusOK, map[string]any{"main": commit, "page": d})
}

func (s *Server) searchDocs(w http.ResponseWriter, r *http.Request) {
	b, p := s.project(w, r)
	if p == nil {
		return
	}
	if _, ok := s.indexed(w, r, b, p); !ok {
		return
	}
	hits, err := b.SearchDocs(r.Context(), p, r.URL.Query().Get("q"), 50)
	if err != nil {
		s.boardError(w, "searching the documentation", err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"pages": orEmpty(hits)})
}

// listSkills is the Settings screen's skills and context (12.9): the base layer, the project's own
// layer on main, and the skill tickets agents asked for, read only.
func (s *Server) listSkills(w http.ResponseWriter, r *http.Request) {
	b, p := s.project(w, r)
	if p == nil {
		return
	}
	layer, err := b.ProjectLayer(r.Context(), p)
	if err != nil {
		s.boardError(w, "reading the project's skills", err)
		return
	}
	type request struct {
		Ticket   string               `json:"ticket"`
		Title    string               `json:"title"`
		Stage    string               `json:"stage"`
		Requests []board.SkillRequest `json:"requests"`
	}
	tickets, err := b.Tickets(r.Context(), p)
	if err != nil {
		s.boardError(w, "reading the skill tickets", err)
		return
	}
	asked := []request{}
	for _, t := range tickets {
		if !t.Skill {
			continue
		}
		rs, err := b.SkillRequests(r.Context(), t)
		if err != nil {
			s.boardError(w, "reading the skill tickets", err)
			return
		}
		asked = append(asked, request{Ticket: t.Key, Title: t.Title, Stage: t.Stage, Requests: orEmpty(rs)})
	}
	custom, _, _ := board.CustomHarnessMap(r.Context(), b.Pool, "harness/")
	harnessCustom := map[string]string{}
	for pth, c := range custom {
		if len(pth) > len("harness/") {
			harnessCustom[pth[len("harness/"):]] = c
		}
	}
	base := harnesstree.BaseWithOverrides(harnessCustom)
	counts := map[string]int{}
	for _, it := range base {
		counts[it.Kind]++
	}
	writeJSON(w, http.StatusOK, map[string]any{"base": base, "baseCounts": counts, "project": layer, "asked": asked})
}
