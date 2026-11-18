package hosting

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/kesokaj/crew/internal/capacity"
	"github.com/kesokaj/crew/internal/credstore"
	"github.com/kesokaj/crew/internal/harness"
	"github.com/kesokaj/crew/internal/orchestrator"
	"github.com/kesokaj/crew/internal/settings"
)

// Git is what hosting reads of a project's repository: main's head in the canonical repository ("" when
// it has none), a file of a commit, and a commit as git archive writes it.
type Git interface {
	Main(ctx context.Context, name string) string
	FileAt(ctx context.Context, name, commit, path string) ([]byte, error)
	Archive(ctx context.Context, name, commit string, w io.Writer) error
}

// The bounds of a deploy's steps (plan M19 Interfaces).
var (
	BuildTimeout  = 60 * time.Minute
	CheckTimeout  = 120 * time.Second
	CheckEvery    = 2 * time.Second
	CopyTimeout   = 10 * time.Minute
	RetireAfter   = 20 * time.Second
	DiskTimesTar  = uint64(3)
	roomEvery     = 30 * time.Second
	stopWaitLimit = 2 * time.Minute
)

// PreconditionError is a deploy, a roll back or a stop the project's state refuses now, with the reason
// the operator reads (409).
type PreconditionError struct{ Msg string }

func (e *PreconditionError) Error() string { return e.Msg }

// composeFiles are the names a Dockerfile setting has when it is a compose project, which M19 does not
// host (plan M19 D3).
var composeFiles = []string{"compose.yaml", "compose.yml", "docker-compose.yaml", "docker-compose.yml"}

// IsCompose says whether hosting.dockerfile names a compose file.
func IsCompose(dockerfile string) bool {
	return slices.Contains(composeFiles, filepath.Base(dockerfile))
}

// Ask is one deploy to make.
type Ask struct {
	Why string // asked, follow_main, rollback, hostable, settings
	By  string // operator, follow_main, hostable, settings
	// From is the deploy whose image a roll back or a settings deploy takes, by its number.
	From int
	// RestoreData puts back, for a roll back, the data as the deploy From left it.
	RestoreData bool
	// Tickets are the keys of the tickets Done since the deploy before.
	Tickets []string
	// Commit is the commit to build; "" is main's head now.
	Commit string
}

// Deploy is a deploy as the Hosting screen lists it.
type Deploy struct {
	ID        int64      `json:"-"`
	Number    int        `json:"number"`
	Commit    string     `json:"commit"`
	Image     string     `json:"image,omitempty"`
	Why       string     `json:"why"`
	Tickets   []string   `json:"tickets"`
	State     string     `json:"state"`
	Detail    string     `json:"detail"`
	DataCopy  string     `json:"-"`
	From      int64      `json:"-"`
	StartedAt time.Time  `json:"startedAt"`
	LiveAt    *time.Time `json:"liveAt,omitempty"`
}

// project is what a deploy needs of its project.
type project struct{ id, name, key, state string }

// running is the deploys this server carries, by project, with what ends one.
type running struct {
	mu   sync.Mutex
	by   map[string]context.CancelFunc
	done map[string]chan struct{}
}

func (r *running) start(p string, cancel context.CancelFunc) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.by == nil {
		r.by, r.done = map[string]context.CancelFunc{}, map[string]chan struct{}{}
	}
	if _, ok := r.by[p]; ok {
		return false
	}
	r.by[p], r.done[p] = cancel, make(chan struct{})
	return true
}

func (r *running) end(p string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if d, ok := r.done[p]; ok {
		close(d)
	}
	delete(r.by, p)
	delete(r.done, p)
}

// stop cancels a project's running deploy and waits, at most limit, until it ended.
func (r *running) stop(p string, limit time.Duration) {
	r.mu.Lock()
	cancel, done := r.by[p], r.done[p]
	r.mu.Unlock()
	if cancel == nil {
		return
	}
	cancel()
	select {
	case <-done:
	case <-time.After(limit):
	}
}

// SandboxID is the id of a project's hosted sandbox of a deploy (plan M19 D31): its name holds the
// project's id, so two projects' never meet.
func SandboxID(projectID string, number int) string {
	return "host-" + strings.ReplaceAll(projectID, "-", "")[:12] + "-" + strconv.Itoa(number)
}

// ImageRef is the tag of a deploy's image (plan M19 D30).
func ImageRef(projectID string, number int) string {
	return "crew-host/" + projectID + ":" + strconv.Itoa(number)
}

func (s *Service) projectDir(id string) string { return filepath.Join(s.Dir, id) }
func (s *Service) dataDir(id string) string    { return filepath.Join(s.Dir, id, "data") }
func (s *Service) copyDir(id, name string) string {
	return filepath.Join(s.Dir, id, "copies", name)
}
func (s *Service) buildDir(id string, number int) string {
	return filepath.Join(s.Builds, "host-"+id+"-"+strconv.Itoa(number))
}

