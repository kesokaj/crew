package httpapi_test

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"testing"

	"github.com/kesokaj/crew/internal/board"
	"github.com/kesokaj/crew/internal/machinecare"
)

// The settings screens of M17 (14.2, 14.3): a key's history, newest first, from the change rows of its
// set, and Put back of one of its lines, through the same validation as a PUT.

type historyView struct {
	Key     string `json:"key"`
	Default any    `json:"default"`
	Changes []struct {
		Seq       int64  `json:"seq"`
		Who       string `json:"who"`
		Before    any    `json:"before"`
		After     any    `json:"after"`
		BeforeSet bool   `json:"beforeSet"`
		AfterSet  bool   `json:"afterSet"`
	} `json:"changes"`
}

// putMachine changes the machine's settings as the screen does: the whole document, read first.
func putMachine(t *testing.T, call func(method, path, body string, code int, out any), edit func(d map[string]any)) {
	t.Helper()
	var d map[string]any
	call("GET", "/api/settings/machine", "", 200, &d)
	edit(d)
	b, _ := json.Marshal(d)
	call("PUT", "/api/settings/machine", string(b), 200, nil)
}

func TestSettingsHistoryAndPutBackOfTheMachine(t *testing.T) {
	t.Parallel()
	srv := newServer(t, true)
	call := func(method, path, body string, code int, out any) {
		t.Helper()
		h := signedIn
		if method != "GET" {
			h = change
		}
		got, resp := do(t, srv, method, path, h, body)
		if got != code {
			t.Fatalf("%s %s: %d %s", method, path, got, resp)
		}
		if out != nil {
			if err := json.Unmarshal([]byte(resp), out); err != nil {
				t.Fatalf("%s %s: %v: %s", method, path, err, resp)
			}
		}
	}
	history := func(key string) historyView {
		t.Helper()
		var h historyView
		call("GET", "/api/settings/machine/history?key="+key, "", 200, &h)
		return h
	}
	disk := func(d map[string]any) map[string]any { return d["disk"].(map[string]any) }

	if h := history("disk.clean_at"); len(h.Changes) != 0 || h.Default != float64(85) || h.Key != "disk.clean_at" {
		t.Fatalf("before any change: %+v", h)
	}
	putMachine(t, call, func(d map[string]any) { disk(d)["clean_at"] = 70 })
	putMachine(t, call, func(d map[string]any) {
		disk(d)["clean_at"] = 75
		d["max_concurrent_runs"] = 2
	})
	// A change of another key is no line of this one.
	putMachine(t, call, func(d map[string]any) { disk(d)["stop_at"] = 96 })

	h := history("disk.clean_at")
	if len(h.Changes) != 2 {
		t.Fatalf("two lines, newest first: %+v", h)
	}
	newest, oldest := h.Changes[0], h.Changes[1]
	if newest.Before != float64(70) || newest.After != float64(75) || !newest.BeforeSet || !newest.AfterSet || newest.Who != admin {
		t.Fatalf("the newest line: %+v", newest)
	}
	if oldest.BeforeSet || oldest.Before != nil || oldest.After != float64(70) || oldest.Seq >= newest.Seq {
		t.Fatalf("the oldest line, from the default: %+v", oldest)
	}
	stopSeq := history("disk.stop_at").Changes[0].Seq

	for _, key := range []string{"disk.nothing", "prices", "runtime_image", ""} {
		if code, body := do(t, srv, "GET", "/api/settings/machine/history?key="+key, signedIn, ""); code != 404 || !strings.Contains(body, "NOT_FOUND") {
			t.Fatalf("history of %q: %d %s", key, code, body)
		}
	}

	// Put back the newest line: the value before it, as a new change by the operator.
	var after map[string]any
	call("POST", "/api/settings/machine/putback", fmt.Sprintf(`{"seq": %d, "key": "disk.clean_at"}`, newest.Seq), 200, &after)
	if disk(after)["clean_at"] != float64(70) {
		t.Fatalf("after Put back: %v", disk(after))
	}
	h = history("disk.clean_at")
	if len(h.Changes) != 3 || h.Changes[0].Before != float64(75) || h.Changes[0].After != float64(70) || h.Changes[0].Who != admin {
		t.Fatalf("Put back is a line of its own: %+v", h)
	}
	if after["max_concurrent_runs"] != float64(2) || disk(after)["stop_at"] != float64(96) {
		t.Fatalf("Put back of one key changed others: %v", after)
	}

	// Put back the line that set the key: it is unset again, so its default applies.
	call("POST", "/api/settings/machine/putback", fmt.Sprintf(`{"seq": %d, "key": "disk.clean_at"}`, oldest.Seq), 200, &after)
	if disk(after)["clean_at"] != float64(85) {
		t.Fatalf("back to the default: %v", disk(after))
	}
	if top := history("disk.clean_at").Changes[0]; top.AfterSet || top.After != nil {
		t.Fatalf("the key is taken out of the own document, not set to the default: %+v", top)
	}

	// A change that did not touch the key, and one that does not exist: no such line.
	for _, seq := range []int64{stopSeq, 1 << 40} {
		if code, body := do(t, srv, "POST", "/api/settings/machine/putback", change, fmt.Sprintf(`{"seq": %d, "key": "disk.clean_at"}`, seq)); code != 404 {
			t.Fatalf("Put back of change %d: %d %s", seq, code, body)
		}
	}
	if code, _ := do(t, srv, "POST", "/api/settings/machine/putback", change, fmt.Sprintf(`{"seq": %d, "key": "prices"}`, newest.Seq)); code != 404 {
		t.Fatalf("Put back of a retired key: %d", code)
	}

	// A value that no longer validates beside the others is refused, with the reason, and nothing is stored.
	putMachine(t, call, func(d map[string]any) { disk(d)["clean_at"] = 92 })
	putMachine(t, call, func(d map[string]any) { disk(d)["clean_at"] = 80; disk(d)["stop_at"] = 90 })
	hist := history("disk.clean_at")
	line := hist.Changes[0]
	code, body := do(t, srv, "POST", "/api/settings/machine/putback", change, fmt.Sprintf(`{"seq": %d, "key": "disk.clean_at"}`, line.Seq))
	if code != 400 || !strings.Contains(body, "clean_at must be above 0 and below stop_at") {
		t.Fatalf("a Put back that does not validate: %d %s", code, body)
	}
	if n := len(history("disk.clean_at").Changes); n != len(hist.Changes) {
		t.Fatalf("a refused Put back stored a change: %d lines, %d before", n, len(hist.Changes))
	}
	if code, _ := do(t, srv, "POST", "/api/settings/machine/putback", signedIn, fmt.Sprintf(`{"seq": %d, "key": "disk.clean_at"}`, line.Seq)); code != 403 {
		t.Fatalf("a Put back from no page of Crew's: %d", code)
	}
}

