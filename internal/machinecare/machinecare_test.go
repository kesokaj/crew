package machinecare_test

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"errors"
	"math/big"
	"net"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/kesokaj/crew/internal/db"
	"github.com/kesokaj/crew/internal/db/dbtest"
	"github.com/kesokaj/crew/internal/hold"
	"github.com/kesokaj/crew/internal/machinecare"
)

var stockholm = func() *time.Location {
	l, err := time.LoadLocation("Europe/Stockholm")
	if err != nil {
		panic(err)
	}
	return l
}()

// at is a fake clock: a day of 2026 at hh:mm in Stockholm.
func at(day, hh, mm int) time.Time { return time.Date(2026, 10, day, hh, mm, 0, 0, stockholm) }

func news(t *testing.T, d *dbtest.DB, kind string) []string {
	t.Helper()
	rows, err := d.Server.Query(context.Background(), `SELECT text FROM machine_news WHERE kind = $1 ORDER BY id`, kind)
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for rows.Next() {
		var s string
		if err := rows.Scan(&s); err != nil {
			t.Fatal(err)
		}
		out = append(out, s)
	}
	return out
}

func check(t *testing.T, d *dbtest.DB, now time.Time, required bool) machinecare.Answer {
	t.Helper()
	a, err := machinecare.RestartCheck(context.Background(), d.Server, now, stockholm, required)
	if err != nil {
		t.Fatal(err)
	}
	return a
}

// live gives each sandbox the token Start gives it, which Remove takes first.
func live(t *testing.T, d *dbtest.DB, ids ...string) {
	t.Helper()
	for _, id := range ids {
		if _, err := d.Server.Exec(context.Background(), `INSERT INTO sandbox_tokens (sha256, sandbox, ends_at) VALUES ($1, $1, now() + interval '1 hour')`, id); err != nil {
			t.Fatal(err)
		}
	}
}

func state(t *testing.T, d *dbtest.DB) hold.State {
	t.Helper()
	st, err := hold.Read(context.Background(), d.Server)
	if err != nil {
		t.Fatal(err)
	}
	return st
}

// The restart in the hours (16.10, D19): the first yes from the machine is recorded and kept; outside
// machine_care.restart_hours, or with a run going, it is no with the reason; in the hours and quiet it
// is yes, with its news written and new runs held while the machine goes down. A hosted project and
// the probe do not count as going.
func TestARestartWaitsForQuietHours(t *testing.T) {
	ctx := context.Background()
	d := dbtest.New(t, false)
	if a := check(t, d, at(20, 12, 0), false); a.Restart || state(t, d).RebootWanted != nil {
		t.Fatalf("nothing needed: %+v", a)
	}
	a := check(t, d, at(20, 12, 0), true)
	if a.Restart || !strings.Contains(a.Why, "outside the hours 02:00-05:00 (Europe/Stockholm)") {
		t.Fatalf("outside the hours: %+v", a)
	}
	if w := state(t, d).RebootWanted; w == nil || !w.Equal(at(20, 12, 0)) {
		t.Fatalf("the first yes is not recorded: %v", w)
	}
	check(t, d, at(20, 12, 15), true)
	if w := state(t, d).RebootWanted; !w.Equal(at(20, 12, 0)) {
		t.Fatalf("a later ask moved the first time: %v", w)
	}
	if _, err := d.Server.Exec(ctx, `INSERT INTO sandboxes (id, kind, container, image) VALUES
		('run-1', 'run', 'run-1', 'x'), ('host-1', 'hosted', 'host-1', 'x'), ('probe-1', 'probe', 'probe-1', 'x'), ('old', 'run', 'old', 'x'),
		('lost', 'check', 'lost', 'x')`); err != nil {
		t.Fatal(err)
	}
	// Every sandbox but lost holds its token, as Start gives it; lost is one whose container Docker
	// failed to remove: Remove took its token, and it is going no longer.
	live(t, d, "run-1", "host-1", "probe-1", "old")
	if _, err := d.Server.Exec(ctx, `UPDATE sandboxes SET removed_at = now() WHERE id = 'old'`); err != nil {
		t.Fatal(err)
	}
	a = check(t, d, at(21, 3, 0), true)
	if a.Restart || a.Why != "not quiet: a run sandbox going" {
		t.Fatalf("a run going: %+v", a)
	}
	if len(news(t, d, "restart")) != 0 || state(t, d).Held() != "" {
		t.Fatal("a no wrote news or held Crew")
	}
	if _, err := d.Server.Exec(ctx, `UPDATE sandboxes SET removed_at = now() WHERE id = 'run-1'`); err != nil {
		t.Fatal(err)
	}
	a = check(t, d, at(21, 3, 0), true)
	if !a.Restart || !strings.Contains(a.Why, "in the hours 02:00-05:00") {
		t.Fatalf("quiet in the hours: %+v", a)
	}
	if n := news(t, d, "restart"); len(n) != 1 || !strings.HasPrefix(n[0], "The machine restarts for security updates") {
		t.Fatalf("the news of the restart: %q", n)
	}
	if st := state(t, d); !st.Restarting || st.Held() != hold.RestartingWhy {
		t.Fatalf("a yes does not hold new runs: %+v", st)
	}
	// After the restart the machine needs none: what was kept goes, and so does the hold.
	if a := check(t, d, at(21, 3, 20), false); a.Restart {
		t.Fatalf("no file: %+v", a)
	}
	if st := state(t, d); st.RebootWanted != nil || st.Restarting || st.Held() != "" {
		t.Fatalf("not cleared: %+v", st)
	}
}

