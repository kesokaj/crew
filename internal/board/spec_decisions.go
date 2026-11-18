package board

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path"
	"slices"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"
	"go.yaml.in/yaml/v3"

	"github.com/kesokaj/crew/internal/cli"
	"github.com/kesokaj/crew/internal/db"
	"github.com/kesokaj/crew/internal/gitstore"
	"github.com/kesokaj/crew/internal/settings"
)

// SpecDecision records one architectural/feature decision (DEC-NNN) linked to a spec.
type SpecDecision struct {
	ID               int64     `json:"id"`
	ProjectID        string    `json:"projectId"`
	Number           int       `json:"number"`
	Key              string    `json:"key"`
	SpecID           int64     `json:"specId"`
	SpecKey          string    `json:"specKey,omitempty"`
	QuestionID       *int64    `json:"questionId,omitempty"`
	DecisionID       *int64    `json:"decisionId,omitempty"`
	AskingRole       string    `json:"askingRole,omitempty"`
	DecidedBy        string    `json:"decidedBy"`
	Question         string    `json:"question"`
	Choice           string    `json:"choice"`
	Alternatives     []string  `json:"alternatives"`
	Rationale        string    `json:"rationale"`
	State            string    `json:"state"`
	EscalationReason string    `json:"escalationReason,omitempty"`
	ArtifactPath     string    `json:"artifactPath"`
	PromptKey        string    `json:"promptKey,omitempty"`
	PromptVersion    int       `json:"promptVersion,omitempty"`
	Provenance       string    `json:"provenance,omitempty"`
	SourceMessageID  *int64    `json:"sourceMessageId,omitempty"`
	SupersededBy     *int      `json:"supersededBy,omitempty"`
	CreatedAt        time.Time `json:"createdAt"`
	UpdatedAt        time.Time `json:"updatedAt"`
}

// DecideSpecInput resolves a Tier-2 feature spec with a recorded decision and optional Backlog tickets.
type DecideSpecInput struct {
	SpecRef          string
	QuestionID       int64
	AskingRole       string
	DecidedByRole    string
	DecidedByRun     string
	BodyMarkdown     string
	Question         string
	Choice           string
	Alternatives     []string
	Rationale        string
	TouchedPaths     []string
	AltersFoundation bool
	Tickets          []NewTicket
}

// DecideSpecResult reports the updated spec, recorded DEC-NNN decision, escalation status, and emitted tickets.
type DecideSpecResult struct {
	Spec           Spec         `json:"spec"`
	Decision       SpecDecision `json:"decision"`
	Escalated      bool         `json:"escalated"`
	EmittedTickets []*Ticket    `json:"emittedTickets,omitempty"`
}

// EffectiveFeatureSpecsMode combines project settings (approvals.feature_specs) and main WORKFLOW.md.
// Manual mode in either place requires operator confirmation before Feature Specs emit tickets.
func EffectiveFeatureSpecsMode(ctx context.Context, q db.Querier, projectID string, doc settings.Doc) string {
	if settingString(doc, "approvals.feature_specs") == "manual" {
		return "manual"
	}
	var mode string
	err := q.QueryRow(ctx, `SELECT feature_specs FROM workflow_defs WHERE project_id = $1`, projectID).Scan(&mode)
	if err == nil && mode == "manual" {
		return "manual"
	}
	return "autonomous"
}

// ProtectedPaths138 returns the subset of paths that are instruction or CLI/workflow code files under 13.8.
func ProtectedPaths138(paths []string) []string {
	m := cli.NewMatcher(cli.InstructionFiles())
	seen := map[string]bool{}
	var out []string
	for _, raw := range paths {
		p := cleanRepoRelPath(raw)
		if p == "" || seen[p] {
			continue
		}
		if isProtectedPath138(m, p) {
			seen[p] = true
			out = append(out, p)
		}
	}
	sort.Strings(out)
	return out
}

