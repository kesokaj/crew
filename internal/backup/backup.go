// Package backup makes Crew's backups and reads one back (design 15.3, 16.9). A day's backup is one
// tar.gz file: crew.sql, a dump of the database without the events of months that have ended, the
// repositories of projects that have no remote, and the data of hosted projects. Each month that has
// ended is dumped once into a month file beside them, kept for good, and written again when a
// project or a conversation is deleted from it. So a day's file stays small, and 14 of them do not
// hold the same trajectories 14 times. The updater's snapshot (16.4) is a file of the same kind,
// with the database only. The plain dumps of M1, crew-<time>.sql.gz, can still be read back.
package backup

import (
	"archive/tar"
	"bufio"
	"compress/gzip"
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/kesokaj/crew/internal/db"
)

// A day's backup file is named by the time it was made, in UTC, so the names sort by age. Before M14
// it was a plain dump, crew-<time>.sql.gz; the last of those are kept and read back as before.
var (
	fileName = regexp.MustCompile(`^crew-\d{8}T\d{6}Z\.(tar\.gz|sql\.gz)$`)
	// A month file holds the events of one month that has ended.
	monthFile = regexp.MustCompile(`^events-(\d{4})-(\d{2})\.sql\.gz$`)
	partition = regexp.MustCompile(`^events_(\d{4})_(\d{2})$`)
)

// The entries of a backup file. months.txt is first, so a restore knows which month files it needs
// before it reads the dump.
const (
	entryMonths = "months.txt"
	entryDump   = "crew.sql"
	entryRepos  = "repos/"
	entryHosted = "hosted/"
)

// Name returns the file name of a day's backup made at t.
func Name(t time.Time) string { return "crew-" + t.UTC().Format("20060102T150405Z") + ".tar.gz" }

// MonthName is the file of the events of month, as YYYY-MM.
func MonthName(month string) string { return "events-" + month + ".sql.gz" }

// Plan is what a day's backup holds and where it goes.
type Plan struct {
	// Conn is the role that can only read.
	Conn db.Conn
	// Dir is the backups folder: the days' files and the month files.
	Dir  string
	Keep int
	Now  time.Time
	// Repos are the bare repositories of projects with no remote, by name, as folders.
	Repos map[string]string
	// Hosted is <data>/hosted, the hosted projects' folders, whose data/ each is in the backup and
	// never their copies or logs (M19, plan D18); "" leaves them out.
	Hosted string
	// Note hears of a hosted project's file left out because it changed while it was read.
	Note func(string)
}

// Dump makes a day's backup of the database alone into dir, keeping the newest keep. It is Daily with
// no repositories.
func Dump(ctx context.Context, conn db.Conn, dir string, keep int, now time.Time) (string, error) {
	return Daily(ctx, Plan{Conn: conn, Dir: dir, Keep: keep, Now: now})
}

// Daily writes the month files that are missing or out of date, then a day's file with everything
// else, then removes the oldest days' files beyond Keep. A backup that fails leaves no file behind.
// It returns the new file's path.
func Daily(ctx context.Context, p Plan) (string, error) {
	if p.Keep < 1 {
		return "", errors.New("backup: keep must be at least 1")
	}
	if err := os.MkdirAll(p.Dir, 0o700); err != nil {
		return "", err
	}
	months, err := SyncMonths(ctx, p.Conn, p.Dir, p.Now)
	if err != nil {
		return "", err
	}
	final := filepath.Join(p.Dir, Name(p.Now))
	if err := write(ctx, p.Conn, p.Dir, final, p.Dir, months, p.Repos, p.Hosted, p.Note); err != nil {
		return "", err
	}
	return final, prune(p.Dir, p.Keep)
}

// Snapshot writes the updater's snapshot (16.4) into file: the database, without the months whose
// month files in monthsDir are up to date. It writes no month file itself: those are the daily
// job's, and a month without one is in the snapshot.
func Snapshot(ctx context.Context, conn db.Conn, monthsDir, file string) error {
	months, err := currentMonths(ctx, conn, monthsDir, time.Now())
	if err != nil {
		return err
	}
	return write(ctx, conn, filepath.Dir(file), file, monthsDir, months, nil, "", nil)
}

