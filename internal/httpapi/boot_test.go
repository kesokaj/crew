package httpapi_test

import (
	"context"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/kesokaj/crew/internal/hold"
	"github.com/kesokaj/crew/internal/inbox"
)

// The inbox's two items of the machine through the app (21): the frame says microVMs do not start;
// Run the check starts a probe, one at a time; Resume lifts the hold and sets the count to 0, with
// its row in the history; Read it takes the time the database could not be written away.
func TestTheMachinesItemsThroughTheApp(t *testing.T) {
	t.Parallel()
	k := newTicketKit(t)
	ctx := context.Background()
	probes := make(chan struct{}, 4)
	k.driver.Probe = func(context.Context) error { probes <- struct{}{}; return nil }
	for range 2 {
		if _, err := hold.Boot(ctx, k.d.Server, false, "kata: /dev/kvm is missing"); err != nil {
			t.Fatal(err)
		}
	}
	var f struct{ Held string }
	k.call("GET", "/api/frame", "", 200, &f)
	if !strings.HasPrefix(f.Held, "MicroVMs do not start: kata: /dev/kvm is missing") {
		t.Fatalf("the frame: %q", f.Held)
	}
	var started struct{ Started bool }
	k.call("POST", "/api/machine/boot/probe", "", 202, &started)
	select {
	case <-probes:
	case <-time.After(5 * time.Second):
		t.Fatal("Run the check started no probe")
	}
	if !started.Started {
		t.Fatal("Run the check says it started none")
	}
	k.call("POST", "/api/machine/boot/resume", "", 200, nil)
	if st, _ := hold.Read(ctx, k.d.Server); st.BootFailed != "" || st.BootFailures != 0 {
		t.Fatalf("after Resume: %+v", st)
	}
	if k.count(`SELECT count(*) FROM changes WHERE tbl = 'crew_state' AND after->>'bootFailures' = '0'`) != 1 {
		t.Fatal("Resume has no row in the history")
	}
	from := time.Now().Add(-time.Hour)
	if err := hold.WritableAgain(ctx, k.d.Server, &from, time.Now()); err != nil {
		t.Fatal(err)
	}
	var items struct{ Items []inbox.Item }
	k.call("GET", "/api/inbox", "", 200, &items)
	if !slices.ContainsFunc(items.Items, func(it inbox.Item) bool { return it.Kind == inbox.KindDBWritable }) {
		t.Fatalf("%+v", items.Items)
	}
	k.call("POST", "/api/machine/db-writable/read", "", 200, nil)
	k.call("GET", "/api/inbox", "", 200, &items)
	if slices.ContainsFunc(items.Items, func(it inbox.Item) bool { return it.Kind == inbox.KindDBWritable }) {
		t.Fatalf("after Read it: %+v", items.Items)
	}
}