func cleanRepoRelPath(raw string) string {
	p := strings.TrimSpace(strings.TrimPrefix(raw, "./"))
	p = path.Clean(strings.ReplaceAll(p, "\\", "/"))
	if p == "." || p == "" {
		return ""
	}
	return strings.TrimPrefix(p, "/")
}

func isProtectedPath138(m *cli.Matcher, p string) bool {
	if p == ".." || strings.HasPrefix(p, "../") {
		return true
	}
	if m.Matches(p) {
		return true
	}
	base := path.Base(p)
	if base == "AGENTS.md" || base == "GEMINI.md" || p == "WORKFLOW.md" {
		return true
	}
	if p == ".agents" || strings.HasPrefix(p, ".agents/") {
		return true
	}
	if p == ".github/workflows" || strings.HasPrefix(p, ".github/workflows/") {
		return true
	}
	return false
}

// EvaluateSpecGuardrails determines whether a Tier-2 feature spec decision must escalate to @user.
func EvaluateSpecGuardrails(projectState, mode string, altersFoundation bool, touchedPaths []string) (bool, string, []string) {
	protected := ProtectedPaths138(touchedPaths)
	var reasons []string
	if projectState == "planning" {
		reasons = append(reasons, "000-foundation.md still waits for operator Project OK")
	}
	if altersFoundation {
		reasons = append(reasons, "alters 000-foundation.md scope or non-goals")
	}
	if len(protected) > 0 {
		reasons = append(reasons, "touches 13.8 protected files: "+strings.Join(protected, ", "))
	}
	if mode == "manual" {
		reasons = append(reasons, "approvals.feature_specs is manual")
	}
	return len(reasons) > 0, strings.Join(reasons, "; "), protected
}