// write dumps the database without months into a temporary file, and writes the backup file from it.
// months.txt names the folder of the month files, so a snapshot kept elsewhere finds them.
func write(ctx context.Context, conn db.Conn, tmpDir, final, monthsDir string, months []string, repos map[string]string, hosted string, note func(string)) error {
	env, err := conn.Env()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(final), 0o700); err != nil {
		return err
	}
	sqlFile, err := os.CreateTemp(tmpDir, ".dump-*.sql")
	if err != nil {
		return err
	}
	defer os.Remove(sqlFile.Name())
	// --clean --if-exists: the dump removes what it makes before it makes it, so it can be read back
	// into a database that has the schema. The partitions of the months in their own files keep their
	// table, empty, and get their rows from the month file.
	args := []string{"--format=plain", "--clean", "--if-exists", "--no-password"}
	for _, m := range months {
		args = append(args, "--exclude-table-data=public.events_"+strings.ReplaceAll(m, "-", "_"))
	}
	if err := pgDump(ctx, env, sqlFile, args...); err != nil {
		sqlFile.Close()
		return err
	}
	if err := sqlFile.Close(); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(final), ".backup-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name()) // a no-op after the rename
	zw := gzip.NewWriter(tmp)
	tw := tar.NewWriter(zw)
	err = func() error {
		list := "dir: " + monthsDir + "\n" + strings.Join(months, "\n") + "\n"
		if err := addBytes(tw, entryMonths, []byte(list)); err != nil {
			return err
		}
		if err := addFile(tw, entryDump, sqlFile.Name()); err != nil {
			return err
		}
		names := make([]string, 0, len(repos))
		for name := range repos {
			names = append(names, name)
		}
		sort.Strings(names)
		for _, name := range names {
			if err := addTree(tw, entryRepos+name+".git", repos[name]); err != nil {
				return fmt.Errorf("the repository %s: %w", name, err)
			}
		}
		if err := tw.WriteHeader(&tar.Header{Name: entryHosted, Typeflag: tar.TypeDir, Mode: 0o700, ModTime: time.Now()}); err != nil {
			return err
		}
		if hosted != "" {
			if note == nil {
				note = func(string) {}
			}
			if err := addHosted(tw, hosted, tmpDir, note); err != nil {
				return fmt.Errorf("the hosted projects' data: %w", err)
			}
		}
		if err := tw.Close(); err != nil {
			return err
		}
		return zw.Close()
	}()
	if err != nil {
		tmp.Close()
		return err
	}
	return finish(tmp, final)
}

// finish syncs and closes a written temporary file and gives it its name, readable by its owner only.
func finish(tmp *os.File, final string) error {
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Chmod(tmp.Name(), 0o600); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), final)
}

func pgDump(ctx context.Context, env []string, out io.Writer, args ...string) error {
	cmd := exec.CommandContext(ctx, "pg_dump", args...)
	cmd.Env = append(os.Environ(), env...)
	cmd.Stdout = out
	var stderr strings.Builder
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("pg_dump: %v: %s", err, strings.TrimSpace(stderr.String()))
	}
	return nil
}

func addBytes(tw *tar.Writer, name string, body []byte) error {
	if err := tw.WriteHeader(&tar.Header{Name: name, Mode: 0o600, Size: int64(len(body)), ModTime: time.Now()}); err != nil {
		return err
	}
	_, err := tw.Write(body)
	return err
}

func addFile(tw *tar.Writer, name, path string) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		return err
	}
	if err := tw.WriteHeader(&tar.Header{Name: name, Mode: 0o600, Size: st.Size(), ModTime: st.ModTime()}); err != nil {
		return err
	}
	_, err = io.Copy(tw, f)
	return err
}

// addTree adds the folder root under the name prefix: its folders and regular files. A link is left
// out: a repository Crew made holds none, and one that someone put there is not followed.
func addTree(tw *tar.Writer, prefix, root string) error {
	return filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		name := prefix
		if rel != "." {
			name = prefix + "/" + filepath.ToSlash(rel)
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		switch {
		case d.IsDir():
			return tw.WriteHeader(&tar.Header{Name: name + "/", Typeflag: tar.TypeDir, Mode: int64(info.Mode().Perm()), ModTime: info.ModTime()})
		case info.Mode().IsRegular():
			hdr := &tar.Header{Name: name, Mode: int64(info.Mode().Perm()), Size: info.Size(), ModTime: info.ModTime()}
			if err := tw.WriteHeader(hdr); err != nil {
				return err
			}
			f, err := os.Open(path)
			if err != nil {
				return err
			}
			defer f.Close()
			_, err = io.CopyN(tw, f, info.Size())
			return err
		}
		return nil
	})
}

