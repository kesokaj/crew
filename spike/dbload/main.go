// Command dbload measures the database for spike items 44 and 46 (design section 24), against a
// throwaway PostgreSQL that spike/dbload.sh starts with crew-db's image and limits. It never touches
// Crew's own database.
//
//	dbload migrate                 apply Crew's schema, as crew_migrate
//	dbload load WRITERS DURATION   item 46: WRITERS runs of the real run driver, on FakeHarness,
//	                               replay the recordings of testdata/ as fast as the database stores them
//	dbload fill MEGABYTES          add synthetic events, made from the recordings, until the events
//	                               table holds MEGABYTES
//	dbload dumprestore DIR         item 44: Crew's own dump (backup.Dump) of the database into DIR, and
//	                               its restore (backup.Restore) into a second database, timed
//	dbload size                    the size of the database, of the events, and the events' count
package main

import (
	"bufio"
	"bytes"
	"context"
	"crypto/rand"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/kesokaj/crew/internal/backup"
	"github.com/kesokaj/crew/internal/cli"
	"github.com/kesokaj/crew/internal/db"
	"github.com/kesokaj/crew/internal/harness"
	"github.com/kesokaj/crew/internal/runs"
)

func main() {
	if err := run(context.Background(), os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "dbload:", err)
		os.Exit(1)
	}
}

func run(ctx context.Context, args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("usage: dbload migrate | load WRITERS DURATION | fill MEGABYTES | dumprestore DIR | size")
	}
	switch args[0] {
	case "migrate":
		dsn, err := db.ConnFromEnv(db.RoleMigrate).URL()
		if err != nil {
			return err
		}
		conn, err := pgx.Connect(ctx, dsn)
		if err != nil {
			return err
		}
		defer conn.Close(ctx)
		applied, err := db.Migrate(ctx, conn)
		fmt.Println("applied", applied)
		return err
	case "load":
		if len(args) != 3 {
			return fmt.Errorf("load WRITERS DURATION")
		}
		n, _ := strconv.Atoi(args[1])
		d, err := time.ParseDuration(args[2])
		if err != nil {
			return err
		}
		return load(ctx, n, d)
	case "fill":
		mb, _ := strconv.Atoi(args[1])
		return fill(ctx, int64(mb)<<20)
	case "dumprestore":
		return dumpRestore(ctx, args[1])
	case "size":
		pool, err := db.Open(ctx, db.ConnFromEnv(db.RoleServer))
		if err != nil {
			return err
		}
		defer pool.Close()
		return size(ctx, pool)
	}
	return fmt.Errorf("unknown command %q", args[0])
}

type line struct {
	cli  string
	text []byte
}

// corpus is every line of every recording, with the CLI that printed it.
func corpus() ([]line, error) {
	var out []line
	for _, name := range cli.Names() {
		files, _ := filepath.Glob(filepath.Join("testdata", name, "*.jsonl"))
		sort.Strings(files)
		for _, f := range files {
			b, err := os.ReadFile(f)
			if err != nil {
				return nil, err
			}
			sc := bufio.NewScanner(bytes.NewReader(b))
			sc.Buffer(make([]byte, 1<<20), 16<<20)
			for sc.Scan() {
				if len(bytes.TrimSpace(sc.Bytes())) > 0 {
					out = append(out, line{name, append([]byte(nil), sc.Bytes()...)})
				}
			}
		}
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("no recordings under testdata/: run from the repository's root")
	}
	return out, nil
}