// DecideSpec records a Tier-2 feature spec decision. When within 000-foundation.md, 13.8 file rules,
// and autonomous mode after Project OK, it records DEC-NNN autonomously, mirrors specs/decisions/tickets
// to main, and emits YAML tickets into Backlog; otherwise it escalates to the operator Inbox.
func (b *Board) DecideSpec(ctx context.Context, p *Project, in DecideSpecInput) (*DecideSpecResult, error) {
	if p == nil {
		return nil, errf(Invalid, "project is required")
	}
	choice := strings.TrimSpace(in.Choice)
	if choice == "" || utf8.RuneCountInString(choice) > 500 || strings.ContainsAny(choice, "\r\n") {
		return nil, errf(Invalid, "choice: one line of at most 500 characters")
	}
	rationale := strings.TrimSpace(in.Rationale)
	if rationale == "" {
		return nil, errf(Invalid, "rationale is required")
	}
	for i := range in.Tickets {
		if err := checkBreakdownTicket(&in.Tickets[i]); err != nil {
			return nil, errf(Invalid, "ticket %d (%s): %v", i+1, in.Tickets[i].Title, err)
		}
	}
	var res DecideSpecResult
	err := b.inTx(ctx, func(tx pgx.Tx) error {
		locked, err := projectByRef(ctx, tx, p.ID, true)
		if err != nil {
			return err
		}
		if locked.State == "archived" {
			return errf(Refused, "%s is archived", locked.Name)
		}
		sp, _, err := specByRefTx(ctx, tx, locked.ID, in.SpecRef, true)
		if err != nil {
			return err
		}
		if sp.Tier == TierFoundation || sp.Number == 0 {
			return errf(Refused, "000-foundation.md requires explicit operator Project OK")
		}
		doc, err := b.Settings(ctx, tx, locked)
		if err != nil {
			return err
		}
		mode := EffectiveFeatureSpecsMode(ctx, tx, locked.ID, doc)
		allPaths := mergeUniquePaths(sp.TouchedPaths, in.TouchedPaths, ticketDocsPaths(in.Tickets))
		alters := sp.AltersFoundation || in.AltersFoundation
		escalate, escReason, _ := EvaluateSpecGuardrails(locked.State, mode, alters, allPaths)

		qID, qText, qRole, qTicketID, err := resolveSpecQuestionTx(ctx, tx, sp.ID, in.QuestionID, in.Question, in.AskingRole)
		if err != nil {
			return err
		}
		var decNum int
		if err := tx.QueryRow(ctx, `SELECT coalesce(max(number), 0) + 1 FROM spec_decisions WHERE project_id = $1`, locked.ID).Scan(&decNum); err != nil {
			return err
		}
		decKey := fmt.Sprintf("DEC-%03d", decNum)
		artifactPath := ".crew/decisions/" + decKey + ".md"
		alts := cleanAlternatives(in.Alternatives)
		altsJSON, _ := json.Marshal(alts)
		body := nonEmpty(strings.TrimSpace(in.BodyMarkdown), sp.BodyMarkdown)

		if escalate {
			proposedJSON, _ := json.Marshal(in.Tickets)
			decidedBy := "pending (@user)"
			if err := insertSpecDecisionTx(ctx, tx, locked.ID, sp.ID, decNum, decKey, qID, nil,
				qRole, decidedBy, qText, choice, altsJSON, rationale, "pending_operator", escReason, artifactPath, &res.Decision); err != nil {
				return err
			}
			res.Decision.SpecKey = sp.SpecKey
			res.Decision.Alternatives = alts
			row := tx.QueryRow(ctx, `UPDATE specs SET
					body_markdown = $2, state = 'escalated', approval_mode = 'manual',
					decided_by = $3, escalation_reason = $4, touched_paths = $5,
					alters_foundation = $6, proposed_tickets = $7::jsonb, updated_at = now()
				WHERE id = $1
				RETURNING id, project_id::text, $8::text, number, slug, spec_key, path, tier, title,
					body_markdown, state, approval_mode, decided_by, escalation_reason, touched_paths,
					alters_foundation, thread_ref, factory_task_id::text, emitted_keys, created_by,
					approved_at, created_at, updated_at, proposed_tickets`,
				sp.ID, body, decidedBy, escReason, nonNil(allPaths), alters, proposedJSON, locked.Name)
			updated, _, err := scanSpec(row)
			if err != nil {
				return err
			}
			if _, err := tx.Exec(ctx, `UPDATE spec_questions SET state = 'escalated' WHERE spec_id = $1 AND state IN ('open', 'processing')`, sp.ID); err != nil {
				return err
			}
			res.Spec = updated
			res.Escalated = true
			return db.RecordProjectChange(ctx, tx, locked.ID, nonEmpty(in.DecidedByRole, "planner"), "spec_decisions", decKey, nil, res)
		}

		decidedBy := formatAutonomousDecider(nonEmpty(in.DecidedByRole, "planner"), qRole)
		var legacyDecID *int64
		runRef := existingRunUUID(ctx, tx, in.DecidedByRun)
		var did int64
		if err := tx.QueryRow(ctx, `INSERT INTO decisions (project, ticket, run, role, question, alternatives, choice, reason)
			VALUES ($1, $2, nullif($3, '')::uuid, $4, $5, $6::jsonb, $7, $8) RETURNING id`,
			locked.ID, qTicketID, runRef, nonEmpty(in.DecidedByRole, "planner"), qText, altsJSON, choice, rationale).Scan(&did); err != nil {
			return err
		}
		legacyDecID = &did

		if err := insertSpecDecisionTx(ctx, tx, locked.ID, sp.ID, decNum, decKey, qID, legacyDecID,
			qRole, decidedBy, qText, choice, altsJSON, rationale, "approved", "", artifactPath, &res.Decision); err != nil {
			return err
		}
		res.Decision.SpecKey = sp.SpecKey
		res.Decision.Alternatives = alts

		emitted, err := b.emitSpecTicketsTx(ctx, tx, locked, doc, &sp, in.Tickets, decidedBy)
		if err != nil {
			return err
		}
		res.EmittedTickets = emitted
		emittedKeys := slices.Clone(sp.EmittedKeys)
		for _, tk := range emitted {
			emittedKeys = append(emittedKeys, tk.Key)
		}
		row := tx.QueryRow(ctx, `UPDATE specs SET
				body_markdown = $2, state = 'approved', approval_mode = 'autonomous',
				decided_by = $3, escalation_reason = '', touched_paths = $4,
				alters_foundation = false, proposed_tickets = '[]'::jsonb,
				emitted_keys = $5, approved_at = coalesce(approved_at, $6), updated_at = now()
			WHERE id = $1
			RETURNING id, project_id::text, $7::text, number, slug, spec_key, path, tier, title,
				body_markdown, state, approval_mode, decided_by, escalation_reason, touched_paths,
				alters_foundation, thread_ref, factory_task_id::text, emitted_keys, created_by,
				approved_at, created_at, updated_at, proposed_tickets`,
			sp.ID, body, decidedBy, nonNil(allPaths), nonNil(emittedKeys), b.now(), locked.Name)
		updated, _, err := scanSpec(row)
		if err != nil {
			return err
		}
		res.Spec = updated
		if _, err := tx.Exec(ctx, `UPDATE spec_questions
			SET state = 'answered', answer = $2, answered_at = now()
			WHERE spec_id = $1 AND state IN ('open', 'processing')`, sp.ID, choice+" — "+rationale); err != nil {
			return err
		}
		if updated.FactoryTaskID != nil {
			if _, err := tx.Exec(ctx, `UPDATE factory_tasks
				SET state = 'done', artifact_path = $2, summary = $3, updated_at = now()
				WHERE id = $1::uuid`, *updated.FactoryTaskID, updated.Path, truncateLine(decKey+": "+choice, maxTitle)); err != nil {
				return err
			}
		}
		sigDetail, _ := json.Marshal(map[string]any{
			"spec": updated.SpecKey, "decision": decKey, "decidedBy": decidedBy,
			"text": fmt.Sprintf("%s decided autonomously (%s): %s", updated.SpecKey, decKey, choice),
		})
		if _, err := tx.Exec(ctx, `INSERT INTO signals (project, kind, detail) VALUES ($1, 'spec_decision', $2::jsonb)`,
			locked.ID, sigDetail); err != nil {
			return err
		}
		return db.RecordProjectChange(ctx, tx, locked.ID, decidedBy, "spec_decisions", decKey, nil, res)
	})
	if err != nil {
		return nil, err
	}
	b.mirrorSpecDecisionOutcome(ctx, p, &res)
	if len(res.EmittedTickets) > 0 {
		_, _ = b.RankBacklog(ctx, p.ID)
	}
	msg := fmt.Sprintf("%s decided autonomously (%s): %s", res.Spec.SpecKey, res.Decision.Key, res.Decision.Choice)
	if res.Escalated {
		msg = fmt.Sprintf("%s (%s) escalated to @user Inbox: %s", res.Spec.SpecKey, res.Decision.Key, res.Spec.EscalationReason)
	} else if len(res.Spec.EmittedKeys) > 0 {
		msg += fmt.Sprintf(" -> Backlog %s", strings.Join(res.Spec.EmittedKeys, ", "))
	}
	firstTk := ""
	if len(res.Spec.EmittedKeys) > 0 {
		firstTk = res.Spec.EmittedKeys[0]
	}
	b.emitRoomEvent(ctx, p, res.Spec.ThreadRef, res.Spec.Path, firstTk, "planner", in.DecidedByRun, msg)
	return &res, nil
}

