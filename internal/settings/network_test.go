package settings_test

import (
	"context"
	"slices"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/kesokaj/crew/internal/db/dbtest"
	"github.com/kesokaj/crew/internal/settings"
)

// A sandbox's mode is its ticket's, else its project's, else the machine's, and "inherit" passes the
// choice on at each step (13.6).
func TestTheNetworkModeGoesTicketThenProjectThenMachine(t *testing.T) {
	ctx := context.Background()
	d := dbtest.New(t, false)
	const fixture = "f1c50000-0000-4000-8000-000000000001"
	var ticket int64
	if err := d.Server.QueryRow(ctx, `INSERT INTO tickets (project, number, title, kind, stage, origin, created_by, position)
		VALUES ($1, 901, 'net', 'feature', 'backlog', 'operator', 'operator', 1) RETURNING id`, fixture).Scan(&ticket); err != nil {
		t.Fatal(err)
	}
	mode := func(project string, ticket int64) string {
		t.Helper()
		m, err := settings.NetworkMode(ctx, d.Server, project, ticket)
		if err != nil {
			t.Fatal(err)
		}
		return m
	}
	setMachine := func(doc string) {
		t.Helper()
		if _, err := d.Server.Exec(ctx, `DELETE FROM settings WHERE project IS NULL`); err != nil {
			t.Fatal(err)
		}
		if _, err := d.Server.Exec(ctx, `INSERT INTO settings (project, doc, updated_by) VALUES (NULL, $1::jsonb, 'test')`, doc); err != nil {
			t.Fatal(err)
		}
	}
	setProject := func(network string) {
		t.Helper()
		doc, _ := settings.ProjectDefaults("fixture", "FIX")
		doc["sandbox"].(settings.Doc)["network"] = network
		if err := pgx.BeginFunc(ctx, d.Server, func(tx pgx.Tx) error {
			return settings.SaveProject(ctx, tx, fixture, "fixture", "FIX", doc, "test")
		}); err != nil {
			t.Fatal(err)
		}
	}

	// Nothing set anywhere: the machine's default, the internet.
	if m := mode(fixture, ticket); m != "internet" {
		t.Fatalf("with nothing set: %q", m)
	}
	if m := mode("", 0); m != "internet" {
		t.Fatalf("no project: %q", m)
	}
	setMachine(`{"network": {"mode": "model_only"}}`)
	if m := mode(fixture, ticket); m != "model_only" {
		t.Fatalf("the machine's mode: %q", m)
	}
	setProject("lan")
	if m := mode(fixture, ticket); m != "lan" {
		t.Fatalf("the project's mode: %q", m)
	}
	if m := mode("", 0); m != "model_only" {
		t.Fatalf("a sandbox of no project has the machine's mode: %q", m)
	}
	if _, err := d.Server.Exec(ctx, `UPDATE tickets SET network = 'internet' WHERE id = $1`, ticket); err != nil {
		t.Fatal(err)
	}
	if m := mode(fixture, ticket); m != "internet" {
		t.Fatalf("the ticket's mode: %q", m)
	}
	setProject("inherit")
	if _, err := d.Server.Exec(ctx, `UPDATE tickets SET network = 'inherit' WHERE id = $1`, ticket); err != nil {
		t.Fatal(err)
	}
	if m := mode(fixture, ticket); m != "model_only" {
		t.Fatalf("inherit twice is the machine's: %q", m)
	}

	// A value that is no mode is an error, never a guess.
	if _, err := d.Server.Exec(ctx, `UPDATE tickets SET network = 'open' WHERE id = $1`, ticket); err != nil {
		t.Fatal(err)
	}
	if _, err := settings.NetworkMode(ctx, d.Server, fixture, ticket); err == nil {
		t.Fatal("a ticket with the mode 'open' got a mode")
	}

	// The proxy's list is the listing's until the machine has its own.
	hosts, err := settings.ModelOnlyHosts(ctx, d.Server)
	if err != nil || !slices.Contains(hosts, "daily-cloudcode-pa.googleapis.com") || len(hosts) != 5 {
		t.Fatalf("hosts %v (%v)", hosts, err)
	}
	setMachine(`{"network": {"model_only_hosts": ["daily-cloudcode-pa.googleapis.com"]}}`)
	if hosts, _ := settings.ModelOnlyHosts(ctx, d.Server); !slices.Equal(hosts, []string{"daily-cloudcode-pa.googleapis.com"}) {
		t.Fatalf("the machine's own list: %v", hosts)
	}
}

