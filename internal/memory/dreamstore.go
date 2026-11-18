// Package memory implements Crew's Dreamstore typed memory tiers (episodic, semantic,
// procedural) with continuous validity scoring, commit-drift decay, check-pass boosts,
// and automatic < 0.40 quarantine filtering (design 9.10, R215).
package memory

import (
	"context"
	"errors"
	"fmt"
	"math"
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"

	"github.com/kesokaj/crew/internal/board"
	"github.com/kesokaj/crew/internal/db"
)

const (
	QuarantineThreshold  = 0.40
	DriftPenalty         = 0.35
	PassBoost            = 0.15
	ContradictionPenalty = 0.45
)

// DreamKinds lists supported cognitive memory tiers in Dreamstore.
var DreamKinds = []string{"episodic", "semantic", "procedural"}

// ErrInvalid indicates invalid Dreamstore input.
var ErrInvalid = errors.New("invalid dreamstore entry")

// ErrNotFound indicates a missing Dreamstore entry.
var ErrNotFound = errors.New("dreamstore entry not found")

// Entry represents a single cognitive Dreamstore entry scored continuously for validity.
type Entry struct {
	ID                 int64     `json:"id"`
	ProjectID          string    `json:"projectId"`
	Number             int       `json:"number"`
	Kind               string    `json:"kind"`
	Title              string    `json:"title"`
	Body               string    `json:"body"`
	AppliesTo          []string  `json:"appliesTo"`
	Paths              []string  `json:"paths"`
	Keywords           []string  `json:"keywords"`
	ValidityScore      float64   `json:"validityScore"`
	VerifiedCommit     string    `json:"verifiedCommit,omitempty"`
	EvidenceRuns       []string  `json:"evidenceRuns"`
	HitPassCount       int       `json:"hitPassCount"`
	ContradictionCount int       `json:"contradictionCount"`
	Author             string    `json:"author"`
	Status             string    `json:"status"`
	RetiredReason      *string   `json:"retiredReason,omitempty"`
	CreatedAt          time.Time `json:"createdAt"`
	UpdatedAt          time.Time `json:"updatedAt"`
}

// AddInput creates a new Dreamstore entry anchored to optional paths and evidence.
type AddInput struct {
	ProjectID      string
	Kind           string
	Title          string
	Body           string
	AppliesTo      []string
	Paths          []string
	Keywords       []string
	VerifiedCommit string
	EvidenceRuns   []string
	Author         string
}

const dreamCols = `id, project_id::text, number, kind, title, body, applies_to, paths, keywords,
	validity_score, verified_commit, evidence_runs, hit_pass_count, contradiction_count,
	author, status, retired_reason, created_at, updated_at`

func scanDream(row pgx.Row) (*Entry, error) {
	var e Entry
	err := row.Scan(
		&e.ID, &e.ProjectID, &e.Number, &e.Kind, &e.Title, &e.Body, &e.AppliesTo, &e.Paths, &e.Keywords,
		&e.ValidityScore, &e.VerifiedCommit, &e.EvidenceRuns, &e.HitPassCount, &e.ContradictionCount,
		&e.Author, &e.Status, &e.RetiredReason, &e.CreatedAt, &e.UpdatedAt,
	)
	if e.AppliesTo == nil {
		e.AppliesTo = []string{"all"}
	}
	if e.Paths == nil {
		e.Paths = []string{}
	}
	if e.Keywords == nil {
		e.Keywords = []string{}
	}
	if e.EvidenceRuns == nil {
		e.EvidenceRuns = []string{}
	}
	return &e, err
}

func cleanSlice(in []string) []string {
	var out []string
	for _, s := range in {
		if s = strings.TrimSpace(s); s != "" && !slices.Contains(out, s) {
			out = append(out, s)
		}
	}
	if out == nil {
		out = []string{}
	}
	return out
}

func clampScore(v float64) float64 {
	if v < 0 {
		return 0
	}
	if v > 1 {
		return 1
	}
	return math.Round(v*100) / 100
}

