package httpapi

import (
	"net/http"

	"github.com/kesokaj/crew/internal/board"
	"github.com/kesokaj/crew/internal/settings"
)

// Crew's project (16.2, since 0.51): one button on the Crew level imports updates.source_repo as a
// project and sets improvements.crew_project to it.
func (s *Server) crewProjectRoutes(api *routes) {
	api.HandleFunc("GET /api/crew/project", s.getCrewProject)
	api.HandleFunc("POST /api/crew/project", s.makeCrewProject)
}

// crewSettings are the two settings Crew's project stands on.
func crewSettings(doc settings.Doc) (project, repo string) {
	imp, _ := doc["improvements"].(settings.Doc)
	up, _ := doc["updates"].(settings.Doc)
	project, _ = imp["crew_project"].(string)
	repo, _ = up["source_repo"].(string)
	return project, repo
}

func (s *Server) getCrewProject(w http.ResponseWriter, r *http.Request) {
	pool := s.pool(w)
	if pool == nil {
		return
	}
	doc, err := s.machineDoc(r.Context(), pool)
	if err != nil {
		s.boardError(w, "reading the settings", err)
		return
	}
	project, repo := crewSettings(doc)
	writeJSON(w, http.StatusOK, map[string]any{"project": nullable(project), "sourceRepo": nullable(repo)})
}

func nullable(s string) any {
	if s == "" {
		return nil
	}
	return s
}

// makeCrewProject is Make Crew's project: the repository updates.source_repo names, imported, and
// improvements.crew_project set to it. A second is refused.
func (s *Server) makeCrewProject(w http.ResponseWriter, r *http.Request) {
	b := s.board(w)
	if b == nil {
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
	current, repo := crewSettings(doc)
	src := board.GitHubSource(repo)
	if s.CrewSource != nil {
		src = s.CrewSource(repo)
	}
	who := r.Header.Get(HeaderEmail)
	p, err := b.MakeCrewProject(ctx, src, current, who)
	if err != nil {
		s.boardError(w, "making Crew's project", err)
		return
	}
	if err := s.updateMachine(ctx, pool, who, func(doc settings.Doc) error {
		doc["improvements"].(settings.Doc)["crew_project"] = p.Name
		return nil
	}); err != nil {
		s.settingsError(w, err)
		return
	}
	s.changed(w, b, http.StatusCreated, p)
}
