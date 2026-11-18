## 17. What Crew takes from styrman-crew and styrman-harness

I read both repositories at their latest commits: styrman-crew at `5c9e6fb` (2026-10-06) and styrman-harness at `43faca0` (version 2.7.0).

### 17.1 The decision

Crew was started as an independent codebase (originally private under `kesokaj/crew`, published under Apache-2.0 at design version 2.01), with its own server and UI. It adapted eight infrastructure subsystems from `styrman-crew` as static files without prior history (17.2).

- Crew replaced the author's predecessor `styrman-crew` installation on the initial build host (17.6), while its host setup and installer (`deploy/crew-ctl`, `deploy/host-setup.sh`) work cleanly on any fresh supported Debian or Ubuntu host.
- The server is written in Go and the web UI in TypeScript. You know both well.
- The copied parts are renamed from styrman to crew.
- The harness is a folder in Crew. `sh-agent` is not used (12.1).
- The two old repositories are not changed, and their rules do not bind Crew.
- Crew replaces styrman-crew as what you work in. What styrman-crew gives a person at a terminal is not carried over, except terminals for diagnostics (13.11).

### 17.2 What is copied from styrman-crew, and what is not

**Copied.**

| Part | In styrman-crew | In Crew |
| --- | --- | --- |
| Installing Docker, Kata, and Cloud Hypervisor on a fresh machine | `deploy/host-setup.sh`, `deploy/lib/kata-config.sh` | Changed a little: renamed, without the three steps that belong to styrman-crew's own server, and with a system user in place of the operator's account (13.2) |
| Starting, stopping, adopting, and removing a microVM | `internal/orchestrator` | Changed: built around one microVM per project today, around one per purpose in Crew (13.3). The copied half built around one microVM per project, with its idle handling and its setup microVM, was reached by no binary and is gone since 0.84. |
| Running a command in a sandbox | `Orchestrator.Exec` | Changed. Today it stops after 30 seconds when the caller sets no time limit, keeps 2 MB of output, does not stream, and cannot stop the process. Crew starts `crew-run` with it and reads the run's files instead (12.4). |
| Sandboxes that cannot reach each other or the machine | One bridge per microVM, `deploy/firewall/` | Changed: the IPv6 rules are made complete, and hosted projects get a bridge kind of their own (16.10, 19.4). The server joins every sandbox bridge at a fixed address for the broker, as today, and must do so before the sandbox's first request. |
| The internet switch | Sandbox and LAN modes, chosen by the bridge's name | Changed: Model only is added (13.6) |
| The runtime image | `image/` | Changed: the harness is built in instead of `sh-agent`, `crew-run` is added, the download of `sh-agent` is taken out, only `agy` stays, and each release records which version of `agy` its image holds (12.8) |
| The credential store and broker | `internal/credstore`, `internal/credbroker` | Changed. The store keeps its values in the database, encrypted. The broker also asks GitHub for a sandbox's temporary token. A run gets the login of the one CLI it runs. |
| Signing in | The setup microVM and the setup script, with keep and renew for each login. In styrman-crew this is spread over the server's API, the credential harvest, and a script in the image. | Gathered in one package and moved to the first screen (12.6), and written again there for Crew's Setup in M2, which keeps the git identity reader, `stripControls`, and the check of a public key's line (`THIRD_PARTY.md`). The copied start and stop of the setup microVM, in the orchestrator, was reached by no binary and is gone since 0.84: Setup's microVM is a sandbox like any other. |
| A terminal in the browser | `internal/terminal`, and the terminal pane in the UI | Written again for Crew: Setup's terminal (`internal/setup/`) and the project terminal (`internal/projterm/`, 13.11) each speak to a sandbox of their own. The copied `internal/terminal`, with the store of tmux records it imported, was reached by no binary and is gone since 0.84. |
| Tests | The tests of the deploy scripts and of the orchestrator, and the integration suite | The script and orchestrator tests are copied. The script tests are Go tests in styrman-crew's `internal/config`, so they come with that package. The integration suite drives styrman-crew's own API, so it is written again against Crew's. Neither repository has continuous integration, so Crew's gate is the first. |

**Copied, with changes that matter for safety.**

