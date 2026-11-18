// Package cli holds what Crew knows of the agent CLI it runs: Antigravity's agy, in agy.go, the only
// one since 0.42 (design 12.2, AGENTS.md "Agent CLIs"). Nothing outside this package branches on the
// name of a CLI; code that needs to know something about it asks its adapter.
package cli

import (
	"context"
	"errors"
	"fmt"
	"path"
	"slices"
	"strings"
	"time"

	"github.com/kesokaj/crew/internal/runspec"
)

// Runner runs a command in the sandbox and returns what it printed and its exit code. err is set
// only when the command could not be run at all.
type Runner func(ctx context.Context, name string, args ...string) (stdout, stderr []byte, exit int, err error)

// The states of a login check.
const (
	LoginOK      = "ok"      // the CLI is signed in and its login works
	LoginRefused = "refused" // the login is missing or the vendor refused it
	LoginError   = "error"   // the check could not tell: a fault that is not the login's
)

// LoginResult is what a login check found (12.6).
type LoginResult struct {
	CLI    string `json:"cli"`
	State  string `json:"state"`
	Detail string `json:"detail"`
	// Real says the check made a real request, which also tells a refused login from a present one.
	Real    bool    `json:"real"`
	Models  []Model `json:"models,omitempty"`
	Credits string  `json:"credits,omitempty"`
	// ModelsError says why the list of models could not be read, when the login works (12.3).
	ModelsError string `json:"modelsError,omitempty"`
	// Limits is what the CLI said about its limits (12.7).
	Limits []Limit `json:"limits,omitempty"`
}

// Adapter is one CLI.
type Adapter interface {
	Name() string
	// LoginCheck checks the login in the sandbox it runs in. real makes the one real request the
	// scheduled check makes (12.6).
	LoginCheck(ctx context.Context, run Runner, real bool) LoginResult
	// LoginFile is where the CLI keeps the login it renews itself, relative to the home.
	LoginFile() string
	// RealEvery is how often the scheduled check makes its real request: 0 is every check.
	RealEvery() time.Duration
	// PrepareHome writes what the CLI needs in a home before it first runs, once its login is there.
	PrepareHome(home string) error
	// CheckLogin refuses what cannot be this CLI's login, before it is stored.
	CheckLogin(login []byte) error
	// Account returns whose a login is, asked of the vendor and never read from the login's own
	// claims, or "" when the vendor offers no way to ask (12.6, spike item 56).
	Account(ctx context.Context, login []byte) (string, error)
	// Renewed takes from a login that came back from a sandbox only what a renewal changes, into
	// the stored login, and returns the result to store. A sandbox may write anything in its home,
	// so nothing else of it is kept (12.6). A CLI whose login is never renewed refuses.
	Renewed(stored, offered []byte) ([]byte, error)

	// PrepareRun writes what a run needs in its home beside the login: the CLI's settings for full
	// access inside the sandbox, or for none when the call has no tools (12.2, 12.4), and Crew's MCP
	// server with the run's token when mcp is set (11.5).
	PrepareRun(home, workspace string, tools bool, mcp *MCP) error
	// TurnCommand is how one headless turn is started (12.2, 12.4).
	TurnCommand(t Turn) Command
	// Parse reads one line the CLI printed into one run event (12.5).
	Parse(line []byte) Event
	// Outcome reads why a turn ended from its last result, standard error, and exit code (12.4).
	Outcome(o TurnOutput) Outcome
	// Usage reads a result line (12.10).
	Usage(result []byte) Usage
	// WorkspaceInstructions are the files of a workspace that the CLI reads as instructions: a run that
	// judges gets main's, never the branch's it judges (13.8). A name with no slash matches the file of
	// that name in any folder; one with a slash is that path, or a folder and all in it.
	WorkspaceInstructions() []string
	// CodeFiles are the files of a repository that are code for this CLI, not text: hooks, settings,
	// MCP servers, and skills and agents that can declare hooks (13.8). A ticket that changes one does
	// not merge by itself. Matched as WorkspaceInstructions.
	CodeFiles() []string
	// IndexFiles are the files of a workspace that name more folders for the CLI to read, by whether
	// what they name is code (true) or instructions (false): agy's .agents/plugins.json and the like
	// (13.8). Every workspace-relative folder one names counts as code or instructions too.
	IndexFiles() map[string]bool
	// WorkspaceConfig are the files of a workspace that the CLI loads by itself, whatever its own
	// settings say, such as hooks and MCP servers (13.8): a run that does not build gets a workspace
	// without them.
	WorkspaceConfig() []string
	// ReadModels reads the models the signed-in account can use, with no model request (12.3).
	ReadModels(ctx context.Context, run Runner, ask Asker) ([]Model, error)
	// Named reads a name the operator added beside the CLI's list (12.3).
	Named(name string) Model
	// ShortList is what the adapter falls back to when its CLI's list was never read (12.3).
	ShortList() []Model
	// LimitGroup is the group of models whose limits a model shares (12.7).
	LimitGroup(model string) string
	// ModelLimit is the model that has a limit of its own inside its group, or "" (12.7).
	ModelLimit(model string) string
	// Reached is the group, or model:<name>, that a run on model marks when it ended at a limit with
	// detail (12.7).
	Reached(model, detail string) string
	// GroupName is how the vendor names a group of models.
	GroupName(group string) string
	// ReadLimits reads what is left of each limit with no model request, or nil when the CLI only
	// says so while it works (12.7).
	ReadLimits(ctx context.Context, run Runner) ([]Limit, error)

	// ConversationFiles are the CLI's own files of a conversation, relative to the home: what a new
	// sandbox needs to continue it (12.10). Empty when id cannot be one of the CLI's ids.
	ConversationFiles(id, workspace string) []string
	// ForbiddenEnv reports whether an environment variable name is forbidden by this CLI adapter.
	ForbiddenEnv(name string) bool
}

