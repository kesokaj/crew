// Package settings holds the machine's settings and each project's: their defaults, their
// validation, and how they are read from and written to the database and to a file (design 14.1 to
// 14.3). A project's are in project.go.
package settings

import (
	"bytes"
	"context"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"reflect"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"github.com/jackc/pgx/v5"
	"go.yaml.in/yaml/v3"

	"github.com/kesokaj/crew/internal/db"
)

// defaultsYAML is the listing of crew.yaml in section 14.1 of the design, as it is there. A test
// keeps the two the same.
//
//go:embed crew.yaml
var defaultsYAML []byte

// Doc is a set of settings as nested maps, as YAML and JSON give it.
type Doc = map[string]any

// Values that are not settings but come from the install file, or from the release, fill the
// placeholders of the listing (14.1).
type Machine struct {
	Version    string // the Crew version, for runtime_image
	AdminEmail string // CREW_ADMIN_EMAIL, for notifications.mail_to
	SourceRepo string // CREW_SOURCE_REPO, the first value of updates.source_repo
}

// Defaults returns the machine's settings as the listing gives them, with its placeholders filled.
func Defaults(m Machine) (Doc, error) {
	var d Doc
	if err := yaml.Unmarshal(defaultsYAML, &d); err != nil {
		return nil, fmt.Errorf("the defaults: %w", err)
	}
	d = normalize(d).(Doc)
	set := func(path string, v any) {
		parts := strings.Split(path, ".")
		cur := d
		for _, p := range parts[:len(parts)-1] {
			cur = cur[p].(Doc)
		}
		cur[parts[len(parts)-1]] = v
	}
	set("runtime_image", "crew-runtime:"+m.Version)
	set("notifications.mail_to", nullIfEmpty(m.AdminEmail))
	set("updates.source_repo", nullIfEmpty(m.SourceRepo))
	set("github.owner", nil)
	return d, nil
}

func nullIfEmpty(s string) any {
	if s == "" {
		return nil
	}
	return s
}

// schema says what a set of settings may hold beyond the shape of its defaults.
type schema struct {
	// Settings whose default is null, with the kind of value they take when set.
	nullable map[string]string
	// Maps whose keys are names the operator may add to, such as a CLI or a role. A new key takes
	// the shape of the entries already there.
	open map[string]bool
	// Settings that take one of a few words.
	enums map[string][]string
	// Settings that must not be set by hand.
	fixed map[string]bool
	// Settings whose value has a shape of its own, checked by a function of their own.
	special map[string]func(v any) []string
	// Rules between values, over the defaults with the operator's values laid on.
	bounds func(merged Doc) []string
}

var machineSchema = &schema{
	nullable: map[string]string{
		"updates.backup_copy_to":    "string",
		"keep_trajectories_days":    "int",
		"notifications.mail_to":     "string",
		"updates.source_repo":       "string",
		"github.owner":              "string",
		"improvements.crew_project": "string",
	},
	open: map[string]bool{
		"agents.roles":        true,
		"agents.brackets":     true,
		"agents.extra_models": true,
	},
	enums: map[string][]string{
		"network.mode":           {"internet", "model_only", "lan"},
		"agents.at_limit":        {"move", "wait"},
		"updates.auto_install":   {"text", "unprotected", "never"},
		"updates.install_test":   {"local", "off"},
		"updates.publish":        {"github", "off"},
		"planning.rewrite":       {"suggest", "off"},
		"hosting.default_access": {"private", "listed", "google", "public"},
		"improvements.approval":  {"auto", "operator"},
		"agents.roles.*.effort":  Efforts,
	},
	// The release sets them.
	fixed:  map[string]bool{"runtime_image": true, "version": true},
	bounds: machineBounds,
}

// Efforts are the efforts an agent can be given (12.3).
var Efforts = []string{"low", "medium", "high", "xhigh", "max"}

