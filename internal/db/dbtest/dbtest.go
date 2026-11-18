// Package dbtest gives a test a database of its own, on the PostgreSQL that tools/check-db.sh starts
// for make check, with the three roles of an installation and every schema step applied.
package dbtest

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/kesokaj/crew/internal/db"
)

// EnvHost names the test database's host. make check sets it; a test that needs a database fails
// without it rather than skip, so a missing database is never counted as coverage.
const EnvHost = "CREW_TEST_DB_HOST"

// EnvNoDrop skips dropping each test's database when a caller sets it explicitly; make check leaves
// it unset so finished per-test databases are dropped instead of filling the tmpfs PostgreSQL container.
const EnvNoDrop = "CREW_TEST_NO_DROP"

const (
	sharedTemplate     = "crew_tpl"
	sharedTemplateLock = int64(77201941)
)

var (
	tplOnce sync.Once
	tplName string
	tplErr  error
)

// DB is one test's database.
type DB struct {
	Name string
	// Server is a pool as the server's role, crew.
	Server *pgxpool.Pool
}

// Conn returns how to reach this test's database as role.
func (d *DB) Conn(role string) db.Conn {
	c := db.ConnFromEnv(role)
	c.Host = os.Getenv(EnvHost)
	c.Name = d.Name
	return c
}

// ConnectAs opens a single connection as role.
func (d *DB) ConnectAs(t *testing.T, role string) *pgx.Conn {
	t.Helper()
	dsn, err := d.Conn(role).URL()
	if err != nil {
		t.Fatal(err)
	}
	conn, err := pgx.Connect(context.Background(), dsn)
	if err != nil {
		t.Fatalf("connecting as %s: %v", role, err)
	}
	t.Cleanup(func() { conn.Close(context.Background()) })
	return conn
}

// New makes an empty database owned by crew_migrate, with the grants of an installation, and applies
// every schema step unless empty is true. For empty == false, the first call on the PostgreSQL cluster
// makes one shared migrated template database (crew_tpl) under an advisory lock and every call across
// parallel test processes copies it with CREATE DATABASE ... TEMPLATE, then runs database-level grants
// again because PostgreSQL's TEMPLATE does not copy database ACLs; crew_tpl itself stays until
// tools/check-db.sh discards the tmpfs container. Each test's own database is dropped when the test ends.
func New(t *testing.T, empty bool) *DB {
	t.Helper()
	host := os.Getenv(EnvHost)
	if host == "" {
		t.Fatalf("%s is not set: run the database tests with make check, which starts the test database", EnvHost)
	}
	ctx := context.Background()
	name, err := randName("crew_t_")
	if err != nil {
		t.Fatal(err)
	}
	d := &DB{Name: name}

	admin := db.ConnFromEnv("postgres")
	admin.Host, admin.Name = host, "postgres"
	adsn, err := admin.URL()
	if err != nil {
		t.Fatal(err)
	}
	super, err := pgx.Connect(ctx, adsn)
	if err != nil {
		t.Fatalf("connecting as postgres: %v", err)
	}
	defer super.Close(ctx)

	if empty {
		if _, err := super.Exec(ctx, "CREATE DATABASE "+d.Name+" OWNER crew_migrate"); err != nil {
			t.Fatal(err)
		}
		if err := applyGrants(ctx, host, d.Name); err != nil {
			t.Fatal(err)
		}
	} else {
		if err := createFromTemplate(ctx, host, super, d.Name); err != nil {
			t.Fatal(err)
		}
		if err := applyDatabaseGrants(ctx, super, d.Name); err != nil {
			t.Fatal(err)
		}
	}
	if os.Getenv(EnvNoDrop) == "" {
		t.Cleanup(func() {
			c, err := pgx.Connect(context.Background(), adsn)
			if err != nil {
				t.Errorf("dropping %s: %v", d.Name, err)
				return
			}
			defer c.Close(context.Background())
			if _, err := c.Exec(context.Background(), "DROP DATABASE "+d.Name+" WITH (FORCE)"); err != nil {
				t.Errorf("dropping %s: %v", d.Name, err)
			}
		})
	}
	pool, err := db.Open(ctx, d.Conn(db.RoleServer))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	d.Server = pool
	return d
}

