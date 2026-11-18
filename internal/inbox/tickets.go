package inbox

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/kesokaj/crew/internal/db"
)

// newFeatures are the tickets in Analysis that are new features during their notice (7.6): each goes
// ahead by itself when its notice has passed, unless it is held; with approvals.new_feature set to
// operator it has no clock, and waits for Approve now.
func newFeatures(ctx context.Context, q db.Querier) ([]Item, error) {
	rows, err := q.Query(ctx, `SELECT p.name, p.key || '-' || t.number, t.title, t.goal, t.created_by, t.origin, t.hold, t.notice_until,
			coalesce(c.at, t.updated_at), coalesce((SELECT string_agg(p.key || '-' || f.number, ', ') FROM ticket_links l
				JOIN tickets f ON f.id = l.to_ticket WHERE l.from_ticket = t.id AND l.kind = 'created_from'), '')
		FROM tickets t JOIN projects p ON p.id = t.project
		LEFT JOIN LATERAL (SELECT at FROM changes WHERE tbl = 'tickets' AND project = p.id AND row_key = p.key || '-' || t.number
			AND after->'analysis'->>'status' = 'new_feature_notice' ORDER BY seq DESC LIMIT 1) c ON true
		WHERE t.stage = 'analysis' AND t.analysis->>'status' = 'new_feature_notice' AND p.state <> 'archived' ORDER BY t.id`)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(r pgx.CollectableRow) (Item, error) {
		var it Item
		var key, title, goal, by, origin, from string
		var hold bool
		var until *time.Time
		if err := r.Scan(&it.Project, &key, &title, &goal, &by, &origin, &hold, &until, &it.Since, &from); err != nil {
			return it, err
		}
		it.ID, it.Kind = "feature/"+it.Project+"/"+key, KindNewFeature
		it.Title = "New feature: " + key + " " + title
		// created_by is the role and the run, "builder · <run>": the role says enough here.
		role, _, _ := strings.Cut(by, " · ")
		who := "Created by the " + role
		if origin == "operator" {
			who = "Created by you"
		} else if origin != "agent" {
			who = "Created through " + origin + " by " + role
		}
		if from != "" {
			who += " while working on " + from
		}
		it.Body = who + ". Goal: " + strings.TrimSpace(goal)
		if !strings.HasSuffix(it.Body, ".") {
			it.Body += "."
		}
		base := "/api/projects/" + it.Project + "/tasks/" + key + "/decision"
		hl := Alternative{Label: "Hold", Action: base, Body: map[string]any{"choice": "hold"}}
		switch {
		case hold:
			it.Body += " On hold: it does not go ahead by itself."
			hl = Alternative{Label: "Lift the hold", Action: base, Body: map[string]any{"choice": "release"}}
		case until == nil:
			it.Body += " It waits for you: this project's new features do not go ahead by themselves."
		default:
			it.Body += " It goes ahead by itself at " + until.Local().Format("Mon 15:04") + ", unless you discard it or hold it."
		}
		it.Alternatives = []Alternative{
			{Label: "Open", Link: "/p/" + it.Project + "/tickets/" + key},
			{Label: "Approve now", Action: base, Body: map[string]any{"choice": "approve_now"}, Main: true},
			{Label: "Discard", Action: base, Body: map[string]any{"choice": "discard"}},
			hl,
		}
		return it, nil
	})
}

