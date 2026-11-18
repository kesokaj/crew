## 5. Architecture

Everything below runs on one machine, which Crew has to itself. Crew's server and UI are written for Crew. Copied from styrman-crew are the sandbox orchestrator, the host setup, the firewall, the runtime image, the credential store and broker, the setup microVM (written again for Crew's Setup, 12.6; the copied start and stop, which nothing used, are gone since 0.84), the terminal bridge (written again for Setup and the project terminal; the copy, which nothing used, is gone since 0.84), and the ingress (section 17).

```
 Browser (web UI)
      | HTTPS
 +----v-----------+
 | crew-caddy     |  sign-in, TLS                                (container)
 +----+-----------+
      |
 +----v-----------------------------------------------------------------+
 | crew-server (Go, one binary, container)                              |
 |                                                                      |
 |  written for Crew                          copied from styrman-crew  |
 |  ----------------                          ------------------------  |
 |  API and web UI       Planning service     Sandbox orchestrator      |
 |  Board service        Run orchestrator     Credential store          |
 |  Run driver           Crew MCP server      Credential broker         |
 |  Git store            Git endpoint         Setup microVM             |
 |  Monitor              Trajectory store     Terminal bridge           |
 |  Prompt store         Summary writer                                 |
 |  Knowledge service    Documentation                                  |
 |  Signal collector     Improvement service                            |
 |  Quota guard          Login check          Versions                  |
 |  Notifier             Hosting service      GitHub client             |
 +---+-----------+-------------------+----------------------------------+
     |           |                   |
     |       crew-db            Docker (Kata runtime "mvm")
     |    (PostgreSQL)               |
 +---v-----------+   +---------------v--+ +------------------+ +------------------+
 | Canonical     |   | microVM: run A   | | microVM: run B   | | microVM: project |
 | repo          |   | agy              | | agy              | | terminal for the |
 | main, task/*, |   | crew-run         | | crew-run         | | operator         |
 | sub/*         |   | own clone, home  | | own clone, home  | | own clone, home  |
 +---^-------+---+   | own Docker       | | own Docker       | | own Docker       |
     |       |       +---+--------------+ +---+--------------+ +---+--------------+
     |       | push,     |   no path between sandboxes             |
     |       v fetch     +------------------+----------------------+
     |   GitHub (optional remote)           | only these ways out, by network mode
 +---+-----------+                          +---> Crew MCP, credential broker (on the sandbox's bridge)
 | Quarantine    | <--- git over HTTP ------+---> the CLI's model service (subscription login)
 | repo          |                          +---> Internet, when the mode allows it
 +---------------+

 Beside the server, each in its own container:
   crew-db        PostgreSQL. Everything Crew saves is in it. Only the server and the updater reach it.
   crew-updater   starts, checks, and switches versions
 On the machine, outside Docker, as root:  firewall timer, crew-ctl with its restart timer
```

| Component | From | Responsibility |
| --- | --- | --- |
| Ingress | styrman-crew | HTTPS and Google sign-in for the accounts the operator allows. Adds an identity header that the server requires. Gives each hosted project its address (19.4). |
| API and UI | Crew | REST and WebSocket, and the web UI built into the server binary. |
| Sandbox orchestrator | styrman-crew, changed | Starts, adopts, and removes microVMs through Docker with the Kata runtime (section 13). |
| Credential store and broker | styrman-crew, changed | Keeps the credentials in the database, encrypted, and gives each sandbox the ones it may have, over that sandbox's own network (12.6). |
| Setup microVM | Crew; the copy is gone since 0.84 | Where the operator signs in, at first start and again later (12.6). |
| Terminal bridge | Crew; the copy is gone since 0.84 | Connects a terminal in the browser to a shell in a microVM. Used by Setup and by the terminals for diagnostics (13.11). |
| Login check | Crew | Checks that each CLI is signed in, before every run and on a schedule. Starts the setup microVM for signing in again (12.6). |
| Planning service | Crew | Plan drafts, project creation, the original plan, the breakdown, and the project OK (7.2). |
| Board service | Crew | Tickets, their stages and subtasks, analysis decisions, limits, and the merge queue. Validates and applies every change to a task. |
| Check runner | Crew | Runs goal and exit checks: commands in a check sandbox, and the verdicts of a review or a verification from the record. Part of the board service. |
| Run orchestrator | Crew | Compares the board with running agents and starts, continues, or stops runs. Holds the limit on lead runs and checks the machine has room (7.9). |
| Run driver | Crew | Starts a CLI for one subtask and turns its output into events (12.4). |
| Quota guard | Crew | Tracks the subscriptions' limits, one state for each group of models of each CLI, and holds back new runs on a group while its limit is reached (12.7). |
| Crew MCP server | Crew | What agents talk to: asking about the project, creating tickets and subtasks, and writing records. The operator's own tools can use it with a personal token (7.5, 11.5, 11.7). |
| Monitor | Crew | Watches every task and run, and applies the watchdog rules (8.2). |
| Trajectory store | Crew | Writes every run's events to the database and serves them for replay (8.3). |
| Prompt store | Crew | Saves the full prompt of every run and the parts it was built from (8.6). |
| Summary writer | Crew | Writes a task's summary when it ends (8.4). |
| Assistant | Crew | Answers the operator in a conversation and in the chat of a ticket. It reads and tries things in a sandbox of its own, and changes no code, no ticket, and no setting. In a conversation it can propose a ticket, which is analysed like any ticket an agent created. In a chat it has no tool that writes (11.8, 11.9). |
| Prompt writer | Crew | Proposes a better wording of the operator's first message in a plan, and answers Improve text and Review text on text the operator selected (7.2). |
| Knowledge service | Crew | Memory, bugs, feedback, eval cases, proposed changes, and analytics (section 9). |
| Documentation service | Crew | Indexes the pages on `main`, serves and searches them, writes the changelog, and marks pages that may be out of date (9.9). |
| Git store | Crew | The only code that writes to `main`. Reads git for every code history, diff, and branch the UI shows (6.7). |
| Git endpoint | Crew | Serves the quarantine repo to sandboxes over their own network, and accepts pushes only to the run's own branch. Serves local projects to the operator for cloning (6.7). |
| GitHub client | Crew | Pushes and fetches over SSH with the key from Setup. With `gh` signed in, it also creates repositories, opens pull requests and marks them merged, and publishes release pages (6.6). |
| Signal collector | Crew | Records signals from every project as events and records arrive (10.2). |
| Improvement service | Crew | Schedules the improver, checks and tests its proposals, and measures the effect (10.4, 10.5). |
| Versions | Crew | Asks the updater for a build once, runs the gate's steps on it, and keeps the release list. Gives notice of a new release, counts the 24 hours, and asks the updater to update or roll back (section 16). |
| Updater | Crew, new | A small container beside the server. Loads a release, checks it beside the running version, switches, watches, and switches back (16.4). |
| Firewall | styrman-crew | Runs as root outside Docker, on a timer. Keeps sandboxes away from the machine, the local network, and each other. |
| Notifier | Crew | Sends a mail when the inbox gets an item, with its alternatives, and news when something finished or failed (section 18). |
| Hosting service | Crew | Builds a project into an image, starts it as a sandbox, checks it, and switches the address to it. Keeps the deploy records (section 19). |
| Settings | Crew | Rows in the database, for the machine and for each project, read by every component (section 14). |
| Database | PostgreSQL (or SQLite WAL) + BlobStore | `crew-db`, in its own container (or embedded `SQLite` WAL for lightweight single-node installs), paired with `BlobStore` (`Local FS` or optional `GCS`) for heavy immutable trajectory archives and snapshots (`5.1`, `6.5`, `15.3`, `R220`). |

