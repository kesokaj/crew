package httpapi_test

import (
	"context"
	"strings"
	"testing"

	"github.com/kesokaj/crew/baseline"
	"github.com/kesokaj/crew/internal/release"
)

// A baseline file of the release's commit that git cannot read, here one past the bound of a read,
// skips the gate's eval suites as one the commit does not have does, and says that it could not be
// read, not that the commit has none (16.2 step 5, since 1.04).
func TestTheGatesEvalSuitesSayABaselineFileCouldNotBeRead(t *testing.T) {
	t.Parallel()
	k, live, l := gateEvals(t, "")
	k.earlierSuite(t)
	// Just below the largest of the files, so the reads before them are within it.
	var largest int64
	for _, f := range baseline.EvalFiles() {
		text, err := baseline.Text(f)
		if err != nil {
			t.Fatal(err)
		}
		largest = max(largest, int64(len(text)))
	}
	k.b.Git.MaxOutput = largest - 1
	res, err := live.Step(context.Background(), l)
	if err != nil || res == nil || res.Live != release.LiveSkipped || len(res.Skipped) != 1 ||
		!strings.Contains(res.Skipped[0], "could not be read") || strings.Contains(res.Skipped[0], "has no baseline/") {
		t.Fatalf("%+v %v", res, err)
	}
}
