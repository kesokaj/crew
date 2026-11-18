package inbox_test

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/kesokaj/crew/internal/db/dbtest"
	"github.com/kesokaj/crew/internal/hold"
	"github.com/kesokaj/crew/internal/inbox"
)

func itemOf(t *testing.T, items []inbox.Item, kind string) *inbox.Item {
	t.Helper()
	for i := range items {
		if items[i].Kind == kind {
			return &items[i]
		}
	}
	return nil
}

// Two microVMs that did not boot put the Kata and KVM check in the inbox (21): the error with the
// machine check's line, Run the check (a probe now) and Resume; the item goes when a boot lifts the
// hold. The database written again is an item with the time it could not be written, from and to,
// or from a time not known, until Read it.
func TestTheMachinesTwoItems(t *testing.T) {
	ctx := context.Background()
	d := dbtest.New(t, false)
	m := machine(t, nil)
	list := func() []inbox.Item {
		t.Helper()
		items, err := inbox.List(ctx, d.Server, m)
		if err != nil {
			t.Fatal(err)
		}
		return items
	}
	for range 2 {
		if _, err := hold.Boot(ctx, d.Server, false, "kata: /dev/kvm is missing; the machine check: Docker 29.1.3 has the runtime mvm"); err != nil {
			t.Fatal(err)
		}
	}
	it := itemOf(t, list(), inbox.KindMachineBoot)
	if it == nil || it.ID != "boot" || it.Title != "MicroVMs do not start: new runs are held" ||
		!strings.HasPrefix(it.Body, "kata: /dev/kvm is missing; the machine check: Docker 29.1.3 has the runtime mvm.") ||
		len(it.Alternatives) != 2 || it.Alternatives[0].Label != "Run the check" || it.Alternatives[0].Action != "/api/machine/boot/probe" ||
		it.Alternatives[1].Label != "Resume" || it.Alternatives[1].Action != "/api/machine/boot/resume" || !it.Alternatives[1].Main {
		t.Fatalf("%+v", it)
	}
	if _, err := hold.Boot(ctx, d.Server, true, ""); err != nil {
		t.Fatal(err)
	}
	if it := itemOf(t, list(), inbox.KindMachineBoot); it != nil {
		t.Fatalf("after a boot: %+v", it)
	}

	from := time.Now().Add(-40 * time.Minute)
	if err := hold.WritableAgain(ctx, d.Server, &from, from.Add(14*time.Minute)); err != nil {
		t.Fatal(err)
	}
	it = itemOf(t, list(), inbox.KindDBWritable)
	if it == nil || it.Title != "The database can be written again" || it.Since.Sub(from.Add(14*time.Minute)).Abs() > time.Second ||
		!strings.HasPrefix(it.Body, "Crew could not write its database for 14 min, until 26 min ago.") ||
		len(it.Alternatives) != 1 || it.Alternatives[0].Label != "Read it" || it.Alternatives[0].Action != "/api/machine/db-writable/read" {
		t.Fatalf("%+v", it)
	}
	if err := hold.WritableAgain(ctx, d.Server, nil, from); err != nil {
		t.Fatal(err)
	}
	if it = itemOf(t, list(), inbox.KindDBWritable); it == nil ||
		!strings.HasPrefix(it.Body, "Crew could not write its database until 40 min ago, from a time not known: Crew restarted while it could not write.") {
		t.Fatalf("%+v", it)
	}
	tx, err := d.Server.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err := hold.ReadWritable(ctx, tx, "operator"); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	if it := itemOf(t, list(), inbox.KindDBWritable); it != nil {
		t.Fatalf("after Read it: %+v", it)
	}
}