// The hosts a Sources sandbox reaches are the listing's `sandbox.sources` until the machine has its
// own (13.6, since 0.51), and a list for Model only is not one for Sources.
func TestTheSourcesAreTheirOwnList(t *testing.T) {
	ctx := context.Background()
	d := dbtest.New(t, false)
	hosts, err := settings.SourcesHosts(ctx, d.Server)
	if err != nil || !slices.Contains(hosts, "proxy.golang.org") || !slices.Contains(hosts, "registry.npmjs.org") {
		t.Fatalf("hosts %v (%v)", hosts, err)
	}
	if slices.Contains(hosts, "daily-cloudcode-pa.googleapis.com") {
		t.Fatalf("the sources hold the model: %v", hosts)
	}
	// Neither agy's tarball, which the server downloads and hands to the build (12.8, since 0.57), nor
	// the vendor's install script, which the image no longer runs (since 0.56): storage.googleapis.com
	// takes uploads to any bucket (13.6).
	if slices.Contains(hosts, "storage.googleapis.com") || slices.Contains(hosts, "antigravity.google") {
		t.Fatalf("the sources for agy: %v", hosts)
	}
	if _, err := d.Server.Exec(ctx, `INSERT INTO settings (project, doc, updated_by) VALUES (NULL, $1::jsonb, 'test')`,
		`{"sandbox": {"sources": ["proxy.golang.org"]}, "network": {"model_only_hosts": ["aicode.googleapis.com"]}}`); err != nil {
		t.Fatal(err)
	}
	if hosts, _ := settings.SourcesHosts(ctx, d.Server); !slices.Equal(hosts, []string{"proxy.golang.org"}) {
		t.Fatalf("the machine's own list: %v", hosts)
	}
}

// A project's check sandboxes have its sandbox.check_network when it names a mode, Sources among them;
// with inherit they have the runs' mode (13.6, 13.9, since 0.53). A check runs code an agent wrote, so
// its mode is never wider than its project's runs: wider, it is theirs (security review of 0.53). A
// ticket's own mode is the ceiling instead where it is wider, which only the operator sets; a ticket
// narrowed to Model only narrows its agent runs alone, which hold the login, not its checks (since 0.55,
// the operator's choice of L4). A project's runs never have Sources: it lets the package sources through
// and not the model's hosts.
func TestAChecksNetworkModeIsItsOwnOrTheRuns(t *testing.T) {
	ctx := context.Background()
	d := dbtest.New(t, false)
	const fixture = "f1c50000-0000-4000-8000-000000000001"
	newTicket := func(number int, network string) int64 {
		var id int64
		if err := d.Server.QueryRow(ctx, `INSERT INTO tickets (project, number, title, kind, stage, origin, created_by, position, network)
			VALUES ($1, $2, 'net', 'feature', 'backlog', 'operator', 'operator', 1, $3) RETURNING id`, fixture, number, network).Scan(&id); err != nil {
			t.Fatal(err)
		}
		return id
	}
	wide, narrow := newTicket(902, "internet"), newTicket(903, "model_only")
	save := func(network, check string) error {
		doc, _ := settings.ProjectDefaults("fixture", "FIX")
		sb := doc["sandbox"].(settings.Doc)
		sb["network"], sb["check_network"] = network, check
		return pgx.BeginFunc(ctx, d.Server, func(tx pgx.Tx) error {
			return settings.SaveProject(ctx, tx, fixture, "fixture", "FIX", doc, "test")
		})
	}
	check := func(ticket int64, want string) {
		t.Helper()
		m, err := settings.CheckNetworkMode(ctx, d.Server, fixture, ticket)
		if err != nil || m != want {
			t.Fatalf("the check's mode %q, not %q: %v", m, want, err)
		}
	}
	// The wide ticket's runs have the internet: Sources is narrower, and its check has it.
	if err := save("model_only", "sources"); err != nil {
		t.Fatal(err)
	}
	check(wide, "sources")
	if m, err := settings.NetworkMode(ctx, d.Server, fixture, 0); err != nil || m != "model_only" {
		t.Fatalf("the runs' mode %q: %v", m, err)
	}
	// Runs in Model only: Sources is wider, so a check of no ticket, or of a ticket in Model only, has
	// Model only; the LAN is wider than the wide ticket's internet, so its check has the internet.
	check(0, "model_only")
	check(narrow, "model_only")
	if err := save("model_only", "lan"); err != nil {
		t.Fatal(err)
	}
	check(wide, "internet")
	if err := save("model_only", "inherit"); err != nil {
		t.Fatal(err)
	}
	check(wide, "internet") // the ticket's, as its runs have
	// The project's runs have the internet: a ticket in Model only narrows its runs, not its check,
	// which has the project's check_network, or with inherit the project's runs' mode.
	if err := save("internet", "sources"); err != nil {
		t.Fatal(err)
	}
	if m, err := settings.NetworkMode(ctx, d.Server, fixture, narrow); err != nil || m != "model_only" {
		t.Fatalf("the narrow ticket's runs %q: %v", m, err)
	}
	check(narrow, "sources")
	check(0, "sources")
	if err := save("internet", "inherit"); err != nil {
		t.Fatal(err)
	}
	check(narrow, "internet")
	if err := save("internet", "lan"); err != nil {
		t.Fatal(err)
	}
	check(narrow, "internet")
	if err := save("sources", "inherit"); err == nil {
		t.Fatal("a project's runs were given Sources")
	}
	if err := save("model_only", "everything"); err == nil {
		t.Fatal("a check network mode that is none was stored")
	}
}
