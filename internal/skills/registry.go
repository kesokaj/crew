package skills

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/kesokaj/crew/internal/board"
	"github.com/kesokaj/crew/internal/db"
	"github.com/kesokaj/crew/internal/gitstore"
)

var (
	ErrBlocked = errors.New("skill failed static security vetting")
	ErrInvalid = errors.New("invalid skill registration")
	hexSHARe   = regexp.MustCompile(`^[0-9a-fA-F]{7,64}$`)
)

// Tiers enumerates Crew's 4-tier Skills Registry taxonomy (design 12.11).
var Tiers = []string{"base", "shared", "project", "dream", "vendored"}

// Entry is one persisted Skills Registry catalog record.
type Entry struct {
	ID                 int64     `json:"id"`
	Slug               string    `json:"slug"`
	Tier               string    `json:"tier"`
	SourceProjectID    *string   `json:"sourceProjectId,omitempty"`
	Name               string    `json:"name"`
	Path               string    `json:"path"`
	Description        string    `json:"description"`
	Content            string    `json:"content,omitempty"`
	SourceURL          string    `json:"sourceUrl,omitempty"`
	PinnedSHA          string    `json:"pinnedSha,omitempty"`
	Licence            string    `json:"licence,omitempty"`
	ScanVerdict        string    `json:"scanVerdict"`
	ScanFindings       []string  `json:"scanFindings"`
	ValidityScore      float64   `json:"validityScore"`
	FirstPassDelta     float64   `json:"firstPassDelta"`
	PromptBytes        int       `json:"promptBytes"`
	TokenFootprint     int       `json:"tokenFootprint"`
	InstalledFactories []string  `json:"installedFactories,omitempty"`
	CreatedAt          time.Time `json:"createdAt"`
	UpdatedAt          time.Time `json:"updatedAt"`
}

// RegisterInput describes a skill submission to VetSkill + skills_registry.
type RegisterInput struct {
	Tier            string            `json:"tier"`
	SourceProjectID string            `json:"sourceProjectId"`
	Name            string            `json:"name"`
	Content         string            `json:"content"`
	ExtraFiles      map[string]string `json:"extraFiles"`
	SourceURL       string            `json:"sourceUrl"`
	PinnedSHA       string            `json:"pinnedSha"`
	Licence         string            `json:"licence"`
	ValidityScore   float64           `json:"validityScore"`
}

// VerifyVendoredPin validates that a vendored external skill carries a valid HTTPS source URL,
// 7–64 char hex commit SHA pin, and explicit licence.
func VerifyVendoredPin(sourceURL, sha, licence string) error {
	src := strings.TrimSpace(sourceURL)
	if !strings.HasPrefix(src, "https://") && !strings.HasPrefix(src, "http://") {
		return fmt.Errorf("%w: vendored skill requires an explicit repository URL", ErrInvalid)
	}
	if !hexSHARe.MatchString(strings.TrimSpace(sha)) {
		return fmt.Errorf("%w: vendored skill requires a 7-64 char hex commit SHA pin, got %q", ErrInvalid, sha)
	}
	if strings.TrimSpace(licence) == "" {
		return fmt.Errorf("%w: vendored skill requires an explicit licence in VENDORED.md", ErrInvalid)
	}
	return nil
}

// UpsertVendoredRow appends or replaces a skill's pin row inside .agents/skills/VENDORED.md.
func UpsertVendoredRow(existingMD, skillName, sourceURL, sha, licence string) (string, error) {
	if err := VerifyVendoredPin(sourceURL, sha, licence); err != nil {
		return "", err
	}
	header := "# Vendored Skills\n\n| Skill | Source | SHA | Licence |\n| --- | --- | --- | --- |\n"
	lines := strings.Split(strings.TrimRight(strings.ReplaceAll(existingMD, "\r\n", "\n"), "\n"), "\n")
	if strings.TrimSpace(existingMD) == "" {
		return fmt.Sprintf("%s| %s | %s | %s | %s |\n", header, skillName, sourceURL, sha, licence), nil
	}
	row := fmt.Sprintf("| %s | %s | %s | %s |", skillName, sourceURL, sha, licence)
	replaced := false
	for i, l := range lines {
		tr := strings.TrimSpace(l)
		if strings.HasPrefix(tr, "| "+skillName+" |") || strings.HasPrefix(tr, "|"+skillName+"|") {
			lines[i] = row
			replaced = true
			break
		}
	}
	if !replaced {
		lines = append(lines, row)
	}
	return strings.Join(lines, "\n") + "\n", nil
}