// Add validates, secret-checks, deduplicates (Jaccard >= 0.80), and inserts a Dreamstore entry.
func Add(ctx context.Context, q db.Querier, in AddInput) (*Entry, error) {
	if strings.TrimSpace(in.ProjectID) == "" {
		return nil, fmt.Errorf("%w: project id is required", ErrInvalid)
	}
	kind := strings.ToLower(strings.TrimSpace(in.Kind))
	if !slices.Contains(DreamKinds, kind) {
		return nil, fmt.Errorf("%w: kind must be one of %s", ErrInvalid, strings.Join(DreamKinds, ", "))
	}
	title := strings.TrimSpace(in.Title)
	body := strings.TrimSpace(in.Body)
	if title == "" || utf8.RuneCountInString(title) > 200 || strings.ContainsAny(title, "\r\n") {
		return nil, fmt.Errorf("%w: title must be one line of at most 200 characters", ErrInvalid)
	}
	if body == "" || utf8.RuneCountInString(body) > 4000 {
		return nil, fmt.Errorf("%w: body is required (max 4000 chars)", ErrInvalid)
	}
	applies := cleanSlice(in.AppliesTo)
	if len(applies) == 0 {
		applies = []string{"all"}
	}
	paths := cleanSlice(in.Paths)
	keywords := cleanSlice(in.Keywords)
	runs := cleanSlice(in.EvidenceRuns)
	if board.HoldsSecret(nil, append([]string{title, body}, slices.Concat(applies, paths, keywords)...)...) {
		return nil, fmt.Errorf("%w: entry holds secret-shaped text", ErrInvalid)
	}
	existing, err := List(ctx, q, in.ProjectID, false)
	if err != nil {
		return nil, err
	}
	words := board.Words(title + " " + body)
	for _, ex := range existing {
		if board.Overlap(words, board.Words(ex.Title+" "+ex.Body)) >= board.NearDuplicate {
			return nil, fmt.Errorf("%w: near duplicate of dreamstore entry D%04d (%s)", ErrInvalid, ex.Number, ex.Title)
		}
	}
	author := strings.TrimSpace(in.Author)
	if author == "" {
		author = "builder"
	}
	var nextNum int
	if err := q.QueryRow(ctx, `SELECT coalesce(max(number), 0) + 1 FROM dreamstore_entries WHERE project_id = $1`, in.ProjectID).Scan(&nextNum); err != nil {
		return nil, err
	}
	return scanDream(q.QueryRow(ctx, `INSERT INTO dreamstore_entries (
			project_id, number, kind, title, body, applies_to, paths, keywords,
			validity_score, verified_commit, evidence_runs, author, status)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, 1.0, $9, $10, $11, 'active')
		RETURNING `+dreamCols,
		in.ProjectID, nextNum, kind, title, body, applies, paths, keywords,
		strings.TrimSpace(in.VerifiedCommit), runs, author))
}

