package models_test

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/kesokaj/crew/internal/cli"
	"github.com/kesokaj/crew/internal/db"
	"github.com/kesokaj/crew/internal/db/dbtest"
	. "github.com/kesokaj/crew/internal/models"
	"github.com/kesokaj/crew/internal/settings"
)

// The guard every run passes lets a check run on the builder's family only when Choose itself would
// have: when no entry of the role's list of another family is listed, which Choose marks Off (12.3,
// since 1.15).
func TestTheRuleCannotBeMetExactlyWhenChooseRunsTheCheckOff(t *testing.T) {
	ctx := context.Background()
	d := dbtest.New(t, false)
	signIn(t, d, "signed_in", "agy")
	readRecorded(t, d, "agy")
	claudeOnly := machine(t, settings.Doc{"agents": settings.Doc{"roles": settings.Doc{"reviewer": settings.Doc{"model": []any{"agy/claude-opus-5-5", "agy/claude-sonnet-5-5"}}}}})
	// A name no CLI lists is not an entry of another family that is offered.
	unlisted := machine(t, settings.Doc{"agents": settings.Doc{"roles": settings.Doc{"reviewer": settings.Doc{"model": []any{"agy/claude-opus-5-5", "agy/gemini-0-none"}}}}})
	for _, c := range []struct {
		name    string
		machine settings.Doc
		avoid   string
		want    bool
	}{
		{"the default list, a Claude build", machine(t, nil), "claude", false},
		{"the default list, a Gemini build", machine(t, nil), "gemini", false},
		{"a list of Claude only, a Claude build", claudeOnly, "claude", true},
		{"a list of Claude only, a Gemini build", claudeOnly, "gemini", false},
		{"another family that no CLI lists", unlisted, "claude", true},
	} {
		ask := Ask{Role: "reviewer", Machine: c.machine, Avoid: c.avoid}
		got, err := RuleCannotBeMet(ctx, d.Server, ask)
		if err != nil || got != c.want {
			t.Errorf("%s: %v %v", c.name, got, err)
		}
		ch, err := Choose(ctx, d.Server, ask)
		if err != nil || ch.Off != c.want {
			t.Errorf("%s: Choose's Off %v %v", c.name, ch.Off, err)
		}
	}
	// The default judge list starts on claude-sonnet-5-5 and falls back to Gemini flash when avoiding Claude.
	for _, avoid := range []string{"gemini", "claude"} {
		ask := Ask{Role: "judge", Machine: machine(t, nil), Avoid: avoid}
		if got, err := RuleCannotBeMet(ctx, d.Server, ask); err != nil || got {
			t.Errorf("judge avoid %s: %v %v", avoid, got, err)
		}
	}
	// When a reviewer's list has only Gemini entries (flash first), claude-sonnet-5-5 in agents.brackets.flash
	// is disallowed by the model rule for both RuleCannotBeMet and Choose.
	flashOnly := machine(t, settings.Doc{"agents": settings.Doc{"roles": settings.Doc{"reviewer": settings.Doc{"model": []any{"agy/gemini-3.8-flash", "agy/gemini-4-argon"}}}}})
	ask := Ask{Role: "reviewer", Machine: flashOnly, Avoid: "gemini"}
	if got, err := RuleCannotBeMet(ctx, d.Server, ask); err != nil || !got {
		t.Fatalf("flashOnly RuleCannotBeMet = %v, %v", got, err)
	}
	if ch, err := Choose(ctx, d.Server, ask); err != nil || !ch.Off || ch.Model != "gemini-3.8-flash" {
		t.Fatalf("flashOnly Choose = %+v, %v", ch, err)
	}
	// A Set by you check role whose primary entry matches Avoid and whose next entry is a cross-family
	// model outside (claude, pro) (claude-sonnet-5-5 on a code ticket, or gpt-oss-120b) moves to that
	// cross-family entry when 3p has room (RuleCannotBeMet = false, Off = false), and falls back to the
	// builder's family at 3p's limit (RuleCannotBeMet = true, Off = true).
	for _, altModel := range []string{"claude-sonnet-5-5", "gpt-oss-120b"} {
		setDoc := machine(t, settings.Doc{"agents": settings.Doc{
			"roles": settings.Doc{"reviewer": settings.Doc{"model": []any{"agy/gemini-4-argon", "agy/" + altModel}, "effort": "high"}},
			"auto":  settings.Doc{"set_by_you": []any{"reviewer"}},
		}})
		setAsk := Ask{Role: "reviewer", Machine: setDoc, Avoid: "gemini"}
		if got, err := RuleCannotBeMet(ctx, d.Server, setAsk); err != nil || got {
			t.Fatalf("set_by_you %s RuleCannotBeMet with room = %v, %v", altModel, got, err)
		}
		if ch, err := Choose(ctx, d.Server, setAsk); err != nil || ch.Off || ch.Model != altModel || ch.ModelFrom != FromCheckRule {
			t.Fatalf("set_by_you %s Choose with room = %+v, %v", altModel, ch, err)
		}
		if _, err := Reached(ctx, d.Server, "agy", "3p", "weekly", time.Time{}); err != nil {
			t.Fatal(err)
		}
		if got, err := RuleCannotBeMet(ctx, d.Server, setAsk); err != nil || !got {
			t.Fatalf("set_by_you %s RuleCannotBeMet at 3p limit = %v, %v", altModel, got, err)
		}
		if ch, err := Choose(ctx, d.Server, setAsk); err != nil || !ch.Off || ch.Model != "gemini-4-argon" || ch.ModelFrom != FromLimit {
			t.Fatalf("set_by_you %s Choose at 3p limit = %+v, %v", altModel, ch, err)
		}
		d.Server.Exec(ctx, `DELETE FROM quota`)
	}
	gone, cancel := context.WithCancel(ctx)
	cancel()
	if _, err := RuleCannotBeMet(gone, d.Server, Ask{Role: "reviewer", Machine: claudeOnly, Avoid: "claude"}); err == nil {
		t.Fatal("a read that fails says the rule cannot be met")
	}
}

