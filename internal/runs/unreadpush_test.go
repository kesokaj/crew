package runs

import (
	"context"
	"testing"

	"github.com/kesokaj/crew/internal/gitendpoint"
)

// A ref the git endpoint could not read, which it names with no new value, is no push to record
// (since 1.08).
func TestARefWithNoValueIsNoPushToRecord(t *testing.T) {
	k := newKit(t)
	k.d.RecordPush(gitendpoint.Access{Repo: "fixture", Who: "terminal t"}, "refs/heads/main", "", "")
	var n int
	if err := k.db.Server.QueryRow(context.Background(), `SELECT count(*) FROM ref_pushes`).Scan(&n); err != nil || n != 0 {
		t.Fatalf("%d pushes recorded, %v", n, err)
	}
}
