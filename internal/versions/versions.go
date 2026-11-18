// Package versions is the server's side of releases (design 16.1 to 16.5, 16.8): it finds the
// release folders on the machine and records them, decides which kind each is from the files it
// changes, gives notice, installs a release of text by itself after its notice unless it is held,
// and asks the updater for every install and roll back. It follows the updater's requests into
// update_requests and the history, and installs a new updater once the server has been healthy on
// the new version: the updater never replaces itself. No agent reaches any of it: the Crew MCP has no
// tool for it, and its routes are the operator's (16.3).
package versions

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/kesokaj/crew/internal/db"
	"github.com/kesokaj/crew/internal/release"
	"github.com/kesokaj/crew/internal/settings"
	"github.com/kesokaj/crew/internal/updater"
)

// ReleaseGit is what the hold of a release reads of Crew's project's history (16.3, since 0.55).
type ReleaseGit interface {
	IsAncestor(ctx context.Context, name, a, b string) (bool, error)
}

// Updater is the updater as the server reaches it: its socket.
type Updater interface {
	Status(ctx context.Context) (updater.Status, error)
	Install(ctx context.Context, a updater.Ask) (*updater.Request, error)
	Stop(ctx context.Context) error
}

// Replacer installs a new updater: the server's part of 16.8, never the updater's own.
type Replacer interface {
	ReplaceUpdater(ctx context.Context, version string) error
}

// Who asks the updater, as the request records it.
const (
	ByOperator = "operator"
	ByClock    = updater.ByClock
)

// Service is the server's side of releases.
type Service struct {
	Pool *pgxpool.Pool
	// Releases is the folder of release folders, which the server reads only.
	Releases string
	// Running is the version of this server.
	Running  string
	Updater  Updater
	Replacer Replacer
	// GitHub and Registry make the private copy of each release (16.9); nil makes none. Builder builds
	// a release of Crew's project (16.2 step 4); nil builds none.
	GitHub   GitHub
	Registry Registry
	Builder  *Builder
	// Git reads Crew's project's history for the hold of a release that holds an improvement the
	// operator took out (16.3, since 0.55); it is there also where the server builds nothing.
	Git      ReleaseGit
	Settings func(ctx context.Context) (settings.Doc, error)
	Now      func() time.Time
	Log      *slog.Logger
}

func (s *Service) now() time.Time {
	if s.Now != nil {
		return s.Now()
	}
	return time.Now()
}

