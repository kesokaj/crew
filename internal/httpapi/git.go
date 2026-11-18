package httpapi

import (
	"errors"
	"net/http"
	"slices"
	"strconv"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/kesokaj/crew/internal/board"
	"github.com/kesokaj/crew/internal/gitstore"
)

// The routes of git in the app (design 6.7): the Git screen's branches, history, tags, commits, and
// comparisons, and a ticket's branch with its commits grouped by the subtask that pushed them. They
// read; nothing here writes a repository.
func (s *Server) gitRoutes(api *routes) {
	api.HandleFunc("GET /api/projects/{p}/git/branches", s.gitBranches)
	api.HandleFunc("GET /api/projects/{p}/git/log", s.gitLog)
	api.HandleFunc("GET /api/projects/{p}/git/commits/{c}", s.gitCommit)
	api.HandleFunc("GET /api/projects/{p}/git/compare", s.gitCompare)
	api.HandleFunc("GET /api/projects/{p}/git/tree", s.gitTree)
	api.HandleFunc("GET /api/projects/{p}/git/blob", s.gitBlob)
	api.HandleFunc("GET /api/projects/{p}/tasks/{t}/git", s.taskGit)
	api.HandleFunc("GET /api/runs/{id}/git", s.runGit)
}

// RunGit is what one run pushed (6.7, Subtask): its commits, and the diff of only that run, from
// its first push's before to its last push's after.
type RunGit struct {
	Ref     string              `json:"ref"`
	From    string              `json:"from"`
	To      string              `json:"to"`
	Commits []gitstore.Commit   `json:"commits"`
	Files   []gitstore.FileDiff `json:"files"`
}