// Register statically vets a skill bundle and records it in skills_registry.
func Register(ctx context.Context, q db.Querier, in RegisterInput) (*Entry, ScanReport, error) {
	tier := strings.ToLower(strings.TrimSpace(in.Tier))
	if !slices.Contains(Tiers, tier) {
		return nil, ScanReport{}, fmt.Errorf("%w: unsupported tier %q", ErrInvalid, in.Tier)
	}
	name := strings.TrimSpace(in.Name)
	files := map[string]string{}
	for k, v := range in.ExtraFiles {
		files[k] = v
	}
	if in.Content != "" {
		files["SKILL.md"] = in.Content
	}
	report := VetSkill(name, files)
	if tier == "vendored" && report.Verdict == "PASS" {
		if err := VerifyVendoredPin(in.SourceURL, in.PinnedSHA, in.Licence); err != nil {
			report.Verdict = "BLOCKED"
			report.Findings = append(report.Findings, err.Error())
		}
	}
	slug := name
	if in.SourceProjectID != "" && (tier == "project" || tier == "dream") {
		slug = in.SourceProjectID + "/" + name
	}
	skillPath := ".agents/skills/" + name + "/SKILL.md"
	if tier == "base" {
		skillPath = "skills/" + name + "/SKILL.md"
	}
	findingsJSON, _ := json.Marshal(report.Findings)
	score := in.ValidityScore
	if score <= 0 || score > 1 {
		score = 1.0
	}
	var srcProj any
	if strings.TrimSpace(in.SourceProjectID) != "" {
		srcProj = strings.TrimSpace(in.SourceProjectID)
	}
	var rawFindings []byte
	var e Entry
	err := q.QueryRow(ctx, `INSERT INTO skills_registry (
			slug, tier, source_project_id, name, path, description, content,
			source_url, pinned_sha, licence, scan_verdict, scan_findings,
			validity_score, prompt_bytes, token_footprint, updated_at)
		VALUES ($1, $2, $3::uuid, $4, $5, $6, $7, $8, $9, $10, $11, $12::jsonb, $13, $14, $15, now())
		ON CONFLICT (tier, slug) DO UPDATE SET
			name = EXCLUDED.name,
			description = EXCLUDED.description,
			content = EXCLUDED.content,
			source_url = EXCLUDED.source_url,
			pinned_sha = EXCLUDED.pinned_sha,
			licence = EXCLUDED.licence,
			scan_verdict = EXCLUDED.scan_verdict,
			scan_findings = EXCLUDED.scan_findings,
			validity_score = EXCLUDED.validity_score,
			prompt_bytes = EXCLUDED.prompt_bytes,
			token_footprint = EXCLUDED.token_footprint,
			updated_at = now()
		RETURNING id, slug, tier, source_project_id::text, name, path, description, content,
			source_url, pinned_sha, licence, scan_verdict, scan_findings,
			validity_score, first_pass_delta, prompt_bytes, token_footprint, created_at, updated_at`,
		slug, tier, srcProj, name, skillPath, report.Description, in.Content,
		strings.TrimSpace(in.SourceURL), strings.TrimSpace(in.PinnedSHA), strings.TrimSpace(in.Licence),
		report.Verdict, findingsJSON, score, report.PromptBytes, report.TokenFootprint,
	).Scan(&e.ID, &e.Slug, &e.Tier, &e.SourceProjectID, &e.Name, &e.Path, &e.Description, &e.Content,
		&e.SourceURL, &e.PinnedSHA, &e.Licence, &e.ScanVerdict, &rawFindings,
		&e.ValidityScore, &e.FirstPassDelta, &e.PromptBytes, &e.TokenFootprint, &e.CreatedAt, &e.UpdatedAt)
	if err != nil {
		return nil, report, err
	}
	_ = json.Unmarshal(rawFindings, &e.ScanFindings)
	if e.ScanFindings == nil {
		e.ScanFindings = []string{}
	}
	return &e, report, nil
}

