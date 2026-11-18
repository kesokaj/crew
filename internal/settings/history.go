package settings

import (
	"context"
	"errors"
	"sort"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/kesokaj/crew/internal/db"
)

// A key's history (14.2): every change of a set is a row of changes with the set's own document
// before and after it, so a key's line is a row where the value at its path differs. A key that is
// absent from the own document is its default.

// Line is one change of one key.
type Line struct {
	Seq       int64     `json:"seq"`
	At        time.Time `json:"at"`
	Who       string    `json:"who"`
	Before    any       `json:"before"`
	After     any       `json:"after"`
	BeforeSet bool      `json:"beforeSet"`
	AfterSet  bool      `json:"afterSet"`
}

// History is a key's lines, newest first, with its default now.
type History struct {
	Key     string `json:"key"`
	Default any    `json:"default"`
	Changes []Line `json:"changes"`
}

// ErrNoSuchKey is a key that is no setting of its set, or one that is not set by hand.
var ErrNoSuchKey = errors.New("not a setting")

// ErrNoSuchChange is a change that is not one of the key's lines.
var ErrNoSuchChange = errors.New("no such change of the setting")

const (
	historyLines = 50   // the lines a history shows
	historyRows  = 2000 // the rows of a set it reads, newest first
)

// MachineHistory is a key's history in the machine's settings.
func MachineHistory(ctx context.Context, q db.Querier, m Machine, key string) (History, error) {
	defaults, err := Defaults(m)
	if err != nil {
		return History{}, err
	}
	return history(ctx, q, machineSchema, defaults, machineKey, key)
}

// ProjectHistory is a key's history in a project's settings.
func ProjectHistory(ctx context.Context, q db.Querier, project, name, pkey, key string) (History, error) {
	defaults, err := ProjectDefaults(name, pkey)
	if err != nil {
		return History{}, err
	}
	return history(ctx, q, projectSchema, defaults, project, key)
}

func history(ctx context.Context, q db.Querier, sc *schema, defaults Doc, rowKey, key string) (History, error) {
	def, ok := sc.key(defaults, key)
	if !ok {
		return History{}, ErrNoSuchKey
	}
	lines, err := lines(ctx, q, rowKey, key, 0)
	if err != nil {
		return History{}, err
	}
	if lines == nil {
		lines = []Line{}
	}
	return History{Key: key, Default: def, Changes: lines}, nil
}

// key says whether path is a setting that can be changed by hand, and gives its default.
func (sc *schema) key(defaults Doc, path string) (any, bool) {
	top, _, _ := strings.Cut(path, ".")
	if path == "" || sc.fixed[top] || notStored[top] {
		return nil, false
	}
	if def, ok := lookup(defaults, path); ok {
		return def, true
	}
	if i := strings.LastIndexByte(path, '.'); i > 0 && path[i+1:] != "" && sc.open[path[:i]] {
		if m, ok := lookup(defaults, path[:i]); ok {
			if d, ok := m.(Doc); ok && len(d) > 0 {
				return nil, true
			}
		}
	}
	return nil, false
}

// lines reads the set's rows, newest first, and keeps those that touched the key; with seq, only that
// row.
func lines(ctx context.Context, q db.Querier, rowKey, key string, seq int64) ([]Line, error) {
	rows, err := q.Query(ctx, `SELECT seq, at, who, coalesce(before, 'null'), coalesce(after, 'null') FROM changes
		WHERE tbl = 'settings' AND row_key = $1 AND ($2::bigint = 0 OR seq = $2::bigint) ORDER BY seq DESC LIMIT $3`, rowKey, seq, historyRows)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Line
	for rows.Next() && len(out) < historyLines {
		var l Line
		var before, after []byte
		if err := rows.Scan(&l.Seq, &l.At, &l.Who, &before, &after); err != nil {
			return nil, err
		}
		b, err := decodeJSON(before)
		if err != nil {
			return nil, err
		}
		a, err := decodeJSON(after)
		if err != nil {
			return nil, err
		}
		l.Before, l.BeforeSet = lookup(b, key)
		l.After, l.AfterSet = lookup(a, key)
		if l.BeforeSet != l.AfterSet || !same(l.Before, l.After) {
			out = append(out, l)
		}
	}
	return out, rows.Err()
}

// line is the key's line of change seq, or ErrNoSuchChange.
func line(ctx context.Context, q db.Querier, rowKey, key string, seq int64) (Line, error) {
	if seq <= 0 {
		return Line{}, ErrNoSuchChange
	}
	ls, err := lines(ctx, q, rowKey, key, seq)
	if err != nil {
		return Line{}, err
	}
	if len(ls) == 0 {
		return Line{}, ErrNoSuchChange
	}
	return ls[0], nil
}

