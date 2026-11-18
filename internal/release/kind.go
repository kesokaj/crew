package release

import (
	"sort"
	"strings"
)

// The kinds of release (16.3).
const (
	// KindText changes only text for agents: it installs by itself after its notice.
	KindText = "text"
	// KindCode changes anything else: it waits for Install.
	KindCode = "code"
	// KindSetup needs a newer machine setup: it waits, and Install stops at its first step with the
	// one command to run on the machine.
	KindSetup = "setup"
)

// Protected is the list of protected parts (16.8), as paths of the repository. The running version
// decides from it; a release that changes the list is itself protected, since the list is in
// internal/release.
var Protected = []string{
	// The updater, the notice and hold step, the roll back path, the code that decides the kind,
	// the gate and the build with their scripts and their sandboxes, the code that chooses the image a
	// run starts from, and crew-gate's settings (since 0.59: review of M16, F3); a continued run's
	// image is chosen as a started run's (since 0.62: security review of M16's close).
	"cmd/crew-updater/", "internal/updater/", "internal/release/", "internal/versions/", "deploy/", "tools/", "Makefile",
	"internal/harness/", "internal/runs/driver.go", "internal/runs/continue.go", "internal/board/crewproject.go",
	// The press of Run the live gate and the diff it shows, which give a candidate's image the login
	// (since 0.59: security review of ef7a82a..4a601e2).
	"internal/httpapi/versions.go", "web/src/lib/gate.ts", "web/src/components/gate.tsx",
	// The credential broker and store, the git endpoint, the MCP tokens, the ingress and its
	// sign-in, sessions, the network mode and the credentials a sandbox gets, and the removal of
	// secrets.
	"internal/credbroker/", "internal/credstore/", "internal/gitendpoint/", "internal/mcp/tokens.go", "internal/mcp/mcp.go",
	"ingress/", "internal/httpapi/server.go", "internal/egress/", "internal/sandboxes/",
	"internal/redact/", "internal/fence/", "internal/runs/roles.go",
	// The code that asks GitHub for a sandbox's token and limits it to reading the repositories it is
	// given, and the code that gives the project terminal's (safety rule 7; since 0.98: security review
	// of the release of 0.3.11, finding 1, and of its fix); a run's are chosen in roles.go and harness.
	"internal/githubapp/", "internal/projterm/",
	// The code that holds the operator's logins or hands them on: the server's own use of GitHub,
	// Setup, which makes them and checks the App, and the test of a login that comes back from a run
	// (safety rules 6, 8 and 9; since 0.98, the same review of its fix), with the adapter of agy whole,
	// since its account check is in google.go and its registry in cli.go (the review of that fix).
	"internal/github/", "internal/setup/", "internal/logins/", "internal/cli/",
	// The mail account that sends notifications, which notify reads and mail sends with, and the rule
	// for the paths in a sandbox's home that a login goes through (since 0.98: the security review of
	// 0617580, outside its round).
	"internal/notify/", "internal/mail/", "internal/homepath/",
	// The secrets of hosted projects, which hosting stores and hands to a hosted project's container:
	// the whole package, so no split leaves it (since 0.98, the same review).
	"internal/hosting/",
	// Where every sandbox is started, kept apart, run in and removed, and the microVM runtimes an
	// install may name: the whole package since 0.82, where it was sandbox.go alone, so no split or
	// helper leaves it (security review of the release of 0.3.4, finding 1, its extension, and the
	// review of its fixes); and since 0.83 the whole of internal/config, where it was config.go alone,
	// with the check that the server's network forwards nothing (extension 2, findings 1 and 2).
	"internal/orchestrator/", "internal/config/",
	// The server's command, whole since 0.87: its wiring of the vault, the broker, the proxy's check of
	// Sources, the sandboxes' door and what is removed of a sandbox's secrets (machine.go, since 0.62:
	// security review of M16's close), and of the ingress's door and the orchestrator it asks (doors.go
	// and serve.go, since 0.85); whole, since any file of it runs in the same process before serve and
	// could set what the door reads (security review of 15e9dc4..ae44ea3, finding 2).
	"cmd/crew/",
	// The database container with its roles, and the schema steps and the code that applies them and
	// grants on them, which change stored data whatever a step's header says older versions read (design
	// 0.58; security review of 002e3a3..ef7a82a).
	"deploy/db/", "internal/db/",
	// The settings keys for approvals, network mode, stages (and, until 0.79 retired them, the
	// credentials a sandbox got), the agents' model and effort, and agents.auto; and the code that
	// chooses a model with the numbers it chooses by, which no release changes (safety rule 15; since
	// 0.59).
	"internal/settings/", "internal/models/",
	// What Crew depends on.
	"go.mod", "go.sum", "ingress/go.mod", "ingress/go.sum", "web/package.json", "web/package-lock.json",
	"versions.lock", "image/",
	// The improver's own definition and the steps from a proposal to the baseline (10.4, 10.5).
	"improver.md", "improver.go", "internal/improve/",
	// The eval's reading of which runs measured their case, the eval's flow that acts on it, and the
	// comparison that decides an override, an improvement and a release's evals by them (since 1.02:
	// the security review of 1.01, finding 6, and the operator's popup answers of session 69, "Protect
	// both, reading moved" and, after its review, "Protect evals.go too").
	"internal/pipeline/evalend.go", "internal/pipeline/evals.go", "internal/measure/",
	// How a ticket's check, a subtask's next choice at a limit, and a fit check's judge keep off the
	// builder's family, which internal/models chooses by (safety rule 15; since 1.14: the security
	// review of 1.13's change, second round, finding 3, and the operator's popup answer of session 81,
	// "New checkfamily.go + atlimit.go").
	"internal/pipeline/checkfamily.go", "internal/pipeline/atlimit.go",
	// The guard of the same rule that every run passes, at its start and at Crew's move of it at a limit,
	// with the reads of the builder's model it keeps off, which roles.go and continue.go call (since
	// 1.15: the security review of 1.14's change, finding 1, and the operator's popup answer of session
	// 82, "runs/checkguard.go").
	"internal/runs/checkguard.go",
	// The bound of a tree a check lists, which the git endpoint's bound of a push stands beside (since
	// 1.03: the security review of the tree's bound, and the operator's popup answer of session 70,
	// "Protect listtree.go").
	"internal/gitstore/listtree.go",
	// The bound of every other read of git's output by the server, with the reads themselves, so no
	// unprotected file reads around it (since 1.04: the security review of the tree's bound, round one,
	// finding 3, and the operator's popup answer of session 71, "Protect it, move output in").
	"internal/gitstore/readbound.go",
	// The cuts of a history's fields and what one read keeps of them, which stream in readbound.go
	// hands git's output to (since 1.09: the review of 1.09's change, and the operator's popup answer
	// of session 76, "Protect history.go").
	"internal/gitstore/history.go",
	// The store's reading of an index file of the agent CLI through the links on its way, and since 1.06
	// of the links of a tree that stand for a file or a folder that instructs or is code, which the
	// guards on code and instructions read by (since 1.05: the security review of 1.05's change, and
	// the operator's popup answer of session 72, "Protect indexfile.go").
	"internal/gitstore/indexfile.go",
}

