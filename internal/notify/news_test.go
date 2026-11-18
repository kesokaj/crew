package notify_test

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/kesokaj/crew/internal/httpapi"
	"github.com/kesokaj/crew/internal/notify"
)

// newsKit is the kit with an account, the news read from the kit's clock on, the daily mail's mark at
// the clock, and a project shop.
func newsKit(t *testing.T) *kit {
	k := newKit(t)
	k.account()
	k.exec(`UPDATE notify_state SET news_from = $1, digest_from = $2`, k.now.Add(-time.Hour), k.now)
	k.exec(`INSERT INTO projects (id, name, key, repo_source) VALUES (gen_random_uuid(), 'shop', 'SF', 'local'), (gen_random_uuid(), 'crew-gate', 'CG', 'local')`)
	k.exec(`UPDATE projects SET hidden = true WHERE name = 'crew-gate'`)
	return k
}

func (k *kit) subjects() []string {
	var out []string
	for _, m := range k.messages() {
		out = append(out, m.Subject)
	}
	return out
}

// The three kinds sent at once (18.1, R175, D15, D25): a project Done, an update by the clock that
// ended, any request the updater switched back, and a high-severity bug, each once; an install the
// operator pressed, a request still running, a medium bug and the gate's hidden project, none; a row
// that came late is still found; inside quiet hours they wait until the hours end (D14).
func TestTheThreeKindsGoAtOnce(t *testing.T) {
	k := newsKit(t)
	at := k.now.Add(-10 * time.Minute)
	k.exec(`INSERT INTO changes (who, tbl, row_key, project, before, after, at) SELECT 'board', 'projects', id::text, id, '{"state":"running"}', '{"state":"done"}', $1 FROM projects WHERE name IN ('shop', 'crew-gate')`, at)
	k.exec(`INSERT INTO update_requests (id, version, from_version, action, asked_by, state, asked_at, ended_at) VALUES
		(1, '0.3.0', '0.2.9', 'install', 'clock', 'done', $1, $1),
		(2, '0.3.1', '0.3.0', 'install', 'clock', 'failed', $1, $1),
		(3, '0.3.2', '0.3.1', 'install', 'operator', 'done', $1, $1),
		(4, '0.3.3', '0.3.2', 'install', 'operator', 'switched_back', $1, $1),
		(5, '0.3.4', '0.3.3', 'install', 'clock', 'running', $1, NULL),
		(6, '0.1.0', NULL, 'install', 'clock', 'done', $1, $2)`, at, k.now.Add(-2*time.Hour))
	k.exec(`INSERT INTO bugs (project, number, title, severity, found_by, created_at) SELECT id, 1, $2, 'high', 'tester · r_1', $1 FROM projects WHERE name = 'shop'`, at, "Total is wrong\r\nwith tax")
	k.exec(`INSERT INTO bugs (project, number, title, severity, found_by, created_at) SELECT id, 2, 'Typo', 'medium', 'tester · r_1', $1 FROM projects WHERE name = 'shop'`, at)
	k.pass()
	want := []string{
		"Crew · shop · shop is Done",
		"Crew · crew · Crew 0.3.0 was installed",
		"Crew · crew · Crew 0.3.1 was not installed",
		"Crew · crew · Crew 0.3.3 was switched back",
		"Crew · shop · High-severity bug: Total is wrong with tax",
	}
	if got := k.subjects(); strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Fatalf("sent:\n%s", strings.Join(got, "\n"))
	}
	if body := k.messages()[3].Text; !strings.Contains(body, "0.3.2 runs again") || !strings.Contains(body, "Versions: https://crew.example/versions") {
		t.Fatalf("the switch back: %s", body)
	}
	// A request copied late, ended before the bug that was already mailed, is still found.
	k.exec(`INSERT INTO update_requests (id, version, from_version, action, asked_by, state, asked_at, ended_at) VALUES (7, '0.3.5', '0.3.4', 'install', 'clock', 'done', $1, $1)`, k.now.Add(-30*time.Minute))
	k.later(time.Minute)
	k.pass()
	k.pass()
	if got := k.subjects(); len(got) != 6 || got[5] != "Crew · crew · Crew 0.3.5 was installed" {
		t.Fatalf("the late one: %v", got)
	}
	// In quiet hours a bug waits until 07:00.
	k.now = time.Date(2026, 10, 20, 23, 0, 0, 0, time.UTC)
	k.exec(`INSERT INTO bugs (project, number, title, severity, found_by, created_at) SELECT id, 3, 'Lost orders', 'high', 'verifier · r_2', $1 FROM projects WHERE name = 'shop'`, k.now)
	k.pass()
	k.now = time.Date(2026, 10, 21, 6, 59, 0, 0, time.UTC)
	k.pass()
	if n := len(k.messages()); n != 6 {
		t.Fatalf("sent in quiet hours: %d", n)
	}
	k.now = time.Date(2026, 10, 21, 7, 0, 0, 0, time.UTC)
	k.pass()
	if got := k.subjects(); len(got) != 7 || got[6] != "Crew · shop · High-severity bug: Lost orders" {
		t.Fatalf("at 07:00: %v", got)
	}
}