// List returns the days' backups in dir, oldest first: the tar.gz files and the plain dumps before
// them.
func List(dir string) ([]string, error) {
	entries, err := os.ReadDir(dir)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var names []string
	for _, e := range entries {
		if e.Type().IsRegular() && fileName.MatchString(e.Name()) {
			names = append(names, filepath.Join(dir, e.Name()))
		}
	}
	// By the time in the name, whichever kind it is.
	sort.Slice(names, func(i, j int) bool { return stamp(names[i]) < stamp(names[j]) })
	return names, nil
}

func stamp(path string) string {
	b := filepath.Base(path)
	return strings.TrimSuffix(strings.TrimSuffix(b, ".tar.gz"), ".sql.gz")
}

// ListMonths returns the month files in dir, as YYYY-MM, oldest first.
func ListMonths(dir string) ([]string, error) {
	entries, err := os.ReadDir(dir)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var months []string
	for _, e := range entries {
		if m := monthFile.FindStringSubmatch(e.Name()); m != nil && e.Type().IsRegular() {
			months = append(months, m[1]+"-"+m[2])
		}
	}
	sort.Strings(months)
	return months, nil
}

func prune(dir string, keep int) error {
	names, err := List(dir)
	if err != nil {
		return err
	}
	for len(names) > keep {
		if err := os.Remove(names[0]); err != nil {
			return err
		}
		names = names[1:]
	}
	return nil
}

// endedMonths returns the partitions of events of months that ended before now, as YYYY-MM, with
// the rows each holds now.
func endedMonths(ctx context.Context, conn db.Conn, now time.Time) (map[string]int64, error) {
	pool, err := db.Open(ctx, conn)
	if err != nil {
		return nil, err
	}
	defer pool.Close()
	rows, err := pool.Query(ctx, `SELECT c.relname FROM pg_inherits i JOIN pg_class c ON c.oid = i.inhrelid
		JOIN pg_class p ON p.oid = i.inhparent WHERE p.relname = 'events' AND p.relnamespace = 'public'::regnamespace`)
	if err != nil {
		return nil, err
	}
	thisMonth := now.UTC().Format("2006-01")
	var names []string
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			rows.Close()
			return nil, err
		}
		if m := partition.FindStringSubmatch(name); m != nil && m[1]+"-"+m[2] < thisMonth {
			names = append(names, m[1]+"-"+m[2])
		}
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	out := map[string]int64{}
	for _, m := range names {
		var n int64
		if err := pool.QueryRow(ctx, `SELECT count(*) FROM public.events_`+strings.ReplaceAll(m, "-", "_")).Scan(&n); err != nil {
			return nil, err
		}
		out[m] = n
	}
	return out, nil
}

// rowsFile holds the number of rows a month file was written with: a month whose partition now holds
// another number lost rows to a deletion, and its file is written again.
func rowsFile(dir, month string) string { return filepath.Join(dir, "events-"+month+".rows") }

func recordedRows(dir, month string) (int64, bool) {
	body, err := os.ReadFile(rowsFile(dir, month))
	if err != nil {
		return 0, false
	}
	n, err := strconv.ParseInt(strings.TrimSpace(string(body)), 10, 64)
	if err != nil {
		return 0, false
	}
	if _, err := os.Stat(filepath.Join(dir, MonthName(month))); err != nil {
		return 0, false
	}
	return n, true
}

// currentMonths returns the ended months whose month file in dir holds the rows of the partition.
func currentMonths(ctx context.Context, conn db.Conn, dir string, now time.Time) ([]string, error) {
	ended, err := endedMonths(ctx, conn, now)
	if err != nil {
		return nil, err
	}
	var out []string
	for m, n := range ended {
		if have, ok := recordedRows(dir, m); ok && have == n {
			out = append(out, m)
		}
	}
	sort.Strings(out)
	return out, nil
}

