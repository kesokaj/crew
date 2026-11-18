package board

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/kesokaj/crew/baseline"
	"github.com/kesokaj/crew/internal/db"
	"github.com/kesokaj/crew/internal/version"
)

// Override is a project's own version of one file of the baseline (10.3, 9.6): the whole file. It
// stays in its project.
type Override struct {
	ID         int64           `json:"-"`
	Project    string          `json:"-"`
	Number     int             `json:"number"`
	Path       string          `json:"path"`
	Version    int             `json:"version"`
	Text       string          `json:"text"`
	Why        string          `json:"why"`
	Status     string          `json:"status"`
	ProposedBy string          `json:"proposedBy"`
	Eval       json.RawMessage `json:"eval"`
	DecidedBy  *string         `json:"decidedBy"`
	DecidedWhy *string         `json:"decidedWhy"`
	AppliedAt  *time.Time      `json:"appliedAt"`
	AppliedOn  *string         `json:"appliedOn"`
	Measure    json.RawMessage `json:"measure"`
	CreatedAt  time.Time       `json:"createdAt"`
	EndedAt    *time.Time      `json:"endedAt"`
	// Waits says the override waits for the operator: a checking role's file, the prompt writer's, or
	// a project whose approvals.agent_change is operator.
	Waits bool `json:"waits"`
}

// The states of an override.
const (
	OverrideProposed  = "proposed"
	OverrideWaiting   = "waiting"
	OverrideTesting   = "testing"
	OverrideApplied   = "applied"
	OverrideRefused   = "refused"
	OverrideTakenBack = "taken_back"
	OverrideWithdrawn = "withdrawn"
	OverrideReplaced  = "replaced"
)

const overrideCols = `o.id, o.project::text, o.number, o.path, o.version, o.text, o.why, o.status, o.proposed_by, o.eval,
	o.decided_by, o.decided_why, o.applied_at, o.applied_on, o.measure, o.created_at, o.ended_at`

func scanOverride(row pgx.Row) (*Override, error) {
	var o Override
	if err := row.Scan(&o.ID, &o.Project, &o.Number, &o.Path, &o.Version, &o.Text, &o.Why, &o.Status, &o.ProposedBy, &o.Eval,
		&o.DecidedBy, &o.DecidedWhy, &o.AppliedAt, &o.AppliedOn, &o.Measure, &o.CreatedAt, &o.EndedAt); err != nil {
		return nil, err
	}
	o.Waits = o.Status == OverrideWaiting
	return &o, nil
}

// AppliedOverride is the override a project applies to a file of the baseline, or nil.
func AppliedOverride(ctx context.Context, q db.Querier, project, path string) (*Override, error) {
	if project == "" {
		return nil, nil
	}
	o, err := scanOverride(q.QueryRow(ctx, `SELECT `+overrideCols+` FROM overrides o WHERE o.project = $1 AND o.path = $2 AND o.status = 'applied'`, project, path))
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	return o, err
}

// BaselineText is a file of the baseline as it applies in a project: its override when one is
// applied, otherwise a global customization in crew_harness_files or the baseline's own (11.2, step 2).
// source names which, for the saved prompt.
func BaselineText(ctx context.Context, q db.Querier, project, path, crewVersion string) (text, source string, err error) {
	text, err = baseline.Text(path)
	if err != nil {
		return "", "", err
	}
	source = "baseline/" + path + "@" + crewVersion
	if q != nil {
		var custom string
		if err := q.QueryRow(ctx, `SELECT content FROM crew_harness_files WHERE path = $1`, "baseline/"+path).Scan(&custom); err == nil && strings.TrimSpace(custom) != "" {
			text = custom
			source = "baseline/" + path + "@custom"
		}
	}
	o, err := AppliedOverride(ctx, q, project, path)
	if err != nil || o == nil {
		return text, source, err
	}
	return o.Text, fmt.Sprintf("override o_%d v%d of %s", o.Number, o.Version, source), nil
}

// Overrides lists a project's overrides, newest first.
func (b *Board) Overrides(ctx context.Context, p *Project) ([]*Override, error) {
	rows, err := b.Pool.Query(ctx, `SELECT `+overrideCols+` FROM overrides o WHERE o.project = $1 ORDER BY o.number DESC`, p.ID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []*Override{}
	for rows.Next() {
		o, err := scanOverride(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, o)
	}
	return out, rows.Err()
}

// Override is one of a project's overrides by its number.
func (b *Board) Override(ctx context.Context, p *Project, number int) (*Override, error) {
	o, err := scanOverride(b.Pool.QueryRow(ctx, `SELECT `+overrideCols+` FROM overrides o WHERE o.project = $1 AND o.number = $2`, p.ID, number))
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, errf(NotFound, "no override o_%d", number)
	}
	return o, err
}

