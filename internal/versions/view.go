package versions

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
	"unicode"

	"github.com/jackc/pgx/v5"

	"github.com/kesokaj/crew/internal/release"
	"github.com/kesokaj/crew/internal/updater"
)

// Screen is what the Versions screen shows (16.4, 16.5, versions.png): the running version, the next
// release and whether it waits for you, the updater with its last changes, every kept release with
// roll back, and the steps of the request that runs, with the updater's own log.
type Screen struct {
	Running  *Release      `json:"running"`
	Next     *Release      `json:"next"`
	Updater  UpdaterView   `json:"updater"`
	Releases []Release     `json:"releases"`
	Requests []RequestView `json:"requests"`
	// OnOldImage is how many sandboxes still run on the image of another version: each keeps the
	// image it started with until it ends (16.7).
	OnOldImage int `json:"onOldImage"`
	OnImage    int `json:"onImage"`
	// Candidate is the version whose gate the screen shows (16.2 step 6, since 0.52): a gate whose live
	// part runs, whatever its version; else the newest, by when it started, of the builds and the gates
	// whose version is newer than the running release's; never installed; "" when there is none.
	Candidate string `json:"candidate,omitempty"`
	// Publish says a release that passed its gate is copied to GitHub (16.9, since 0.53): updates.publish
	// is github and there is a GitHub and a registry to copy to. Off, nothing is copied.
	Publish bool `json:"publish"`
	// Building is the version of the build that runs, and CanBuild whether this server builds at all:
	// Build a release is offered when it does and none runs (16.2 step 3, since 0.54).
	Building string `json:"building,omitempty"`
	CanBuild bool   `json:"canBuild"`
	// DeployFiles says whether crew-ctl and its files on the machine are the running release's (16.11,
	// since 0.55).
	DeployFiles DeployFilesView `json:"deployFiles"`
	// MeasureAfter is how many tickets a release is measured over: improvements.measure_after_tasks.
	MeasureAfter int `json:"measureAfter"`
}

// DeployFilesView says whether crew-ctl and its files on the machine are the running release's, as
// install recorded them (16.11, since 0.55): the server cannot read root's files, so a file changed in
// place since is crew-ctl's to tell. "same"; "differ", with how many and the paths, at most 20 by path;
// "unrecorded", when install recorded none, as one before 0.55, or its record cannot be read; "" when
// the running release has no deploy archive to compare with, as a build from a checkout. Command is
// what root runs to put the release's own in place: it checks the archive against release.json's hash
// before anything of it runs, then runs its crew-ctl's install --deploy-files, which checks it again,
// with the gate, and puts the files in place. An archive that does not check gets no command, and Error
// says why. An update is not held for any of it.
type DeployFilesView struct {
	State   string   `json:"state,omitempty"`
	Differ  int      `json:"differ,omitempty"`
	Paths   []string `json:"paths,omitempty"`
	Command string   `json:"command,omitempty"`
	Error   string   `json:"error,omitempty"`
}

// deployFiles compares what install recorded in the releases folder with the running release's deploy
// archive. The server reads both from the releases folder, which it mounts at the machine's own path,
// so the command names the archive where root finds it.
func (s *Service) deployFiles() DeployFilesView {
	folder := filepath.Join(s.Releases, s.Running)
	archive := filepath.Join(folder, release.DeployFile)
	f, err := os.Open(archive)
	if err != nil {
		return DeployFilesView{}
	}
	sum := sha256.New()
	want, err := release.DeployHashes(io.TeeReader(f, sum))
	if err == nil {
		_, err = io.Copy(sum, f)
	}
	f.Close()
	if err != nil {
		return DeployFilesView{Error: "the deploy archive of " + s.Running + " cannot be read"}
	}
	v := DeployFilesView{}
	if r, err := release.Read(folder); err != nil || r.Manifest.Deploy == nil || r.Manifest.Deploy.File != release.DeployFile {
		v.Error = "release.json of " + s.Running + " names no deploy archive"
	} else if r.Manifest.Deploy.SHA256 != hex.EncodeToString(sum.Sum(nil)) {
		v.Error = "the deploy archive of " + s.Running + " is not the one release.json names"
	} else {
		// The archive, then the very crew-ctl unpacked from it, are checked before it runs (security
		// review of 864fb57..97382bc, rounds 1 and 2).
		v.Command = `echo ` + shellQuote(r.Manifest.Deploy.SHA256+"  "+archive) + ` | sha256sum -c - && d=$(mktemp -d) && tar -xzf ` + shellQuote(archive) +
			` -C "$d" deploy/crew-ctl && echo ` + shellQuote(want["deploy/crew-ctl"]+"  ") + `"$d/deploy/crew-ctl" | sha256sum -c - && "$d/deploy/crew-ctl" install --deploy-files`
	}
	b, err := os.ReadFile(filepath.Join(s.Releases, release.DeployRecord))
	if errors.Is(err, os.ErrNotExist) {
		v.State = "unrecorded"
		return v
	}
	var rec struct {
		Files map[string]string `json:"files"`
	}
	if err != nil || json.Unmarshal(b, &rec) != nil {
		v.State = "unrecorded"
		if v.Error == "" {
			v.Error = "the record of the deploy files cannot be read"
		}
		return v
	}
	paths := make([]string, 0, len(want))
	for p := range want {
		paths = append(paths, p)
	}
	sort.Strings(paths)
	for _, p := range paths {
		if rec.Files[p] != want[p] {
			if v.Differ < 20 {
				v.Paths = append(v.Paths, p)
			}
			v.Differ++
		}
	}
	if v.Differ == 0 {
		return DeployFilesView{State: "same"}
	}
	v.State = "differ"
	return v
}