// OperatorDecideSpec handles @user Approve or Discard on an escalated or draft Tier-2 feature spec.
func (b *Board) OperatorDecideSpec(ctx context.Context, p *Project, specRef, choice, reason, who string) (*DecideSpecResult, error) {
	if p == nil {
		return nil, errf(Invalid, "project is required")
	}
	ch := strings.ToLower(strings.TrimSpace(choice))
	if ch != "approve" && ch != "discard" {
		return nil, errf(Invalid, "choice must be approve or discard")
	}
	who = nonEmpty(who, Operator)
	var res DecideSpecResult
	err := b.inTx(ctx, func(tx pgx.Tx) error {
		locked, err := projectByRef(ctx, tx, p.ID, true)
		if err != nil {
			return err
		}
		sp, proposedRaw, err := specByRefTx(ctx, tx, locked.ID, specRef, true)
		if err != nil {
			return err
		}
		if sp.Tier == TierFoundation || sp.Number == 0 {
			return errf(Refused, "000-foundation.md requires explicit operator Project OK (/ok)")
		}
		if ch == "discard" {
			row := tx.QueryRow(ctx, `UPDATE specs SET state = 'discarded', escalation_reason = $2,
					proposed_tickets = '[]'::jsonb, updated_at = now() WHERE id = $1
				RETURNING id, project_id::text, $3::text, number, slug, spec_key, path, tier, title,
					body_markdown, state, approval_mode, decided_by, escalation_reason, touched_paths,
					alters_foundation, thread_ref, factory_task_id::text, emitted_keys, created_by,
					approved_at, created_at, updated_at, proposed_tickets`,
				sp.ID, truncateLine(nonEmpty(reason, "discarded by operator"), 500), locked.Name)
			updated, _, err := scanSpec(row)
			if err != nil {
				return err
			}
			res.Spec = updated
			if _, err := tx.Exec(ctx, `UPDATE spec_decisions SET state = 'rejected', decided_by = $2, updated_at = now()
				WHERE spec_id = $1 AND state = 'pending_operator'`, sp.ID, who); err != nil {
				return err
			}
			if _, err := tx.Exec(ctx, `UPDATE spec_questions SET state = 'answered', answer = 'discarded by operator', answered_at = now()
				WHERE spec_id = $1 AND state IN ('open', 'processing', 'escalated')`, sp.ID); err != nil {
				return err
			}
			if updated.FactoryTaskID != nil {
				if _, err := tx.Exec(ctx, `UPDATE factory_tasks SET state = 'cancelled', updated_at = now() WHERE id = $1::uuid`, *updated.FactoryTaskID); err != nil {
					return err
				}
			}
			return db.RecordProjectChange(ctx, tx, locked.ID, who, "specs", sp.SpecKey, sp.State, updated.State)
		}
		if locked.State == "planning" {
			return errf(Refused, "%s waits for operator Project OK before feature specs enter Backlog", locked.Name)
		}
		var proposed []NewTicket
		if len(proposedRaw) > 0 && string(proposedRaw) != "[]" {
			if err := json.Unmarshal(proposedRaw, &proposed); err != nil {
				return err
			}
		}
		doc, err := b.Settings(ctx, tx, locked)
		if err != nil {
			return err
		}
		emitted, err := b.emitSpecTicketsTx(ctx, tx, locked, doc, &sp, proposed, who)
		if err != nil {
			return err
		}
		res.EmittedTickets = emitted
		emittedKeys := slices.Clone(sp.EmittedKeys)
		for _, tk := range emitted {
			emittedKeys = append(emittedKeys, tk.Key)
		}
		row := tx.QueryRow(ctx, `UPDATE specs SET state = 'approved', decided_by = $2,
				escalation_reason = '', proposed_tickets = '[]'::jsonb, emitted_keys = $3,
				approved_at = coalesce(approved_at, $4), updated_at = now()
			WHERE id = $1
			RETURNING id, project_id::text, $5::text, number, slug, spec_key, path, tier, title,
				body_markdown, state, approval_mode, decided_by, escalation_reason, touched_paths,
				alters_foundation, thread_ref, factory_task_id::text, emitted_keys, created_by,
				approved_at, created_at, updated_at, proposed_tickets`,
			sp.ID, who, nonNil(emittedKeys), b.now(), locked.Name)
		updated, _, err := scanSpec(row)
		if err != nil {
			return err
		}
		res.Spec = updated
		if _, err := tx.Exec(ctx, `UPDATE spec_decisions SET state = 'approved', decided_by = $2, updated_at = now()
			WHERE spec_id = $1 AND state = 'pending_operator'`, sp.ID, who); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `UPDATE spec_questions SET state = 'answered', answer = 'approved by operator', answered_at = now()
			WHERE spec_id = $1 AND state IN ('open', 'processing', 'escalated')`, sp.ID); err != nil {
			return err
		}
		if updated.FactoryTaskID != nil {
			if _, err := tx.Exec(ctx, `UPDATE factory_tasks SET state = 'done', artifact_path = $2, summary = 'approved by operator', updated_at = now()
				WHERE id = $1::uuid`, *updated.FactoryTaskID, updated.Path); err != nil {
				return err
			}
		}
		decs, err := b.listSpecDecisionsBySpec(ctx, tx, sp.ID)
		if err == nil && len(decs) > 0 {
			res.Decision = decs[len(decs)-1]
		}
		return db.RecordProjectChange(ctx, tx, locked.ID, who, "specs", sp.SpecKey, sp.State, updated.State)
	})
	if err != nil {
		return nil, err
	}
	b.mirrorSpecDecisionOutcome(ctx, p, &res)
	if len(res.EmittedTickets) > 0 {
		_, _ = b.RankBacklog(ctx, p.ID)
	}
	firstTk := ""
	var emittedKeys []string
	for _, tk := range res.EmittedTickets {
		if firstTk == "" {
			firstTk = tk.Key
		}
		emittedKeys = append(emittedKeys, tk.Key)
	}
	opMsg := fmt.Sprintf("%s %sd by @user", res.Spec.SpecKey, ch)
	if len(emittedKeys) > 0 {
		opMsg += " -> Backlog " + strings.Join(emittedKeys, ", ")
	}
	b.emitRoomEvent(ctx, p, res.Spec.ThreadRef, res.Spec.Path, firstTk, "user", "", opMsg)
	return &res, nil
}

