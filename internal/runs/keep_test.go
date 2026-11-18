package runs

import (
	"context"
	"testing"
	"time"

	"github.com/kesokaj/crew/internal/harness"
	"github.com/kesokaj/crew/internal/settings"
)

// A run's CLI files are kept for keep_cli_files_days after its last turn (12.10, 14.1; M17 phase 2):
// the listing's 7 days, then the machine's own number, not a number of the code, so the Kept card of
// Machine settings changes it.
func TestARunsCLIFilesAreKeptForKeepCLIFilesDays(t *testing.T) {
	k := newKit(t)
	ctx := context.Background()
	file := ".gemini/antigravity-cli/conversations/" + agyConv + ".db"
	keptFor := func() time.Duration {
		t.Helper()
		k.play(harness.Script{Recording: agyRun, Files: map[string][]byte{file: []byte("{}\n")}})
		run := k.start(StartRequest{})
		k.wait(run, "idle")
		var secs float64
		if err := k.db.Server.QueryRow(ctx, `SELECT extract(epoch FROM f.keep_until - f.saved_at) FROM cli_files f
			JOIN runs r ON r.conversation = f.conversation WHERE r.id = $1`, run).Scan(&secs); err != nil {
			t.Fatal(err)
		}
		return time.Duration(secs * float64(time.Second))
	}
	near := func(got, want time.Duration) bool { return got > want-time.Minute && got < want+time.Minute }
	if got := keptFor(); !near(got, 7*24*time.Hour) {
		t.Fatalf("by default kept for %s after the turn, want 7 days", got)
	}
	def, _ := settings.Defaults(settings.Machine{Version: "1.0.0"})
	doc := settings.Merge(def, settings.Doc{"keep_cli_files_days": 3})
	if err := settings.Save(ctx, k.db.Server, settings.Machine{Version: "1.0.0"}, doc, "test"); err != nil {
		t.Fatal(err)
	}
	if got := keptFor(); !near(got, 3*24*time.Hour) {
		t.Fatalf("set to 3, kept for %s after the turn", got)
	}
}