// SyncMonths writes the month file of each ended month that has none, or whose partition lost rows
// since, and returns the months whose files are up to date. A month whose file cannot be written is
// left out, so a day's file holds its events instead.
func SyncMonths(ctx context.Context, conn db.Conn, dir string, now time.Time) ([]string, error) {
	ended, err := endedMonths(ctx, conn, now)
	if err != nil {
		return nil, err
	}
	env, err := conn.Env()
	if err != nil {
		return nil, err
	}
	var out []string
	for m, n := range ended {
		if have, ok := recordedRows(dir, m); ok && have == n {
			out = append(out, m)
			continue
		}
		if err := writeMonth(ctx, env, dir, m, n); err != nil {
			continue
		}
		out = append(out, m)
	}
	sort.Strings(out)
	return out, nil
}

// writeMonth dumps the rows of one month's partition, and then the number it was written with.
func writeMonth(ctx context.Context, env []string, dir, month string, rows int64) error {
	tmp, err := os.CreateTemp(dir, ".month-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	zw := gzip.NewWriter(tmp)
	if err := pgDump(ctx, env, zw, "--format=plain", "--data-only", "--no-password",
		"--table=public.events_"+strings.ReplaceAll(month, "-", "_")); err != nil {
		tmp.Close()
		return err
	}
	if err := zw.Close(); err != nil {
		tmp.Close()
		return err
	}
	if err := finish(tmp, filepath.Join(dir, MonthName(month))); err != nil {
		return err
	}
	return os.WriteFile(rowsFile(dir, month), []byte(strconv.FormatInt(rows, 10)+"\n"), 0o600)
}

// sqlOnly passes a plain dump through only when psql reads it in its restricted mode from start to
// end: a dump pg_dump wrote opens with \restrict KEY, in which psql refuses every meta-command, also
// one after SQL on the same line, until \unrestrict KEY, which pg_dump writes as its last command
// (16.9). A file that someone else put in the backup folder must not run a shell command as the role
// that owns the schema, so the filter refuses a file that does not open so, a backslash command at
// the start of a line, and \unrestrict anywhere but as the file's last command with its key. Each
// file of a restore, the dump and every month file, has a filter of its own.
type sqlOnly struct {
	r      *bufio.Reader
	inCopy bool
	key    string // the key of \restrict, once read
	closed bool   // \unrestrict KEY was read: only comments may follow
	buf    []byte
	err    error
}

var (
	restrictLine = regexp.MustCompile(`^\\restrict ([A-Za-z0-9]+)$`)
	// unrestrict is psql's \unrestrict where it is a command: after an odd number of backslashes. In
	// COPY data a backslash is written doubled, so data that holds the word does not match.
	unrestrict = regexp.MustCompile(`(^|[^\\])(\\\\)*\\unrestrict`)
)

func (s *sqlOnly) refuse(why string) (int, error) {
	s.err = errors.New("the file " + why + ": not a dump Crew made")
	return 0, s.err
}

func (s *sqlOnly) Read(p []byte) (int, error) {
	for len(s.buf) == 0 {
		if s.err != nil {
			if s.err == io.EOF && s.key != "" && !s.closed {
				return s.refuse("ends before pg_dump's \\unrestrict")
			}
			return 0, s.err
		}
		line, err := s.r.ReadString('\n')
		if err != nil && err != io.EOF {
			return 0, err
		}
		if err == io.EOF {
			s.err = io.EOF
		}
		if line == "" {
			continue
		}
		bare := strings.TrimRight(line, "\r\n")
		trimmed := strings.TrimLeft(bare, " \t")
		comment := trimmed == "" || strings.HasPrefix(trimmed, "--")
		switch {
		case s.closed:
			if !comment {
				return s.refuse("holds more after its \\unrestrict")
			}
		case s.key == "":
			// Before \restrict only pg_dump's opening comments.
			if m := restrictLine.FindStringSubmatch(bare); m != nil {
				s.key = m[1]
			} else if !comment {
				return s.refuse("does not open with pg_dump's \\restrict")
			}
		case bare == "\\unrestrict "+s.key && !s.inCopy:
			s.closed = true
		case unrestrict.MatchString(bare):
			return s.refuse("holds a \\unrestrict before its end")
		case s.inCopy:
			if bare == "\\." {
				s.inCopy = false
			}
		case strings.HasPrefix(bare, "COPY ") && strings.HasSuffix(bare, "FROM stdin;"):
			s.inCopy = true
		case strings.HasPrefix(trimmed, "\\"):
			return s.refuse("holds a psql meta-command outside its data")
		}
		s.buf = []byte(line)
	}
	n := copy(p, s.buf)
	s.buf = s.buf[n:]
	return n, nil
}

// Options say what Restore reads besides the dump.
type Options struct {
	// NoMonths leaves the month files out: the scratch copy the updater tries a release on does not
	// need the trajectories of ended months (16.4).
	NoMonths bool
	// MonthsDir is where the month files are; by default the folder the backup names, else the
	// folder of the file.
	MonthsDir string
	// MonthsRoot, when set, is the folder the month files must be under, whatever a backup names:
	// a file's own words never choose what else a restore reads.
	MonthsRoot string
	// Canonical is the folder of the repositories: a repository in the backup that is not there is
	// put back. "" leaves them out.
	Canonical string
	// Hosted is <data>/hosted: a hosted project's data in the backup is put back where its data/ is
	// absent or empty (plan M19 D18). "" leaves it out.
	Hosted string
}

// Restore reads a backup back into the database as the role that owns the schema, in one
// transaction with the month files it names: a restore that fails changes nothing. It ends with
// crew_apply_grants, which puts back the privileges a dump does not carry. Then it puts back the
// repositories that are missing.
func Restore(ctx context.Context, conn db.Conn, file string) error {
	return RestoreWith(ctx, conn, file, Options{})
}

// RestoreWith is Restore with options.
func RestoreWith(ctx context.Context, conn db.Conn, file string, opt Options) error {
	env, err := conn.Env()
	if err != nil {
		return err
	}
	f, err := os.Open(file)
	if err != nil {
		return err
	}
	defer f.Close()
	zr, err := gzip.NewReader(f)
	if err != nil {
		return fmt.Errorf("%s: not a backup: %w", file, err)
	}
	var dump io.Reader
	var months []string
	if strings.HasSuffix(file, ".sql.gz") {
		dump = zr
	} else {
		tr := tar.NewReader(zr)
		var named string
		if named, months, err = readMonths(tr); err != nil {
			return fmt.Errorf("%s: %w", file, err)
		}
		if opt.MonthsDir == "" {
			opt.MonthsDir = named
		}
		hdr, err := tr.Next()
		if err != nil || hdr.Name != entryDump {
			return fmt.Errorf("%s: not a backup Crew made: no %s after %s", file, entryDump, entryMonths)
		}
		dump = tr
	}
	if opt.MonthsDir == "" {
		opt.MonthsDir = filepath.Dir(file)
	}
	if opt.MonthsRoot != "" && len(months) > 0 && !opt.NoMonths {
		root, dir := filepath.Clean(opt.MonthsRoot), filepath.Clean(opt.MonthsDir)
		if dir != root && !strings.HasPrefix(dir, root+string(filepath.Separator)) {
			return fmt.Errorf("%s names its month files in %s, which is not under %s", file, opt.MonthsDir, opt.MonthsRoot)
		}
	}
	parts := []io.Reader{&sqlOnly{r: bufio.NewReader(dump)}}
	if !opt.NoMonths {
		for _, m := range months {
			mf, err := os.Open(filepath.Join(opt.MonthsDir, MonthName(m)))
			if err != nil {
				return fmt.Errorf("the backup needs the month file of %s: %w", m, err)
			}
			defer mf.Close()
			mz, err := gzip.NewReader(mf)
			if err != nil {
				return fmt.Errorf("the month file of %s: %w", m, err)
			}
			parts = append(parts, strings.NewReader("\n"), &sqlOnly{r: bufio.NewReader(mz)})
		}
	}
	// After a restore every personal token is ended (16.11): one taken away after the backup is not
	// given back by it. The sessions are the ingress's, signed with its key (20.3).
	parts = append(parts, strings.NewReader("\nSELECT public.crew_apply_grants();\n"+
		"UPDATE public.personal_tokens SET revoked_at = now() WHERE revoked_at IS NULL;\n"))
	cmd := exec.CommandContext(ctx, "psql", "--no-password", "--quiet", "--single-transaction",
		"-v", "ON_ERROR_STOP=1", "--file=-")
	cmd.Env = append(os.Environ(), env...)
	cmd.Stdin = io.MultiReader(parts...)
	cmd.Stdout = io.Discard
	var stderr strings.Builder
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("psql: %v: %s", err, strings.TrimSpace(stderr.String()))
	}
	if opt.Canonical != "" && strings.HasSuffix(file, ".tar.gz") {
		if err := restoreRepos(file, opt.Canonical); err != nil {
			return err
		}
	}
	if opt.Hosted != "" && strings.HasSuffix(file, ".tar.gz") {
		return restoreHosted(file, opt.Hosted)
	}
	return nil
}