// MCP is Crew's MCP server as a run reaches it: its address on the sandbox's door, and the run's
// token, sent as a bearer (11.5, spike items 10 and 71).
type MCP struct {
	URL   string
	Token string
}

// MCPServer is the name Crew's server has in a CLI's configuration.
const MCPServer = "crew"

// The stored login of the CLI is the credential of Setup with the CLI's own name (credstore.Agy).

var adapters = []Adapter{Agy{}}

// Get returns the adapter of a CLI.
func Get(name string) (Adapter, bool) {
	i := slices.IndexFunc(adapters, func(a Adapter) bool { return a.Name() == name })
	if i < 0 {
		return nil, false
	}
	return adapters[i], true
}

// Names lists the CLIs Crew runs.
func Names() []string {
	out := make([]string, len(adapters))
	for i, a := range adapters {
		out[i] = a.Name()
	}
	return out
}

func lastLine(b []byte) string {
	lines := splitLines(string(b))
	if len(lines) == 0 {
		return ""
	}
	return lines[len(lines)-1]
}

// SafeID says an id from a CLI's output is one a CLI gives: letters, digits, dashes, and
// underscores, beginning with a letter or a digit. Such an id, which a sandbox printed, never names
// another path in a file name, and never reads as a flag on the next turn's command line.
func SafeID(id string) bool {
	if id == "" || len(id) > 128 || id[0] == '-' || id[0] == '_' {
		return false
	}
	for _, r := range id {
		if !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '-' || r == '_') {
			return false
		}
	}
	return true
}

// Matches says path is one of the patterns of WorkspaceInstructions or CodeFiles, below one, or stands
// in front of one (Above); every path matches when an index file names the whole workspace (AllPaths).
func Matches(path string, patterns []string) bool {
	return slices.Contains(patterns, AllPaths) || Below(path, patterns) || Above(path, patterns)
}

// AllPaths is what Named gives for an entry that is the whole workspace: Matches reads it as every
// path, Below as none, so a may_change or a subtask's touches never allow by it (since 1.05).
const AllPaths = "/"

// ErrNamedOutside is an entry of an index file through /proc or /dev, the sandbox's own links, which
// can lead back into the workspace: never read as one that names nothing (since 1.05).
var ErrNamedOutside = errors.New("an index file names a folder through /proc or /dev, which Crew does not follow")

// The most of an index file Crew reads (since 1.07, the operator's popup answer of session 74, "1,024 ×
// 4 KiB; 1 MiB in guards"): at most MaxIndexBytes of it, by its size in the tree before it is read, and
// at most MaxIndexEntries entries and inherits, none longer than MaxEntryBytes, Linux's longest path. The
// folders an index file names are matched against every link and every changed path of a tree, and one
// entry of 1 MiB cost the matching some ten seconds, one of 64 MiB hours.
const (
	MaxIndexBytes   = 1 << 20
	MaxIndexEntries = 1024
	MaxEntryBytes   = 4096

	// Sideloaded project skill and instruction limits (12.9, 13.8): at most 4,096 files, 64 MiB total,
	// and a maximum path depth of 32 components.
	MaxInstructionEntries = 4096
	MaxInstructionBytes   = 64 << 20
	MaxInstructionDepth   = 32
)

