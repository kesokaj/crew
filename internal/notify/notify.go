// Package notify sends Crew's mail (design 18): one mail for each episode of an inbox item that needs
// the operator and one reminder, the news of the day in one mail, four kinds of news at once, and a
// test message. Every message is a row of notifications, written before it is sent and kept as it
// looked; the inbox stays the full list, and nothing depends on a mail having arrived (R145).
package notify

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/kesokaj/crew/internal/credstore"
	"github.com/kesokaj/crew/internal/mail"
	"github.com/kesokaj/crew/internal/settings"
)

// NewsLine is one line of Activity's news (20.1), for the daily mail.
type NewsLine struct {
	At      time.Time
	Project string
	Text    string
}

// Notifier sends the mail. Its zero Vault means the key file is missing: there is no account.
type Notifier struct {
	Pool     *pgxpool.Pool
	Vault    *credstore.Vault
	Machine  settings.Machine
	Hostname string // CREW_HOSTNAME: links are https://<it>/…
	Loc      *time.Location
	Now      func() time.Time
	Send     func(ctx context.Context, addr string, acc mail.Account, m mail.Message) error
	News     func(ctx context.Context, after time.Time) ([]NewsLine, error)
	Log      *slog.Logger
}

// Production is mail.Send with the system's roots.
func Production(ctx context.Context, addr string, acc mail.Account, m mail.Message) error {
	return mail.Send(ctx, addr, acc, m, nil)
}

func (n *Notifier) now() time.Time {
	if n.Now != nil {
		return n.Now()
	}
	return time.Now()
}

// config is what the notifier reads of the machine's settings on each use.
type config struct {
	To          string // notifications.mail_to; empty: no mail
	FromName    string
	SMTP        string
	ItemsOn     bool // notifications.needs_you holds mail
	NewsOn      bool // notifications.news is digest
	RemindAfter time.Duration
	Quiet       settings.Hours
}

func (n *Notifier) config(ctx context.Context) (config, error) {
	doc, err := settings.Effective(ctx, n.Pool, n.Machine)
	if err != nil {
		return config{}, err
	}
	sec, _ := doc["notifications"].(map[string]any)
	str := func(k string) string { v, _ := sec[k].(string); return strings.TrimSpace(v) }
	c := config{To: str("mail_to"), FromName: str("mail_from_name"), SMTP: str("mail_smtp"), NewsOn: str("news") == "digest"}
	if c.FromName == "" {
		c.FromName = "Crew"
	}
	if list, ok := sec["needs_you"].([]any); ok {
		for _, v := range list {
			if v == "mail" {
				c.ItemsOn = true
			}
		}
	}
	hours := 4.0
	switch v := sec["remind_after_hours"].(type) {
	case float64:
		hours = v
	case int:
		hours = float64(v)
	case int64:
		hours = float64(v)
	}
	c.RemindAfter = time.Duration(hours * float64(time.Hour))
	if c.Quiet, err = settings.ParseHours(str("quiet_hours")); err != nil {
		return config{}, fmt.Errorf("notifications.quiet_hours: %w", err)
	}
	return c, nil
}

// cut keeps at most n bytes of s, whole runes.
func cut(s string, n int) string {
	if len(s) <= n {
		return s
	}
	s = s[:n]
	for !utf8.ValidString(s) {
		s = s[:len(s)-1]
	}
	return s
}

// The states of the mail account (D21).
const (
	AccountNone     = "none"     // no credential, or it does not open with the key file
	AccountUntested = "untested" // stored, nothing sent with it yet
	AccountOK       = "ok"       // the server took a message with it, and refused none since
	AccountRefused  = "refused"  // the server refused the login: nothing is sent until it is entered again
)

// AccountView is Setup's Mail row. It never holds the password.
type AccountView struct {
	Account   *string    `json:"account"`
	State     string     `json:"state"`
	OKAt      *time.Time `json:"okAt,omitempty"`
	RefusedAt *time.Time `json:"refusedAt,omitempty"`
	Error     string     `json:"error,omitempty"`
}

