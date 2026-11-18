package board

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"go.yaml.in/yaml/v3"

	"github.com/kesokaj/crew/internal/db"
	"github.com/kesokaj/crew/internal/gitstore"
)

const (
	// TierFoundation marks 000-foundation.md, gated strictly by operator Project OK (Rule 12).
	TierFoundation = "foundation"
	// TierFeature marks NNN-<slug>.md feature specs, supporting @user or autonomous @planner decisions.
	TierFeature = "feature"
)

// Spec is one Two-Tier Planning Studio contract (000-foundation.md or NNN-<slug>.md).
type Spec struct {
	ID               int64      `json:"id"`
	ProjectID        string     `json:"projectId"`
	Project          string     `json:"project,omitempty"`
	Number           int        `json:"number"`
	Slug             string     `json:"slug"`
	SpecKey          string     `json:"specKey"`
	Path             string     `json:"path"`
	Tier             string     `json:"tier"`
	Title            string     `json:"title"`
	BodyMarkdown     string     `json:"bodyMarkdown"`
	State            string     `json:"state"`
	ApprovalMode     string     `json:"approvalMode"`
	DecidedBy        string     `json:"decidedBy,omitempty"`
	EscalationReason string     `json:"escalationReason,omitempty"`
	TouchedPaths     []string   `json:"touchedPaths"`
	AltersFoundation bool       `json:"altersFoundation"`
	ThreadRef        string     `json:"threadRef,omitempty"`
	FactoryTaskID    *string    `json:"factoryTaskId,omitempty"`
	EmittedKeys      []string   `json:"emittedKeys"`
	CreatedBy        string     `json:"createdBy"`
	ApprovedAt       *time.Time `json:"approvedAt,omitempty"`
	CreatedAt        time.Time  `json:"createdAt"`
	UpdatedAt        time.Time  `json:"updatedAt"`
}

// SpecQuestion is one peer-agent question directed to @planner in a feature thread.
type SpecQuestion struct {
	ID          int64      `json:"id"`
	ProjectID   string     `json:"projectId"`
	SpecID      int64      `json:"specId"`
	TicketID    *int64     `json:"ticketId,omitempty"`
	AskingRole  string     `json:"askingRole"`
	AskingRun   *string    `json:"askingRun,omitempty"`
	ThreadRef   string     `json:"threadRef,omitempty"`
	Question    string     `json:"question"`
	ContextNote string     `json:"contextNote,omitempty"`
	HopCount    int        `json:"hopCount"`
	PlannerRun  *string    `json:"plannerRun,omitempty"`
	Answer      string     `json:"answer,omitempty"`
	State       string     `json:"state"`
	CreatedAt   time.Time  `json:"createdAt"`
	AnsweredAt  *time.Time `json:"answeredAt,omitempty"`
}

// SpecDetail returns one spec with its questions, DEC-* decisions, and any pending proposed tickets.
type SpecDetail struct {
	Spec
	Questions       []SpecQuestion `json:"questions"`
	Decisions       []SpecDecision `json:"decisions"`
	ProposedTickets []NewTicket    `json:"proposedTickets,omitempty"`
}

// EnsureSpecInput creates or updates a Tier-2 feature spec (.crew/specs/NNN-<slug>.md).
type EnsureSpecInput struct {
	Ref              string
	Title            string
	Slug             string
	BodyMarkdown     string
	TouchedPaths     []string
	AltersFoundation bool
	ThreadRef        string
	CreatedBy        string
}

// AskPlannerInput posts an autonomous or operator feature design question toward @planner.
type AskPlannerInput struct {
	SpecRef          string
	Title            string
	Slug             string
	TicketID         int64
	TicketKey        string
	AskingRole       string
	AskingRun        string
	Question         string
	ContextNote      string
	TouchedPaths     []string
	AltersFoundation bool
	SkipRoomMirror   bool
}

var nonSlugChars = regexp.MustCompile(`[^a-z0-9]+`)

func slugifySpecTitle(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	s = nonSlugChars.ReplaceAllString(s, "-")
	s = strings.Trim(s, "-")
	if s == "" {
		return "feature"
	}
	if len(s) > 48 {
		s = strings.Trim(s[:48], "-")
	}
	if s == "" || s == "foundation" {
		return "feature-spec"
	}
	return s
}

