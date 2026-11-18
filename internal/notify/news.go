package notify

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"strings"
	"time"

	"github.com/kesokaj/crew/internal/updater"
)

// newsBack is how far back the four kinds are read: a row that came late, such as an update's
// request the server copies when it reads the updater, is still found (D25); its key sends it once.
const newsBack = 7 * 24 * time.Hour

// digestLines is how many lines one daily mail lists; the rest are on the Crew level's Activity.
const digestLines = 200

// newsItem is one of the four kinds sent at once (18.1, R175): a project Done, an update or a roll
// back Crew made itself, a high-severity bug, and a hosted project that stopped answering (M19).
type newsItem struct {
	key, project, subject, body string
	links                       []Link
}

// news queues the four kinds and the daily mail (18.1). The sending holds them in quiet hours (D14),
// and the daily mail is due only when they end; with mail off for news they are recorded as dropped,
// so they are not sent when it comes on (D9).
func (n *Notifier) news(ctx context.Context, c config, on bool, now time.Time) error {
	items, err := n.atOnce(ctx, now)
	if err != nil {
		return err
	}
	for _, it := range items {
		if err := n.queueNews(ctx, "news", it, on, now); err != nil {
			return err
		}
	}
	return n.digest(ctx, c, on, now)
}

// atOnce reads the four kinds whose key has no row yet, after notify_state.news_from (D25).
func (n *Notifier) atOnce(ctx context.Context, now time.Time) ([]newsItem, error) {
	base := "https://" + n.Hostname
	var out []newsItem
	// A project Done: the board's change of its state (reconcile.go), not the gate's hidden project.
	rows, err := n.Pool.Query(ctx, `SELECT c.seq, p.id::text, p.name FROM changes c JOIN projects p ON p.id = c.project
		WHERE c.tbl = 'projects' AND c.after->>'state' = 'done' AND NOT p.hidden
		  AND c.at > (SELECT news_from FROM notify_state) AND c.at > $1
		  AND NOT EXISTS (SELECT 1 FROM notifications n WHERE n.kind = 'news' AND n.item = 'done/' || p.id::text || '/' || c.seq::text)
		ORDER BY c.seq`, now.Add(-newsBack))
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var seq int64
		var id, name string
		if err := rows.Scan(&seq, &id, &name); err != nil {
			rows.Close()
			return nil, err
		}
		out = append(out, newsItem{key: fmt.Sprintf("done/%s/%d", id, seq), project: name,
			subject: "Crew · " + name + " · " + name + " is Done",
			body:    name + " is Done: every ticket is Done, cancelled, skipped or discarded. The lead writes its final report.",
			links:   []Link{{Label: "Open the project", URL: base + "/p/" + url.PathEscape(name) + "/board", Main: true}}})
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	// An update or a roll back Crew made itself: an install by the clock that ended, or any request the
	// updater switched back. One the operator pressed is theirs, and Versions shows it (D15).
	rows, err = n.Pool.Query(ctx, `SELECT r.id, r.version, coalesce(r.from_version, ''), r.state FROM update_requests r
		WHERE r.action IN ('install', 'rollback') AND r.ended_at IS NOT NULL
		  AND ((r.asked_by = $2 AND r.state IN ('done', 'failed', 'switched_back', 'stopped')) OR r.state = 'switched_back')
		  AND r.ended_at > (SELECT news_from FROM notify_state) AND r.ended_at > $1
		  AND NOT EXISTS (SELECT 1 FROM notifications n WHERE n.kind = 'news' AND n.item = 'update/' || r.id::text)
		ORDER BY r.id`, now.Add(-newsBack), updater.ByClock)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var id int64
		var version, from, state string
		if err := rows.Scan(&id, &version, &from, &state); err != nil {
			rows.Close()
			return nil, err
		}
		it := newsItem{key: fmt.Sprintf("update/%d", id), links: []Link{{Label: "Versions", URL: base + "/versions", Main: true}}}
		switch state {
		case "done":
			it.subject = "Crew · crew · Crew " + version + " was installed"
			it.body = "Crew " + version + " installed by itself, as a release that changes only prompts and harness text does when its notice has passed."
		case "switched_back":
			it.subject = "Crew · crew · Crew " + version + " was switched back"
			it.body = "The updater switched back from " + version + " because it was not healthy"
			if from != "" {
				it.body += "; " + from + " runs again"
			}
			it.body += "."
		default:
			it.subject = "Crew · crew · Crew " + version + " was not installed"
			it.body = "Its check stopped it, and nothing was replaced. Versions shows why."
		}
		out = append(out, it)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	// A high-severity bug (9.4).
	rows, err = n.Pool.Query(ctx, `SELECT p.id::text, p.name, b.number, b.title, b.found_by FROM bugs b JOIN projects p ON p.id = b.project
		WHERE b.severity = 'high' AND NOT p.hidden AND b.created_at > (SELECT news_from FROM notify_state) AND b.created_at > $1
		  AND NOT EXISTS (SELECT 1 FROM notifications n WHERE n.kind = 'news' AND n.item = 'bug/' || p.id::text || '/' || b.number::text)
		ORDER BY b.id`, now.Add(-newsBack))
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var id, name, title, by string
		var number int
		if err := rows.Scan(&id, &name, &number, &title, &by); err != nil {
			rows.Close()
			return nil, err
		}
		role, _, _ := strings.Cut(by, " · ")
		out = append(out, newsItem{key: fmt.Sprintf("bug/%s/%d", id, number), project: name,
			subject: "Crew · " + name + " · High-severity bug: " + oneLine(title),
			body:    "Found by the " + role + ". No decision is needed: its fix ticket is analysed by another agent.",
			links:   []Link{{Label: "The bugs", URL: base + "/p/" + url.PathEscape(name) + "/knowledge?tab=bugs", Main: true}}})
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	// A hosted project that stopped answering (19.3, plan M19 D11): one mail for each episode, which
	// its time names; Crew restarted it before, and goes on every 10 minutes.
	rows, err = n.Pool.Query(ctx, `SELECT p.id::text, p.name, h.down_since FROM hosting h JOIN projects p ON p.id = h.project
		WHERE h.state = 'failing' AND h.down_since IS NOT NULL AND NOT p.hidden
		  AND h.down_since > (SELECT news_from FROM notify_state) AND h.down_since > $1
		  AND NOT EXISTS (SELECT 1 FROM notifications n WHERE n.kind = 'news'
		    AND n.item = 'host/' || p.id::text || '/down/' || floor(extract(epoch FROM h.down_since))::bigint::text)
		ORDER BY h.down_since`, now.Add(-newsBack))
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var id, name string
		var since time.Time
		if err := rows.Scan(&id, &name, &since); err != nil {
			rows.Close()
			return nil, err
		}
		out = append(out, newsItem{key: fmt.Sprintf("host/%s/down/%d", id, since.Unix()), project: name,
			subject: "Crew · " + name + " · Stopped answering",
			body: name + " stopped answering its health address. Crew restarted it, and it still does not answer; Crew restarts it " +
				"every 10 minutes while it does not. The deploy before can be rolled back to on the Hosting screen.",
			links: []Link{{Label: "Hosting", URL: base + "/p/" + url.PathEscape(name) + "/hosting", Main: true}}})
	}
	rows.Close()
	return out, rows.Err()
}

