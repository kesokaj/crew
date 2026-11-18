package httpapi_test

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/kesokaj/crew/internal/credstore"
	"github.com/kesokaj/crew/internal/db/dbtest"
	"github.com/kesokaj/crew/internal/httpapi"
	"github.com/kesokaj/crew/internal/mail"
	"github.com/kesokaj/crew/internal/mail/mailtest"
	"github.com/kesokaj/crew/internal/notify"
	"github.com/kesokaj/crew/internal/settings"
)

// syncBuffer is a log both the server and the test touch.
type syncBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *syncBuffer) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}

func (s *syncBuffer) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.String()
}

type mailServer struct {
	srv  *httptest.Server
	smtp *mailtest.Server
	log  *syncBuffer
	n    *notify.Notifier
}

// newMailServer is the server with the notifier on a test database, a vault, and a fake SMTP server
// that notifications.mail_smtp names; every log line of the server and the notifier is kept.
func newMailServer(t *testing.T) *mailServer {
	t.Helper()
	d := dbtest.New(t, false)
	vault, err := credstore.NewVault(d.Server, bytes.Repeat([]byte{7}, credstore.KeySize))
	if err != nil {
		t.Fatal(err)
	}
	smtp := mailtest.Start(t)
	buf := &syncBuffer{}
	log := slog.New(slog.NewTextHandler(buf, &slog.HandlerOptions{Level: slog.LevelDebug}))
	m := settings.Machine{Version: "1.0.0", AdminEmail: admin}
	n := &notify.Notifier{Pool: d.Server, Vault: vault, Machine: m, Hostname: host, Loc: time.UTC, Log: log,
		Send: func(ctx context.Context, addr string, acc mail.Account, msg mail.Message) error {
			return mail.Send(ctx, addr, acc, msg, smtp.TLS)
		}}
	s := &httpapi.Server{Machine: m, Hostname: host, AdminEmail: admin, IngressToken: []byte(token), Log: log}
	s.Pool.Store(d.Server)
	s.Notify.Store(n)
	srv := httptest.NewServer(s.Handler())
	t.Cleanup(srv.Close)
	ms := &mailServer{srv: srv, smtp: smtp, log: buf, n: n}
	// The machine's mail goes to the fake server.
	var doc map[string]any
	_, body := do(t, srv, "GET", "/api/settings/machine", signedIn, "")
	json.Unmarshal([]byte(body), &doc)
	doc["notifications"].(map[string]any)["mail_smtp"] = smtp.Addr
	b, _ := json.Marshal(doc)
	if code, body := do(t, srv, "PUT", "/api/settings/machine", change, string(b)); code != 200 {
		t.Fatalf("the settings: %d %s", code, body)
	}
	return ms
}

const appPassword = "abcd efgh ijkl mnop"