// brokenRows are the rows of the read of the signed-in CLIs, broken in one way: a row that does not
// scan, or a read that ends in an error.
type brokenRows struct {
	pgx.Rows
	scan bool
}

var errBroken = errors.New("the read broke")

func (r brokenRows) Scan(dest ...any) error {
	if r.scan {
		return errBroken
	}
	return r.Rows.Scan(dest...)
}

func (r brokenRows) Err() error {
	if r.scan {
		return r.Rows.Err()
	}
	return errBroken
}

// breaksSignedIn is a pool whose read of the signed-in CLIs is broken.
type breaksSignedIn struct {
	db.Querier
	scan bool
}

func (q breaksSignedIn) Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error) {
	rows, err := q.Querier.Query(ctx, sql, args...)
	if err == nil && strings.Contains(sql, "FROM cli_logins WHERE state = 'signed_in'") {
		return brokenRows{Rows: rows, scan: q.scan}, nil
	}
	return rows, err
}

// A read of the signed-in CLIs that fails is an error, never no CLI signed in, which made every run
// wait for a sign-in (since 1.15).
func TestAFailedReadOfWhoIsSignedInIsAnError(t *testing.T) {
	ctx := context.Background()
	d := dbtest.New(t, false)
	signIn(t, d, "signed_in", "agy")
	readRecorded(t, d, "agy")
	ask := Ask{Role: "reviewer", Machine: machine(t, nil)}
	if c, err := Choose(ctx, d.Server, ask); err != nil || c.CLI != "agy" {
		t.Fatalf("the read as it is: %+v %v", c, err)
	}
	for _, scan := range []bool{false, true} {
		_, err := Choose(ctx, breaksSignedIn{Querier: d.Server, scan: scan}, ask)
		if _, wait := IsWait(err); wait || !errors.Is(err, errBroken) {
			t.Errorf("a read whose scan breaks %v: %v", scan, err)
		}
	}
}

