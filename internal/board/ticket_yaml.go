package board

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"

	"github.com/jackc/pgx/v5"

	"github.com/kesokaj/crew/internal/settings"
)

// FormatTicketYAML renders one ticket as .crew/tickets/<KEY>.md YAML frontmatter + Markdown sections.
func (b *Board) FormatTicketYAML(ctx context.Context, p *Project, ref string) ([]byte, error) {
	t, err := b.Ticket(ctx, p, ref)
	if err != nil {
		return nil, err
	}
	return formatMirrorTicket(t, t.Stage)
}

// SyncTicketsFromMain imports new executable .crew/tickets/<KEY>.md files from main and updates
// allowed YAML metadata on existing tickets while enforcing R179 / Rule 3 (never moving stages
// backwards or reopening done/closed tickets, and rejecting non-executable ticket kinds).
func (b *Board) SyncTicketsFromMain(ctx context.Context, p *Project, who string) (added int, updated int, err error) {
	if who == "" {
		who = Backend
	}
	doc, err := b.Settings(ctx, b.Pool, p)
	if err != nil {
		return 0, 0, err
	}
	pipe := PipelineOf(doc)
	files, names, err := b.readMirrorFiles(ctx, p)
	if err != nil {
		return 0, 0, err
	}
	if err := validateExecutableMirrorKinds(files, names); err != nil {
		return 0, 0, err
	}
	mts, err := decodeMirrorTickets(p, pipe, files, names)
	if err != nil {
		return 0, 0, err
	}
	if len(mts) == 0 {
		return 0, 0, nil
	}
	err = b.inTx(ctx, func(tx pgx.Tx) error {
		var newOnes []MirrorTicket
		for _, mt := range mts {
			num, _ := strconv.Atoi(mt.ID)
			cur, gerr := ticketByNumber(ctx, tx, p.ID, num, true)
			if gerr != nil {
				var berr *Error
				if errors.As(gerr, &berr) && berr.Kind == NotFound {
					if mt.Stage != settings.StageBacklog && mt.Stage != settings.StagePlanning {
						// Newly discovered YAML tickets enter the active flow via Backlog.
						mt.Stage = settings.StageBacklog
						if mt.Analysis.Status == "" {
							mt.Analysis.Status = StatusWaiting
						}
					}
					newOnes = append(newOnes, mt)
					continue
				}
				return gerr
			}
			changed, uerr := syncExistingTicketYAML(ctx, tx, cur, mt)
			if uerr != nil {
				return uerr
			}
			if changed {
				updated++
			}
		}
		if len(newOnes) > 0 {
			if err := insertMirrorTickets(ctx, tx, p, newOnes, who); err != nil {
				return err
			}
			added = len(newOnes)
		}
		for _, mt := range mts {
			if len(mt.DependsOn) == 0 {
				continue
			}
			num, _ := strconv.Atoi(mt.ID)
			cur, gerr := ticketByNumber(ctx, tx, p.ID, num, false)
			if gerr != nil {
				return gerr
			}
			for _, dep := range mt.DependsOn {
				dep = strings.TrimSpace(dep)
				if dep == "" {
					continue
				}
				if err := b.link(ctx, tx, p, cur, dep, "depends_on"); err != nil {
					return err
				}
			}
		}
		return nil
	})
	return added, updated, err
}

func validateExecutableMirrorKinds(files map[string][]byte, names []string) error {
	for _, k := range names {
		if !mirrorTicketFile.MatchString(k) {
			continue
		}
		mt, err := parseMirrorTicket(files[k])
		if err != nil {
			return fmt.Errorf("%s: %w", k, err)
		}
		if strings.TrimSpace(mt.Kind) != "" && !slices.Contains(mirrorKinds, mt.Kind) {
			return errf(Refused, "%s: non-executable ticket kind %q (only executable code tickets enter Kanban)", k, mt.Kind)
		}
	}
	return nil
}

func syncExistingTicketYAML(ctx context.Context, tx pgx.Tx, cur *Ticket, mt MirrorTicket) (bool, error) {
	// Never touch closed/done tickets' stages or widen unapproved may_change without approval.
	analysis := cur.Analysis
	if len(mt.Analysis.MayChange) > 0 && !slices.Equal(analysis.MayChange, mt.Analysis.MayChange) {
		if !analysis.Approved {
			analysis.MayChange = slices.Clone(mt.Analysis.MayChange)
		}
	}
	maxTries := mt.MaxTries
	if maxTries < 1 || maxTries > 20 {
		maxTries = cur.MaxTries
	}
	changed := mt.Spec != cur.SpecRef ||
		maxTries != cur.MaxTries ||
		mt.AssignedAgent != cur.AssignedAgent ||
		mt.BudgetTokens != cur.BudgetTokens ||
		!slices.Equal(analysis.MayChange, cur.Analysis.MayChange)
	if !changed {
		return false, nil
	}
	_, err := tx.Exec(ctx, `UPDATE tickets SET spec_ref = $2, max_tries = $3, assigned_agent = $4, budget_tokens = $5,
		analysis = $6, updated_at = now() WHERE id = $1`,
		cur.ID, mt.Spec, maxTries, mt.AssignedAgent, max(mt.BudgetTokens, int64(0)), analysis)
	return err == nil, err
}