func (s *Service) project(ctx context.Context, id string) (project, error) {
	p := project{id: id}
	err := s.Pool.QueryRow(ctx, `SELECT name, key, state FROM projects WHERE id = $1`, id).Scan(&p.name, &p.key, &p.state)
	return p, err
}

func (s *Service) base() context.Context {
	if s.Base != nil {
		return s.Base
	}
	return context.Background()
}

// Deploy makes a deploy of a project and carries it on in the background (19.2 steps 3 to 7): 202,
// with its row. It is refused while one runs, when the machine or the name cannot host, and for a
// compose project; with no Dockerfile on main it says so.
func (s *Service) Deploy(ctx context.Context, projectID string, a Ask) (Deploy, error) {
	p, err := s.project(ctx, projectID)
	if err != nil {
		return Deploy{}, err
	}
	if why := s.refused(p.name); why != "" {
		return Deploy{}, &PreconditionError{why}
	}
	if s.Runtime == nil || s.Git == nil {
		return Deploy{}, &PreconditionError{"hosting needs Docker, which this server does not reach"}
	}
	if p.state == "archived" {
		return Deploy{}, &PreconditionError{"an archived project is not hosted"}
	}
	st, err := settingsOf(ctx, s.Pool, p.id, p.name, p.key)
	if err != nil {
		return Deploy{}, err
	}
	if IsCompose(st.Dockerfile) {
		return Deploy{}, &PreconditionError{"a compose project is not hosted yet: one Dockerfile is (19.3)"}
	}
	var from Deploy
	commit := a.Commit
	if a.From > 0 {
		if from, err = s.deployByNumber(ctx, p.id, a.From); err != nil {
			return Deploy{}, &PreconditionError{fmt.Sprintf("there is no deploy %d", a.From)}
		}
		if ok, err := s.canRollBack(ctx, p.id, from); err != nil || !ok {
			return Deploy{}, &PreconditionError{fmt.Sprintf("the image of deploy %d is no longer kept", a.From)}
		}
		if a.RestoreData {
			if c, err := s.copyOf(ctx, p.id, from); err != nil || c == "" {
				return Deploy{}, &PreconditionError{fmt.Sprintf("the copy of the data deploy %d left is no longer kept", a.From)}
			}
		}
		commit = from.Commit
	} else if a.Why == "rollback" || a.Why == "settings" {
		return Deploy{}, errors.New("a roll back or a settings deploy names the deploy whose image it takes")
	}
	if commit == "" {
		if commit = s.Git.Main(ctx, p.name); commit == "" {
			return Deploy{}, &PreconditionError{"main has no commit to host yet"}
		}
	}
	if a.From == 0 {
		body, err := s.Git.FileAt(ctx, p.name, commit, st.Dockerfile)
		if err != nil {
			return Deploy{}, err
		}
		if body == nil {
			return Deploy{}, &NoDockerfileError{Dockerfile: st.Dockerfile, Commit: commit}
		}
	}
	if a.From == 0 && s.Builder == nil {
		return Deploy{}, &PreconditionError{"hosting needs Docker, which this server does not reach"}
	}
	// A build lists the tickets Done since what serves began, when no caller said (19.5).
	if a.From == 0 && a.Tickets == nil {
		if cur, err := s.liveOf(ctx, p.id); err == nil && cur.ok {
			if a.Tickets, err = s.doneSince(ctx, p.id, cur.deploy.StartedAt); err != nil {
				return Deploy{}, err
			}
		}
	}
	d, err := s.record(ctx, p.id, a, commit, from)
	if err != nil {
		return Deploy{}, err
	}
	dctx, cancel := context.WithCancel(s.base())
	if !s.run.start(p.id, cancel) {
		cancel()
		s.fail(context.WithoutCancel(ctx), p.id, d, "another deploy of this project is carried on")
		return Deploy{}, &PreconditionError{"a deploy of this project runs"}
	}
	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		defer s.run.end(p.id)
		defer cancel()
		s.carry(dctx, p, st, d, a, from)
	}()
	return d, nil
}

// NoDockerfileError is a project whose main has no Dockerfile where hosting.dockerfile says.
type NoDockerfileError struct{ Dockerfile, Commit string }

func (e *NoDockerfileError) Error() string {
	return "main has no " + e.Dockerfile + " yet"
}

// Wait waits until the deploys this server carries ended: a test's, and a stop's.
func (s *Service) Wait() { s.wg.Wait() }

