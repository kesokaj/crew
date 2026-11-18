// Package db is Crew's database: how it is reached, the schema with its numbered changes, and the
// queries the server makes (design 15.3).
package db

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

// The three roles (15.3). The server is crew; crew_migrate may change the schema, and is used only by
// `crew admin migrate` and `restore`; crew_read can only read, and makes the backups.
const (
	RoleServer  = "crew"
	RoleMigrate = "crew_migrate"
	RoleRead    = "crew_read"
)

// Conn says how to reach the database as one role. The password is read from a file, a Docker
// secret, and never from the environment itself.
type Conn struct {
	Host         string
	Port         string
	Name         string
	Role         string
	PasswordFile string
}

// ConnFromEnv reads CREW_DB_HOST, CREW_DB_PORT, and CREW_DB_NAME, and takes the password of role from
// CREW_DB_SECRETS_DIR/db_<role>, which is /run/secrets unless set.
func ConnFromEnv(role string) Conn {
	get := func(k, def string) string {
		if v := os.Getenv(k); v != "" {
			return v
		}
		return def
	}
	return Conn{
		Host:         get("CREW_DB_HOST", "crew-db"),
		Port:         get("CREW_DB_PORT", "5432"),
		Name:         get("CREW_DB_NAME", "crew"),
		Role:         role,
		PasswordFile: strings.TrimSuffix(get("CREW_DB_SECRETS_DIR", "/run/secrets"), "/") + "/db_" + role,
	}
}

// URL is the connection URL, with the password, for pgx and for the PostgreSQL tools. It is never
// logged or printed.
func (c Conn) URL() (string, error) {
	pw, err := os.ReadFile(c.PasswordFile)
	if err != nil {
		return "", fmt.Errorf("the password of %s: %w", c.Role, err)
	}
	password := strings.TrimSpace(string(pw))
	if password == "" {
		return "", fmt.Errorf("the password file of %s is empty", c.Role)
	}
	u := url.URL{
		Scheme:   "postgres",
		User:     url.UserPassword(c.Role, password),
		Host:     net.JoinHostPort(c.Host, c.Port),
		Path:     "/" + c.Name,
		RawQuery: "sslmode=disable&application_name=crew",
	}
	return u.String(), nil
}

// Env is the connection in the variables the PostgreSQL tools read, password included, so that it is
// never on a command line.
func (c Conn) Env() ([]string, error) {
	pw, err := os.ReadFile(c.PasswordFile)
	if err != nil {
		return nil, fmt.Errorf("the password of %s: %w", c.Role, err)
	}
	return []string{"PGHOST=" + c.Host, "PGPORT=" + c.Port, "PGDATABASE=" + c.Name, "PGUSER=" + c.Role,
		"PGPASSWORD=" + strings.TrimSpace(string(pw)), "PGSSLMODE=disable", "PGAPPNAME=crew"}, nil
}

// Open connects a pool and checks that the database answers. The database is on the core services'
// own network and has no port on the machine, so the connection is not encrypted.
func Open(ctx context.Context, c Conn) (*pgxpool.Pool, error) {
	dsn, err := c.URL()
	if err != nil {
		return nil, err
	}
	cfg, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		return nil, errors.New("the database address could not be read")
	}
	cfg.MaxConns = 16
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		return nil, fmt.Errorf("connecting to the database as %s: %w", c.Role, err)
	}
	pctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	if err := pool.Ping(pctx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("the database does not answer as %s: %w", c.Role, err)
	}
	return pool, nil
}

// Querier is what both a pool and a transaction offer.
type Querier interface {
	Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error)
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
}
