package notify_test

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/kesokaj/crew/internal/credstore"
	"github.com/kesokaj/crew/internal/db"
	"github.com/kesokaj/crew/internal/db/dbtest"
	"github.com/kesokaj/crew/internal/inbox"
	"github.com/kesokaj/crew/internal/mail"
	"github.com/kesokaj/crew/internal/mail/mailtest"
	"github.com/kesokaj/crew/internal/notify"
	"github.com/kesokaj/crew/internal/settings"
)

const admin = "op@example.com"

// kit is a notifier on a test database with a clock the test moves and a Send that keeps what it
// was given, or fails as the test says.
type kit struct {
	t    *testing.T
	db   *dbtest.DB
	pool *pgxpool.Pool
	n    *notify.Notifier
	now  time.Time

	mu   sync.Mutex
	sent []mail.Message
	fail error
}

func newKit(t *testing.T) *kit {
	t.Helper()
	d := dbtest.New(t, false)
	vault, err := credstore.NewVault(d.Server, bytes.Repeat([]byte{3}, credstore.KeySize))
	if err != nil {
		t.Fatal(err)
	}
	// The models the default roles name, so no agent is without one and the inbox holds only what a
	// test makes.
	if _, err := d.Server.Exec(context.Background(), `INSERT INTO models (cli, name, model) VALUES ('agy', 'claude-opus-5-5', 'claude-opus-5-5'),
		('agy', 'gemini-4-argon', 'gemini-4-argon'), ('agy', 'gemini-3.8-flash', 'gemini-3.8-flash'), ('agy', 'claude-sonnet-5-5', 'claude-sonnet-5-5')`); err != nil {
		t.Fatal(err)
	}
	// Noon UTC, outside the default quiet hours, 22:00 to 07:00.
	k := &kit{t: t, db: d, pool: d.Server, now: time.Date(2026, 10, 20, 12, 0, 0, 0, time.UTC)}
	k.n = &notify.Notifier{Pool: d.Server, Vault: vault, Machine: settings.Machine{Version: "1.0.0", AdminEmail: admin},
		Hostname: "crew.example", Loc: time.UTC, Log: slog.New(slog.NewTextHandler(io.Discard, nil)),
		Now: func() time.Time { return k.now },
		Send: func(_ context.Context, addr string, acc mail.Account, m mail.Message) error {
			k.mu.Lock()
			defer k.mu.Unlock()
			if k.fail != nil {
				return k.fail
			}
			k.sent = append(k.sent, m)
			return nil
		}}
	return k
}

func (k *kit) account() {
	k.t.Helper()
	if _, err := k.n.SetAccount(context.Background(), "crew@example.com", "pw-pw-pw", "test"); err != nil {
		k.t.Fatal(err)
	}
}

func (k *kit) pass() {
	k.t.Helper()
	if err := k.n.Pass(context.Background()); err != nil {
		k.t.Fatal(err)
	}
}

func (k *kit) later(d time.Duration) { k.now = k.now.Add(d) }

func (k *kit) items() []inbox.Item {
	k.t.Helper()
	doc, err := settings.Effective(context.Background(), k.pool, k.n.Machine)
	if err != nil {
		k.t.Fatal(err)
	}
	items, err := inbox.List(context.Background(), k.pool, doc)
	if err != nil {
		k.t.Fatal(err)
	}
	return items
}

func (k *kit) messages() []mail.Message {
	k.mu.Lock()
	defer k.mu.Unlock()
	return append([]mail.Message(nil), k.sent...)
}

func (k *kit) exec(sql string, args ...any) {
	k.t.Helper()
	if _, err := k.pool.Exec(context.Background(), sql, args...); err != nil {
		k.t.Fatal(err)
	}
}

// signedOut makes agy signed out, as the login check writes it: its updated_at moves with each check.
func (k *kit) signedOut() {
	k.exec(`INSERT INTO cli_logins (cli, state, failures, checked_at, result, updated_at) VALUES ('agy', 'signed_out', 2, now(), 'refused: /home/agent/.gemini/token', now())
		ON CONFLICT (cli) DO UPDATE SET state = 'signed_out', result = excluded.result, updated_at = now()`)
}

type row struct {
	kind, item, state string
	ended             bool
	attempts          int
	nextAt            time.Time
	errText           string
}

func (k *kit) rows() []row {
	k.t.Helper()
	rs, err := k.pool.Query(context.Background(), `SELECT kind, coalesce(item, ''), state, ended_at IS NOT NULL, attempts, next_at, coalesce(error, '') FROM notifications ORDER BY id`)
	if err != nil {
		k.t.Fatal(err)
	}
	defer rs.Close()
	var out []row
	for rs.Next() {
		var r row
		if err := rs.Scan(&r.kind, &r.item, &r.state, &r.ended, &r.attempts, &r.nextAt, &r.errText); err != nil {
			k.t.Fatal(err)
		}
		out = append(out, r)
	}
	return out
}

