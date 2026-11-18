package inbox

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/kesokaj/crew/internal/db"
)

// featureSpecs lists Tier-2 feature specs escalated for @user confirmation because they alter
// 000-foundation.md scope, touch 13.8 protected files, or run under approvals.feature_specs: manual.
func featureSpecs(ctx context.Context, q db.Querier) ([]Item, error) {
	rows, err := q.Query(ctx, `SELECT p.name, s.spec_key, s.title, s.escalation_reason, s.thread_ref, s.updated_at,
			coalesce((SELECT d.choice FROM spec_decisions d
				WHERE d.spec_id = s.id AND d.state = 'pending_operator'
				ORDER BY d.id DESC LIMIT 1), '')
		FROM specs s
		JOIN projects p ON p.id = s.project_id
		WHERE s.tier = 'feature' AND s.state = 'escalated' AND p.state <> 'archived'
		ORDER BY s.updated_at ASC, s.id ASC`)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(r pgx.CollectableRow) (Item, error) {
		var proj, key, title, reason, thread, choice string
		var since time.Time
		if err := r.Scan(&proj, &key, &title, &reason, &thread, &since, &choice); err != nil {
			return Item{}, err
		}
		body := strings.TrimSpace(reason)
		if body == "" {
			body = "Feature spec requires operator confirmation before entering Backlog."
		}
		if choice != "" {
			body += " Proposed decision: " + choice + "."
		}
		if thread != "" {
			body += " Thread: " + thread + "."
		}
		action := fmt.Sprintf("/api/projects/%s/specs/%s/decision", proj, key)
		return Item{
			ID:      fmt.Sprintf("spec/%s/%s", proj, key),
			Kind:    KindFeatureSpec,
			Project: proj,
			Title:   fmt.Sprintf("Feature spec %s: %s", key, title),
			Body:    body,
			Since:   since,
			Alternatives: []Alternative{
				{Label: "Approve", Action: action, Body: map[string]any{"choice": "approve"}, Main: true},
				{Label: "Discard", Action: action, Body: map[string]any{"choice": "discard"}},
			},
		}, nil
	})
}