// ListSpecDecisions lists all DEC-NNN feature spec decisions for a project, newest first.
func (b *Board) ListSpecDecisions(ctx context.Context, p *Project) ([]SpecDecision, error) {
	if p == nil {
		return nil, errf(Invalid, "project is required")
	}
	rows, err := b.Pool.Query(ctx, `SELECT d.id, d.project_id::text, d.number, d.key, d.spec_id, s.spec_key,
			d.question_id, d.decision_id, d.asking_role, d.decided_by, d.question, d.choice,
			d.alternatives, d.rationale, d.state, d.escalation_reason, d.artifact_path,
			d.prompt_key, d.prompt_version, d.provenance, d.source_message_id, d.superseded_by,
			d.created_at, d.updated_at
		FROM spec_decisions d JOIN specs s ON s.id = d.spec_id
		WHERE d.project_id = $1 ORDER BY d.number DESC`, p.ID)
	if err != nil {
		return nil, err
	}
	return collectSpecDecisions(rows)
}

func (b *Board) listSpecDecisionsBySpec(ctx context.Context, q db.Querier, specID int64) ([]SpecDecision, error) {
	rows, err := q.Query(ctx, `SELECT d.id, d.project_id::text, d.number, d.key, d.spec_id, s.spec_key,
			d.question_id, d.decision_id, d.asking_role, d.decided_by, d.question, d.choice,
			d.alternatives, d.rationale, d.state, d.escalation_reason, d.artifact_path,
			d.prompt_key, d.prompt_version, d.provenance, d.source_message_id, d.superseded_by,
			d.created_at, d.updated_at
		FROM spec_decisions d JOIN specs s ON s.id = d.spec_id
		WHERE d.spec_id = $1 ORDER BY d.number ASC`, specID)
	if err != nil {
		return nil, err
	}
	return collectSpecDecisions(rows)
}