// Agents distinguishes CheckRule when another family is at its limit and falls back to the builder's
// family from when no CLI in use offers another family; and Windows/RecordLimits drop expired limit rows (12.3, 12.7).
func TestAgentsCheckRuleAndExpiredLimitWindows(t *testing.T) {
	ctx := context.Background()
	d := dbtest.New(t, false)
	signIn(t, d, "signed_in", "agy")
	readRecorded(t, d, "agy")

	// 1. When Claude is at its limit, CheckRule says another family is at its limit and falls back to Gemini.
	if _, err := Reached(ctx, d.Server, "agy", "3p", "weekly", time.Time{}); err != nil {
		t.Fatal(err)
	}
	views, err := Agents(ctx, d.Server, machine(t, nil))
	if err != nil {
		t.Fatal(err)
	}
	var rev AgentView
	for _, v := range views {
		if v.Role == "reviewer" {
			rev = v
		}
	}
	if rev.CheckRule != "cannot be met now: another family is at its limit, falls back to agy/gemini-4-argon" {
		t.Fatalf("CheckRule at limit = %q", rev.CheckRule)
	}

	// 1b. When 3p is above slow_down_at (0.85) and Gemini has room, CheckRule reports slow_down_at fallback.
	d.Server.Exec(ctx, `DELETE FROM quota`)
	if err := RecordLimits(ctx, d.Server, "agy", []cli.Limit{{Group: "3p", Window: "weekly", Used: 0.85, ResetsAt: time.Now().Add(time.Hour)}}, "check"); err != nil {
		t.Fatal(err)
	}
	views, err = Agents(ctx, d.Server, machine(t, nil))
	if err != nil {
		t.Fatal(err)
	}
	for _, v := range views {
		if v.Role == "reviewer" {
			rev = v
		}
	}
	if rev.CheckRule != "cannot be met now: another family is above slow_down_at, falls back to agy/gemini-4-argon" {
		t.Fatalf("CheckRule at slow_down_at = %q", rev.CheckRule)
	}

	// 2. When reviewer's list has only Gemini models, CheckRule says no CLI in use offers another family.
	d.Server.Exec(ctx, `DELETE FROM quota`)
	geminiOnly := machine(t, settings.Doc{"agents": settings.Doc{"roles": settings.Doc{"reviewer": settings.Doc{"model": []any{"agy/gemini-4-argon"}}}}})
	views, err = Agents(ctx, d.Server, geminiOnly)
	if err != nil {
		t.Fatal(err)
	}
	for _, v := range views {
		if v.Role == "reviewer" {
			rev = v
		}
	}
	if rev.CheckRule != "cannot be met: no CLI in use offers another family than gemini" {
		t.Fatalf("CheckRule single family = %q", rev.CheckRule)
	}

	// 3. An expired win = 'limit' row is omitted by Windows and cleaned up by RecordLimits.
	if _, err := d.Server.Exec(ctx, `INSERT INTO quota (cli, grp, win, used, reached_until, source)
		VALUES ('agy', 'claude', 'limit', 1, now() - interval '1 hour', 'run')`); err != nil {
		t.Fatal(err)
	}
	ws, err := Windows(ctx, d.Server)
	if err != nil || len(ws) != 0 {
		t.Fatalf("expired limit window returned by Windows: %+v %v", ws, err)
	}
	if err := RecordLimits(ctx, d.Server, "agy", nil, "check"); err != nil {
		t.Fatal(err)
	}
	var count int
	d.Server.QueryRow(ctx, `SELECT count(*) FROM quota WHERE win = 'limit'`).Scan(&count)
	if count != 0 {
		t.Fatalf("expired limit row not deleted by RecordLimits: %d", count)
	}
}