// queueNews writes a news or a digest row once by its key.
func (n *Notifier) queueNews(ctx context.Context, kind string, it newsItem, on bool, now time.Time) error {
	var b strings.Builder
	b.WriteString(strings.TrimPrefix(it.subject, "Crew · ") + "\n\n" + it.body + "\n")
	if len(it.links) > 0 {
		b.WriteString("\n")
	}
	for _, l := range it.links {
		b.WriteString(l.Label + ": " + l.URL + "\n")
	}
	b.WriteString("\nNo decision is needed. Everything is also on the Crew level's Activity.\n")
	raw, _ := json.Marshal(it.links)
	if it.links == nil {
		raw = []byte("[]")
	}
	state, why := "queued", ""
	if !on {
		state, why = "dropped", "mail was off"
	}
	_, err := n.Pool.Exec(ctx, `INSERT INTO notifications (kind, item, project, subject, body, links, state, error, created_at, next_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, nullif($8, ''), $9, $9) ON CONFLICT (item) WHERE kind IN ('news', 'digest') DO NOTHING`,
		kind, it.key, it.project, it.subject, cut(b.String(), 20000), raw, state, why, now)
	return err
}

// digest is the daily mail (D13): when quiet hours have just ended, Activity's news since the last
// one, up to that end; a day with none sends none. The mark moves either way.
func (n *Notifier) digest(ctx context.Context, c config, on bool, now time.Time) error {
	end := c.Quiet.LastEnd(now, n.Loc)
	var from time.Time
	if err := n.Pool.QueryRow(ctx, `SELECT digest_from FROM notify_state`).Scan(&from); err != nil {
		return err
	}
	if !end.After(from) {
		return nil
	}
	var lines []NewsLine
	if n.News != nil {
		all, err := n.News(ctx, from)
		if err != nil {
			return err
		}
		for _, l := range all {
			if !l.At.After(end) {
				lines = append(lines, l)
			}
		}
	}
	if len(lines) > 0 {
		day := end.In(n.Loc).Format("2006-01-02")
		var b strings.Builder
		shown := lines
		if len(shown) > digestLines {
			shown = shown[:digestLines]
		}
		for _, l := range shown {
			where := l.Project
			if where == "" {
				where = "crew"
			}
			b.WriteString(l.At.In(n.Loc).Format("Jan 2 15:04") + "  " + where + ": " + oneLine(cut(l.Text, 300)) + "\n")
		}
		if more := len(lines) - len(shown); more > 0 {
			fmt.Fprintf(&b, "\nAnd %d more on the Crew level's Activity.\n", more)
		}
		it := newsItem{key: "digest/" + day, subject: fmt.Sprintf("Crew · crew · The news of %s: %d lines", day, len(lines)),
			body:  strings.TrimRight(b.String(), "\n"),
			links: []Link{{Label: "The Crew level", URL: "https://" + n.Hostname + "/crew", Main: true}}}
		if err := n.queueNews(ctx, "digest", it, on, now); err != nil {
			return err
		}
	}
	_, err := n.Pool.Exec(ctx, `UPDATE notify_state SET digest_from = $1`, end)
	return err
}
