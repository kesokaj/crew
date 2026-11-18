package main

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// ProofFile is where crew-run proof writes its event, relative to the home.
const ProofFile = ".crew/proof.jsonl"

// proof writes one event into the home, as a turn writes its spool: install's proof that an event a
// microVM prints is stored (16.11). It names the kernel, which is the microVM's own.
func proof(e env, stdout, stderr io.Writer) int {
	kernel, _ := os.ReadFile("/proc/sys/kernel/osrelease")
	line, _ := json.Marshal(map[string]string{"type": "crew_proof", "at": time.Now().UTC().Format(time.RFC3339Nano),
		"kernel": strings.TrimSpace(string(kernel))})
	if err := writeFile(filepath.Join(e.Home, ProofFile), append(line, '\n'), 0o644); err != nil {
		fmt.Fprintln(stderr, "crew-run proof:", err)
		return 1
	}
	fmt.Fprintf(stdout, "%s\n", line)
	return 0
}