// Validate checks a set of settings against the defaults: every key must be a setting, every value of
// the kind its setting takes, and a few values within their bounds. It returns every problem found.
func Validate(d Doc, defaults Doc) error { return machineSchema.validate(d, defaults) }

func (sc *schema) validate(d Doc, defaults Doc) error {
	var errs []string
	if v, ok := d["version"]; !ok || v != 1 {
		errs = append(errs, "version: must be 1")
	}
	sc.walk("", d, defaults, &errs)
	if sc.bounds != nil {
		errs = append(errs, sc.bounds(Merge(defaults, d))...)
	}
	if len(errs) > 0 {
		sort.Strings(errs)
		return &InvalidError{Problems: errs}
	}
	return nil
}

// InvalidError lists what is wrong with a set of settings.
type InvalidError struct{ Problems []string }

func (e *InvalidError) Error() string { return strings.Join(e.Problems, "\n") }

func (sc *schema) walk(prefix string, got, want Doc, errs *[]string) {
	for k, v := range got {
		path := join(prefix, k)
		if prefix == "" && sc.fixed[k] {
			continue
		}
		if f := sc.special[path]; f != nil {
			for _, e := range f(v) {
				*errs = append(*errs, path+": "+e)
			}
			continue
		}
		def, ok := want[k]
		if !ok {
			if sc.open[prefix] && len(want) > 0 {
				def = anyValue(want)
			} else {
				*errs = append(*errs, path+": not a setting")
				continue
			}
		}
		sc.check(path, v, def, errs)
	}
}

func anyValue(m Doc) any {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return m[keys[0]]
}

func (sc *schema) check(path string, v, def any, errs *[]string) {
	if def == nil {
		kind, ok := sc.nullable[path]
		if !ok {
			*errs = append(*errs, path+": not a setting")
			return
		}
		if vk := kindOf(v); v != nil && vk != kind && !(kind == "float" && vk == "int") {
			*errs = append(*errs, fmt.Sprintf("%s: must be %s or null", path, article(kind)))
		}
		return
	}
	if v == nil {
		if _, ok := sc.nullable[path]; !ok {
			*errs = append(*errs, path+": must be set")
		}
		return
	}
	dk, vk := kindOf(def), kindOf(v)
	if dk == "float" && vk == "int" {
		vk = "float"
	}
	if dk != vk {
		*errs = append(*errs, fmt.Sprintf("%s: must be %s", path, article(dk)))
		return
	}
	switch dk {
	case "map":
		sc.walk(path, v.(Doc), def.(Doc), errs)
	case "list":
		dl, vl := def.([]any), v.([]any)
		for i, e := range vl {
			if len(dl) == 0 {
				if kindOf(e) != "string" {
					*errs = append(*errs, fmt.Sprintf("%s[%d]: must be a word", path, i))
				}
				continue
			}
			sc.check(fmt.Sprintf("%s[%d]", path, i), e, dl[0], errs)
		}
	case "string":
		words := sc.enums[path]
		if words == nil {
			words = sc.enums[sc.wildcard(path)]
		}
		if words != nil && !contains(words, v.(string)) {
			*errs = append(*errs, fmt.Sprintf("%s: must be one of %s", path, strings.Join(words, ", ")))
		}
	}
}

// wildcard replaces the key of an open map in a path with *, so agents.roles.lead.effort is
// agents.roles.*.effort.
func (sc *schema) wildcard(path string) string {
	for o := range sc.open {
		if rest, ok := strings.CutPrefix(path, o+"."); ok {
			if _, after, ok := strings.Cut(rest, "."); ok {
				return o + ".*." + after
			}
		}
	}
	return path
}

// num reads a number of a set of settings by its path.
func num(d Doc, path string) (float64, bool) {
	cur := any(d)
	for _, p := range strings.Split(path, ".") {
		m, ok := cur.(Doc)
		if !ok {
			return 0, false
		}
		cur = m[p]
	}
	return number(cur)
}

