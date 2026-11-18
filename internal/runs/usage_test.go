package runs

import (
	"context"
	"fmt"
	"testing"

	"github.com/kesokaj/crew/internal/harness"
)

// usageTurn is one turn of agy whose result says the conversation's totals so far (spike item 39).
func usageTurn(in, out, cached int64) harness.Script {
	return harness.Script{Lines: [][]byte{
		[]byte(`{"event":"init","conversation_id":"` + agyConv + `","init":{"model":"gemini-3.8-flash-low"}}`),
		[]byte(fmt.Sprintf(`{"event":"result","result":{"conversation_id":"%s","status":"SUCCESS","response":"ok","usage":{"input_tokens":%d,"output_tokens":%d,"cache_read_tokens":%d}}}`,
			agyConv, in, out, cached)),
	}}
}

// usageOf is a run's tokens in, out, and read from the cache, and whether a cost is written on it.
func (k *kit) usageOf(run string) (in, out, cached int64, costed bool) {
	k.t.Helper()
	if err := k.db.Server.QueryRow(context.Background(), `SELECT tokens_in, tokens_out, tokens_cached, cost_usd IS NOT NULL OR cost_basis IS NOT NULL
		FROM runs WHERE id = $1`, run).Scan(&in, &out, &cached, &costed); err != nil {
		k.t.Fatal(err)
	}
	return in, out, cached, costed
}

// agy reports tokens as totals of the conversation: each turn adds the difference to its run, and no
// cost is written, since Crew estimates none (12.7, since 0.53).
func TestATurnsTokensAreTheDifferenceOfItsTotals(t *testing.T) {
	k := newKit(t)
	k.play(usageTurn(1000, 100, 0), usageTurn(3000, 300, 5000))
	run := k.start(StartRequest{Model: "gemini-3.8-flash", Effort: "low"})
	v := k.wait(run, "idle")
	if in, out, cached, costed := k.usageOf(run); in != 1000 || out != 100 || cached != 0 || costed {
		t.Fatalf("after one turn: %d %d %d, a cost %v", in, out, cached, costed)
	}
	if _, err := k.d.Continue(context.Background(), v.Subtask, ContinueRequest{Text: "More."}); err != nil {
		t.Fatal(err)
	}
	k.wait(run, "idle")
	if in, out, cached, costed := k.usageOf(run); in != 3000 || out != 300 || cached != 5000 || costed {
		t.Fatalf("after two turns: %d %d %d, a cost %v", in, out, cached, costed)
	}
}

// A conversation whose totals were written before Crew kept the cache's tokens takes its first
// reading as the baseline, so a turn is not given the whole conversation's cache.
func TestTotalsFromBeforeTheCacheWasKeptAreTheBaseline(t *testing.T) {
	k := newKit(t)
	k.play(usageTurn(1000, 100, 0), usageTurn(1000, 100, 90000))
	run := k.start(StartRequest{Model: "gemini-3.8-flash", Effort: "low"})
	v := k.wait(run, "idle")
	if _, err := k.db.Server.Exec(context.Background(), `UPDATE conversations SET cli_totals = cli_totals - 'tokensCached'
		WHERE id = (SELECT conversation FROM runs WHERE id = $1)`, run); err != nil {
		t.Fatal(err)
	}
	if _, err := k.d.Continue(context.Background(), v.Subtask, ContinueRequest{Text: "More."}); err != nil {
		t.Fatal(err)
	}
	k.wait(run, "idle")
	if _, _, cached, _ := k.usageOf(run); cached != 0 {
		t.Fatalf("%d: the old totals' cache was given to one turn", cached)
	}
}