// load starts n runs of the real driver on FakeHarness, each replaying the recordings of its CLI
// without a pause, and stops them after d. The database's memory is sampled from outside.
func load(ctx context.Context, n int, d time.Duration) error {
	pool, err := db.Open(ctx, db.ConnFromEnv(db.RoleServer))
	if err != nil {
		return err
	}
	defer pool.Close()
	lines, err := corpus()
	if err != nil {
		return err
	}
	byCLI := map[string][][]byte{}
	for _, l := range lines {
		byCLI[l.cli] = append(byCLI[l.cli], l.text)
	}
	for _, c := range cli.Names() {
		pool.Exec(ctx, `INSERT INTO cli_logins (cli, state, checked_at) VALUES ($1, 'signed_in', now())
			ON CONFLICT (cli) DO UPDATE SET state = 'signed_in'`, c)
	}
	f := &harness.Fake{
		Next: func(spec harness.RunSpec, turn int) harness.Script {
			// The recordings, over and over: far more than d can store.
			var ls [][]byte
			for i := 0; i < 2000; i++ {
				ls = append(ls, byCLI[spec.CLI]...)
			}
			return harness.Script{Lines: ls, Hold: true}
		},
		// The driver records each run's sandbox, so each needs its row, as sandboxes.Manager makes it.
		MakeSandbox: func(id string) error {
			_, err := pool.Exec(ctx, `INSERT INTO sandboxes (id, kind, container, image) VALUES ($1, 'run', $1, 'dbload') ON CONFLICT DO NOTHING`, id)
			return err
		},
	}
	drv := &runs.Driver{Pool: pool, Harness: f, Version: "dbload", Image: "dbload", Log: slog.New(slog.NewTextHandler(io.Discard, nil))}
	before := count(ctx, pool)
	start := time.Now()
	var ids []string
	for i := 0; i < n; i++ {
		c := cli.Names()[i%len(cli.Names())]
		id, err := drv.Start(ctx, runs.StartRequest{CLI: c, Prompt: "dbload", MaxTurns: 1 << 30})
		if err != nil {
			return err
		}
		ids = append(ids, id)
	}
	fmt.Printf("writers %d, for %s, started %s\n", n, d, start.UTC().Format(time.RFC3339))
	tick := time.NewTicker(10 * time.Second)
	defer tick.Stop()
	end := time.After(d)
	last, lastAt := before, start
loop:
	for {
		select {
		case <-tick.C:
			c := count(ctx, pool)
			fmt.Printf("%s events %d, %.0f a second in the last 10 s\n", time.Now().UTC().Format("15:04:05"), c-before,
				float64(c-last)/time.Since(lastAt).Seconds())
			last, lastAt = c, time.Now()
		case <-end:
			break loop
		}
	}
	stored := count(ctx, pool) - before
	elapsed := time.Since(start)
	for _, id := range ids {
		drv.Stop(ctx, id)
	}
	time.Sleep(2 * time.Second)
	drv.Close()
	fmt.Printf("stored %d events in %s: %.0f a second, %.1f a second per writer\n", stored, elapsed.Round(time.Second),
		float64(stored)/elapsed.Seconds(), float64(stored)/elapsed.Seconds()/float64(n))
	return size(ctx, pool)
}

func count(ctx context.Context, pool *pgxpool.Pool) int64 {
	var n int64
	pool.QueryRow(ctx, `SELECT count(*) FROM events`).Scan(&n)
	return n
}

func size(ctx context.Context, pool *pgxpool.Pool) error {
	var dbBytes, evBytes, n, rawBytes int64
	if err := pool.QueryRow(ctx, `SELECT pg_database_size(current_database()),
		(SELECT coalesce(sum(pg_total_relation_size(c.oid)), 0) FROM pg_inherits i JOIN pg_class c ON c.oid = i.inhrelid
			WHERE i.inhparent = 'events'::regclass),
		(SELECT count(*) FROM events), (SELECT coalesce(sum(length(raw)), 0) FROM events)`).Scan(&dbBytes, &evBytes, &n, &rawBytes); err != nil {
		return err
	}
	fmt.Printf("database %d MB, events %d MB with their indexes, %d events, %d MB of original lines; %.0f bytes on disk per event, %.2f bytes on disk per byte of line\n",
		dbBytes>>20, evBytes>>20, n, rawBytes>>20, float64(evBytes)/float64(max(n, 1)), float64(evBytes)/float64(max(rawBytes, 1)))
	return nil
}