// One mail for each episode of an item, also when its Since moves with every login check (D17); one
// reminder after notifications.remind_after_hours and no second; a new episode when it comes back after
// it went; the mail says no path the login check printed (R175, 18.3).
func TestOneMailForEachEpisodeAndOneReminder(t *testing.T) {
	k := newKit(t)
	k.account()
	k.signedOut()
	k.pass()
	msgs := k.messages()
	if len(msgs) != 1 || msgs[0].To != admin || !strings.Contains(msgs[0].Subject, "agy is signed out") {
		t.Fatalf("the first mail: %+v", msgs)
	}
	if strings.Contains(msgs[0].Text, "/home/agent") || !strings.Contains(msgs[0].Text, "Sign in again: https://crew.example/setup") ||
		!strings.Contains(msgs[0].Text, "Check now: https://crew.example/inbox?choose=Check+now&item=signin%2Fagy") {
		t.Fatalf("its text: %s", msgs[0].Text)
	}
	for i := 0; i < 3; i++ {
		k.later(time.Hour)
		k.signedOut() // the check runs again, and Since moves
		k.pass()
	}
	if n := len(k.messages()); n != 1 {
		t.Fatalf("after 3 h of checks: %d mails", n)
	}
	k.later(time.Hour)
	k.pass()
	msgs = k.messages()
	if len(msgs) != 2 || !strings.HasPrefix(msgs[1].Subject, "Still waiting: Crew · crew · agy is signed out") {
		t.Fatalf("the reminder: %+v", msgs)
	}
	k.later(8 * time.Hour)
	k.pass()
	if n := len(k.messages()); n != 2 {
		t.Fatalf("a second reminder: %d mails", n)
	}
	k.exec(`UPDATE cli_logins SET state = 'signed_in'`)
	k.pass()
	if r := k.rows(); len(r) != 2 || !r[0].ended {
		t.Fatalf("after the sign-in: %+v", r)
	}
	k.signedOut()
	k.pass()
	if n := len(k.messages()); n != 3 {
		t.Fatalf("a new episode: %d mails", n)
	}
}

// An item that goes before its mail could go drops the mail; with mail off an episode is recorded as
// dropped, and is not sent when mail comes on (D9).
func TestWhatIsNotSentIsDropped(t *testing.T) {
	k := newKit(t)
	k.signedOut()
	k.pass()
	if r := k.rows(); len(r) != 1 || r[0].state != "dropped" || r[0].errText != "mail was off" {
		t.Fatalf("no account: %+v", r)
	}
	k.account()
	k.pass()
	if n := len(k.messages()); n != 0 {
		t.Fatalf("mailed when mail came on: %d", n)
	}
	k.exec(`UPDATE cli_logins SET state = 'signed_in'`)
	k.pass()
	k.exec(`UPDATE crew_state SET disk_full = 'The disk is 96% full', disk_at = now() WHERE id = 1`)
	k.fail = fmt.Errorf("reaching the mail server: connection refused")
	k.pass()
	k.exec(`UPDATE crew_state SET disk_full = NULL, disk_at = NULL WHERE id = 1`)
	k.pass()
	r := k.rows()
	if len(r) != 2 || r[1].item != "disk" || r[1].state != "dropped" || !r[1].ended || r[1].errText != "it no longer waited for you" {
		t.Fatalf("gone before it went: %+v", r)
	}
}

// A send that fails is tried again after 1, 5 and 15 minutes, then every hour; from the third try the
// inbox says mail is not getting through, and that item is never mailed (D8, 18.2).
func TestRetriesAndTheInboxsWord(t *testing.T) {
	k := newKit(t)
	k.account()
	k.fail = fmt.Errorf("the mail server refused the message: 554 no")
	k.signedOut()
	start := k.now
	waits := []time.Duration{time.Minute, 5 * time.Minute, 15 * time.Minute, time.Hour, time.Hour}
	for i, w := range waits {
		k.pass()
		r := k.rows()[0]
		if r.attempts != i+1 || r.state != "queued" || !r.nextAt.Equal(k.now.Add(w)) || !strings.Contains(r.errText, "554") {
			t.Fatalf("try %d at %s: %+v", i+1, k.now.Sub(start), r)
		}
		k.later(w - time.Second)
		k.pass()
		if k.rows()[0].attempts != i+1 {
			t.Fatalf("tried again before %s", w)
		}
		k.later(time.Second)
	}
	items := k.items()
	var failing *inbox.Item
	for i := range items {
		if items[i].Kind == inbox.KindMailFailing {
			failing = &items[i]
		}
	}
	if failing == nil || !strings.Contains(failing.Body, "554") {
		t.Fatalf("the inbox: %+v", items)
	}
	for _, r := range k.rows() {
		if r.item == "mail/failing" {
			t.Fatalf("the mail's own trouble was queued: %+v", r)
		}
	}
	k.fail = nil
	k.pass()
	if r := k.rows()[0]; r.state != "sent" || len(k.messages()) != 1 {
		t.Fatalf("at last: %+v", r)
	}
	for _, it := range k.items() {
		if it.Kind == inbox.KindMailFailing {
			t.Fatal("the inbox still says mail is not getting through")
		}
	}
}