func normalizePeerRole(role string) string {
	r := strings.ToLower(strings.TrimPrefix(strings.TrimSpace(role), "@"))
	if r == "" {
		return "builder"
	}
	return r
}

type foundationFrontMatter struct {
	Spec      string `yaml:"spec"`
	Tier      string `yaml:"tier"`
	Path      string `yaml:"path"`
	Project   string `yaml:"project"`
	Key       string `yaml:"key"`
	State     string `yaml:"state"`
	Approvals string `yaml:"approvals"`
}

func renderFoundationSpecMarkdown(name, key, goal, planText, state string) string {
	if strings.TrimSpace(goal) == "" {
		goal = "Built with Crew."
	}
	var buf bytes.Buffer
	buf.WriteString("---\n")
	enc := yaml.NewEncoder(&buf)
	enc.SetIndent(2)
	if err := enc.Encode(foundationFrontMatter{
		Spec:      "SPEC-000",
		Tier:      TierFoundation,
		Path:      ".crew/specs/000-foundation.md",
		Project:   name,
		Key:       key,
		State:     state,
		Approvals: "operator",
	}); err == nil {
		_ = enc.Close()
	}
	fmt.Fprintf(&buf, "---\n\n# 000 — Foundation Spec (%s)\n\n## Goal\n\n%s\n\n## Plan\n\n%s\n",
		name, strings.TrimSpace(goal), strings.TrimSpace(planText))
	return buf.String()
}

type featureSpecFrontMatter struct {
	Spec         string   `yaml:"spec"`
	Tier         string   `yaml:"tier"`
	Path         string   `yaml:"path"`
	State        string   `yaml:"state"`
	ApprovalMode string   `yaml:"approval_mode"`
	Thread       string   `yaml:"thread,omitempty"`
	DecidedBy    string   `yaml:"decided_by,omitempty"`
	Emitted      []string `yaml:"emitted_tickets,omitempty"`
}

func renderFeatureSpecMarkdown(sp Spec) []byte {
	var buf bytes.Buffer
	buf.WriteString("---\n")
	enc := yaml.NewEncoder(&buf)
	enc.SetIndent(2)
	if err := enc.Encode(featureSpecFrontMatter{
		Spec:         sp.SpecKey,
		Tier:         sp.Tier,
		Path:         sp.Path,
		State:        sp.State,
		ApprovalMode: sp.ApprovalMode,
		Thread:       sp.ThreadRef,
		DecidedBy:    sp.DecidedBy,
		Emitted:      sp.EmittedKeys,
	}); err == nil {
		_ = enc.Close()
	}
	body := strings.TrimSpace(sp.BodyMarkdown)
	if body == "" {
		body = fmt.Sprintf("# %s — %s", sp.SpecKey, sp.Title)
	}
	fmt.Fprintf(&buf, "---\n\n%s\n", body)
	return buf.Bytes()
}

func (b *Board) seedFoundationSpecTx(ctx context.Context, tx pgx.Tx, p *Project, goal, planText string, own bool, who string) error {
	state, decidedBy := "waiting_project_ok", ""
	var approvedAt *time.Time
	if own {
		state, decidedBy = "approved", Operator
		now := b.now()
		approvedAt = &now
	}
	md := renderFoundationSpecMarkdown(p.Name, p.Key, goal, planText, state)
	_, err := tx.Exec(ctx, `INSERT INTO specs (
			project_id, number, slug, spec_key, path, tier, title, body_markdown,
			state, approval_mode, decided_by, created_by, approved_at)
		VALUES ($1, 0, 'foundation', 'SPEC-000', '.crew/specs/000-foundation.md', 'foundation',
			'Foundation Spec', $2, $3, 'project_ok', $4, $5, $6)
		ON CONFLICT (project_id, number) DO NOTHING`,
		p.ID, md, state, decidedBy, nonEmpty(who, Operator), approvedAt)
	return err
}

