package release

import (
	"maps"
	"testing"
)

// A versions.lock is read as a shell and Compose read it: comments skipped, the last line of a key wins.
func TestParseLock(t *testing.T) {
	got := ParseLock([]byte("# A=0\nA=1\n  B=two=2  \nnot a line\nA=3\n"))
	if want := map[string]string{"A": "3", "B": "two=2"}; !maps.Equal(got, want) {
		t.Fatalf("%v", got)
	}
}
