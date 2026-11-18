package projterm_test

import (
	"context"
	"strings"
	"testing"

	"github.com/kesokaj/crew/internal/gitendpoint"
)

// A push from the terminal whose refs the git endpoint could not read names main with no new value:
// whatever it brought to main is not taken, Crew's main is given back, and the history says why
// (since 1.08).
func TestAMainWhoseRefsCouldNotBeReadIsNotTaken(t *testing.T) {
	ctx := context.Background()
	s, f := setup(t)
	term, err := s.Open(ctx, "shop", "", "operator")
	if err != nil {
		t.Fatal(err)
	}
	back := f.givenBack
	s.Pushed(gitendpoint.Access{Repo: "shop", Who: "terminal " + term.Sandbox}, "refs/heads/main", "", "")
	h := history(t, f)
	if len(f.held) != 0 || f.givenBack != back+1 || !strings.Contains(h[len(h)-1], "the refs of its push could not be read") {
		t.Fatalf("held %v, given back %d times, history %q", f.held, f.givenBack-back, h[len(h)-1])
	}
}