// List returns Dreamstore entries for a project, excluding < 0.40 quarantined entries when
// includeQuarantined is false.
func List(ctx context.Context, q db.Querier, projectID string, includeQuarantined bool) ([]Entry, error) {
	where := `status IN ('active', 'drift') AND validity_score >= $2`
	args := []any{projectID, QuarantineThreshold}
	if includeQuarantined {
		where = `status <> 'retired'`
		args = []any{projectID}
	}
	rows, err := q.Query(ctx, `SELECT `+dreamCols+` FROM dreamstore_entries WHERE project_id = $1 AND `+where+`
		ORDER BY validity_score DESC, number DESC LIMIT 200`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Entry{}
	for rows.Next() {
		e, serr := scanDream(rows)
		if serr != nil {
			return nil, serr
		}
		out = append(out, *e)
	}
	return out, rows.Err()
}

// Search performs full-text search over Dreamstore entries; agent queries exclude quarantined rows.
func Search(ctx context.Context, q db.Querier, projectID, query string, limit int, forAgent bool) ([]Entry, error) {
	query = strings.TrimSpace(query)
	if limit <= 0 || limit > 100 {
		limit = 20
	}
	if query == "" {
		return List(ctx, q, projectID, !forAgent)
	}
	statusCond := `status IN ('active', 'drift') AND validity_score >= 0.40`
	if !forAgent {
		statusCond = `status <> 'retired'`
	}
	rows, err := q.Query(ctx, `SELECT `+dreamCols+` FROM dreamstore_entries, websearch_to_tsquery('simple', $2) q
		WHERE project_id = $1 AND `+statusCond+`
		  AND (search_tsv @@ q OR title ILIKE '%' || $2 || '%' OR body ILIKE '%' || $2 || '%')
		ORDER BY ts_rank(search_tsv, q) DESC, validity_score DESC, number DESC LIMIT $3`, projectID, query, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Entry{}
	for rows.Next() {
		e, serr := scanDream(rows)
		if serr != nil {
			return nil, serr
		}
		out = append(out, *e)
	}
	return out, rows.Err()
}

// ApplyCommitDrift decays validity on entries whose paths overlap touchedPaths and quarantines
// any entry whose validity drops below QuarantineThreshold (0.40).
func ApplyCommitDrift(ctx context.Context, q db.Querier, projectID, newCommit string, touchedPaths []string) (decayed int, quarantined int, err error) {
	if len(touchedPaths) == 0 {
		return 0, 0, nil
	}
	entries, err := List(ctx, q, projectID, false)
	if err != nil {
		return 0, 0, err
	}
	for _, e := range entries {
		if len(e.Paths) == 0 || !anyPathMatches(e.Paths, touchedPaths) {
			continue
		}
		if strings.TrimSpace(newCommit) != "" && e.VerifiedCommit == strings.TrimSpace(newCommit) {
			continue
		}
		nextScore := clampScore(e.ValidityScore - DriftPenalty)
		nextStatus := "drift"
		if nextScore < QuarantineThreshold {
			nextStatus = "quarantined"
			quarantined++
		}
		if _, uerr := q.Exec(ctx, `UPDATE dreamstore_entries SET validity_score = $2, status = $3, updated_at = now() WHERE id = $1`,
			e.ID, nextScore, nextStatus); uerr != nil {
			return decayed, quarantined, uerr
		}
		decayed++
	}
	return decayed, quarantined, nil
}

// RecordOutcome boosts validity when a run verifies an entry (first-try pass) or decays/quarantines
// on contradiction.
func RecordOutcome(ctx context.Context, q db.Querier, projectID string, number int, runID, verifiedCommit string, passedFirstTry, contradicted bool) (*Entry, error) {
	cur, err := scanDream(q.QueryRow(ctx, `SELECT `+dreamCols+` FROM dreamstore_entries WHERE project_id = $1 AND number = $2`, projectID, number))
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	score := cur.ValidityScore
	hits := cur.HitPassCount
	contras := cur.ContradictionCount
	runs := slices.Clone(cur.EvidenceRuns)
	if r := strings.TrimSpace(runID); r != "" && !slices.Contains(runs, r) {
		runs = append(runs, r)
	}
	commit := cur.VerifiedCommit
	if strings.TrimSpace(verifiedCommit) != "" {
		commit = strings.TrimSpace(verifiedCommit)
	}
	if passedFirstTry {
		score = clampScore(score + PassBoost)
		hits++
	}
	if contradicted {
		score = clampScore(score - ContradictionPenalty)
		contras++
	}
	status := cur.Status
	if status != "retired" {
		if score < QuarantineThreshold {
			status = "quarantined"
		} else if passedFirstTry {
			status = "active"
		}
	}
	return scanDream(q.QueryRow(ctx, `UPDATE dreamstore_entries SET
			validity_score = $2, verified_commit = $3, evidence_runs = $4,
			hit_pass_count = $5, contradiction_count = $6, status = $7, updated_at = now()
		WHERE id = $1 RETURNING `+dreamCols, cur.ID, score, commit, runs, hits, contras, status))
}

func anyPathMatches(patterns, touched []string) bool {
	for _, pat := range patterns {
		for _, f := range touched {
			if board.PathMatch(pat, f) {
				return true
			}
		}
	}
	return false
}