// ErrIndexEntries is an index file with more entries than MaxIndexEntries or an entry longer than
// MaxEntryBytes: never read as one that names only some of them.
var ErrIndexEntries = fmt.Errorf("an index file names more than %d folders, or a folder longer than %d bytes, more than Crew reads", MaxIndexEntries, MaxEntryBytes)

// Below says path is one of the patterns, or in a folder one names: a name with no slash at any depth,
// and one that ends with a slash is that folder at the root.
func Below(path string, patterns []string) bool {
	for _, p := range patterns {
		if strings.HasSuffix(p, "/") {
			if path+"/" == p || strings.HasPrefix(path, p) {
				return true
			}
			continue
		}
		if !strings.Contains(p, "/") {
			if path == p || strings.HasSuffix(path, "/"+p) {
				return true
			}
			continue
		}
		if path == p || strings.HasPrefix(path, p+"/") {
			return true
		}
	}
	return false
}

// Above says path is a folder above a pattern: a changed path there can only be a file or a link put in
// that folder's place, such as .agents made a link to a folder elsewhere (review of M6, round 2).
func Above(path string, patterns []string) bool {
	for _, p := range patterns {
		if strings.HasPrefix(p, path+"/") {
			return true
		}
	}
	return false
}

// Named are the workspace-relative folders an index file names (its entries and what it inherits),
// each read from the workspace at runspec.Workspace: a path that leads outside it, by / or ~, the
// agent's home, or by "..", is not counted, and one that comes back into it is (since 1.05, after its
// security review). The whole workspace is AllPaths, and an entry through /proc or /dev is
// ErrNamedOutside (its second round). More entries than MaxIndexEntries, or one longer than
// MaxEntryBytes, is ErrIndexEntries, with every folder it names, which crew-run reads (since 1.07). A
// file that is not JSON Crew reads exactly is ErrIndexParse (indexPaths), and an empty one names
// nothing.
func Named(index []byte) ([]string, error) {
	entries, count, err := indexPaths(index)
	if err != nil {
		return nil, err
	}
	var out []string
	over := count > MaxIndexEntries
	for _, entry := range entries {
		over = over || len(entry) > MaxEntryBytes
		p := entry
		switch {
		case p == "":
			continue
		case p == "~" || strings.HasPrefix(p, "~/"):
			p = path.Dir(runspec.Workspace) + p[1:]
		case strings.HasPrefix(p, "~"):
			continue // another user's home
		case !strings.HasPrefix(p, "/"):
			p = runspec.Workspace + "/" + p
		}
		p = path.Clean(p)
		if p == runspec.Workspace {
			out = append(out, AllPaths)
			continue
		}
		if p == "/proc" || p == "/dev" || strings.HasPrefix(p, "/proc/") || strings.HasPrefix(p, "/dev/") {
			return nil, ErrNamedOutside
		}
		rel, ok := strings.CutPrefix(p, runspec.Workspace+"/")
		if !ok {
			continue // outside the workspace
		}
		out = append(out, rel+"/") // a folder at the root, as Below reads it
	}
	if over {
		return out, ErrIndexEntries
	}
	return out, nil
}

// Indexed are the folders the index files of every CLI name in a tree, by whether they are code: read
// is how a file of the tree is read, nil when it is not there, with its way through links (Follow).
// Every path of a way through a link counts as the index file itself, and an index file that names a
// folder through /proc or /dev is an error (since 1.05). The errors of every index file are joined, so a
// reader sees each kind, whichever file came first (since 1.07).
func Indexed(read func(path string) ([]byte, Way)) (code, instructions []string, err error) {
	for _, n := range Names() {
		a, _ := Get(n)
		for f, isCode := range a.IndexFiles() {
			b, way := read(f)
			names, nerr := Named(b)
			err = errors.Join(err, nerr)
			named := append(way.Paths(), names...)
			instructions = append(instructions, named...)
			if isCode {
				code = append(code, named...)
			}
		}
	}
	return code, instructions, err
}

// InstructionFiles are the files that instruct agents for any CLI (13.8): what each reads as
// instructions, what is code for it, and the repository's GitHub workflows. A ticket changes one only
// when its may_change names it. What the index files of a tree name is added by the caller (Indexed).
func InstructionFiles() []string {
	out := CodeFiles()
	for _, n := range Names() {
		a, _ := Get(n)
		out = append(out, a.WorkspaceInstructions()...)
	}
	return out
}

// CodeFiles are the files that are code for any CLI, and the repository's GitHub workflows (13.8).
func CodeFiles() []string {
	out := []string{".github/workflows", "WORKFLOW.md"}
	for _, n := range Names() {
		a, _ := Get(n)
		out = append(out, a.CodeFiles()...)
	}
	return out
}
