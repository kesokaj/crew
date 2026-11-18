package versions

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/kesokaj/crew/internal/capacity"
	"github.com/kesokaj/crew/internal/cli"
	"github.com/kesokaj/crew/internal/gitstore"
	"github.com/kesokaj/crew/internal/harness"
	"github.com/kesokaj/crew/internal/orchestrator"
	"github.com/kesokaj/crew/internal/planning"
	"github.com/kesokaj/crew/internal/release"
	"github.com/kesokaj/crew/internal/updater"
)

// The build of a release (16.2 step 4, since 0.51). The server exports a commit of Crew's project,
// builds it in a check sandbox with the running release's own deploy/release.sh, and hands the image
// files to the updater, which loads them and writes the release folder. What the running release
// decides by, the files of the commit, the runtime image's inputs, the schema, and the deploy
// archive, is read here from the commit itself: nothing of it comes from the candidate's program.

// BuildGit is what a build reads of Crew's project's canonical repository.
type BuildGit interface {
	Archive(ctx context.Context, name, commit string, w io.Writer) error
	LsTree(ctx context.Context, name, commit string) (string, error)
	FileAt(ctx context.Context, name, commit, path string) ([]byte, error)
	Log(ctx context.Context, name, what, since string, limit int) ([]gitstore.Commit, error)
	// Main, FirstParents, and Follows are what choosing the commit reads (16.2 step 3).
	Main(ctx context.Context, name string) string
	FirstParents(ctx context.Context, name, from, to string) ([]string, error)
	Follows(ctx context.Context, name, a, b string) bool
	// IsAncestor is Follows with the error of a history it cannot read: what holds a release reads it
	// (16.3, since 0.55).
	IsAncestor(ctx context.Context, name, a, b string) (bool, error)
	// FirstParentLog is what a release holds reads (16.2 step 6, since 0.54).
	FirstParentLog(ctx context.Context, name, from, to string, limit int) ([]gitstore.Commit, error)
}

// Loader is the updater's load (16.11).
type Loader interface {
	Load(ctx context.Context, a updater.LoadAsk) (*updater.Loaded, error)
}

// Builder builds releases of Crew, one at a time.
type Builder struct {
	Pool    *pgxpool.Pool
	Git     BuildGit
	Sandbox harness.Builder
	Loader  Loader
	// Releases is the folder of release folders, which the server reads only; Builds is its own,
	// where a build's files wait for the updater.
	Releases string
	Builds   string
	// Running is this server's version, whose deploy archive holds the script that builds.
	Running string
	// Room is the capacity check every sandbox passes (13.10); nil is none, in a test.
	Room *capacity.Gate
	// Gates is the updater's gate, and Live the gate's live steps on this server; nil gates nothing
	// (16.2 step 5).
	Gates   GateUpdater
	Live    LiveSteps
	Timeout time.Duration
	// MaxTree is the largest tree a build exports; 0 is what a sandbox takes, harness.MaxTree.
	// LoadWait is how often a build asks a busy updater again; 0 is 30 seconds.
	MaxTree  int64
	LoadWait time.Duration
	Now      func() time.Time
	Log      *slog.Logger
	// AgyClient downloads agy's tarball; nil is a client of its own.
	AgyClient *http.Client

	wg sync.WaitGroup
	// held is what releases with a folder hold, read once (Changes).
	heldMu sync.Mutex
	held   map[string]*Changes
}

// The states of a build.
const (
	BuildRunning = "running"
	BuildDone    = "done"
	BuildFailed  = "failed"
)

// Build is one build as the database has it; Log is the end of its output.
type Build struct {
	ID        int64      `json:"id"`
	Version   string     `json:"version"`
	Commit    string     `json:"commit"`
	AskedBy   string     `json:"askedBy"`
	State     string     `json:"state"`
	Log       string     `json:"log"`
	Error     string     `json:"error,omitempty"`
	StartedAt time.Time  `json:"startedAt"`
	EndedAt   *time.Time `json:"endedAt,omitempty"`
}

// ErrBuildRuns is the answer while another build runs: one at a time (16.2 step 3). ErrBuiltOnce is the
// answer for a version that is on the machine, or that a build made.
var (
	ErrBuildRuns = errors.New("a build of a release runs already")
	ErrBuiltOnce = errors.New("a version is built once")
)

// maxBuildLog is how much of a build's output is kept, its end.
const maxBuildLog = 64 << 10