### 5.1 Seven-module Control Plane and split storage architecture (M21 to M26, since 1.47)

Inside Crew's single Go server binary (`cmd/crew-server`) and its isolated microVMs, the Software Factory reimagining organizes responsibilities into seven explicit modules — four domain APIs and three runtime/storage layers:

| Layer | Module | Responsibility |
| --- | --- | --- |
| Domain API | **Tasks API** (`6.8`, `7.14`, `R217`) | Single trackable work-item boundary. Zero agent work executes outside a tracked Task ID, YAML contract, token/budget ledger, sandbox binding, and Trajectory trace. Code work (`feature`, `fix`, `chore`, `docs`) flows through Kanban `Backlog` -> `Done`; non-code work (`spec`, `research`, `dream`, actionable `@agent` `#chat` requests) stays tracked outside the code backlog. |
| Domain API | **Knowledge API** (`9.11`, `12.11`, `R218`, `R221`) | Cross-project shared knowledge collected across every factory on the installation: scrubbed (`internal/redact`) and provenance-fenced (`internal/fence`) **Skills Registry** (`SKILL.md` across base, shared, dream-synthesized, and SHA-pinned `VENDORED.md` tiers), cross-factory bug/issue advisories, and reusable architectural patterns. Never shares sandboxes or credentials; acting on an advisory or skill creates a Task in `Tasks API`. |
| Domain API | **Memory API** (`9.10`, `10.8`) | Project-scoped `Dreamstore` (`episodic`, `semantic`, `procedural` entries with `0.00` to `1.00` commit-drift and pass-hit validity scoring) and scheduled Dreaming loops strictly scoped to harness context micro-adjustments, `SKILL.md` synthesis, and SHA-pinned vetted external skill imports. |
| Domain API | **Agent API** (`11.10`) | Multi-session ACP peer control plane over `agy acp --stdio --process-per-session` (`1 session = 1 peer agent`): session lifecycle, health/heartbeats, live steering/abort, `@coordinator` dispatch, `max_tries` watchdog escalation, and bounded `#<project>` channel &amp; per-feature thread `@user`/`@agent` routing (including autonomous `@agent -> @planner` spec decisions, `max_agent_hops`). |
| Runtime | **Harness Runtime** (`11.10`, `12.2`, `12.11`) | CLI adapter seam (`internal/cli/agy.go` active, kept open for future `pi` and `claude` adapters), `baseline/WORKFLOW.md` loading from `main`, live-synced Skills Registry bundles into warm microVMs, scoped MCP credentialing, and real-time trajectory event capture. |
| Runtime | **ComputeRuntime** (`13.12`, `R219`) | Unified sandbox interface (`Start`, `Exec`, `Suspend`, `Resume`, `Snapshot`, `Restore`, `Stats`, `Destroy`). Tier-1 runs Docker + Kata (`crew-proj-<id>` warm microVMs with live `PauseSandbox`/`UnpauseSandbox` freeze/thaw and sealed `.tar.gz` snapshots, already implemented today) while keeping a clean seam for future Kubernetes (`RuntimeClass: kata` + `BlobStore` snapshot offload). |
| Storage | **StateDB + BlobStore** (`6.8`, `R220`) | Lightweight split storage: relational **State DB** behind repository interfaces supporting `PostgreSQL` (`crew-db`, production multi-factory default) or embedded `SQLite` WAL (lightweight single-binary mode), plus immutable **BlobStore** (`Local FS` or optional `GCS`) for `.jsonl.zst` trajectory archives, `512 MB` sealed VM snapshots, E2E media, and release images. |
