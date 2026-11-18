package board

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"go.yaml.in/yaml/v3"

	"github.com/kesokaj/crew/internal/db"
	"github.com/kesokaj/crew/internal/gitstore"
)

// ResearchActions are the allowed recommended next actions for a @researcher brief.
var ResearchActions = []string{"retry_builder", "ask_planner", "ask_user"}

// ResearchBrief holds one clean-context @researcher diagnosis saved after a ticket hits max_tries.
type ResearchBrief struct {
	ID                int64     `json:"id"`
	ProjectID         string    `json:"projectId"`
	TicketID          int64     `json:"ticketId"`
	TicketKey         string    `json:"ticketKey"`
	TaskID            *string   `json:"taskId,omitempty"`
	RunID             *string   `json:"runId,omitempty"`
	TriesUsed         int       `json:"triesUsed"`
	WIPRef            string    `json:"wipRef,omitempty"`
	HeadSHA           string    `json:"headSha,omitempty"`
	ArtifactPath      string    `json:"artifactPath"`
	Diagnosis         string    `json:"diagnosis"`
	RecommendedAction string    `json:"recommendedAction"`
	CreatedAt         time.Time `json:"createdAt"`
}

// SaveResearchBriefInput describes one research brief to persist to PostgreSQL and git mirror.
type SaveResearchBriefInput struct {
	ProjectID         string
	Ticket            *Ticket
	TaskID            string
	RunID             string
	SubtaskID         int64
	WIPRef            string
	HeadSHA           string
	Diagnosis         string
	RecommendedAction string
}

type researchFrontMatter struct {
	Ticket            string `yaml:"ticket"`
	TaskID            string `yaml:"task,omitempty"`
	TriesUsed         int    `yaml:"tries_used"`
	WIPRef            string `yaml:"wip_ref,omitempty"`
	HeadSHA           string `yaml:"head_sha,omitempty"`
	RecommendedAction string `yaml:"recommended_action"`
}

func renderResearchBriefMarkdown(t *Ticket, in SaveResearchBriefInput, artifactPath string) ([]byte, error) {
	var buf bytes.Buffer
	buf.WriteString("---\n")
	enc := yaml.NewEncoder(&buf)
	enc.SetIndent(2)
	err := enc.Encode(researchFrontMatter{
		Ticket: t.Key, TaskID: strings.TrimSpace(in.TaskID), TriesUsed: t.TriesUsed,
		WIPRef: strings.TrimSpace(in.WIPRef), HeadSHA: strings.TrimSpace(in.HeadSHA),
		RecommendedAction: in.RecommendedAction,
	})
	if err != nil {
		return nil, err
	}
	if err := enc.Close(); err != nil {
		return nil, err
	}
	fmt.Fprintf(&buf, "---\n\n# Research Brief: %s\n\n- Artifact: `%s`\n- Recommended Action: `%s`\n\n## Diagnosis\n\n%s\n",
		t.Key, artifactPath, in.RecommendedAction, strings.TrimSpace(in.Diagnosis))
	return buf.Bytes(), nil
}