func machineBounds(merged Doc) []string {
	var errs []string
	num := func(path string) (float64, bool) { return num(merged, path) }
	// The improver's schedule bounds it since its weekly budget went (0.53): none of it is 0.
	for _, p := range []string{"improvements.run_every_days", "improvements.run_after_tasks", "improvements.measure_after_tasks"} {
		if v, ok := num(p); ok && v < 1 {
			errs = append(errs, p+": must be 1 or more")
		}
	}
	clean, ok1 := num("disk.clean_at")
	stop, ok2 := num("disk.stop_at")
	if ok1 && ok2 && !(clean > 0 && clean < stop && stop <= 100) {
		errs = append(errs, "disk: clean_at must be above 0 and below stop_at, and stop_at at most 100")
	}
	// What retention keeps (16.10): the updater never removes the running release or the one before
	// it, so fewer than 2 means nothing, and 20 bounds the disk; nothing else is kept 0 times or days.
	if v, ok := num("updates.keep_versions"); ok && (v < 2 || v > 20) {
		errs = append(errs, "updates.keep_versions: must be from 2 to 20")
	}
	for _, p := range []string{"updates.backup_keep", "hosting.keep_images", "keep_trajectories_days"} {
		if v, ok := num(p); ok && v < 1 {
			errs = append(errs, p+": must be 1 or more")
		}
	}
	// The CLI's files are kept at most a year, so their keep time fits an interval.
	for _, p := range []string{"keep_cli_files_days", "conversations.keep_cli_files_days"} {
		if v, ok := num(p); ok && (v < 1 || v > 365) {
			errs = append(errs, p+": must be from 1 to 365")
		}
	}
	// A conversation's sandbox (11.8): removed after minutes to hours without a message, a few at once,
	// and summarised every few turns, so the third way of 12.10 reads a summary and the last turns.
	for _, b := range []struct {
		path     string
		low, top float64
	}{{"max_concurrent_runs", 1, 64}, {"max_active_tickets", 1, 64}, {"conversations.idle_minutes", 5, 240}, {"conversations.max_sandboxes", 1, 20}, {"conversations.summary_every_turns", 5, 100}, {"sandbox.suspend_after_minutes", 1, 240}, {"sandbox.stop_after_minutes", 1, 1440}, {"sandbox.max_snapshots", 1, 50}} {
		if v, ok := num(b.path); ok && (v < b.low || v > b.top) {
			errs = append(errs, fmt.Sprintf("%s: must be from %g to %g", b.path, b.low, b.top))
		}
	}
	if susp, ok1 := num("sandbox.suspend_after_minutes"); ok1 {
		if stop, ok2 := num("sandbox.stop_after_minutes"); ok2 && stop < susp {
			errs = append(errs, "sandbox.stop_after_minutes: must be at least sandbox.suspend_after_minutes")
		}
	}
	// A session lasts at most the ingress's 30 days, which the key only shortens; a fresh sign-in is
	// at most 15 minutes old, as 20.3 says, and the key only shortens that too.
	if v, ok := num("sessions.days"); ok && (v < 1 || v > 30) {
		errs = append(errs, "sessions.days: must be from 1 to 30")
	}
	if v, ok := num("sessions.fresh_minutes"); ok && (v < 1 || v > 15) {
		errs = append(errs, "sessions.fresh_minutes: must be from 1 to 15")
	}
	// The hours the machine may restart in for security updates (16.10), read in CREW_TIMEZONE.
	if mc, ok := merged["machine_care"].(Doc); ok {
		if v, ok := mc["restart_hours"].(string); ok {
			if _, err := ParseHours(v); err != nil {
				errs = append(errs, "machine_care.restart_hours: "+err.Error())
			}
		}
	}
	// The mail (18): the quiet hours the notifier reads on each pass, a reminder at least an hour after
	// the first mail, the server the app password goes to, and web push, off until the phone (18.2).
	if v, ok := num("notifications.remind_after_hours"); ok && v < 1 {
		errs = append(errs, "notifications.remind_after_hours: must be 1 or more")
	}
	if nt, ok := merged["notifications"].(Doc); ok {
		if v, ok := nt["quiet_hours"].(string); ok {
			if _, err := ParseHours(v); err != nil {
				errs = append(errs, "notifications.quiet_hours: "+err.Error())
			}
		}
		if v, ok := nt["mail_smtp"].(string); ok {
			host, port, err := net.SplitHostPort(v)
			if n, perr := strconv.Atoi(port); err != nil || host == "" || perr != nil || n < 1 || n > 65535 {
				errs = append(errs, "notifications.mail_smtp: must be host:port, such as smtp.gmail.com:587")
			}
		}
		if v, ok := nt["web_push"].(bool); ok && v {
			errs = append(errs, "notifications.web_push: stays false until the phone is taken up (18.2)")
		}
	}
	if v, ok := num("cpu_overcommit"); ok && v < 1 {
		errs = append(errs, "cpu_overcommit: must be at least 1")
	}
	if v, ok := num("agents.auto.explore_share"); ok && (v < 0 || v > 1) {
		errs = append(errs, "agents.auto.explore_share: must be from 0 to 1")
	}
	if agents, ok := merged["agents"].(Doc); ok {
		if brackets, ok := agents["brackets"].(Doc); ok {
			errs = append(errs, checkBrackets(brackets)...)
		}
	}
	return errs
}