// Start records a build of commit of the project as version and runs it in the background. A version
// that is on the machine, or that a build made or makes, is refused, and so is a second build while
// one runs.
func (b *Builder) Start(ctx context.Context, project, version, commit, askedBy string) (*Build, error) {
	if !releaseNumber.MatchString(version) {
		return nil, fmt.Errorf("%q is not X.Y.Z", version)
	}
	if !gitstore.ValidCommit(commit) {
		return nil, fmt.Errorf("%q is not a commit", commit)
	}
	if _, err := os.Lstat(filepath.Join(b.Releases, version)); err == nil {
		return nil, fmt.Errorf("the release %s is on this machine already: %w", version, ErrBuiltOnce)
	}
	bd := &Build{Version: version, Commit: commit, AskedBy: askedBy, State: BuildRunning}
	err := b.Pool.QueryRow(ctx, `INSERT INTO builds (version, commit, asked_by) VALUES ($1, $2, $3) RETURNING id, started_at`,
		version, commit, askedBy).Scan(&bd.ID, &bd.StartedAt)
	var pe *pgconn.PgError
	if errors.As(err, &pe) && pe.Code == "23505" {
		if pe.ConstraintName == "builds_one_running" {
			return nil, ErrBuildRuns
		}
		return nil, fmt.Errorf("%s is built already: %w", version, ErrBuiltOnce)
	}
	if err != nil {
		return nil, err
	}
	b.wg.Add(1)
	go func() {
		defer b.wg.Done()
		b.run(context.WithoutCancel(ctx), bd, project)
	}()
	return bd, nil
}

// Wait waits for the builds that run.
func (b *Builder) Wait() { b.wg.Wait() }

// Recover ends the builds a stop of the server cut off: their sandbox went with the server, and their
// files are removed.
func (b *Builder) Recover(ctx context.Context) error {
	if _, err := b.Pool.Exec(ctx, `UPDATE builds SET state = 'failed', error = 'the server stopped while it ran', ended_at = now()
		WHERE state = 'running'`); err != nil {
		return err
	}
	entries, err := os.ReadDir(b.Builds)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), "b") {
			_ = os.RemoveAll(filepath.Join(b.Builds, e.Name()))
		}
	}
	return nil
}

