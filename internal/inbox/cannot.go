package inbox

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/kesokaj/crew/internal/cli"
	"github.com/kesokaj/crew/internal/db"
	"github.com/kesokaj/crew/internal/models"
	"github.com/kesokaj/crew/internal/settings"
)

// agentsWithNoModel are the agents none of whose entries a CLI in use offers now: their runs wait
// until the operator gives them a model, or a CLI offers one again (12.3).
func agentsWithNoModel(ctx context.Context, q db.Querier, machine settings.Doc) ([]Item, error) {
	none, err := models.NoModel(ctx, q, machine)
	if err != nil {
		return nil, err
	}
	var out []Item
	for _, u := range none {
		out = append(out, Item{
			ID: "nomodel/" + u.Role, Kind: KindCrewCannot, Since: u.Since,
			Title: "The " + u.Role + " has no model that can run",
			Body: "No CLI in use offers any of its entries now (" + strings.Join(u.Entries, ", ") + "). Its runs wait until you give it a " +
				"model in the Models screen, or a CLI offers one of them again.",
			Alternatives: []Alternative{{Label: "Give it a model", Link: "/models", Main: true}},
		})
	}
	return out, nil
}

// unbracketedModels are the models a CLI in use offers that appear in no bracket of agents.brackets
// (12.3, 7.8): each is a bracket of its own until the operator places it.
func unbracketedModels(ctx context.Context, q db.Querier, machine settings.Doc) ([]Item, error) {
	list, err := models.Unbracketed(ctx, q, machine)
	if err != nil {
		return nil, err
	}
	var out []Item
	for _, entry := range list {
		c, m, _ := strings.Cut(entry, "/")
		var since time.Time
		if err := q.QueryRow(ctx, `SELECT coalesce(min(first_seen), 'epoch') FROM models WHERE cli = $1 AND model = $2`, c, m).Scan(&since); err != nil {
			return nil, err
		}
		out = append(out, Item{
			ID: "nobracket/" + entry, Kind: KindCrewCannot, Since: since,
			Title:        entry + " is in no bracket",
			Body:         c + " offers " + m + ", which no bracket in agents.brackets names. It is a bracket of its own until you place it.",
			Alternatives: []Alternative{{Label: "Place it in a bracket", Link: "/settings", Main: true}},
		})
	}
	return out, nil
}

// unknownFamilies are the tickets whose stuck line says a check waits on a model of a family Crew
// does not know (12.3, 7.8): read from the stuck line, so the item goes when the stuck line does.
func unknownFamilies(ctx context.Context, q db.Querier) ([]Item, error) {
	rows, err := q.Query(ctx, `SELECT p.name, p.key || '-' || t.number, t.title, t.stuck, t.updated_at
		FROM tickets t JOIN projects p ON p.id = t.project
		WHERE NOT p.hidden AND p.state <> 'archived' AND t.stage NOT IN ('done', 'closed')
		  AND strpos(coalesce(t.stuck, ''), $1) > 0
		ORDER BY t.id`, cli.UnknownFamily)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(r pgx.CollectableRow) (Item, error) {
		var it Item
		var key, title string
		if err := r.Scan(&it.Project, &key, &title, &it.Body, &it.Since); err != nil {
			return it, err
		}
		it.ID, it.Kind = "family/"+it.Project+"/"+key, KindCrewCannot
		it.Title = key + " " + title + " waits on a family Crew does not know"
		it.Alternatives = []Alternative{{Label: "Open", Link: "/p/" + it.Project + "/tickets/" + key, Main: true}}
		return it, nil
	})
}

// waitingWhileBracketHasRoom are the tickets whose stuck line says a run waits at its group's limit
// while a model of its bracket in another group has room (12.3, 12.7, 7.8): read from the stuck line.
func waitingWhileBracketHasRoom(ctx context.Context, q db.Querier) ([]Item, error) {
	rows, err := q.Query(ctx, `SELECT p.name, p.key || '-' || t.number, t.title, t.stuck, t.updated_at
		FROM tickets t JOIN projects p ON p.id = t.project
		WHERE NOT p.hidden AND p.state <> 'archived' AND t.stage NOT IN ('done', 'closed')
		  AND strpos(coalesce(t.stuck, ''), $1) > 0
		ORDER BY t.id`, models.WaitBracketHasRoom)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(r pgx.CollectableRow) (Item, error) {
		var it Item
		var key, title string
		if err := r.Scan(&it.Project, &key, &title, &it.Body, &it.Since); err != nil {
			return it, err
		}
		it.ID, it.Kind = "bracket/"+it.Project+"/"+key, KindCrewCannot
		it.Title = key + " " + title + " waits while its bracket has room"
		it.Alternatives = []Alternative{{Label: "Open", Link: "/p/" + it.Project + "/tickets/" + key, Main: true}}
		return it, nil
	})
}