func checkBrackets(brackets Doc) []string {
	var errs []string
	names := make([]string, 0, len(brackets))
	for name := range brackets {
		names = append(names, name)
	}
	sort.Strings(names)
	seen := map[string]string{}
	for _, name := range names {
		list, ok := brackets[name].([]any)
		if !ok {
			continue
		}
		if len(list) == 0 {
			errs = append(errs, "agents.brackets."+name+": must list at least one CLI/model")
			continue
		}
		for _, raw := range list {
			s, ok := raw.(string)
			if !ok {
				continue
			}
			if !cliModel.MatchString(s) {
				errs = append(errs, fmt.Sprintf("agents.brackets.%s: %q must be a CLI and a model, such as agy/claude-opus-5-5", name, s))
				continue
			}
			if prev, dup := seen[s]; dup {
				if prev == name {
					errs = append(errs, fmt.Sprintf("agents.brackets.%s: %s is named twice", name, s))
				} else {
					errs = append(errs, fmt.Sprintf("agents.brackets.%s: %s is already in bracket %s", name, s, prev))
				}
				continue
			}
			seen[s] = name
		}
	}
	return errs
}

func kindOf(v any) string {
	switch v.(type) {
	case bool:
		return "bool"
	case int:
		return "int"
	case float64:
		return "float"
	case string:
		return "string"
	case []any:
		return "list"
	case Doc:
		return "map"
	case nil:
		return "null"
	}
	return fmt.Sprintf("%T", v)
}

func article(kind string) string {
	return map[string]string{
		"bool": "true or false", "int": "a whole number", "float": "a number", "string": "a word",
		"list": "a list", "map": "a group of settings",
	}[kind]
}

func contains(words []string, w string) bool {
	for _, x := range words {
		if x == w {
			return true
		}
	}
	return false
}

func join(prefix, k string) string {
	if prefix == "" {
		return k
	}
	return prefix + "." + k
}

// normalize turns what a YAML decoder, or a JSON decoder with UseNumber, gives into Doc, []any, int,
// float64, string, bool, and nil, so values compare the same whichever way they were read.
func normalize(v any) any {
	switch x := v.(type) {
	case map[string]any:
		out := Doc{}
		for k, e := range x {
			out[k] = normalize(e)
		}
		return out
	case []any:
		out := make([]any, len(x))
		for i, e := range x {
			out[i] = normalize(e)
		}
		return out
	case json.Number:
		if i, err := x.Int64(); err == nil {
			return int(i)
		}
		f, _ := x.Float64()
		return f
	case int64:
		return int(x)
	}
	return v
}