// createdTasks are the tickets the operator did not make that wait for the operator's decision, with
// the analysing agent's proposal and its reason (7.3, since 0.47): Approve makes the ticket ready,
// Discard closes it.
func createdTasks(ctx context.Context, q db.Querier) ([]Item, error) {
	rows, err := q.Query(ctx, `SELECT p.name, p.key || '-' || t.number, t.title, t.goal, t.created_by, t.origin,
			coalesce(t.analysis->'proposal'->>'choice', ''), coalesce(t.analysis->'proposal'->>'reason', ''), coalesce(t.analysis->'proposal'->>'by', ''), t.updated_at
		FROM tickets t JOIN projects p ON p.id = t.project
		WHERE t.stage = 'analysis' AND t.analysis->>'status' = 'waits_for_operator' AND p.state <> 'archived' ORDER BY t.id`)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(r pgx.CollectableRow) (Item, error) {
		var it Item
		var key, title, goal, by, origin, choice, reason, analyst string
		if err := r.Scan(&it.Project, &key, &title, &goal, &by, &origin, &choice, &reason, &analyst, &it.Since); err != nil {
			return it, err
		}
		it.ID, it.Kind = "created/"+it.Project+"/"+key, KindCreatedTask
		it.Title = "Decide on " + key + " " + title
		role, _, _ := strings.Cut(by, " · ")
		who := "Created by the " + role
		if origin != "agent" {
			who = "Created through " + origin + " by " + role
		}
		ana, _, _ := strings.Cut(analyst, " · ")
		it.Body = who + ". Goal: " + strings.TrimSpace(goal)
		if !strings.HasSuffix(it.Body, ".") {
			it.Body += "."
		}
		it.Body += " The " + ana + " who analysed it proposes to " + choice + " it"
		if strings.TrimSpace(reason) != "" {
			it.Body += ": " + strings.TrimSpace(reason)
		}
		it.Body += ". This project's tickets that agents create wait for your decision."
		base := "/api/projects/" + it.Project + "/tasks/" + key + "/decision"
		it.Alternatives = []Alternative{
			{Label: "Open", Link: "/p/" + it.Project + "/tickets/" + key},
			{Label: "Approve", Action: base, Body: map[string]any{"choice": "approve_now"}, Main: choice == "approve"},
			{Label: "Discard", Action: base, Body: map[string]any{"choice": "discard"}, Main: choice != "approve"},
		}
		return it, nil
	})
}

// pullsDiverged are the tickets whose branch someone pushed on GitHub that does not follow Crew's: it
// is not taken, and Crew's pushes of the branch do not go through until the two meet (21).
func pullsDiverged(ctx context.Context, q db.Querier) ([]Item, error) {
	rows, err := q.Query(ctx, `SELECT p.name, p.key || '-' || t.number, t.title, r.branch, r.number, r.diverged, coalesce(r.diverged_why, 'it does not follow Crew''s branch'), r.updated_at
		FROM pull_requests r JOIN tickets t ON t.id = r.ticket JOIN projects p ON p.id = r.project
		WHERE r.diverged IS NOT NULL AND r.state = 'open' AND t.stage NOT IN ('done', 'closed') AND p.state <> 'archived' ORDER BY t.id`)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(r pgx.CollectableRow) (Item, error) {
		var it Item
		var key, title, branch, head, why string
		var number *int
		if err := r.Scan(&it.Project, &key, &title, &branch, &number, &head, &why, &it.Since); err != nil {
			return it, err
		}
		it.ID, it.Kind = "diverged/"+it.Project+"/"+key, KindPullDiverged
		it.Title = "The branch of " + key + " " + title + " moved on GitHub, and Crew does not take it"
		n := ""
		if number != nil {
			n = fmt.Sprintf(" of pull request #%d", *number)
		}
		it.Body = "Someone pushed " + branch + n + " on GitHub to " + head[:min(12, len(head))] + ", and " + why + ". " +
			"Crew does not take it, and keeps its own: its pushes of the branch do not go through until the two meet."
		it.Alternatives = []Alternative{
			{Label: "Open", Link: "/p/" + it.Project + "/tickets/" + key},
			{Label: "Keep Crew's", Action: "/api/projects/" + it.Project + "/tasks/" + key + "/pull/keep", Main: true},
		}
		return it, nil
	})
}