// The daily mail (18.1, D13, D27): when quiet hours end, Activity's news since the last one, up to that
// end; none on a day with no news; once a day; more than 200 lines say how many more there are.
func TestTheDailyMail(t *testing.T) {
	k := newsKit(t)
	k.n.News = func(ctx context.Context, after time.Time) ([]notify.NewsLine, error) {
		lines, err := httpapi.ReadNews(ctx, k.pool, after)
		var out []notify.NewsLine
		for _, l := range lines {
			out = append(out, notify.NewsLine{At: l.At, Project: l.Project, Text: l.Text})
		}
		return out, err
	}
	news := func(key, text string, at time.Time) {
		k.exec(`INSERT INTO machine_news (kind, key, text, at) VALUES ('certificate', $1, $2, $3)`, key, text, at)
	}
	news("a", "Before the mark", k.now.Add(-time.Minute))
	news("b", "The certificate runs out in 9 days", k.now.Add(time.Hour))
	news("c", "A restart for security updates waits", k.now.Add(10*time.Hour))
	k.pass()
	if n := len(k.messages()); n != 0 {
		t.Fatalf("a daily mail before quiet hours ended: %d", n)
	}
	k.now = time.Date(2026, 10, 21, 6, 0, 0, 0, time.UTC)
	news("d", "After the end", time.Date(2026, 10, 21, 7, 30, 0, 0, time.UTC))
	k.pass()
	k.now = time.Date(2026, 10, 21, 8, 0, 0, 0, time.UTC)
	k.pass()
	k.pass()
	msgs := k.messages()
	if len(msgs) != 1 || msgs[0].Subject != "Crew · crew · The news of 2026-10-21: 2 lines" {
		t.Fatalf("the daily mail: %v", k.subjects())
	}
	if !strings.Contains(msgs[0].Text, "Oct 20 13:00  crew: The certificate runs out in 9 days") || strings.Contains(msgs[0].Text, "Before the mark") ||
		strings.Contains(msgs[0].Text, "After the end") || !strings.Contains(msgs[0].Text, "The Crew level: https://crew.example/crew") {
		t.Fatalf("its text: %s", msgs[0].Text)
	}
	// The next day holds what came after the end, and a day with none sends none.
	k.now = time.Date(2026, 10, 22, 7, 5, 0, 0, time.UTC)
	k.pass()
	k.now = time.Date(2026, 10, 23, 7, 5, 0, 0, time.UTC)
	k.pass()
	if got := k.subjects(); len(got) != 2 || got[1] != "Crew · crew · The news of 2026-10-22: 1 lines" {
		t.Fatalf("the next days: %v", got)
	}
	// 230 lines: 200 are listed, and it says how many more.
	for i := 0; i < 230; i++ {
		news(fmt.Sprintf("m%d", i), fmt.Sprintf("Line %d", i), time.Date(2026, 10, 23, 12, 0, i, 0, time.UTC))
	}
	k.now = time.Date(2026, 10, 24, 7, 0, 0, 0, time.UTC)
	k.pass()
	msgs = k.messages()
	if len(msgs) != 3 || strings.Count(msgs[2].Text, "Line ") != 200 || !strings.Contains(msgs[2].Text, "And 30 more on the Crew level's Activity.") {
		t.Fatalf("a long day: %d mails, %d lines", len(msgs), strings.Count(msgs[2].Text, "Line "))
	}
}