// Kind is what a release changes, compared with the running one.
type Kind struct {
	Kind string `json:"kind"`
	// Changed is every path that was added, changed, or removed.
	Changed []string `json:"changed"`
	// Protected is the changed paths on the list, the agents whose model or effort changed, and a
	// schema the running version cannot read.
	Protected []string `json:"protected"`
}

// TextFile reports whether a path is text for agents (16.3): Markdown under baseline/ or harness/,
// and not in a folder of hooks.
func TextFile(path string) bool {
	if !strings.HasSuffix(path, ".md") || strings.Contains(path, "/hooks/") {
		return false
	}
	return strings.HasPrefix(path, "baseline/") || strings.HasPrefix(path, "harness/")
}

func protectedPath(path string) bool {
	for _, p := range Protected {
		if strings.HasPrefix(path, p) {
			return true
		}
	}
	return false
}

// Classify says which kind next is, against running, on a machine whose setup is machineSetup. It is
// decided by the running version, from the paths the release changes, never from what the release
// says about itself.
func Classify(running, next Manifest, machineSetup int) Kind {
	k := Kind{Kind: KindText, Changed: []string{}, Protected: []string{}}
	seen := map[string]bool{}
	for p, h := range next.Files {
		if running.Files[p] != h {
			k.Changed = append(k.Changed, p)
			seen[p] = true
		}
	}
	for p := range running.Files {
		if _, ok := next.Files[p]; !ok && !seen[p] {
			k.Changed = append(k.Changed, p)
		}
	}
	sort.Strings(k.Changed)
	for _, p := range k.Changed {
		if !TextFile(p) {
			k.Kind = KindCode
		}
		if protectedPath(p) {
			k.Protected = append(k.Protected, p)
		}
	}
	// An agent's model or effort is a setting, not text.
	var agents []string
	for p := range unionKeys(running.Agents, next.Agents) {
		if running.Agents[p] != next.Agents[p] {
			agents = append(agents, p)
		}
	}
	sort.Strings(agents)
	for _, p := range agents {
		k.Kind = KindCode
		k.Protected = append(k.Protected, p+": the model or the effort")
	}
	if next.Schema != running.Schema {
		k.Kind = KindCode
	}
	if next.ReadBy > running.Schema {
		k.Protected = append(k.Protected, "a schema the running version cannot read")
	}
	if len(next.Images) != 0 && len(running.Images) != 0 && (next.Images["postgres"].Tag != running.Images["postgres"].Tag || next.Images["postgres"].ID != running.Images["postgres"].ID) {
		k.Kind = KindCode
	}
	if next.MachineSetup > machineSetup {
		k.Kind = KindSetup
	}
	return k
}

func unionKeys(a, b map[string]string) map[string]bool {
	u := map[string]bool{}
	for k := range a {
		u[k] = true
	}
	for k := range b {
		u[k] = true
	}
	return u
}

// InstallsBySelf reports whether a release of kind k installs by itself after its notice, under the
// setting updates.auto_install (16.3): text, unprotected, or never.
func InstallsBySelf(k Kind, autoInstall string) bool {
	switch autoInstall {
	case "text":
		return k.Kind == KindText
	case "unprotected":
		return k.Kind != KindSetup && len(k.Protected) == 0
	}
	return false
}
