package main

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"io"
	"log/slog"
	"math/big"
	"net"
	"strings"
	"testing"
	"time"

	"github.com/kesokaj/crew/internal/db/dbtest"
)

// The daily look at the ingress's certificate (16.10), by CREW_TLS: with acme a certificate of no
// public CA is news that it does not verify, never a date; with staging its date is read as it is;
// with internal nothing is read.
func TestTheCertificateCheckByCREWTLS(t *testing.T) {
	ctx := context.Background()
	const name = "crew.example.com"
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{SerialNumber: big.NewInt(7), DNSNames: []string{name},
		NotBefore: time.Now().Add(-80 * 24 * time.Hour), NotAfter: time.Now().Add(10*24*time.Hour - time.Hour)}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	cert := tls.Certificate{Certificate: [][]byte{der}, PrivateKey: key}
	ln, err := tls.Listen("tcp", "127.0.0.1:0", &tls.Config{Certificates: []tls.Certificate{cert}})
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go func(c net.Conn) { _ = c.(*tls.Conn).Handshake(); c.Close() }(c)
		}
	}()
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	news := func(d *dbtest.DB) []string {
		rows, err := d.Server.Query(ctx, `SELECT text FROM machine_news WHERE kind = 'certificate' ORDER BY id`)
		if err != nil {
			t.Fatal(err)
		}
		defer rows.Close()
		var out []string
		for rows.Next() {
			var s string
			rows.Scan(&s)
			out = append(out, s)
		}
		return out
	}
	for _, c := range []struct{ tls, want string }{
		{"acme", "does not verify against the CAs Crew's server trusts"},
		{"staging", "runs out in 10 days"},
		{"internal", ""},
	} {
		d := dbtest.New(t, false)
		e := env{Hostname: name, TLS: c.tls, IngressAddr: ln.Addr().String(), Timezone: "Europe/Stockholm"}
		certificateCheck(ctx, d.Server, e, log)
		certificateCheck(ctx, d.Server, e, log)
		got := news(d)
		if c.want == "" {
			if len(got) != 0 {
				t.Errorf("%s: %q", c.tls, got)
			}
			continue
		}
		if len(got) != 1 || !strings.Contains(got[0], c.want) {
			t.Errorf("%s: %q", c.tls, got)
		}
	}
}
