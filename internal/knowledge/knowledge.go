// Package knowledge implements Crew's installation-wide Cross-Project Knowledge API
// (design 9.11, R218): secret-scrubbed and provenance-fenced shared skills, bug
// advisories, and reusable specs across factories without sharing sandboxes or credentials.
package knowledge

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"

	"github.com/kesokaj/crew/internal/board"
	"github.com/kesokaj/crew/internal/db"
	"github.com/kesokaj/crew/internal/fence"
	"github.com/kesokaj/crew/internal/redact"
	"github.com/kesokaj/crew/internal/skills"
)

var (
	ErrInvalid  = errors.New("invalid knowledge entry")
	ErrNotFound = errors.New("knowledge entry not found")
	urlCredsRe  = regexp.MustCompile(`(https?://)([^/\s:@]+:[^/\s@]+@)`)
)

// Kinds lists the artifact classes shared across factories via Knowledge API.
var Kinds = []string{"shared_skill", "advisory", "spec_pattern"}

// Entry is one installation-wide scrubbed and provenance-fenced knowledge entry.
type Entry struct {
	ID                int64     `json:"id"`
	Number            int       `json:"number"`
	Key               string    `json:"key"`
	Kind              string    `json:"kind"`
	SourceProjectID   *string   `json:"sourceProjectId,omitempty"`
	SourceProjectName string    `json:"sourceProjectName,omitempty"`
	SourceCommit      string    `json:"sourceCommit,omitempty"`
	SkillName         string    `json:"skillName,omitempty"`
	Title             string    `json:"title"`
	Summary           string    `json:"summary"`
	Body              string    `json:"body"`
	FencedBody        string    `json:"fencedBody"`
	Tags              []string  `json:"tags"`
	ValidityScore     float64   `json:"validityScore"`
	Scrubbed          bool      `json:"scrubbed"`
	CreatedBy         string    `json:"createdBy"`
	CreatedAt         time.Time `json:"createdAt"`
}

// PromoteInput promotes a factory finding, skill, or spec pattern into Knowledge API.
type PromoteInput struct {
	Kind              string   `json:"kind"`
	SourceProjectID   string   `json:"sourceProjectId"`
	SourceProjectName string   `json:"sourceProjectName"`
	SourceCommit      string   `json:"sourceCommit"`
	SkillName         string   `json:"skillName"`
	Title             string   `json:"title"`
	Summary           string   `json:"summary"`
	Body              string   `json:"body"`
	Tags              []string `json:"tags"`
	ValidityScore     float64  `json:"validityScore"`
	ExtraSecrets      []string `json:"extraSecrets"`
	CreatedBy         string   `json:"createdBy"`
}

// ActInput applies a cross-project entry inside targetProject strictly via Tasks API.
type ActInput struct {
	EntryKey     string `json:"entryKey"`
	TicketKind   string `json:"ticketKind"`
	CheckCmd     string `json:"checkCmd"`
	CreateTicket bool   `json:"createTicket"`
	Actor        string `json:"actor"`
}

const knowCols = `id, number, key, kind, source_project_id::text, source_project_name, source_commit,
	skill_name, title, summary, body, fenced_body, tags, validity_score, scrubbed, created_by, created_at`

func scanEntry(row pgx.Row) (*Entry, error) {
	var e Entry
	err := row.Scan(&e.ID, &e.Number, &e.Key, &e.Kind, &e.SourceProjectID, &e.SourceProjectName,
		&e.SourceCommit, &e.SkillName, &e.Title, &e.Summary, &e.Body, &e.FencedBody, &e.Tags,
		&e.ValidityScore, &e.Scrubbed, &e.CreatedBy, &e.CreatedAt)
	if e.Tags == nil {
		e.Tags = []string{}
	}
	return &e, err
}

