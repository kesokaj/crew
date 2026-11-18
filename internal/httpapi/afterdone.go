package httpapi

import (
	"context"
	"net/http"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/kesokaj/crew/internal/board"
)

// After Done (7.13, 15.4): revert a merged ticket, amend the plan, protect a project from being
// archived or deleted, and delete a project. Archive is POST /api/projects/{p}/archive.
func (s *Server) afterDoneRoutes(api *routes) {
	api.HandleFunc("POST /api/projects/{p}/tasks/{t}/revert", s.revertTask)
	api.HandleFunc("POST /api/projects/{p}/amendments", s.addAmendment)
	api.HandleFunc("PUT /api/projects/{p}/protected", s.putProtected)
	api.HandleFunc("DELETE /api/projects/{p}", s.deleteProject)
}

func (s *Server) putProtected(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Protected bool `json:"protected"`
	}
	if b, p := s.project(w, r); p != nil && decode(w, r, &req) {
		if p, err := b.SetProtected(r.Context(), p.ID, req.Protected, board.Operator); err != nil {
			s.boardError(w, "protecting the project", err)
		} else {
			s.changed(w, b, http.StatusOK, p)
		}
	}
}

func (s *Server) revertTask(w http.ResponseWriter, r *http.Request) {
	b := s.board(w)
	if b == nil {
		return
	}
	p, err := b.Project(r.Context(), r.PathValue("p"))
	if err != nil {
		s.boardError(w, "reading the project", err)
		return
	}
	t, err := b.Revert(r.Context(), p, r.PathValue("t"), board.Operator)
	if err != nil {
		s.boardError(w, "reverting the ticket", err)
		return
	}
	s.changed(w, b, http.StatusCreated, t)
}

func (s *Server) addAmendment(w http.ResponseWriter, r *http.Request) {
	b := s.board(w)
	if b == nil {
		return
	}
	var req struct {
		Text string `json:"text"`
	}
	if !decode(w, r, &req) {
		return
	}
	am, err := b.AddAmendment(r.Context(), r.PathValue("p"), req.Text, board.Operator)
	if err != nil {
		s.boardError(w, "amending the plan", err)
		return
	}
	if pl := s.Planner.Load(); pl != nil {
		pl.Kick()
	}
	s.changed(w, b, http.StatusCreated, am)
}

// deleteWait is how long a delete waits for the project's runs to end.
var deleteWait = time.Minute

// deleteProject deletes a project when its name is typed (7.13): it is archived first, so nothing new
// starts; its runs are stopped and awaited; its rows go in one transaction; its repositories after
// that. A repository that cannot be removed is said, and the rows are gone already.
func (s *Server) deleteProject(w http.ResponseWriter, r *http.Request) {
	b := s.board(w)
	if b == nil {
		return
	}
	var req struct {
		Name string `json:"name"`
	}
	if !decode(w, r, &req) {
		return
	}
	ctx := r.Context()
	p, err := b.Project(ctx, r.PathValue("p"))
	if err != nil {
		s.boardError(w, "reading the project", err)
		return
	}
	// Every refusal comes before the project is touched.
	if err := b.MayDelete(ctx, p, req.Name); err != nil {
		s.boardError(w, "deleting the project", err)
		return
	}
	if p.State != "archived" {
		if p, err = b.SetState(ctx, p.ID, "archive", "", board.Operator); err != nil {
			s.boardError(w, "archiving the project before it is deleted", err)
			return
		}
	}
	if d := s.Runs.Load(); d != nil {
		if err := stopProjectRuns(ctx, b, p, d.Stop); err != nil {
			s.boardError(w, "stopping the project's runs", err)
			return
		}
	}
	// Hosting goes first, while its rows say what it holds: the sandbox, the images, the data, the
	// values (plan M19, Delete).
	if h := s.Hosting.Load(); h != nil {
		if err := h.Forget(ctx, p.ID); err != nil {
			s.boardError(w, "removing the project's hosting", err)
			return
		}
	}
	if _, err := b.Delete(ctx, p.ID, req.Name, board.Operator); err != nil {
		s.boardError(w, "deleting the project", err)
		return
	}
	// The rows are gone; a repository that cannot be removed now is said, and swept when the server
	// starts again.
	out := map[string]any{"deleted": p.Name}
	if b.Git != nil {
		if err := b.Git.Remove(p.Name); err != nil {
			s.Log.Error("the repositories of a deleted project were not removed", "project", p.Name, "error", err)
			out["repositoriesLeft"] = "its repositories could not be removed now; Crew removes them when it starts again"
		}
	}
	s.Log.Info("the operator deleted a project", "project", p.Name)
	writeJSON(w, http.StatusOK, out)
}

// stopProjectRuns stops every run of the project that has not ended, and waits for them to end, for
// at most deleteWait.
func stopProjectRuns(ctx context.Context, b *board.Board, p *board.Project, stop func(context.Context, string) error) error {
	live := func() ([]string, error) {
		rows, err := b.Pool.Query(ctx, `SELECT r.id::text FROM runs r JOIN subtasks s ON s.id = r.subtask
			WHERE (s.project = $1 OR s.ticket IN (SELECT id FROM tickets WHERE project = $1)) AND r.state <> 'ended'`, p.ID)
		if err != nil {
			return nil, err
		}
		return pgx.CollectRows(rows, pgx.RowTo[string])
	}
	ids, err := live()
	if err != nil {
		return err
	}
	for _, id := range ids {
		stop(ctx, id)
	}
	deadline := time.Now().Add(deleteWait)
	for len(ids) > 0 && time.Now().Before(deadline) {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(200 * time.Millisecond):
		}
		if ids, err = live(); err != nil {
			return err
		}
	}
	return nil
}