func (b *Board) approveFoundationSpecTx(ctx context.Context, tx pgx.Tx, p *Project, who string) error {
	var title, body string
	err := tx.QueryRow(ctx, `SELECT title, body_markdown FROM specs WHERE project_id = $1 AND number = 0 FOR UPDATE`, p.ID).Scan(&title, &body)
	if errors.Is(err, pgx.ErrNoRows) {
		plan, pErr := readPlan(ctx, tx, p.ID)
		if pErr != nil {
			return pErr
		}
		return b.seedFoundationSpecTx(ctx, tx, p, p.Goal, plan.Original, true, who)
	}
	if err != nil {
		return err
	}
	plan, err := readPlan(ctx, tx, p.ID)
	if err != nil {
		return err
	}
	md := renderFoundationSpecMarkdown(p.Name, p.Key, p.Goal, plan.Original, "approved")
	_, err = tx.Exec(ctx, `UPDATE specs
		SET state = 'approved', decided_by = $2, body_markdown = $3, approved_at = $4, updated_at = now()
		WHERE project_id = $1 AND number = 0`,
		p.ID, nonEmpty(who, Operator), md, b.now())
	return err
}

func (b *Board) mirrorFoundationSpecOnMain(ctx context.Context, p *Project) {
	if p == nil || b.Git == nil || b.Identity == nil || !b.Git.Exists(p.Name) {
		return
	}
	if p.RepoSource != "local" && !strings.HasPrefix(p.RepoSource, "github") {
		return
	}
	sp, err := b.GetSpec(ctx, p, "SPEC-000")
	if err != nil {
		return
	}
	id, err := b.Identity(ctx)
	if err != nil {
		return
	}
	_, _ = b.Git.CommitOverlay(ctx, p.Name, gitstore.Overlay{
		sp.Path: func([]byte) []byte { return []byte(sp.BodyMarkdown) },
	}, p.Key+": approve .crew/specs/000-foundation.md on Project OK", id)
}

const specCols = `s.id, s.project_id::text, pr.name, s.number, s.slug, s.spec_key, s.path, s.tier,
	s.title, s.body_markdown, s.state, s.approval_mode, s.decided_by, s.escalation_reason,
	s.touched_paths, s.alters_foundation, s.thread_ref, s.factory_task_id::text, s.emitted_keys,
	s.created_by, s.approved_at, s.created_at, s.updated_at`

func scanSpec(row pgx.Row) (Spec, []byte, error) {
	var s Spec
	var proposedJSON []byte
	err := row.Scan(&s.ID, &s.ProjectID, &s.Project, &s.Number, &s.Slug, &s.SpecKey, &s.Path, &s.Tier,
		&s.Title, &s.BodyMarkdown, &s.State, &s.ApprovalMode, &s.DecidedBy, &s.EscalationReason,
		&s.TouchedPaths, &s.AltersFoundation, &s.ThreadRef, &s.FactoryTaskID, &s.EmittedKeys,
		&s.CreatedBy, &s.ApprovedAt, &s.CreatedAt, &s.UpdatedAt, &proposedJSON)
	if s.TouchedPaths == nil {
		s.TouchedPaths = []string{}
	}
	if s.EmittedKeys == nil {
		s.EmittedKeys = []string{}
	}
	return s, proposedJSON, err
}

func specByRefTx(ctx context.Context, q db.Querier, projectID, ref string, lock bool) (Spec, []byte, error) {
	ref = strings.TrimSpace(ref)
	if ref == "" {
		return Spec{}, nil, errf(Invalid, "spec reference is required")
	}
	numFilter := -1
	trimmed := strings.TrimPrefix(strings.TrimPrefix(ref, ".crew/specs/"), "specs/")
	trimmed = strings.TrimSuffix(trimmed, ".md")
	up := strings.ToUpper(trimmed)
	if strings.HasPrefix(up, "SPEC-") {
		if n, err := strconv.Atoi(strings.TrimPrefix(up, "SPEC-")); err == nil && n >= 0 {
			numFilter = n
		}
	} else if dash := strings.IndexByte(trimmed, '-'); dash > 0 {
		if n, err := strconv.Atoi(trimmed[:dash]); err == nil && n >= 0 {
			numFilter = n
		}
	} else if n, err := strconv.Atoi(trimmed); err == nil && n >= 0 {
		numFilter = n
	}
	forSQL := ""
	if lock {
		forSQL = " FOR UPDATE OF s"
	}
	row := q.QueryRow(ctx, `SELECT `+specCols+`, s.proposed_tickets FROM specs s
		JOIN projects pr ON pr.id = s.project_id
		WHERE s.project_id = $1 AND (
			s.path = $2 OR lower(s.spec_key) = lower($2) OR lower(s.slug) = lower($2)
			OR ($3 >= 0 AND s.number = $3)
		) LIMIT 1`+forSQL, projectID, ref, numFilter)
	s, proposed, err := scanSpec(row)
	if errors.Is(err, pgx.ErrNoRows) {
		return Spec{}, nil, errf(NotFound, "no such spec %q", ref)
	}
	return s, proposed, err
}