// credential is what the vault keeps under credstore.Mail.
type credential struct {
	Account  string `json:"account"`
	Password string `json:"password"`
}

// login reads the account from the vault: ErrNoAccount when none is stored or it does not open.
func (n *Notifier) login(ctx context.Context) (mail.Account, error) {
	if n.Vault == nil {
		return mail.Account{}, ErrNoAccount
	}
	c, err := n.Vault.Get(ctx, credstore.Mail)
	if errors.Is(err, credstore.ErrNotFound) || errors.Is(err, credstore.ErrUnreadable) {
		return mail.Account{}, ErrNoAccount
	}
	if err != nil {
		return mail.Account{}, err
	}
	var cr credential
	if err := json.Unmarshal(c.Data, &cr); err != nil || cr.Account == "" || cr.Password == "" {
		return mail.Account{}, ErrNoAccount
	}
	return mail.Account{User: cr.Account, Password: cr.Password}, nil
}

// ErrNoAccount is a mail account that is not set up in Setup, or that the key file does not open.
var ErrNoAccount = errors.New("no mail account is set up in Setup")

// Account is Setup's Mail row.
func (n *Notifier) Account(ctx context.Context) (AccountView, error) {
	var v AccountView
	acc, err := n.login(ctx)
	switch {
	case errors.Is(err, ErrNoAccount):
		v.State = AccountNone
		if n.Vault == nil {
			v.Error = "the key file is missing, so no credential opens"
		} else if _, err := n.Vault.Get(ctx, credstore.Mail); errors.Is(err, credstore.ErrUnreadable) {
			v.Error = "the stored account does not open with the key file: enter it again"
		}
		return v, nil
	case err != nil:
		return v, err
	}
	v.Account = &acc.User
	var errText *string
	if err := n.Pool.QueryRow(ctx, `SELECT login_ok_at, login_refused_at, login_error FROM notify_state`).Scan(&v.OKAt, &v.RefusedAt, &errText); err != nil {
		return v, err
	}
	switch {
	case v.RefusedAt != nil && (v.OKAt == nil || v.RefusedAt.After(*v.OKAt)):
		v.State = AccountRefused
		if errText != nil {
			v.Error = *errText
		}
	case v.OKAt != nil:
		v.State = AccountOK
	default:
		v.State = AccountUntested
	}
	return v, nil
}

// SetAccount stores the account and its app password, and makes it untested: a refusal or a success
// of the account before says nothing of this one.
func (n *Notifier) SetAccount(ctx context.Context, account, password, who string) (AccountView, error) {
	if n.Vault == nil {
		return AccountView{}, errors.New("the key file is missing: no credential can be stored")
	}
	addr, err := mail.Bare(account)
	if err != nil {
		return AccountView{}, &InputError{err.Error()}
	}
	if password == "" || len(password) > 200 || strings.ContainsAny(password, "\r\n\x00") {
		return AccountView{}, &InputError{"the app password is 1 to 200 characters on one line"}
	}
	data, _ := json.Marshal(credential{Account: addr, Password: password})
	if err := n.Vault.Put(ctx, credstore.Credential{ID: credstore.Mail, Kind: "value", Account: addr, Data: data}, who); err != nil {
		return AccountView{}, err
	}
	if _, err := n.Pool.Exec(ctx, `UPDATE notify_state SET login_ok_at = NULL, login_refused_at = NULL, login_error = NULL`); err != nil {
		return AccountView{}, err
	}
	return n.Account(ctx)
}

// InputError is a value the operator sent that cannot be taken.
type InputError struct{ Msg string }

func (e *InputError) Error() string { return e.Msg }

// Link is one alternative of a message, as a link.
type Link struct {
	Label string `json:"label"`
	URL   string `json:"url"`
	Main  bool   `json:"main,omitempty"`
}