// Merge returns base with over laid on top: groups of settings merge, every other value replaces.
func Merge(base, over Doc) Doc {
	out := Doc{}
	for k, v := range base {
		out[k] = v
	}
	for k, v := range over {
		if bm, ok := out[k].(Doc); ok {
			if om, ok := v.(Doc); ok {
				out[k] = Merge(bm, om)
				continue
			}
		}
		out[k] = v
	}
	return out
}

// Diff returns what of d differs from base: the operator's own values, which are what is stored, so
// that a default that changes later reaches every value the operator did not set (14.2).
func Diff(d, base Doc) Doc {
	out := diff(d, base)
	for k := range notStored {
		delete(out, k)
	}
	return out
}

// keepRoles stores the machine's entry of every agent whole: it is the machine's own from install on,
// which copies it from the baseline, so a release whose listing has other lists changes no agent (12.3).
func keepRoles(own, d Doc) {
	agents, ok := d["agents"].(Doc)
	if !ok {
		return
	}
	roles, ok := agents["roles"].(Doc)
	if !ok {
		return
	}
	a, _ := own["agents"].(Doc)
	if a == nil {
		a = Doc{}
		own["agents"] = a
	}
	a["roles"] = roles
}

func diff(d, base Doc) Doc {
	out := Doc{}
	for k, v := range d {
		bv, ok := base[k]
		if bm, isMap := bv.(Doc); ok && isMap {
			if vm, isMap := v.(Doc); isMap {
				if sub := diff(vm, bm); len(sub) > 0 {
					out[k] = sub
				}
				continue
			}
		}
		if !ok || !same(v, bv) {
			out[k] = v
		}
	}
	return out
}

// Values that are never the operator's own: the release sets them, or the project was created with
// them.
var notStored = map[string]bool{"version": true, "runtime_image": true, "project": true, "key": true}

// same compares two values, a whole number and a number with the same value as equal: a file
// writes the number 2.0 as 2. Lists and groups compare by their values, so a price of 4.00 in a list
// is the same after an export.
func same(a, b any) bool {
	if fa, ok := number(a); ok {
		fb, ok := number(b)
		return ok && fa == fb
	}
	switch x := a.(type) {
	case []any:
		y, ok := b.([]any)
		if !ok || len(x) != len(y) {
			return false
		}
		for i := range x {
			if !same(x[i], y[i]) {
				return false
			}
		}
		return true
	case Doc:
		y, ok := b.(Doc)
		if !ok || len(x) != len(y) {
			return false
		}
		for k, v := range x {
			if w, ok := y[k]; !ok || !same(v, w) {
				return false
			}
		}
		return true
	}
	return reflect.DeepEqual(a, b)
}

func number(v any) (float64, bool) {
	switch n := v.(type) {
	case int:
		return float64(n), true
	case float64:
		return n, true
	}
	return 0, false
}

// Parse reads a settings file.
func Parse(b []byte) (Doc, error) {
	var d Doc
	dec := yaml.NewDecoder(bytes.NewReader(b))
	dec.KnownFields(true)
	if err := dec.Decode(&d); err != nil {
		return nil, fmt.Errorf("not a settings file: %w", err)
	}
	if d == nil {
		return nil, errors.New("not a settings file: it is empty")
	}
	n, ok := normalize(d).(Doc)
	if !ok {
		return nil, errors.New("not a settings file")
	}
	return n, nil
}

// ParseJSON reads a set of settings sent to the API.
func ParseJSON(b []byte) (Doc, error) {
	d, err := decodeJSON(b)
	if err != nil {
		return nil, fmt.Errorf("not a settings document: %w", err)
	}
	return d, nil
}