// News that could not go within a day is dropped, not sent late (D33); with mail off for news it is
// recorded as dropped (D9).
func TestStaleNewsIsDropped(t *testing.T) {
	k := newsKit(t)
	k.exec(`INSERT INTO notifications (kind, item, subject, body, created_at, next_at) VALUES ('news', 'bug/x/1', 's', 'b', $1, $1)`, k.now.Add(-25*time.Hour))
	k.pass()
	if r := k.rows(); len(r) != 1 || r[0].state != "dropped" || r[0].errText != "older than a day; see Activity" || len(k.messages()) != 0 {
		t.Fatalf("%+v", r)
	}
	k.exec(`DELETE FROM credentials`)
	k.exec(`INSERT INTO bugs (project, number, title, severity, found_by, created_at) SELECT id, 1, 'Lost orders', 'high', 'tester', $1 FROM projects WHERE name = 'shop'`, k.now)
	k.pass()
	if r := k.rows(); len(r) != 2 || r[1].state != "dropped" || r[1].errText != "mail was off" {
		t.Fatalf("mail off: %+v", r)
	}
}

// A hosted project that stopped answering is the fourth kind sent at once (18.1, plan M19 D11): one
// mail for each episode, held in quiet hours as the others, linking the Hosting screen; a new episode
// is a new mail. A deploy that went live is a line of the daily mail.
func TestAHostedProjectThatStoppedAnsweringGoesAtOnce(t *testing.T) {
	k := newsKit(t)
	at := k.now.Add(-5 * time.Minute)
	k.exec(`INSERT INTO hosting (project, state, down_since) SELECT id, 'failing', $1 FROM projects WHERE name = 'shop'`, at)
	k.pass()
	k.pass()
	if got := k.subjects(); len(got) != 1 || got[0] != "Crew · shop · Stopped answering" {
		t.Fatalf("sent: %v", got)
	}
	if body := k.messages()[0].Text; !strings.Contains(body, "Hosting: https://crew.example/p/shop/hosting") {
		t.Fatalf("its text: %s", body)
	}
	// It answers again, then stops again: a second episode, a second mail.
	k.exec(`UPDATE hosting SET state = 'live', down_since = NULL`)
	k.pass()
	k.exec(`UPDATE hosting SET state = 'failing', down_since = $1`, k.now.Add(-time.Minute))
	k.pass()
	if n := len(k.messages()); n != 2 {
		t.Fatalf("a second episode: %d mails", n)
	}
	// In quiet hours it waits until they end.
	k.now = time.Date(2026, 10, 20, 23, 0, 0, 0, time.UTC)
	k.exec(`UPDATE hosting SET down_since = $1`, k.now)
	k.pass()
	if n := len(k.messages()); n != 2 {
		t.Fatalf("sent in quiet hours: %d", n)
	}
	k.now = time.Date(2026, 10, 21, 7, 0, 0, 0, time.UTC)
	k.pass()
	if n := len(k.messages()); n != 3 {
		t.Fatalf("at 07:00: %d", n)
	}
	k.exec(`INSERT INTO deploys (project, number, commit, why, asked_by, state, live_at) SELECT id, 4, repeat('a', 40), 'follow_main', 'follow_main', 'live', $1 FROM projects WHERE name = 'shop'`, k.now)
	lines, err := httpapi.ReadNews(context.Background(), k.pool, k.now.Add(-time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, l := range lines {
		found = found || l.Text == "shop is live: deploy 4, commit aaaaaaa, by Follow main"
	}
	if !found {
		t.Fatalf("no line of the deploy: %+v", lines)
	}
}