func TestSettingsHistoryAndPutBackOfAProject(t *testing.T) {
	t.Parallel()
	bs := newBoardServer(t)
	bs.call(t, "POST", "/api/projects", `{"name": "shop", "key": "SF", "plan": "x", "repository": {"kind": "local"}}`, 201, nil)
	limits := func(d map[string]any) map[string]any { return d["limits"].(map[string]any) }
	var d map[string]any
	bs.call(t, "GET", "/api/projects/shop/settings", "", 200, &d)
	limits(d)["max_fix_rounds"] = 4
	b, _ := json.Marshal(d)
	bs.call(t, "PUT", "/api/projects/shop/settings", string(b), 200, nil)

	var h historyView
	bs.call(t, "GET", "/api/projects/shop/settings/history?key=limits.max_fix_rounds", "", 200, &h)
	if len(h.Changes) != 1 || h.Changes[0].BeforeSet || h.Changes[0].After != float64(4) || h.Default != float64(3) {
		t.Fatalf("the project's history: %+v", h)
	}
	bs.call(t, "GET", "/api/projects/shop/settings/history?key=limits.nothing", "", 404, nil)

	// A change of the machine's settings is no line of the project's.
	var m map[string]any
	bs.call(t, "GET", "/api/settings/machine", "", 200, &m)
	m["disk"].(map[string]any)["clean_at"] = 70
	b, _ = json.Marshal(m)
	bs.call(t, "PUT", "/api/settings/machine", string(b), 200, nil)
	var mh historyView
	bs.call(t, "GET", "/api/settings/machine/history?key=disk.clean_at", "", 200, &mh)
	bs.call(t, "POST", "/api/projects/shop/settings/putback", fmt.Sprintf(`{"seq": %d, "key": "limits.max_fix_rounds"}`, mh.Changes[0].Seq), 404, nil)

	bs.call(t, "POST", "/api/projects/shop/settings/putback", fmt.Sprintf(`{"seq": %d, "key": "limits.max_fix_rounds"}`, h.Changes[0].Seq), 200, &d)
	if limits(d)["max_fix_rounds"] != float64(3) {
		t.Fatalf("after Put back: %v", limits(d))
	}
	bs.call(t, "GET", "/api/projects/shop/settings/history?key=limits.max_fix_rounds", "", 200, &h)
	if len(h.Changes) != 2 || h.Changes[0].AfterSet {
		t.Fatalf("Put back is a line of its own, back to the default: %+v", h)
	}

	// A change of another project is no line of this one.
	bs.call(t, "POST", "/api/projects", `{"name": "till", "key": "TL", "plan": "x", "repository": {"kind": "local"}}`, 201, nil)
	bs.call(t, "GET", "/api/projects/till/settings", "", 200, &d)
	limits(d)["max_fix_rounds"] = 5
	b, _ = json.Marshal(d)
	bs.call(t, "PUT", "/api/projects/till/settings", string(b), 200, nil)
	var th historyView
	bs.call(t, "GET", "/api/projects/till/settings/history?key=limits.max_fix_rounds", "", 200, &th)
	bs.call(t, "POST", "/api/projects/shop/settings/putback", fmt.Sprintf(`{"seq": %d, "key": "limits.max_fix_rounds"}`, th.Changes[0].Seq), 404, nil)

	// A Put back that raises a limit is recorded as the operator's raise, as a PUT that raises it is (7.7).
	bs.call(t, "GET", "/api/projects/shop/settings", "", 200, &d)
	limits(d)["max_fix_rounds"] = 2
	b, _ = json.Marshal(d)
	bs.call(t, "PUT", "/api/projects/shop/settings", string(b), 200, nil)
	bs.call(t, "GET", "/api/projects/shop/settings/history?key=limits.max_fix_rounds", "", 200, &h)
	bs.call(t, "POST", "/api/projects/shop/settings/putback", fmt.Sprintf(`{"seq": %d, "key": "limits.max_fix_rounds"}`, h.Changes[0].Seq), 200, nil)
	var raised int
	if err := bs.b.Pool.QueryRow(context.Background(), `SELECT count(*) FROM signals s JOIN projects p ON p.id = s.project
		WHERE p.name = 'shop' AND s.kind = 'operator_intervention' AND s.detail->>'what' = $1 AND s.detail->>'key' = 'limits.max_fix_rounds'
		AND (s.detail->>'from')::numeric = 2 AND (s.detail->>'to')::numeric = 3`, board.LimitRaised).Scan(&raised); err != nil || raised != 1 {
		t.Fatalf("the raise by Put back: %d %v", raised, err)
	}
}

