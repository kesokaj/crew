package httpapi_test

import (
	"context"
	"strings"
	"testing"
)

// A case whose work Crew cannot compare keeps Crew's own sentence with its fit check, never git's error,
// and ends waiting for the operator (rule 14; since 1.14, the security review of 1.14's change, second
// round, finding 1).
func TestAFitCheckThatCannotCompareItsWorkKeepsCrewsSentence(t *testing.T) {
	t.Parallel()
	k := newTicketKit(t)
	k.needShop("need: Counts\n", "nothing needed\n", "nothing needed\n")
	ctx := context.Background()
	k.call("POST", "/api/projects/shop/evals/cases", `{"ticket": "SF-1", "heldOut": ["1.txt"], "why": "typical"}`, 201, nil)
	k.d.Server.Exec(ctx, `UPDATE eval_cases SET base_commit = '0123456789abcdef0123456789abcdef01234567' WHERE number = 1`)
	stop := k.run()
	k.until("the case's check ended", func() bool { return k.cases()[1].Fit.State == "error" })
	stop()
	why := k.cases()[1].Fit.Why
	if want := "Crew could not give the judge the case: the case's work cannot be read"; !strings.HasSuffix(why, want) || strings.Contains(why, "0123456789ab") {
		t.Fatalf("the case's reason: %q", why)
	}
}