// ListSpecs lists all specs for a project, starting with 000-foundation.md.
func (b *Board) ListSpecs(ctx context.Context, p *Project) ([]Spec, error) {
	if p == nil {
		return nil, errf(Invalid, "project is required")
	}
	rows, err := b.Pool.Query(ctx, `SELECT `+specCols+`, s.proposed_tickets FROM specs s
		JOIN projects pr ON pr.id = s.project_id
		WHERE s.project_id = $1 ORDER BY s.number ASC`, p.ID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Spec{}
	for rows.Next() {
		s, _, err := scanSpec(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, s)
	}
	return out, rows.Err()
}

// GetSpec loads one spec along with its peer questions and DEC-* decisions.
func (b *Board) GetSpec(ctx context.Context, p *Project, ref string) (*SpecDetail, error) {
	if p == nil {
		return nil, errf(Invalid, "project is required")
	}
	sp, proposedRaw, err := specByRefTx(ctx, b.Pool, p.ID, ref, false)
	if err != nil {
		return nil, err
	}
	detail := &SpecDetail{Spec: sp, Questions: []SpecQuestion{}, Decisions: []SpecDecision{}}
	if len(proposedRaw) > 0 && string(proposedRaw) != "[]" {
		_ = json.Unmarshal(proposedRaw, &detail.ProposedTickets)
	}
	qRows, err := b.Pool.Query(ctx, `SELECT id, project_id::text, spec_id, ticket_id, asking_role, asking_run::text,
			thread_ref, question, context_note, hop_count, planner_run::text, answer, state, created_at, answered_at
		FROM spec_questions WHERE spec_id = $1 ORDER BY id ASC`, sp.ID)
	if err != nil {
		return nil, err
	}
	detail.Questions, err = pgx.CollectRows(qRows, func(r pgx.CollectableRow) (SpecQuestion, error) {
		var q SpecQuestion
		err := r.Scan(&q.ID, &q.ProjectID, &q.SpecID, &q.TicketID, &q.AskingRole, &q.AskingRun,
			&q.ThreadRef, &q.Question, &q.ContextNote, &q.HopCount, &q.PlannerRun, &q.Answer, &q.State, &q.CreatedAt, &q.AnsweredAt)
		return q, err
	})
	if err != nil {
		return nil, err
	}
	detail.Decisions, err = b.listSpecDecisionsBySpec(ctx, b.Pool, sp.ID)
	if err != nil {
		return nil, err
	}
	return detail, nil
}

func mintSpecTaskTx(ctx context.Context, tx pgx.Tx, p *Project, specKey, title, path, threadRef, createdBy string) (string, error) {
	var num int
	if err := tx.QueryRow(ctx, `SELECT coalesce(max(number), 0) + 1 FROM factory_tasks WHERE project_id = $1`, p.ID).Scan(&num); err != nil {
		return "", err
	}
	var id string
	if err := tx.QueryRow(ctx, `SELECT gen_random_uuid()::text`).Scan(&id); err != nil {
		return "", err
	}
	key := fmt.Sprintf("%s-T%d", p.Key, num)
	ft := FactoryTask{
		ID: id, ProjectID: p.ID, Project: p.Name, Number: num, Key: key, Kind: "spec",
		Title: truncateLine("Spec "+specKey+": "+title, maxTitle), Role: "@planner",
		State: "queued", SpecRef: path, OriginRoom: nonEmpty(threadRef, "#"+p.Name),
		MaxTries: 3, ArtifactPath: path, CreatedBy: nonEmpty(createdBy, "planner"),
	}
	yamlDoc, err := renderTaskYAMLContract(ft)
	if err != nil {
		return "", err
	}
	if _, err := tx.Exec(ctx, `INSERT INTO factory_tasks (
			id, project_id, number, key, kind, title, role, state, spec_ref, origin_room,
			yaml_contract, max_tries, budget_tokens, artifact_path, created_by)
		VALUES ($1::uuid, $2, $3, $4, 'spec', $5, '@planner', 'queued', $6, $7, $8, 3, 0, $9, $10)`,
		ft.ID, p.ID, num, ft.Key, ft.Title, ft.SpecRef, ft.OriginRoom, yamlDoc, ft.ArtifactPath, ft.CreatedBy); err != nil {
		return "", err
	}
	return ft.ID, nil
}

// WriteFeatureSpec creates or updates a Tier-2 feature spec (.crew/specs/NNN-<slug>.md) outside Backlog.
func (b *Board) WriteFeatureSpec(ctx context.Context, p *Project, in EnsureSpecInput) (*Spec, error) {
	if p == nil {
		return nil, errf(Invalid, "project is required")
	}
	if strings.TrimSpace(in.Ref) == "" && strings.TrimSpace(in.Title) == "" {
		return nil, errf(Invalid, "title or ref is required for a feature spec")
	}
	var out Spec
	err := b.inTx(ctx, func(tx pgx.Tx) error {
		locked, err := projectByRef(ctx, tx, p.ID, true)
		if err != nil {
			return err
		}
		if locked.State == "archived" {
			return errf(Refused, "%s is archived", locked.Name)
		}
		if strings.TrimSpace(in.Ref) != "" {
			cur, _, err := specByRefTx(ctx, tx, locked.ID, in.Ref, true)
			if err == nil {
				if cur.Tier == TierFoundation || cur.Number == 0 {
					return errf(Refused, "000-foundation.md is governed by Project OK and cannot be modified by WriteFeatureSpec")
				}
				return b.updateFeatureSpecTx(ctx, tx, locked, &cur, in, &out)
			}
			if kindOf(err) != NotFound {
				return err
			}
		}
		return b.insertFeatureSpecTx(ctx, tx, locked, in, &out)
	})
	if err != nil {
		return nil, err
	}
	b.mirrorSpecArtifact(ctx, p, out.Path, renderFeatureSpecMarkdown(out), fmt.Sprintf("%s: save %s", p.Key, out.SpecKey))
	return &out, nil
}

func (b *Board) updateFeatureSpecTx(ctx context.Context, tx pgx.Tx, p *Project, cur *Spec, in EnsureSpecInput, out *Spec) error {
	title := nonEmpty(strings.TrimSpace(in.Title), cur.Title)
	body := nonEmpty(strings.TrimSpace(in.BodyMarkdown), cur.BodyMarkdown)
	touched := mergeUniquePaths(cur.TouchedPaths, in.TouchedPaths, nil)
	alters := cur.AltersFoundation || in.AltersFoundation
	taskID := cur.FactoryTaskID
	if taskID == nil {
		id, err := mintSpecTaskTx(ctx, tx, p, cur.SpecKey, title, cur.Path, cur.ThreadRef, in.CreatedBy)
		if err != nil {
			return err
		}
		taskID = &id
	}
	row := tx.QueryRow(ctx, `UPDATE specs SET
			title = $2, body_markdown = $3, touched_paths = $4, alters_foundation = $5,
			factory_task_id = coalesce(factory_task_id, $6::uuid), updated_at = now()
		WHERE id = $1
		RETURNING id, project_id::text, $7::text, number, slug, spec_key, path, tier, title,
			body_markdown, state, approval_mode, decided_by, escalation_reason, touched_paths,
			alters_foundation, thread_ref, factory_task_id::text, emitted_keys, created_by,
			approved_at, created_at, updated_at, proposed_tickets`,
		cur.ID, title, body, touched, alters, *taskID, p.Name)
	sp, _, err := scanSpec(row)
	*out = sp
	return err
}

func (b *Board) insertFeatureSpecTx(ctx context.Context, tx pgx.Tx, p *Project, in EnsureSpecInput, out *Spec) error {
	var num int
	if err := tx.QueryRow(ctx, `SELECT greatest(coalesce(max(number), 0) + 1, 1) FROM specs WHERE project_id = $1`, p.ID).Scan(&num); err != nil {
		return err
	}
	title := strings.TrimSpace(in.Title)
	if title == "" {
		title = fmt.Sprintf("Feature Spec %03d", num)
	}
	slug := slugifySpecTitle(nonEmpty(in.Slug, title))
	specKey := fmt.Sprintf("SPEC-%03d", num)
	pth := fmt.Sprintf(".crew/specs/%03d-%s.md", num, slug)
	thread := nonEmpty(strings.TrimSpace(in.ThreadRef), fmt.Sprintf("feat/%03d-%s", num, slug))
	body := nonEmpty(strings.TrimSpace(in.BodyMarkdown), fmt.Sprintf("# %s — %s\n", specKey, title))
	doc, err := b.Settings(ctx, tx, p)
	if err != nil {
		return err
	}
	mode := EffectiveFeatureSpecsMode(ctx, tx, p.ID, doc)
	taskID, err := mintSpecTaskTx(ctx, tx, p, specKey, title, pth, thread, in.CreatedBy)
	if err != nil {
		return err
	}
	row := tx.QueryRow(ctx, `INSERT INTO specs (
			project_id, number, slug, spec_key, path, tier, title, body_markdown,
			state, approval_mode, touched_paths, alters_foundation, thread_ref, factory_task_id, created_by)
		VALUES ($1, $2, $3, $4, $5, 'feature', $6, $7, 'draft', $8, $9, $10, $11, $12::uuid, $13)
		RETURNING id, project_id::text, $14::text, number, slug, spec_key, path, tier, title,
			body_markdown, state, approval_mode, decided_by, escalation_reason, touched_paths,
			alters_foundation, thread_ref, factory_task_id::text, emitted_keys, created_by,
			approved_at, created_at, updated_at, proposed_tickets`,
		p.ID, num, slug, specKey, pth, title, body, mode, nonNil(mergeUniquePaths(in.TouchedPaths, nil, nil)),
		in.AltersFoundation, thread, taskID, nonEmpty(in.CreatedBy, "planner"), p.Name)
	sp, _, err := scanSpec(row)
	if err != nil {
		return err
	}
	*out = sp
	return db.RecordProjectChange(ctx, tx, p.ID, nonEmpty(in.CreatedBy, "planner"), "specs", sp.SpecKey, nil, sp)
}

// AskPlanner queues a design question from a peer agent (or operator) to @planner in a feature thread.
func (b *Board) AskPlanner(ctx context.Context, p *Project, in AskPlannerInput) (*Spec, *SpecQuestion, error) {
	if p == nil {
		return nil, nil, errf(Invalid, "project is required")
	}
	qText := strings.TrimSpace(in.Question)
	if qText == "" || len(qText) > maxText {
		return nil, nil, errf(Invalid, "question is required and must be at most %d bytes", maxText)
	}
	role := normalizePeerRole(in.AskingRole)
	maxHops := 3
	if wf, err := b.GetWorkflow(ctx, b.Pool, p); err == nil && wf != nil && wf.MaxAgentHops > 0 {
		maxHops = wf.MaxAgentHops
	}
	var outSpec Spec
	var outQ SpecQuestion
	err := b.inTx(ctx, func(tx pgx.Tx) error {
		locked, err := projectByRef(ctx, tx, p.ID, true)
		if err != nil {
			return err
		}
		if locked.State == "archived" {
			return errf(Refused, "%s is archived", locked.Name)
		}
		var ticketID *int64
		if in.TicketID > 0 {
			tid := in.TicketID
			ticketID = &tid
		} else if strings.TrimSpace(in.TicketKey) != "" {
			if n, err := ParseNumber(locked, in.TicketKey); err == nil {
				var tid int64
				if err := tx.QueryRow(ctx, `SELECT id FROM tickets WHERE project = $1 AND number = $2`, locked.ID, n).Scan(&tid); err != nil && !errors.Is(err, pgx.ErrNoRows) {
					return err
				} else if err == nil {
					ticketID = &tid
				}
			}
		}
		var sp Spec
		if strings.TrimSpace(in.SpecRef) != "" {
			cur, _, err := specByRefTx(ctx, tx, locked.ID, in.SpecRef, true)
			if err == nil {
				if cur.Tier == TierFoundation || cur.Number == 0 {
					return errf(Refused, "cannot modify 000-foundation.md via feature ask_planner; use a Tier-2 feature spec")
				}
				sp = cur
			} else if kindOf(err) != NotFound {
				return err
			}
		}
		if sp.ID == 0 {
			title := nonEmpty(strings.TrimSpace(in.Title), firstSentence(qText))
			if err := b.insertFeatureSpecTx(ctx, tx, locked, EnsureSpecInput{
				Title: title, Slug: in.Slug, TouchedPaths: in.TouchedPaths, AltersFoundation: in.AltersFoundation,
				CreatedBy: role,
			}, &sp); err != nil {
				return err
			}
		}
		var hops int
		if err := tx.QueryRow(ctx, `SELECT count(*) FROM spec_questions WHERE spec_id = $1`, sp.ID).Scan(&hops); err != nil {
			return err
		}
		nextHop := hops + 1
		if nextHop > maxHops {
			return errf(Refused, "max_agent_hops (%d) reached for %s", maxHops, sp.SpecKey)
		}
		taskID := sp.FactoryTaskID
		if taskID == nil {
			id, err := mintSpecTaskTx(ctx, tx, locked, sp.SpecKey, sp.Title, sp.Path, sp.ThreadRef, role)
			if err != nil {
				return err
			}
			taskID = &id
		} else {
			if _, err := tx.Exec(ctx, `UPDATE factory_tasks SET state = 'queued', updated_at = now()
				WHERE id = $1::uuid AND state IN ('done', 'failed')`, *taskID); err != nil {
				return err
			}
		}
		newState := sp.State
		if newState != "escalated" {
			newState = "deliberating"
		}
		touched := mergeUniquePaths(sp.TouchedPaths, in.TouchedPaths, nil)
		alters := sp.AltersFoundation || in.AltersFoundation
		row := tx.QueryRow(ctx, `UPDATE specs SET state = $2, touched_paths = $3, alters_foundation = $4,
				factory_task_id = coalesce(factory_task_id, $5::uuid), updated_at = now()
			WHERE id = $1
			RETURNING id, project_id::text, $6::text, number, slug, spec_key, path, tier, title,
				body_markdown, state, approval_mode, decided_by, escalation_reason, touched_paths,
				alters_foundation, thread_ref, factory_task_id::text, emitted_keys, created_by,
				approved_at, created_at, updated_at, proposed_tickets`,
			sp.ID, newState, touched, alters, *taskID, locked.Name)
		outSpec, _, err = scanSpec(row)
		if err != nil {
			return err
		}
		err = tx.QueryRow(ctx, `INSERT INTO spec_questions (
				project_id, spec_id, ticket_id, asking_role, asking_run, thread_ref, question, context_note, hop_count, state)
			VALUES ($1, $2, $3, $4, nullif($5, '')::uuid, $6, $7, $8, $9, 'open')
			RETURNING id, project_id::text, spec_id, ticket_id, asking_role, asking_run::text, thread_ref,
				question, context_note, hop_count, planner_run::text, answer, state, created_at, answered_at`,
			locked.ID, sp.ID, ticketID, role, strings.TrimSpace(in.AskingRun), sp.ThreadRef,
			qText, strings.TrimSpace(in.ContextNote), nextHop).Scan(
			&outQ.ID, &outQ.ProjectID, &outQ.SpecID, &outQ.TicketID, &outQ.AskingRole, &outQ.AskingRun,
			&outQ.ThreadRef, &outQ.Question, &outQ.ContextNote, &outQ.HopCount, &outQ.PlannerRun,
			&outQ.Answer, &outQ.State, &outQ.CreatedAt, &outQ.AnsweredAt)
		return err
	})
	if err != nil {
		return nil, nil, err
	}
	b.mirrorSpecArtifact(ctx, p, outSpec.Path, renderFeatureSpecMarkdown(outSpec), fmt.Sprintf("%s: %s peer question %s", p.Key, role, outSpec.SpecKey))
	if !in.SkipRoomMirror {
		b.emitRoomEvent(ctx, p, outSpec.ThreadRef, outSpec.Path, in.TicketKey, role, in.AskingRun,
			fmt.Sprintf("@planner Question (hop %d): %s", outQ.HopCount, qText))
	}
	return &outSpec, &outQ, nil
}

func (b *Board) mirrorSpecArtifact(ctx context.Context, p *Project, path string, data []byte, message string) {
	if b.Git == nil || b.Identity == nil || !b.Git.Exists(p.Name) {
		return
	}
	id, err := b.Identity(ctx)
	if err != nil {
		return
	}
	_, _ = b.Git.CommitOverlay(ctx, p.Name, gitstore.Overlay{
		path: func([]byte) []byte { return data },
	}, message, id)
}

func nonEmpty(a, b string) string {
	if strings.TrimSpace(a) != "" {
		return strings.TrimSpace(a)
	}
	return b
}

func truncateLine(s string, maxLen int) string {
	s = strings.Join(strings.Fields(s), " ")
	if len(s) <= maxLen {
		return s
	}
	return s[:maxLen]
}

func firstSentence(s string) string {
	s = truncateLine(s, 72)
	if s == "" {
		return "Feature design"
	}
	return s
}