| Part | In styrman-crew | Why |
| --- | --- | --- |
| The ingress | The vendored Caddy with Google sign-in, as its own Go module | You confirmed this. Crew runs on dave, so the UI is opened from another machine. Four things change (19.4, 20.3). It no longer reads container labels or holds the Docker socket. It no longer passes its shared secret or its cookie to a hosted project. Sessions are Crew's own, with a list of devices. And a hosted project gets a bridge the ingress can reach, which styrman-crew's firewall does not allow today. |

**Not copied.**

| Part of styrman-crew | In Crew instead |
| --- | --- |
| The server, its API, and its web UI | Written for Crew: the board, tickets, runs, git, and versions |
| One long-lived microVM per project, with idle handling | Originally a microVM per run at M1; since 1.37 (`13.3`, `13.12`), persistent warm project microVMs (`crew-proj-<project>`) with `PauseSandbox`/`UnpauseSandbox`, snapshot/restore, isolated git worktrees, and throwaway check sandboxes |
| The file explorer and the Git view | Crew's own git screens (6.7) |
| The Browser view, port previews, and share links | Not in the first versions. The E2E stage still uses a browser inside its own sandbox. |
| Several users, each with their own homes and credentials | One developer |
| The download of `sh-agent` when the image is built | The harness is in the image (12.1) |
| The updater: the root script on a timer, signed tags, building from source on the machine | A new updater (section 16). You said the old one worked only so-so. |

### 17.3 Copied from styrman-harness

The base is styrman-harness as of its version 2.7.0: 36 skills, 8 subagents, one rule, and the global context. The content is copied into `harness/` and adapted once. After that it is Crew's, and it changes through Crew's own tickets and improvements. You said that anything in it may be changed or dropped, so this table is where the copy starts, not a promise.

| Part | What happens to it |
| --- | --- |
| The global context, `context/AGENTS.md` | Rewritten for Crew: an agent works one ticket in one run, writes its records through the Crew MCP, and has no person at a terminal to ask. |
| The rules `critical-thinking` and `code-modularity` | `critical-thinking` as is; `code-modularity` copied from `fyr` in 1.35 |
| The subagents `explorer`, `reviewer`, `security`, `verifier`, `documentation`, `changelog`, `bugs` | Kept. Where they call `sh-agent search` or `sh-agent memory`, they call the Crew MCP. |
| The subagent `dreamer` | Dropped. The improver does this (10.4). |
| Skills about a craft: `bash`, `golang`, `typescript`, `frontend`, `frontend-ui-modern`, `frontend-ui-economist`, `docker`, `terraform`, `testing-strategy`, `codebase-design`, `json-api-style`, `mermaid-syntax`, `review-standards`, and the Google Cloud skills (`api-auth-gcp`, `cloud-run-deploy`, `data-gcp`, `gcloud-setup`, `gke-basics`, `operate-gcp`, `google-catalog`) | As is (`frontend-ui-modern`, `frontend-ui-economist`, and the `fyr` updates to `typescript`, `golang`, `review-standards`, and `bash` copied from `fyr` in 1.35). Until 0.79 they were why gcloud was one of a run's credentials; since then no run gets gcloud (12.6), so an agent uses them for what needs no sign-in, such as writing Terraform or a deploy plan, and runs no command against Google Cloud. |
| Skills about the way of working: `feature`, `bug`, `review`, `preflight`, `handoff`, `spec`, `requirements`, `research`, `grilling` | Their content moves into Crew's stage prompts and guides, because a Crew stage is what each of them describes. They are not installed as skills. |
| Skills about the harness itself: `init-project`, `project-memory`, `feedback-log`, `retro`, `distill`, `dream`, `harness-audit`, `explain-project` | Dropped. Crew's tickets, memory, feedback, and improver replace them. |
| The skill `find-skills`, and the manifest `skills/VENDORED.md` | Kept. The builder of a skill ticket uses them, and a project's skills from outside are recorded the same way (12.9). |
| Where a project keeps its own layer: `AGENTS.md`, `.agents/skills/`, `.agents/rules/` | Kept as the project layer (12.9) |
| The translation for `agy` | Moved into Crew's image build, as the build target for `agy`. The source stays in Claude Code's format (12.1) |
| How to call `agy` headless: the dreamer and the test kits | Copied into `crew-run` |
| The recorded quirks of `agy` | Kept as tests of the run driver (12.2) |
| The redaction rules | Copied into the event sink (8.3) |

- 29 files of the harness mention `sh-agent`, most often `sh-agent search` and `sh-agent memory`. Each mention is rewritten or removed in the copy.
- The trail that `init-project` creates under `.agents/docs/` is not created in new projects. Crew's tickets and records hold the same things. A repository that already has a trail keeps it, and Crew shows it as documentation (9.9).

