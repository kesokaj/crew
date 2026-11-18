// Package mail sends Crew's notifications as plain-text mail over SMTP with an account (design 18.2,
// 18.4): the operator's Google account with an app password, or a mail service on their domain. The
// password goes over the wire only after STARTTLS, and only to the configured server.
package mail

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/tls"
	"encoding/hex"
	"errors"
	"fmt"
	"mime"
	"mime/quotedprintable"
	"net"
	"net/mail"
	"net/smtp"
	"net/textproto"
	"strings"
	"time"
)

// Account is the login a message is sent with; User is the address it comes from.
type Account struct{ User, Password string }

// Message is one mail: to one address, from FromName at the account's address.
type Message struct{ To, FromName, Subject, Text string }

var (
	// ErrLoginRefused is a 534 or 535 answer to AUTH: the account or its app password is wrong, or
	// Google removed the password with a change of the account's password (18.2).
	ErrLoginRefused = errors.New("the mail login was refused")
	// ErrNoTLS is a server that offers no STARTTLS: the password is never sent in the clear.
	ErrNoTLS = errors.New("the mail server offers no STARTTLS")
)

// timeout bounds a dial and the whole conversation with the server.
const timeout = 30 * time.Second

// subjectMax is how many characters of a subject are kept.
const subjectMax = 120

// Send dials addr (host:port), requires STARTTLS, authenticates with PLAIN, and sends m from
// "FromName" <acc.User> as text/plain. A 534 or 535 answer to AUTH is ErrLoginRefused with the
// server's words. conf is nil in production: the system's roots, and the server name from addr; a test
// passes its own roots.
func Send(ctx context.Context, addr string, acc Account, m Message, conf *tls.Config) error {
	host, _, err := net.SplitHostPort(addr)
	if err != nil || host == "" {
		return fmt.Errorf("the mail server %q is not host:port", addr)
	}
	from, err := Bare(acc.User)
	if err != nil {
		return fmt.Errorf("the account: %w", err)
	}
	to, err := Bare(m.To)
	if err != nil {
		return fmt.Errorf("the address to send to: %w", err)
	}
	data, err := compose(from, to, m)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	conn, err := (&net.Dialer{}).DialContext(ctx, "tcp", addr)
	if err != nil {
		return fmt.Errorf("reaching the mail server: %w", err)
	}
	deadline, _ := ctx.Deadline()
	conn.SetDeadline(deadline)
	// A cancel closes the connection, which ends any read the client waits on.
	stop := context.AfterFunc(ctx, func() { conn.Close() })
	defer stop()
	c, err := smtp.NewClient(conn, host)
	if err != nil {
		conn.Close()
		return fmt.Errorf("the mail server's greeting: %w", err)
	}
	defer c.Close()
	if err := c.Hello("localhost"); err != nil {
		return fmt.Errorf("the mail server's EHLO: %w", err)
	}
	if ok, _ := c.Extension("STARTTLS"); !ok {
		return ErrNoTLS
	}
	tc := &tls.Config{ServerName: host, MinVersion: tls.VersionTLS12}
	if conf != nil {
		tc = conf.Clone()
		if tc.ServerName == "" {
			tc.ServerName = host
		}
	}
	if err := c.StartTLS(tc); err != nil {
		return fmt.Errorf("the mail server's STARTTLS: %w", err)
	}
	if err := c.Auth(smtp.PlainAuth("", acc.User, acc.Password, host)); err != nil {
		var te *textproto.Error
		if errors.As(err, &te) && (te.Code == 534 || te.Code == 535) {
			return fmt.Errorf("%w: %d %s", ErrLoginRefused, te.Code, te.Msg)
		}
		return fmt.Errorf("the mail login: %w", err)
	}
	if err := c.Mail(from); err != nil {
		return fmt.Errorf("the mail server took no sender: %w", err)
	}
	if err := c.Rcpt(to); err != nil {
		return fmt.Errorf("the mail server took no recipient: %w", err)
	}
	w, err := c.Data()
	if err != nil {
		return fmt.Errorf("the mail server's DATA: %w", err)
	}
	if _, err := w.Write(data); err != nil {
		return fmt.Errorf("sending the message: %w", err)
	}
	if err := w.Close(); err != nil {
		return fmt.Errorf("the mail server refused the message: %w", err)
	}
	// The message was taken; a QUIT that fails changes nothing.
	c.Quit()
	return nil
}

// Bare is one bare address, as net/mail reads it: a name or a second address is refused.
func Bare(s string) (string, error) {
	a, err := mail.ParseAddress(s)
	if err != nil {
		return "", fmt.Errorf("%q is not a mail address", s)
	}
	if a.Name != "" || a.Address != strings.TrimSpace(s) {
		return "", fmt.Errorf("%q is not one bare mail address", s)
	}
	return a.Address, nil
}

// oneLine takes CR and LF out of a header's value, so a title an agent wrote adds no header.
func oneLine(s string) string {
	return strings.Join(strings.Fields(strings.NewReplacer("\r", " ", "\n", " ").Replace(s)), " ")
}

// compose writes the message: Crew's own headers, then the text, quoted-printable.
func compose(from, to string, m Message) ([]byte, error) {
	subject := []rune(oneLine(m.Subject))
	if len(subject) > subjectMax {
		subject = append(subject[:subjectMax-1], '…')
	}
	id := make([]byte, 12)
	if _, err := rand.Read(id); err != nil {
		return nil, err
	}
	_, domain, _ := strings.Cut(from, "@")
	var b bytes.Buffer
	header := func(k, v string) { fmt.Fprintf(&b, "%s: %s\r\n", k, v) }
	header("From", (&mail.Address{Name: oneLine(m.FromName), Address: from}).String())
	header("To", (&mail.Address{Address: to}).String())
	header("Subject", mime.QEncoding.Encode("utf-8", string(subject)))
	header("Date", time.Now().Format(time.RFC1123Z))
	header("Message-ID", "<"+hex.EncodeToString(id)+"@"+domain+">")
	header("MIME-Version", "1.0")
	header("Content-Type", "text/plain; charset=utf-8")
	header("Content-Transfer-Encoding", "quoted-printable")
	b.WriteString("\r\n")
	qp := quotedprintable.NewWriter(&b)
	if _, err := qp.Write([]byte(strings.ReplaceAll(m.Text, "\r\n", "\n"))); err != nil {
		return nil, err
	}
	if err := qp.Close(); err != nil {
		return nil, err
	}
	return b.Bytes(), nil
}
