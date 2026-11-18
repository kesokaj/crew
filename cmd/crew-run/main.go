// Command crew-run runs an agent CLI inside a sandbox (design 12.4): `turn`, `acp-daemon`,
// `acp-turn`, `acp-steer`, `acp-cancel`, `login-check`, and `creds`. It holds no secrets and makes
// no decisions about the board.
//
//	crew-run creds [--quiet]               pull this sandbox's credentials from the broker into the home
//	crew-run login-check                   check the login of this sandbox's CLI (12.6)
//	crew-run turn turn-NNNN                one turn of a run, with the files of its control folder
//	crew-run acp-daemon                    run agy acp --stdio --process-per-session inside a project microVM
//	crew-run acp-turn [turn-NNNN]          one ACP turn inside a project microVM
//	crew-run acp-steer [--run ID] [TEXT]   deliver a mid-turn steer to an active ACP session
//	crew-run acp-cancel [--run ID]         cancel and close an active ACP session
//	crew-run proof                         write one event into the home, for install's proof (16.11)
//	crew-run version
package main

import (
	"fmt"
	"io"
	"os"

	"github.com/kesokaj/crew/internal/version"
)

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}

func run(args []string, stdout, stderr io.Writer) int {
	verb := ""
	if len(args) > 0 {
		verb = args[0]
	}
	switch verb {
	case "version":
		fmt.Fprintln(stdout, version.Version)
		return 0
	case "creds":
		return creds(sandboxEnv(), args[1:], stdout, stderr)
	case "login-check":
		return loginCheck(sandboxEnv(), args[1:], stdout, stderr)
	case "turn":
		return turn(sandboxEnv(), args[1:], stdout, stderr)
	case "acp-daemon":
		return acpDaemon(sandboxEnv(), args[1:], stdout, stderr)
	case "acp-turn":
		return acpTurn(sandboxEnv(), args[1:], stdout, stderr)
	case "acp-steer":
		return acpSteer(sandboxEnv(), args[1:], stdout, stderr)
	case "acp-cancel":
		return acpCancel(sandboxEnv(), args[1:], stdout, stderr)
	case "proof":
		return proof(sandboxEnv(), stdout, stderr)
	}
	fmt.Fprintln(stderr, "usage: crew-run version | creds | login-check | turn | acp-daemon | acp-turn | acp-steer | acp-cancel | proof")
	return 2
}

// env is what crew-run reads from its sandbox: where the home is and where the broker answers.
type env struct {
	Home   string
	Broker string
	// Workspace is where a run's clone is; "" is runspec.Workspace. Tests give another.
	Workspace string
}

func sandboxEnv() env {
	home := os.Getenv("HOME")
	if home == "" || home == "/" {
		home = "/home/agent"
	}
	return env{Home: home, Broker: os.Getenv("CREW_CRED_BROKER")}
}