// A refused login stops all sending until the account is entered again, and the inbox says so (62 of
// the recovery matrix); the messages that waited go then.
func TestARefusedLoginStopsSending(t *testing.T) {
	k := newKit(t)
	k.account()
	k.fail = fmt.Errorf("%w: 535 5.7.8 Username and Password not accepted", mail.ErrLoginRefused)
	k.signedOut()
	k.exec(`UPDATE crew_state SET disk_full = 'The disk is 96% full', disk_at = now() WHERE id = 1`)
	calls := 0
	send := k.n.Send
	k.n.Send = func(ctx context.Context, addr string, acc mail.Account, m mail.Message) error {
		calls++
		return send(ctx, addr, acc, m)
	}
	k.pass()
	if calls != 1 {
		t.Fatalf("%d sends after a refusal", calls)
	}
	r := k.rows()
	if r[0].state != "queued" || r[1].state != "queued" || r[1].attempts != 0 {
		t.Fatalf("after the refusal: %+v", r)
	}
	items := k.items()
	found := false
	for _, it := range items {
		found = found || (it.Kind == inbox.KindMailRefused && strings.Contains(it.Body, "crew@example.com") && strings.Contains(it.Body, "not accepted"))
	}
	if !found {
		t.Fatalf("the inbox: %+v", items)
	}
	k.later(2 * time.Hour)
	k.pass()
	if calls != 1 {
		t.Fatalf("sent while refused: %d", calls)
	}
	k.fail = nil
	k.account()
	k.pass()
	if calls != 3 || len(k.messages()) != 2 {
		t.Fatalf("after the account again: %d sends, %d mails", calls, len(k.messages()))
	}
	items = k.items()
	for _, it := range items {
		if it.Kind == inbox.KindMailRefused {
			t.Fatal("the refusal stays in the inbox")
		}
	}
}