func createFromTemplate(ctx context.Context, host string, super *pgx.Conn, name string) error {
	tplOnce.Do(func() {
		tplName, tplErr = makeTemplate(ctx, host, super)
	})
	if tplErr != nil {
		return tplErr
	}
	_, err := super.Exec(ctx, "CREATE DATABASE "+name+" TEMPLATE "+tplName+" OWNER crew_migrate")
	return err
}

// applyDatabaseGrants applies the database-level ACLs that CREATE DATABASE ... TEMPLATE does not copy
// over the already-open superuser connection, while schema and table privileges come from the template.
func applyDatabaseGrants(ctx context.Context, super *pgx.Conn, name string) error {
	grants, err := Grants(name)
	if err != nil {
		return err
	}
	for _, q := range grants {
		if !strings.Contains(strings.ToUpper(q), " ON DATABASE ") {
			continue
		}
		if _, err := super.Exec(ctx, q); err != nil {
			return fmt.Errorf("%s: %w", q, err)
		}
	}
	return nil
}

func makeTemplate(ctx context.Context, host string, super *pgx.Conn) (string, error) {
	if _, err := super.Exec(ctx, "SELECT pg_advisory_lock($1)", sharedTemplateLock); err != nil {
		return "", fmt.Errorf("locking template database: %w", err)
	}
	defer func() {
		// Best-effort unlock before super closes at the end of New.
		_, _ = super.Exec(context.Background(), "SELECT pg_advisory_unlock($1)", sharedTemplateLock)
	}()

	var exists, sealed bool
	if err := super.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM pg_database WHERE datname = $1),
		EXISTS (SELECT 1 FROM pg_database WHERE datname = $1 AND NOT datallowconn)`, sharedTemplate).Scan(&exists, &sealed); err != nil {
		return "", err
	}
	if sealed {
		return sharedTemplate, nil
	}
	if exists {
		if _, err := super.Exec(ctx, "DROP DATABASE "+sharedTemplate+" WITH (FORCE)"); err != nil {
			return "", err
		}
	}
	if _, err := super.Exec(ctx, "CREATE DATABASE "+sharedTemplate+" OWNER crew_migrate"); err != nil {
		return "", err
	}
	if err := applyGrants(ctx, host, sharedTemplate); err != nil {
		return "", err
	}
	mig := db.ConnFromEnv(db.RoleMigrate)
	mig.Host, mig.Name = host, sharedTemplate
	mdsn, err := mig.URL()
	if err != nil {
		return "", err
	}
	mconn, err := pgx.Connect(ctx, mdsn)
	if err != nil {
		return "", fmt.Errorf("connecting as %s: %w", db.RoleMigrate, err)
	}
	_, migErr := db.Migrate(ctx, mconn)
	closeErr := mconn.Close(ctx)
	if migErr != nil {
		return "", migErr
	}
	if closeErr != nil {
		return "", closeErr
	}
	if _, err := super.Exec(ctx, "ALTER DATABASE "+sharedTemplate+" ALLOW_CONNECTIONS false"); err != nil {
		return "", err
	}
	return sharedTemplate, nil
}

// applyGrants runs the statements of deploy/db/10-roles.sh for the database name, so the roles and
// privileges match an installation.
func applyGrants(ctx context.Context, host, name string) error {
	admin := db.ConnFromEnv("postgres")
	admin.Host, admin.Name = host, name
	sdsn, err := admin.URL()
	if err != nil {
		return err
	}
	sc, err := pgx.Connect(ctx, sdsn)
	if err != nil {
		return err
	}
	defer sc.Close(ctx)
	grants, err := Grants(name)
	if err != nil {
		return err
	}
	for _, q := range grants {
		if _, err := sc.Exec(ctx, q); err != nil {
			return fmt.Errorf("%s: %w", q, err)
		}
	}
	return nil
}

func randName(prefix string) (string, error) {
	b := make([]byte, 6)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return prefix + hex.EncodeToString(b), nil
}

// Grants returns the statements of deploy/db/10-roles.sh that give the database crew its privileges,
// for a database of another name: everything but making the roles and the database.
func Grants(name string) ([]string, error) {
	_, here, _, _ := runtime.Caller(0)
	path := filepath.Join(filepath.Dir(here), "..", "..", "..", "deploy", "db", "10-roles.sh")
	body, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	out, err := db.Grants(body, name)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	return out, nil
}