// The screens offer the words the server takes: the schemas' own lists, not a copy in the UI.
func TestSettingsSchemaNamesTheWords(t *testing.T) {
	t.Parallel()
	srv := newServer(t, false)
	code, body := do(t, srv, "GET", "/api/settings/schema", signedIn, "")
	if code != 200 {
		t.Fatalf("%d %s", code, body)
	}
	var s map[string]struct {
		Enums    map[string][]string `json:"enums"`
		Nullable map[string]string   `json:"nullable"`
		Fixed    []string            `json:"fixed"`
		Floats   []string            `json:"floats"`
	}
	if err := json.Unmarshal([]byte(body), &s); err != nil {
		t.Fatal(err)
	}
	if got := s["machine"].Enums["updates.auto_install"]; !slices.Equal(got, []string{"text", "unprotected", "never"}) {
		t.Fatalf("machine enums: %v", got)
	}
	if got := s["project"].Enums["approvals.new_feature"]; !slices.Equal(got, []string{"notice", "operator", "agent"}) {
		t.Fatalf("project enums: %v", got)
	}
	if !slices.Contains(s["machine"].Fixed, "runtime_image") || s["machine"].Nullable["keep_trajectories_days"] != "int" {
		t.Fatalf("machine: %+v", s["machine"])
	}
	// A setting that takes a fraction is named, so a screen does not offer whole steps for one that
	// holds a whole number now, such as cpu_overcommit at 2 (review of M17 P1).
	for _, f := range []string{"cpu_overcommit", "quota.slow_down_at", "agents.auto.explore_share"} {
		if !slices.Contains(s["machine"].Floats, f) {
			t.Fatalf("machine floats %v lack %s", s["machine"].Floats, f)
		}
	}
	if !slices.Contains(s["project"].Floats, "limits.max_added_share") || slices.Contains(s["machine"].Floats, "disk.clean_at") {
		t.Fatalf("floats: machine %v, project %v", s["machine"].Floats, s["project"].Floats)
	}
}

