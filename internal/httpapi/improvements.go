package httpapi

import (
	"context"
	"errors"
	"net/http"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/kesokaj/crew/baseline"
	"github.com/kesokaj/crew/internal/board"
	"github.com/kesokaj/crew/internal/improve"
	"github.com/kesokaj/crew/internal/settings"
	"github.com/kesokaj/crew/internal/version"
)

// The Crew level's improvements and its baseline (10.3 to 10.5, 10.7), for the operator.
func (s *Server) improvementRoutes(api *routes) {
	api.HandleFunc("GET /api/crew/improvements", s.listImprovements)
	api.HandleFunc("GET /api/crew/improvements/{n}", s.getImprovement)
	api.HandleFunc("POST /api/crew/improvements/{n}", s.decideImprovement)
	api.HandleFunc("POST /api/crew/improver/run", s.runImprover)
	api.HandleFunc("GET /api/crew/baseline", s.getBaseline)
	api.HandleFunc("GET /api/crew/baseline/file", s.getGlobalBaselineFile)
	api.HandleFunc("PUT /api/crew/baseline/file", s.saveGlobalBaselineFile)
	api.HandleFunc("DELETE /api/crew/baseline/file", s.deleteGlobalBaselineFile)
}

func (s *Server) improvements(w http.ResponseWriter) *improve.Service {
	pool := s.pool(w)
	if pool == nil {
		return nil
	}
	b := s.board(w)
	if b == nil {
		return nil
	}
	return &improve.Service{Pool: pool, Board: b, Running: version.Version,
		Settings: func(ctx context.Context) (settings.Doc, error) { return settings.Effective(ctx, pool, s.Machine) }}
}

func (s *Server) improvementError(w http.ResponseWriter, err error) {
	var r *improve.Refusal
	switch {
	case errors.Is(err, improve.ErrNotFound):
		writeError(w, http.StatusNotFound, "NOT_FOUND", "no such improvement")
	case errors.As(err, &r):
		writeError(w, http.StatusBadRequest, "FAILED_PRECONDITION", r.Msg)
	default:
		s.boardError(w, "the improvements", err)
	}
}

// improvementLine is an improvement as the list shows it, without its patch.
type improvementLine struct {
	*improve.Improvement
	Patch any `json:"patch,omitempty"`
}

func (s *Server) listImprovements(w http.ResponseWriter, r *http.Request) {
	svc := s.improvements(w)
	if svc == nil {
		return
	}
	list, err := svc.List(r.Context())
	if err != nil {
		s.improvementError(w, err)
		return
	}
	out := []improvementLine{}
	for _, x := range list {
		out = append(out, improvementLine{Improvement: x})
	}
	var last *time.Time
	if err := svc.Pool.QueryRow(r.Context(), `SELECT max(r.started_at) FROM runs r JOIN subtasks s ON s.id = r.subtask WHERE s.scope = 'improver'`).Scan(&last); err != nil {
		s.improvementError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"improvements": out, "improverLastRun": last})
}

// FileChange is one file of an improvement: the text it has now and the text the patch gives it.
type FileChange struct {
	Path   string `json:"path"`
	Before string `json:"before"`
	After  string `json:"after"`
}

// ProjectEvals are the numbers one project's evals gave an improvement.
type ProjectEvals struct {
	Project string `json:"project"`
	Cases   int    `json:"cases"`
	Result  any    `json:"result"`
}

func (s *Server) getImprovement(w http.ResponseWriter, r *http.Request) {
	svc := s.improvements(w)
	if svc == nil {
		return
	}
	n, ok := number(w, r, "improvement")
	if !ok {
		return
	}
	x, err := svc.Get(r.Context(), n)
	if err != nil {
		s.improvementError(w, err)
		return
	}
	files := []FileChange{}
	for _, path := range x.Target {
		before, _ := improve.Current(path)
		files = append(files, FileChange{Path: path, Before: before, After: x.Patch[path]})
	}
	rows, err := svc.Pool.Query(r.Context(), `SELECT p.name, e.cases, e.result FROM improvement_evals e JOIN projects p ON p.id = e.project
		WHERE e.improvement = $1 ORDER BY p.name`, x.ID)
	if err != nil {
		s.improvementError(w, err)
		return
	}
	evals, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (ProjectEvals, error) {
		var e ProjectEvals
		return e, row.Scan(&e.Project, &e.Cases, &e.Result)
	})
	if err != nil {
		s.improvementError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"improvement": x, "files": files, "projectEvals": orEmpty(evals)})
}