// ProposeOverride saves a new version of one file of the baseline for a project (9.6, R62). The
// operator's own applies at once: their text is kept, and it is measured like any other. One a lead
// proposes waits for the operator when it is a checking role's file, the prompt writer's, or the
// project says so; every other goes to the project's evals, in testing. A newer proposal for the
// same file withdraws one that has not been decided.
func (b *Board) ProposeOverride(ctx context.Context, p *Project, path, text, why, by, run string) (*Override, error) {
	if !baseline.Overridable(path) {
		return nil, errf(Invalid, "path: %q is no file of the baseline a project may replace: the markdown under agents/, stages/, guides/, or templates/", path)
	}
	if strings.TrimSpace(text) == "" || len(text) > 100000 {
		return nil, errf(Invalid, "text: the whole file, at most 100 kB")
	}
	if len(why) > 4000 {
		return nil, errf(Invalid, "why: at most 4000 characters")
	}
	var out *Override
	err := b.inTx(ctx, func(tx pgx.Tx) error {
		locked, err := projectByRef(ctx, tx, p.ID, true)
		if err != nil {
			return err
		}
		if locked.State == "archived" {
			return errf(Refused, "%s is archived", locked.Name)
		}
		cur, _, err := BaselineText(ctx, tx, locked.ID, path, version.Version)
		if err != nil {
			return err
		}
		if strings.TrimSpace(cur) == strings.TrimSpace(text) {
			return errf(Invalid, "text: it is the text %s applies already", path)
		}
		doc, err := b.Settings(ctx, tx, locked)
		if err != nil {
			return err
		}
		status := OverrideTesting
		switch {
		case by == Operator:
			status = OverrideApplied
		case baseline.WaitsForOperator(path) || settingString(doc, "approvals.agent_change") == "operator":
			status = OverrideWaiting
		}
		if _, err := tx.Exec(ctx, `UPDATE overrides SET status = 'withdrawn', ended_at = now(), decided_by = $3, decided_why = 'a newer version was proposed'
			WHERE project = $1 AND path = $2 AND status IN ('proposed', 'waiting', 'testing')`, locked.ID, path, by); err != nil {
			return err
		}
		var id int64
		if err := tx.QueryRow(ctx, `INSERT INTO overrides (project, number, path, version, text, why, status, proposed_by, proposed_run)
			VALUES ($1, (SELECT coalesce(max(number), 0) + 1 FROM overrides WHERE project = $1),
				$2, (SELECT coalesce(max(version), 0) + 1 FROM overrides WHERE project = $1 AND path = $2), $3, $4, 'proposed', $5, nullif($6, '')::uuid)
			RETURNING id`, locked.ID, path, text, why, by, run).Scan(&id); err != nil {
			return err
		}
		o, err := scanOverride(tx.QueryRow(ctx, `SELECT `+overrideCols+` FROM overrides o WHERE o.id = $1`, id))
		if err != nil {
			return err
		}
		if err := db.RecordProjectChange(ctx, tx, locked.ID, by, "overrides", fmt.Sprintf("o_%d", o.Number), nil,
			map[string]any{"path": path, "version": o.Version, "why": why}); err != nil {
			return err
		}
		if status == OverrideApplied {
			if err := b.applyOverride(ctx, tx, locked, o, by, "the operator's own text"); err != nil {
				return err
			}
		} else if err := setOverride(ctx, tx, o, status, "", ""); err != nil {
			return err
		}
		out, err = scanOverride(tx.QueryRow(ctx, `SELECT `+overrideCols+` FROM overrides o WHERE o.id = $1`, id))
		return err
	})
	return out, err
}

func setOverride(ctx context.Context, tx pgx.Tx, o *Override, status, by, why string) error {
	ended := status == OverrideRefused || status == OverrideTakenBack || status == OverrideWithdrawn || status == OverrideReplaced
	_, err := tx.Exec(ctx, `UPDATE overrides SET status = $2, decided_by = coalesce(nullif($3, ''), decided_by), decided_why = coalesce(nullif($4, ''), decided_why),
		ended_at = CASE WHEN $5 THEN now() ELSE ended_at END WHERE id = $1`, o.ID, status, by, why, ended)
	return err
}

// applyOverride makes an override the one its project applies to its file, in place of the one
// before, and records the signal (10.2, Override).
func (b *Board) applyOverride(ctx context.Context, tx pgx.Tx, p *Project, o *Override, by, why string) error {
	if _, err := tx.Exec(ctx, `UPDATE overrides SET status = 'replaced', ended_at = now() WHERE project = $1 AND path = $2 AND status = 'applied' AND id <> $3`,
		p.ID, o.Path, o.ID); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `UPDATE overrides SET status = 'applied', applied_at = now(), applied_on = $2,
		decided_by = coalesce(decided_by, $3), decided_why = coalesce(decided_why, $4) WHERE id = $1`, o.ID, version.Version, by, why); err != nil {
		return err
	}
	return RecordSignal(ctx, tx, Signal{Project: p.ID, Kind: "override", Detail: map[string]any{"what": "applied", "override": o.Number, "path": o.Path,
		"version": o.Version, "by": by, "text": fmt.Sprintf("%s applies o_%d to %s", p.Name, o.Number, o.Path)}})
}