// The edges of the hours: From is in them, To is not.
func TestTheHoursEdges(t *testing.T) {
	d := dbtest.New(t, false)
	for _, c := range []struct {
		now  time.Time
		want bool
	}{{at(20, 1, 59), false}, {at(20, 2, 0), true}, {at(20, 4, 59), true}, {at(20, 5, 0), false}} {
		if _, err := d.Server.Exec(context.Background(), `UPDATE crew_state SET restart_at = NULL, reboot_wanted_at = NULL`); err != nil {
			t.Fatal(err)
		}
		if a := check(t, d, c.now, true); a.Restart != c.want {
			t.Errorf("%s: %+v", c.now.Format("15:04"), a)
		}
	}
}

// Day 7 (16.10, D19): a restart that waited seven days holds new runs, with its news once, and comes
// as soon as the runs finished, at any hour; before day 7 nothing is held.
func TestDaySevenHoldsNewRunsAndRestartsWhenQuiet(t *testing.T) {
	ctx := context.Background()
	d := dbtest.New(t, false)
	if _, err := d.Server.Exec(ctx, `INSERT INTO sandboxes (id, kind, container, image) VALUES ('run-1', 'run', 'run-1', 'x')`); err != nil {
		t.Fatal(err)
	}
	live(t, d, "run-1")
	check(t, d, at(1, 12, 0), true)
	if a := check(t, d, at(8, 11, 59), true); a.Restart || state(t, d).RestartWait != nil {
		t.Fatalf("day 6 and 23:59 hours: %+v", a)
	}
	a := check(t, d, at(8, 12, 0), true)
	if a.Restart || a.Why != "not quiet: a run sandbox going" {
		t.Fatalf("day 7 with a run going: %+v", a)
	}
	st := state(t, d)
	if st.RestartWait == nil || st.Held() != hold.RestartWaitWhy {
		t.Fatalf("day 7 does not hold: %+v %q", st, st.Held())
	}
	check(t, d, at(8, 12, 15), true)
	if n := news(t, d, "restart_wait"); len(n) != 1 || !strings.Contains(n[0], "waited 7 days") {
		t.Fatalf("the news of the wait, once: %q", n)
	}
	if _, err := d.Server.Exec(ctx, `UPDATE sandboxes SET removed_at = now()`); err != nil {
		t.Fatal(err)
	}
	a = check(t, d, at(8, 12, 30), true)
	if !a.Restart || !strings.Contains(a.Why, "after 7 days of waiting") {
		t.Fatalf("day 7, quiet, outside the hours: %+v", a)
	}
	if n := news(t, d, "restart"); len(n) != 1 {
		t.Fatalf("the news of the restart: %q", n)
	}
	// A reboot that failed: the next tick says yes again, and writes no second line of news.
	if a := check(t, d, at(8, 12, 45), true); !a.Restart {
		t.Fatalf("the yes again: %+v", a)
	}
	if n := news(t, d, "restart"); len(n) != 1 {
		t.Fatalf("a reboot that failed wrote news again: %q", n)
	}
	check(t, d, at(8, 13, 0), false)
	if st := state(t, d); st.RestartWait != nil || st.Held() != "" {
		t.Fatalf("the restart did not clear the wait: %+v", st)
	}
}

