package notify

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"net/url"
	"slices"
	"strings"
	"time"

	"github.com/kesokaj/crew/internal/inbox"
	"github.com/kesokaj/crew/internal/mail"
	"github.com/kesokaj/crew/internal/settings"
)

// Run makes a pass at once and then every interval, until ctx ends. A check server starts none (16.4).
func (n *Notifier) Run(ctx context.Context, every time.Duration) {
	t := time.NewTicker(every)
	defer t.Stop()
	for {
		if err := n.Pass(ctx); err != nil && ctx.Err() == nil {
			n.Log.Error("notifications: a pass failed", "error", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

// claimed is how long a row may stay sending before it is taken again: a server that stopped in the
// middle of a send. That one message may then come twice, never not at all (D24).
const claimed = 5 * time.Minute

// sendAtMost is how many messages one pass sends; the rest go on the next.
const sendAtMost = 20

// Pass is one round (18.1): the episodes of the inbox's items, their reminders, the news, and the
// sending of what waits.
func (n *Notifier) Pass(ctx context.Context) error {
	c, err := n.config(ctx)
	if err != nil {
		return err
	}
	acc, err := n.login(ctx)
	hasAccount := err == nil
	if err != nil && !errors.Is(err, ErrNoAccount) {
		return err
	}
	var refused bool
	if err := n.Pool.QueryRow(ctx, `SELECT login_refused_at IS NOT NULL AND (login_ok_at IS NULL OR login_refused_at > login_ok_at) FROM notify_state`).Scan(&refused); err != nil {
		return err
	}
	// A refused login holds what is queued until the account is entered again; with no account or no
	// address mail is off, and what happens meanwhile is not sent later (D9, D37).
	mayQueue := hasAccount && c.To != ""
	canSend := mayQueue && !refused
	now := n.now()
	doc, err := settings.Effective(ctx, n.Pool, n.Machine)
	if err != nil {
		return err
	}
	items, err := inbox.List(ctx, n.Pool, doc)
	if err != nil {
		// Nothing is ended or opened on a list that could not be read.
		n.Log.Error("notifications: the inbox could not be read", "error", err)
	} else {
		if err := n.episodes(ctx, items, c.ItemsOn && mayQueue, now); err != nil {
			return err
		}
		if err := n.reminders(ctx, items, c, c.ItemsOn && mayQueue, now); err != nil {
			return err
		}
	}
	// News that cannot be read holds back no mail of what needs you (review of M18, F1).
	if err := n.news(ctx, c, c.NewsOn && mayQueue, now); err != nil {
		n.Log.Error("notifications: the news could not be read", "error", err)
	}
	if !canSend {
		return nil
	}
	return n.sendWaiting(ctx, c, acc, now)
}

// notMailed are the inbox's kinds about the mail itself: a mail would not get through either.
var notMailed = []string{inbox.KindMailRefused, inbox.KindMailFailing}

// episodes opens an episode for each listed item that has none, and ends each open one whose item is
// no longer listed (D17). An item whose Since moves is the same episode while its id is listed.
func (n *Notifier) episodes(ctx context.Context, items []inbox.Item, on bool, now time.Time) error {
	listed := map[string]bool{}
	for _, it := range items {
		if !slices.Contains(notMailed, it.Kind) {
			listed[it.ID] = true
		}
	}
	rows, err := n.Pool.Query(ctx, `SELECT id, item FROM notifications WHERE kind = 'needs_you' AND ended_at IS NULL`)
	if err != nil {
		return err
	}
	open := map[string]int64{}
	for rows.Next() {
		var id int64
		var item string
		if err := rows.Scan(&id, &item); err != nil {
			rows.Close()
			return err
		}
		open[item] = id
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}
	for item, id := range open {
		if listed[item] {
			continue
		}
		// Its own row and its reminder, if they had not gone yet, go no more.
		if _, err := n.Pool.Exec(ctx, `UPDATE notifications SET ended_at = $2,
				state = CASE WHEN state IN ('queued', 'sending') THEN 'dropped' ELSE state END,
				error = CASE WHEN state IN ('queued', 'sending') THEN 'it no longer waited for you' ELSE error END
			WHERE id = $1`, id, now); err != nil {
			return err
		}
		if _, err := n.Pool.Exec(ctx, `UPDATE notifications SET state = 'dropped', error = 'it no longer waited for you'
			WHERE episode = $1 AND state IN ('queued', 'sending')`, id); err != nil {
			return err
		}
	}
	for _, it := range items {
		if !listed[it.ID] {
			continue
		}
		if _, ok := open[it.ID]; ok {
			continue
		}
		if err := n.queue(ctx, "needs_you", it, nil, on, now); err != nil {
			return err
		}
	}
	return nil
}

// reminders queue one more mail for each episode whose first went remind_after_hours ago and still
// waits, once (R175). Its text is the item's as it is now.
func (n *Notifier) reminders(ctx context.Context, items []inbox.Item, c config, on bool, now time.Time) error {
	byID := map[string]inbox.Item{}
	for _, it := range items {
		byID[it.ID] = it
	}
	rows, err := n.Pool.Query(ctx, `SELECT e.id, e.item FROM notifications e
		WHERE e.kind = 'needs_you' AND e.ended_at IS NULL AND e.state = 'sent' AND e.sent_at <= $1
		  AND NOT EXISTS (SELECT 1 FROM notifications r WHERE r.kind = 'reminder' AND r.episode = e.id)`, now.Add(-c.RemindAfter))
	if err != nil {
		return err
	}
	type due struct {
		id   int64
		item string
	}
	var list []due
	for rows.Next() {
		var d due
		if err := rows.Scan(&d.id, &d.item); err != nil {
			rows.Close()
			return err
		}
		list = append(list, d)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}
	for _, d := range list {
		it, ok := byID[d.item]
		if !ok {
			continue
		}
		id := d.id
		if err := n.queue(ctx, "reminder", it, &id, on, now); err != nil {
			return err
		}
	}
	return nil
}

// queue writes the mail of an item: queued when mail is on, else dropped, so it is not sent when mail
// comes on again (D9). A second writer of the same episode or reminder changes nothing.
func (n *Notifier) queue(ctx context.Context, kind string, it inbox.Item, episode *int64, on bool, now time.Time) error {
	subject, body, links := n.itemMail(it, kind == "reminder")
	raw, _ := json.Marshal(links)
	state, why := "queued", ""
	if !on {
		state, why = "dropped", "mail was off"
	}
	conflict := `ON CONFLICT (item) WHERE kind = 'needs_you' AND ended_at IS NULL DO NOTHING`
	if kind == "reminder" {
		conflict = `ON CONFLICT (episode) WHERE kind = 'reminder' DO NOTHING`
	}
	_, err := n.Pool.Exec(ctx, `INSERT INTO notifications (kind, item, episode, project, subject, body, links, state, error, created_at, next_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, nullif($9, ''), $10, $10) `+conflict,
		kind, it.ID, episode, it.Project, subject, body, raw, state, why, now)
	return err
}

// sendWaiting claims what waits and sends it, oldest first; news waits in quiet hours (D14). A
// refused login stops the round: nothing more is sent until the account is entered again.
func (n *Notifier) sendWaiting(ctx context.Context, c config, acc mail.Account, now time.Time) error {
	// A send that was cut off is taken again; a test message is not: it said so to its press.
	if _, err := n.Pool.Exec(ctx, `UPDATE notifications SET
			state = CASE WHEN kind = 'test' THEN 'failed' ELSE 'queued' END,
			error = CASE WHEN kind = 'test' THEN 'the server stopped while it was sent' ELSE error END
		WHERE state = 'sending' AND next_at < $1`, now.Add(-claimed)); err != nil {
		return err
	}
	// News that could not go within a day is in Activity; it is not sent late (D33).
	if _, err := n.Pool.Exec(ctx, `UPDATE notifications SET state = 'dropped', error = 'older than a day; see Activity'
		WHERE state = 'queued' AND kind IN ('news', 'digest') AND created_at < $1`, now.Add(-24*time.Hour)); err != nil {
		return err
	}
	quiet := c.Quiet.Holds(now, n.Loc)
	rows, err := n.Pool.Query(ctx, `UPDATE notifications SET state = 'sending', attempts = attempts + 1, next_at = $1
		WHERE id IN (SELECT id FROM notifications
			WHERE state = 'queued' AND kind <> 'test' AND next_at <= $1 AND (NOT $2 OR kind NOT IN ('news', 'digest'))
			ORDER BY id LIMIT $3 FOR UPDATE SKIP LOCKED)
		RETURNING id, subject, body, attempts`, now, quiet, sendAtMost)
	if err != nil {
		return err
	}
	type claim struct {
		id            int64
		subject, body string
		attempts      int
	}
	var list []claim
	for rows.Next() {
		var x claim
		if err := rows.Scan(&x.id, &x.subject, &x.body, &x.attempts); err != nil {
			rows.Close()
			return err
		}
		list = append(list, x)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}
	slices.SortFunc(list, func(a, b claim) int { return cmp.Compare(a.id, b.id) })
	for i, x := range list {
		err := n.Send(ctx, c.SMTP, acc, mail.Message{To: c.To, FromName: c.FromName, Subject: x.subject, Text: x.body})
		if err == nil {
			if err := n.record(ctx, x.id, nil, ""); err != nil {
				return err
			}
			continue
		}
		if err := n.record(ctx, x.id, err, "queued"); err != nil {
			return err
		}
		if _, err := n.Pool.Exec(ctx, `UPDATE notifications SET next_at = $2 WHERE id = $1`, x.id, now.Add(backoff(x.attempts))); err != nil {
			return err
		}
		if errors.Is(err, mail.ErrLoginRefused) {
			// The rest of the claim goes back as it was: they were not tried.
			for _, y := range list[i+1:] {
				if _, err := n.Pool.Exec(ctx, `UPDATE notifications SET state = 'queued', attempts = attempts - 1 WHERE id = $1`, y.id); err != nil {
					return err
				}
			}
			return nil
		}
	}
	return nil
}

// backoff is the wait after the attempts-th failure: 1, 5, 15 minutes, then every hour (D8).
func backoff(attempts int) time.Duration {
	switch attempts {
	case 1:
		return time.Minute
	case 2:
		return 5 * time.Minute
	case 3:
		return 15 * time.Minute
	}
	return time.Hour
}

// lines are what a mail says of an item (D26): the body for the kinds whose body is Crew's own
// sentences with an operator's or an analysis's words, the first sentence of a release, and a fixed
// sentence for every other kind, so no file name, path, commit, tool output or error is mailed. The
// body is one line, so words an agent wrote cannot make a line of their own that reads as one of
// Crew's links (rule 14; security review of M18, S1).
func lines(it inbox.Item) string {
	switch it.Kind {
	case inbox.KindProjectOK, inbox.KindProjectPaused, inbox.KindDiskFull, inbox.KindNewFeature, inbox.KindCreatedTask:
		return oneLine(sentences(it.Body, 400))
	case inbox.KindRelease:
		return oneLine(firstSentence(it.Body))
	case inbox.KindMachineBoot:
		return "Two microVMs in a row did not boot, so new runs wait: Kata or KVM on the machine needs you."
	case inbox.KindDBWritable:
		return "The database can be written again after a time it could not be. Crew shows for how long."
	case inbox.KindCLISignedOut:
		return "Runs on this CLI wait until you sign in to it again in Setup."
	case inbox.KindCrewCannot:
		return "An agent has no model its CLI offers, so its runs wait."
	case inbox.KindTerminalMain:
		return "The project terminal pushed a main that waits for you. Crew shows what it changes."
	case inbox.KindPullDiverged:
		return "Its branch went another way on GitHub than Crew's. Crew shows what you can do."
	case inbox.KindMayChange:
		return "An agent created this ticket, and its analysis says it may change files that instruct agents. It waits for you to agree."
	case inbox.KindCodeChange:
		return "Its branch changes files that are code for the agent CLI, or GitHub workflows. It never merges by itself: Crew shows the change."
	case inbox.KindRemoteMain:
		return "Commits came in on the remote from outside Crew that change files that are code. Crew shows them."
	case inbox.KindReleaseFailed:
		return "The updater did not install a release, and nothing was replaced. Crew shows why."
	case inbox.KindOverride:
		return "A proposed change of how this project's work is checked waits for you."
	}
	return "Open it in Crew to see why it waits."
}

// sentences keeps whole sentences of s up to max bytes, or cuts it with an ellipsis.
func sentences(s string, max int) string {
	s = strings.TrimSpace(s)
	if len(s) <= max {
		return s
	}
	if i := strings.LastIndex(s[:max], ". "); i > 0 {
		return s[:i+1]
	}
	return strings.TrimSpace(cut(s, max-3)) + "…"
}

func firstSentence(s string) string {
	if i := strings.Index(s, ". "); i >= 0 {
		return s[:i+1]
	}
	return s
}

// itemMail is an item's mail (18.3): the title, the project, the item's lines, and one line for each
// alternative. An action opens the inbox with it chosen; a page opens that page. Nothing decides.
func (n *Notifier) itemMail(it inbox.Item, reminder bool) (string, string, []Link) {
	about := it.Project
	if about == "" {
		about = "crew"
	}
	title := oneLine(it.Title)
	subject := "Crew · " + about + " · " + title
	if reminder {
		subject = "Still waiting: " + subject
	}
	base := "https://" + n.Hostname
	var links []Link
	for _, a := range it.Alternatives {
		l := Link{Label: a.Label, Main: a.Main}
		switch {
		case a.Action != "":
			l.URL = base + "/inbox?" + url.Values{"item": {it.ID}, "choose": {a.Label}}.Encode()
		case a.Link != "":
			l.URL = base + a.Link
		default:
			continue
		}
		links = append(links, l)
	}
	var b strings.Builder
	b.WriteString(title + "\n")
	b.WriteString("Crew · " + about + "\n\n")
	b.WriteString(lines(it) + "\n")
	// Crew's links, each a line of its own under their heading: a link an agent wrote stays inside the
	// one line of the item's text above, never a line that reads as one of these.
	if len(links) > 0 {
		b.WriteString("\nChoose in Crew:\n")
	}
	for _, l := range links {
		b.WriteString("  " + l.Label + ": " + l.URL + "\n")
	}
	b.WriteString("\nA link alone never decides: Crew opens on the item with the choice ready, and Confirm decides. Everything is also in the inbox.\n")
	return subject, b.String(), links
}

// oneLine takes CR and LF out of a title an agent wrote.
func oneLine(s string) string {
	return strings.Join(strings.Fields(s), " ")
}
