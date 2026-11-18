package runs

import (
	"context"
	"errors"
	"testing"
)

// On a docs or chore ticket, a review's guard checks the flash bracket (so a list with claude-opus-5-5
// and gemini-3.8-flash, and no Anthropic flash entry, lets the review fall back to gemini-3.8-flash with
// check_rule_not_met), while a custom stage check on the same ticket stays on pro and refuses
// gemini-3.8-flash while claude-opus-5-5 can run.
func TestDocsAndChoreReviewGuardUsesFlashBracket(t *testing.T) {
	k, ticket := guardKit(t)
	ctx := context.Background()
	if _, err := k.db.Server.Exec(ctx, `UPDATE tickets SET kind = 'docs' WHERE id = $1`, ticket); err != nil {
		t.Fatal(err)
	}
	k.built(ticket, "gemini-3.8-flash")
	k.setting("", `{"agents": {"roles": {"reviewer": {"model": ["agy/claude-opus-5-5", "agy/gemini-3.8-flash"]}}}}`)

	// Stage check uses pro: claude-opus-5-5 is offered and runnable, so gemini-3.8-flash is refused.
	if _, err := k.check(ticket, "stage", "stage", "gemini-3.8-flash"); !errors.Is(err, ErrCheckFamily) {
		t.Fatalf("stage check on builder's family while claude-opus-5-5 is available: %v", err)
	}

	// Docs review uses flash: claude-opus-5-5 is pro (disallowed for flash review), so gemini-3.8-flash
	// is allowed with check_rule_not_met.
	run, err := k.check(ticket, "reviewer", "review", "gemini-3.8-flash")
	if err != nil {
		t.Fatalf("docs review fallback when no Anthropic flash model is listed: %v", err)
	}
	k.wait(run, "idle", "ended")
	if n := k.count(`SELECT count(*) FROM signals WHERE kind = 'check_rule_not_met' AND run = $1 AND ticket = $2`, run, ticket); n != 1 {
		t.Fatalf("expected 1 check_rule_not_met signal for docs review fallback, got %d", n)
	}
}