// Row is one message of notifications.
type Row struct {
	ID        int64      `json:"id"`
	Kind      string     `json:"kind"`
	Item      *string    `json:"item"`
	Project   string     `json:"project"`
	Subject   string     `json:"subject"`
	Body      string     `json:"body"`
	Links     []Link     `json:"links"`
	State     string     `json:"state"`
	Error     *string    `json:"error"`
	CreatedAt time.Time  `json:"createdAt"`
	SentAt    *time.Time `json:"sentAt"`
	Open      bool       `json:"open"`
}

// rowColumns are the columns scan reads, with open: a needs_you whose episode has not ended, or a
// reminder of one.
const rowColumns = `n.id, n.kind, n.item, n.project, n.subject, n.body, n.links, n.state, n.error, n.created_at, n.sent_at,
	(n.kind = 'needs_you' AND n.ended_at IS NULL) OR (n.kind = 'reminder' AND EXISTS (SELECT 1 FROM notifications e WHERE e.id = n.episode AND e.ended_at IS NULL))`

func scan(r pgx.Row) (Row, error) {
	var x Row
	var links []byte
	err := r.Scan(&x.ID, &x.Kind, &x.Item, &x.Project, &x.Subject, &x.Body, &links, &x.State, &x.Error, &x.CreatedAt, &x.SentAt, &x.Open)
	if err == nil {
		err = json.Unmarshal(links, &x.Links)
	}
	if x.Links == nil {
		x.Links = []Link{}
	}
	return x, err
}

// MailView is the Notifications screen's Mail card.
type MailView struct {
	On       bool    `json:"on"`
	Why      string  `json:"why,omitempty"` // why it is off
	To       string  `json:"to"`
	FromName string  `json:"fromName"`
	Account  *string `json:"account"`
	State    string  `json:"state"`
	ItemsOn  bool    `json:"itemsOn"`
	NewsOn   bool    `json:"newsOn"`
	Quiet    string  `json:"quietHours"`
	Remind   float64 `json:"remindAfterHours"`
}

// Sent is what was sent, newest first, at most 100, with the Mail card.
func (n *Notifier) Sent(ctx context.Context) (MailView, []Row, error) {
	c, err := n.config(ctx)
	if err != nil {
		return MailView{}, nil, err
	}
	acc, err := n.Account(ctx)
	if err != nil {
		return MailView{}, nil, err
	}
	v := MailView{To: c.To, FromName: c.FromName, Account: acc.Account, State: acc.State, ItemsOn: c.ItemsOn, NewsOn: c.NewsOn,
		Quiet: formatHours(c.Quiet), Remind: c.RemindAfter.Hours()}
	v.Why = offBecause(c, acc.State)
	v.On = v.Why == ""
	rows, err := n.Pool.Query(ctx, `SELECT `+rowColumns+` FROM notifications n ORDER BY n.id DESC LIMIT 100`)
	if err != nil {
		return v, nil, err
	}
	defer rows.Close()
	out := []Row{}
	for rows.Next() {
		x, err := scan(rows)
		if err != nil {
			return v, nil, err
		}
		out = append(out, x)
	}
	return v, out, rows.Err()
}

// offBecause says why no mail goes, or "" when some does.
func offBecause(c config, state string) string {
	switch {
	case c.To == "":
		return "notifications.mail_to is empty"
	case state == AccountNone:
		return "no mail account is set up in Setup"
	case state == AccountRefused:
		return "the mail login was refused: enter the account again in Setup"
	case !c.ItemsOn && !c.NewsOn:
		return "notifications.needs_you has no mail, and notifications.news is not digest"
	}
	return ""
}

func formatHours(h settings.Hours) string {
	f := func(d time.Duration) string { return fmt.Sprintf("%02d:%02d", int(d.Hours()), int(d.Minutes())%60) }
	return f(h.From) + "-" + f(h.To)
}

// ErrNoMail is a test message that cannot be sent: what is missing is in its text.
type ErrNoMail struct{ Why string }

func (e *ErrNoMail) Error() string { return e.Why }