// Run scans, follows the updater, and keeps the clock, every interval until ctx ends.
func (s *Service) Run(ctx context.Context, every time.Duration) {
	for {
		if err := s.Pass(ctx); err != nil && ctx.Err() == nil {
			s.Log.Warn("releases", "error", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(every):
		}
	}
}

// Pass is one round: the folders, the updater's requests, the updater's own version, and the clock.
func (s *Service) Pass(ctx context.Context) error {
	st, stErr := s.Updater.Status(ctx)
	setup := 0
	if stErr == nil {
		setup = st.MachineSetup
	}
	if err := s.Scan(ctx, setup); err != nil {
		return err
	}
	if stErr != nil {
		return fmt.Errorf("the updater: %w", stErr)
	}
	if err := s.Follow(ctx, st); err != nil {
		return err
	}
	if err := s.replaceUpdater(ctx, st); err != nil {
		s.Log.Warn("installing the new updater", "error", err)
	}
	// An improvement taken out before it was installed holds the releases that wait with it before the
	// clock can install one (16.3, since 0.55): a press after their notice, or one that failed halfway.
	// The clock reads the hold again for the release it installs, so an error here is only told.
	if err := s.HoldTakenOut(ctx); err != nil {
		s.Log.Warn("holding the releases of an improvement taken out", "error", err)
	}
	clockErr := s.Clock(ctx, st)
	// Each release installed is measured once against the one it replaced (10.7, since 0.57); a
	// measure that fails is only told: it holds no clock and no install.
	if err := s.Measure(ctx); err != nil {
		s.Log.Warn("measuring the releases", "error", err)
	}
	if err := s.AutoBuild(ctx); err != nil {
		s.Log.Warn("building a release by itself", "error", err)
	}
	if s.Builder != nil {
		project, d, err := s.crewProject(ctx)
		if err == nil {
			err = s.Builder.GateRound(ctx, st, project, installTestSetting(d))
		}
		if err != nil {
			s.Log.Warn("the gate of a release", "error", err)
		}
	}
	// Last: a push to the registry can take long, and the clock and the updater's progress do not
	// wait for it.
	if err := s.Publish(ctx); err != nil {
		s.Log.Warn("copying releases to GitHub", "error", err)
	}
	return clockErr
}

func (s *Service) settings(ctx context.Context) (auto string, hours int, err error) {
	d, err := s.Settings(ctx)
	if err != nil {
		return "", 0, err
	}
	u, _ := d["updates"].(settings.Doc)
	auto, _ = u["auto_install"].(string)
	hours, _ = u["install_after_hours"].(int)
	if auto == "" {
		auto = "text"
	}
	// Another installation names no repository it builds from, and holds no source to tell text from
	// code by: every release waits for Install there, whatever it changes (16.12).
	if repo, _ := u["source_repo"].(string); !settings.SourceRepo.MatchString(repo) {
		auto = "never"
	}
	return auto, hours, nil
}

type row struct {
	Version  string
	Gate     bool
	ByHand   bool
	State    string
	Held     bool
	Replaced *string
}

// Scan records each release folder that is new, gives notice of a release newer than the running
// one once it passed the gate, and marks the running one (16.3). A folder that is gone is removed.
// setup is the machine setup the updater read, 0 when it is not known.
func (s *Service) Scan(ctx context.Context, setup int) error {
	folders, bad, err := release.List(s.Releases)
	if err != nil {
		return err
	}
	for v, err := range bad {
		s.Log.Warn("a release folder that cannot be read", "version", v, "error", err)
	}
	var running *release.Folder
	for _, f := range folders {
		if f.Manifest.Version == s.Running {
			running = f
		}
	}
	auto, hours, err := s.settings(ctx)
	if err != nil {
		return err
	}
	known := map[string]row{}
	rows, err := s.Pool.Query(ctx, `SELECT version, gate IS NOT NULL, by_hand, state, held, replaced_by FROM releases`)
	if err != nil {
		return err
	}
	for rows.Next() {
		var r row
		if err := rows.Scan(&r.Version, &r.Gate, &r.ByHand, &r.State, &r.Held, &r.Replaced); err != nil {
			rows.Close()
			return err
		}
		known[r.Version] = r
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}
	seen := map[string]bool{}
	for _, f := range folders {
		v := f.Manifest.Version
		seen[v] = true
		r, ok := known[v]
		switch {
		case !ok:
			if err := s.record(ctx, f, running, setup, auto, hours); err != nil {
				return err
			}
		case !r.Gate && f.Gate != nil:
			// The gate passed after the folder was found: notice now.
			if err := s.gated(ctx, f, running, setup, auto, hours); err != nil {
				return err
			}
		}
		if ok && !r.ByHand && f.Manifest.ByHand {
			manifest, err := json.Marshal(f.Manifest)
			if err != nil {
				return err
			}
			if _, err := s.Pool.Exec(ctx, `UPDATE releases SET by_hand = true, manifest_sha256 = $2, manifest = $3 WHERE version = $1`,
				v, f.ManifestSHA256, manifest); err != nil {
				return err
			}
		}
	}
	return pgx.BeginFunc(ctx, s.Pool, func(tx pgx.Tx) error {
		// The running version is this server's; the one that ran before is kept.
		if _, err := tx.Exec(ctx, `UPDATE releases SET state = 'running', installed_at = coalesce(installed_at, $2),
				held = false, replaced_by = NULL WHERE version = $1 AND (state <> 'running' OR installed_at IS NULL)`, s.Running, s.now()); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `UPDATE releases SET state = 'kept' WHERE state = 'running' AND version <> $1`, s.Running); err != nil {
			return err
		}
		for v, r := range known {
			if !seen[v] && r.State != "removed" {
				if _, err := tx.Exec(ctx, `UPDATE releases SET state = 'removed' WHERE version = $1`, v); err != nil {
					return err
				}
				if err := db.RecordChange(ctx, tx, "crew", "releases", v, map[string]string{"state": r.State}, map[string]string{"state": "removed"}); err != nil {
					return err
				}
			}
		}
		return nil
	})
}

// kindOf is the kind of f against the running release. Without the running one's folder, a build
// from a checkout, nothing can be compared: the release waits for Install, as code does.
func kindOf(f, running *release.Folder, setup int) release.Kind {
	if running == nil {
		return release.Kind{Kind: release.KindCode, Changed: []string{}, Protected: []string{"the running version has no release folder to compare with"}}
	}
	if setup == 0 {
		setup = running.Manifest.MachineSetup
	}
	return release.Classify(running.Manifest, f.Manifest, setup)
}