// ScrubAndFence strips secrets/credentials and wraps text with Crew's provenance fence (Rule 14).
func ScrubAndFence(key, srcProject, raw string, extraSecrets ...string) (clean, fenced string) {
	red := redact.New(extraSecrets...)
	clean = red.Text(raw)
	clean = urlCredsRe.ReplaceAllString(clean, "$1"+redact.Marker+"@")
	f := fence.New(clean)
	srcLabel := strings.TrimSpace(srcProject)
	if srcLabel == "" {
		srcLabel = "installation"
	}
	inner := f.Data("Knowledge API "+key, "sibling factory "+srcLabel, clean)
	fenced = fmt.Sprintf("<crew_data_fence kind=\"knowledge_api\" key=%q source=%q>\n%s\n</crew_data_fence>",
		key, srcLabel, inner)
	return clean, fenced
}

// Promote scrubs, fences, validates (including VetSkill for shared_skill), and stores an entry.
func Promote(ctx context.Context, q db.Querier, in PromoteInput) (*Entry, error) {
	kind := strings.ToLower(strings.TrimSpace(in.Kind))
	if !slices.Contains(Kinds, kind) {
		return nil, fmt.Errorf("%w: kind must be one of %s", ErrInvalid, strings.Join(Kinds, ", "))
	}
	title := strings.TrimSpace(in.Title)
	body := strings.TrimSpace(in.Body)
	if title == "" || utf8.RuneCountInString(title) > 200 || body == "" {
		return nil, fmt.Errorf("%w: title and body are required", ErrInvalid)
	}
	red := redact.New(in.ExtraSecrets...)
	cleanTitle := urlCredsRe.ReplaceAllString(red.Text(title), "$1"+redact.Marker+"@")
	cleanSummary := urlCredsRe.ReplaceAllString(red.Text(strings.TrimSpace(in.Summary)), "$1"+redact.Marker+"@")
	if cleanSummary == "" {
		cleanSummary = cleanTitle
	}
	var num int
	if err := q.QueryRow(ctx, `SELECT coalesce(max(number), 0) + 1 FROM knowledge_entries`).Scan(&num); err != nil {
		return nil, err
	}
	key := fmt.Sprintf("KNOW-%03d", num)
	cleanBody, fencedBody := ScrubAndFence(key, in.SourceProjectName, body, in.ExtraSecrets...)

	if kind == "shared_skill" && strings.TrimSpace(in.SkillName) != "" {
		if _, rep, rerr := skills.Register(ctx, q, skills.RegisterInput{
			Tier:            "shared",
			SourceProjectID: in.SourceProjectID,
			Name:            strings.TrimSpace(in.SkillName),
			Content:         cleanBody,
			ValidityScore:   in.ValidityScore,
		}); rerr != nil {
			return nil, rerr
		} else if rep.Verdict == "BLOCKED" {
			return nil, fmt.Errorf("%w: %s", skills.ErrBlocked, strings.Join(rep.Findings, "; "))
		}
	}

	var srcProj any
	if s := strings.TrimSpace(in.SourceProjectID); s != "" {
		srcProj = s
	}
	tags := make([]string, 0, len(in.Tags))
	for _, t := range in.Tags {
		if t = strings.TrimSpace(t); t != "" && !slices.Contains(tags, t) {
			tags = append(tags, t)
		}
	}
	score := in.ValidityScore
	if score <= 0 || score > 1 {
		score = 1.0
	}
	actor := strings.TrimSpace(in.CreatedBy)
	if actor == "" {
		actor = board.Operator
	}
	return scanEntry(q.QueryRow(ctx, `INSERT INTO knowledge_entries (
			number, key, kind, source_project_id, source_project_name, source_commit,
			skill_name, title, summary, body, fenced_body, tags, validity_score, scrubbed, created_by)
		VALUES ($1, $2, $3, $4::uuid, $5, $6, $7, $8, $9, $10, $11, $12, $13, true, $14)
		RETURNING `+knowCols,
		num, key, kind, srcProj, strings.TrimSpace(in.SourceProjectName), strings.TrimSpace(in.SourceCommit),
		strings.TrimSpace(in.SkillName), cleanTitle, cleanSummary, cleanBody, fencedBody, tags, score, actor))
}