// SendTest sends one test message now to notifications.mail_to and waits for it (D22): sent, or
// failed with the error; it is not tried again. A refused account may be tried, to see it work again.
func (n *Notifier) SendTest(ctx context.Context) (Row, error) {
	c, err := n.config(ctx)
	if err != nil {
		return Row{}, err
	}
	if c.To == "" {
		return Row{}, &ErrNoMail{"notifications.mail_to is empty"}
	}
	acc, err := n.login(ctx)
	if errors.Is(err, ErrNoAccount) {
		return Row{}, &ErrNoMail{ErrNoAccount.Error()}
	}
	if err != nil {
		return Row{}, err
	}
	subject := "Crew · crew · A test message"
	body := "This is a test message from Crew at " + n.Hostname + ".\n\n" +
		"What needs you comes like this, with its alternatives as links. A link alone never decides: Crew opens on the item with the choice ready, and Confirm decides. Everything is also in the inbox.\n"
	var id int64
	if err := n.Pool.QueryRow(ctx, `INSERT INTO notifications (kind, subject, body, state, attempts) VALUES ('test', $1, $2, 'sending', 1) RETURNING id`,
		subject, body).Scan(&id); err != nil {
		return Row{}, err
	}
	sendErr := n.Send(ctx, c.SMTP, acc, mail.Message{To: c.To, FromName: c.FromName, Subject: subject, Text: body})
	// What came of it is recorded also when the page that asked went away meanwhile (review of M18, F6).
	ctx = context.WithoutCancel(ctx)
	if err := n.record(ctx, id, sendErr, "failed"); err != nil {
		return Row{}, err
	}
	return scan(n.Pool.QueryRow(ctx, `SELECT `+rowColumns+` FROM notifications n WHERE n.id = $1`, id))
}

// record writes the outcome of one send: sent, with the account's success; or the error, with the
// state the row takes then, and a refused login on notify_state.
func (n *Notifier) record(ctx context.Context, id int64, sendErr error, failedState string) error {
	now := n.now()
	if sendErr == nil {
		_, err := n.Pool.Exec(ctx, `WITH s AS (UPDATE notify_state SET login_ok_at = $2)
			UPDATE notifications SET state = 'sent', sent_at = $2, error = NULL WHERE id = $1`, id, now)
		return err
	}
	text := cut(sendErr.Error(), 400)
	n.Log.Warn("a mail was not sent", "notification", id, "error", text)
	if errors.Is(sendErr, mail.ErrLoginRefused) {
		if _, err := n.Pool.Exec(ctx, `UPDATE notify_state SET login_refused_at = $1, login_error = $2`, now, text); err != nil {
			return err
		}
	}
	_, err := n.Pool.Exec(ctx, `UPDATE notifications SET state = $2, error = $3 WHERE id = $1`, id, failedState, text)
	return err
}

// Sent is when a message of an item's open episode went (inbox.png: "Notified by mail at 09:01").
type Sent struct {
	Kind   string    `json:"kind"`
	SentAt time.Time `json:"sentAt"`
}

// Notified are the messages sent of each open episode, by the item's id, oldest first.
func Notified(ctx context.Context, q interface {
	Query(context.Context, string, ...any) (pgx.Rows, error)
}) (map[string][]Sent, error) {
	rows, err := q.Query(ctx, `SELECT n.item, n.kind, n.sent_at FROM notifications n
		WHERE n.state = 'sent' AND n.item IS NOT NULL AND (
			(n.kind = 'needs_you' AND n.ended_at IS NULL)
			OR (n.kind = 'reminder' AND EXISTS (SELECT 1 FROM notifications e WHERE e.id = n.episode AND e.ended_at IS NULL)))
		ORDER BY n.sent_at`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string][]Sent{}
	for rows.Next() {
		var item string
		var s Sent
		if err := rows.Scan(&item, &s.Kind, &s.SentAt); err != nil {
			return nil, err
		}
		out[item] = append(out[item], s)
	}
	return out, rows.Err()
}