// Format writes a set of settings as the file `crew-ctl settings export` prints.
func Format(d Doc) ([]byte, error) {
	var buf bytes.Buffer
	buf.WriteString("# Crew's machine settings (crew.yaml). Change a value and read the file back with\n")
	buf.WriteString("# crew-ctl settings import. The defaults and what each setting does are in section 14.1\n")
	buf.WriteString("# of the design. Crew's address, the admin email, and the time zone are not here: they\n")
	buf.WriteString("# are in /etc/crew/crew.env, changed with crew-ctl edit.\n")
	enc := yaml.NewEncoder(&buf)
	enc.SetIndent(2)
	if err := enc.Encode(d); err != nil {
		return nil, err
	}
	if err := enc.Close(); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// The machine's row in settings has no project.
const machineKey = "machine"

// Load returns the operator's own values of the machine's settings, as stored.
func Load(ctx context.Context, q db.Querier) (Doc, error) {
	var raw []byte
	err := q.QueryRow(ctx, `SELECT doc FROM settings WHERE project IS NULL`).Scan(&raw)
	if errors.Is(err, pgx.ErrNoRows) {
		return Doc{}, nil
	}
	if err != nil {
		return nil, err
	}
	return decodeJSON(raw)
}

func decodeJSON(raw []byte) (Doc, error) {
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	var d Doc
	if err := dec.Decode(&d); err != nil {
		return nil, err
	}
	if d == nil {
		return Doc{}, nil
	}
	d = normalize(d).(Doc)
	DropRetired(d)
	return d, nil
}

// Effective returns the machine's settings as they apply: the defaults with the operator's values.
func Effective(ctx context.Context, q db.Querier, m Machine) (Doc, error) {
	defaults, err := Defaults(m)
	if err != nil {
		return nil, err
	}
	own, err := Load(ctx, q)
	if err != nil {
		return nil, err
	}
	return Merge(defaults, own), nil
}

// Save validates a whole set of settings and stores what of it differs from the defaults, with its
// row in changes, in one transaction. A set that does not validate changes nothing (14.2).
func Save(ctx context.Context, pool interface {
	Begin(context.Context) (pgx.Tx, error)
}, m Machine, d Doc, who string) error {
	return SaveGuarded(ctx, pool, m, d, who, nil)
}

// Guard is asked, under the lock of the set, with the settings as they are and as they would be,
// both whole, before a change is stored; an error it returns stores nothing. The fresh sign-in of
// 20.3 is one (internal/httpapi); Crew's own changes of a setting pass none.
type Guard func(before, after Doc) error

// SaveGuarded is Save with a guard.
func SaveGuarded(ctx context.Context, pool interface {
	Begin(context.Context) (pgx.Tx, error)
}, m Machine, d Doc, who string, guard Guard) error {
	defaults, err := Defaults(m)
	if err != nil {
		return err
	}
	DropRetired(d)
	if err := Validate(d, defaults); err != nil {
		return err
	}
	own := Diff(d, defaults)
	keepRoles(own, d)
	tx, err := pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx) //nolint:errcheck
	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock($1)`, machineLock); err != nil {
		return err
	}
	if err := checkCrewProject(ctx, tx, m, d); err != nil {
		return err
	}
	before, err := Load(ctx, tx)
	if err != nil {
		return err
	}
	if reflect.DeepEqual(before, own) {
		return nil
	}
	if guard != nil {
		if err := guard(Merge(defaults, before), Merge(defaults, own)); err != nil {
			return err
		}
	}
	ownJSON, err := json.Marshal(own)
	if err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `INSERT INTO settings (project, doc, updated_by) VALUES (NULL, $1, $2)
		ON CONFLICT ((coalesce(project::text, 'machine'))) DO UPDATE
		SET doc = excluded.doc, updated_by = excluded.updated_by, updated_at = now()`, ownJSON, who); err != nil {
		return err
	}
	if err := db.RecordChange(ctx, tx, who, "settings", machineKey, before, json.RawMessage(ownJSON)); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// machineLock is the key of the lock every change of the machine's settings takes, so two changes
// made from what each read never overwrite each other (reviews of M8).
const machineLock = 4_112_008

// LockMachine takes the lock of the machine's settings in tx, for a change elsewhere that a save of
// them reads, such as archiving the project improvements.crew_project names (16.2).
func LockMachine(ctx context.Context, tx pgx.Tx) error {
	_, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock($1)`, machineLock)
	return err
}

