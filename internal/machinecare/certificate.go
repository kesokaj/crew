package machinecare

import (
	"context"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/hex"
	"errors"
	"fmt"
	"math"
	"net"
	"time"
	"unicode/utf8"

	"github.com/kesokaj/crew/internal/db"
)

// CertificateDays is how few days a certificate may have left before the operator gets news (16.10):
// the ingress renews one a third of its life before it runs out, so at 14 days a renewal failed.
const CertificateDays = 14

// ReadCertificate reads the certificates the ingress gives for name, at addr on the Compose network
// (crew-caddy:443), never through the public name: what the machine's own DNS or a proxy answers is
// not the ingress. The handshake verifies nothing, as nothing is sent over the connection and a
// certificate of the ingress's own CA (CREW_TLS staging) is read the same way; VerifyCertificate
// checks a public one. The first is the site's own.
func ReadCertificate(ctx context.Context, addr, name string) ([]*x509.Certificate, error) {
	d := tls.Dialer{NetDialer: &net.Dialer{Timeout: 10 * time.Second}, Config: &tls.Config{
		ServerName:         name,
		InsecureSkipVerify: true, // see above, and VerifyCertificate
		MinVersion:         tls.VersionTLS12,
	}}
	c, err := d.DialContext(ctx, "tcp", addr)
	if err != nil {
		return nil, err
	}
	defer c.Close()
	certs := c.(*tls.Conn).ConnectionState().PeerCertificates
	if len(certs) == 0 {
		return nil, errors.New("the ingress gave no certificate")
	}
	return certs, nil
}

// VerifyCertificate checks that chain is a certificate for name from a CA of roots (nil: the system's),
// as it was when it began: one that ran out still verifies, so its news is not lost, and one that
// something else on the network shows in the ingress's place does not (security review of
// 398035b..31089ee).
func VerifyCertificate(chain []*x509.Certificate, name string, roots *x509.CertPool) error {
	if len(chain) == 0 {
		return errors.New("no certificate")
	}
	inter := x509.NewCertPool()
	for _, c := range chain[1:] {
		inter.AddCert(c)
	}
	_, err := chain[0].Verify(x509.VerifyOptions{DNSName: name, Roots: roots, Intermediates: inter,
		CurrentTime: chain[0].NotBefore.Add(time.Minute)})
	return err
}

// CertificateNews writes news once for a certificate that has CertificateDays or fewer left at now,
// keyed by its hash, so a renewed one would be news of its own; its time is on the clock of loc
// (CREW_TIMEZONE). It says whether the certificate is close to its end.
func CertificateNews(ctx context.Context, q db.Querier, name string, cert *x509.Certificate, now time.Time, loc *time.Location) (bool, error) {
	left := cert.NotAfter.Sub(now)
	if left > CertificateDays*24*time.Hour {
		return false, nil
	}
	sum := sha256.Sum256(cert.Raw)
	var text string
	if left <= 0 {
		text = fmt.Sprintf("The certificate of %s ran out at %s and was not renewed: the ingress's log says why (crew-ctl logs ingress)",
			name, cert.NotAfter.In(loc).Format("Jan 2 15:04 MST"))
	} else {
		days := int(math.Ceil(left.Hours() / 24))
		text = fmt.Sprintf("The certificate of %s runs out in %d %s, at %s, and was not renewed: the ingress's log says why (crew-ctl logs ingress)",
			name, days, plural(days, "day", "days"), cert.NotAfter.In(loc).Format("Jan 2 15:04 MST"))
	}
	return true, News(ctx, q, "certificate", "certificate:"+hex.EncodeToString(sum[:]), text)
}

// UnverifiedNews writes news once for a certificate at the ingress's address that does not verify
// (CREW_TLS acme): Crew cannot tell from it when Crew's own runs out, which the operator must know
// rather than miss the warning of 14 days (security review of 31089ee..2afd57b).
func UnverifiedNews(ctx context.Context, q db.Querier, name string, cert *x509.Certificate, why error) error {
	sum := sha256.Sum256(cert.Raw)
	text := fmt.Sprintf("The certificate at the ingress's address for %s does not verify against the CAs Crew's server trusts (%s), so Crew cannot tell when its certificate runs out: the ingress's log says why (crew-ctl logs ingress)",
		name, cut(why.Error(), 300))
	return News(ctx, q, "certificate", "certificate-unverified:"+hex.EncodeToString(sum[:]), text)
}

func cut(s string, n int) string {
	if len(s) <= n {
		return s
	}
	for n > 0 && !utf8.RuneStart(s[n]) {
		n--
	}
	return s[:n] + " …"
}

func plural(n int, one, more string) string {
	if n == 1 {
		return one
	}
	return more
}
