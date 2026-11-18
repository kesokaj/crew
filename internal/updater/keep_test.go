package updater_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/kesokaj/crew/internal/release"
	"github.com/kesokaj/crew/internal/updater"
)

func (k *kit) kept() string {
	k.t.Helper()
	fs, _, err := release.List(k.rel)
	if err != nil {
		k.t.Fatal(err)
	}
	var got []string
	for _, f := range fs {
		got = append(got, f.Manifest.Version)
	}
	return strings.Join(got, " ")
}

// updates.keep_versions reaches the updater with the server's ask (16.4 step 7, D30): it keeps that
// many releases with their images, and remembers the number, so a later ask that names none, such as
// crew-ctl's, keeps the same. A number below 2 or above 20 is refused before anything runs.
func TestTheUpdaterKeepsKeepVersions(t *testing.T) {
	k := newKit(t, "0.1.0", "0.1.0", "0.2.0", "0.3.0", "0.4.0", "0.5.0")
	req, _ := k.ask(updater.Ask{Version: "0.4.0", AskedBy: "operator", Keep: 3}, false)
	if req.State != updater.StateDone {
		t.Fatalf("%+v", req)
	}
	// 0.1.0 is the one before 0.4.0, so 0.2.0 and 0.3.0 go.
	if got := k.kept(); got != "0.1.0 0.4.0 0.5.0" || !k.m.called("Remove 0.3.0") {
		t.Fatalf("kept %v, calls %v", got, k.m.Calls())
	}
	if st, _ := k.u.State(); st.Keep != 3 {
		t.Fatalf("the state keeps %d", st.Keep)
	}
	k.release("0.6.0", 51, 0, true)
	req, _ = k.ask(updater.Ask{Version: "0.6.0", AskedBy: "root"}, true)
	if req.State != updater.StateDone {
		t.Fatalf("%+v", req)
	}
	if got := k.kept(); got != "0.4.0 0.5.0 0.6.0" {
		t.Fatalf("an ask that names no number kept %v", got)
	}
	for _, n := range []int{1, 21, -3} {
		req, err := k.ask(updater.Ask{Version: "0.5.0", AskedBy: "operator", Keep: n}, false)
		if err == nil || req == nil || !strings.Contains(req.Error, "keep") || k.running() != "0.6.0" {
			t.Fatalf("keep %d: %v %+v", n, err, req)
		}
	}
	if st, _ := k.u.State(); st.Keep != 3 {
		t.Fatalf("a refused number was kept: %d", st.Keep)
	}
}

// A release no longer kept takes its snapshot of the database with it; a kept one keeps its own:
// one dump for each kept version (16.4 step 7, 16.9).
func TestAReleasesSnapshotGoesWithIt(t *testing.T) {
	k := newKit(t, "0.1.0", "0.1.0", "0.2.0", "0.3.0", "0.4.0")
	snaps := filepath.Join(filepath.Dir(k.rel), "updater", "snapshots")
	if err := os.MkdirAll(snaps, 0o700); err != nil {
		t.Fatal(err)
	}
	for _, v := range []string{"0.2.0", "0.3.0"} {
		if err := os.WriteFile(filepath.Join(snaps, v+".tar.gz"), []byte(v), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	req, _ := k.ask(updater.Ask{Version: "0.4.0", AskedBy: "operator", Keep: 3}, false)
	if req.State != updater.StateDone || k.kept() != "0.1.0 0.3.0 0.4.0" {
		t.Fatalf("%+v, kept %s", req, k.kept())
	}
	if _, err := os.Stat(filepath.Join(snaps, "0.2.0.tar.gz")); !os.IsNotExist(err) {
		t.Fatalf("the snapshot of 0.2.0 stays: %v", err)
	}
	for _, v := range []string{"0.1.0", "0.3.0"} {
		if _, err := os.Stat(filepath.Join(snaps, v+".tar.gz")); err != nil {
			t.Fatalf("the snapshot of kept %s: %v", v, err)
		}
	}
}

// The server's client names updates.keep_versions in every install ask (D30); a number it cannot
// read, or one outside 2 to 20 that a set stored before the bound may hold, is sent as none, so the
// updater keeps the number it has and no install is refused for it.
func TestTheClientSendsKeepVersions(t *testing.T) {
	var got []int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var a updater.Ask
		_ = json.NewDecoder(r.Body).Decode(&a)
		got = append(got, a.Keep)
		w.WriteHeader(http.StatusAccepted)
		_, _ = w.Write([]byte(`{"version":"` + a.Version + `"}`))
	}))
	defer srv.Close()
	for _, c := range []struct {
		n   int
		err error
	}{{4, nil}, {0, errors.New("the database")}, {1, nil}, {50, nil}} {
		cl := &updater.Client{HTTP: srv.Client(), Base: srv.URL,
			Keep: func(context.Context) (int, error) { return c.n, c.err }}
		if _, err := cl.Install(context.Background(), updater.Ask{Version: "0.2.0", AskedBy: "operator"}); err != nil {
			t.Fatal(err)
		}
	}
	cl := &updater.Client{HTTP: srv.Client(), Base: srv.URL, Keep: func(context.Context) (int, error) { return 4, nil }}
	if _, err := cl.Install(context.Background(), updater.Ask{Version: "0.2.0", AskedBy: "operator", Keep: 6}); err != nil {
		t.Fatal(err)
	}
	if fmt.Sprint(got) != "[4 0 0 0 6]" {
		t.Fatalf("sent %v", got)
	}
}

