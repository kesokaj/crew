package rooms

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"slices"
	"strings"

	"github.com/jackc/pgx/v5"

	"github.com/kesokaj/crew/internal/board"
)

// MaxChoicePromptBytes bounds the serialized size of one keyed choice prompt card.
const MaxChoicePromptBytes = 32 * 1024

// ErrStaleChoiceVersion is returned when answering an outdated version of a keyed choice prompt.
var ErrStaleChoiceVersion = errors.New("choice prompt version has been superseded")

var choiceKeyRe = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{1,47}$`)

// ChoiceOption represents one clickable answer option in a keyed chat choice card.
type ChoiceOption struct {
	ID          string `json:"id"`
	Label       string `json:"label"`
	Description string `json:"description,omitempty"`
}

// ChoicePromptSpec defines an interactive keyed decision card posted into #<project> or feat/<NNN>.
type ChoicePromptSpec struct {
	ID       string         `json:"id"`
	Version  int            `json:"version"`
	Update   bool           `json:"update,omitempty"`
	Replaced bool           `json:"replaced,omitempty"`
	Question string         `json:"question"`
	Choices  []ChoiceOption `json:"choices"`
	Default  string         `json:"default,omitempty"`
	SpecRef  string         `json:"specRef,omitempty"`
}

// ChoiceResponseSpec records how a keyed choice card was resolved and its provenance.
type ChoiceResponseSpec struct {
	PromptKey       string `json:"promptKey"`
	PromptVersion   int    `json:"promptVersion"`
	Choice          string `json:"choice"`
	Provenance      string `json:"provenance"`
	SourceMessageID int64  `json:"sourceMessageId,omitempty"`
	Rationale       string `json:"rationale,omitempty"`
}

// RespondChoiceInput answers a keyed choice prompt directly (@user) or via agent interpretation.
type RespondChoiceInput struct {
	PromptKey       string `json:"promptKey"`
	PromptVersion   int    `json:"promptVersion"`
	Choice          string `json:"choice"`
	SenderRole      string `json:"senderRole"`
	RunID           string `json:"runId"`
	SourceMessageID int64  `json:"sourceMessageId"`
	Rationale       string `json:"rationale"`
}

// ValidateChoicePrompt checks bounds and structure for a structured chat choice prompt card.
func ValidateChoicePrompt(spec *ChoicePromptSpec) error {
	if spec == nil {
		return fmt.Errorf("%w: choicePrompt is required when kind is choice_prompt", ErrInvalid)
	}
	spec.ID = strings.ToLower(strings.TrimSpace(spec.ID))
	if !choiceKeyRe.MatchString(spec.ID) {
		return fmt.Errorf("%w: choicePrompt.id must match ^[a-z0-9][a-z0-9_-]{1,47}$", ErrInvalid)
	}
	spec.Question = strings.TrimSpace(spec.Question)
	if spec.Question == "" || len([]rune(spec.Question)) > 2000 {
		return fmt.Errorf("%w: choicePrompt.question must be 1..2000 characters", ErrInvalid)
	}
	if len(spec.Choices) < 2 || len(spec.Choices) > 10 {
		return fmt.Errorf("%w: choicePrompt.choices must have 2..10 options", ErrInvalid)
	}
	seenID := map[string]bool{}
	for i := range spec.Choices {
		c := &spec.Choices[i]
		c.Label = strings.TrimSpace(c.Label)
		c.ID = strings.TrimSpace(c.ID)
		if c.ID == "" {
			c.ID = c.Label
		}
		if c.ID == "" || c.Label == "" {
			return fmt.Errorf("%w: each choice requires id and label", ErrInvalid)
		}
		if seenID[c.ID] {
			return fmt.Errorf("%w: duplicate choice option %q", ErrInvalid, c.ID)
		}
		seenID[c.ID] = true
	}
	spec.Default = strings.TrimSpace(spec.Default)
	if spec.Default != "" && !seenID[spec.Default] {
		return fmt.Errorf("%w: choicePrompt.default %q is not one of choices", ErrInvalid, spec.Default)
	}
	raw, err := json.Marshal(spec)
	if err != nil {
		return err
	}
	if len(raw) > MaxChoicePromptBytes {
		return fmt.Errorf("%w: choicePrompt exceeds %d bytes", ErrInvalid, MaxChoicePromptBytes)
	}
	return nil
}

func prepareChoicePromptTx(ctx context.Context, tx pgx.Tx, projectID string, spec *ChoicePromptSpec) error {
	var prevID int64
	var prevRaw []byte
	err := tx.QueryRow(ctx, `SELECT id, choice_prompt FROM chat_messages
		WHERE project_id = $1::uuid AND kind = 'choice_prompt' AND choice_prompt->>'id' = $2
		ORDER BY id DESC LIMIT 1 FOR UPDATE`, projectID, spec.ID).Scan(&prevID, &prevRaw)
	if errors.Is(err, pgx.ErrNoRows) {
		spec.Version = 1
		spec.Update = false
		spec.Replaced = false
		return nil
	}
	if err != nil {
		return err
	}
	if !spec.Update {
		return fmt.Errorf("%w: choice prompt %q already exists (pass update=true to revise)", ErrInvalid, spec.ID)
	}
	var prev ChoicePromptSpec
	if err := json.Unmarshal(prevRaw, &prev); err != nil {
		return err
	}
	prev.Replaced = true
	updatedRaw, err := json.Marshal(&prev)
	if err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `UPDATE chat_messages SET choice_prompt = $2::jsonb WHERE id = $1`, prevID, updatedRaw); err != nil {
		return err
	}
	spec.Version = max(prev.Version, 1) + 1
	spec.Update = false
	spec.Replaced = false
	return nil
}

// RespondChoicePrompt records a choice answer directly from @user or interpreted by @planner/@coordinator,
// posting a choice_response message and updating spec_decisions with full provenance.
func (s *Service) RespondChoicePrompt(ctx context.Context, p *board.Project, in RespondChoiceInput) (*PostResult, *board.SpecDecision, error) {
	if p == nil {
		return nil, nil, fmt.Errorf("%w: project is required", ErrInvalid)
	}
	key := strings.ToLower(strings.TrimSpace(in.PromptKey))
	choice := strings.TrimSpace(in.Choice)
	if key == "" || choice == "" {
		return nil, nil, fmt.Errorf("%w: promptKey and choice are required", ErrInvalid)
	}
	senderRole, senderHandle, err := s.normalizeProjectSender(ctx, p, in.SenderRole)
	if err != nil {
		return nil, nil, err
	}
	provenance := "direct"
	if senderRole != "user" {
		if !slices.Contains([]string{"planner", "coordinator", "curator"}, senderRole) {
			return nil, nil, fmt.Errorf("%w: only @user, @planner, @coordinator, or @curator may record a choice response", ErrInvalid)
		}
		provenance = "agent_interpreted"
		if in.SourceMessageID <= 0 {
			return nil, nil, fmt.Errorf("%w: agent_interpreted response requires sourceMessageId", ErrInvalid)
		}
	}

	var promptMsgID int64
	var roomID int64
	var threadID *int64
	var threadKey string
	var promptRaw []byte
	err = s.Pool.QueryRow(ctx, `SELECT m.id, m.room_id, m.thread_id, coalesce(th.thread_key, ''), m.choice_prompt
		FROM chat_messages m
		LEFT JOIN chat_threads th ON th.id = m.thread_id
		WHERE m.project_id = $1::uuid AND m.kind = 'choice_prompt' AND m.choice_prompt->>'id' = $2
		ORDER BY m.id DESC LIMIT 1`, p.ID, key).Scan(&promptMsgID, &roomID, &threadID, &threadKey, &promptRaw)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil, fmt.Errorf("choice prompt %q not found", key)
	}
	if err != nil {
		return nil, nil, err
	}
	var spec ChoicePromptSpec
	if err := json.Unmarshal(promptRaw, &spec); err != nil {
		return nil, nil, err
	}
	if spec.Replaced || (in.PromptVersion > 0 && in.PromptVersion != spec.Version) {
		return nil, nil, ErrStaleChoiceVersion
	}
	matchedChoice := ""
	var alts []string
	for _, opt := range spec.Choices {
		alts = append(alts, opt.Label)
		if opt.ID == choice || strings.EqualFold(opt.Label, choice) {
			matchedChoice = opt.ID
		}
	}
	if matchedChoice == "" {
		return nil, nil, fmt.Errorf("%w: choice %q is not an option on prompt %q", ErrInvalid, choice, key)
	}

	if provenance == "agent_interpreted" {
		var srcProj string
		var srcRoom int64
		var srcThread *int64
		var srcRole string
		serr := s.Pool.QueryRow(ctx, `SELECT project_id::text, room_id, thread_id, sender_role FROM chat_messages WHERE id = $1`, in.SourceMessageID).
			Scan(&srcProj, &srcRoom, &srcThread, &srcRole)
		if errors.Is(serr, pgx.ErrNoRows) {
			return nil, nil, fmt.Errorf("%w: sourceMessageId %d not found", ErrInvalid, in.SourceMessageID)
		}
		if serr != nil {
			return nil, nil, serr
		}
		if srcProj != p.ID {
			return nil, nil, ErrCrossProjectBoundary
		}
		if srcRoom != roomID || !ptrInt64Eq(srcThread, threadID) || srcRole != "user" {
			return nil, nil, fmt.Errorf("%w: sourceMessageId %d must be a @user message in the same thread", ErrInvalid, in.SourceMessageID)
		}
	}

	var decision board.SpecDecision
	err = pgx.BeginFunc(ctx, s.Pool, func(tx pgx.Tx) error {
		var existingNum int
		var existingProv string
		qerr := tx.QueryRow(ctx, `SELECT number, provenance FROM spec_decisions
			WHERE project_id = $1::uuid AND prompt_key = $2 AND prompt_version = $3 AND state <> 'overridden'
			ORDER BY id DESC LIMIT 1 FOR UPDATE`, p.ID, key, spec.Version).Scan(&existingNum, &existingProv)
		if qerr != nil && !errors.Is(qerr, pgx.ErrNoRows) {
			return qerr
		}
		if qerr == nil && existingProv == "direct" && provenance == "agent_interpreted" {
			return fmt.Errorf("%w: cannot overwrite direct @user decision DEC-%03d on %s v%d", ErrInvalid, existingNum, key, spec.Version)
		}

		specID, spErr := resolveChoiceSpecIDTx(ctx, tx, p.ID, spec.SpecRef, threadID)
		if spErr != nil {
			return spErr
		}
		var nextNum int
		if err := tx.QueryRow(ctx, `SELECT coalesce(max(number), 0) + 1 FROM spec_decisions WHERE project_id = $1::uuid`, p.ID).Scan(&nextNum); err != nil {
			return err
		}
		decKey := fmt.Sprintf("DEC-%03d", nextNum)
		if qerr == nil && existingProv == "agent_interpreted" && provenance == "direct" {
			if _, uerr := tx.Exec(ctx, `UPDATE spec_decisions SET state = 'overridden', superseded_by = $3, updated_at = now()
				WHERE project_id = $1::uuid AND number = $2`, p.ID, existingNum, nextNum); uerr != nil {
				return uerr
			}
		}
		altsJSON, _ := json.Marshal(alts)
		rationale := strings.TrimSpace(in.Rationale)
		if rationale == "" {
			rationale = fmt.Sprintf("Recorded %s selection %q for choice prompt %s (v%d).", provenance, matchedChoice, key, spec.Version)
		}
		var srcPtr *int64
		if in.SourceMessageID > 0 {
			v := in.SourceMessageID
			srcPtr = &v
		}
		var altsOut []byte
		err := tx.QueryRow(ctx, `INSERT INTO spec_decisions (
				project_id, number, key, spec_id, asking_role, decided_by, question, choice, alternatives,
				rationale, state, artifact_path, prompt_key, prompt_version, provenance, source_message_id)
			VALUES ($1::uuid, $2, $3, $4, $5, $6, $7, $8, $9::jsonb, $10, 'approved', '', $11, $12, $13, $14)
			RETURNING id, project_id::text, number, key, spec_id, question_id, decision_id, asking_role,
				decided_by, question, choice, alternatives, rationale, state, escalation_reason, artifact_path,
				prompt_key, prompt_version, provenance, source_message_id, superseded_by, created_at, updated_at`,
			p.ID, nextNum, decKey, specID, senderRole, senderHandle, spec.Question, matchedChoice, altsJSON,
			rationale, key, spec.Version, provenance, srcPtr).Scan(
			&decision.ID, &decision.ProjectID, &decision.Number, &decision.Key, &decision.SpecID,
			&decision.QuestionID, &decision.DecisionID, &decision.AskingRole, &decision.DecidedBy,
			&decision.Question, &decision.Choice, &altsOut, &decision.Rationale, &decision.State,
			&decision.EscalationReason, &decision.ArtifactPath, &decision.PromptKey, &decision.PromptVersion,
			&decision.Provenance, &decision.SourceMessageID, &decision.SupersededBy, &decision.CreatedAt, &decision.UpdatedAt)
		if err != nil {
			return err
		}
		_ = json.Unmarshal(altsOut, &decision.Alternatives)
		return nil
	})
	if err != nil {
		return nil, nil, err
	}

	no := false
	respSpec := &ChoiceResponseSpec{
		PromptKey:       key,
		PromptVersion:   spec.Version,
		Choice:          matchedChoice,
		Provenance:      provenance,
		SourceMessageID: in.SourceMessageID,
		Rationale:       strings.TrimSpace(in.Rationale),
	}
	res, perr := s.PostMessage(ctx, p, PostInput{
		ThreadKey:      threadKey,
		ReplyToID:      promptMsgID,
		SenderRole:     in.SenderRole,
		RunID:          in.RunID,
		Kind:           "choice_response",
		Intent:         "inform",
		Body:           fmt.Sprintf("Decision `%s` (%s v%d): selected `%s` (%s -> %s)", decision.Key, key, spec.Version, matchedChoice, provenance, senderHandle),
		Actionable:     &no,
		ChoiceResponse: respSpec,
	})
	if perr != nil {
		return nil, &decision, perr
	}
	return res, &decision, nil
}

func resolveChoiceSpecIDTx(ctx context.Context, tx pgx.Tx, projectID, specRef string, threadID *int64) (int64, error) {
	if ref := strings.TrimSpace(specRef); ref != "" {
		var id int64
		if err := tx.QueryRow(ctx, `SELECT id FROM specs WHERE project_id = $1::uuid AND (upper(spec_key) = upper($2) OR path = $2 OR slug = $2) LIMIT 1`,
			projectID, ref).Scan(&id); err == nil {
			return id, nil
		}
	}
	if threadID != nil {
		var id int64
		if err := tx.QueryRow(ctx, `SELECT spec_id FROM chat_threads WHERE id = $1 AND spec_id IS NOT NULL`, *threadID).Scan(&id); err == nil {
			return id, nil
		}
	}
	var fallback int64
	if err := tx.QueryRow(ctx, `SELECT id FROM specs WHERE project_id = $1::uuid ORDER BY number ASC LIMIT 1`, projectID).Scan(&fallback); err != nil {
		return 0, fmt.Errorf("project has no spec ledger row for decision recording: %w", err)
	}
	return fallback, nil
}
