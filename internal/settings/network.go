package settings

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync"

	"github.com/jackc/pgx/v5"

	"github.com/kesokaj/crew/internal/config"
)

// RowQuerier is what reads one row: a pool, a connection, or a transaction.
type RowQuerier interface {
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
}

// NetworkMode is the network mode a sandbox of a project starts in (13.6): the ticket's own mode
// when it has one, else the project's `sandbox.network`, else the machine's `network.mode`.
// "inherit" passes the choice on. ticket 0 is a sandbox of no ticket; project "" one of no project.
// A value that is not a mode, which validation never stores, is an error rather than a guess.
func NetworkMode(ctx context.Context, q RowQuerier, project string, ticket int64) (string, error) {
	pick := func(v any, from string) (string, bool, error) {
		s, _ := v.(string)
		if s == "" || s == "inherit" {
			return "", false, nil
		}
		if !slices.Contains(config.Modes, s) {
			return "", false, fmt.Errorf("%s names the network mode %q, which is none", from, s)
		}
		return s, true, nil
	}
	if ticket > 0 {
		var n string
		if err := q.QueryRow(ctx, `SELECT network FROM tickets WHERE id = $1`, ticket).Scan(&n); err != nil {
			return "", fmt.Errorf("the ticket's network mode: %w", err)
		}
		if m, ok, err := pick(n, "the ticket"); err != nil || ok {
			return m, err
		}
	}
	if project != "" {
		own, err := projectOwn(ctx, q, project)
		if err != nil {
			return "", err
		}
		sb, _ := own["sandbox"].(Doc)
		if m, ok, err := pick(sb["network"], "the project's settings"); err != nil || ok {
			return m, err
		}
	}
	var raw []byte
	err := q.QueryRow(ctx, `SELECT doc FROM settings WHERE project IS NULL`).Scan(&raw)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return "", err
	}
	var machine Doc
	if len(raw) > 0 {
		if machine, err = decodeJSON(raw); err != nil {
			return "", err
		}
	}
	nw, _ := machine["network"].(Doc)
	if m, ok, err := pick(nw["mode"], "the machine's settings"); err != nil || ok {
		return m, err
	}
	defaults, err := Defaults(Machine{})
	if err != nil {
		return "", err
	}
	m, _, err := pick(defaults["network"].(Doc)["mode"], "the machine's defaults")
	return m, err
}

// CheckNetworkMode is the network mode of a project's check sandbox (13.6, 13.9, since 0.53): the
// project's sandbox.check_network when it names a mode, Sources among them, else the mode its runs have.
// A check runs no agent, so Sources, which lets the package sources through and not the model's hosts,
// is enough for one: Crew's own project checks so, with make check and a Docker daemon of the check
// sandbox's own (16.2). A check runs code an agent wrote, so its mode is never wider than its project's
// runs: wider, it is theirs (security review of 0.53). A ticket's own mode counts where it is wider,
// which only the operator sets; a ticket narrowed to Model only, as Analysis may, narrows its agent runs
// alone, which hold the login, and not its checks (since 0.55, the operator's choice of L4).
func CheckNetworkMode(ctx context.Context, q RowQuerier, project string, ticket int64) (string, error) {
	runs, err := NetworkMode(ctx, q, project, 0)
	if err != nil || project == "" {
		return runs, err
	}
	if ticket > 0 {
		own, err := NetworkMode(ctx, q, project, ticket)
		if err != nil {
			return "", err
		}
		if breadth[own] > breadth[runs] {
			runs = own
		}
	}
	own, err := projectOwn(ctx, q, project)
	if err != nil {
		return "", err
	}
	sb, _ := own["sandbox"].(Doc)
	switch m, _ := sb["check_network"].(string); {
	case m == "" || m == "inherit":
		return runs, nil
	case !slices.Contains(config.SandboxModes, m):
		return "", fmt.Errorf("the project's settings name the check sandboxes' network mode %q, which is none", m)
	case breadth[m] > breadth[runs]:
		return runs, nil
	default:
		return m, nil
	}
}