// Operator-edited non-AutoRoles (prompt, planner, judge) saved in changes, and all roles when
// agents.auto.on is false, resolve as FromOperator: they honour non-default primary entries ahead of
// Gemini fallbacks, stay on their chosen model above slow_down_at, and wait at their group's limit (12.3, 12.7).
func TestOperatorEditedNonAutoRolesAndAutoOffResolveAsFromOperator(t *testing.T) {
	ctx := context.Background()
	d := dbtest.New(t, false)
	signIn(t, d, "signed_in", "agy")
	readRecorded(t, d, "agy")
	reset := time.Now().Add(2 * time.Hour).Truncate(time.Second)

	// 1. agents.auto.on = false treats planner and prompt as FromOperator.
	autoOff := machine(t, settings.Doc{"agents": settings.Doc{
		"roles": settings.Doc{
			"planner": settings.Doc{"model": []any{"agy/claude-opus-5-5", "agy/gemini-4-argon"}, "effort": "high"},
			"prompt":  settings.Doc{"model": []any{"agy/claude-opus-5-5", "agy/gemini-3.8-flash"}, "effort": "medium"},
		},
		"auto": settings.Doc{"on": false},
	}})
	if err := RecordLimits(ctx, d.Server, "agy", []cli.Limit{{Group: "3p", Window: "5h", Used: 0.85, ResetsAt: reset}}, "login check"); err != nil {
		t.Fatal(err)
	}
	for _, role := range []string{"planner", "prompt"} {
		ch, err := Choose(ctx, d.Server, Ask{Role: role, Machine: autoOff})
		if err != nil || ch.Model != "claude-opus-5-5" || ch.ModelFrom != FromOperator {
			t.Fatalf("auto.on=false %s at 0.85 = %+v, %v", role, ch, err)
		}
	}

	// 2. With agents.auto.on = true, operator-saved edits to prompt and judge in changes resolve as FromOperator.
	m := settings.Machine{Version: "1.0.0"}
	edited := machine(t, settings.Doc{"agents": settings.Doc{
		"roles": settings.Doc{
			"prompt": settings.Doc{"model": []any{"agy/claude-opus-5-5", "agy/gemini-3.8-flash"}, "effort": "medium"},
			"judge":  settings.Doc{"model": []any{"agy/claude-opus-5-5", "agy/gemini-3.8-flash"}, "effort": "low"},
		},
	}})
	if err := settings.Save(ctx, d.Server, m, edited, "op@example.com"); err != nil {
		t.Fatal(err)
	}
	eff, err := settings.Effective(ctx, d.Server, m)
	if err != nil {
		t.Fatal(err)
	}
	ch, err := Choose(ctx, d.Server, Ask{Role: "prompt", Machine: eff})
	if err != nil || ch.Model != "claude-opus-5-5" || ch.ModelFrom != FromOperator {
		t.Fatalf("operator-edited prompt at 0.85 = %+v, %v", ch, err)
	}
	jAsk := Ask{Role: "judge", Machine: eff, Avoid: "gemini"}
	if got, err := RuleCannotBeMet(ctx, d.Server, jAsk); err != nil || got {
		t.Fatalf("operator-edited judge RuleCannotBeMet = %v, %v", got, err)
	}
	ch, err = Choose(ctx, d.Server, jAsk)
	if err != nil || ch.Off || ch.Model != "claude-opus-5-5" || ch.ModelFrom != FromOperator {
		t.Fatalf("operator-edited judge Choose = %+v, %v", ch, err)
	}
	if err := RecordLimits(ctx, d.Server, "agy", []cli.Limit{{Group: "3p", Window: "5h", Used: 1, Reached: true, ResetsAt: reset}}, "login check"); err != nil {
		t.Fatal(err)
	}
	if _, err := Choose(ctx, d.Server, Ask{Role: "prompt", Machine: eff}); err == nil {
		t.Fatal("operator-edited prompt moved at 3p limit instead of waiting")
	}
}
