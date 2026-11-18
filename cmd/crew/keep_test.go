package main

import (
	"context"
	"testing"

	"github.com/kesokaj/crew/internal/db/dbtest"
	"github.com/kesokaj/crew/internal/settings"
)

// The server's keep numbers come from the machine's settings (14.1, 16.9, D30): the updater's
// client names updates.keep_versions in its asks, and the daily backup keeps updates.backup_keep
// files; the listing's 5 and 14 until the operator sets them.
func TestTheServerReadsTheKeepNumbersFromTheSettings(t *testing.T) {
	ctx := context.Background()
	d := dbtest.New(t, false)
	e := env{DataDir: "/var/lib/crew"}
	c := updaterClient("/nonexistent.sock", d.Server)
	keep := func() int {
		t.Helper()
		n, err := c.Keep(ctx)
		if err != nil {
			t.Fatal(err)
		}
		return n
	}
	backups := func() int {
		t.Helper()
		_, n, err := backupPlace(ctx, d.Server, e)
		if err != nil {
			t.Fatal(err)
		}
		return n
	}
	if keep() != 5 || backups() != 14 {
		t.Fatalf("the listing's: keep %d, backups %d", keep(), backups())
	}
	def, _ := settings.Defaults(e.machine())
	doc := settings.Merge(def, settings.Doc{"updates": settings.Doc{"keep_versions": 7, "backup_keep": 3}})
	if err := settings.Save(ctx, d.Server, e.machine(), doc, "test"); err != nil {
		t.Fatal(err)
	}
	if keep() != 7 || backups() != 3 {
		t.Fatalf("the operator's: keep %d, backups %d", keep(), backups())
	}
}
