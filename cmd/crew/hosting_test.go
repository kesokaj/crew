package main

import "testing"

// The orchestrator gets the ingress's container and the server's listener from the server's own
// environment (19.4, BUG-003): without the ingress no hosted bridge can be made, and without the
// listener the probe of a hosted bridge does not try the server's port.
func TestTheOrchestratorKnowsTheIngressAndTheListener(t *testing.T) {
	t.Setenv("CREW_INGRESS_CONTAINER", "crew-caddy")
	t.Setenv("CREW_SELF_CONTAINER", "crew-server")
	t.Setenv("CREW_LISTEN", ":8080")
	cfg := orchConfig(loadEnv())
	if cfg.IngressContainer != "crew-caddy" || cfg.SelfContainer != "crew-server" || cfg.Addr != ":8080" || cfg.BrokerAddr != ":8091" {
		t.Fatalf("%+v", cfg)
	}
}
