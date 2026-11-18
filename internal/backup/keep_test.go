package backup_test

import (
	"context"
	"strings"
	"testing"

	"github.com/kesokaj/crew/internal/backup"
	"github.com/kesokaj/crew/internal/db"
)

// The days' files are kept backup_keep deep, and a month file is kept for good (16.9): three nights
// with Keep 1 leave one day's file and the ended month's file.
func TestAMonthFileIsKeptForGoodBesideBackupKeepDays(t *testing.T) {
	ctx := context.Background()
	d, now := monthKit(t)
	dir := t.TempDir()
	for i := range 3 {
		if _, err := backup.Daily(ctx, backup.Plan{Conn: d.Conn(db.RoleRead), Dir: dir, Keep: 1, Now: now.AddDate(0, 0, i)}); err != nil {
			t.Fatal(err)
		}
	}
	days, _ := backup.List(dir)
	months, _ := backup.ListMonths(dir)
	if len(days) != 1 || !strings.HasSuffix(days[0], backup.Name(now.AddDate(0, 0, 2))) || strings.Join(months, " ") != "2026-08" {
		t.Fatalf("days %v, months %v", days, months)
	}
}