func collectSpecDecisions(rows pgx.Rows) ([]SpecDecision, error) {
	defer rows.Close()
	out := []SpecDecision{}
	for rows.Next() {
		var d SpecDecision
		var altRaw []byte
		if err := rows.Scan(&d.ID, &d.ProjectID, &d.Number, &d.Key, &d.SpecID, &d.SpecKey,
			&d.QuestionID, &d.DecisionID, &d.AskingRole, &d.DecidedBy, &d.Question, &d.Choice,
			&altRaw, &d.Rationale, &d.State, &d.EscalationReason, &d.ArtifactPath,
			&d.PromptKey, &d.PromptVersion, &d.Provenance, &d.SourceMessageID, &d.SupersededBy,
			&d.CreatedAt, &d.UpdatedAt); err != nil {
			return nil, err
		}
		if len(altRaw) > 0 {
			_ = json.Unmarshal(altRaw, &d.Alternatives)
		}
		if d.Alternatives == nil {
			d.Alternatives = []string{}
		}
		out = append(out, d)
	}
	return out, rows.Err()
}

func insertSpecDecisionTx(ctx context.Context, tx pgx.Tx, projectID string, specID int64, num int, key string, qID, decID *int64,
	askingRole, decidedBy, question, choice string, altsJSON []byte, rationale, state, escReason, artifactPath string, out *SpecDecision) error {
	var altOut []byte
	err := tx.QueryRow(ctx, `INSERT INTO spec_decisions (
			project_id, number, key, spec_id, question_id, decision_id, asking_role,
			decided_by, question, choice, alternatives, rationale, state, escalation_reason, artifact_path)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11::jsonb, $12, $13, $14, $15)
		RETURNING id, project_id::text, number, key, spec_id, question_id, decision_id, asking_role,
			decided_by, question, choice, alternatives, rationale, state, escalation_reason, artifact_path,
			prompt_key, prompt_version, provenance, source_message_id, superseded_by, created_at, updated_at`,
		projectID, num, key, specID, qID, decID, askingRole, decidedBy, question, choice,
		altsJSON, rationale, state, escReason, artifactPath).Scan(
		&out.ID, &out.ProjectID, &out.Number, &out.Key, &out.SpecID, &out.QuestionID, &out.DecisionID, &out.AskingRole,
		&out.DecidedBy, &out.Question, &out.Choice, &altOut, &out.Rationale, &out.State, &out.EscalationReason,
		&out.ArtifactPath, &out.PromptKey, &out.PromptVersion, &out.Provenance, &out.SourceMessageID, &out.SupersededBy,
		&out.CreatedAt, &out.UpdatedAt)
	if err == nil && len(altOut) > 0 {
		_ = json.Unmarshal(altOut, &out.Alternatives)
	}
	return err
}