// Get is the newest build of a version, or nil.
func (b *Builder) Get(ctx context.Context, version string) (*Build, error) {
	var bd Build
	var why *string
	err := b.Pool.QueryRow(ctx, `SELECT id, version, commit, asked_by, state, log, error, started_at, ended_at FROM builds
		WHERE version = $1 ORDER BY id DESC LIMIT 1`, version).Scan(&bd.ID, &bd.Version, &bd.Commit, &bd.AskedBy, &bd.State,
		&bd.Log, &why, &bd.StartedAt, &bd.EndedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if why != nil {
		bd.Error = *why
	}
	return &bd, err
}

func (b *Builder) run(ctx context.Context, bd *Build, project string) {
	out := filepath.Join(b.Builds, fmt.Sprintf("b%d", bd.ID))
	defer os.RemoveAll(out)
	log, err := b.build(ctx, bd, project, out)
	state, why := BuildDone, ""
	if err != nil {
		state, why = BuildFailed, err.Error()
		if b.Log != nil {
			b.Log.Warn("a build of a release failed", "version", bd.Version, "error", err)
		}
	}
	if _, err := b.Pool.Exec(ctx, `UPDATE builds SET state = $2, log = $3, error = nullif($4, ''), ended_at = now() WHERE id = $1`,
		bd.ID, state, storable(log), storable(why)); err != nil && b.Log != nil {
		b.Log.Error("recording the end of a build", "build", bd.ID, "error", err)
	}
}

// build is the build itself; it returns the end of the sandbox's output.
func (b *Builder) build(ctx context.Context, bd *Build, project, out string) (string, error) {
	script, err := b.script()
	if err != nil {
		return "", err
	}
	// The commit is read into memory twice, as its tar and as its files: it is cut off at the size a
	// sandbox takes, before the second (review of M16 P3).
	var tree bytes.Buffer
	limit := b.MaxTree
	if limit <= 0 {
		limit = harness.MaxTree
	}
	if err := b.Git.Archive(ctx, project, bd.Commit, &capped{w: &tree, left: limit, limit: limit}); err != nil {
		return "", fmt.Errorf("exporting %s: %w", abbrev(bd.Commit, 12), err)
	}
	listed, err := b.Git.LsTree(ctx, project, bd.Commit)
	if err != nil {
		return "", fmt.Errorf("the files of %s: %w", abbrev(bd.Commit, 12), err)
	}
	facts, err := ReadCommit(tree.Bytes(), listed, func(p string) ([]byte, error) { return b.Git.FileAt(ctx, project, bd.Commit, p) })
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(b.Builds, 0o750); err != nil {
		return "", err
	}
	// agy's tarball as the commit pins it, downloaded here and handed to the build, whose sandbox
	// reaches no storage.googleapis.com (12.8, 13.6, since 0.57). Its folder is beside the build's,
	// which the sandbox must find absent, and a stop's leftover is removed by Recover.
	agyDir := out + ".agy"
	defer os.RemoveAll(agyDir)
	agy, err := fetchAgy(ctx, b.AgyClient, facts.Tree.Lock, agyDir)
	if err != nil {
		return "", err
	}
	sizing, recorded, err := b.room(ctx, fmt.Sprintf("build:%d", bd.ID), "the build of Crew "+bd.Version)
	if err != nil {
		return "", fmt.Errorf("room for the build's sandbox: %w", err)
	}
	defer recorded()
	spec := harness.BuildSpec{Version: bd.Version, Tree: tree.Bytes(), Script: script, Agy: agy, Out: out, Timeout: b.Timeout, Sizing: sizing,
		Recorded: recorded}
	if b.Running != "" {
		if running, rerr := release.Read(filepath.Join(b.Releases, b.Running)); rerr == nil &&
			release.SameRuntime(running.Manifest, release.Manifest{Files: facts.Files, Runtime: facts.Tree.Runtime}) {
			rtPath := filepath.Join(b.Releases, b.Running, "images", "crew-runtime.tar")
			if fi, serr := os.Lstat(rtPath); serr == nil && fi.Mode().IsRegular() {
				spec.BaseRuntime = rtPath
				spec.BaseVersion = b.Running
			}
		}
	}
	res, err := b.Sandbox.Build(ctx, spec)
	if err != nil {
		return res.Output, err
	}
	if err := os.WriteFile(filepath.Join(out, release.DeployFile), facts.Tree.Deploy, 0o640); err != nil {
		return res.Output, err
	}
	sum := sha256.Sum256(facts.Tree.Deploy)
	res.Hashes[release.DeployFile] = hex.EncodeToString(sum[:])
	notes, err := b.notes(ctx, project, bd)
	if err != nil {
		return res.Output, err
	}
	pins := map[string]string{cli.AgyName: agyPin(filepath.Join(out, "pins.json")), "postgres": facts.Postgres}
	ask := updater.LoadAsk{Version: bd.Version, Dir: out, Hashes: res.Hashes, Commit: bd.Commit,
		Files: facts.Files, Agents: facts.Agents, Runtime: facts.Tree.Runtime, Schema: facts.Tree.Schema, ReadBy: facts.Tree.ReadBy,
		MachineSetup: facts.Tree.MachineSetup, Pins: pins, Notes: notes}
	// The updater loads nothing while it installs: the build waits for it rather than be thrown away.
	every, until := b.LoadWait, b.now().Add(2*time.Hour)
	if every <= 0 {
		every = 30 * time.Second
	}
	for {
		_, err = b.Loader.Load(ctx, ask)
		if !errors.Is(err, updater.ErrBusy) || !b.now().Before(until) {
			break
		}
		select {
		case <-ctx.Done():
			return res.Output, ctx.Err()
		case <-time.After(every):
		}
	}
	if err != nil {
		return res.Output, fmt.Errorf("the updater did not load the build: %w", err)
	}
	return res.Output, nil
}

// room waits until the capacity check gives the build's sandbox room, of the size build, which it
// gets as every check sandbox does (13.10), and asks again every half minute: the export of the
// runtime image needs more memory than a large sandbox has (since 0.60). It holds the check's
// lock until the sandbox's row exists, which the check counts: recorded lets it go, once.
func (b *Builder) room(ctx context.Context, key, what string) (orchestrator.Sizing, func(), error) {
	if b.Room == nil {
		return orchestrator.Sizing{}, func() {}, nil
	}
	const size = capacity.Build
	ask := capacity.Ask{Key: key, Size: size, What: what,
		Priority: planning.NormalPriority, Own: planning.NormalPriority}
	for {
		b.Room.Mu.Lock()
		ok, err := b.Room.Take(ctx, ask)
		if err == nil && ok {
			break
		}
		b.Room.Mu.Unlock()
		if err != nil {
			return orchestrator.Sizing{}, nil, err
		}
		select {
		case <-ctx.Done():
			return orchestrator.Sizing{}, nil, ctx.Err()
		case <-time.After(30 * time.Second):
		}
	}
	var once sync.Once
	recorded := func() {
		once.Do(func() {
			if err := b.Room.Taken(context.WithoutCancel(ctx), key); err != nil && b.Log != nil {
				b.Log.Error("taking a started build out of the queue for room", "error", err)
			}
			b.Room.Mu.Unlock()
		})
	}
	s, err := b.Room.SizeFor(ctx, size)
	if err != nil {
		recorded()
		return orchestrator.Sizing{}, nil, err
	}
	return s.Sizing(), recorded, nil
}

// script is deploy/release.sh of the running release's deploy archive, which is checked against the
// hash its release.json names: never the candidate's copy, and never the machine's files (16.2).
func (b *Builder) script() ([]byte, error) {
	f, err := release.Read(filepath.Join(b.Releases, b.Running))
	if err != nil {
		return nil, fmt.Errorf("the running release %s: %w", b.Running, err)
	}
	p, err := f.DeployPath()
	if err != nil {
		return nil, fmt.Errorf("%w: a release Crew builds is built by the running release's own script, so the first release with one is built by hand (crew-ctl build)", err)
	}
	body, err := os.ReadFile(p)
	if err != nil {
		return nil, err
	}
	sum := sha256.Sum256(body)
	if hex.EncodeToString(sum[:]) != f.Manifest.Deploy.SHA256 {
		return nil, fmt.Errorf("the deploy archive of %s has changed since it was built", b.Running)
	}
	return fileInArchive(body, "deploy/release.sh")
}

// fileInArchive is one file of a deploy archive.
func fileInArchive(archive []byte, name string) ([]byte, error) {
	gz, err := gzip.NewReader(bytes.NewReader(archive))
	if err != nil {
		return nil, err
	}
	tr := tar.NewReader(gz)
	for {
		h, err := tr.Next()
		if errors.Is(err, io.EOF) {
			return nil, fmt.Errorf("the deploy archive holds no %s", name)
		}
		if err != nil {
			return nil, err
		}
		if h.Name == name && h.Typeflag == tar.TypeReg {
			return io.ReadAll(io.LimitReader(tr, 1<<20))
		}
	}
}

// Commit is what the running server reads of a commit it builds, from Crew's project itself: every
// file with its git hash, the model and effort lines of the agent files, the tree's facts, and the
// version of PostgreSQL's image its versions.lock names. `crew-ctl build` reads the same of a checkout
// with git and `crew release write --tree`.
type Commit struct {
	Files, Agents map[string]string
	Tree          *release.Tree
	Postgres      string
}

// ReadCommit reads a commit: tree is its tar, listed the lines of `git ls-tree -r`, and file a file
// of it by its path.
func ReadCommit(tree []byte, listed string, file func(path string) ([]byte, error)) (*Commit, error) {
	t, err := release.ReadTree(bytes.NewReader(tree))
	if err != nil {
		return nil, err
	}
	files, err := release.ParseTree(strings.NewReader(listed))
	if err != nil {
		return nil, err
	}
	agents := map[string]string{}
	for p := range files {
		if !release.AgentFile(p) {
			continue
		}
		body, err := file(p)
		if err != nil {
			return nil, err
		}
		if s := release.AgentSettings(body); s != "" {
			agents[p] = s
		}
	}
	tag, _, _ := strings.Cut(t.Lock["POSTGRES_IMAGE"], "@")
	pg, ok := strings.CutPrefix(tag, "postgres:")
	if !ok || pg == "" {
		return nil, errors.New("the tree's versions.lock names no POSTGRES_IMAGE")
	}
	return &Commit{Files: files, Agents: agents, Tree: t, Postgres: pg}, nil
}

// notes is notes.md of a build (16.9): what it holds (Changes), its tickets and then the commits from
// outside, each line screened as a title is, since agents and the operator wrote them (16.2 step 6,
// since 0.54). Where that cannot be read, the last commits of the repository.
func (b *Builder) notes(ctx context.Context, project string, bd *Build) (string, error) {
	ch, err := b.Changes(ctx, project, bd.Version, bd.Commit)
	if err != nil {
		return "", err
	}
	var s strings.Builder
	if ch.Note != "" {
		// Titled in Crew's words, not by the first commit's subject (round 2 of the security review of
		// 95d116d).
		fmt.Fprintf(&s, "# Crew %s: the last commits of Crew's project\n\n", bd.Version)
		commits, err := b.Git.Log(ctx, project, bd.Commit, "", 30)
		if err != nil {
			return "", err
		}
		s.WriteString("The last commits:\n\n")
		for _, c := range commits {
			if len(c.Parents) > 1 {
				continue
			}
			fmt.Fprintf(&s, "- %s (%s)\n", plain(c.Subject), abbrev(c.ID, 7))
		}
		return s.String(), nil
	}
	// Its title is Crew's own words, what it holds counted: an agent's ticket title would name the
	// release on the Install item (security review of 95d116d).
	if title := heldTitle(ch); title != "" {
		fmt.Fprintf(&s, "# Crew %s: %s\n\n", bd.Version, title)
	} else {
		fmt.Fprintf(&s, "# Crew %s\n\n", bd.Version)
	}
	if ch.Since != nil {
		fmt.Fprintf(&s, "Since %s (%s):\n", ch.Since.Version, abbrev(ch.Since.Commit, 12))
	} else {
		s.WriteString("Since no release Crew's project holds:\n")
	}
	if len(ch.Tickets) > 0 {
		s.WriteString("\nTickets:\n\n")
		for _, t := range ch.Tickets {
			fmt.Fprintf(&s, "- %s\n", plain(t.Key+": "+t.Title))
		}
	}
	if len(ch.Outside) > 0 {
		s.WriteString("\nFrom outside:\n\n")
		for _, c := range ch.Outside {
			fmt.Fprintf(&s, "- %s (%s)\n", c.Subject, abbrev(c.Commit, 7))
		}
	}
	return s.String(), nil
}

// heldTitle counts what a release holds, "" for nothing: "3 tickets and 1 commit from outside"; a
// list that was cut is "more than" its count.
func heldTitle(ch *Changes) string {
	count := func(n int, one string, cut bool) string {
		s := fmt.Sprintf("%d %ss", n, one)
		if n == 1 {
			s = "1 " + one
		}
		if cut {
			s = "more than " + s
		}
		return s
	}
	var parts []string
	if n := len(ch.Tickets); n > 0 {
		parts = append(parts, count(n, "ticket", ch.Cut.Tickets))
	}
	if n := len(ch.Outside); n > 0 {
		parts = append(parts, count(n, "commit", ch.Cut.Outside)+" from outside")
	}
	return strings.Join(parts, " and ")
}

// agyPin is what the runtime image got of agy, from the build's pins.json: data the candidate's
// image wrote, so one line of printable text at most 80 characters, or "unknown".
func agyPin(path string) string {
	body, err := os.ReadFile(path)
	if err != nil || len(body) > 64<<10 {
		return "unknown"
	}
	var pins map[string]any
	if json.Unmarshal(body, &pins) != nil {
		return "unknown"
	}
	v, _ := pins[cli.AgyName].(string)
	v = plain(v)
	if r := []rune(v); len(r) > 80 {
		v = string(r[:80])
	}
	if v == "" {
		return "unknown"
	}
	return v
}

// storable is text a column takes: valid UTF-8, no NUL, and the end of it at most maxBuildLog bytes.
func storable(s string) string {
	s = strings.ReplaceAll(strings.ToValidUTF8(s, "\uFFFD"), "\x00", "")
	if len(s) <= maxBuildLog {
		return s
	}
	s = s[len(s)-maxBuildLog:]
	for i := 0; i < len(s) && i < utf8.UTFMax; i++ {
		if utf8.RuneStart(s[i]) {
			return s[i:]
		}
	}
	return s
}

// abbrev is the first n characters of a commit's hash.
func abbrev(id string, n int) string {
	if len(id) > n {
		return id[:n]
	}
	return id
}

// capped is a writer that refuses more than left bytes.
type capped struct {
	w           io.Writer
	left, limit int64
}

func (c *capped) Write(p []byte) (int, error) {
	if int64(len(p)) > c.left {
		return 0, fmt.Errorf("the commit's tree is larger than %d bytes", c.limit)
	}
	c.left -= int64(len(p))
	return c.w.Write(p)
}

func (b *Builder) now() time.Time {
	if b.Now != nil {
		return b.Now()
	}
	return time.Now()
}