// record writes the deploy's row, its number the next of the project's, and the project's hosting
// row, deploying while nothing is live. A deploy that runs already refuses it (the index).
func (s *Service) record(ctx context.Context, projectID string, a Ask, commit string, from Deploy) (Deploy, error) {
	d := Deploy{Commit: commit, Why: a.Why, Tickets: a.Tickets, State: "building"}
	if d.Tickets == nil {
		d.Tickets = []string{}
	}
	var fromID *int64
	if from.ID > 0 {
		fromID, d.Image, d.State = &from.ID, from.Image, "starting"
	}
	err := pgx.BeginFunc(ctx, s.Pool, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `INSERT INTO hosting (project, state) VALUES ($1, 'deploying')
			ON CONFLICT (project) DO UPDATE SET state = CASE WHEN hosting.live_deploy IS NULL THEN 'deploying' ELSE hosting.state END,
				updated_at = now()`, projectID); err != nil {
			return err
		}
		return tx.QueryRow(ctx, `INSERT INTO deploys (project, number, commit, image, why, asked_by, from_deploy, tickets, state)
			VALUES ($1, (SELECT coalesce(max(number), 0) + 1 FROM deploys WHERE project = $1), $2, nullif($3, ''), $4, $5, $6, $7, $8)
			RETURNING id, number, started_at`, projectID, commit, d.Image, a.Why, a.By, fromID, d.Tickets, d.State).Scan(&d.ID, &d.Number, &d.StartedAt)
	})
	var pg *pgconn.PgError
	if errors.As(err, &pg) && pg.ConstraintName == "deploys_one_running" {
		var n int
		if sErr := s.Pool.QueryRow(ctx, `SELECT number FROM deploys WHERE project = $1 AND state IN ('building', 'starting', 'checking')`, projectID).Scan(&n); sErr != nil && !errors.Is(sErr, pgx.ErrNoRows) {
			return Deploy{}, sErr
		}
		return Deploy{}, &PreconditionError{fmt.Sprintf("deploy %d is running", n)}
	}
	return d, err
}

// live is the deploy that serves a project, with its sandbox and address; ok false when none does.
type live struct {
	deploy           Deploy
	sandbox, address string
	ok               bool
}

func (s *Service) liveOf(ctx context.Context, projectID string) (live, error) {
	var l live
	var id *int64
	var sb, addr *string
	err := s.Pool.QueryRow(ctx, `SELECT live_deploy, sandbox, host(address) FROM hosting WHERE project = $1`, projectID).Scan(&id, &sb, &addr)
	if errors.Is(err, pgx.ErrNoRows) || err == nil && id == nil {
		return l, nil
	}
	if err != nil {
		return l, err
	}
	if l.deploy, err = s.deployByID(ctx, *id); err != nil {
		return l, err
	}
	if sb != nil {
		l.sandbox = *sb
	}
	if addr != nil {
		l.address = *addr
	}
	l.ok = true
	return l, nil
}

const deployCols = `id, number, commit, coalesce(image, ''), why, tickets, state, detail, coalesce(data_copy, ''), coalesce(from_deploy, 0), started_at, live_at`

func scanDeploy(r pgx.Row) (Deploy, error) {
	var d Deploy
	err := r.Scan(&d.ID, &d.Number, &d.Commit, &d.Image, &d.Why, &d.Tickets, &d.State, &d.Detail, &d.DataCopy, &d.From, &d.StartedAt, &d.LiveAt)
	return d, err
}

func (s *Service) deployByID(ctx context.Context, id int64) (Deploy, error) {
	return scanDeploy(s.Pool.QueryRow(ctx, `SELECT `+deployCols+` FROM deploys WHERE id = $1`, id))
}

func (s *Service) deployByNumber(ctx context.Context, projectID string, n int) (Deploy, error) {
	return scanDeploy(s.Pool.QueryRow(ctx, `SELECT `+deployCols+` FROM deploys WHERE project = $1 AND number = $2`, projectID, n))
}

// canRollBack says whether a deploy can be switched back to: it served once, and its image is kept.
func (s *Service) canRollBack(ctx context.Context, projectID string, d Deploy) (bool, error) {
	if d.LiveAt == nil || d.Image == "" || s.Runtime == nil {
		return false, nil
	}
	return s.Runtime.HasImage(ctx, ImageRef(projectID, d.Number))
}

