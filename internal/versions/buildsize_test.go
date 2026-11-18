package versions_test

import (
	"context"
	"testing"

	"github.com/kesokaj/crew/internal/capacity"
	"github.com/kesokaj/crew/internal/orchestrator"
	"github.com/kesokaj/crew/internal/settings"
)

// The build of a release and the gate's make check run in a sandbox of the size build, not large: the
// export of the runtime image needs more than a large sandbox's 4 GB over Kata's base, and its
// dockerd was killed by the memory cgroup at that size (L5, design 0.60).
func TestTheBuildAndTheGatesMakeCheckGetTheBuildSize(t *testing.T) {
	k := newGateKit(t)
	doc, err := settings.Defaults(settings.Machine{})
	if err != nil {
		t.Fatal(err)
	}
	k.b.Room = &capacity.Gate{Pool: k.d.Server,
		Host: func(context.Context) (orchestrator.Host, error) {
			return orchestrator.Host{CPUs: 16, MemMiB: 62 * 1024}, nil
		},
		Settings: func(context.Context) (settings.Doc, error) { return doc, nil }}
	k.build("0.2.0")
	k.sameRuntime("0.2.0")
	k.round("local")
	want := capacity.Sizes(doc)[capacity.Build].Sizing()
	if want.NanoCPUs != 3e9 || want.MemBytes != 6<<30 {
		t.Fatalf("the build size's sizing: %+v", want)
	}
	if len(k.h.Builds) != 1 || k.h.Builds[0].Sizing != want {
		t.Fatalf("the build's sandbox: %+v", k.h.Builds)
	}
	if len(k.h.MakeChecks) != 1 || k.h.MakeChecks[0].Sizing != want {
		t.Fatalf("the gate's make check: %+v", k.h.MakeChecks)
	}
}