// A build, a gate that checks, and an install the updater runs are going too; an install row older
// than six hours is not.
func TestABuildAGateAndAnInstallAreGoing(t *testing.T) {
	ctx := context.Background()
	d := dbtest.New(t, false)
	now := time.Now()
	for _, c := range []struct{ sql, want string }{
		{`INSERT INTO builds (version, commit, asked_by) VALUES ('9.9.9', 'abc', 'operator')`, "a build of a release"},
		{`INSERT INTO update_requests (id, version, action, asked_by, state, asked_at) VALUES (1, '9.9.8', 'install', 'operator', 'running', now())`, "an install"},
	} {
		if _, err := d.Server.Exec(ctx, c.sql); err != nil {
			t.Fatal(err)
		}
		going, err := machinecare.Going(ctx, d.Server, now)
		if err != nil || !strings.Contains(strings.Join(going, ","), c.want) {
			t.Fatalf("%s: %v %v", c.want, going, err)
		}
	}
	if _, err := d.Server.Exec(ctx, `UPDATE update_requests SET asked_at = now() - interval '7 hours'; UPDATE builds SET state = 'done'`); err != nil {
		t.Fatal(err)
	}
	if going, err := machinecare.Going(ctx, d.Server, now); err != nil || len(going) != 0 {
		t.Fatalf("an old install row: %v %v", going, err)
	}
}

// A database that cannot be written is never a yes: no restart without its news (D19).
func TestADatabaseThatCannotBeWrittenIsNoRestart(t *testing.T) {
	ctx := context.Background()
	d := dbtest.New(t, false)
	ro := readOnly(t, d)
	if a, err := machinecare.RestartCheck(ctx, ro, at(21, 3, 0), stockholm, true); err == nil || a.Restart || !strings.HasPrefix(a.Why, "no restart: ") {
		t.Fatalf("the first yes, read only: %+v %v", a, err)
	}
	// The first yes recorded while it could write; then it cannot, in the hours and quiet: the news
	// and the hold cannot be written, so no.
	check(t, d, at(20, 12, 0), true)
	if a, err := machinecare.RestartCheck(ctx, ro, at(21, 3, 0), stockholm, true); err == nil || a.Restart {
		t.Fatalf("quiet in the hours, read only: %+v %v", a, err)
	}
	if len(news(t, d, "restart")) != 0 {
		t.Fatal("news was written")
	}
}