// Update changes the machine's settings as they apply now, under a lock, and stores them with their
// history: change gets the settings read after the lock was taken, and nothing is written when it
// returns an error.
func Update(ctx context.Context, pool interface {
	Begin(context.Context) (pgx.Tx, error)
}, m Machine, who string, change func(Doc) error) error {
	tx, err := pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx) //nolint:errcheck
	if err := UpdateTx(ctx, tx, m, who, change); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// UpdateTx is Update inside a transaction that is open already, which holds the lock until it ends.
func UpdateTx(ctx context.Context, tx pgx.Tx, m Machine, who string, change func(Doc) error) error {
	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock($1)`, machineLock); err != nil {
		return err
	}
	doc, err := Effective(ctx, tx, m)
	if err != nil {
		return err
	}
	doc = deepCopy(doc).(Doc)
	if err := change(doc); err != nil {
		return err
	}
	return Save(ctx, tx, m, doc, who)
}

func deepCopy(v any) any {
	switch x := v.(type) {
	case Doc:
		out := Doc{}
		for k, e := range x {
			out[k] = deepCopy(e)
		}
		return out
	case []any:
		out := make([]any, len(x))
		for i, e := range x {
			out[i] = deepCopy(e)
		}
		return out
	}
	return v
}

// CrewRemote is the SSH address of the repository updates.source_repo names on GitHub: the remote
// Crew's project must have (16.2).
func CrewRemote(repo string) string { return "git@github.com:" + repo + ".git" }

// SourceRepo is the shape of updates.source_repo: owner/name on GitHub.
var SourceRepo = regexp.MustCompile(`^[A-Za-z0-9](?:[A-Za-z0-9-]{0,38})/[A-Za-z0-9._-]{1,100}$`)

// checkCrewProject refuses a save that changes improvements.crew_project or updates.source_repo so
// that the first is not a project whose remote is the repository the second names, or the second is
// no owner/name (16.2). A save that changes neither, such as Crew's own choice of a model, is not held
// by them (review of M16 P2).
func checkCrewProject(ctx context.Context, tx pgx.Tx, m Machine, d Doc) error {
	pair := func(d Doc) (string, string) {
		imp, _ := d["improvements"].(Doc)
		up, _ := d["updates"].(Doc)
		return str(imp["crew_project"]), str(up["source_repo"])
	}
	name, repo := pair(d)
	before, err := Effective(ctx, tx, m)
	if err != nil {
		return err
	}
	if wasName, wasRepo := pair(before); name == wasName && repo == wasRepo {
		return nil
	}
	if repo != "" && !SourceRepo.MatchString(repo) {
		return &InvalidError{Problems: []string{fmt.Sprintf("updates.source_repo: %q is no repository on GitHub, owner/name", repo)}}
	}
	if name == "" {
		return nil
	}
	if repo == "" {
		return &InvalidError{Problems: []string{"improvements.crew_project: updates.source_repo names no repository for Crew's project to come from"}}
	}
	var remote string
	err = tx.QueryRow(ctx, `SELECT coalesce(remote, '') FROM projects WHERE name = $1 AND NOT hidden AND state <> 'archived'`, name).Scan(&remote)
	if errors.Is(err, pgx.ErrNoRows) {
		return &InvalidError{Problems: []string{fmt.Sprintf("improvements.crew_project: there is no project %s", name)}}
	}
	if err != nil {
		return err
	}
	if want := CrewRemote(repo); remote != want {
		return &InvalidError{Problems: []string{fmt.Sprintf("improvements.crew_project: the remote of %s is %q, not %s, which updates.source_repo names", name, remote, want)}}
	}
	return nil
}
