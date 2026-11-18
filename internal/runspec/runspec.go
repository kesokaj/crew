// Package runspec is the whole interface between the backend and crew-run (design 12.4): the names
// in a run's control folder, ~/.crew/, and what its files hold. The backend writes run.json, the
// token, and each turn's prompt before the turn; crew-run writes the spool, standard error, the
// heartbeat, and exit.json, last.
package runspec

import (
	"fmt"
	"regexp"
	"time"
)

// The control folder and its files, relative to the home.
const (
	Dir       = ".crew"
	RunFile   = ".crew/run.json"
	TokenFile = ".crew/token"
	Heartbeat = ".crew/heartbeat"
	// SetupDone says the project's setup ran in this sandbox: it runs once, before the first turn.
	SetupDone = ".crew/setup.done"
	// StopFile asks the turn that runs to stop. The backend writes it into the home on the machine,
	// with the reason; crew-run then interrupts the CLI and keeps the work in progress.
	StopFile = ".crew/stop"
)

// The files of one turn, in its folder.
const (
	PromptFile = "prompt.txt"
	SteerFile  = "steer.txt"
	EventsFile = "events.jsonl"
	StderrFile = "stderr.log"
	ExitFile   = "exit.json"
	// SetupLog is what the project's setup printed, in the folder of the turn it ran before.
	SetupLog = "setup.log"
)

// Workspace is where a run's clone is in the sandbox. It is the same in every sandbox, because
// claude keeps a conversation in a folder named after it (spike item 69).
const Workspace = "/home/agent/work"

// WorktreesDir and RepoMirror are the per-session git worktree root and bare repository mirror
// inside a shared project microVM (design 12.2, 12.4, 13.1, since 1.37).
const (
	WorktreesDir = "/home/agent/worktrees"
	RepoMirror   = "/home/agent/repo.git"
)

// WorkspaceFailed begins the detail of a turn whose workspace crew-run could not make: the run ended
// before its agent started, for Crew's reason. An eval counts it as measuring nothing by its step,
// which the run keeps in end_step, never by this beginning (9.6, since 1.01).
const WorkspaceFailed = "the workspace: "

// HeartbeatEvery is how often crew-run touches the heartbeat while a turn runs.
const HeartbeatEvery = 10 * time.Second

// TurnDir is the folder of turn n, relative to the home.
func TurnDir(n int) string { return fmt.Sprintf("%s/turn-%04d", Dir, n) }

// CleanRunID keeps only safe identifier bytes (`[a-zA-Z0-9_-]`) so a run ID can never traverse out
// of `.crew/runs/` or `/home/agent/worktrees/`.
func CleanRunID(runID string) string {
	var b []byte
	for i := 0; i < len(runID); i++ {
		c := runID[i]
		if (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9') || c == '-' || c == '_' {
			b = append(b, c)
		}
	}
	return string(b)
}

// RunControlDir returns the per-run control directory inside a shared project microVM (`~/.crew/runs/<runID>`),
// or `.crew` when `runID` is empty.
func RunControlDir(runID string) string {
	clean := CleanRunID(runID)
	if clean == "" {
		return Dir
	}
	return fmt.Sprintf("%s/runs/%s", Dir, clean)
}

// RunTurnDir returns the folder of turn n for a specific `runID` inside a shared project microVM.
func RunTurnDir(runID string, n int) string {
	return fmt.Sprintf("%s/turn-%04d", RunControlDir(runID), n)
}

var turnDirName = regexp.MustCompile(`^turn-[0-9]{4}$`)

// ValidTurnDir says name is the name of a turn's folder.
func ValidTurnDir(name string) bool { return turnDirName.MatchString(name) }

