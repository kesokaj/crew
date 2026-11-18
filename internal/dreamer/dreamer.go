// Package dreamer implements Scoped Dreaming for Crew (design 10.8, R216, R217):
// @dreamer stays on Gemini (Rule 15), runs under a tracked dream Tasks API item,
// restricts proposals to AGENTS.md, WORKFLOW.md, .agents/rules/*.md, and
// .agents/skills/<name>/SKILL.md + VENDORED.md, vets every candidate statically,
// and never writes main or syncs warm VMs until @user approves from Operator Inbox.
package dreamer

import (
	"context"
	"encoding/json"
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
	"github.com/kesokaj/crew/internal/gitstore"
	"github.com/kesokaj/crew/internal/knowledge"
	"github.com/kesokaj/crew/internal/skills"
)

var (
	ErrOutOfScope = errors.New("dream proposal target outside allowed harness scope")
	ErrBlocked    = errors.New("dream proposal blocked by static vetting")
	ErrNotFound   = errors.New("dream proposal not found")
	ErrInvalid    = errors.New("invalid dream proposal operation")

	rulePathRe  = regexp.MustCompile(`^\.agents/rules/[a-z0-9][a-z0-9._-]{0,63}\.md$`)
	skillNameRe = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{1,62}$`)
	uuidRe      = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)
)

// Kinds enumerates allowed Scoped Dreaming proposal classes (design 10.8).
var Kinds = []string{"context_micro_adjustment", "skill_synthesis", "vendored_download"}

// PeerSpawner optionally dispatches a tracked read-only @dreamer peer session.
type PeerSpawner func(ctx context.Context, projectID, taskID, title, prompt string) (runID string, err error)

// Proposal represents one persisted dream_proposals record.
type Proposal struct {
	ID              int64     `json:"id"`
	ProjectID       string    `json:"projectId"`
	Number          int       `json:"number"`
	Key             string    `json:"key"`
	FactoryTaskID   *string   `json:"factoryTaskId,omitempty"`
	RunID           *string   `json:"runId,omitempty"`
	Kind            string    `json:"kind"`
	TargetPath      string    `json:"targetPath"`
	SkillName       string    `json:"skillName,omitempty"`
	Title           string    `json:"title"`
	Rationale       string    `json:"rationale"`
	ProposedContent string    `json:"proposedContent"`
	VendoredSource  string    `json:"vendoredSource,omitempty"`
	VendoredSHA     string    `json:"vendoredSha,omitempty"`
	VendoredLicence string    `json:"vendoredLicence,omitempty"`
	ScanVerdict     string    `json:"scanVerdict"`
	ScanFindings    []string  `json:"scanFindings"`
	PromoteShared   bool      `json:"promoteShared"`
	State           string    `json:"state"`
	DecidedBy       string    `json:"decidedBy,omitempty"`
	CreatedAt       time.Time `json:"createdAt"`
	UpdatedAt       time.Time `json:"updatedAt"`
}

// Candidate describes one proposed harness improvement produced during a dream cycle.
type Candidate struct {
	Kind            string `json:"kind"`
	TargetPath      string `json:"targetPath"`
	SkillName       string `json:"skillName"`
	Title           string `json:"title"`
	Rationale       string `json:"rationale"`
	ProposedContent string `json:"proposedContent"`
	VendoredSource  string `json:"vendoredSource"`
	VendoredSHA     string `json:"vendoredSha"`
	VendoredLicence string `json:"vendoredLicence"`
	PromoteShared   bool   `json:"promoteShared"`
}

// RunInput configures one tracked Scoped Dreaming consolidation pass.
type RunInput struct {
	Title      string      `json:"title"`
	Actor      string      `json:"actor"`
	Prompt     string      `json:"prompt"`
	Candidates []Candidate `json:"candidates"`
	Spawn      PeerSpawner `json:"-"`
}

// ValidateScope enforces design 10.8 write-target confinement: only AGENTS.md,
// WORKFLOW.md, .agents/rules/*.md, or .agents/skills/<name>/SKILL.md are valid.
func ValidateScope(kind, targetPath, skillName string) error {
	k := strings.TrimSpace(kind)
	if !slices.Contains(Kinds, k) {
		return fmt.Errorf("%w: unsupported dream proposal kind %q", ErrOutOfScope, kind)
	}
	p := strings.TrimSpace(targetPath)
	if p == "" || strings.HasPrefix(p, "/") || strings.Contains(p, "..") || strings.Contains(p, `\`) {
		return fmt.Errorf("%w: invalid target path %q", ErrOutOfScope, targetPath)
	}
	if k == "context_micro_adjustment" {
		if p == "AGENTS.md" || p == "WORKFLOW.md" || rulePathRe.MatchString(p) {
			return nil
		}
		return fmt.Errorf("%w: context_micro_adjustment must target AGENTS.md, WORKFLOW.md, or .agents/rules/*.md, got %q", ErrOutOfScope, p)
	}
	sk := strings.TrimSpace(skillName)
	if !skillNameRe.MatchString(sk) {
		return fmt.Errorf("%w: skill name %q must match ^[a-z0-9][a-z0-9-]{1,62}$", ErrOutOfScope, skillName)
	}
	want := ".agents/skills/" + sk + "/SKILL.md"
	if p != want {
		return fmt.Errorf("%w: skill proposal must target %q, got %q", ErrOutOfScope, want, p)
	}
	return nil
}

func vetCandidate(c Candidate) skills.ScanReport {
	if c.Kind == "context_micro_adjustment" {
		rep := skills.VetSkill("context-rule", map[string]string{
			"SKILL.md": "---\nname: context-rule\ndescription: context micro-adjustment check\n---\n" + c.ProposedContent,
		})
		if board.HoldsSecret([]string{c.ProposedContent}) {
			rep.Verdict = "BLOCKED"
			rep.Findings = append(rep.Findings, "secret material detected in proposed context adjustment")
		}
		return rep
	}
	rep := skills.VetSkill(c.SkillName, map[string]string{"SKILL.md": c.ProposedContent})
	if c.Kind == "vendored_download" && rep.Verdict == "PASS" {
		if err := skills.VerifyVendoredPin(c.VendoredSource, c.VendoredSHA, c.VendoredLicence); err != nil {
			rep.Verdict = "BLOCKED"
			rep.Findings = append(rep.Findings, err.Error())
		}
	}
	return rep
}

const proposalCols = `id, project_id::text, number, key, factory_task_id::text, run_id::text, kind, target_path,
	skill_name, title, rationale, proposed_content, vendored_source, vendored_sha, vendored_licence,
	scan_verdict, scan_findings, promote_shared, state, decided_by, created_at, updated_at`

func scanProposal(row pgx.Row) (Proposal, error) {
	var p Proposal
	var rawFindings []byte
	err := row.Scan(
		&p.ID, &p.ProjectID, &p.Number, &p.Key, &p.FactoryTaskID, &p.RunID, &p.Kind, &p.TargetPath,
		&p.SkillName, &p.Title, &p.Rationale, &p.ProposedContent, &p.VendoredSource, &p.VendoredSHA, &p.VendoredLicence,
		&p.ScanVerdict, &rawFindings, &p.PromoteShared, &p.State, &p.DecidedBy, &p.CreatedAt, &p.UpdatedAt,
	)
	if err != nil {
		return Proposal{}, err
	}
	_ = json.Unmarshal(rawFindings, &p.ScanFindings)
	if p.ScanFindings == nil {
		p.ScanFindings = []string{}
	}
	return p, nil
}

// RunCycle opens a tracked dream Tasks API item (R217), optionally launches @dreamer,
// statically vets every scoped candidate, and stores Inbox proposals without touching main.
func RunCycle(ctx context.Context, b *board.Board, p *board.Project, in RunInput) (*board.FactoryTask, []Proposal, error) {
	if b == nil || b.Pool == nil || p == nil {
		return nil, nil, fmt.Errorf("%w: board and project are required", ErrInvalid)
	}
	actor := nonEmpty(in.Actor, "dreamer")
	ft, err := b.CreateFactoryTask(ctx, p.ID, board.NewFactoryTask{
		Kind:      "dream",
		Title:     nonEmpty(in.Title, "Dream cycle consolidation for "+p.Name),
		Role:      "@dreamer",
		CreatedBy: actor,
	})
	if err != nil {
		return nil, nil, err
	}

	var runArg any
	if in.Spawn != nil {
		prompt := nonEmpty(in.Prompt, "Propose strictly scoped harness refinements or vetted skills.")
		rid, serr := in.Spawn(ctx, p.ID, ft.ID, ft.Title, prompt)
		if serr != nil {
			_, _ = b.CompleteFactoryTask(ctx, p.ID, ft.ID, "failed", serr.Error(), "", actor)
			return ft, nil, serr
		}
		if uuidRe.MatchString(strings.TrimSpace(rid)) {
			runArg = strings.TrimSpace(rid)
		}
	}

	for _, c := range in.Candidates {
		if verr := ValidateScope(c.Kind, c.TargetPath, c.SkillName); verr != nil {
			_, _ = b.CompleteFactoryTask(ctx, p.ID, ft.ID, "failed", verr.Error(), c.TargetPath, actor)
			return ft, nil, verr
		}
		if strings.TrimSpace(c.Title) == "" || utf8.RuneCountInString(strings.TrimSpace(c.Title)) > 200 || strings.TrimSpace(c.ProposedContent) == "" {
			err := fmt.Errorf("%w: candidate title and proposedContent are required", ErrInvalid)
			_, _ = b.CompleteFactoryTask(ctx, p.ID, ft.ID, "failed", err.Error(), c.TargetPath, actor)
			return ft, nil, err
		}
	}

	out := make([]Proposal, 0, len(in.Candidates))
	tx, err := b.Pool.Begin(ctx)
	if err != nil {
		return ft, nil, err
	}
	defer func() { _ = tx.Rollback(ctx) }()

	for _, c := range in.Candidates {
		rep := vetCandidate(c)
		state := "pending_approval"
		if rep.Verdict != "PASS" {
			state = "blocked"
		}
		findingsJSON, _ := json.Marshal(rep.Findings)
		var num int
		if nerr := tx.QueryRow(ctx, `SELECT coalesce(max(number), 0) + 1 FROM dream_proposals WHERE project_id = $1`, p.ID).Scan(&num); nerr != nil {
			return ft, nil, nerr
		}
		key := fmt.Sprintf("DRM-%03d", num)
		prop, ierr := scanProposal(tx.QueryRow(ctx, `INSERT INTO dream_proposals (
				project_id, number, key, factory_task_id, run_id, kind, target_path, skill_name,
				title, rationale, proposed_content, vendored_source, vendored_sha, vendored_licence,
				scan_verdict, scan_findings, promote_shared, state)
			VALUES ($1, $2, $3, $4::uuid, $5::uuid, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, $16::jsonb, $17, $18)
			RETURNING `+proposalCols,
			p.ID, num, key, ft.ID, runArg, strings.TrimSpace(c.Kind), strings.TrimSpace(c.TargetPath),
			strings.TrimSpace(c.SkillName), strings.TrimSpace(c.Title), strings.TrimSpace(c.Rationale), c.ProposedContent,
			strings.TrimSpace(c.VendoredSource), strings.TrimSpace(c.VendoredSHA), strings.TrimSpace(c.VendoredLicence),
			rep.Verdict, findingsJSON, c.PromoteShared, state,
		))
		if ierr != nil {
			return ft, nil, ierr
		}
		out = append(out, prop)
	}
	if err := tx.Commit(ctx); err != nil {
		return ft, nil, err
	}
	return ft, out, nil
}

// ListProposals returns a project's dream proposals ordered newest first.
func ListProposals(ctx context.Context, q db.Querier, projectID string) ([]Proposal, error) {
	rows, err := q.Query(ctx, `SELECT `+proposalCols+` FROM dream_proposals WHERE project_id = $1 ORDER BY number DESC`, projectID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Proposal{}
	for rows.Next() {
		p, ser := scanProposal(rows)
		if ser != nil {
			return nil, ser
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// DecideProposal approves or discards a pending dream proposal. Approved entries commit onto
// main, update Skills Registry / Knowledge API when requested, live-sync the warm VM, and close the task.
func DecideProposal(ctx context.Context, b *board.Board, p *board.Project, key, decision, homeDir, actor string) (*Proposal, error) {
	if b == nil || b.Pool == nil || p == nil {
		return nil, fmt.Errorf("%w: board and project are required", ErrInvalid)
	}
	dec := strings.ToLower(strings.TrimSpace(decision))
	if dec != "approve" && dec != "discard" {
		return nil, fmt.Errorf("%w: decision must be approve or discard", ErrInvalid)
	}
	who := nonEmpty(actor, board.Operator)
	prop, err := scanProposal(b.Pool.QueryRow(ctx, `SELECT `+proposalCols+` FROM dream_proposals WHERE project_id = $1 AND key = $2`,
		p.ID, strings.ToUpper(strings.TrimSpace(key))))
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, fmt.Errorf("%w: %s", ErrNotFound, key)
	}
	if err != nil {
		return nil, err
	}
	if prop.State == "blocked" || prop.ScanVerdict != "PASS" {
		return &prop, fmt.Errorf("%w: %s (%s)", ErrBlocked, prop.Key, strings.Join(prop.ScanFindings, "; "))
	}
	if prop.State != "pending_approval" {
		return &prop, fmt.Errorf("%w: proposal %s is already %s", ErrInvalid, prop.Key, prop.State)
	}

	if dec == "discard" {
		updated, uerr := scanProposal(b.Pool.QueryRow(ctx,
			`UPDATE dream_proposals SET state = 'discarded', decided_by = $2, updated_at = now() WHERE id = $1 RETURNING `+proposalCols,
			prop.ID, who))
		if uerr != nil {
			return nil, uerr
		}
		closeTaskIfSettled(ctx, b, p, updated.FactoryTaskID, "Discarded dream proposal "+updated.Key, updated.TargetPath, who)
		return &updated, nil
	}

	if verr := ValidateScope(prop.Kind, prop.TargetPath, prop.SkillName); verr != nil {
		return &prop, verr
	}
	if rep := vetCandidate(Candidate{
		Kind: prop.Kind, TargetPath: prop.TargetPath, SkillName: prop.SkillName,
		ProposedContent: prop.ProposedContent, VendoredSource: prop.VendoredSource,
		VendoredSHA: prop.VendoredSHA, VendoredLicence: prop.VendoredLicence,
	}); rep.Verdict != "PASS" {
		return &prop, fmt.Errorf("%w: %s", ErrBlocked, strings.Join(rep.Findings, "; "))
	}

	commitSHA := ""
	if b.Git != nil && b.Git.Exists(p.Name) {
		if b.Identity == nil {
			return nil, fmt.Errorf("%w: git identity missing", ErrInvalid)
		}
		id, ierr := b.Identity(ctx)
		if ierr != nil {
			return nil, ierr
		}
		ov := gitstore.Overlay{
			prop.TargetPath: func([]byte) []byte { return []byte(prop.ProposedContent) },
		}
		if prop.Kind == "vendored_download" {
			existing := ""
			if vb, rerr := b.Git.ReadBlob(ctx, p.Name, "main", ".agents/skills/VENDORED.md"); rerr == nil {
				existing = vb.Content
			}
			vm, uerr := skills.UpsertVendoredRow(existing, prop.SkillName, prop.VendoredSource, prop.VendoredSHA, prop.VendoredLicence)
			if uerr != nil {
				return nil, uerr
			}
			ov[".agents/skills/VENDORED.md"] = func([]byte) []byte { return []byte(vm) }
		}
		sha, cerr := b.Git.CommitOverlay(ctx, p.Name, ov, "Dream proposal "+prop.Key+": "+prop.Title, id)
		if cerr != nil {
			return nil, cerr
		}
		commitSHA = sha
	}

	if prop.Kind == "skill_synthesis" || prop.Kind == "vendored_download" {
		tier := "dream"
		if prop.Kind == "vendored_download" {
			tier = "vendored"
		}
		entry, _, rerr := skills.Register(ctx, b.Pool, skills.RegisterInput{
			Tier: tier, SourceProjectID: p.ID, Name: prop.SkillName, Content: prop.ProposedContent,
			SourceURL: prop.VendoredSource, PinnedSHA: prop.VendoredSHA, Licence: prop.VendoredLicence, ValidityScore: 1.0,
		})
		if rerr != nil {
			return nil, rerr
		}
		if _, ierr := b.Pool.Exec(ctx, `INSERT INTO skill_installs (skill_id, project_id, factory_task_id, state, synced_home, installed_by)
			VALUES ($1, $2, $3::uuid, 'active', $4, $5)
			ON CONFLICT (skill_id, project_id) DO UPDATE SET
				factory_task_id = EXCLUDED.factory_task_id, state = 'active',
				synced_home = EXCLUDED.synced_home, installed_by = EXCLUDED.installed_by, installed_at = now()`,
			entry.ID, p.ID, prop.FactoryTaskID, strings.TrimSpace(homeDir) != "", who); ierr != nil {
			return nil, ierr
		}
		if prop.PromoteShared && prop.Kind == "skill_synthesis" {
			if _, perr := knowledge.Promote(ctx, b.Pool, knowledge.PromoteInput{
				Kind: "shared_skill", SourceProjectID: p.ID, SourceProjectName: p.Name, SourceCommit: commitSHA,
				SkillName: prop.SkillName, Title: prop.Title, Summary: prop.Rationale, Body: prop.ProposedContent, CreatedBy: who,
			}); perr != nil {
				return nil, perr
			}
		}
	}

	if strings.TrimSpace(homeDir) != "" {
		if serr := b.SyncProjectHomeHarness(ctx, p, homeDir, ""); serr != nil {
			return nil, serr
		}
	}

	updated, err := scanProposal(b.Pool.QueryRow(ctx,
		`UPDATE dream_proposals SET state = 'approved', decided_by = $2, updated_at = now() WHERE id = $1 RETURNING `+proposalCols,
		prop.ID, who))
	if err != nil {
		return nil, err
	}
	closeTaskIfSettled(ctx, b, p, updated.FactoryTaskID, "Approved dream proposal "+updated.Key, updated.TargetPath, who)
	return &updated, nil
}

func closeTaskIfSettled(ctx context.Context, b *board.Board, p *board.Project, taskID *string, summary, path, who string) {
	if taskID == nil || *taskID == "" {
		return
	}
	var open int
	if err := b.Pool.QueryRow(ctx, `SELECT count(*) FROM dream_proposals WHERE factory_task_id = $1::uuid AND state = 'pending_approval'`, *taskID).Scan(&open); err == nil && open == 0 {
		_, _ = b.CompleteFactoryTask(ctx, p.ID, *taskID, "done", summary, path, who)
	}
}

func nonEmpty(a, b string) string {
	if s := strings.TrimSpace(a); s != "" {
		return s
	}
	return b
}