// shellQuote is s as one word of a POSIX shell.
func shellQuote(s string) string { return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'" }

// Release is a row of the releases table, with what its folder says.
type Release struct {
	Version      string            `json:"version"`
	Commit       string            `json:"commit"`
	State        string            `json:"state"`
	Kind         string            `json:"kind"`
	Protected    []string          `json:"protected"`
	Changed      int               `json:"changed"`
	ByHand       bool              `json:"byHand"`
	Gate         *release.Gate     `json:"gate"`
	BuiltAt      time.Time         `json:"builtAt"`
	NoticeAt     *time.Time        `json:"noticeAt"`
	InstallAfter *time.Time        `json:"installAfter"`
	Held         bool              `json:"held"`
	HeldWhy      *string           `json:"heldWhy"`
	ReplacedBy   *string           `json:"replacedBy"`
	InstalledAt  *time.Time        `json:"installedAt"`
	Failure      *string           `json:"failure"`
	Published    map[string]any    `json:"published"`
	PublishError *string           `json:"publishError"`
	Schema       int               `json:"schema"`
	ReadBy       int               `json:"readBy"`
	Pins         map[string]string `json:"pins"`
	Images       map[string]string `json:"images"`
	// Notes is notes.md of the folder; Title its first line of what changed.
	Notes string `json:"notes"`
	Title string `json:"title"`
	// Tickets is how many tickets were Done with runs on this version, and FirstRequest how many of
	// them were Done at their first request to move (0.48).
	Tickets      int `json:"tickets"`
	FirstRequest int `json:"firstRequest"`
	// RollBack is whether the screen offers Roll back to this, and Restores whether that reads its
	// snapshot back and loses what was written since (16.6).
	RollBack bool `json:"rollBack"`
	Restores bool `json:"restores"`
	// Measure is the release compared with the one it replaced, once it was measured (10.7, since 0.57);
	// nil before.
	Measure *ReleaseMeasure `json:"measure"`
}

// UpdaterView is the updater as the server reaches it.
type UpdaterView struct {
	Answers  bool   `json:"answers"`
	Error    string `json:"error,omitempty"`
	Version  string `json:"version"`
	Running  string `json:"running"`
	Checking string `json:"checking,omitempty"`
	// Current is the request that runs, with its steps as they come.
	Current *RequestView `json:"current"`
}

// RequestView is a request to the updater, as update_requests holds it.
type RequestView struct {
	ID      int64          `json:"id"`
	Version string         `json:"version"`
	From    *string        `json:"from"`
	Action  string         `json:"action"`
	AskedBy string         `json:"askedBy"`
	State   string         `json:"state"`
	Step    int            `json:"step"`
	Steps   []updater.Step `json:"steps"`
	Error   *string        `json:"error"`
	AskedAt time.Time      `json:"askedAt"`
	EndedAt *time.Time     `json:"endedAt"`
}

// View reads the screen.
func (s *Service) View(ctx context.Context) (Screen, error) {
	out := Screen{Releases: []Release{}, Requests: []RequestView{}, CanBuild: s.Builder != nil, DeployFiles: s.deployFiles(), MeasureAfter: 20}
	if s.Settings != nil {
		if d, err := s.Settings(ctx); err == nil {
			out.MeasureAfter = measureAfter(d)
		}
	}
	if err := s.Pool.QueryRow(ctx, `SELECT version FROM builds WHERE state = 'running' ORDER BY started_at DESC LIMIT 1`).Scan(&out.Building); err != nil &&
		!errors.Is(err, pgx.ErrNoRows) {
		return out, err
	}
	st, err := s.Updater.Status(ctx)
	if err != nil {
		out.Updater.Error = err.Error()
	} else {
		out.Updater = UpdaterView{Answers: true, Version: st.State.Updater, Running: st.State.Running, Checking: st.Checking}
		if st.Current != nil {
			out.Updater.Current = &RequestView{ID: st.Current.ID, Version: st.Current.Version, Action: st.Current.Action, AskedBy: st.Current.AskedBy,
				State: st.Current.State, Step: st.Current.Step, Steps: st.Current.Steps, AskedAt: st.Current.AskedAt}
			if st.Current.From != "" {
				out.Updater.Current.From = &st.Current.From
			}
		}
	}
	rows, err := s.Pool.Query(ctx, `SELECT r.version, r.commit, r.state, r.kind, r.protected, r.changed, r.by_hand, r.gate, r.built_at,
			r.notice_at, r.install_after, r.held, r.held_why, r.replaced_by, r.installed_at, r.failure, r.published, r.publish_error,
			(r.manifest->>'schema')::int, coalesce((r.manifest->>'readBy')::int, 0),
			coalesce(r.manifest->'pins', '{}'::jsonb), r.manifest->'images',
			coalesce(t.n, 0), coalesce(t.first, 0), r.measure
		FROM releases r
		LEFT JOIN LATERAL (SELECT count(*) AS n, count(*) FILTER (WHERE k.fix_rounds = 0) AS first FROM tickets k
			WHERE k.stage = 'done' AND EXISTS (SELECT 1 FROM subtasks x JOIN runs u ON u.subtask = x.id
				WHERE x.ticket = k.id AND u.crew_version = r.version)) t ON true
		WHERE r.state <> 'removed'`)
	if err != nil {
		return out, err
	}
	var running *Release
	for rows.Next() {
		var r Release
		var images map[string]release.Image
		if err := rows.Scan(&r.Version, &r.Commit, &r.State, &r.Kind, &r.Protected, &r.Changed, &r.ByHand, &r.Gate, &r.BuiltAt,
			&r.NoticeAt, &r.InstallAfter, &r.Held, &r.HeldWhy, &r.ReplacedBy, &r.InstalledAt, &r.Failure, &r.Published, &r.PublishError,
			&r.Schema, &r.ReadBy, &r.Pins, &images, &r.Tickets, &r.FirstRequest, &r.Measure); err != nil {
			rows.Close()
			return out, err
		}
		r.Images = map[string]string{}
		for name, img := range images {
			r.Images[name] = img.Tag
		}
		r.Notes, r.Title = s.notes(r.Version)
		// A gate a server before 0.53 recorded, and its hold, are shown without its amounts and in today's
		// words (16.2 step 5, since 0.54).
		r.Gate = r.Gate.Unpriced()
		if r.HeldWhy != nil {
			why := release.Unpriced(*r.HeldWhy)
			r.HeldWhy = &why
		}
		out.Releases = append(out.Releases, r)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return out, err
	}
	// Newest first, by number.
	for i := 1; i < len(out.Releases); i++ {
		for j := i; j > 0 && release.Less(out.Releases[j-1].Version, out.Releases[j].Version); j-- {
			out.Releases[j-1], out.Releases[j] = out.Releases[j], out.Releases[j-1]
		}
	}
	for i := range out.Releases {
		if out.Releases[i].Version == s.Running {
			running = &out.Releases[i]
		}
	}
	for i := range out.Releases {
		r := &out.Releases[i]
		if r.Version != s.Running && (r.State == "kept" || r.State == "failed") && (r.Gate != nil || r.ByHand) {
			r.RollBack = release.Less(r.Version, s.Running)
			if running != nil && r.Schema < running.ReadBy {
				r.Restores = true
			}
		}
		if r.State == "waiting" && r.ReplacedBy == nil && out.Next == nil {
			next := *r
			out.Next = &next
		}
	}
	if running != nil {
		c := *running
		out.Running = &c
	}
	reqs, err := s.Pool.Query(ctx, `SELECT id, version, from_version, action, asked_by, state, step, steps, error, asked_at, ended_at
		FROM update_requests ORDER BY id DESC LIMIT 10`)
	if err != nil {
		return out, err
	}
	for reqs.Next() {
		var q RequestView
		if err := reqs.Scan(&q.ID, &q.Version, &q.From, &q.Action, &q.AskedBy, &q.State, &q.Step, &q.Steps, &q.Error, &q.AskedAt, &q.EndedAt); err != nil {
			reqs.Close()
			return out, err
		}
		out.Requests = append(out.Requests, q)
	}
	reqs.Close()
	if err := reqs.Err(); err != nil {
		return out, err
	}
	if out.Candidate, err = s.candidate(ctx); err != nil {
		return out, err
	}
	if _, out.Publish, err = s.publishing(ctx); err != nil {
		return out, err
	}
	image := "crew-runtime:" + s.Running
	err = s.Pool.QueryRow(ctx, `SELECT count(*) FILTER (WHERE image = $1), count(*) FILTER (WHERE image <> $1)
		FROM sandboxes WHERE removed_at IS NULL AND kind <> 'hosted'`, image).Scan(&out.OnImage, &out.OnOldImage)
	return out, err
}

// candidate is the version whose gate the screen shows (16.2 step 6, since 0.52): the newest, by when
// it started, of the builds and the gates, so a gate crew-ctl gate made, which has no build, counts,
// and a failed build shows until a newer one starts. A gate whose live part runs comes first, whatever
// its version, since its candidate image holds the agent CLI's login: a newer build would hide it, and
// a pressed one runs on when a newer release is installed (security review of M16 P5, two rounds).
// Else a version not newer than the running release's is none, nor one that was installed once: a
// release rolled back from is newer, and its gate is over.
func (s *Service) candidate(ctx context.Context) (string, error) {
	rows, err := s.Pool.Query(ctx, `SELECT v.version, v.live FROM (SELECT version, started_at, false AS live FROM builds
			UNION ALL SELECT version, started_at, state = 'live' FROM gates) v
		WHERE NOT EXISTS (SELECT 1 FROM releases r WHERE r.version = v.version AND r.installed_at IS NOT NULL)
		ORDER BY v.live DESC, v.started_at DESC`)
	if err != nil {
		return "", err
	}
	type row struct {
		version string
		live    bool
	}
	found, err := pgx.CollectRows(rows, func(r pgx.CollectableRow) (row, error) {
		var x row
		return x, r.Scan(&x.version, &x.live)
	})
	if err != nil {
		return "", err
	}
	for _, x := range found {
		if x.live || release.Less(s.Running, x.version) {
			return x.version, nil
		}
	}
	return "", nil
}

// Title is a release's title, from its folder's notes.md: the one given at its build, in the heading
// "# Crew X.Y.Z: title", else the first line of what changed; "" when the folder or its notes are
// gone.
func (s *Service) Title(version string) string {
	_, t := s.notes(version)
	return t
}

// notes reads notes.md of a release folder, and its title.
func (s *Service) notes(version string) (string, string) {
	body, err := os.ReadFile(filepath.Join(s.Releases, version, release.NotesFile))
	if err != nil {
		return "", ""
	}
	text := string(body)
	if len(text) > 64<<10 {
		text = text[:64<<10]
	}
	lines := strings.Split(text, "\n")
	if t, ok := strings.CutPrefix(strings.TrimSpace(lines[0]), "# Crew "+version+": "); ok && plain(t) != "" {
		return text, plain(t)
	}
	for _, l := range lines {
		if t, ok := strings.CutPrefix(strings.TrimSpace(l), "- "); ok {
			return text, plain(t)
		}
	}
	return text, ""
}

// plain is a title as a line may show it: no control, invisible, or direction-changing character,
// and at most 120 characters, as crew-ctl build checks a title it is given; notes.md has no hash in
// release.json, so the server does not take the check as done (security review of 0.50).
func plain(t string) string {
	t = strings.Map(func(r rune) rune {
		if r != ' ' && (unicode.Is(unicode.C, r) || unicode.Is(unicode.Z, r)) {
			return -1
		}
		return r
	}, t)
	if r := []rune(strings.TrimSpace(t)); len(r) > 120 {
		return string(r[:120])
	}
	return strings.TrimSpace(t)
}
