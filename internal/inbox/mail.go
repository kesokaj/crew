package inbox

import (
	"context"
	"errors"
	"strconv"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/kesokaj/crew/internal/db"
)

// The mail's own trouble (18.2, 21): a refused login, and mail that does not get through. They are
// shown here and never mailed, as a mail would not get through either.
const (
	KindMailRefused = "mail_refused"
	KindMailFailing = "mail_failing"
)

// failingAfter is how many attempts a message has had before the inbox says mail does not get through.
const failingAfter = 3

func mailTrouble(ctx context.Context, q db.Querier) ([]Item, error) {
	var out []Item
	var refusedAt *time.Time
	var why, account *string
	err := q.QueryRow(ctx, `SELECT s.login_refused_at, s.login_error, c.account FROM notify_state s
		LEFT JOIN credentials c ON c.id = 'mail'
		WHERE s.login_refused_at IS NOT NULL AND (s.login_ok_at IS NULL OR s.login_refused_at > s.login_ok_at)`).Scan(&refusedAt, &why, &account)
	switch {
	case err == nil:
		body := "The mail server refused the account"
		if account != nil {
			body += " " + *account
		}
		if why != nil {
			body += ": " + *why
		}
		out = append(out, Item{ID: "mail/refused", Kind: KindMailRefused, Title: "Crew's mail login was refused", Since: *refusedAt,
			Body:         body + ". Nothing is sent until you enter the account again in Setup. The inbox has everything meanwhile.",
			Alternatives: []Alternative{{Label: "Open Setup", Link: "/setup", Main: true}}})
	case !errors.Is(err, pgx.ErrNoRows):
		return nil, err
	}
	var n int
	var since *time.Time
	var last *string
	if err := q.QueryRow(ctx, `SELECT count(*), min(created_at), (SELECT error FROM notifications WHERE state = 'queued' AND kind <> 'test' AND attempts >= $1 ORDER BY id DESC LIMIT 1)
		FROM notifications WHERE state = 'queued' AND kind <> 'test' AND attempts >= $1`, failingAfter).Scan(&n, &since, &last); err != nil {
		return nil, err
	}
	if n > 0 {
		body := plural(n, "message has", "messages have") + " not gone after " + strconv.Itoa(failingAfter) + " tries"
		if last != nil {
			body += ": " + *last
		}
		out = append(out, Item{ID: "mail/failing", Kind: KindMailFailing, Title: "Mail is not getting through", Since: *since,
			Body:         body + ". Crew tries again every hour; the inbox has everything meanwhile.",
			Alternatives: []Alternative{{Label: "Open notifications", Link: "/notifications", Main: true}}})
	}
	return out, nil
}
