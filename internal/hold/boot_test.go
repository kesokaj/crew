package hold_test

import (
	"context"
	"strings"
	"testing"

	"github.com/kesokaj/crew/internal/db/dbtest"
	"github.com/kesokaj/crew/internal/hold"
)

// The error kept for the boot hold is at most 600 bytes, cut at a rune and marked: Kata's can be a
// long stack, and it is shown in the top bar and the inbox (security review of 5d1b414).
func TestABootErrorIsKeptAt600Bytes(t *testing.T) {
	ctx := context.Background()
	d := dbtest.New(t, false)
	long := "kata: " + strings.Repeat("é", 2000)
	for range 2 {
		if _, err := hold.Boot(ctx, d.Server, false, long); err != nil {
			t.Fatal(err)
		}
	}
	st, err := hold.Read(ctx, d.Server)
	if err != nil {
		t.Fatal(err)
	}
	if len(st.BootFailed) > 600+len(" …") || !strings.HasSuffix(st.BootFailed, "é …") || !strings.HasPrefix(st.BootFailed, "kata: é") {
		t.Fatalf("kept %d bytes: …%q", len(st.BootFailed), st.BootFailed[len(st.BootFailed)-10:])
	}
}