// DecideOverride is the operator's answer to an override: apply it, also one in testing for which the
// evals could not tell (too few cases, or since 1.10 runs of the change that measured nothing), refuse
// it, or take one back that is applied.
func (b *Board) DecideOverride(ctx context.Context, p *Project, number int, choice, why, who string) (*Override, error) {
	var out *Override
	err := b.inTx(ctx, func(tx pgx.Tx) error {
		locked, err := projectByRef(ctx, tx, p.ID, true)
		if err != nil {
			return err
		}
		o, err := scanOverride(tx.QueryRow(ctx, `SELECT `+overrideCols+` FROM overrides o WHERE o.project = $1 AND o.number = $2 FOR UPDATE`, locked.ID, number))
		if errors.Is(err, pgx.ErrNoRows) {
			return errf(NotFound, "no override o_%d", number)
		}
		if err != nil {
			return err
		}
		open := o.Status == OverrideWaiting || o.Status == OverrideTesting || o.Status == OverrideProposed
		switch {
		case choice == "apply" && open:
			err = b.applyOverride(ctx, tx, locked, o, who, firstNonEmpty(why, "applied by the operator"))
		case choice == "refuse" && open:
			err = setOverride(ctx, tx, o, OverrideRefused, who, firstNonEmpty(why, "refused by the operator"))
		case choice == "take_back" && o.Status == OverrideApplied:
			err = b.takeBack(ctx, tx, locked, o, who, firstNonEmpty(why, "taken back by the operator"))
		case choice == "apply" || choice == "refuse" || choice == "take_back":
			return errf(Refused, "o_%d is %s", o.Number, o.Status)
		default:
			return errf(Invalid, "choice: apply, refuse, or take_back")
		}
		if err != nil {
			return err
		}
		if err := db.RecordProjectChange(ctx, tx, locked.ID, who, "overrides", fmt.Sprintf("o_%d", o.Number),
			map[string]any{"status": o.Status}, map[string]any{"choice": choice, "why": why}); err != nil {
			return err
		}
		out, err = scanOverride(tx.QueryRow(ctx, `SELECT `+overrideCols+` FROM overrides o WHERE o.id = $1`, o.ID))
		return err
	})
	return out, err
}

func (b *Board) takeBack(ctx context.Context, tx pgx.Tx, p *Project, o *Override, by, why string) error {
	if err := setOverride(ctx, tx, o, OverrideTakenBack, by, why); err != nil {
		return err
	}
	return RecordSignal(ctx, tx, Signal{Project: p.ID, Kind: "override", Detail: map[string]any{"what": "taken_back", "override": o.Number,
		"path": o.Path, "version": o.Version, "by": by, "text": fmt.Sprintf("o_%d of %s was taken back: %s", o.Number, o.Path, why)}})
}

// DecideByEvals is the backend's decision on an override in testing, from its project's evals (9.6):
// not worse applies it, worse refuses it, and evals that could not tell (too few cases, or since 1.10
// runs of the change that measured nothing) leave it in testing. eval is the numbers, kept with it,
// beside the result before.
func (b *Board) DecideByEvals(ctx context.Context, id int64, suite int64, verdict string, eval any) error {
	raw, err := json.Marshal(eval)
	if err != nil {
		return err
	}
	return b.inTx(ctx, func(tx pgx.Tx) error {
		o, err := scanOverride(tx.QueryRow(ctx, `SELECT `+overrideCols+` FROM overrides o WHERE o.id = $1 FOR UPDATE`, id))
		if err != nil {
			return err
		}
		if o.Status != OverrideTesting {
			return nil
		}
		p, err := projectByRef(ctx, tx, o.Project, true)
		if err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `UPDATE overrides SET suite = nullif($2, 0), eval = $3 WHERE id = $1`, o.ID, suite, raw); err != nil {
			return err
		}
		switch verdict {
		case "not_worse":
			return b.applyOverride(ctx, tx, p, o, "evals", "the evals were not worse")
		case "worse":
			return setOverride(ctx, tx, o, OverrideRefused, "evals", "the evals were worse")
		}
		return nil
	})
}

// TakeBack is Crew taking an applied override back, on a result that measured worse (9.6, 10.5).
func (b *Board) TakeBack(ctx context.Context, id int64, why string, measure any) error {
	raw, err := json.Marshal(measure)
	if err != nil {
		return err
	}
	return b.inTx(ctx, func(tx pgx.Tx) error {
		o, err := scanOverride(tx.QueryRow(ctx, `SELECT `+overrideCols+` FROM overrides o WHERE o.id = $1 FOR UPDATE`, id))
		if err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `UPDATE overrides SET measure = $2 WHERE id = $1`, o.ID, raw); err != nil {
			return err
		}
		if o.Status != OverrideApplied {
			return nil
		}
		p, err := projectByRef(ctx, tx, o.Project, true)
		if err != nil {
			return err
		}
		return b.takeBack(ctx, tx, p, o, "crew", why)
	})
}

// Measured keeps what an applied override measured that was not worse.
func (b *Board) Measured(ctx context.Context, id int64, measure any) error {
	raw, err := json.Marshal(measure)
	if err != nil {
		return err
	}
	_, err = b.Pool.Exec(ctx, `UPDATE overrides SET measure = $2 WHERE id = $1`, id, raw)
	return err
}
