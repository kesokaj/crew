package main

import (
	"context"
	"io"
	"log/slog"
	"testing"

	"github.com/kesokaj/crew/internal/mcp"
	"github.com/kesokaj/crew/internal/runs"
)

// The planner waits for the takeover, and the takeover says it is done only after its last step.
func TestThePlannerStartsAfterTheTakeOverOfRuns(t *testing.T) {
	m := &machine{adopted: make(chan struct{}), runs: &runs.Driver{}}
	pl, pipe, err := openPlanning(env{}, nil, m, nil, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	if pl.Ready == nil || pl.Ready != (<-chan struct{})(m.adopted) || pipe.Ready != pl.Ready {
		t.Fatal("the planner or the pipeline does not wait for the takeover of runs")
	}
	if talk := openAssistant(env{}, nil, m, nil, nil, nil); talk.Ready != pl.Ready || talk.Runs != m.runs {
		t.Fatal("the conversations do not wait for the takeover of runs, or start no runs")
	}
	if talk := openAssistant(env{}, nil, nil, nil, nil, nil); talk.Runs != nil {
		t.Fatal("a server without a machine has a service that starts runs")
	}
	// Every tool the baseline has words for is served (11.5): to the runs, or to the operator's own
	// tools (11.7).
	var missing []string
	for _, name := range m.mcp.(*mcp.Server).Missing() {
		if m.outside.Tool(name) == nil {
			missing = append(missing, name)
		}
	}
	if len(missing) != 0 {
		t.Fatalf("tools.yaml has words for tools no one serves: %v", missing)
	}
	if m.runs.MCP != "/mcp" || m.mcp == nil {
		t.Fatal("the runs do not get the Crew MCP")
	}
	var order []string
	closedEarly := false
	takeOver(context.Background(), m.adopted,
		func(context.Context) { order = append(order, "sandboxes") },
		func(context.Context) {
			select {
			case <-m.adopted:
				closedEarly = true
			default:
			}
			order = append(order, "runs")
		})
	select {
	case <-m.adopted:
	default:
		t.Fatal("the takeover did not say it was done")
	}
	if closedEarly || len(order) != 2 || order[1] != "runs" {
		t.Fatalf("closed early %v, order %v", closedEarly, order)
	}
}