// copyOf is the copy of the data as deploy d left it: the one the deploy that replaced it made before
// it switched (plan M19 D15, D18), when it is still kept; "" when there is none.
func (s *Service) copyOf(ctx context.Context, projectID string, d Deploy) (string, error) {
	var c *string
	err := s.Pool.QueryRow(ctx, `SELECT data_copy FROM deploys WHERE project = $1 AND number > $2 AND live_at IS NOT NULL
		ORDER BY number LIMIT 1`, projectID, d.Number).Scan(&c)
	if errors.Is(err, pgx.ErrNoRows) || err == nil && c == nil {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	if fi, err := os.Lstat(s.copyDir(projectID, *c)); err != nil || !fi.IsDir() {
		return "", nil
	}
	return *c, nil
}

// carry carries a deploy on from its row to live or failed (19.2 steps 3 to 7).
func (s *Service) carry(ctx context.Context, p project, st Settings, d Deploy, a Ask, from Deploy) {
	sandbox := SandboxID(p.id, d.Number)
	cur, err := s.liveOf(ctx, p.id)
	if err != nil {
		s.fail(ctx, p.id, d, "reading what serves: "+err.Error())
		return
	}
	values, err := s.values(ctx, p.id)
	if errors.Is(err, credstore.ErrUnreadable) {
		s.fail(ctx, p.id, d, "the values must be entered again: they were sealed with a key file that is gone (21)")
		return
	}
	if err != nil {
		s.fail(ctx, p.id, d, "reading the values: "+err.Error())
		return
	}
	if from.ID == 0 {
		img, why := s.build(ctx, p, st, d)
		if why != "" {
			s.fail(ctx, p.id, d, why)
			return
		}
		d.Image = img
	} else if st.Port > 0 {
		// A roll back's or a settings deploy's image was checked when it was built, against the settings
		// of then; the settings of now are what it starts with.
		if why := s.checkLoaded(ctx, d.Image, st); why != "" {
			s.fail(ctx, p.id, d, why)
			return
		}
	}
	// Every deploy names its image by its own number, as a build does, so one that took another's
	// image can be rolled back to, and the images are kept by deploy (plan M19 D30).
	if from.ID != 0 {
		if err := s.Runtime.TagImage(ctx, d.Image, ImageRef(p.id, d.Number)); err != nil {
			s.fail(ctx, p.id, d, "tagging the image: "+err.Error())
			return
		}
	}
	if err := os.MkdirAll(s.dataDir(p.id), 0o750); err != nil {
		s.fail(ctx, p.id, d, "the data folder: "+err.Error())
		return
	}
	if cur.ok && cur.sandbox != "" {
		name := strconv.Itoa(d.Number)
		if why := s.copyData(ctx, p.id, cur.sandbox, name); why != "" {
			s.fail(ctx, p.id, d, why)
			return
		}
		d.DataCopy = name
		if _, err := s.Pool.Exec(ctx, `UPDATE deploys SET data_copy = $2 WHERE id = $1`, d.ID, name); err != nil {
			s.fail(ctx, p.id, d, "recording the copy of the data: "+err.Error())
			return
		}
	}
	restore := ""
	if a.RestoreData {
		if restore, err = s.copyOf(ctx, p.id, from); err != nil || restore == "" {
			s.fail(ctx, p.id, d, fmt.Sprintf("the copy of the data deploy %d left is gone", from.Number))
			return
		}
	} else {
		s.pruneCopies(ctx, p.id, st.DataCopies, "")
	}
	if restore != "" {
		s.rollBackWithData(ctx, p, st, d, cur, sandbox, restore, values)
		return
	}
	addr, why := s.startAndCheck(ctx, p, st, d, sandbox, values)
	if why != "" {
		s.fail(ctx, p.id, d, why)
		return
	}
	s.switchTo(ctx, p, st, d, cur, sandbox, addr)
}

// rollBackWithData stops what serves, puts back the data as the deploy rolled back to left it, and
// starts that deploy's image; when that fails, the data this deploy copied and the version that
// served are put back (plan M19 D15).
func (s *Service) rollBackWithData(ctx context.Context, p project, st Settings, d Deploy, cur live, sandbox, restore string, values map[string]string) {
	// The copy it puts back is named before what serves stops, so a restart in between knows this roll
	// back changes the data (Recover); a roll back that fails clears it once what served is back.
	if _, err := s.Pool.Exec(ctx, `UPDATE deploys SET restored = $2 WHERE id = $1`, d.ID, restore); err != nil {
		s.fail(ctx, p.id, d, "recording the data a roll back puts back: "+err.Error())
		return
	}
	if cur.ok && cur.sandbox != "" {
		if err := s.Runtime.StopHosted(ctx, cur.sandbox); err != nil {
			s.notRestored(ctx, d)
			s.fail(ctx, p.id, d, "stopping what serves: "+err.Error())
			return
		}
	}
	why, addr := "", ""
	if err := replaceData(s.dataDir(p.id), s.copyDir(p.id, restore)); err != nil {
		why = "putting back the data of before: " + err.Error()
	} else if addr, why = s.startAndCheck(ctx, p, st, d, sandbox, values); why == "" {
		s.pruneCopies(ctx, p.id, st.DataCopies, restore)
		s.switchTo(ctx, p, st, d, cur, sandbox, addr)
		return
	}
	// What served comes back, with the data as it was when this deploy began.
	back := []string{why}
	bctx := context.WithoutCancel(ctx)
	if d.DataCopy != "" {
		if err := replaceData(s.dataDir(p.id), s.copyDir(p.id, d.DataCopy)); err != nil {
			back = append(back, "the data could not be put back: "+err.Error())
		} else {
			back = append(back, "the data was put back")
		}
	}
	if cur.ok && cur.sandbox != "" {
		if err := s.Runtime.StartAgain(bctx, cur.sandbox); err != nil {
			back = append(back, fmt.Sprintf("deploy %d could not be started again: %v", cur.deploy.Number, err))
		} else {
			back = append(back, fmt.Sprintf("deploy %d serves again", cur.deploy.Number))
		}
	}
	// Cleared only now: a restart before this finishes the put back itself (recoverRollBacks), which a
	// cleared copy would keep it from (review of the release of M20).
	s.notRestored(bctx, d)
	s.fail(bctx, p.id, d, strings.Join(back, "; "))
}

// notRestored clears the copy a roll back that failed named: it put nothing back that stays.
func (s *Service) notRestored(ctx context.Context, d Deploy) {
	if _, err := s.Pool.Exec(ctx, `UPDATE deploys SET restored = NULL WHERE id = $1`, d.ID); err != nil {
		s.Log.Warn("clearing the data a roll back named", "deploy", d.ID, "error", err)
	}
}

// build builds the deploy's image from its commit, cleans it, checks its config, loads it and tags
// it (19.2 step 3, plan M19 D1, D20, D30), and gives its id; or why it could not.
func (s *Service) build(ctx context.Context, p project, st Settings, d Deploy) (string, string) {
	var tree bytes.Buffer
	if err := s.Git.Archive(ctx, p.name, d.Commit, &limited{w: &tree, left: harness.MaxTree}); err != nil {
		return "", "the commit could not be read: " + err.Error()
	}
	mode, err := settings.CheckNetworkMode(ctx, s.Pool, p.id, 0)
	if err != nil {
		return "", "the build's network mode: " + err.Error()
	}
	dir := s.buildDir(p.id, d.Number)
	defer os.RemoveAll(dir)
	key := fmt.Sprintf("host-build:%s:%d", p.id, d.Number)
	sizing, recorded, err := s.room(ctx, key, capacity.Build, 0, "the build of "+p.name+"'s image")
	if err != nil {
		return "", "waiting for room: " + err.Error()
	}
	res, err := s.Builder.BuildImage(ctx, harness.ImageSpec{Project: p.id, Mode: mode, Tree: tree.Bytes(), Dockerfile: st.Dockerfile,
		Out: filepath.Join(dir, harness.ImageFile), Timeout: BuildTimeout, Sizing: sizing, Recorded: recorded})
	recorded()
	if err != nil {
		return "", "the build failed: " + err.Error() + lastLines(res.Output)
	}
	img, why := s.cleanAndLoad(ctx, p.id, dir, st)
	if why != "" {
		return "", why
	}
	if err := s.Runtime.TagImage(ctx, img, ImageRef(p.id, d.Number)); err != nil {
		return "", "tagging the image: " + err.Error()
	}
	if _, err := s.Pool.Exec(ctx, `UPDATE deploys SET image = $2, state = 'starting' WHERE id = $1`, d.ID, img); err != nil {
		return "", "recording the image: " + err.Error()
	}
	return img, ""
}

// cleanAndLoad cleans the build's tar, checks the image's config, and loads it, requiring the id
// Docker gives to be the config's digest.
func (s *Service) cleanAndLoad(ctx context.Context, projectID, dir string, st Settings) (string, string) {
	src, err := os.Open(filepath.Join(dir, harness.ImageFile))
	if err != nil {
		return "", "the build left no image: " + err.Error()
	}
	defer src.Close()
	fi, err := src.Stat()
	if err != nil {
		return "", err.Error()
	}
	free, err := harness.FreeSpace(dir)
	if err != nil {
		return "", err.Error()
	}
	if free < DiskTimesTar*uint64(fi.Size())+harness.DiskMargin {
		return "", fmt.Sprintf("the image was refused: the disk has %d bytes free, and loading %d needs three times that and a margin of %d", free, fi.Size(), int64(harness.DiskMargin))
	}
	cleaned := filepath.Join(dir, "clean.tar")
	out, err := os.OpenFile(cleaned, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o640)
	if err != nil {
		return "", err.Error()
	}
	img, err := CleanImage(src, out)
	if cerr := out.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return "", "the image was refused: " + err.Error()
	}
	if why := ConfigProblem(img, st.Port, st.Data); why != "" {
		return "", "the image was refused: " + why
	}
	in, err := os.Open(cleaned)
	if err != nil {
		return "", err.Error()
	}
	defer in.Close()
	id, err := s.Runtime.LoadImage(ctx, in)
	if err != nil {
		return "", "the image was refused: " + err.Error()
	}
	// Docker's overlay2 store knows the image by its config's digest, containerd's by its manifest's
	// (plan M19 D42): either is the image the cleaner read, and none other.
	if id != img.ID && (img.Manifest == "" || id != img.Manifest) {
		_ = s.Runtime.RemoveImage(context.WithoutCancel(ctx), id)
		return "", fmt.Sprintf("the image was refused: Docker loaded %s, and the image's config is %s", id, img.ID)
	}
	return id, ""
}