// breadth orders the modes by what a sandbox reaches: Model only its model's hosts; Sources the
// package sources, several of which take uploads; the internet; and the LAN, whatever the machine
// reaches.
var breadth = map[string]int{config.ModeModelOnly: 1, config.ModeSources: 2, config.ModeInternet: 3, config.ModeLAN: 4}

// projectOwn is what a project's settings say of their own, without the defaults.
func projectOwn(ctx context.Context, q RowQuerier, project string) (Doc, error) {
	var raw []byte
	err := q.QueryRow(ctx, `SELECT doc FROM settings WHERE project = $1`, project).Scan(&raw)
	if errors.Is(err, pgx.ErrNoRows) || (err == nil && len(raw) == 0) {
		return Doc{}, nil
	}
	if err != nil {
		return nil, err
	}
	return decodeJSON(raw)
}

// ModelOnlyHosts are the hosts the proxy lets a Model only sandbox reach (13.6): the machine's
// `network.model_only_hosts`, or the listing's when the machine has no list of its own.
func ModelOnlyHosts(ctx context.Context, q RowQuerier) ([]string, error) {
	return machineList(ctx, q, "network", "model_only_hosts")
}

// SourcesHosts are the hosts the proxy lets a Sources sandbox, the build of a release, reach (13.6,
// since 0.51): the machine's `sandbox.sources`, or the listing's.
func SourcesHosts(ctx context.Context, q RowQuerier) ([]string, error) {
	return machineList(ctx, q, "sandbox", "sources")
}

// machineList is the machine's list section.key, or the listing's when the machine has none.
func machineList(ctx context.Context, q RowQuerier, section, key string) ([]string, error) {
	v, err := MachineValue(ctx, q, section+"."+key)
	if err != nil {
		return nil, err
	}
	return strs(v), nil
}

// MachineValue is the machine's setting at a dotted path, or the listing's when the machine has not
// set it, for a reader that needs one value and not the Machine the placeholders take.
func MachineValue(ctx context.Context, q RowQuerier, path string) (any, error) {
	var raw []byte
	err := q.QueryRow(ctx, `SELECT doc FROM settings WHERE project IS NULL`).Scan(&raw)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return nil, err
	}
	d := Doc{}
	if len(raw) > 0 {
		if d, err = decodeJSON(raw); err != nil {
			return nil, err
		}
	}
	if v, ok := at(d, path); ok {
		return v, nil
	}
	defaults, err := Defaults(Machine{})
	if err != nil {
		return nil, err
	}
	v, _ := at(defaults, path)
	return v, nil
}

// Sessions are the machine's sessions.days and sessions.fresh_minutes (20.3), read in one query: every
// request of the operator is checked against them.
func Sessions(ctx context.Context, q RowQuerier) (days, freshMinutes int, err error) {
	var raw []byte
	err = q.QueryRow(ctx, `SELECT doc FROM settings WHERE project IS NULL`).Scan(&raw)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return 0, 0, err
	}
	own := Doc{}
	if len(raw) > 0 {
		if own, err = decodeJSON(raw); err != nil {
			return 0, 0, err
		}
	}
	listing, err := listingDefaults()
	if err != nil {
		return 0, 0, err
	}
	get := func(path string) int {
		v, ok := num(own, path)
		if !ok {
			v, _ = num(listing, path)
		}
		return int(v)
	}
	return get("sessions.days"), get("sessions.fresh_minutes"), nil
}

// listingDefaults are the listing's values, read once: none of them depends on the machine.
var listingDefaults = sync.OnceValues(func() (Doc, error) { return Defaults(Machine{}) })

// at is the value at a dotted path, and whether the path is there.
func at(d Doc, path string) (any, bool) {
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