// A group's line is a change of any key in it, and Put back of the line that set it takes the whole
// group out again (review of M17 P1). A history shows at most 50 lines.
func TestPutBackOfAGroupAndTheHistorysLength(t *testing.T) {
	t.Parallel()
	srv := newServer(t, true)
	call := func(method, path, body string, code int, out any) {
		t.Helper()
		h := signedIn
		if method != "GET" {
			h = change
		}
		if got, resp := do(t, srv, method, path, h, body); got != code {
			t.Fatalf("%s %s: %d %s", method, path, got, resp)
		} else if out != nil {
			if err := json.Unmarshal([]byte(resp), out); err != nil {
				t.Fatal(err)
			}
		}
	}
	disk := func(d map[string]any) map[string]any { return d["disk"].(map[string]any) }
	putMachine(t, call, func(d map[string]any) { disk(d)["clean_at"] = 70 })
	putMachine(t, call, func(d map[string]any) { disk(d)["stop_at"] = 96 })
	var h historyView
	call("GET", "/api/settings/machine/history?key=disk", "", 200, &h)
	if len(h.Changes) != 2 || h.Changes[1].BeforeSet {
		t.Fatalf("the group's lines: %+v", h)
	}
	var after map[string]any
	call("POST", "/api/settings/machine/putback", fmt.Sprintf(`{"seq": %d, "key": "disk"}`, h.Changes[1].Seq), 200, &after)
	if disk(after)["clean_at"] != float64(85) || disk(after)["stop_at"] != float64(95) {
		t.Fatalf("the group put back to its defaults: %v", disk(after))
	}

	// A key whose group is gone from the set gets its group again when an earlier value is put back.
	putMachine(t, call, func(d map[string]any) { disk(d)["clean_at"] = 70 })
	putMachine(t, call, func(d map[string]any) { disk(d)["clean_at"] = 75 })
	call("GET", "/api/settings/machine/history?key=disk.clean_at", "", 200, &h)
	set75 := h.Changes[0]
	call("POST", "/api/settings/machine/putback", fmt.Sprintf(`{"seq": %d, "key": "disk"}`, h.Changes[1].Seq), 200, &after)
	if disk(after)["clean_at"] != float64(85) {
		t.Fatalf("the group taken out: %v", disk(after))
	}
	call("POST", "/api/settings/machine/putback", fmt.Sprintf(`{"seq": %d, "key": "disk.clean_at"}`, set75.Seq), 200, &after)
	if disk(after)["clean_at"] != float64(70) {
		t.Fatalf("an earlier value put back into a group that was gone: %v", disk(after))
	}

	for i := range 51 {
		putMachine(t, call, func(d map[string]any) { d["max_concurrent_runs"] = 2 + i%2 })
	}
	call("GET", "/api/settings/machine/history?key=max_concurrent_runs", "", 200, &h)
	if len(h.Changes) != 50 || h.Changes[0].After != float64(2) {
		t.Fatalf("the newest 50 lines: %d, newest %+v", len(h.Changes), h.Changes[0])
	}
}

// The news of looking after the machine (16.10) is on the Crew level, in the feed of all projects and
// in no project's.
func TestMachineNewsIsOnTheCrewLevel(t *testing.T) {
	t.Parallel()
	k := newTicketKit(t)
	ctx := context.Background()
	if err := machinecare.News(ctx, k.d.Server, "certificate", "certificate:x", "The certificate of crew.example.com runs out in 10 days"); err != nil {
		t.Fatal(err)
	}
	var feed struct {
		Lines []struct{ Kind, Project, Text string }
	}
	k.call("GET", "/api/activity", "", 200, &feed)
	found := false
	for _, l := range feed.Lines {
		if l.Text == "The certificate of crew.example.com runs out in 10 days" {
			found = l.Kind == "news" && l.Project == ""
		}
	}
	if !found {
		t.Fatalf("the news of the certificate: %+v", feed.Lines)
	}
	k.shop([]string{}, nil, "one")
	k.call("GET", "/api/projects/shop/activity", "", 200, &feed)
	for _, l := range feed.Lines {
		if strings.Contains(l.Text, "certificate") {
			t.Fatalf("a project's activity has the machine's news: %q", l.Text)
		}
	}
}