// checkLoaded is the config check of an image Docker holds already, for a roll back or a settings
// deploy, whose settings may have changed since its build.
func (s *Service) checkLoaded(ctx context.Context, id string, st Settings) string {
	var img Image
	var labels []string
	var err error
	img.Volumes, img.Ports, labels, err = s.Runtime.ImageConfig(ctx, id)
	if err != nil {
		return "the image is no longer kept: " + err.Error()
	}
	// A kept image's labels are checked again too: one kept before the check had none (security
	// review of M19, round 2).
	img.Labels = crewLabels(labels)
	if why := ConfigProblem(img, st.Port, st.Data); why != "" {
		return "the image was refused: " + why
	}
	return ""
}

// startAndCheck starts the deploy's sandbox beside what serves, on a bridge of its own proved by the
// probe, and asks its health address until it answers (19.2 steps 4 and 5); it gives the app's
// address on its bridge, or why it could not, with the new sandbox removed.
func (s *Service) startAndCheck(ctx context.Context, p project, st Settings, d Deploy, sandbox string, values map[string]string) (addr, why string) {
	defer func() {
		if why != "" {
			s.removeSandbox(context.WithoutCancel(ctx), sandbox)
		}
	}()
	key := fmt.Sprintf("host:%s:%d", p.id, d.Number)
	size := capacity.Small
	if st.Size == capacity.Large {
		size = capacity.Large
	}
	sizing, recorded, err := s.room(ctx, key, size, capacity.HostedRank, "hosting "+p.name)
	if err != nil {
		return "", "waiting for room: " + err.Error()
	}
	defer recorded()
	// The row first, which the capacity check counts, so its lock is not held over the bridge and the
	// probe (review of M19, finding 4).
	mode := "hosted_offline"
	if st.Internet {
		mode = "hosted_internet"
	}
	if _, err := s.Pool.Exec(ctx, `INSERT INTO sandboxes (id, kind, project, container, network, image, size, mode)
		VALUES ($1, 'hosted', $2, $3, $3, $4, $5, $6)`, sandbox, p.id, orchestrator.SandboxName(sandbox), d.Image, size, mode); err != nil {
		return "", "recording the sandbox: " + err.Error()
	}
	recorded()
	n, err := s.Runtime.CreateHostedNet(ctx, sandbox, st.Internet)
	if err != nil {
		return "", "the sandbox did not start: " + err.Error()
	}
	if err := s.Runtime.ProbeHosted(ctx, sandbox, n); err != nil {
		return "", err.Error()
	}
	env := make([]string, 0, len(values))
	for k, v := range values {
		env = append(env, k+"="+v)
	}
	slices.Sort(env)
	if _, err := s.Runtime.StartHosted(ctx, orchestrator.HostedSpec{ID: sandbox, Project: p.id, Image: d.Image, Port: st.Port,
		DataDir: s.dataDir(p.id), DataMount: st.Data, Env: env, Sizing: sizing, Net: n}); err != nil {
		return "", "the sandbox did not start: " + err.Error()
	}
	s.logLine(p.id, "deploy %d started in %s", d.Number, sandbox)
	if _, err := s.Pool.Exec(ctx, `UPDATE deploys SET state = 'checking' WHERE id = $1`, d.ID); err != nil {
		return "", "recording the check: " + err.Error()
	}
	if err := s.waitHealthy(ctx, n.App, st); err != nil {
		tail := ""
		if out, lerr := s.Runtime.HostedLogs(context.WithoutCancel(ctx), sandbox, time.Time{}); lerr == nil {
			tail = lastLines(string(out))
		}
		return "", err.Error() + tail
	}
	return n.App, ""
}