// A release newer than the one that runs is never removed for keep: it waits to be installed (16.3,
// security review of 1be8ea7, finding 1). With keep 2, installing 0.5.0 while 0.6.0 waits keeps
// 0.6.0, beside the running one and the one before it.
func TestAReleaseNewerThanTheRunningOneIsKept(t *testing.T) {
	k := newKit(t, "0.1.0", "0.1.0", "0.2.0", "0.3.0", "0.4.0", "0.5.0", "0.6.0")
	req, _ := k.ask(updater.Ask{Version: "0.5.0", AskedBy: "clock", Keep: 2}, false)
	if req.State != updater.StateDone {
		t.Fatalf("%+v", req)
	}
	if got := k.kept(); got != "0.1.0 0.5.0 0.6.0" || k.m.called("Remove 0.6.0") {
		t.Fatalf("kept %v, calls %v", got, k.m.Calls())
	}
}

// The number an ask names is kept only by an install that reached its step Keep (security review of
// 1be8ea7, finding 2): an install that fails, or a gate, leaves the number the updater has.
func TestOnlyAnInstallThatKeepsStoresItsNumber(t *testing.T) {
	k := newKit(t, "0.1.0", "0.1.0", "0.2.0", "0.3.0")
	k.m.fail["Smoke 0.2.0"] = errors.New("it broke")
	req, _ := k.ask(updater.Ask{Version: "0.2.0", AskedBy: "operator", Keep: 3}, false)
	if req.State != updater.StateFailed {
		t.Fatalf("%+v", req)
	}
	if st, _ := k.u.State(); st.Keep != 0 {
		t.Fatalf("a failed install stored keep %d", st.Keep)
	}
	delete(k.m.fail, "Smoke 0.2.0")
	if req, _ = k.ask(updater.Ask{Version: "0.2.0", AskedBy: "operator", Keep: 4}, false); req.State != updater.StateDone {
		t.Fatalf("%+v", req)
	}
	if st, _ := k.u.State(); st.Keep != 4 {
		t.Fatalf("the install that kept stored %d", st.Keep)
	}
	if !strings.Contains(lastDetail(req), "4 releases kept") {
		t.Fatalf("the step Keep does not name the number: %q", lastDetail(req))
	}
	// A gate that names a number stores none (round 2 of the security review).
	g := gateAsk(k.release("0.4.0", 51, 0, false), true)
	g.Keep = 2
	if req, err := k.ask(g, false); err != nil || req.State != updater.StateDone || req.Action != updater.ActionGate {
		t.Fatalf("%+v %v", req, err)
	}
	if st, _ := k.u.State(); st.Keep != 4 {
		t.Fatalf("a gate stored keep %d", st.Keep)
	}
}

// An install that is stopped before its switch stores no number either (round 2 of the security
// review of 1be8ea7).
func TestAStoppedInstallStoresNoNumber(t *testing.T) {
	k := newKit(t, "0.1.0", "0.1.0", "0.2.0")
	block := make(chan struct{})
	k.m.blockLoad = block
	if _, err := k.u.Start(context.Background(), updater.Ask{Version: "0.2.0", AskedBy: "operator", Keep: 3}, false); err != nil {
		t.Fatal(err)
	}
	stopped := make(chan error, 1)
	go func() { stopped <- k.u.Stop() }()
	time.Sleep(20 * time.Millisecond)
	close(block)
	if err := <-stopped; err != nil {
		t.Fatal(err)
	}
	if st, _ := k.u.State(); st.Keep != 0 || st.Running != "0.1.0" {
		t.Fatalf("a stopped install stored %+v", st)
	}
}
