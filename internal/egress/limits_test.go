package egress

import (
	"testing"
	"time"
)

// The tunnels' limits are the review's numbers (13.6, review of M10): the tests that shorten them
// put them back, so what ships is this.
func TestTheTunnelsLimitsAreTheDesigns(t *testing.T) {
	if maxTunnels != 64 || idleTimeout != 15*time.Minute || helloTimeout != 15*time.Second {
		t.Fatalf("maxTunnels %d, idleTimeout %s, helloTimeout %s", maxTunnels, idleTimeout, helloTimeout)
	}
}

// The module proxy's limits and its one way out are the design's (13.6, since 0.61): a file at most
// 512 MiB, Go's own limit of a module's zip being 500 MiB, ten minutes for one, and only Go's proxy
// and its bucket.
func TestTheModuleProxysLimitsAreTheDesigns(t *testing.T) {
	if maxModuleBytes != 512<<20 || moduleTimeout != 10*time.Minute || modulesOrigin != "https://proxy.golang.org" ||
		modulesBucket != "https://storage.googleapis.com/proxy-golang-org-prod/" || ModulesPath != "/goproxy/" {
		t.Fatalf("maxModuleBytes %d, moduleTimeout %s, modulesOrigin %s, modulesBucket %s, ModulesPath %s",
			maxModuleBytes, moduleTimeout, modulesOrigin, modulesBucket, ModulesPath)
	}
}