// SaveResearchBrief stores a @researcher brief in research_briefs, completes the linked factory_task,
// writes a ticket note, and mirrors .crew/tickets/<KEY>-research.md onto canonical main.
func (b *Board) SaveResearchBrief(ctx context.Context, p *Project, in SaveResearchBriefInput) (*ResearchBrief, error) {
	if p == nil || in.Ticket == nil {
		return nil, errf(Invalid, "project and ticket are required")
	}
	diag := strings.TrimSpace(in.Diagnosis)
	if diag == "" {
		return nil, errf(Invalid, "diagnosis is required")
	}
	action := strings.TrimSpace(in.RecommendedAction)
	if action == "" {
		action = "retry_builder"
	}
	if !slices.Contains(ResearchActions, action) {
		return nil, errf(Invalid, "recommendedAction must be retry_builder, ask_planner, or ask_user")
	}
	artifactPath := ".crew/tickets/" + in.Ticket.Key + "-research.md"
	mdBytes, err := renderResearchBriefMarkdown(in.Ticket, in, artifactPath)
	if err != nil {
		return nil, err
	}
	if b.Git != nil && b.Identity != nil && b.Git.Exists(p.Name) {
		if id, ierr := b.Identity(ctx); ierr == nil {
			_, _ = b.Git.CommitOverlay(ctx, p.Name, gitstore.Overlay{
				artifactPath: func([]byte) []byte { return mdBytes },
			}, fmt.Sprintf("%s: add @researcher diagnostic brief", in.Ticket.Key), id)
		}
	}
	var brief ResearchBrief
	err = b.inTx(ctx, func(tx pgx.Tx) error {
		err := tx.QueryRow(ctx, `INSERT INTO research_briefs (
				project_id, ticket_id, task_id, run_id, tries_used, wip_ref, head_sha, artifact_path, diagnosis, recommended_action)
			VALUES ($1::uuid, $2, nullif($3, '')::uuid, nullif($4, '')::uuid, $5, $6, $7, $8, $9, $10)
			RETURNING id, project_id::text, ticket_id, task_id::text, run_id::text, tries_used, wip_ref, head_sha,
			          artifact_path, diagnosis, recommended_action, created_at`,
			p.ID, in.Ticket.ID, strings.TrimSpace(in.TaskID), strings.TrimSpace(in.RunID),
			in.Ticket.TriesUsed, strings.TrimSpace(in.WIPRef), strings.TrimSpace(in.HeadSHA),
			artifactPath, diag, action).
			Scan(&brief.ID, &brief.ProjectID, &brief.TicketID, &brief.TaskID, &brief.RunID,
				&brief.TriesUsed, &brief.WIPRef, &brief.HeadSHA, &brief.ArtifactPath,
				&brief.Diagnosis, &brief.RecommendedAction, &brief.CreatedAt)
		if err != nil {
			return err
		}
		brief.TicketKey = in.Ticket.Key
		if strings.TrimSpace(in.TaskID) != "" {
			if _, err := tx.Exec(ctx, `UPDATE factory_tasks
				SET state = 'done', artifact_path = $2, summary = $3, updated_at = now()
				WHERE id = $1::uuid AND project_id = $4::uuid`,
				strings.TrimSpace(in.TaskID), artifactPath, diag, p.ID); err != nil {
				return err
			}
		}
		return db.RecordProjectChange(ctx, tx, p.ID, "researcher", "research_briefs", in.Ticket.Key, nil, brief)
	})
	if err != nil {
		return nil, err
	}
	noteBody := fmt.Sprintf("Research Brief (%s -> %s):\n\n%s\n\nArtifact: %s",
		in.Ticket.Key, action, diag, artifactPath)
	if len(noteBody) > MaxRecord {
		noteBody = noteBody[:MaxRecord]
	}
	_, _ = b.AddRecord(ctx, p, in.Ticket.Key, NewRecord{
		Kind: RecordNote, Author: "researcher", Run: in.RunID, Subtask: in.SubtaskID, Body: noteBody,
	})
	b.emitRoomEvent(ctx, p, "", in.Ticket.SpecRef, in.Ticket.Key, "researcher", in.RunID,
		fmt.Sprintf("Research Brief (%s -> %s): %s [%s]", in.Ticket.Key, action, diag, artifactPath))
	return &brief, nil
}

// ListResearchBriefs returns diagnostic @researcher briefs for one ticket, newest first.
func (b *Board) ListResearchBriefs(ctx context.Context, p *Project, ticketID int64, ticketKey string) ([]ResearchBrief, error) {
	rows, err := b.Pool.Query(ctx, `SELECT id, project_id::text, ticket_id, task_id::text, run_id::text,
			tries_used, wip_ref, head_sha, artifact_path, diagnosis, recommended_action, created_at
		FROM research_briefs WHERE project_id = $1::uuid AND ticket_id = $2
		ORDER BY created_at DESC, id DESC`, p.ID, ticketID)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(r pgx.CollectableRow) (ResearchBrief, error) {
		var x ResearchBrief
		x.TicketKey = ticketKey
		err := r.Scan(&x.ID, &x.ProjectID, &x.TicketID, &x.TaskID, &x.RunID,
			&x.TriesUsed, &x.WIPRef, &x.HeadSHA, &x.ArtifactPath, &x.Diagnosis, &x.RecommendedAction, &x.CreatedAt)
		return x, err
	})
}

// LatestResearchTaskID looks up the latest factory_task id linked to a research run/ticket.
func (b *Board) LatestResearchTaskID(ctx context.Context, ticketID int64) string {
	var id string
	err := b.Pool.QueryRow(ctx, `SELECT id::text FROM factory_tasks
		WHERE ticket_id = $1 AND kind = 'research' ORDER BY created_at DESC LIMIT 1`, ticketID).Scan(&id)
	if errors.Is(err, pgx.ErrNoRows) {
		return ""
	}
	return id
}