// putBack sets the key in own to the value it had before the line, or takes it out when it was
// unset then, so its default applies again, whatever the default is now.
func putBack(own Doc, key string, l Line) {
	parts := strings.Split(key, ".")
	at := own
	for _, p := range parts[:len(parts)-1] {
		next, ok := at[p].(Doc)
		if !ok {
			if !l.BeforeSet {
				return
			}
			next = Doc{}
			at[p] = next
		}
		at = next
	}
	last := parts[len(parts)-1]
	if l.BeforeSet {
		at[last] = deepCopy(l.Before)
		return
	}
	delete(at, last)
}

// PutBack puts a key of the machine's settings back to its value before change seq: a new change,
// validated and guarded as every save is.
func PutBack(ctx context.Context, pool interface {
	Begin(context.Context) (pgx.Tx, error)
}, m Machine, seq int64, key, who string, guard Guard) error {
	defaults, err := Defaults(m)
	if err != nil {
		return err
	}
	if _, ok := machineSchema.key(defaults, key); !ok {
		return ErrNoSuchKey
	}
	tx, err := pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx) //nolint:errcheck
	if err := LockMachine(ctx, tx); err != nil {
		return err
	}
	l, err := line(ctx, tx, machineKey, key, seq)
	if err != nil {
		return err
	}
	own, err := Load(ctx, tx)
	if err != nil {
		return err
	}
	putBack(own, key, l)
	if err := SaveGuarded(ctx, tx, m, Merge(defaults, own), who, guard); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// ProjectPutBack is the document of a project's settings with a key put back to its value before
// change seq, for the caller to save in tx.
func ProjectPutBack(ctx context.Context, tx pgx.Tx, project, name, pkey string, seq int64, key string) (Doc, error) {
	defaults, err := ProjectDefaults(name, pkey)
	if err != nil {
		return nil, err
	}
	if _, ok := projectSchema.key(defaults, key); !ok {
		return nil, ErrNoSuchKey
	}
	l, err := line(ctx, tx, project, key, seq)
	if err != nil {
		return nil, err
	}
	own, err := LoadProject(ctx, tx, project)
	if err != nil {
		return nil, err
	}
	putBack(own, key, l)
	return Merge(defaults, own), nil
}

// lookup reads a value of d by its dotted path.
func lookup(d Doc, path string) (any, bool) {
	cur := any(d)
	for _, p := range strings.Split(path, ".") {
		m, ok := cur.(Doc)
		if !ok {
			return nil, false
		}
		if cur, ok = m[p]; !ok {
			return nil, false
		}
	}
	return cur, true
}

// SchemaView is what a screen needs of a schema beyond the values: the words a setting takes, the
// kind of a setting whose default is null, the settings no one sets by hand, and the settings that
// take a fraction, which a value such as 2 does not tell (cpu_overcommit's default is 2.0).
type SchemaView struct {
	Enums    map[string][]string `json:"enums"`
	Nullable map[string]string   `json:"nullable"`
	Fixed    []string            `json:"fixed"`
	Floats   []string            `json:"floats"`
}

// Schemas are the views of the machine's schema and a project's.
func Schemas(m Machine) (map[string]SchemaView, error) {
	machine, err := Defaults(m)
	if err != nil {
		return nil, err
	}
	project, err := ProjectDefaults("project", "PR")
	if err != nil {
		return nil, err
	}
	view := func(sc *schema, defaults Doc) SchemaView {
		fixed := make([]string, 0, len(sc.fixed))
		for k := range sc.fixed {
			fixed = append(fixed, k)
		}
		sort.Strings(fixed)
		nullable := sc.nullable
		if nullable == nil {
			nullable = map[string]string{}
		}
		floats := []string{}
		for k, kind := range sc.nullable {
			if kind == "float" {
				floats = append(floats, k)
			}
		}
		floatsIn("", defaults, &floats)
		sort.Strings(floats)
		return SchemaView{Enums: sc.enums, Nullable: nullable, Fixed: fixed, Floats: floats}
	}
	return map[string]SchemaView{"machine": view(machineSchema, machine), "project": view(projectSchema, project)}, nil
}

// floatsIn adds the paths of d whose value is a fraction's kind.
func floatsIn(prefix string, d Doc, out *[]string) {
	for k, v := range d {
		switch x := v.(type) {
		case float64:
			*out = append(*out, join(prefix, k))
		case Doc:
			floatsIn(join(prefix, k), x, out)
		}
	}
}