func (b *Board) emitSpecTicketsTx(ctx context.Context, tx pgx.Tx, p *Project, doc settings.Doc, sp *Spec, items []NewTicket, who string) ([]*Ticket, error) {
	if len(items) == 0 {
		return nil, nil
	}
	out := make([]*Ticket, 0, len(items))
	earlier := make([]string, 0, len(items))
	for _, n := range items {
		if strings.TrimSpace(n.SpecRef) == "" {
			n.SpecRef = sp.Path
		}
		n.Approved = true
		tk, err := b.addTicket(ctx, tx, p, doc, n, OriginPlan, who, earlier)
		if err != nil {
			return nil, err
		}
		out = append(out, tk)
		earlier = append(earlier, tk.Key)
	}
	return out, nil
}

func resolveSpecQuestionTx(ctx context.Context, tx pgx.Tx, specID, questionID int64, explicitQ, explicitRole string) (*int64, string, string, *int64, error) {
	var id int64
	var q, role string
	var tid *int64
	var err error
	if questionID > 0 {
		err = tx.QueryRow(ctx, `SELECT id, question, asking_role, ticket_id FROM spec_questions WHERE id = $1 AND spec_id = $2`,
			questionID, specID).Scan(&id, &q, &role, &tid)
	} else {
		err = tx.QueryRow(ctx, `SELECT id, question, asking_role, ticket_id FROM spec_questions
			WHERE spec_id = $1 AND state IN ('open', 'processing') ORDER BY id DESC LIMIT 1`, specID).Scan(&id, &q, &role, &tid)
	}
	if err == nil {
		return &id, nonEmpty(explicitQ, q), nonEmpty(explicitRole, role), tid, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return nil, "", "", nil, err
	}
	return nil, nonEmpty(explicitQ, "Feature design contract"), nonEmpty(explicitRole, ""), nil, nil
}