// fill adds events until the events take target bytes: rows made from the recordings, with the
// payload the adapter gives each line, in runs of 2,000 events, through COPY.
func fill(ctx context.Context, target int64) error {
	pool, err := db.Open(ctx, db.ConnFromEnv(db.RoleServer))
	if err != nil {
		return err
	}
	defer pool.Close()
	lines, err := corpus()
	if err != nil {
		return err
	}
	type row struct {
		typ     string
		payload []byte
		raw     string
	}
	var rows []row
	for _, l := range lines {
		a, _ := cli.Get(l.cli)
		e := a.Parse(l.text)
		p, _ := jsonMarshal(e.Payload)
		rows = append(rows, row{e.Type, p, string(l.text)})
	}
	var evBytes int64
	start := time.Now()
	for i := 0; ; i++ {
		if i%20 == 0 {
			pool.QueryRow(ctx, `SELECT coalesce(sum(pg_total_relation_size(c.oid)), 0) FROM pg_inherits i JOIN pg_class c ON c.oid = i.inhrelid
				WHERE i.inhparent = 'events'::regclass`).Scan(&evBytes)
			fmt.Printf("%s events %d MB\n", time.Since(start).Round(time.Second), evBytes>>20)
			if evBytes >= target {
				break
			}
		}
		run := newUUID()
		var conv string
		err := pgx.BeginFunc(ctx, pool, func(tx pgx.Tx) error {
			var sub int64
			if err := tx.QueryRow(ctx, `INSERT INTO subtasks (scope, project, number, kind, role, title)
				VALUES ('project', $1, $2, 'run', 'builder', 'dbload') RETURNING id`, runs.FixtureProject, i+1).Scan(&sub); err != nil {
				return err
			}
			conv = newUUID()
			if _, err := tx.Exec(ctx, `INSERT INTO conversations (id, purpose, cli) VALUES ($1, 'run', 'claude')`, conv); err != nil {
				return err
			}
			_, err := tx.Exec(ctx, `INSERT INTO runs (id, subtask, number, state, cli, crew_version, image, conversation)
				VALUES ($1, $2, 1, 'ended', 'claude', 'dbload', 'dbload', $3)`, run, sub, conv)
			return err
		})
		if err != nil {
			return err
		}
		at := time.Now().UTC()
		src := make([][]any, 0, 2000)
		for j := 0; j < 2000; j++ {
			r := rows[(i*2000+j)%len(rows)]
			src = append(src, []any{run, conv, int64(j + 1), at.Add(time.Duration(j) * time.Millisecond), r.typ, r.payload, r.raw})
		}
		if _, err := pool.CopyFrom(ctx, pgx.Identifier{"events"}, []string{"run", "conversation", "seq", "at", "type", "payload", "raw"},
			pgx.CopyFromRows(src)); err != nil {
			return err
		}
	}
	return size(ctx, pool)
}

// dumpRestore makes Crew's own dump of the database, and reads it back into a second database, as an
// update's step 3 and a restore do (16.4, 16.9).
func dumpRestore(ctx context.Context, dir string) error {
	start := time.Now()
	file, err := backup.Dump(ctx, db.ConnFromEnv(db.RoleRead), dir, 2, time.Now())
	if err != nil {
		return err
	}
	dumped := time.Since(start)
	fi, _ := os.Stat(file)
	fmt.Printf("dump: %s, %d MB compressed\n", dumped.Round(time.Second), fi.Size()>>20)
	target := db.ConnFromEnv(db.RoleMigrate)
	target.Name = os.Getenv("DBLOAD_RESTORE_DB")
	if target.Name == "" {
		return fmt.Errorf("DBLOAD_RESTORE_DB names the database to restore into")
	}
	start = time.Now()
	if err := backup.Restore(ctx, target, file); err != nil {
		return err
	}
	fmt.Printf("restore: %s\n", time.Since(start).Round(time.Second))
	return nil
}

func jsonMarshal(v any) ([]byte, error) { return json.Marshal(v) }

func newUUID() string {
	var b [16]byte
	rand.Read(b[:])
	b[6] = b[6]&0x0f | 0x40
	b[8] = b[8]&0x3f | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16])
}