func (s *Server) runGit(w http.ResponseWriter, r *http.Request) {
	b := s.board(w)
	if b == nil {
		return
	}
	id := r.PathValue("id")
	out := RunGit{Commits: []gitstore.Commit{}, Files: []gitstore.FileDiff{}}
	var repo string
	err := b.Pool.QueryRow(r.Context(), `SELECT repo, ref, (array_agg(coalesce(old, '') ORDER BY id))[1], (array_agg(new ORDER BY id DESC))[1]
		FROM ref_pushes WHERE run::text = $1 GROUP BY repo, ref ORDER BY max(id) DESC LIMIT 1`, id).Scan(&repo, &out.Ref, &out.From, &out.To)
	if errors.Is(err, pgx.ErrNoRows) {
		writeJSON(w, http.StatusOK, out)
		return
	}
	if err != nil {
		s.boardError(w, "reading the run's pushes", err)
		return
	}
	if out.Commits, err = b.Git.LogRange(r.Context(), repo, out.From, out.To); err != nil {
		s.gitError(w, "reading the run's commits", err)
		return
	}
	if out.From != "" {
		if out.Files, err = b.Git.Compare(r.Context(), repo, out.From, out.To); err != nil {
			s.gitError(w, "reading the run's diff", err)
			return
		}
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) gitError(w http.ResponseWriter, what string, err error) {
	if errors.Is(err, gitstore.ErrNoRev) {
		writeError(w, http.StatusNotFound, "NOT_FOUND", err.Error())
		return
	}
	s.boardError(w, what, err)
}

// GitBranch is a branch as the Git screen lists it, with the ticket it belongs to.
type GitBranch struct {
	gitstore.BranchInfo
	Ticket *string `json:"ticket"`
	Stage  *string `json:"stage"`
}

func (s *Server) gitBranches(w http.ResponseWriter, r *http.Request) {
	b, p := s.project(w, r)
	if p == nil {
		return
	}
	bs, err := b.Git.Branches(r.Context(), p.Name)
	if err != nil {
		s.gitError(w, "reading the branches", err)
		return
	}
	tickets := map[string][2]string{}
	rows, err := b.Pool.Query(r.Context(), `SELECT t.branch, $2 || '-' || t.number, t.stage FROM tickets t WHERE t.project = $1 AND t.branch IS NOT NULL`, p.ID, p.Key)
	if err != nil {
		s.boardError(w, "reading the tickets", err)
		return
	}
	for rows.Next() {
		var br, key, stage string
		if err := rows.Scan(&br, &key, &stage); err != nil {
			rows.Close()
			s.boardError(w, "reading the tickets", err)
			return
		}
		tickets[br] = [2]string{key, stage}
	}
	rows.Close()
	out := make([]GitBranch, 0, len(bs))
	for _, x := range bs {
		g := GitBranch{BranchInfo: x}
		if t, ok := tickets[x.Name]; ok {
			g.Ticket, g.Stage = &t[0], &t[1]
		}
		out = append(out, g)
	}
	// main first, then the newest.
	slices.SortStableFunc(out, func(a, b GitBranch) int {
		if a.Name == "main" || b.Name == "main" {
			return boolInt(b.Name == "main") - boolInt(a.Name == "main")
		}
		return b.When.Compare(a.When)
	})
	tags, err := b.Git.Tags(r.Context(), p.Name)
	if err != nil {
		s.gitError(w, "reading the tags", err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"branches": out, "tags": tags})
}

func boolInt(v bool) int {
	if v {
		return 1
	}
	return 0
}

// gitLog is the history of a branch, tag, or commit, newest first; since leaves out what it has.
func (s *Server) gitLog(w http.ResponseWriter, r *http.Request) {
	b, p := s.project(w, r)
	if p == nil {
		return
	}
	q := r.URL.Query()
	ref := q.Get("ref")
	if ref == "" {
		ref = "main"
	}
	limit, _ := strconv.Atoi(q.Get("limit"))
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	var cs []gitstore.Commit
	var err error
	if ref == "all" {
		cs, err = b.Git.LogAll(r.Context(), p.Name, limit)
	} else {
		cs, err = b.Git.Log(r.Context(), p.Name, ref, q.Get("since"), limit)
	}
	if err != nil {
		s.gitError(w, "reading the history", err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"commits": cs})
}

func (s *Server) gitCommit(w http.ResponseWriter, r *http.Request) {
	b, p := s.project(w, r)
	if p == nil {
		return
	}
	c, files, err := b.Git.Show(r.Context(), p.Name, r.PathValue("c"))
	if err != nil {
		s.gitError(w, "reading the commit", err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"commit": c, "files": files})
}

// gitCompare is what to changed since it left from, for any two points.
func (s *Server) gitCompare(w http.ResponseWriter, r *http.Request) {
	b, p := s.project(w, r)
	if p == nil {
		return
	}
	q := r.URL.Query()
	files, err := b.Git.Compare(r.Context(), p.Name, q.Get("from"), q.Get("to"))
	if err != nil {
		if errors.Is(err, gitstore.ErrNoRev) {
			s.gitError(w, "comparing", err)
			return
		}
		writeError(w, http.StatusBadRequest, "FAILED_PRECONDITION", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"files": files})
}

func (s *Server) gitTree(w http.ResponseWriter, r *http.Request) {
	b, p := s.project(w, r)
	if p == nil {
		return
	}
	ref := r.URL.Query().Get("ref")
	if ref == "" {
		ref = "main"
	}
	files, err := b.Git.Tree(r.Context(), p.Name, ref)
	if err != nil {
		s.gitError(w, "reading the tree", err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ref": ref, "files": files})
}

func (s *Server) gitBlob(w http.ResponseWriter, r *http.Request) {
	b, p := s.project(w, r)
	if p == nil {
		return
	}
	q := r.URL.Query()
	ref := q.Get("ref")
	if ref == "" {
		ref = "main"
	}
	blob, err := b.Git.ReadBlob(r.Context(), p.Name, ref, q.Get("path"))
	if err != nil {
		s.gitError(w, "reading the file", err)
		return
	}
	writeJSON(w, http.StatusOK, blob)
}

// PushGroup is the commits one push, or one subtask's pushes, brought to a ticket's branch.
type PushGroup struct {
	// Subtask is the subtask whose run pushed them, as SF-8.2; empty for a push of no run of the
	// ticket, such as the operator's from the terminal.
	Subtask string            `json:"subtask"`
	Title   string            `json:"title"`
	Role    string            `json:"role"`
	Who     string            `json:"who"`
	From    string            `json:"from"`
	To      string            `json:"to"`
	At      time.Time         `json:"at"`
	Commits []gitstore.Commit `json:"commits"`
}

// TaskGit is a ticket's part of git (20.2): its branch, the merge, the commits by subtask, and the
// diff against main file by file.
type TaskGit struct {
	Branch   *string `json:"branch"`
	Pushed   bool    `json:"pushed"`
	MergedAs *string `json:"mergedAs"`
	// Pull is the ticket's pull request, when it has one (7.12).
	Pull   *board.PullRequest  `json:"pull,omitempty"`
	Groups []PushGroup         `json:"groups"`
	Other  []gitstore.Commit   `json:"other"`
	Files  []gitstore.FileDiff `json:"files"`
	// Omitted is how many commits past maxTaskCommits the groups and Other leave out.
	Omitted int `json:"omitted,omitempty"`
}

func (s *Server) taskGit(w http.ResponseWriter, r *http.Request) {
	b, p := s.project(w, r)
	if p == nil {
		return
	}
	t, err := b.Ticket(r.Context(), p, r.PathValue("t"))
	if err != nil {
		s.boardError(w, "reading the ticket", err)
		return
	}
	g, err := readTaskGit(r.Context(), b, p, t)
	if err != nil {
		s.gitError(w, "reading the ticket's git", err)
		return
	}
	pulls, err := b.PullRequests(r.Context(), p)
	if err != nil {
		s.boardError(w, "reading the pull requests", err)
		return
	}
	for i := range pulls {
		if pulls[i].Ticket == t.Key {
			g.Pull = &pulls[i]
		}
	}
	writeJSON(w, http.StatusOK, g)
}