// Search lists installation-wide Knowledge API entries matching query/kind with provenance fences.
func Search(ctx context.Context, q db.Querier, query, kind string, limit int) ([]Entry, error) {
	query = strings.TrimSpace(query)
	kind = strings.ToLower(strings.TrimSpace(kind))
	if limit <= 0 || limit > 100 {
		limit = 30
	}
	rows, err := q.Query(ctx, `SELECT `+knowCols+` FROM knowledge_entries
		WHERE ($1 = '' OR kind = $1)
		  AND ($2 = '' OR search_tsv @@ websearch_to_tsquery('simple', $2) OR title ILIKE '%' || $2 || '%' OR summary ILIKE '%' || $2 || '%')
		ORDER BY validity_score DESC, number DESC LIMIT $3`, kind, query, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Entry{}
	for rows.Next() {
		e, serr := scanEntry(rows)
		if serr != nil {
			return nil, serr
		}
		out = append(out, *e)
	}
	return out, rows.Err()
}

// Get fetches one Knowledge API entry by key (e.g. KNOW-001).
func Get(ctx context.Context, q db.Querier, key string) (*Entry, error) {
	e, err := scanEntry(q.QueryRow(ctx, `SELECT `+knowCols+` FROM knowledge_entries WHERE upper(key) = upper($1)`, strings.TrimSpace(key)))
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	return e, err
}

// ActOnEntry turns an installation-wide Knowledge API advisory/shared skill into a tracked
// Tasks API work item in targetProject without mutating main directly (R217, R218).
func ActOnEntry(ctx context.Context, b *board.Board, target *board.Project, in ActInput) (*Entry, *board.FactoryTask, *board.Ticket, error) {
	if b == nil || target == nil {
		return nil, nil, nil, fmt.Errorf("%w: board and target project required", ErrInvalid)
	}
	e, err := Get(ctx, b.Pool, in.EntryKey)
	if err != nil {
		return nil, nil, nil, err
	}
	actor := strings.TrimSpace(in.Actor)
	if actor == "" {
		actor = board.Operator
	}
	artifact := ""
	if e.SkillName != "" {
		artifact = ".agents/skills/" + e.SkillName + "/SKILL.md"
	}
	ft, err := b.CreateFactoryTask(ctx, target.ID, board.NewFactoryTask{
		Kind:         "coordinator",
		Title:        "Evaluate " + e.Key + ": " + e.Title,
		Role:         "@coordinator",
		OriginRoom:   "knowledge_api:" + e.Key,
		ArtifactPath: artifact,
		CreatedBy:    actor,
	})
	if err != nil {
		return e, nil, nil, err
	}
	var tk *board.Ticket
	if in.CreateTicket {
		tkKind := strings.TrimSpace(in.TicketKind)
		if tkKind == "" {
			if e.Kind == "advisory" {
				tkKind = "fix"
			} else {
				tkKind = "chore"
			}
		}
		chkCmd := strings.TrimSpace(in.CheckCmd)
		if chkCmd == "" {
			chkCmd = "test -f go.mod"
		}
		tk, err = b.AddTicket(ctx, target.ID, board.NewTicket{
			Title:  "Apply " + e.Key + ": " + e.Title,
			Kind:   tkKind,
			Why:    "Cross-project Knowledge API (" + e.Key + ") from " + e.SourceProjectName,
			What:   e.FencedBody,
			Goal:   "Validate applicability locally under normal checks",
			Checks: []board.CheckSpec{{Kind: "cmd", Spec: chkCmd}},
		}, board.OriginAgent, actor)
		if err != nil {
			return e, ft, nil, err
		}
	}
	return e, ft, tk, nil
}
