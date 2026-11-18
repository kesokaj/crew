// Package harness is the backend's one interface to the agent CLIs (design 12.5): it starts a run in
// a microVM, gives it more turns, stops it, and turns what its CLI prints into run events. The same
// interface serves every CLI; what differs is in the CLI's adapter. Kata is the implementation with
// microVMs, and Fake replays recorded output, so the driver, the monitor, and the UI are tested
// without a model or a microVM.
package harness

import (
	"context"
	"errors"
	"time"

	"github.com/kesokaj/crew/internal/cli"
	"github.com/kesokaj/crew/internal/orchestrator"
	"github.com/kesokaj/crew/internal/runspec"
)

// Harness is the interface of 12.5. Interrupt, Resume, and Alive are the parts 12.4 adds: a stop
// that keeps the work in progress, following a run again after a restart, and whether its microVM
// still runs.
type Harness interface {
	// StartRun starts a microVM and the first turn of the CLI.
	StartRun(ctx context.Context, spec RunSpec) (RunHandle, error)
	// Continue starts another turn in the same conversation and the same microVM, after the last
	// turn ended (12.10, the first way). conversation is the CLI's id of it.
	Continue(ctx context.Context, runID, conversation, text string) error
	// Interrupt asks the turn that runs to stop for reason; the turn then ends, with its work kept.
	Interrupt(ctx context.Context, runID, reason string) error
	// Stop ends the run and removes its microVM with its home.
	Stop(ctx context.Context, runID string) error
	// Events is the run's events, in order; the last event of each turn has End set.
	Events(runID string) <-chan RunEvent
	// Exec runs a command in the run's microVM.
	Exec(ctx context.Context, runID string, cmd []string) (ExecResult, error)
	// ExecFresh runs a check in a new microVM with no agent and no run token: the check sandbox
	// (13.9). An error is the sandbox's own trouble; what the commands did is in the results, and a
	// setup that failed with no command is a result of its own (since 1.11).
	ExecFresh(ctx context.Context, spec CheckSpec) ([]CheckResult, error)
	// Resume follows a run again after a restart of the server, from where its events were stored.
	Resume(ctx context.Context, r Resumed) error
	// Alive reports whether the run's microVM still runs.
	Alive(ctx context.Context, runID string) bool
}

// ConversationFolder is a conversation's work folder in the home, /home/agent/conversation, beside the
// workspace (11.8, plan M20 D6): saved after every turn and put back in a new sandbox.
const ConversationFolder = "conversation"

// Limits are a run's own limits.
type Limits struct {
	Timeout  time.Duration
	MaxTurns int
}

// RunSpec is what a run is started with (12.5).
type RunSpec struct {
	Run     string // the run's id
	Subtask int64
	Project string
	Repo    string // the repository of the git endpoint, or "" for a run with none, such as a plan draft's
	Branch  string // the run's branch, refs/heads/ left out
	// Base is where a branch that does not exist yet starts, or the work in progress of the run this
	// one continues.
	Base   string
	CLI    string
	Model  string
	Effort string
	Prompt string
	Tools  bool
	Limits Limits
	// Continue is the CLI's id of the conversation the run continues, with Files, the CLI's own files
	// of it, put back into the home (12.10, the second way); "" for a new conversation, which the CLI
	// names itself.
	Continue string
	Files    map[string][]byte
	Repos    []string // what its temporary GitHub token may read
	// MCP is the path of Crew's MCP server on the sandbox's door, or "" for a run without it (11.5).
	MCP string
	// NoRepoConfig keeps the repository's own CLI configuration out of the workspace (13.8).
	NoRepoConfig bool
	// MainInstructions gives the run main's skills and context before every turn (12.9, 13.8).
	MainInstructions bool
	// Minimal starts no Docker inside the microVM, for a run that needs none.
	Minimal bool
	// Sizing is what its microVM gets beyond Kata's base, by its role's size (13.10); zero is the base.
	Sizing orchestrator.Sizing
	// Setup is the project's setup command, run before the CLI in a new sandbox of a role that builds
	// or runs code (11.2, since 0.47); SetupTimeout bounds it; SetupMayFail lets the CLI start when it
	// fails, for the builder of the ticket that fixes it.
	Setup        string
	SetupTimeout time.Duration
	SetupMayFail bool
	// Mode is the network mode of its sandbox (13.6); a judge's is Model only whatever it says.
	Mode string
	// ReadOnly says the run pushes nothing, not even its work in progress (6.1): a run that analyses
	// or judges.
	ReadOnly bool
	// Image is the runtime image the run's sandbox starts from, by its id, or "" for the running
	// release's: an eval's run of a release's suite names the candidate's (16.2 step 5, since 0.52).
	Image string
	// Judge starts the run in a judge sandbox (13.3): the login of its CLI and nothing else, no
	// repository, no Crew MCP, and no harness in its home, for one call with no tools (8.4, spike
	// item 79).
	Judge bool
	// Improver starts the run in the improver's sandbox (10.4): Model only whatever is asked, no
	// repository, and the login of its CLI; it reaches the Crew MCP with a token of the Crew level.
	Improver bool
	// OwnBranchOnly says the run's workspace reads its branch and not main: a run of an eval (9.6).
	OwnBranchOnly bool
	// InstructionsAt is the commit a run of an eval that judges takes its instructions from: its case's
	// base commit, since main holds the work it replays (9.6, 13.8).
	InstructionsAt string
	// WithoutLogin gives the sandbox no login of its CLI: how a run that stops for a sign-in is shown
	// without touching the stored login.
	WithoutLogin bool
	// Conversation starts the run in a sandbox of the kind conversation, the operator's conversation
	// (11.8), and Chat in one of the kind chat, a ticket's chat (11.9): what each holds is its kind's
	// (12.6).
	Conversation, Chat bool
	// KeepFolder is the folder of the home that is saved after each turn and put back in a new
	// sandbox, a conversation's work folder (11.8, plan M20 D6); "" is none. Folder is the gzip tar
	// of it to put back, as it was stored.
	KeepFolder string
	Folder     []byte
	// Refresh fetches the branch again and resets the read-only workspace to it before every turn, or
	// to Commit when it is set; Also are more branches to fetch, read-only: a chat's (11.9, D7).
	Refresh bool
	Commit  string
	Also    []string
	// OnSandbox runs when the run's sandbox exists and before its microVM starts, with the sandbox's
	// id and the hash of its token, which is the run's token (12.4).
	OnSandbox func(sandbox, tokenHash string) error
}