// Install activates a vetted skill into targetProject under a tracked Tasks API item,
// writes it on main (+ VENDORED.md pin for vendored skills), records skill_installs, and
// live-syncs the project's persistent warm-VM /home/agent/.gemini/config/skills/ directory.
func Install(ctx context.Context, b *board.Board, target *board.Project, skillNameOrSlug, homeDir, actor string) (*Entry, *board.FactoryTask, error) {
	if b == nil || target == nil {
		return nil, nil, fmt.Errorf("%w: board and project are required", ErrInvalid)
	}
	var e Entry
	var rawFindings []byte
	err := b.Pool.QueryRow(ctx, `SELECT id, slug, tier, source_project_id::text, name, path, description, content,
			source_url, pinned_sha, licence, scan_verdict, scan_findings,
			validity_score, first_pass_delta, prompt_bytes, token_footprint, created_at, updated_at
		FROM skills_registry
		WHERE slug = $1 OR name = $1
		ORDER BY CASE scan_verdict WHEN 'PASS' THEN 0 ELSE 1 END, id DESC
		LIMIT 1`, strings.TrimSpace(skillNameOrSlug)).Scan(
		&e.ID, &e.Slug, &e.Tier, &e.SourceProjectID, &e.Name, &e.Path, &e.Description, &e.Content,
		&e.SourceURL, &e.PinnedSHA, &e.Licence, &e.ScanVerdict, &rawFindings,
		&e.ValidityScore, &e.FirstPassDelta, &e.PromptBytes, &e.TokenFootprint, &e.CreatedAt, &e.UpdatedAt,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil, fmt.Errorf("%w: skill %q not registered", ErrInvalid, skillNameOrSlug)
	}
	if err != nil {
		return nil, nil, err
	}
	_ = json.Unmarshal(rawFindings, &e.ScanFindings)
	if e.ScanVerdict != "PASS" {
		return &e, nil, fmt.Errorf("%w: %s", ErrBlocked, strings.Join(e.ScanFindings, "; "))
	}
	if rep := VetSkill(e.Name, map[string]string{"SKILL.md": e.Content}); rep.Verdict != "PASS" {
		return &e, nil, fmt.Errorf("%w: %s", ErrBlocked, strings.Join(rep.Findings, "; "))
	}
	if e.Tier == "vendored" {
		if verr := VerifyVendoredPin(e.SourceURL, e.PinnedSHA, e.Licence); verr != nil {
			return &e, nil, fmt.Errorf("%w: %v", ErrBlocked, verr)
		}
	}

	ft, err := b.CreateFactoryTask(ctx, target.ID, board.NewFactoryTask{
		Kind:         "coordinator",
		Title:        "Install vetted skill " + e.Name,
		Role:         "@coordinator",
		ArtifactPath: ".agents/skills/" + e.Name + "/SKILL.md",
		CreatedBy:    nonEmpty(actor, board.Operator),
	})
	if err != nil {
		return nil, nil, err
	}

	if b.Git != nil && b.Identity != nil && b.Git.Exists(target.Name) {
		id, ierr := b.Identity(ctx)
		if ierr != nil {
			return nil, ft, ierr
		}
		targetPath := ".agents/skills/" + e.Name + "/SKILL.md"
		ov := gitstore.Overlay{
			targetPath: func([]byte) []byte { return []byte(e.Content) },
		}
		if e.Tier == "vendored" {
			existing := ""
			if vb, verr := b.Git.ReadBlob(ctx, target.Name, "main", ".agents/skills/VENDORED.md"); verr == nil {
				existing = vb.Content
			}
			updated, uerr := UpsertVendoredRow(existing, e.Name, e.SourceURL, e.PinnedSHA, e.Licence)
			if uerr != nil {
				return nil, ft, uerr
			}
			ov[".agents/skills/VENDORED.md"] = func([]byte) []byte { return []byte(updated) }
		}
		if _, cerr := b.Git.CommitOverlay(ctx, target.Name, ov, "Install skill "+e.Name+" ["+ft.Key+"]", id); cerr != nil {
			return nil, ft, cerr
		}
	}

	synced := false
	if strings.TrimSpace(homeDir) != "" {
		if serr := b.SyncProjectHomeHarness(ctx, target, homeDir, ""); serr != nil {
			return nil, ft, serr
		}
		synced = true
	}
	if _, err := b.Pool.Exec(ctx, `INSERT INTO skill_installs (skill_id, project_id, factory_task_id, state, synced_home, installed_by)
		VALUES ($1, $2, $3::uuid, 'active', $4, $5)
		ON CONFLICT (skill_id, project_id) DO UPDATE SET
			factory_task_id = EXCLUDED.factory_task_id,
			state = 'active',
			synced_home = EXCLUDED.synced_home,
			installed_by = EXCLUDED.installed_by,
			installed_at = now()`, e.ID, target.ID, ft.ID, synced, nonEmpty(actor, board.Operator)); err != nil {
		return nil, ft, err
	}
	doneTask, _ := b.CompleteFactoryTask(ctx, target.ID, ft.ID, "done", "Installed vetted skill "+e.Name, ".agents/skills/"+e.Name+"/SKILL.md", nonEmpty(actor, board.Operator))
	if doneTask != nil {
		ft = doneTask
	}
	return &e, ft, nil
}

