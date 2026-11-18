// Package mailtest is a fake SMTP server for tests: STARTTLS with a certificate made for the test,
// AUTH PLAIN, and every message kept as it arrived (design 23: "a fake mail server").
package mailtest

import (
	"bufio"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/base64"
	"math/big"
	"net"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

// Message is one message the server took.
type Message struct {
	From, To string
	User     string // the account it authenticated with
	Data     string // headers and body, as sent, with the dot-stuffing undone
}

// Server is a running fake.
type Server struct {
	Addr string      // 127.0.0.1:port
	TLS  *tls.Config // what a client needs to trust it

	// noTLS makes the server offer no STARTTLS; refuse, when not 0, is its answer to AUTH (534 or 535).
	noTLS  bool
	refuse int

	mu       sync.Mutex
	msgs     []Message
	commands []string
	ln       net.Listener
	srvTLS   *tls.Config
}

// Start starts a server on a free port of 127.0.0.1; it stops when the test ends.
func Start(t testing.TB) *Server {
	t.Helper()
	cert, roots := certificate(t)
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	s := &Server{Addr: ln.Addr().String(), TLS: &tls.Config{RootCAs: roots}, ln: ln,
		srvTLS: &tls.Config{Certificates: []tls.Certificate{cert}}}
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go s.serve(c)
		}
	}()
	t.Cleanup(func() { ln.Close() })
	return s
}

// Messages are the messages taken so far.
func (s *Server) Messages() []Message {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]Message(nil), s.msgs...)
}

// Commands are the command words the server read, in order, AUTH as "AUTH" without its argument.
func (s *Server) Commands() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.commands...)
}

func (s *Server) settings() (noTLS bool, refuse int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.noTLS, s.refuse
}

// Set changes how the server answers from the next connection on.
func (s *Server) Set(noTLS bool, refuse int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.noTLS, s.refuse = noTLS, refuse
}

func (s *Server) serve(c net.Conn) {
	defer func() { c.Close() }()
	c.SetDeadline(time.Now().Add(30 * time.Second))
	noTLS, refuse := s.settings()
	r, w := bufio.NewReader(c), bufio.NewWriter(c)
	say := func(lines ...string) {
		for _, l := range lines {
			w.WriteString(l + "\r\n")
		}
		w.Flush()
	}
	say("220 fake ESMTP")
	secure := false
	var m Message
	for {
		line, err := r.ReadString('\n')
		if err != nil {
			return
		}
		line = strings.TrimRight(line, "\r\n")
		verb, arg, _ := strings.Cut(line, " ")
		verb = strings.ToUpper(verb)
		s.mu.Lock()
		s.commands = append(s.commands, verb)
		s.mu.Unlock()
		switch verb {
		case "EHLO", "HELO":
			if !secure && !noTLS {
				say("250-fake", "250-STARTTLS", "250 AUTH PLAIN")
			} else {
				say("250-fake", "250 AUTH PLAIN")
			}
		case "STARTTLS":
			if noTLS || secure {
				say("502 no")
				continue
			}
			say("220 go ahead")
			tc := tls.Server(c, s.srvTLS)
			if err := tc.Handshake(); err != nil {
				return
			}
			c, secure = tc, true
			r, w = bufio.NewReader(c), bufio.NewWriter(c)
		case "AUTH":
			mech, b64, _ := strings.Cut(arg, " ")
			if strings.ToUpper(mech) != "PLAIN" {
				say("504 only PLAIN")
				continue
			}
			raw, _ := base64.StdEncoding.DecodeString(b64)
			parts := strings.Split(string(raw), "\x00")
			if refuse != 0 {
				say(strconv.Itoa(refuse) + " 5.7.8 Username and Password not accepted")
				continue
			}
			if len(parts) == 3 {
				m.User = parts[1]
			}
			say("235 2.7.0 Accepted")
		case "MAIL":
			_, v, _ := strings.Cut(arg, ":")
			m.From = strings.Trim(v, "<> ")
			say("250 ok")
		case "RCPT":
			_, v, _ := strings.Cut(arg, ":")
			m.To = strings.Trim(v, "<> ")
			say("250 ok")
		case "DATA":
			say("354 go ahead")
			var b strings.Builder
			for {
				l, err := r.ReadString('\n')
				if err != nil {
					return
				}
				if l == ".\r\n" {
					break
				}
				b.WriteString(strings.TrimPrefix(l, "."))
			}
			m.Data = b.String()
			s.mu.Lock()
			s.msgs = append(s.msgs, m)
			s.mu.Unlock()
			m = Message{User: m.User}
			say("250 ok: queued")
		case "QUIT":
			say("221 bye")
			return
		default:
			say("502 not here")
		}
	}
}

// certificate is a self-signed certificate for 127.0.0.1 and localhost, and the roots that trust it.
func certificate(t testing.TB) (tls.Certificate, *x509.CertPool) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "mailtest"},
		NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour),
		IPAddresses: []net.IP{net.ParseIP("127.0.0.1")}, DNSNames: []string{"localhost"},
		KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		BasicConstraintsValid: true, IsCA: true,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	leaf, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatal(err)
	}
	roots := x509.NewCertPool()
	roots.AddCert(leaf)
	return tls.Certificate{Certificate: [][]byte{der}, PrivateKey: key, Leaf: leaf}, roots
}
