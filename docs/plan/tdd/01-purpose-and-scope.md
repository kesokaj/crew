## 1. Purpose and scope

Crew is a kanban system worked by agents instead of humans. It has four main jobs:

1. **Show what agents are doing.** For every task the operator can see which agent is on it, with which model, which effort, and which prompt, what it came from, and every subtask it has used.
2. **Give agents structure.** Stages, limits, and checks are enforced on agents by the backend.
3. **Be the agents' ticketing system.** Every piece of work is a ticket with a number, a branch, commits, a review, and a history, so agents work in a real development workflow.
4. **Hold each project's documentation.** Every project has documentation that its agents write and keep current, stored with the project and readable in the app.

The operator develops a plan with a planning agent, creates a project from it, and gives one OK. Agents then work every task through every stage of the project's board until the project is done.

Every task has a why, a what, and a goal. Any agent can create a task through Crew's own MCP server, and another agent analyses it before it is built. Every agent run works in its own sandbox, every run's prompt and trajectory are saved, and every task ends with a summary in Crew's database. Each project keeps its own documentation, memory, bugs, feedback, evals, and analytics.

Crew is built for one developer who has several projects. The projects share nothing with each other. Crew itself sits above them: it has all the data, receives signals from every project, and uses them to evolve and improve the agents and the pipeline that every project runs on (section 10).

**In scope for v1**

- One developer (the operator) and one machine, with several projects that share nothing with each other.
- Plan drafts, project creation with a repository on GitHub or on the machine, breakdown, and the project OK.
- One board per project, with tasks as the only card, fixed stages, and optional stages chosen per project.
- Tickets: numbers, kinds, links, a branch and a pull request per ticket, and a history.
- Subtasks, saved prompts, and a task view that shows the agent, the model and the effort, the prompt, the source plan, and the subtasks.
- A lead agent and subagents per project. Each run is an agent CLI, started headless with a subscription login and no model API, in its own microVM built from a prepared runtime image. There is one CLI: `agy`, the Antigravity CLI. No other is added.
- Crew is an Apache-2.0 open-source repository (default upstream slug configured per installation via `CREW_SOURCE_REPO`, e.g. `your-org/crew`), with its own server and UI. It originally adapted eight infrastructure subsystems from the author's prior `styrman-crew` and initial harness content from `styrman-harness` (section 17, `THIRD_PARTY.md`; the copied Setup start/stop and terminal bridge were later rewritten in 0.84). It runs on a Linux host with Docker and Kata Containers.
- Git is a large part of the platform, with a repository on the machine or on GitHub (6.6, 6.7).
- Terminals for diagnostics: one for a project, and a jump into the sandbox of a running run (13.11).
- A new updater that checks a release before it replaces anything (section 16).
- One bare-metal machine, used only for Crew.
- Notifications by mail to start with, with the alternatives in the message. No mobile app. The phone comes later (section 18).
- Hosting a built project from the machine, with one button (section 19).
- One runtime image holds the agent CLIs and the harness, and is kept current (12.8).
- Setup at first start, a login check, and signing in again from the app (12.6).
- The model and the effort can be set per role, per project, and per ticket, from the models that the CLIs offer. A model belongs to a CLI, so choosing the model chooses the CLI.
- Clear version numbers, a notice when an update is there, and roll back. A release that changes only prompts and harness text installs by itself after 24 hours, and a release that changes code waits for your click (section 16).
- Install, update, and uninstall that work on a fresh machine, from one file of answers (16.11).
- A private repository that is reproducible and clean: every version it builds from is fixed, and it holds no secret (16.12).
- Crew proposes a better wording of what you write in planning, and you choose which one is sent. Anywhere you write, you can select text and choose Improve text or Review text (7.2).
- Every ticket has a chat, where you ask what happened and comment on text you mark (11.9).
- Crew's MCP server, which agents use to ask about the project and to create more tickets and subtasks, and which the operator's own tools can use too (7.5, 11.5, 11.7).
- Monitoring, saved trajectories, and task summaries.
- Per-project documentation, memory, bugs, feedback, evals, and analytics.
- A Crew level above the projects: signals from every project, a baseline of agents and pipeline that all projects run on, and an improver that proposes changes to it.
- All settings in the database, with a file that can be imported and exported. Editing all of them from the UI came with M17. A few were in the UI from the start: the credentials in Setup, notifications, the stages, the model and the effort per role, the network mode, and hosting.

**Software Factory evolution (M21 to M26, since 1.47)**

Building directly on the v1.46 foundation (`crew-db`, canonical/quarantine git, check sandboxes, warm Kata + Cloud Hypervisor `crew-proj-<id>` microVMs, and `agy acp --stdio --process-per-session`), milestones M21 to M26 reframe each project as a visual Software Factory:

- Two-tier Planning outside Backlog: Project Planning creates the foundational spec (`000-foundation.md`) and seeds `WORKFLOW.md` from `baseline/WORKFLOW.md` (with operator Project OK), while Feature Planning (`.crew/specs/NNN-<slug>.md`) is not always manual from `@user` — peer agents can ask `@planner` and decide feature specs autonomously within `000-foundation.md` and `13.8` guardrails before tickets enter `Tasks API` (`6.8`, `7.14`).
- Everything an agent executes is a trackable `Tasks API` item with a structured YAML-frontmatter contract (`R217`): executable build tickets (`feature`, `fix`, `chore`, `docs`) enter `Backlog`, ranked per project by `@coordinator` and scheduled across the machine by Crew's Global Queue, while non-code tasks (`spec`, `research`, `dream`, actionable `#<project>` chat turns) remain tracked off the Kanban board (`5.1`, `6.8`, `7.14`).
- Multi-session `Agent API` over `Harness Runtime` (`agy acp --stdio --process-per-session`, `1 session = 1 peer agent`, internal CLI subagents disabled), while keeping `internal/cli/` open for future minimal adapters such as `pi` or `claude` (`11.10`, `12.2`).
- `max_tries` circuit breaker that stops stuck runs and hands a clean, separate diagnostic prompt to `@researcher` as a tracked `research` task (`7.14`).
- Lightweight internal Slack-like chat per project (`#<project>` channel + one `feat/<NNN>-<slug>` thread per feature) where agents share messages with each other and `@user` (`11.10`, `20.4`).
- Visual workflow control plane in the containerized `shadcn/ui` web UI shipping all `40` runtime-switchable themes (`20.4`), indexed Trajectory Store search (`8.7`), `Memory API` validity-scored Dreamstore (`episodic`, `semantic`, `procedural`, `9.10`), scoped Dreaming (`10.8`), and cross-project `Knowledge API` (`9.11`, `R218`).
- Formalized `ComputeRuntime` (`13.12`, `R219`) exposing Crew's existing Docker + Kata warm suspend/resume (`PauseSandbox`/`UnpauseSandbox`) and sealed home snapshots behind an interface ready for future Kubernetes (`RuntimeClass: kata`), backed by lightweight split storage (`StateDB` in `PostgreSQL` or embedded `SQLite` WAL, plus `BlobStore` in `Local FS` or optional `GCS`, `5.1`, `R220`).

**Out of scope for v1**

- Multiple users, roles, and teams.
- More than one machine in one installation. An installation is one machine. Crew can be installed on other machines, each as an installation of its own that shares nothing with the first (16.12).
- Repository hosts other than GitHub for creating repositories and pull requests. Any git remote can still be connected.
- Importing from GitHub Issues, Jira, or Linear.
- A phone-sized UI. The phone is skipped for now.