// Run is run.json: what the backend tells crew-run about the run.
type Run struct {
	Run     string `json:"run"`
	Subtask int64  `json:"subtask"`
	Project string `json:"project,omitempty"`
	CLI     string `json:"cli"`
	Model   string `json:"model"`
	Effort  string `json:"effort,omitempty"`
	// Conversation is the CLI's own id of the conversation the next turn continues, or "" for a new
	// one, which the CLI names itself.
	Conversation string `json:"conversation,omitempty"`
	Tools        bool   `json:"tools"`
	Limits       Limits `json:"limits"`
	Git          Git    `json:"git"`
	// NoRepoConfig asks for a workspace without the files the CLI loads from a repository by itself
	// (13.8), for a run that does not build.
	NoRepoConfig bool `json:"noRepoConfig,omitempty"`
	// MainInstructions asks for main's copies of the files the CLI reads as instructions before every
	// turn, in place of the branch's (12.9, 13.8): every run but a builder whose ticket may change them.
	MainInstructions bool `json:"mainInstructions,omitempty"`
	// MCP is the path of Crew's MCP server on the sandbox's door, such as /mcp, or "" for a run that
	// has none (11.5). crew-run checks that it answers before the CLI starts.
	MCP string `json:"mcp,omitempty"`
	// Setup is the project's setup command, which crew-run runs in the workspace before the CLI starts,
	// once in a sandbox, for a role that builds or runs the project's code (11.2, since 0.47); "" is
	// none. A setup that fails ends the run with the reason setup, before the CLI starts.
	Setup string `json:"setup,omitempty"`
	// SetupTimeoutSeconds bounds the setup; 0 is 30 minutes.
	SetupTimeoutSeconds int `json:"setupTimeoutSeconds,omitempty"`
	// SetupMayFail lets the CLI start when the setup fails: the builder of the ticket that fixes it
	// (13.5). What the setup printed is in the turn's setup.log.
	SetupMayFail bool `json:"setupMayFail,omitempty"`
}

// Limits are the run's limits that crew-run applies.
type Limits struct {
	TimeoutSeconds int `json:"timeoutSeconds"`
	MaxTurns       int `json:"maxTurns"`
}

// Git says where the run's work comes from and goes to; empty for a run with no repository, which
// works in an empty folder. Path is on the sandbox's door, the address of the broker
// (CREW_CRED_BROKER).
type Git struct {
	Path   string `json:"path"`   // /git/<repo>.git
	Branch string `json:"branch"` // the branch the run works on, refs/heads/ left out
	// Base is the ref a branch that does not exist yet starts from, or the work in progress of the
	// run this one continues, which it starts from.
	Base string `json:"base"`
	WIP  string `json:"wip"` // refs/crew/wip/<run>: where crew-run keeps the work in progress
	// OwnOnly says the run reads its branch and nothing else: no origin/main. A run of an eval may not
	// read main, which holds the work it replays (9.6).
	OwnOnly bool `json:"ownOnly,omitempty"`
	// InstructionsAt is the commit of the branch's history whose instructions a run that reads no main
	// takes in place of main's: a run of an eval that judges takes its case's base commit's (9.6, 13.8).
	InstructionsAt string `json:"instructionsAt,omitempty"`
	// Refresh fetches Branch and Also again before every turn and resets the read-only workspace to
	// Branch, or to Commit when it is set, before the files the CLI loads by itself are kept out and
	// main's instructions are taken: a ticket's chat (11.9, 13.8).
	Refresh bool     `json:"refresh,omitempty"`
	Commit  string   `json:"commit,omitempty"`
	Also    []string `json:"also,omitempty"`
}

// Exit is exit.json, the last file of a turn.
type Exit struct {
	Exit         int    `json:"exit"`
	Reason       string `json:"reason"`
	Detail       string `json:"detail"`
	Conversation string `json:"conversation,omitempty"`
	// WIP is the commit crew-run pushed to the run's wip ref, or "" when there was nothing to keep.
	WIP string `json:"wip,omitempty"`
	// Step names an end of crew-run's own (steps.go): the server writes its sentence and takes no word
	// of Detail for it.
	Step string `json:"step,omitempty"`
	// WIPNotKept says crew-run could not keep the work in progress; the server adds the note itself.
	WIPNotKept bool `json:"wipNotKept,omitempty"`
	// Error is crew-run's own error, at Step or of keeping the work in progress. The server logs it
	// with the run and stores it nowhere else (13.9).
	Error string `json:"error,omitempty"`
}