func (s *Server) decideImprovement(w http.ResponseWriter, r *http.Request) {
	svc := s.improvements(w)
	if svc == nil {
		return
	}
	n, ok := number(w, r, "improvement")
	if !ok {
		return
	}
	var req struct {
		Choice string `json:"choice"`
	}
	if !decode(w, r, &req) {
		return
	}
	x, err := svc.Decide(r.Context(), n, req.Choice, "operator")
	if err != nil {
		s.improvementError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, x)
}

// runImprover asks for the improver now: it runs on the pipeline's next pass, when a slot is free
// (10.4; since 0.53 no weekly budget).
func (s *Server) runImprover(w http.ResponseWriter, r *http.Request) {
	pool := s.pool(w)
	if pool == nil {
		return
	}
	if _, err := pool.Exec(r.Context(), `UPDATE crew_state SET improver_asked = now() WHERE id = 1`); err != nil {
		s.boardError(w, "asking for the improver", err)
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]any{"asked": true})
}

// BaselineFile is a file of the baseline or the harness, with whether an improvement may change it.
type BaselineFile struct {
	Path       string `json:"path"`
	MayChange  bool   `json:"mayChange"`
	Note       string `json:"note,omitempty"`
	Customized bool   `json:"customized,omitempty"`
}

// BaselineVersion is a Crew version with what it changed.
type BaselineVersion struct {
	Version   string     `json:"version"`
	Installed *time.Time `json:"installed"`
	Running   bool       `json:"running"`
	Note      string     `json:"note"`
}

// getBaseline is what every project runs on (10.3): its files, and the versions with what each changed.
func (s *Server) getBaseline(w http.ResponseWriter, r *http.Request) {
	pool := s.pool(w)
	if pool == nil {
		return
	}
	harnessFiles, customSet, err := board.ListGlobalHarnessFiles(r.Context(), pool)
	if err != nil {
		s.boardError(w, "reading the global harness", err)
		return
	}
	files := []BaselineFile{}
	for _, p := range baseline.Paths() {
		f := BaselineFile{Path: "baseline/" + p, Customized: customSet["baseline/"+p]}
		switch {
		case baseline.Checking(p):
			f.Note = "a role that checks"
		case baseline.PromptWriter(p):
			f.MayChange, f.Note = true, "waits for you"
		case baseline.Overridable(p):
			f.MayChange = true
		default:
			f.Note = "not text for agents"
		}
		files = append(files, f)
	}
	harness := map[string]int{}
	for _, it := range harnessFiles {
		if it.Kind != "context" {
			harness[it.Kind]++
		}
	}
	rows, err := pool.Query(r.Context(), `SELECT r.version, r.installed_at, coalesce((SELECT i.title FROM improvements i WHERE i.release_to = r.version
			ORDER BY i.id DESC LIMIT 1), '') FROM releases r ORDER BY coalesce(r.installed_at, r.found_at) DESC LIMIT 20`)
	if err != nil {
		s.boardError(w, "reading the versions", err)
		return
	}
	versions, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (BaselineVersion, error) {
		var v BaselineVersion
		err := row.Scan(&v.Version, &v.Installed, &v.Note)
		v.Running = v.Version == version.Version
		return v, err
	})
	if err != nil {
		s.boardError(w, "reading the versions", err)
		return
	}
	// A version that carried no improvement is titled by its release: the title given at its build,
	// else its first change, as crew--baseline.png shows every version with a line (M15 on dave
	// showed only the number).
	if vs := s.Versions.Load(); vs != nil {
		for i := range versions {
			if versions[i].Note == "" {
				versions[i].Note = vs.Title(versions[i].Version)
			}
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"version": version.Version, "files": files, "harness": harness, "harnessFiles": orEmpty(harnessFiles), "versions": orEmpty(versions)})
}