// waitHealthy asks http://<addr>:<port><health> every CheckEvery until it answers 2xx, at most
// CheckTimeout; a redirect is no answer.
func (s *Service) waitHealthy(ctx context.Context, addr string, st Settings) error {
	url := "http://" + net.JoinHostPort(addr, strconv.Itoa(st.Port)) + st.Health
	deadline := time.Now().Add(CheckTimeout)
	for {
		if s.answers(ctx, url) {
			return nil
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("%s did not answer in %s", st.Health, CheckTimeout)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(CheckEvery):
		}
	}
}

// healthClient asks a hosted project's health address on its bridge: no proxy, no redirect.
var healthClient = &http.Client{
	Timeout:       5 * time.Second,
	Transport:     &http.Transport{Proxy: nil, DisableKeepAlives: true},
	CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
}

func (s *Service) answers(ctx context.Context, url string) bool {
	c := s.HealthClient
	if c == nil {
		c = healthClient
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return false
	}
	resp, err := c.Do(req)
	if err != nil {
		return false
	}
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 64<<10))
	resp.Body.Close()
	return resp.StatusCode >= 200 && resp.StatusCode < 300
}

// switchTo makes the checked deploy the one that serves, in one transaction, with the port and the
// health address it was checked on, which the route and health read until the next switch (review of
// M19, finding 3), and removes the one before RetireAfter later, when the ingress took up the new
// route (19.2 step 6).
func (s *Service) switchTo(ctx context.Context, p project, st Settings, d Deploy, cur live, sandbox, addr string) {
	err := pgx.BeginFunc(ctx, s.Pool, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `UPDATE hosting SET state = 'live', live_deploy = $2, sandbox = $3, address = $4::inet,
				retiring = $5, restarts = 0, down_since = NULL, port = $6, health = $7, updated_at = now()
			WHERE project = $1`, p.id, d.ID, sandbox, addr, nullable(cur.sandbox), st.Port, st.Health); err != nil {
			return err
		}
		// A roll back leaves main's head behind on purpose: Follow main does not deploy it again, as it
		// does not a head that failed, until a newer one (review of M19, finding 7).
		if d.Why == "rollback" {
			if head := s.Git.Main(ctx, p.name); head != "" {
				if _, err := tx.Exec(ctx, `UPDATE hosting SET failed_head = $2 WHERE project = $1`, p.id, head); err != nil {
					return err
				}
			}
		}
		if _, err := tx.Exec(ctx, `UPDATE deploys SET state = 'replaced', ended_at = now() WHERE project = $1 AND state = 'live'`, p.id); err != nil {
			return err
		}
		_, err := tx.Exec(ctx, `UPDATE deploys SET state = 'live', live_at = now() WHERE id = $1`, d.ID)
		return err
	})
	if err != nil {
		s.fail(context.WithoutCancel(ctx), p.id, d, "switching: "+err.Error())
		return
	}
	s.logLine(p.id, "deploy %d is live", d.Number)
	s.Log.Info("a hosted project switched", "project", p.name, "deploy", d.Number, "sandbox", sandbox)
	s.pruneImages(context.WithoutCancel(ctx), p.id)
	if cur.ok && cur.sandbox != "" {
		s.wg.Add(1)
		go func() {
			defer s.wg.Done()
			select {
			case <-time.After(RetireAfter):
			case <-s.base().Done():
				return
			}
			s.retire(context.WithoutCancel(s.base()), p.id, cur.sandbox)
		}()
	}
}

