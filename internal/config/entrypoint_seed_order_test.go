package config

import (
	"os"
	"regexp"
	"strings"
	"testing"
)

// The harness reaches a home from the image's skeleton, before /run/crew/ready, and the boot
// reports it after: the markers are cleared first, the copy keeps what the home has, and
// harness_finish writes /run/crew/harness-done once, in the background, after ready (design 12.1).
func TestEntrypointCopiesTheHarnessBeforeReady(t *testing.T) {
	raw, err := os.ReadFile("../../image/entrypoint.sh")
	if err != nil {
		t.Fatal(err)
	}
	src := string(raw)

	clear := "rm -f /run/crew/ready /run/crew/harness-done"
	clearAt := strings.Index(src, clear)
	if clearAt < 0 {
		t.Fatalf("entrypoint does not clear both markers with %q", clear)
	}
	if first := strings.Index(src, "touch /run/crew/"); first >= 0 && first < clearAt {
		t.Errorf("a marker is written (offset %d) before the markers are cleared (offset %d)", first, clearAt)
	}
	if strings.Contains(src, "sh-agent") {
		t.Error("the entrypoint still names sh-agent, which Crew does not use (design 12.1)")
	}

	const copySkel = "cp -a --update=none /crew/home-skel/. "
	copyAt := strings.Index(src, copySkel)
	if copyAt < 0 {
		t.Fatalf("the entrypoint does not copy the home skeleton with %q, which keeps what the home has", copySkel)
	}
	readyAt := strings.LastIndex(src, "touch /run/crew/ready\n")
	if readyAt < 0 {
		t.Fatal("no `touch /run/crew/ready` in the entrypoint")
	}
	if copyAt > readyAt {
		t.Errorf("the harness is copied (offset %d) after the ready marker (offset %d)", copyAt, readyAt)
	}

	fn := regexp.MustCompile(`(?ms)^harness_finish\(\) \{\n.*?^\}\n`).FindStringIndex(src)
	if fn == nil {
		t.Fatal("harness_finish() not found as a top-level function")
	}
	if !strings.Contains(src[fn[0]:fn[1]], "touch /run/crew/harness-done") {
		t.Error("harness_finish does not write /run/crew/harness-done")
	}
	call := regexp.MustCompile(`(?m)^harness_finish &$`).FindAllStringIndex(src, -1)
	if len(call) != 1 {
		t.Fatalf("`harness_finish &` appears %d times as a statement, want exactly 1", len(call))
	}
	if call[0][0] < readyAt {
		t.Errorf("harness_finish is started (offset %d) before the ready marker is written (offset %d)", call[0][0], readyAt)
	}
}

// A run gets the harness also in a minimal sandbox, which has no Docker inside: CREW_HARNESS opens
// the copy that minimal mode otherwise skips (12.1). Found on dave: a minimal run's agy listed none
// of the harness's skills.
func TestEntrypointGivesAMinimalRunTheHarness(t *testing.T) {
	raw, err := os.ReadFile("../../image/entrypoint.sh")
	if err != nil {
		t.Fatal(err)
	}
	src := string(raw)
	guard := `if { [ -z "${MINIMAL}" ] || [ -n "${CREW_HARNESS:-}" ]; } && [ -d /crew/home-skel ]; then`
	at := strings.Index(src, guard)
	copyAt := strings.Index(src, "cp -a --update=none /crew/home-skel/. ")
	if at < 0 || copyAt < at {
		t.Fatalf("the copy of the home skeleton is not guarded by %q", guard)
	}
	minimalReady := strings.Index(src, `if [ -n "${MINIMAL}" ]; then`+"\n    touch /run/crew/ready")
	if minimalReady < 0 || minimalReady < copyAt {
		t.Fatal("a minimal sandbox reports ready before the harness is copied")
	}
}