// readMonths reads months.txt: the folder of the month files, and the months the dump leaves to them.
func readMonths(tr *tar.Reader) (string, []string, error) {
	hdr, err := tr.Next()
	if err != nil || hdr.Name != entryMonths {
		return "", nil, errors.New("not a backup Crew made: it does not start with " + entryMonths)
	}
	body, err := io.ReadAll(io.LimitReader(tr, 1<<16))
	if err != nil {
		return "", nil, err
	}
	var dir string
	var months []string
	for _, l := range strings.Split(string(body), "\n") {
		l = strings.TrimSpace(l)
		switch {
		case l == "":
		case strings.HasPrefix(l, "dir: "):
			dir = strings.TrimPrefix(l, "dir: ")
		case monthLine.MatchString(l):
			months = append(months, l)
		default:
			return "", nil, fmt.Errorf("%s names %q, which is not a month", entryMonths, l)
		}
	}
	return dir, months, nil
}

var monthLine = regexp.MustCompile(`^\d{4}-\d{2}$`)

// restoreRepos puts back each repository of the backup that is not in canonical. One that is there is
// never replaced.
func restoreRepos(file, canonical string) error {
	f, err := os.Open(file)
	if err != nil {
		return err
	}
	defer f.Close()
	zr, err := gzip.NewReader(f)
	if err != nil {
		return err
	}
	tr := tar.NewReader(zr)
	skip := map[string]bool{}
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return err
		}
		rel, ok := strings.CutPrefix(hdr.Name, entryRepos)
		if !ok || rel == "" {
			continue
		}
		clean := filepath.Clean(filepath.FromSlash(rel))
		if filepath.IsAbs(clean) || clean == "." || strings.HasPrefix(clean, "..") {
			return fmt.Errorf("the backup names %q, which is not in its folder", hdr.Name)
		}
		repo := strings.SplitN(filepath.ToSlash(clean), "/", 2)[0]
		if !strings.HasSuffix(repo, ".git") {
			return fmt.Errorf("the backup names %q, which is not a repository", hdr.Name)
		}
		if _, seen := skip[repo]; !seen {
			_, err := os.Stat(filepath.Join(canonical, repo))
			skip[repo] = err == nil
		}
		if skip[repo] {
			continue
		}
		dst := filepath.Join(canonical, clean)
		switch hdr.Typeflag {
		case tar.TypeDir:
			if err := os.MkdirAll(dst, 0o755); err != nil {
				return err
			}
		case tar.TypeReg:
			if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
				return err
			}
			out, err := os.OpenFile(dst, os.O_CREATE|os.O_EXCL|os.O_WRONLY, os.FileMode(hdr.Mode)&0o755)
			if err != nil {
				return err
			}
			if _, err := io.CopyN(out, tr, hdr.Size); err != nil {
				out.Close()
				return err
			}
			if err := out.Close(); err != nil {
				return err
			}
		}
	}
}

