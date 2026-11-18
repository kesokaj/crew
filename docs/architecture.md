---
title: Architecture
covers: [cmd/**, internal/**, ingress/**, deploy/**, web/src/**, harness/**, baseline/**, image/**]
---
# Architecture

Crew `2.0` is a self-hosted **Software Factory per project** built around **seven modules** across four domain APIs and three runtime/storage layers. Each project's peer agents run inside that project's own warm Kata Containers (`mvm`) microVM (`crew-proj-<project>`) in isolated per-run git worktrees with per-session credentials, every check and setup step executes inside a throwaway microVM of its own, and every hosted project runs inside its own microVM, while the backend (`crew-server`) is the single writer of `StateDB` (`PostgreSQL` `crew-db` default or embedded `SQLite` WAL) and of canonical `main` in each project's repository.

The complete 28-section technical design lives in [`docs/plan/agent-kanban-tdd.md`](plan/agent-kanban-tdd.md) and [`docs/plan/tdd/`](plan/tdd/), the Software Factory `2.0` (`M21`–`M26`) blueprint lives in [`docs/plan/software-factory-plan.md`](plan/software-factory-plan.md), and visual per-feature deep-dive guides with Mermaid diagrams live in [`docs/features/README.md`](features/README.md).

## Seven-Module Control Plane (Design 5.1, M21–M26)

| Layer | Module | Design Ref | Core Contract |
| --- | --- | --- | --- |
| Domain API | **Tasks API** | `6.8`, `7.14`, `R217` | Mandatory pre-compute work boundary (`FactoryTask`). Zero agent work runs without a tracked Task ID, `.crew/tickets/<KEY>.md` YAML mirror, token ledger, sandbox binding, and Trajectory trace. Code work (`feature`, `fix`, `chore`, `docs`) flows through Kanban `Backlog` -> `Done`; non-code work (`spec`, `research`, `dream`, actionable `@agent` `#<project>` requests) stays tracked off-board. |
| Domain API | **Knowledge API** | `9.11`, `R218` | Installation-wide secret-scrubbed (`internal/redact`) and provenance-fenced (`internal/fence`, `<crew_data_fence>`) repository of cross-factory skills, bug advisories, and reusable architectural patterns. Never shares sandboxes or credentials; acting on an advisory mints a tracked `Tasks API` item. |
| Domain API | **Skills Registry** | `12.9`, `12.11`, `R221` | Four-tier catalog (`Base Harness`, `Cross-Factory Shared`, `Project & Dream-Synthesized`, `Vendored External` SHA-pinned in `.agents/skills/VENDORED.md`) with static AST vetting, `request_skill` Jaccard `>= 0.5` deduplication, per-factory task installation, and live overlay sync into warm Kata microVMs (`.gemini/config/skills/`). |
| Domain API | **Memory API** | `9.10`, `10.8`, `R215`–`R216` | Project-scoped **Dreamstore** (`episodic`, `semantic`, `procedural`, `0.00`–`1.00` validity with commit-drift decay, check-pass boosts, and `< 0.40` quarantine) + **Scoped Dreaming** (`@dreamer` restricted to harness context micro-adjustments, `SKILL.md` synthesis, and vetted external skill imports behind `@user` approval). |
| Domain API | **Agent API** | `11.10`, `R212`, `R214` | Multi-session ACP peer control plane (`1 session = 1 peer agent` over `agy acp --stdio --process-per-session`, internal CLI subagents disabled): `/api/agents/sessions` (`/turn`, `/steer`, `/abort`, `/stream`), health heartbeats, `@coordinator` backlog dispatch, `max_tries -> @researcher` clean-context escalation (`<KEY>-research.md`), and bounded `#<project>` / `feat/<NNN>` routing. |
| Runtime | **Harness Runtime** | `11.10`, `12.2`, `12.11` | `internal/cli/` adapter (`cli.Names()` locked to `agy`; `IsolationDedicatedKataVM`, `Capabilities`, `0600` OAuth `AuthSpec`, and `MultiProviderForbiddenEnv` seam kept open for `pi` and `claude`), `WORKFLOW.md` + `@security` and bounded custom specialists read strictly from canonical `main` (`13.8`, fail-closed symlink refusal), live-synced Skills Registry overlay, and per-session MCP credentials. |
| Runtime | **ComputeRuntime** | `13.12`, `R219` | Formal lifecycle interface (`Start`, `Exec`, `Suspend`, `Resume`, `Snapshot`, `Restore`, `Stats`, `Destroy`) over Docker + Kata warm per-project microVMs (`PauseSandbox`/`UnpauseSandbox`, sealed `.tar.gz` home snapshots) with a clean seam for future Kubernetes (`RuntimeClass: kata`). |
| Storage | **StateDB + BlobStore** | `5.1`, `6.8`, `R220` | Split storage: relational `StateDB` (`PostgreSQL` `crew-db` default or embedded `SQLite` WAL mode) paired with immutable `BlobStore` (`Local FS` or optional `GCS`) for `.jsonl.zst` trajectory archives and `512 MB` sealed microVM snapshots. |

### Two-Tier Planning, Unified Project Chat, and Visual Control Plane

- **Two-Tier Planning (`6.8`, `7.2`, `7.14`):** Tier 1 covers `.crew/specs/000-foundation.md` and initial `WORKFLOW.md`, gated by explicit operator Project OK (`Rule 12`). Tier 2 covers `.crew/specs/NNN-<slug>.md` feature specs initiated by `@user` or autonomously by peer agents (`@agent -> @planner` Q&A, searchable `DEC-*` rulings, and `@coordinator` YAML ticket emission into `Backlog`). Any change altering `000-foundation.md`, touching `13.8` protected files, or running under `approvals.feature_specs: manual` escalates to `#<project>` and **Inbox**.
- **Retired `lead` Role (`15.3`, schema step 86):** `lead` was retired into `planner` (plan drafts, breakdowns, and feature specs on Gemini) and `curator` (stewardship, ticket analysis, look-again, housekeeping, and final reports), alongside peer specialists `@coordinator`, `@builder`, `@researcher`, `@reviewer`, `@security`, `@tester`, and `@dreamer`.
- **Unified `#<project>` Channel & `feat/<NNN>-<slug>` Threads (`11.10`, `20.4`):** Replaces standalone 1:1 conversations and per-ticket chat workflows for project work with one canonical `#<project>` room and per-feature threads featuring CommonMark `ExtractProseMentions` (`10`-recipient cap), orthogonal `kind`/`intent` + `chat_message_addressees` (`posted_nobody_woken`), queue-by-default turns vs `@user`-only **Interrupt active turn now** (`steer`), interactive versioned keyed `ChoicePrompt` cards (`v<n>`, `Superseded`, `direct` `@user` vs `agent_interpreted` provenance in `spec_decisions`), `> 4000`-rune agent offload (`read_chat(messageId=...)`), `max_agent_hops` (`default: 3`), and `<crew_data_fence>` wrappers (`Rule 14`).
- **Direction A Control Plane (`20.4`):** React + TanStack Router + Tailwind CSS shell (`sidebar-07` inset collapsible navigation) with **40 runtime-switchable themes** (`catppuccin` dark default, light/dark toggle `d`, Swatch Gallery persisted in `operator_prefs`), `⌘K` command palette, `390px` mobile drawer with `VisualViewport` keyboard handling, zero inline `style=` attributes (CSP), and strict zero-currency/budget copy checks (`nodollars`).

## Core Containers on the Machine (Design 5 & 13.1)

Every runtime component of Crew runs in Docker (`deploy/compose.yaml`):

| Container | Image / Entry | Role |
| --- | --- | --- |
| `crew-caddy` | `ingress/` (`caddy`) | TLS termination, Google OAuth sign-in, session cookies, CSRF and rate limiting, and routing for Crew (`CREW_HOSTNAME`) and hosted project apps (`CREW_APPS_DOMAIN`). Holds no Docker socket and no project secrets. |
| `crew-server` | `cmd/crew/` (`crew serve`) | HTTP API (`internal/httpapi/`), embedded React web UI (`web/`), orchestrator, `Agent API`, `Tasks API`, `Rooms`, `Knowledge API`, `Skills Registry`, `Dreamstore`, `ComputeRuntime` (`internal/sandboxes/`), git HTTP endpoint (`internal/gitendpoint/`), egress proxy (`internal/egress/`), and credential broker (`internal/credbroker/`). |
| `crew-db` | `postgres:18.6-trixie` | PostgreSQL 18 primary `StateDB` (`internal/db/`), reachable only on the internal `crew-net` bridge (paired with optional embedded `SQLite` WAL in `internal/db/sqlite/` and `BlobStore` in `internal/blobstore/`). No sandbox has a route to `crew-db`. |
| `crew-updater` | `cmd/crew-updater/` | Listens on `/run/crew/updater.sock` to gate, verify on a scratch DB + smoke microVM, atomically swap, watch health for 10 minutes, and roll back releases (`internal/updater/`). |

## Sandboxes and Network Isolation (Design 12 & 13)

Every sandbox is a Docker container started with `--runtime mvm` (Kata Containers + Cloud Hypervisor) on its own dedicated `/29` bridge (`deploy/firewall/crew-firewall.sh`):

- **Warm per-project microVMs (`crew-proj-<project>`, `sdb-*` / `mdl-*` / `lan-*`):** Each project has one persistent-home microVM (`<CREW_DATA_DIR>/projects/<id>/home`, with secret-key-sealed snapshots under `<CREW_DATA_DIR>/projects/<id>/snapshots/`) running `crew-run acp-daemon` (`agy acp --stdio --process-per-session`) as user `agent` (`1000:1000`, never root) with the `agy` CLI (`internal/cli/`), live-synced `.gemini/config/skills/` overlay, per-session git worktrees (`/home/agent/worktrees/<run-id>` backed by `/home/agent/repo.git`), a 1-hour read-only GitHub token for the project's repositories, and per-session Crew MCP tokens (`internal/sandboxes/`). Idle project VMs suspend (`ContainerPause`, `0% CPU`) after `sandbox.suspend_after_minutes` and stop (`0 MB` RAM) after `sandbox.stop_after_minutes`.
- **Model-only sandboxes (`mdl-*`):** Used for planning, prompt rewrites, summaries, judges, clean-context `@researcher` escalation, `model_only` runs, improver, and login checks; egress is restricted to `network.model_only_hosts` through `crew-server`'s TLS SNI proxy.
- **Sources sandboxes (`src-*`):** Used when building a release or checking `main` in a throwaway check sandbox (`internal/versions/`, `internal/harness/`).
- **Hosted project sandboxes (`app-*`, `apo-*`):** Run deployed project containers behind `crew-caddy` (`internal/hosting/`).

## Codebase Layout & Package Table (Design 15.1)

### Top-Level Directories & Commands

| Path | Responsibility |
| --- | --- |
| `cmd/crew/` | Server (`crew serve`) and operator CLI (`crew admin ...`, `crew release write`, `crew health`). |
| `cmd/crew-run/` | In-sandbox run driver (`turn`, `acp-daemon`, `acp-turn`, `acp-steer`, `acp-cancel`, `login-check`, `creds`, `proof`) executing `agy` turns and streaming trajectory events. |
| `cmd/crew-updater/` | Standalone release gate, scratch-DB migration checker, atomic container switcher, and 10-minute watch/rollback daemon. |
| `cmd/crew-harnessbuild/` | Builds `harness/` into the runtime image's home skeleton for `agy` (`internal/harnessbuild/`). |
| `ingress/` | Caddy ingress Go module (`github.com/kesokaj/crew/ingress`): Google OAuth2 sign-in portal, route generator, and cookie stripping. |
| `web/` | React + TanStack Router + Tailwind CSS control plane (`Direction A`, 40 themes, `Skills Registry`, `Project Chat`, `Factory Line`, `Dreamstore`, `Runtimes & Storage`). |
| `baseline/` & `harness/` | Embedded agent role definitions (`planner`, `coordinator`, `curator`, `builder`, `researcher`, `reviewer`, `security`, `tester`, `dreamer`, etc.), `WORKFLOW.md`, stage prompts, guides, `tools.yaml`, and base `agy` harness files. |
| `image/` & `deploy/` | Runtime Kata sandbox `Dockerfile`, `crew-ctl`, `compose.yaml`, `host-setup.sh`, `release.sh`, and `firewall/crew-firewall.sh`. |
| `improver.go`, `improver.md` | Embedded Crew-level improver agent definition (`10.3`). |
| `tools/` | Verification checks (`check-clean.py`, `check-versions.py`, `plan-copies.py`, `tools_test.py`), `check-image/`, `e2e/` (`sandbox.sh` + Playwright `browser/`), and `install-test/`. |

### `internal/` Backend Packages

| Package | Responsibility |
| --- | --- |
| `internal/analytics` | Per-project and Crew-level metrics, rollups, and aggregates (`9.7`). |
| `internal/assistant` | Legacy/standalone operator conversations and read-only branch chat helpers (`11.8`, `11.9`). |
| `internal/backup` | Daily `.tar.gz` database/git/vault backups, monthly event archives, and restore (`16.9`). |
| `internal/blobstore` | Immutable `BlobStore` interface (`Local FS` in `fs.go` and `GCS` in `gcs.go`) for `.jsonl.zst` trajectory archives and sealed microVM snapshots (`5.1`, `R220`). |
| `internal/board` | Projects (`Protected` locks, `crew-gate`), tickets, stages, subtasks, `Tasks API` (`FactoryTask`), `.crew/specs/` & `spec_decisions`, `research_briefs`, `WORKFLOW.md` + custom specialist parser, merge queue, checks, docs index, and project knowledge records. |
| `internal/capacity` | Machine room checks by CPU, memory, and reserve (`7.9`, `13.10`). |
| `internal/cli` | `agy` adapter (`agy.go`, `acp.go`, `adapter.go`): `cli.Names()` locked to `agy`, `IsolationDedicatedKataVM`, `Capabilities`, `0600` OAuth `AuthSpec`, and `MultiProviderForbiddenEnv` seam kept open for `pi`/`claude` (`12.2`). |
| `internal/config` | Sandbox labels, guest environment constants, bridge names, microVM runtimes, and IP-forwarding checks. |
| `internal/credbroker` | Serves scoped credentials to sandboxes over each sandbox's dedicated bridge (`12.6`). |
| `internal/credstore` | AES-encrypted credential vault backed by `/etc/crew/secret.key` (`12.6`). |
| `internal/db` | PostgreSQL pool/migrations (`db/migrations/` steps `0001`–`0094`, `db/dbtest/` template test DBs) and optional embedded `SQLite` WAL `StateDB` (`db/sqlite/`, `5.1`, `R220`). |
| `internal/diskguard` | Disk usage monitoring, retention cleanup (`clean_at`), and hold enforcement (`stop_at`, `16.10`). |
| `internal/dreamer` | **Scoped Dreaming** (`@dreamer` cycle tracked by `Tasks API`, restricted to harness/skill paths, statically vetted, gated on `@user` Inbox approval before `main` commit and warm-VM sync, `10.8`, `R216`). |
| `internal/egress` | HTTPS `CONNECT` proxy for `model_only` and `sources` sandboxes plus Go module proxy (`13.6`). |
| `internal/export` | Project export to files and paused re-import (`6.2`). |
| `internal/fence` | `<crew_data_fence>` provenance wrappers around untrusted agent text, peer chat, cross-factory knowledge, and check outputs (`13.8`, `Rule 14`). |
| `internal/gitendpoint` | HTTP git endpoint for sandboxes and project terminals, pre-receive hook enforcement, and push bounds (`6.1`, `13.8`). |
| `internal/github` | Server-side SSH and `gh` client for repository creation, PR sync, and release publishing (`6.6`). |
| `internal/githubapp` | Short-lived, read-only, project-scoped GitHub App installation token minting (`12.6`). |
| `internal/gitstore` | Canonical and quarantine bare repositories, merges, diffs, bounded git output readers, and symlink-safe blob/index resolution (`6.1`, `13.8`). |
| `internal/harness` | `Harness` interface, `FakeHarness`, `CheckRunner`, and Kata command driver (`22`). |
| `internal/harnessbuild` | Compiles `harness/` context, rules, and skills into the runtime image home skeleton (`17.3`). |
| `internal/hold` | `Pause all`, `Stop all`, microVM boot failure holds, and disk/database write probes (`7.9`, `21`). |
| `internal/homepath` | Symlink-safe path resolution inside a sandbox's home directory (`12.6`). |
| `internal/hosting` | Builds, starts, probes, and routes hosted project apps behind `crew-caddy` with encrypted deploy settings (`section 19`). |
| `internal/httpapi` | REST, SSE, and WebSocket handlers (`server.go`, `agent_api.go`, `rooms.go`, `m22_api.go`–`m26_api.go`, `m25_factory.go`, `m25_trajectories.go`, `m25_blobs.go`, fresh sign-in gates). |
| `internal/improve` | Crew-level improver scheduler, generic check runner, and baseline patches (`section 10`). |
| `internal/inbox` | Aggregates operator decisions, `13.8` escalations, `@dreamer` proposals, and system notices (`7.8`). |
| `internal/knowledge` | Installation-wide **Cross-Project Knowledge API** (`9.11`, `R218`): secret scrubbing, `<crew_data_fence>` isolation, search, and `Tasks API` actuation across factories. |
| `internal/logins` | Scheduled/on-demand CLI login checks, quota tracking, and Setup re-auth (`12.6`, `12.7`). |
| `internal/machinecare` | Quiet-hour security update restarts and TLS certificate expiry checks (`16.10`). |
| `internal/mail` | SMTP sender, `mail/mailtest/` stand-in server, and notification email rendering (`section 18`). |
| `internal/mcp` | Crew MCP server, scoped tool authorization (`ScopeProject`, `ScopeRoom`, `ScopeDraft`, `ScopeOutside`), and personal tokens (`11.5`, `11.7`). |
| `internal/measure` | Eval suite comparisons and release effect measurement (`9.6`, `10.5`). |
| `internal/memory` | **Memory API Dreamstore** (`9.10`, `R215`): typed `episodic`/`semantic`/`procedural` memory entries, `0.00`–`1.00` validity scoring, commit-drift decay, pass boosts, and `< 0.40` quarantine. |
| `internal/models` | Deterministic `agents.brackets` classification, `(version DESC, first_seen DESC)` ranking, promotions, check-family policy (`Rule 15`), quota fallback, and `models/modelstest/` (`12.3`, `12.7`). |
| `internal/notify` | Notification queue, daily digests, reminders, and action links (`section 18`). |
| `internal/orchestrator` | MicroVM lifecycle (`crew-proj-<id>`, ephemeral sandboxes), exec, container stats, pause/unpause, and bridge attachment (`13`). |
| `internal/pins` | Daily check for `agy` and `versions.lock` publisher updates (`12.8`). |
| `internal/pipeline` | Reconcile loop, `@coordinator` dispatch, `max_tries -> @researcher` circuit breaker, stage execution, reviews, tests, custom `WORKFLOW.md` specialists, merge queue, evals, and MCP room/M26 tools (`sections 7, 9, 13`). |
| `internal/planning` | Two-tier plan drafts, `.crew/specs/000-foundation.md` vs `NNN-<slug>.md`, autonomous `@agent -> @planner` feature decisions, and Project OK (`7.2`, `7.14`). |
| `internal/projterm` | Browser-attached project terminal in read-only diagnostics and edit modes (`13.11`). |
| `internal/redact` | Secret scrubbing before events, logs, trajectories, or Knowledge API entries are stored (`8.3`, `9.11`). |
| `internal/release` | Release classification (`text`/`code`/`setup`), `16.8` protected paths, runtime-image input hashes, and release notes (`16.2`, `16.3`, `16.8`). |
| `internal/rewrite` | Prompt writer proposals and inline text Improve/Review actions (`7.2`). |
| `internal/rooms` | Unified `#<project>` channels and `feat/<NNN>-<slug>` threads: CommonMark `ExtractProseMentions`, orthogonal `kind`/`intent` + `chat_message_addressees`, `steer` vs queue, keyed `ChoicePrompt` cards, `> 4000`-rune offload, and `max_agent_hops` (`11.10`, `R214`). |
| `internal/runs` | Multi-session `Agent API` (`acp` peer sessions, `/turn`, `/steer`, `/abort`, `/stream`), global `max_concurrent_runs` queue, quota guard, spool reader, watchdog, and `FactoryTask` pre-compute guard (`11.10`, `12`). |
| `internal/runspec` | JSON/file protocol exchanged between `crew-server` and `crew-run` inside a sandbox home (`12.4`). |
| `internal/sandboxes` | Formal `ComputeRuntime` interface and Docker+Kata driver (`runtime.go`), warm project microVMs, idle suspend/stop reaping, sealed home snapshots, and ephemeral check/setup/conversation sandboxes (`13.3`, `13.12`, `R219`). |
| `internal/selftest` | End-to-end self-test run by `crew-ctl selftest` and release verification (`16.4`, `16.11`). |
| `internal/sessions` | 30-day device sessions, handover nonces, and 15-minute fresh sign-in enforcement (`20.3`). |
| `internal/settings` | Machine (`crew.yaml`) and per-project (`board.yaml`) settings schema validation, defaults, and history (`section 14`). |
| `internal/setup` | Setup microVM, browser terminal, and encrypted login bundle capture (`12.6`). |
| `internal/skills` | 4-tier **Skills Registry** (`vet.go` static AST + `VENDORED.md` SHA/licence checks, `registry.go` Jaccard `>= 0.5` `request_skill` deduplication, per-factory task installation, and warm-VM live overlay sync, `12.11`, `R221`). |
| `internal/updater` | Release verification daemon, scratch-DB migration check, smoke microVM, atomic swap, 10-minute watch, and rollback (`16.4`). |
| `internal/version` | Running binary version metadata (`version.Version`). |
| `internal/versions` | Release builder orchestration, gate runner, auto-install clock, and updater socket client (`16.2`–`16.4`). |
