package board

import (
	"context"
	"fmt"
	"slices"

	"github.com/jackc/pgx/v5"

	"github.com/kesokaj/crew/internal/settings"
)

// What the operator did when the pipeline did not run by itself, or decided otherwise than they
// would (10.2). These count most.
const (
	SentBack        = "breakdown_sent_back"
	EditedBeforeOK  = "edited_before_ok"
	FeatureDiscard  = "feature_discarded"
	DecisionChanged = "decision_changed"
	LimitRaised     = "limit_raised"
	MergeReverted   = "merge_reverted"
)

// intervention records that the operator stepped in, as the signal operator_intervention with what
// they did and the screen's words for it.
func intervention(ctx context.Context, tx pgx.Tx, project string, ticket int64, what, text string, more map[string]any) error {
	d := map[string]any{"what": what, "text": text}
	for k, v := range more {
		d[k] = v
	}
	return RecordSignal(ctx, tx, Signal{Project: project, Ticket: ticket, Kind: "operator_intervention", Detail: d})
}

// raisedLimits are the project's limits that went up in a change of its settings: every number under
// limits (10.2, "raises a project limit"; since 0.53 none is a budget).
func raisedLimits(before, after settings.Doc) []map[string]any {
	var out []map[string]any
	var keys []string
	if l, ok := after["limits"].(settings.Doc); ok {
		for k := range l {
			keys = append(keys, "limits."+k)
		}
	}
	slices.Sort(keys)
	for _, k := range keys {
		was, ok1 := settingNumber(before, k)
		now, ok2 := settingNumber(after, k)
		if ok1 && ok2 && now > was {
			out = append(out, map[string]any{"key": k, "from": was, "to": now})
		}
	}
	return out
}

func limitText(r map[string]any) string {
	return fmt.Sprintf("Raised %v from %v to %v", r["key"], r["from"], r["to"])
}