// projectDir is a hosted project's folder's name: its id.
var projectDir = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)

// addHosted adds each hosted project's data folder as hosted/<project id>/data/…, never its copies or
// its logs (plan M19 D18). The app writes while it is read: a file that went, or whose size changed
// while it was copied, is left out and noted, and the backup goes on. A link is left out.
func addHosted(tw *tar.Writer, hosted, tmpDir string, note func(string)) error {
	entries, err := os.ReadDir(hosted)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	for _, e := range entries {
		if !e.IsDir() || !projectDir.MatchString(e.Name()) {
			continue
		}
		data := filepath.Join(hosted, e.Name(), "data")
		if fi, err := os.Lstat(data); err != nil || !fi.IsDir() {
			continue
		}
		if err := addTreeTolerant(tw, entryHosted+e.Name()+"/data", data, tmpDir, note); err != nil {
			return fmt.Errorf("the data of %s: %w", e.Name(), err)
		}
	}
	return nil
}

// addTreeTolerant is addTree of a folder something writes while it is read, the app of a hosted
// project: through a root at the folder, so a folder swapped for a link while it is walked leads
// nowhere out of it, and each file opened without following a link (security review of M19). Each
// file is copied aside first, and one that went, did not keep its size, or finds no room on the disk
// is left out with a note. A folder that cannot be read, its own root included, is left out with a
// note too: the app owns it, and the backup of everything else does not wait on it (round 2).
func addTreeTolerant(tw *tar.Writer, prefix, folder, tmpDir string, note func(string)) error {
	root, err := os.OpenRoot(folder)
	if err != nil {
		note("left out " + folder + ": " + err.Error())
		return nil
	}
	defer root.Close()
	return fs.WalkDir(root.FS(), ".", func(rel string, d fs.DirEntry, err error) error {
		if err != nil {
			note("left out " + filepath.Join(folder, rel) + ": " + err.Error())
			if d != nil && d.IsDir() {
				return fs.SkipDir
			}
			return nil
		}
		walked(folder, rel)
		name := prefix
		if rel != "." {
			name = prefix + "/" + rel
		}
		switch {
		case d.IsDir():
			info, err := d.Info()
			if err != nil {
				note("left out " + filepath.Join(folder, rel) + ": " + err.Error())
				return fs.SkipDir
			}
			return tw.WriteHeader(&tar.Header{Name: name + "/", Typeflag: tar.TypeDir, Mode: int64(info.Mode().Perm()), ModTime: info.ModTime()})
		case d.Type().IsRegular():
			in, err := root.OpenFile(rel, os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
			if err != nil {
				note("left out " + filepath.Join(folder, rel) + ": " + err.Error())
				return nil
			}
			defer in.Close()
			info, err := in.Stat()
			if err != nil || !info.Mode().IsRegular() {
				note("left out " + filepath.Join(folder, rel) + ": it is no plain file")
				return nil
			}
			aside, ok, err := copyAside(in, info.Size(), tmpDir)
			if err != nil {
				return err
			}
			if !ok {
				note("left out " + filepath.Join(folder, rel) + ": it changed while the backup read it, or the disk has no room for it")
				return nil
			}
			defer os.Remove(aside.Name())
			defer aside.Close()
			if err := tw.WriteHeader(&tar.Header{Name: name, Mode: int64(info.Mode().Perm()), Size: info.Size(), ModTime: info.ModTime()}); err != nil {
				return err
			}
			_, err = io.CopyN(tw, aside, info.Size())
			return err
		}
		return nil
	})
}

// walked is called with each entry the walk of a hosted folder takes, before it is read; a test plays
// the app's writes there.
var walked = func(folder, rel string) {}

// hostedMargin is what the disk keeps free while a hosted project's file is copied aside: the disk
// holds the database too (security review of M19).
var hostedMargin uint64 = 10 << 30

// freeSpace is the room on the disk of a folder; a test stands in for it.
var freeSpace = func(dir string) (uint64, error) {
	var st syscall.Statfs_t
	if err := syscall.Statfs(dir, &st); err != nil {
		return 0, err
	}
	return st.Bavail * uint64(st.Bsize), nil
}

// copyAside copies an open file of size bytes into a temporary file, opened at its start; ok is false
// when its length was not size, or the disk has no room for it twice beside hostedMargin: once aside
// and once in the archive, which is on the same disk (security review of M19, round 2).
func copyAside(in *os.File, size int64, tmpDir string) (*os.File, bool, error) {
	if free, err := freeSpace(tmpDir); err != nil || free < 2*uint64(size)+hostedMargin {
		return nil, false, err
	}
	tmp, err := os.CreateTemp(tmpDir, ".hosted-*")
	if err != nil {
		return nil, false, err
	}
	n, err := io.Copy(tmp, io.LimitReader(in, size+1))
	if err == nil && n == size {
		_, err = tmp.Seek(0, io.SeekStart)
		if err == nil {
			return tmp, true, nil
		}
	}
	tmp.Close()
	os.Remove(tmp.Name())
	return nil, false, err
}

// restoring is called when the restore has looked at a project's data/ and before it writes there; a
// test plays the app's writes there.
var restoring = func(data string) {}

// restoreHosted puts back each hosted project's data from the backup where its data/ is absent or
// empty, through a root at hosted, as the files of whoever restores; a project's data that has files
// is never touched, and an entry that is not a folder or a plain file is left out.
func restoreHosted(file, hosted string) error {
	f, err := os.Open(file)
	if err != nil {
		return err
	}
	defer f.Close()
	zr, err := gzip.NewReader(f)
	if err != nil {
		return err
	}
	// The folder is made and opened at the first project's data the backup holds: a backup with none,
	// such as the updater's snapshot, writes nothing, also where nothing can be written (BUG-006).
	var root *os.Root
	defer func() {
		if root != nil {
			root.Close()
		}
	}()
	tr := tar.NewReader(zr)
	take := map[string]bool{}
	roots := map[string]*os.Root{}
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return err
		}
		rel, ok := strings.CutPrefix(hdr.Name, entryHosted)
		if !ok || rel == "" {
			continue
		}
		parts := strings.SplitN(strings.TrimSuffix(rel, "/"), "/", 3)
		if len(parts) < 2 || !projectDir.MatchString(parts[0]) || parts[1] != "data" ||
			path.Clean(rel) != strings.TrimSuffix(rel, "/") || slices.Contains(strings.Split(rel, "/"), "..") {
			return fmt.Errorf("the backup names %q, which is not a hosted project's data", hdr.Name)
		}
		project := parts[0]
		if root == nil {
			if err := os.MkdirAll(hosted, 0o750); err != nil {
				return err
			}
			if root, err = os.OpenRoot(hosted); err != nil {
				return err
			}
		}
		if _, seen := take[project]; !seen {
			take[project] = emptyOrAbsent(root, project+"/data")
			if take[project] {
				// The project's own data/ is the root its files are written through: a link the app put
				// there after the look leads nowhere out of it (security review of M19).
				if err := root.MkdirAll(project+"/data", 0o750); err != nil {
					return err
				}
				if fi, err := root.Lstat(project + "/data"); err != nil || !fi.IsDir() {
					return fmt.Errorf("the data folder of %s is no folder", project)
				}
				r, err := root.OpenRoot(project + "/data")
				if err != nil {
					return err
				}
				defer r.Close()
				roots[project] = r
				restoring(filepath.Join(hosted, project, "data"))
			}
		}
		if !take[project] {
			continue
		}
		into := roots[project]
		dst := strings.TrimPrefix(strings.TrimSuffix(rel, "/"), project+"/data")
		dst = strings.TrimPrefix(dst, "/")
		if dst == "" {
			continue
		}
		switch hdr.Typeflag {
		case tar.TypeDir:
			if err := into.MkdirAll(dst, 0o750); err != nil {
				return err
			}
		case tar.TypeReg:
			if err := into.MkdirAll(path.Dir(dst), 0o750); err != nil {
				return err
			}
			out, err := into.OpenFile(dst, os.O_CREATE|os.O_EXCL|os.O_WRONLY, os.FileMode(hdr.Mode)&0o750)
			if err != nil {
				return err
			}
			if _, err := io.CopyN(out, tr, hdr.Size); err != nil {
				out.Close()
				return err
			}
			if err := out.Close(); err != nil {
				return err
			}
		}
	}
}

// emptyOrAbsent says whether a folder under root is not there, or holds nothing.
func emptyOrAbsent(root *os.Root, dir string) bool {
	d, err := root.Open(dir)
	if errors.Is(err, fs.ErrNotExist) {
		return true
	}
	if err != nil {
		return false
	}
	defer d.Close()
	names, err := d.Readdirnames(1)
	return err == io.EOF && len(names) == 0
}