// Two notifiers on one database, as an old and a new server for a moment during an update, send a
// message once; a send cut off by a stopped server is taken again after 5 minutes (D24).
func TestOneSendOfEachMessage(t *testing.T) {
	k := newKit(t)
	k.account()
	other := *k.n
	slow := func(ctx context.Context, addr string, acc mail.Account, m mail.Message) error {
		time.Sleep(50 * time.Millisecond)
		return k.n.Send(ctx, addr, acc, m)
	}
	other.Send = slow
	k.signedOut()
	var wg sync.WaitGroup
	for _, n := range []*notify.Notifier{k.n, &other} {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := n.Pass(context.Background()); err != nil {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	if n := len(k.messages()); n != 1 {
		t.Fatalf("%d mails from two notifiers", n)
	}
	k.exec(`UPDATE crew_state SET disk_full = 'full', disk_at = now() WHERE id = 1`)
	k.exec(`INSERT INTO notifications (kind, item, subject, body, state, next_at) VALUES ('needs_you', 'disk', 's', 'b', 'sending', $1)`, k.now.Add(-4*time.Minute))
	k.pass()
	if n := len(k.messages()); n != 1 {
		t.Fatalf("a row sending 4 minutes was taken: %d", n)
	}
	k.later(2 * time.Minute)
	k.pass()
	if n := len(k.messages()); n != 2 {
		t.Fatalf("a row sending 6 minutes was not taken: %d", n)
	}
}

// A pass whose inbox cannot be read ends no episode.
func TestAnUnreadableInboxEndsNothing(t *testing.T) {
	k := newKit(t)
	k.account()
	k.signedOut()
	k.pass()
	owner := k.db.ConnectAs(t, db.RoleMigrate)
	if _, err := owner.Exec(context.Background(), `ALTER TABLE cli_logins RENAME TO cli_logins_away`); err != nil {
		t.Fatal(err)
	}
	k.pass()
	if _, err := owner.Exec(context.Background(), `ALTER TABLE cli_logins_away RENAME TO cli_logins`); err != nil {
		t.Fatal(err)
	}
	if r := k.rows(); len(r) != 1 || r[0].ended {
		t.Fatalf("%+v", r)
	}
}

// E2E 1 and 5 in Go (D30): the pass with the real sender and a fake SMTP server; one mail, its
// reminder, nothing after the item went; a refused login stops it until the account is entered again.
func TestThePassThroughARealSender(t *testing.T) {
	k := newKit(t)
	s := mailtest.Start(t)
	k.n.Send = func(ctx context.Context, addr string, acc mail.Account, m mail.Message) error {
		return mail.Send(ctx, addr, acc, m, s.TLS)
	}
	k.exec(`INSERT INTO settings (project, doc, updated_by) VALUES (NULL, jsonb_build_object('notifications', jsonb_build_object('mail_smtp', $1::text)), 'test')
		ON CONFLICT ((coalesce(project::text, 'machine'))) DO UPDATE SET doc = settings.doc || excluded.doc`, s.Addr)
	k.account()
	k.signedOut()
	k.pass()
	k.later(4 * time.Hour)
	k.pass()
	k.exec(`UPDATE cli_logins SET state = 'signed_in'`)
	k.later(4 * time.Hour)
	k.pass()
	msgs := s.Messages()
	if len(msgs) != 2 || msgs[0].To != admin || msgs[0].User != "crew@example.com" || !strings.Contains(msgs[1].Data, "Subject: =?utf-8?q?Still_waiting:") {
		t.Fatalf("the server got: %+v", msgs)
	}
	s.Set(false, 535)
	k.signedOut()
	k.later(time.Hour)
	k.pass()
	k.later(time.Hour)
	k.pass()
	if n := strings.Count(strings.Join(s.Commands(), " "), "AUTH"); n != 3 {
		t.Fatalf("%d logins: a refused one was tried again", n)
	}
	s.Set(false, 0)
	k.account()
	k.pass()
	if len(s.Messages()) != 3 {
		t.Fatalf("after the account again: %d", len(s.Messages()))
	}
}

// A send claimed by another notifier, its row locked, is skipped, not waited for (D24; review of M18,
// F4): the pass goes on and sends it once the lock is gone.
func TestAClaimedRowIsSkipped(t *testing.T) {
	k := newKit(t)
	k.account()
	ctx := context.Background()
	k.exec(`UPDATE crew_state SET disk_full = 'full', disk_at = now() WHERE id = 1`)
	k.exec(`INSERT INTO notifications (kind, item, subject, body, next_at) VALUES ('needs_you', 'disk', 's', 'b', $1)`, k.now)
	tx, err := k.pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(ctx, `SELECT id FROM notifications WHERE item = 'disk' FOR UPDATE`); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- k.n.Pass(ctx) }()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		tx.Rollback(ctx)
		<-done
		t.Fatal("a pass waited on a row another notifier holds")
	}
	if n := len(k.messages()); n != 0 {
		t.Fatalf("a held row was sent: %d", n)
	}
	tx.Rollback(ctx)
	k.pass()
	if n := len(k.messages()); n != 1 {
		t.Fatalf("after the lock: %d", n)
	}
}

// News that cannot be read holds back no mail of what needs you (review of M18, F1).
func TestUnreadableNewsHoldsBackNoItem(t *testing.T) {
	k := newKit(t)
	k.account()
	k.exec(`UPDATE notify_state SET digest_from = $1`, k.now.Add(-30*time.Hour))
	k.n.News = func(context.Context, time.Time) ([]notify.NewsLine, error) {
		return nil, fmt.Errorf("activity: no answer")
	}
	k.signedOut()
	k.pass()
	if n := len(k.messages()); n != 1 {
		t.Fatalf("%d mails", n)
	}
}

// A refused login holds what comes meanwhile; it is sent once the account is entered again (recovery
// row 62; review of M18, F2). With no account at all it is dropped (D9).
func TestARefusalHoldsWhatComesMeanwhile(t *testing.T) {
	k := newKit(t)
	k.account()
	k.exec(`UPDATE notify_state SET login_refused_at = $1, login_error = '535'`, k.now)
	k.signedOut()
	k.pass()
	if r := k.rows(); len(r) != 1 || r[0].state != "queued" || len(k.messages()) != 0 {
		t.Fatalf("while refused: %+v", r)
	}
	k.account()
	k.pass()
	if n := len(k.messages()); n != 1 {
		t.Fatalf("after the account again: %d", n)
	}
}

// What came of a test message is recorded also when the page that asked went away (review of M18, F6).
func TestATestMessageIsRecordedWhenThePageGoes(t *testing.T) {
	k := newKit(t)
	k.account()
	ctx, cancel := context.WithCancel(context.Background())
	send := k.n.Send
	k.n.Send = func(c context.Context, addr string, acc mail.Account, m mail.Message) error {
		err := send(c, addr, acc, m)
		cancel()
		return err
	}
	row, err := k.n.SendTest(ctx)
	if err != nil || row.State != "sent" {
		t.Fatalf("%+v %v", row, err)
	}
	if r := k.rows(); len(r) != 1 || r[0].state != "sent" {
		t.Fatalf("%+v", r)
	}
}