// Setup's mail account (18.4, 20.3): stored only behind a fresh sign-in, shown without its password,
// untested until a message went, ok after one, refused after the server refused it, and untested
// again when it is entered again. The test message goes to notifications.mail_to and is not tried
// again; the password is in no answer and no log line.
func TestTheMailAccountAndTheTestMessage(t *testing.T) {
	t.Parallel()
	ms := newMailServer(t)
	get := func() notify.AccountView {
		var v notify.AccountView
		code, body := do(t, ms.srv, "GET", "/api/setup/mail", signedIn, "")
		if code != 200 || strings.Contains(body, appPassword) {
			t.Fatalf("GET: %d %s", code, body)
		}
		json.Unmarshal([]byte(body), &v)
		return v
	}
	if v := get(); v.State != notify.AccountNone || v.Account != nil {
		t.Fatalf("before: %+v", v)
	}
	if code, body := do(t, ms.srv, "POST", "/api/notifications/test", change, ""); code != 409 || !strings.Contains(body, "Setup") {
		t.Fatalf("a test without an account: %d %s", code, body)
	}
	put := `{"account": "crew@example.com", "password": "` + appPassword + `"}`
	if code, body := do(t, ms.srv, "PUT", "/api/setup/mail", signedInAt(stale, ""), put); code != 401 || reason(body) != httpapi.ReasonFresh {
		t.Fatalf("PUT, stale: %d %s", code, body)
	}
	for _, bad := range []string{`{"account": "Crew <crew@example.com>", "password": "x"}`, `{"account": "crew@example.com", "password": ""}`,
		`{"account": "crew@example.com", "password": "a\nb"}`} {
		if code, body := do(t, ms.srv, "PUT", "/api/setup/mail", signedInAt(time.Minute, ""), bad); code != 400 {
			t.Fatalf("PUT %s: %d %s", bad, code, body)
		}
	}
	code, body := do(t, ms.srv, "PUT", "/api/setup/mail", signedInAt(time.Minute, ""), put)
	if code != 200 || strings.Contains(body, appPassword) {
		t.Fatalf("PUT, fresh: %d %s", code, body)
	}
	if v := get(); v.State != notify.AccountUntested || v.Account == nil || *v.Account != "crew@example.com" {
		t.Fatalf("stored: %+v", v)
	}
	var row notify.Row
	code, body = do(t, ms.srv, "POST", "/api/notifications/test", change, "")
	json.Unmarshal([]byte(body), &row)
	if code != 200 || row.State != "sent" || row.Kind != "test" {
		t.Fatalf("the test: %d %s", code, body)
	}
	msgs := ms.smtp.Messages()
	if len(msgs) != 1 || msgs[0].To != admin || msgs[0].User != "crew@example.com" || !strings.Contains(msgs[0].Data, "test message") {
		t.Fatalf("the messages: %+v", msgs)
	}
	if v := get(); v.State != notify.AccountOK {
		t.Fatalf("after a message: %+v", v)
	}
	ms.smtp.Set(false, 535)
	code, body = do(t, ms.srv, "POST", "/api/notifications/test", change, "")
	json.Unmarshal([]byte(body), &row)
	if code != 200 || row.State != "failed" || row.Error == nil || !strings.Contains(*row.Error, "refused") {
		t.Fatalf("a refused test: %d %s", code, body)
	}
	if v := get(); v.State != notify.AccountRefused || !strings.Contains(v.Error, "not accepted") {
		t.Fatalf("refused: %+v", v)
	}
	if code, _ := do(t, ms.srv, "PUT", "/api/setup/mail", signedInAt(time.Minute, ""), put); code != 200 {
		t.Fatal(code)
	}
	if v := get(); v.State != notify.AccountUntested {
		t.Fatalf("entered again: %+v", v)
	}
	var list struct {
		Mail notify.MailView
		Sent []notify.Row
	}
	_, body = do(t, ms.srv, "GET", "/api/notifications", signedIn, "")
	json.Unmarshal([]byte(body), &list)
	if len(list.Sent) != 2 || list.Sent[0].State != "failed" || list.Sent[1].State != "sent" || !list.Mail.On || list.Mail.To != admin {
		t.Fatalf("the list: %s", body)
	}
	if strings.Contains(ms.log.String(), appPassword) {
		t.Fatal("the password is in the log")
	}
	if !strings.Contains(ms.log.String(), "a mail was not sent") {
		t.Fatal("the log is not captured")
	}
}

// With notifications.mail_to empty no test message goes, and the Mail card says why.
func TestNoMailWithoutAnAddress(t *testing.T) {
	t.Parallel()
	ms := newMailServer(t)
	if code, _ := do(t, ms.srv, "PUT", "/api/setup/mail", signedInAt(time.Minute, ""), `{"account": "crew@example.com", "password": "pw"}`); code != 200 {
		t.Fatal(code)
	}
	var doc map[string]any
	_, body := do(t, ms.srv, "GET", "/api/settings/machine", signedIn, "")
	json.Unmarshal([]byte(body), &doc)
	doc["notifications"].(map[string]any)["mail_to"] = nil
	b, _ := json.Marshal(doc)
	if code, body := do(t, ms.srv, "PUT", "/api/settings/machine", signedInAt(time.Minute, ""), string(b)); code != 200 {
		t.Fatalf("%d %s", code, body)
	}
	if code, body := do(t, ms.srv, "POST", "/api/notifications/test", change, ""); code != 409 || !strings.Contains(body, "mail_to") {
		t.Fatalf("%d %s", code, body)
	}
	_, body = do(t, ms.srv, "GET", "/api/notifications", signedIn, "")
	if !strings.Contains(body, `"on":false`) || !strings.Contains(body, "mail_to is empty") {
		t.Fatal(body)
	}
	if len(ms.smtp.Commands()) != 0 {
		t.Fatal("the server was reached")
	}
}
