// Package hold is the one switch that holds all of Crew (7.9, 16.10, 21): Pause all, Stop all, two
// microVMs in a row that did not boot, the disk guard's stop, and a restart for security updates. Every start of a run, a turn, or a check asks it, and so do the clocks of
// notices and Crew's own changes of a model. It is one row of the database, so a restart keeps it.
package hold

import (
	"context"
	"errors"
	"time"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"

	"github.com/kesokaj/crew/internal/db"
)

// State is whether Crew is held, and why.
type State struct {
	// PausedAt is set while the operator paused Crew; Why is pause or stop.
	PausedAt *time.Time `json:"pausedAt,omitempty"`
	PausedBy string     `json:"pausedBy,omitempty"`
	Why      string     `json:"why,omitempty"`
	// DiskFull is the guard's reason while the data folder is too full to start anything.
	DiskFull string     `json:"diskFull,omitempty"`
	DiskAt   *time.Time `json:"diskAt,omitempty"`
	// BootFailed is the error of the second microVM in a row that did not boot (21, D10): while it is
	// set, nothing new starts but the probe. BootFailures counts the failures since the last boot.
	BootFailed   string     `json:"bootFailed,omitempty"`
	BootFailedAt *time.Time `json:"bootFailedAt,omitempty"`
	BootFailures int        `json:"bootFailures"`
	// RebootWanted is when crew-ctl first said the machine needs a restart for security updates
	// (16.10); RestartWait is set once it waited 7 days, and holds until the machine restarted.
	// Restarting is a yes to crew-ctl in the last ten minutes: the machine is going down.
	RebootWanted *time.Time `json:"rebootWanted,omitempty"`
	RestartWait  *time.Time `json:"restartWait,omitempty"`
	Restarting   bool       `json:"restarting,omitempty"`
}

// What the top bar and every start say while a restart for security updates holds Crew (16.10).
const (
	RestartingWhy  = "A restart for security updates: the machine restarts now"
	RestartWaitWhy = "A restart for security updates waits: runs finish first"
)

// BootsInARow is how many microVMs in a row that do not boot hold Crew (21).
const BootsInARow = 2

// Held says why nothing new starts now, or "" when it may.
func (s State) Held() string {
	switch {
	case s.Why == "stop":
		return "Crew is stopped: Stop all"
	case s.PausedAt != nil:
		return "Crew is paused: Pause all"
	case s.BootFailed != "":
		return "MicroVMs do not start: " + s.BootFailed
	case s.DiskFull != "":
		return "The disk is too full: " + s.DiskFull
	case s.Restarting:
		return RestartingWhy
	case s.RestartWait != nil:
		return RestartWaitWhy
	}
	return ""
}

// Read reads the state.
func Read(ctx context.Context, q db.Querier) (State, error) {
	var s State
	var by, why, disk, boot *string
	err := q.QueryRow(ctx, `SELECT paused_at, paused_by, why, disk_full, disk_at, boot_failed, boot_failed_at, boot_failures,
			reboot_wanted_at, restart_wait, coalesce(restart_at > now() - make_interval(secs => $1), false)
		FROM crew_state WHERE id = 1`, RestartingFor.Seconds()).
		Scan(&s.PausedAt, &by, &why, &disk, &s.DiskAt, &boot, &s.BootFailedAt, &s.BootFailures,
			&s.RebootWanted, &s.RestartWait, &s.Restarting)
	if errors.Is(err, pgx.ErrNoRows) {
		return s, nil
	}
	s.PausedBy, s.Why, s.DiskFull, s.BootFailed = deref(by), deref(why), deref(disk), deref(boot)
	return s, err
}

// Held says why nothing new starts now, or "". A state that cannot be read holds: a brake that
// fails open is no brake.
func Held(ctx context.Context, q db.Querier) string {
	s, err := Read(ctx, q)
	if err != nil {
		return "Crew cannot read whether it is paused: " + err.Error()
	}
	return s.Held()
}

// Pause holds Crew for the operator: why is pause, or stop once Stop all ended the runs. A pause
// that is there already keeps its time, so its clocks move on by the whole of it.
func Pause(ctx context.Context, tx pgx.Tx, why, by string) (State, error) {
	before, err := Read(ctx, tx)
	if err != nil {
		return before, err
	}
	if _, err := tx.Exec(ctx, `UPDATE crew_state SET paused_at = coalesce(paused_at, now()), paused_by = $2,
		why = CASE WHEN why = 'stop' THEN 'stop' ELSE $1 END WHERE id = 1`, why, by); err != nil {
		return before, err
	}
	if err := db.RecordChange(ctx, tx, by, "crew_state", "1", before, map[string]any{"why": why}); err != nil {
		return before, err
	}
	return Read(ctx, tx)
}

// Resume lets Crew start again: every notice whose clock ran when Crew was paused gets the time it
// was paused added (7.9). It says how long Crew was paused.
func Resume(ctx context.Context, tx pgx.Tx, by string) (time.Duration, error) {
	before, err := Read(ctx, tx)
	if err != nil || before.PausedAt == nil {
		return 0, err
	}
	var paused time.Duration
	if err := tx.QueryRow(ctx, `SELECT now() - paused_at FROM crew_state WHERE id = 1`).Scan(&paused); err != nil {
		return 0, err
	}
	if _, err := tx.Exec(ctx, `UPDATE tickets SET notice_until = notice_until + (now() - $1::timestamptz)
		WHERE notice_until IS NOT NULL AND notice_until > $1`, *before.PausedAt); err != nil {
		return 0, err
	}
	if _, err := tx.Exec(ctx, `UPDATE crew_state SET paused_at = NULL, paused_by = NULL, why = NULL WHERE id = 1`); err != nil {
		return 0, err
	}
	return paused, db.RecordChange(ctx, tx, by, "crew_state", "1", before, map[string]any{"why": nil})
}

