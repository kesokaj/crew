package mail_test

import (
	"context"
	"errors"
	"mime"
	"net/mail"
	"slices"
	"strings"
	"testing"

	crewmail "github.com/kesokaj/crew/internal/mail"
	"github.com/kesokaj/crew/internal/mail/mailtest"
)

var acc = crewmail.Account{User: "crew@example.com", Password: "app-password-1234"}

// A message goes after STARTTLS and AUTH PLAIN, from "Crew" at the account, to one address, with a
// Q-encoded subject and a quoted-printable text part (18.2, 18.4).
func TestAMessageGoesAfterSTARTTLS(t *testing.T) {
	s := mailtest.Start(t)
	err := crewmail.Send(context.Background(), s.Addr, acc, crewmail.Message{
		To: "op@example.com", FromName: "Crew", Subject: "Crew · shop · OK för shop", Text: "Give the OK: https://crew.example/inbox?item=ok%2Fshop&choose=Give+the+OK\n\nA link alone never decides.",
	}, s.TLS)
	if err != nil {
		t.Fatal(err)
	}
	msgs := s.Messages()
	if len(msgs) != 1 {
		t.Fatalf("%d messages", len(msgs))
	}
	m := msgs[0]
	if m.User != acc.User || m.From != acc.User || m.To != "op@example.com" {
		t.Fatalf("user %q from %q to %q", m.User, m.From, m.To)
	}
	cmds := s.Commands()
	if i, j := slices.Index(cmds, "STARTTLS"), slices.Index(cmds, "AUTH"); i < 0 || j < i {
		t.Fatalf("AUTH before STARTTLS: %v", cmds)
	}
	parsed, err := mail.ReadMessage(strings.NewReader(m.Data))
	if err != nil {
		t.Fatal(err)
	}
	if from := parsed.Header.Get("From"); from != `"Crew" <crew@example.com>` {
		t.Fatalf("From %q", from)
	}
	subject, err := new(mime.WordDecoder).DecodeHeader(parsed.Header.Get("Subject"))
	if err != nil || subject != "Crew · shop · OK för shop" {
		t.Fatalf("Subject %q %v", subject, err)
	}
	if ct := parsed.Header.Get("Content-Type"); ct != "text/plain; charset=utf-8" {
		t.Fatalf("Content-Type %q", ct)
	}
	if !strings.Contains(m.Data, "item=3Dok%2Fshop") {
		t.Fatalf("the link is not in the quoted-printable text: %q", m.Data)
	}
}

// A server that offers no STARTTLS never sees AUTH: the password is not sent in the clear (D2).
func TestNoSTARTTLSNoPassword(t *testing.T) {
	s := mailtest.Start(t)
	s.Set(true, 0)
	err := crewmail.Send(context.Background(), s.Addr, acc, crewmail.Message{To: "op@example.com", FromName: "Crew", Subject: "s", Text: "t"}, s.TLS)
	if !errors.Is(err, crewmail.ErrNoTLS) {
		t.Fatalf("got %v", err)
	}
	if slices.Contains(s.Commands(), "AUTH") || len(s.Messages()) != 0 {
		t.Fatalf("commands %v, messages %d", s.Commands(), len(s.Messages()))
	}
}

// 534 and 535 are a refused login, with the server's words; nothing is sent.
func TestARefusedLogin(t *testing.T) {
	for _, code := range []int{534, 535} {
		s := mailtest.Start(t)
		s.Set(false, code)
		err := crewmail.Send(context.Background(), s.Addr, acc, crewmail.Message{To: "op@example.com", FromName: "Crew", Subject: "s", Text: "t"}, s.TLS)
		if !errors.Is(err, crewmail.ErrLoginRefused) || !strings.Contains(err.Error(), "not accepted") {
			t.Fatalf("%d: got %v", code, err)
		}
		if len(s.Messages()) != 0 {
			t.Fatalf("%d: a message was sent", code)
		}
	}
}

// A subject an agent wrote cannot add a header, and reads as one line: CR and LF become spaces, so a
// Bcc line stays text of the subject (safety rule 14).
func TestASubjectAddsNoHeader(t *testing.T) {
	s := mailtest.Start(t)
	err := crewmail.Send(context.Background(), s.Addr, acc, crewmail.Message{
		To: "op@example.com", FromName: "Crew\r\nBcc: x@evil.example", Subject: "Fix it\r\nBcc: x@evil.example\r\n\r\nbody", Text: "t",
	}, s.TLS)
	if err != nil {
		t.Fatal(err)
	}
	parsed, err := mail.ReadMessage(strings.NewReader(s.Messages()[0].Data))
	if err != nil {
		t.Fatal(err)
	}
	if bcc := parsed.Header["Bcc"]; len(bcc) != 0 {
		t.Fatalf("a Bcc header: %v", bcc)
	}
	if n := len(parsed.Header["Subject"]); n != 1 {
		t.Fatalf("%d Subject headers", n)
	}
	// It reads as one line: the breaks are spaces, not encoded control characters.
	subject, err := new(mime.WordDecoder).DecodeHeader(parsed.Header.Get("Subject"))
	if err != nil || subject != "Fix it Bcc: x@evil.example body" {
		t.Fatalf("Subject %q %v", subject, err)
	}
}

// A recipient or an account that is not one bare address is refused before anything is dialled.
func TestOnlyOneBareAddress(t *testing.T) {
	s := mailtest.Start(t)
	for _, to := range []string{"a@example.com, b@example.com", "Eve <e@example.com>", "nobody", "a@example.com\r\nRCPT TO:<b@example.com>"} {
		err := crewmail.Send(context.Background(), s.Addr, acc, crewmail.Message{To: to, Subject: "s", Text: "t"}, s.TLS)
		if err == nil {
			t.Fatalf("%q was taken", to)
		}
	}
	if len(s.Commands()) != 0 {
		t.Fatalf("the server was reached: %v", s.Commands())
	}
}
