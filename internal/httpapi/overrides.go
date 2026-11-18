package httpapi

import (
	"net/http"

	"github.com/kesokaj/crew/baseline"
	"github.com/kesokaj/crew/internal/board"
)

// A project's overrides of the baseline (10.3, 9.6): what it applies, what waits, and the files it may
// replace. The operator's own override applies at once; the operator answers one that waits.
func (s *Server) overrideRoutes(api *routes) {
	api.HandleFunc("GET /api/projects/{p}/overrides", s.listOverrides)
	api.HandleFunc("POST /api/projects/{p}/overrides", s.addOverride)
	api.HandleFunc("POST /api/projects/{p}/overrides/{n}", s.decideOverride)
}

// OverrideFile is a file of the baseline a project may replace, with whether a change of it waits for
// the operator.
type OverrideFile struct {
	Path  string `json:"path"`
	Waits bool   `json:"waits"`
}

func (s *Server) listOverrides(w http.ResponseWriter, r *http.Request) {
	b, p := s.project(w, r)
	if p == nil {
		return
	}
	list, err := b.Overrides(r.Context(), p)
	if err != nil {
		s.boardError(w, "reading the overrides", err)
		return
	}
	files := []OverrideFile{}
	for _, path := range baseline.Paths() {
		if baseline.Overridable(path) {
			files = append(files, OverrideFile{Path: path, Waits: baseline.WaitsForOperator(path)})
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"overrides": list, "files": files})
}

func (s *Server) addOverride(w http.ResponseWriter, r *http.Request) {
	b, p := s.project(w, r)
	if p == nil {
		return
	}
	var req struct {
		Path string `json:"path"`
		Text string `json:"text"`
		Why  string `json:"why"`
	}
	if !decode(w, r, &req) {
		return
	}
	o, err := b.ProposeOverride(r.Context(), p, req.Path, req.Text, req.Why, board.Operator, "")
	if err != nil {
		s.boardError(w, "saving the override", err)
		return
	}
	s.changed(w, b, http.StatusCreated, o)
}

func (s *Server) decideOverride(w http.ResponseWriter, r *http.Request) {
	b, p := s.project(w, r)
	if p == nil {
		return
	}
	n, ok := number(w, r, "override")
	if !ok {
		return
	}
	var req struct {
		Choice string `json:"choice"`
		Why    string `json:"why"`
	}
	if !decode(w, r, &req) {
		return
	}
	o, err := b.DecideOverride(r.Context(), p, n, req.Choice, req.Why, board.Operator)
	if err != nil {
		s.boardError(w, "deciding the override", err)
		return
	}
	s.changed(w, b, http.StatusOK, o)
}