func readOnly(t *testing.T, d *dbtest.DB) *pgxpool.Pool {
	t.Helper()
	url, err := d.Conn(db.RoleServer).URL()
	if err != nil {
		t.Fatal(err)
	}
	cfg, err := pgxpool.ParseConfig(url)
	if err != nil {
		t.Fatal(err)
	}
	cfg.ConnConfig.RuntimeParams["default_transaction_read_only"] = "on"
	pool, err := pgxpool.NewWithConfig(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	return pool
}

// certificate makes a certificate for name that runs out at notAfter, with its key.
func certificate(t *testing.T, name string, notAfter time.Time) tls.Certificate {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{SerialNumber: big.NewInt(time.Now().UnixNano()), Subject: pkix.Name{CommonName: name}, DNSNames: []string{name},
		NotBefore: notAfter.Add(-90 * 24 * time.Hour), NotAfter: notAfter}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	return tls.Certificate{Certificate: [][]byte{der}, PrivateKey: key}
}

// ingress serves cert for name only, as the ingress serves a site by its name; it says its address.
func ingress(t *testing.T, name string, cert tls.Certificate) string {
	t.Helper()
	ln, err := tls.Listen("tcp", "127.0.0.1:0", &tls.Config{GetCertificate: func(h *tls.ClientHelloInfo) (*tls.Certificate, error) {
		if h.ServerName != name {
			return nil, errors.New("no site " + h.ServerName)
		}
		return &cert, nil
	}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go func(c net.Conn) { _ = c.(*tls.Conn).Handshake(); c.Close() }(c)
		}
	}()
	return ln.Addr().String()
}

// Certificate news at 14 days (16.10): the ingress's certificate is read by Crew's own name at the
// Compose address, and one with 14 days or fewer left is news once; one with more is none.
func TestCertificateNewsAtFourteenDays(t *testing.T) {
	ctx := context.Background()
	d := dbtest.New(t, false)
	now := time.Now()
	const name = "crew.example.com"
	addr := ingress(t, name, certificate(t, name, now.Add(10*24*time.Hour-time.Hour)))
	if _, err := machinecare.ReadCertificate(ctx, addr, "other.example.com"); err == nil {
		t.Fatal("another name got a certificate")
	}
	chain, err := machinecare.ReadCertificate(ctx, addr, name)
	if err != nil {
		t.Fatal(err)
	}
	cert := chain[0]
	for range 2 {
		near, err := machinecare.CertificateNews(ctx, d.Server, name, cert, now, stockholm)
		if err != nil || !near {
			t.Fatalf("10 days: %v %v", near, err)
		}
	}
	if n := news(t, d, "certificate"); len(n) != 1 || !strings.Contains(n[0], "The certificate of crew.example.com runs out in 10 days") {
		t.Fatalf("the news, once: %q", n)
	}
	far, err := machinecare.ReadCertificate(ctx, ingress(t, name, certificate(t, name, now.Add(15*24*time.Hour))), name)
	if err != nil {
		t.Fatal(err)
	}
	if near, err := machinecare.CertificateNews(ctx, d.Server, name, far[0], now, stockholm); err != nil || near {
		t.Fatalf("15 days: %v %v", near, err)
	}
	edge := certificate(t, name, now.Add(14*24*time.Hour))
	c14, _ := x509.ParseCertificate(edge.Certificate[0])
	gone := certificate(t, name, now.Add(-time.Hour))
	cGone, _ := x509.ParseCertificate(gone.Certificate[0])
	for _, c := range []*x509.Certificate{c14, cGone} {
		if near, err := machinecare.CertificateNews(ctx, d.Server, name, c, now, stockholm); err != nil || !near {
			t.Fatalf("%s: %v %v", c.NotAfter, near, err)
		}
	}
	if n := news(t, d, "certificate"); len(n) != 3 || !strings.Contains(n[1], "runs out in 14 days") || !strings.Contains(n[2], "ran out at") {
		t.Fatalf("14 days and gone: %q", n)
	}
}

// With CREW_TLS acme the certificate is one of a CA for Crew's name, as it was when it began: one that
// ran out still verifies, a certificate of no known CA or for another name does not.
func TestVerifyCertificate(t *testing.T) {
	const name = "crew.example.com"
	caKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	caTmpl := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "test CA"}, IsCA: true, BasicConstraintsValid: true,
		KeyUsage: x509.KeyUsageCertSign, NotBefore: now.Add(-365 * 24 * time.Hour), NotAfter: now.Add(365 * 24 * time.Hour)}
	caDER, err := x509.CreateCertificate(rand.Reader, caTmpl, caTmpl, &caKey.PublicKey, caKey)
	if err != nil {
		t.Fatal(err)
	}
	ca, _ := x509.ParseCertificate(caDER)
	roots := x509.NewCertPool()
	roots.AddCert(ca)
	leaf := func(dns string, notAfter time.Time) *x509.Certificate {
		key, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
		tmpl := &x509.Certificate{SerialNumber: big.NewInt(time.Now().UnixNano()), DNSNames: []string{dns},
			NotBefore: notAfter.Add(-90 * 24 * time.Hour), NotAfter: notAfter, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}}
		der, err := x509.CreateCertificate(rand.Reader, tmpl, ca, &key.PublicKey, caKey)
		if err != nil {
			t.Fatal(err)
		}
		c, _ := x509.ParseCertificate(der)
		return c
	}
	if err := machinecare.VerifyCertificate([]*x509.Certificate{leaf(name, now.Add(10*24*time.Hour)), ca}, name, roots); err != nil {
		t.Errorf("a certificate of the CA: %v", err)
	}
	if err := machinecare.VerifyCertificate([]*x509.Certificate{leaf(name, now.Add(-time.Hour))}, name, roots); err != nil {
		t.Errorf("a certificate that ran out: %v", err)
	}
	if err := machinecare.VerifyCertificate([]*x509.Certificate{leaf("other.example.com", now.Add(10*24*time.Hour))}, name, roots); err == nil {
		t.Error("a certificate for another name verified")
	}
	self, _ := x509.ParseCertificate(certificate(t, name, now.Add(10*24*time.Hour)).Certificate[0])
	if err := machinecare.VerifyCertificate([]*x509.Certificate{self}, name, roots); err == nil {
		t.Error("a certificate of no known CA verified")
	}
	if err := machinecare.VerifyCertificate(nil, name, roots); err == nil {
		t.Error("no certificate verified")
	}
}