func existingRunUUID(ctx context.Context, tx pgx.Tx, runID string) string {
	runID = strings.TrimSpace(runID)
	if runID == "" {
		return ""
	}
	var exists bool
	if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM runs WHERE id::text = $1)`, runID).Scan(&exists); err != nil || !exists {
		return ""
	}
	return runID
}

func formatAutonomousDecider(deciderRole, askingRole string) string {
	d := normalizePeerRole(deciderRole)
	if d == "builder" && strings.TrimSpace(deciderRole) == "" {
		d = "planner"
	}
	a := strings.ToLower(strings.TrimPrefix(strings.TrimSpace(askingRole), "@"))
	if a != "" && a != d {
		return fmt.Sprintf("autonomous (@%s + @%s)", d, a)
	}
	return fmt.Sprintf("autonomous (@%s)", d)
}

func mergeUniquePaths(a, b, c []string) []string {
	seen := map[string]bool{}
	out := []string{}
	for _, list := range [][]string{a, b, c} {
		for _, p := range list {
			p = cleanRepoRelPath(p)
			if p == "" || seen[p] {
				continue
			}
			seen[p] = true
			out = append(out, p)
		}
	}
	return out
}

func ticketDocsPaths(tks []NewTicket) []string {
	var out []string
	for _, tk := range tks {
		out = append(out, tk.Docs...)
	}
	return out
}

func cleanAlternatives(alts []string) []string {
	var out []string
	for _, a := range alts {
		if a = strings.TrimSpace(a); a != "" {
			out = append(out, a)
		}
	}
	if out == nil {
		out = []string{}
	}
	return out
}

func (b *Board) mirrorSpecDecisionOutcome(ctx context.Context, p *Project, res *DecideSpecResult) {
	if b.Git == nil || b.Identity == nil || !b.Git.Exists(p.Name) || res.Spec.Path == "" {
		return
	}
	id, err := b.Identity(ctx)
	if err != nil {
		return
	}
	overlay := gitstore.Overlay{
		res.Spec.Path: func([]byte) []byte { return renderFeatureSpecMarkdown(res.Spec) },
	}
	if res.Decision.Key != "" && res.Decision.ArtifactPath != "" {
		decBytes := renderSpecDecisionMarkdown(res.Spec, res.Decision)
		overlay[res.Decision.ArtifactPath] = func([]byte) []byte { return decBytes }
	}
	for _, tk := range res.EmittedTickets {
		if raw, err := formatMirrorTicket(tk, tk.Stage); err == nil {
			overlay[fmt.Sprintf(".crew/tickets/%s.md", tk.Key)] = func([]byte) []byte { return raw }
		}
	}
	_, _ = b.Git.CommitOverlay(ctx, p.Name, overlay, fmt.Sprintf("%s: %s (%s)", p.Key, res.Spec.SpecKey, res.Spec.State), id)
}

func renderSpecDecisionMarkdown(sp Spec, d SpecDecision) []byte {
	var buf bytes.Buffer
	buf.WriteString("---\n")
	enc := yaml.NewEncoder(&buf)
	enc.SetIndent(2)
	_ = enc.Encode(map[string]any{
		"decision":   d.Key,
		"spec":       sp.SpecKey,
		"state":      d.State,
		"decided_by": d.DecidedBy,
		"choice":     d.Choice,
	})
	_ = enc.Close()
	fmt.Fprintf(&buf, "---\n\n# %s: %s\n\n- Spec: `%s`\n- State: `%s`\n- Decided by: `%s`\n\n## Question\n\n%s\n\n## Choice\n\n%s\n\n## Rationale\n\n%s\n",
		d.Key, sp.Title, sp.SpecKey, d.State, d.DecidedBy, d.Question, d.Choice, d.Rationale)
	return buf.Bytes()
}