// record inserts a release found for the first time.
func (s *Service) record(ctx context.Context, f, running *release.Folder, setup int, auto string, hours int) error {
	k := kindOf(f, running, setup)
	manifest, err := json.Marshal(f.Manifest)
	if err != nil {
		return err
	}
	var gate []byte
	if f.Gate != nil {
		if gate, err = json.Marshal(f.Gate); err != nil {
			return err
		}
	}
	state := "kept"
	if f.Manifest.Version == s.Running {
		state = "running"
	}
	err = pgx.BeginFunc(ctx, s.Pool, func(tx pgx.Tx) error {
		// The version that runs when its folder is found was installed then, as far as Crew knows.
		var installed *time.Time
		if state == "running" {
			t := s.now()
			installed = &t
		}
		if _, err := tx.Exec(ctx, `INSERT INTO releases (version, commit, manifest_sha256, manifest, built_at, kind, protected, changed, by_hand, gate, state, installed_at)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12) ON CONFLICT (version) DO NOTHING`,
			f.Manifest.Version, f.Manifest.Commit, f.ManifestSHA256, manifest, f.Manifest.BuiltAt, k.Kind, k.Protected, len(k.Changed),
			f.Manifest.ByHand, nullJSON(gate), state, installed); err != nil {
			return err
		}
		return db.RecordChange(ctx, tx, "crew", "releases", f.Manifest.Version, nil, map[string]any{"state": state, "kind": k.Kind, "gate": f.Gate != nil})
	})
	if err != nil || f.Gate == nil || state == "running" {
		return err
	}
	return s.notice(ctx, f, k, auto, hours)
}

// gated records a gate that passed after the folder was found, and gives notice.
func (s *Service) gated(ctx context.Context, f, running *release.Folder, setup int, auto string, hours int) error {
	k := kindOf(f, running, setup)
	gate, err := json.Marshal(f.Gate)
	if err != nil {
		return err
	}
	if _, err := s.Pool.Exec(ctx, `UPDATE releases SET gate = $2, kind = $3, protected = $4, changed = $5 WHERE version = $1 AND manifest_sha256 = $6`,
		f.Manifest.Version, gate, k.Kind, k.Protected, len(k.Changed), f.ManifestSHA256); err != nil {
		return err
	}
	if f.Manifest.Version == s.Running {
		return nil
	}
	return s.notice(ctx, f, k, auto, hours)
}

func nullJSON(b []byte) any {
	if b == nil {
		return nil
	}
	return b
}

// notice gives notice of a release newer than the running one (16.3, step 2): it waits from now,
// for its clock when it installs by itself, else for Install. A release it replaces, a held one too,
// is no longer next.
func (s *Service) notice(ctx context.Context, f *release.Folder, k release.Kind, auto string, hours int) error {
	v := f.Manifest.Version
	if !release.Less(s.Running, v) {
		return nil
	}
	now := s.now()
	var after *time.Time
	// A gate that skipped a step never lets a release install by itself, whatever its kind, and a suite
	// that measured worse holds it with the numbers (16.2 step 5).
	if release.InstallsBySelf(k, auto) && f.Gate.InstallsBySelfAfterGate() {
		t := now.Add(time.Duration(hours) * time.Hour)
		after = &t
	}
	held, why := false, ""
	if f.Gate != nil && f.Gate.Hold != "" {
		held, why = true, f.Gate.Hold
	}
	// An improvement the operator took out before it was installed holds a release that has it and not
	// its revert (16.3, since 0.55).
	taken, err := s.TakenOutHold(ctx, f.Manifest.Commit)
	if err != nil {
		return err
	}
	if taken != "" {
		held, why = true, strings.TrimPrefix(why+"; "+taken, "; ")
	}
	return pgx.BeginFunc(ctx, s.Pool, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `UPDATE releases SET state = 'waiting', notice_at = $2, install_after = $3, held = $4, held_why = nullif($5, '')
				WHERE version = $1`, v, now, after, held, why); err != nil {
			return err
		}
		// Every other release that waited is replaced by this one, when it is older.
		olders, err := tx.Query(ctx, `SELECT version FROM releases WHERE state IN ('waiting', 'failed') AND replaced_by IS NULL AND version <> $1`, v)
		if err != nil {
			return err
		}
		var replace []string
		for olders.Next() {
			var o string
			if err := olders.Scan(&o); err != nil {
				olders.Close()
				return err
			}
			if release.Less(o, v) {
				replace = append(replace, o)
			}
		}
		olders.Close()
		for _, o := range replace {
			if _, err := tx.Exec(ctx, `UPDATE releases SET replaced_by = $2, state = CASE WHEN state = 'waiting' THEN 'kept' ELSE state END WHERE version = $1`, o, v); err != nil {
				return err
			}
		}
		return db.RecordChange(ctx, tx, "crew", "releases", v, nil, map[string]any{"state": "waiting", "kind": k.Kind,
			"protected": k.Protected, "replaces": replace, "installAfter": after})
	})
}