### 17.4 Old rules that do not carry over

Both old repositories state rules that only you may change. You have decided: Crew is a new project, so they stay where they are.

| Old rule | In Crew |
| --- | --- |
| styrman-crew: the agent CLI in a terminal is the interface | Agents run headless and are shown on a board. Terminals are there for diagnostics. |
| styrman-crew: no database | Crew has one, and since 0.25 it is the brain: everything Crew saves is in it (6.5). |
| styrman-crew: one project is one microVM | One warm project microVM (`crew-proj-<id>`) with `PauseSandbox` idle suspend and isolated per-run worktrees + MCP tokens (since 1.37; earlier M3–M20 revisions used one ephemeral microVM per run) (13.3). |
| styrman-crew: a few users, each fully isolated | Crew is for one developer. |
| styrman-harness: it reaches a machine only through `sh-agent`, with no hooks and no gates | The harness is part of Crew and is in the image. Crew's stages are the gates. |

### 17.5 What the copy fixes

| Found in the old code | In Crew |
| --- | --- |
| A login that stops working is noticed only when an agent fails | The login check, and signing in again from the inbox (12.6) |
| A login renewed inside a sandbox is not taken back into the store | The newer login is kept when a run ends, after a test and only for the same account (12.6) |
| `agy` is installed at whatever version is newest on the build day | Pinned by its tarball and sha512 in `versions.lock` since 0.56; the release records the version too, and the image is kept (12.8). |
| The harness arrives through a separate program at a version that lags behind | Built into the image (12.1) |
| The updater fetches, builds, replaces, and only then checks | A release built once on the machine, checked beside the running version, then switched (16.4) |
| A roll back does not undo a one-way change to stored state | Changes in two releases, and a snapshot before each update (16.6) |

**How the copying is done.** In M1 each part is copied with the packages it imports, renamed, and unchanged in what it does, and the tests that came with it pass. The orchestrator and the terminal import styrman-crew's `internal/config`, so a cut-down copy of it comes along. Each change in the tables above is then made in the milestone that needs it, and that milestone replaces the tests: the ingress without container labels in M1, the credential store in the database in M2, and one microVM per purpose in M3. Comments in the copied files that point at styrman-crew's own decision and bug records are rewritten to say the reason, or removed, because those records are not copied (16.12).

### 17.6 Taking over a prior host

Where a host runs styrman-crew beforehand, any prior styrman-crew installation is removed first so that only Crew runs there.

| Step | What happens |
| --- | --- |
| 1. The spike | Originally planned to run while styrman-crew was still installed; styrman-crew was uninstalled at level 3 on 2026-10-12 before M0, so the spike ran against a stand-in of its runtime image, built from `5c9e6fb` (M0). |
| 2. What you keep | Push or copy out anything you still want from styrman-crew's projects before uninstalling; their homes and work folders live only on the machine. |
| 3. Remove | Executed before installing Crew (`styrman-ctl uninstall --level 3`): the stack, every project microVM, the firewall chains, the units, the updater, the deploy clone, the images, and the project data are gone. Docker and Kata are kept, and Docker's settings are restored from styrman-crew's backup. |
| 4. Install Crew | `crew-ctl install` runs (16.11). `doctor` first checks whether styrman-crew left anything behind: its data folder and two small files warn, and any leftover containers, units, or firewall chains fail preflight. The host setup finds Docker and Kata and leaves them when they match `versions.lock`. A Docker at another version of the same line warns, and `--upgrade-runtime` replaces it; an older Kata is moved aside after its proof (13.2, since 0.68). |
| 5. Free the disk | Done with step 3: level 3 removed styrman-crew's data folder. |

- **The gap.** During the initial bootstrap (step 3 until M3), no Crew agent ran automatically on the reference host; Crew was bootstrapped by an external coding agent until Crew could build Crew (M16). From M6 single tickets ran on it directly.
- **The names.** Crew can use the host name and the domain styrman-crew had, or new ones. They are in the install file (16.11).
- **Nothing is shared.** Crew's containers, firewall chains, units, and folders have their own names, so nothing of styrman-crew that was left behind is used by mistake. The bridge prefixes `sdb-` and `lan-` are kept, because the copied firewall matches them, and step 3 has removed styrman-crew's bridges before.
