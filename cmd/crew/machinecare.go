package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/kesokaj/crew/internal/db"
	"github.com/kesokaj/crew/internal/machinecare"
)

// restartCheck is `crew admin restart-check --reboot-required=yes|no` (16.10), which crew-ctl's timer
// runs in the server's container: exit 0 is the one yes, and only after its news is stored; not quiet,
// outside the hours, or any error is exit 1 with the reason.
func restartCheck(ctx context.Context, e env, args []string, stdout io.Writer) error {
	var required bool
	switch {
	case len(args) == 1 && args[0] == "--reboot-required=yes":
		required = true
	case len(args) == 1 && args[0] == "--reboot-required=no":
	default:
		return usageError("admin restart-check --reboot-required=yes|no")
	}
	pool, err := db.Open(ctx, db.ConnFromEnv(db.RoleServer))
	if err != nil {
		return fmt.Errorf("no restart: %w", err)
	}
	defer pool.Close()
	a, err := machinecare.RestartCheck(ctx, pool, time.Now(), e.location(), required)
	if err != nil {
		return errors.New(a.Why)
	}
	if !a.Restart {
		if !required {
			fmt.Fprintln(stdout, a.Why)
			return nil
		}
		return errors.New("no restart: " + a.Why)
	}
	fmt.Fprintln(stdout, a.Why)
	return nil
}

// certificateCheck reads the ingress's certificate for Crew's address over the Compose network, checks
// it against the system's CAs with CREW_TLS acme, and sends news once when it has 14 days or fewer left,
// or when it does not verify (16.10). With CREW_TLS internal the ingress makes
// its own certificates of a few hours, renewed all day: there is nothing to tell.
func certificateCheck(ctx context.Context, pool *pgxpool.Pool, e env, log *slog.Logger) {
	if e.TLS == "internal" || e.Hostname == "" {
		return
	}
	chain, err := machinecare.ReadCertificate(ctx, e.IngressAddr, e.Hostname)
	if err != nil {
		log.Error("reading the ingress's certificate", "addr", e.IngressAddr, "error", err)
		return
	}
	// A certificate of the ACME service is one of a public CA; staging's is not, and is read as it is.
	if e.TLS == "acme" {
		if verr := machinecare.VerifyCertificate(chain, e.Hostname, nil); verr != nil {
			log.Error("the certificate at the ingress's address is not one of a public CA for Crew's name", "addr", e.IngressAddr, "error", verr)
			if err := machinecare.UnverifiedNews(ctx, pool, e.Hostname, chain[0], verr); err != nil {
				log.Error("the news of a certificate that does not verify", "error", err)
			}
			return
		}
	}
	cert := chain[0]
	near, err := machinecare.CertificateNews(ctx, pool, e.Hostname, cert, time.Now(), e.location())
	if err != nil {
		log.Error("the news of the ingress's certificate", "error", err)
		return
	}
	if near {
		log.Warn("the ingress's certificate runs out soon", "not_after", cert.NotAfter)
	}
}