func nullable(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

// retire removes a replaced sandbox and clears it from the hosting row.
func (s *Service) retire(ctx context.Context, projectID, sandbox string) {
	s.removeSandbox(ctx, sandbox)
	if _, err := s.Pool.Exec(ctx, `UPDATE hosting SET retiring = NULL WHERE project = $1 AND retiring = $2`, projectID, sandbox); err != nil {
		s.Log.Warn("clearing a retired hosted sandbox", "sandbox", sandbox, "error", err)
	}
	s.logLine(projectID, "%s was removed", sandbox)
}

// removeSandbox removes a hosted sandbox, its bridge and its row.
func (s *Service) removeSandbox(ctx context.Context, sandbox string) {
	if err := s.Runtime.RemoveSandbox(ctx, sandbox); err != nil {
		s.Log.Warn("removing a hosted sandbox", "sandbox", sandbox, "error", err)
		return
	}
	if _, err := s.Pool.Exec(ctx, `UPDATE sandboxes SET removed_at = now() WHERE id = $1 AND removed_at IS NULL`, sandbox); err != nil {
		s.Log.Warn("closing a hosted sandbox's row", "sandbox", sandbox, "error", err)
	}
}

// fail ends a deploy as failed with why, in Crew's words; what served serves on. A project that had
// nothing live is stopped.
func (s *Service) fail(ctx context.Context, projectID string, d Deploy, why string) {
	ctx = context.WithoutCancel(ctx)
	if len(why) > 4000 {
		why = strings.ToValidUTF8(why[:3990], "") + " …"
	}
	if _, err := s.Pool.Exec(ctx, `UPDATE deploys SET state = 'failed', detail = $2, ended_at = now() WHERE id = $1 AND state IN ('building', 'starting', 'checking')`,
		d.ID, why); err != nil {
		s.Log.Error("recording a failed deploy", "deploy", d.ID, "error", err)
	}
	if _, err := s.Pool.Exec(ctx, `UPDATE hosting SET state = 'stopped', updated_at = now() WHERE project = $1 AND live_deploy IS NULL AND state = 'deploying'`,
		projectID); err != nil {
		s.Log.Error("recording a hosting with nothing live", "project", projectID, "error", err)
	}
	if d.Why == "follow_main" || d.Why == "hostable" {
		if _, err := s.Pool.Exec(ctx, `UPDATE hosting SET failed_head = $2 WHERE project = $1`, projectID, d.Commit); err != nil {
			s.Log.Error("recording a head that failed", "project", projectID, "error", err)
		}
	}
	// Its own tag goes; an image another deploy names stays.
	if d.Number > 0 {
		_ = s.Runtime.RemoveImage(ctx, ImageRef(projectID, d.Number))
	}
	s.logLine(projectID, "deploy %d failed: %s", d.Number, why)
	s.Log.Warn("a deploy failed", "project", projectID, "deploy", d.Number, "why", why)
}

// pruneImages keeps the newest hosting.keep_images images of a project's deploys that served, and
// never the live one's or one a running deploy uses (plan M19 D30).
func (s *Service) pruneImages(ctx context.Context, projectID string) {
	keep := 3
	if v, err := settings.MachineValue(ctx, s.Pool, "hosting.keep_images"); err == nil {
		if n := num(v); n > 0 {
			keep = n
		}
	}
	rows, err := s.Pool.Query(ctx, `SELECT number FROM deploys d WHERE project = $1 AND image IS NOT NULL AND live_at IS NOT NULL
		AND state NOT IN ('live', 'building', 'starting', 'checking')
		AND NOT EXISTS (SELECT 1 FROM deploys r WHERE r.project = d.project AND r.from_deploy = d.id AND r.state IN ('building', 'starting', 'checking'))
		ORDER BY number DESC OFFSET $2`, projectID, max(keep-1, 0))
	if err != nil {
		s.Log.Warn("listing the images to remove", "project", projectID, "error", err)
		return
	}
	numbers, err := pgx.CollectRows(rows, pgx.RowTo[int])
	if err != nil {
		return
	}
	for _, n := range numbers {
		if err := s.Runtime.RemoveImage(ctx, ImageRef(projectID, n)); err != nil {
			s.Log.Warn("removing a hosted project's image", "project", projectID, "deploy", n, "error", err)
		}
	}
}

// room waits until the capacity check gives the sandbox room (13.10), asking again every half
// minute, and holds the check's lock until its row exists: recorded lets it go, once. With no check
// (a test) there is room at the base size.
func (s *Service) room(ctx context.Context, key, size string, rank int, what string) (orchestrator.Sizing, func(), error) {
	if s.Room == nil {
		return orchestrator.Sizing{}, func() {}, nil
	}
	ask := capacity.Ask{Key: key, Owner: "hosting", Size: size, Rank: rank, What: what}
	for {
		s.Room.Mu.Lock()
		ok, err := s.Room.Take(ctx, ask)
		if err == nil && ok {
			break
		}
		s.Room.Mu.Unlock()
		if err != nil {
			return orchestrator.Sizing{}, nil, err
		}
		select {
		case <-ctx.Done():
			_ = s.Room.Taken(context.WithoutCancel(ctx), key)
			return orchestrator.Sizing{}, nil, ctx.Err()
		case <-time.After(roomEvery):
		}
	}
	var once sync.Once
	recorded := func() {
		once.Do(func() {
			if err := s.Room.Taken(context.WithoutCancel(ctx), key); err != nil {
				s.Log.Error("taking a hosted start out of the queue for room", "error", err)
			}
			s.Room.Mu.Unlock()
		})
	}
	sz, err := s.Room.SizeFor(ctx, size)
	if err != nil {
		recorded()
		return orchestrator.Sizing{}, nil, err
	}
	return sz.Sizing(), recorded, nil
}

// Stop stops hosting a project (19.5): a running deploy is ended, the sandbox removed, the route gone;
// the data, its copies and the images are kept.
func (s *Service) Stop(ctx context.Context, projectID string) error {
	if s.Runtime == nil {
		return &PreconditionError{"hosting needs Docker, which this server does not reach"}
	}
	s.run.stop(projectID, stopWaitLimit)
	cur, err := s.liveOf(ctx, projectID)
	if err != nil {
		return err
	}
	var retiring *string
	err = pgx.BeginFunc(ctx, s.Pool, func(tx pgx.Tx) error {
		if err := tx.QueryRow(ctx, `UPDATE hosting SET state = 'stopped', live_deploy = NULL, sandbox = NULL, address = NULL, down_since = NULL,
			updated_at = now() WHERE project = $1 RETURNING retiring`, projectID).Scan(&retiring); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `UPDATE deploys SET state = 'failed', detail = 'stopped while it ran', ended_at = now()
			WHERE project = $1 AND state IN ('building', 'starting', 'checking')`, projectID); err != nil {
			return err
		}
		_, err := tx.Exec(ctx, `UPDATE deploys SET state = 'stopped', ended_at = now() WHERE project = $1 AND state = 'live'`, projectID)
		return err
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return &PreconditionError{"the project is not hosted"}
	}
	if err != nil {
		return err
	}
	bctx := context.WithoutCancel(ctx)
	if cur.sandbox != "" {
		s.removeSandbox(bctx, cur.sandbox)
	}
	if retiring != nil {
		s.retire(bctx, projectID, *retiring)
	}
	s.logLine(projectID, "hosting stopped")
	return nil
}

// lastLines is the end of an output, for a deploy's detail.
func lastLines(out string) string {
	out = strings.TrimSpace(out)
	if out == "" {
		return ""
	}
	lines := strings.Split(out, "\n")
	if len(lines) > 20 {
		lines = lines[len(lines)-20:]
	}
	tail := strings.Join(lines, "\n")
	if len(tail) > 2000 {
		tail = strings.ToValidUTF8(tail[len(tail)-2000:], "")
	}
	return "\n" + tail
}

// limited takes at most left bytes and then fails.
type limited struct {
	w    io.Writer
	left int
}

func (l *limited) Write(p []byte) (int, error) {
	if len(p) > l.left {
		return 0, fmt.Errorf("the tree is larger than %d bytes", harness.MaxTree)
	}
	l.left -= len(p)
	return l.w.Write(p)
}