// mayChange are the tickets an agent created, ready in Analysis, that wait for the operator to agree
// that they may change the files that instruct agents their may_change names (13.8).
func mayChange(ctx context.Context, q db.Querier) ([]Item, error) {
	rows, err := q.Query(ctx, `SELECT p.name, p.key || '-' || t.number, t.title, t.created_by, t.updated_at,
			ARRAY(SELECT jsonb_array_elements_text(t.analysis->'mayChange'))
		FROM tickets t JOIN projects p ON p.id = t.project
		WHERE t.stage = 'analysis' AND t.analysis->>'status' = 'ready' AND t.origin NOT IN ('plan', 'operator')
		  AND jsonb_typeof(t.analysis->'mayChange') = 'array' AND jsonb_array_length(t.analysis->'mayChange') > 0
		  AND NOT coalesce((t.analysis->>'mayChangeAgreed')::boolean, false) AND p.state <> 'archived'
		ORDER BY t.id`)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(r pgx.CollectableRow) (Item, error) {
		var it Item
		var key, title, by string
		var paths []string
		if err := r.Scan(&it.Project, &key, &title, &by, &it.Since, &paths); err != nil {
			return it, err
		}
		role, _, _ := strings.Cut(by, " · ")
		it.ID, it.Kind = "maychange/"+it.Project+"/"+key, KindMayChange
		it.Title = key + " " + title + " may change files that instruct agents"
		it.Body = "Created by the " + role + ". Its analysis says it may change " + strings.Join(paths, ", ") +
			". Agents read these files as instructions, so a ticket an agent created changes them only when you agree. " +
			"It waits in Analysis until you answer. Refuse lets it go on without them."
		base := "/api/projects/" + it.Project + "/tasks/" + key + "/may-change"
		it.Alternatives = []Alternative{
			{Label: "Open", Link: "/p/" + it.Project + "/tickets/" + key},
			{Label: "Refuse", Action: base, Body: map[string]any{"choice": "refuse"}},
			{Label: "Agree", Action: base, Body: map[string]any{"choice": "agree"}, Main: true},
		}
		return it, nil
	})
}

// codeChanges are the tickets whose branch, at its head, changes files that are code (13.8): Allow
// merges that head, Refuse sends it back to its builder in place.
func codeChanges(ctx context.Context, q db.Querier) ([]Item, error) {
	rows, err := q.Query(ctx, `SELECT p.name, p.key || '-' || t.number, t.title, t.code_wait->>'head', coalesce(t.code_wait->>'main', ''),
			ARRAY(SELECT jsonb_array_elements_text(t.code_wait->'files')), coalesce(c.at, t.updated_at)
		FROM tickets t JOIN projects p ON p.id = t.project
		LEFT JOIN LATERAL (SELECT at FROM changes WHERE tbl = 'tickets' AND project = p.id AND row_key = p.key || '-' || t.number
			ORDER BY seq DESC LIMIT 1) c ON true
		WHERE t.code_wait IS NOT NULL AND t.stage NOT IN ('done', 'closed') AND p.state <> 'archived' ORDER BY t.id`)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(r pgx.CollectableRow) (Item, error) {
		var it Item
		var key, title, head, main string
		var files []string
		if err := r.Scan(&it.Project, &key, &title, &head, &main, &files, &it.Since); err != nil {
			return it, err
		}
		it.ID, it.Kind = "code/"+it.Project+"/"+key+"/"+head, KindCodeChange
		it.Title = key + " " + title + " changes files that are code"
		changed := strings.Join(files, ", ")
		if len(files) > 8 {
			changed = strings.Join(files[:8], ", ") + ", and " + strconv.Itoa(len(files)-8) + " more"
		}
		it.Body = "Its branch at " + head[:min(7, len(head))] + " changes " + changed + ". These files are code for the agent CLI, or GitHub " +
			"workflows, so the ticket never merges by itself. Read the diff: Allow merges this head; Refuse sends it back to its builder, " +
			"who takes the change out."
		base := "/api/projects/" + it.Project + "/tasks/" + key + "/code"
		diff := "/p/" + it.Project + "/git?to=" + head
		if main != "" {
			diff = "/p/" + it.Project + "/git?from=" + main + "&to=" + head
		}
		it.Alternatives = []Alternative{
			{Label: "See the diff", Link: diff},
			{Label: "Refuse", Action: base, Body: map[string]any{"choice": "refuse", "head": head}},
			{Label: "Allow the merge", Action: base, Body: map[string]any{"choice": "allow", "head": head}, Main: true},
		}
		return it, nil
	})
}