// RunHandle names a started run.
type RunHandle struct {
	Run     string
	Sandbox string
}

// RunEvent is one event of a run (12.5): one line the CLI printed, normalized by its adapter, or
// an event the harness made itself, such as the end of a turn.
type RunEvent struct {
	Run  string
	Turn int
	At   time.Time
	cli.Event
	// Raw is the line as the CLI printed it, before secrets are removed; nil for an event the harness
	// made itself.
	Raw []byte
	// Offset is where the turn's spool goes on after this line; 0 for an event that is not a line.
	Offset int64
	// End is set on the last event of a turn.
	End *TurnEnd
}

// TurnEnd is how a turn ended.
type TurnEnd struct {
	runspec.Exit
	// Result is the turn's result: the last result line before the CLI ended (12.4).
	Result []byte
	// Files are the CLI's own files of the conversation after the turn, by their path in the home.
	Files map[string][]byte
	// ByHarness says the harness ended the turn itself, such as for a microVM that died, and the
	// reason is its own, not one read from the sandbox's home.
	ByHarness bool
	// FolderPath is a file on the server with the gzip tar of the run's KeepFolder after the turn,
	// beside the sandbox's home, which the driver reads and removes; FolderNote says why it was not
	// saved, or what was left out of it (D17).
	FolderPath, FolderNote string
}

// Resumed is a run to follow again after a restart.
type Resumed struct {
	Run          string
	Sandbox      string
	CLI          string
	Turn         int
	Offset       int64
	Conversation string
	// TurnDone says the turn ended and its end was stored: the run waits for its next turn, and the
	// turn's end is not given again.
	TurnDone bool
	// Spec is the run's run.json as the backend knows it, for the turns that follow.
	Spec runspec.Run
	// KeepFolder is the run's KeepFolder, saved after each of its turns.
	KeepFolder string
}

// ExecResult is what a command printed and its exit code.
type ExecResult struct {
	Code   int
	Output string
}

// CheckSpec is one run of a check sandbox (13.3, 13.9): the tree it checks, the project's setup
// command, and the commands, each run in the tree as the sandbox's user with its own time limit.
type CheckSpec struct {
	Project string
	// Mode is the network mode of the check sandbox: its project's, or its ticket's (13.6), since
	// checks install packages.
	Mode string
	// Repos are the GitHub repositories its read-only token may read (12.6).
	Repos []string
	// Tree is the code to check, as a tar archive.
	Tree []byte
	// HeldOut is the tar archive of an eval case's held-out paths, written into the sandbox only
	// after Setup ran on Tree and the sandbox's route out was cut (9.6, 13.9).
	HeldOut []byte
	// Cut disconnects the check sandbox from its bridge and drops its door after Setup and before
	// HeldOut is placed and Cmds run (9.6, 13.6).
	Cut bool
	// Setup is the project's setup command, run first; "" is none.
	Setup string
	Cmds  []CheckCmd
	// Timeout bounds each command; a command that runs longer is an error, not a failure (13.9).
	Timeout time.Duration
	// Sizing is what the check sandbox gets beyond Kata's base: a large sandbox's (13.10).
	Sizing orchestrator.Sizing
}

// SetupError begins the error of a check command whose sandbox's project setup failed before it ran,
// and of the setup's own result in a check with no command (SetupCheck).
const SetupError = "the project's setup"

// CheckCmd is one command of a check run: its name, as the ticket or the project names it, and the
// shell command.
type CheckCmd struct {
	Name string
	Run  string
}

// CheckResult is what one command of a check run did. Error is set when the sandbox could not tell:
// the setup failed, or the command ran past its time.
type CheckResult struct {
	Name   string `json:"name"`
	Code   int    `json:"code"`
	Passed bool   `json:"passed"`
	Error  string `json:"error,omitempty"`
	Output string `json:"output,omitempty"`
}

// MaxTree is the largest tree a check sandbox takes.
const MaxTree = 512 << 20

// ErrNoRun is returned for a run the harness does not follow.
var ErrNoRun = errors.New("the harness does not follow this run")

// ErrBusy is returned by Continue while a turn of the run is still running.
var ErrBusy = errors.New("a turn of this run is still running")