// ListByProject returns registered entries and per-factory installed project names.
func ListByProject(ctx context.Context, q db.Querier) ([]Entry, error) {
	rows, err := q.Query(ctx, `SELECT r.id, r.slug, r.tier, r.source_project_id::text, r.name, r.path, r.description,
			r.source_url, r.pinned_sha, r.licence, r.scan_verdict, r.scan_findings,
			r.validity_score, r.first_pass_delta, r.prompt_bytes, r.token_footprint, r.created_at, r.updated_at,
			coalesce((SELECT array_agg(p.name ORDER BY p.name) FROM skill_installs si JOIN projects p ON p.id = si.project_id WHERE si.skill_id = r.id AND si.state = 'active'), '{}')
		FROM skills_registry r ORDER BY r.tier, r.name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Entry{}
	for rows.Next() {
		var e Entry
		var raw []byte
		if ser := rows.Scan(&e.ID, &e.Slug, &e.Tier, &e.SourceProjectID, &e.Name, &e.Path, &e.Description,
			&e.SourceURL, &e.PinnedSHA, &e.Licence, &e.ScanVerdict, &raw,
			&e.ValidityScore, &e.FirstPassDelta, &e.PromptBytes, &e.TokenFootprint, &e.CreatedAt, &e.UpdatedAt,
			&e.InstalledFactories); ser != nil {
			return nil, ser
		}
		_ = json.Unmarshal(raw, &e.ScanFindings)
		if e.ScanFindings == nil {
			e.ScanFindings = []string{}
		}
		if e.InstalledFactories == nil {
			e.InstalledFactories = []string{}
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

// RequestSkillDedup delegates to board.RequestSkill so overlapping requests (>= 0.5 Jaccard)
// collapse onto a single skill ticket.
func RequestSkillDedup(ctx context.Context, b *board.Board, p *board.Project, req board.SkillRequest, run, role, who string) (*board.Ticket, bool, error) {
	return b.RequestSkill(ctx, p, req, run, role, who)
}

func nonEmpty(a, b string) string {
	if s := strings.TrimSpace(a); s != "" {
		return s
	}
	return b
}