// SetDisk records the disk guard's stop, or clears it with "".
func SetDisk(ctx context.Context, q db.Querier, why string) error {
	_, err := q.Exec(ctx, `UPDATE crew_state SET disk_full = nullif($1, ''),
		disk_at = CASE WHEN $1 = '' THEN NULL ELSE coalesce(disk_at, now()) END WHERE id = 1`, why)
	return err
}

// Boot counts a start that reached Docker's (21, D10): a microVM that did not boot adds one, and the
// second in a row holds Crew with why; one that booted, a run's, a check's, or the probe's, sets the
// count to 0 and lifts that hold. It says whether this call set the hold.
func Boot(ctx context.Context, q db.Querier, ok bool, why string) (bool, error) {
	if ok {
		_, err := q.Exec(ctx, `UPDATE crew_state SET boot_failures = 0, boot_failed = NULL, boot_failed_at = NULL
			WHERE id = 1 AND (boot_failures <> 0 OR boot_failed IS NOT NULL)`)
		return false, err
	}
	if why == "" {
		why = "a microVM did not boot"
	}
	why = Cut(why, maxWhy)
	var held bool
	err := q.QueryRow(ctx, `UPDATE crew_state SET boot_failures = boot_failures + 1,
			boot_failed = CASE WHEN boot_failures + 1 >= $2 THEN $1 ELSE boot_failed END,
			boot_failed_at = CASE WHEN boot_failures + 1 >= $2 THEN coalesce(boot_failed_at, now()) ELSE boot_failed_at END
		WHERE id = 1 RETURNING boot_failures = $2`, why, BootsInARow).Scan(&held)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	return held, err
}

// ResumeBoot is the operator's Resume of the boot hold (21, D22): the hold goes and the count is 0,
// so it takes two more failures in a row to hold again.
func ResumeBoot(ctx context.Context, tx pgx.Tx, by string) error {
	before, err := Read(ctx, tx)
	if err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `UPDATE crew_state SET boot_failures = 0, boot_failed = NULL, boot_failed_at = NULL WHERE id = 1`); err != nil {
		return err
	}
	return db.RecordChange(ctx, tx, by, "crew_state", "1", map[string]any{"bootFailed": before.BootFailed, "bootFailures": before.BootFailures},
		map[string]any{"bootFailed": nil, "bootFailures": 0})
}

// Unwritable is a time the database could not be written, kept once it could again until the
// operator has read it (21). From is nil when it is not known: Crew restarted while it could not
// write, and kept no time of its own.
type Unwritable struct {
	From *time.Time `json:"from,omitempty"`
	To   time.Time  `json:"to"`
}

// ReadUnwritable reads the time the database could not be written, or nil.
func ReadUnwritable(ctx context.Context, q db.Querier) (*Unwritable, error) {
	var u Unwritable
	var to *time.Time
	err := q.QueryRow(ctx, `SELECT db_unwritable_from, db_unwritable_to FROM crew_state WHERE id = 1`).Scan(&u.From, &to)
	if errors.Is(err, pgx.ErrNoRows) || (err == nil && to == nil) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	u.To = *to
	return &u, nil
}

// WritableAgain records that the database can be written again, and since when it could not: the
// write probe's first write after a failure.
func WritableAgain(ctx context.Context, q db.Querier, from *time.Time, to time.Time) error {
	_, err := q.Exec(ctx, `UPDATE crew_state SET db_unwritable_from = $1, db_unwritable_to = $2 WHERE id = 1`, from, to)
	return err
}

// ReadWritable is the operator's Read it of the item: the time goes.
func ReadWritable(ctx context.Context, tx pgx.Tx, by string) error {
	before, err := ReadUnwritable(ctx, tx)
	if err != nil || before == nil {
		return err
	}
	if _, err := tx.Exec(ctx, `UPDATE crew_state SET db_unwritable_from = NULL, db_unwritable_to = NULL WHERE id = 1`); err != nil {
		return err
	}
	return db.RecordChange(ctx, tx, by, "crew_state", "1", map[string]any{"dbUnwritableFrom": before.From, "dbUnwritableTo": before.To},
		map[string]any{"dbUnwritableFrom": nil, "dbUnwritableTo": nil})
}

// RestartingFor is how long a yes to crew-ctl holds new runs (16.10): the machine goes down in it, and
// a restart that did not happen lets Crew start again after it.
const RestartingFor = 10 * time.Minute

// maxWhy bounds the error that is kept and shown: Kata's can be a long stack (security review of
// 5d1b414).
const maxWhy = 600

// Cut keeps at most n bytes of s, cut at a rune and marked.
func Cut(s string, n int) string {
	if len(s) <= n {
		return s
	}
	cut := n
	for cut > 0 && !utf8.RuneStart(s[cut]) {
		cut--
	}
	return s[:cut] + " …"
}

func deref(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}
