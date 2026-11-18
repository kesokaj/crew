package deploy

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"go.yaml.in/yaml/v3"
)

// The server and the ingress join the sandboxes' and the hosted projects' bridges, so Compose starts
// both with IPv4 forwarding off in their network namespace (13.6; BUG-020): Docker's default is the
// machine's, which forwards.
func TestTheServerAndTheIngressForwardNothing(t *testing.T) {
	b, err := os.ReadFile("compose.yaml")
	if err != nil {
		t.Fatal(err)
	}
	var c struct {
		Services map[string]struct {
			Sysctls     map[string]string `yaml:"sysctls"`
			Environment map[string]string `yaml:"environment"`
			DNS         []string          `yaml:"dns"`
		} `yaml:"services"`
	}
	if err := yaml.Unmarshal(b, &c); err != nil {
		t.Fatal(err)
	}
	for _, s := range []string{"crew-server", "crew-caddy"} {
		svc, ok := c.Services[s]
		if !ok {
			t.Fatalf("compose.yaml has no service %s", s)
		}
		if got := svc.Sysctls["net.ipv4.ip_forward"]; got != "0" {
			t.Errorf("%s: net.ipv4.ip_forward is %q, want \"0\"", s, got)
		}
		if strings.Join(svc.DNS, ",") != "8.8.8.8,1.1.1.1" {
			t.Errorf("%s: dns is %v, want [8.8.8.8 1.1.1.1] so a boot before DHCP writes /etc/resolv.conf still leaves 127.0.0.11 with external nameservers", s, svc.DNS)
		}
	}
	// The operator's override reaches the server from the install file, which crew-ctl and the
	// updater both hand Compose, and is empty unless it is set there.
	if got := c.Services["crew-server"].Environment["CREW_ALLOW_IP_FORWARD"]; got != "${CREW_ALLOW_IP_FORWARD:-}" {
		t.Errorf("crew-server: CREW_ALLOW_IP_FORWARD is %q, want ${CREW_ALLOW_IP_FORWARD:-}", got)
	}
}

// The override is a key of the install file (13.6, since 0.85): edit takes 1 or empty, a write of the
// file by install or edit keeps it, and doctor says while it is set.
func TestTheForwardingOverrideIsAKeyOfTheInstallFile(t *testing.T) {
	c := newCtl(t)
	c.install(good + "CREW_ALLOW_IP_FORWARD=1\n")
	envFile := filepath.Join(c.root, "etc", "crew", "crew.env")
	if out, code := c.run("edit", "--dry-run", "CREW_ALLOW_IP_FORWARD=yes"); code == 0 {
		t.Errorf("CREW_ALLOW_IP_FORWARD=yes was accepted\n%s", out)
	}
	for _, kv := range []string{"CREW_ALLOW_IP_FORWARD=1", "CREW_ALLOW_IP_FORWARD="} {
		if out, code := c.run("edit", "--dry-run", kv); code != 0 || !strings.Contains(out, "+ write") {
			t.Errorf("%s: exit %d\n%s", kv, code, out)
		}
	}
	// What install and edit run: the answers read from the file, another one changed, the file
	// written again. install(1) is the folder's, which needs root, so it is left out.
	cmd := exec.Command("bash", "-c", `source crew-ctl; install() { :; }; DRY=0; load_answers; A_CREW_TIMEZONE=Europe/Stockholm; write_env`)
	cmd.Env = c.env
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	b, err := os.ReadFile(envFile)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(b), "\nCREW_ALLOW_IP_FORWARD=1\n") || !strings.Contains(string(b), "\nCREW_TIMEZONE=Europe/Stockholm\n") {
		t.Fatalf("the install file written again lost the override:\n%s", b)
	}
	warn := "CREW_ALLOW_IP_FORWARD=1: the server starts sandboxes even when its network forwards"
	if out, _ := c.run("doctor"); !strings.Contains(out, warn) {
		t.Errorf("doctor does not say the override is set:\n%s", out)
	}
	c.install(good)
	if out, _ := c.run("doctor"); strings.Contains(out, "CREW_ALLOW_IP_FORWARD") {
		t.Errorf("doctor names an override that is not set:\n%s", out)
	}
}

// Doctor fails when the server or the ingress runs without forwarding off, and names the deploy files
// to put in place (13.6; BUG-020); a service that does not run is not named.
func TestDoctorSaysWhetherTheServerForwards(t *testing.T) {
	c := newCtl(t)
	c.install(good)
	// A docker that runs the containers in FAKE_UP and answers each one's sysctl from FAKE_<name>.
	fake := `#!/bin/bash
case "$1" in
ps) for n in ${FAKE_UP}; do echo "$n"; [ -z "${FAKE_SLOW:-}" ] || sleep 0.2; done ;;
inspect) v="FAKE_${!#//-/_}"; echo "${!v:-<no value>}" ;;
esac
`
	if err := os.WriteFile(filepath.Join(c.root, "bin", "docker"), []byte(fake), 0o755); err != nil {
		t.Fatal(err)
	}
	doctor := func(env ...string) string {
		cmd := exec.Command("bash", "-c", `source crew-ctl; DOC_RESULTS=(); doctor_forwarding; printf '%s\n' "${DOC_RESULTS[@]}"`)
		cmd.Env = append(append([]string{}, c.env...), env...)
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("%v\n%s", err, out)
		}
		return string(out)
	}
	out := doctor("FAKE_UP=crew-server crew-caddy", "FAKE_crew_server=0", "FAKE_crew_caddy=0")
	if !strings.Contains(out, "ok|forwarding|crew-server is started with net.ipv4.ip_forward=0") ||
		!strings.Contains(out, "ok|forwarding|crew-caddy is started with net.ipv4.ip_forward=0") {
		t.Errorf("both forward nothing:\n%s", out)
	}
	// crew-ctl runs with pipefail: a grep that stops reading at its match must not turn the docker that
	// writes on after it into a failed pipe, and so a running server into one that is not there (BUG-021).
	out = doctor("FAKE_UP=crew-server crew-caddy", "FAKE_SLOW=1", "FAKE_crew_server=0", "FAKE_crew_caddy=0")
	if !strings.Contains(out, "ok|forwarding|crew-server is started with net.ipv4.ip_forward=0") {
		t.Errorf("a docker that writes on after the match:\n%s", out)
	}
	out = doctor("FAKE_UP=crew-server crew-caddy", "FAKE_crew_caddy=0")
	if !strings.Contains(out, "fail|forwarding|crew-server is not started with net.ipv4.ip_forward=0") ||
		!strings.Contains(out, "crew-ctl install --deploy-files") || strings.Contains(out, "fail|forwarding|crew-caddy") {
		t.Errorf("a server started with Docker's default:\n%s", out)
	}
	if out := doctor("FAKE_UP=crew-db"); strings.Contains(out, "forwarding") {
		t.Errorf("a service that does not run is named:\n%s", out)
	}
	// The whole `doctor` command runs the check, so deleting its call from cmd_doctor is caught: the
	// fail line for a running server that forwards is in `doctor`'s own output, not only the function's.
	c.env = append(c.env, "FAKE_UP=crew-server crew-caddy", "FAKE_crew_server=1", "FAKE_crew_caddy=0")
	out, _ = c.run("doctor")
	if !strings.Contains(out, "forwarding") || !strings.Contains(out, "crew-server is not started with net.ipv4.ip_forward=0") {
		t.Errorf("crew-ctl doctor does not run the forwarding check:\n%s", out)
	}
}