// checkRuleNotMet are the open tickets whose newest run of a checking role ran on the builder's
// family because no model of another family could run (12.3, 12.7): a clear notice in the inbox as
// well as the check_rule_not_met signal, until a newer run of that role runs on another family or
// the ticket is done or closed.
func checkRuleNotMet(ctx context.Context, q db.Querier) ([]Item, error) {
	rows, err := q.Query(ctx, `SELECT p.name, p.key || '-' || t.number, t.title, coalesce(s.detail->>'role', 'reviewer'),
			coalesce(s.detail->>'family', ''), coalesce(r.model, ''), s.at
		FROM signals s
		JOIN tickets t ON t.id = s.ticket
		JOIN projects p ON p.id = t.project
		JOIN runs r ON r.id = s.run
		JOIN subtasks sub ON sub.id = r.subtask
		WHERE s.kind = 'check_rule_not_met' AND NOT p.hidden AND p.state <> 'archived' AND t.stage NOT IN ('done', 'closed')
		  AND r.id = (SELECT r2.id FROM runs r2 JOIN subtasks sub2 ON sub2.id = r2.subtask
			WHERE sub2.ticket = t.id AND sub2.role = sub.role ORDER BY r2.started_at DESC, r2.number DESC LIMIT 1)
		ORDER BY s.id`)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(r pgx.CollectableRow) (Item, error) {
		var it Item
		var key, title, role, family, model string
		if err := r.Scan(&it.Project, &key, &title, &role, &family, &model, &it.Since); err != nil {
			return it, err
		}
		it.ID, it.Kind = "checkfamily/"+it.Project+"/"+key+"/"+role, KindCrewCannot
		it.Title = fmt.Sprintf("%s %s: the %s ran on the builder's family (%s)", key, title, role, family)
		it.Body = fmt.Sprintf("The %s ran on %s (%s family), the same family as the builder, because no model of another family could run.", role, model, family)
		it.Alternatives = []Alternative{{Label: "Open", Link: "/p/" + it.Project + "/tickets/" + key, Main: true}}
		return it, nil
	})
}

// failedTickets are the open tickets Crew put on hold at limits.max_ticket_failures (7.7, 7.8):
// read from tickets.hold and the newest hold change, so lifting the hold or raising the limit clears
// the item immediately.
func failedTickets(ctx context.Context, q db.Querier) ([]Item, error) {
	rows, err := q.Query(ctx, `SELECT p.name, p.key || '-' || t.number, t.title, coalesce(t.stuck, ''), c.at
		FROM tickets t
		JOIN projects p ON p.id = t.project
		JOIN LATERAL (SELECT who, at, coalesce((after->>'hold')::boolean, false) AS held FROM changes
			WHERE project = p.id AND tbl = 'tickets' AND row_key = p.key || '-' || t.number AND after ? 'hold'
			ORDER BY seq DESC LIMIT 1) c ON c.who <> $1 AND c.held
		WHERE NOT p.hidden AND p.state <> 'archived' AND t.hold AND t.stage NOT IN ('done', 'closed')
		ORDER BY t.id`, Operator)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(r pgx.CollectableRow) (Item, error) {
		var it Item
		var key, title, stuck string
		if err := r.Scan(&it.Project, &key, &title, &stuck, &it.Since); err != nil {
			return it, err
		}
		it.ID, it.Kind = "failed/"+it.Project+"/"+key, KindCrewCannot
		it.Title = key + " " + title + " is held after repeated failures"
		body := strings.TrimSpace(stuck)
		if body == "" {
			body = "Crew put this ticket on hold at limits.max_ticket_failures."
		} else if !strings.HasSuffix(body, ".") {
			body += "."
		}
		it.Body = body + " Lift the hold on the ticket or raise limits.max_ticket_failures in the project's settings to let it go on."
		it.Alternatives = []Alternative{
			{Label: "Open", Link: "/p/" + it.Project + "/tickets/" + key, Main: true},
			{Label: "Raise the limit", Link: "/p/" + it.Project + "/settings"},
		}
		return it, nil
	})
}
