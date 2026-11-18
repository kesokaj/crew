# Crew: requirements

- **Version:** 2.01, matching version 2.01 of the design
- **Date:** 2026-11-18
- **Companion:** `agent-kanban-tdd.md` (technical design). Each requirement ends with the design section that covers it.

Crew is a kanban system worked by agents instead of humans. The operator develops a plan, creates a project from it, and gives one OK. Agents then work the tickets through the stages of the project's board until every ticket is Done, cancelled, skipped, or discarded.

Crew is for one developer who has several projects. The projects share nothing with each other. Crew itself has all the data and gets signals from every project, and uses them to improve the agents and the pipeline.

Crew is an open-source repository (Apache-2.0, `LICENSE`; originally started under `kesokaj/crew`), with its own server and UI. It copies eight parts of styrman-crew: the sandbox orchestrator, the host setup, the firewall, the runtime image, the credential store and broker, the setup microVM (written again for Crew's Setup, 12.6; the copied start and stop, which nothing used, are gone since 0.84), the terminal bridge (written again for Setup and the project terminal; the copy, which nothing used, is gone since 0.84), and the ingress. Its harness, which is what the agents read, starts as a copy of styrman-harness. The agent is a command-line agent, signed in with a subscription: `agy`, the Antigravity CLI, and no other. No model API is used. Everything Crew saves is in its database, and every project's code is in a git repository, on the machine or on GitHub. It has clear versions, updates itself, and can be rolled back from inside the app.

## What Crew is for

1. **It shows what agents are doing.** Every ticket, every subtask, every agent, its model and effort, and its prompt can be seen while it runs and afterwards.
2. **It gives agents structure.** Stages, limits, and checks are enforced on agents by Crew.
3. **It is the agents' ticketing system.** Every piece of work is a ticket with a number, a branch, commits, a review, and a history, so agents work in a real development workflow.
4. **It holds each project's documentation.** Every project has documentation, written and kept current by its agents, stored with the project and readable in the app.
5. **It runs without the operator as far as it can.** After one OK, the operator is needed only at a project limit, for a sign-in, for a release that must wait, and for a change to a file that is code for the CLI or for GitHub.
6. **It learns.** Each project learns for itself. Crew learns from all projects and improves what they all run on.

## The flow in nine steps

1. **A plan is developed.** The operator and a planning agent write a plan before any project exists.
2. **The operator creates a project.** Crew creates the git repository, on GitHub or on the machine. GitHub is reached over SSH.
3. **The plan is added to the project** as its original plan.
4. **An agent analyses the plan** and splits it into tickets. They wait in Planning.
5. **The operator says OK.** The pipeline then runs until every ticket is Done, cancelled, skipped, or discarded.
6. **Every ticket is monitored.** The trajectory of every run is saved, and a summary of each ticket is stored in Crew's database.
7. **Every ticket has a why, a what, and a goal.**
8. **Agents can create tickets and subtasks.** Crew has its own MCP server. Any agent can ask it about the project and create a new ticket in the project it runs in. The ticket goes to the backlog. The lead, while it analyses a ticket, and the builder can also add subtasks to that ticket. An agent that meets a choice the plan does not settle decides, records the decision, and carries on.
9. **An agent analyses each ticket from the backlog.** It sets the ticket's why, what, and goal, and approves, skips, or discards it, or gives notice of it as a new feature.

## The stages

Every project has its own board. A ticket moves through the stages from left to right.

| Stage | Kind | What happens there |
| --- | --- | --- |
| Planning | Fixed | What is not yet accepted work: the tickets from the breakdown, before the project OK. |
| Backlog | Fixed | Accepted work that waits to be analysed. Tickets that agents create land here. |
| Analysis | Fixed | An agent analyses the ticket against the plan and the current code, and sets its why, what, goal, and checks. A skipped ticket, and a new feature during its 24-hour notice, wait here too. |
| In progress | Fixed | A builder does the work on the ticket's branch. |
| Review | Optional, on by default | Another agent reads the change. |
| Test | Optional, on by default | Another agent runs the tests in a fresh sandbox. |
| E2E | Optional, on when asked for | An agent starts the whole application and drives it through the user flows the ticket touches. |
| Verification | Optional, on when asked for | An agent checks the result against the ticket's goal and the source plan. |
| Done | Fixed | The ticket is merged, its summary is written, and the changelog is updated. |

The optional stages sit between In progress and Done. Which ones a project uses is decided with its plan and can be changed later.

A ticket only moves to the right. It never goes back a stage. A ticket that fails a stage is fixed where it stands. A ticket that cannot get through is cancelled and replaced by other tickets.

## Two levels

- **Project level.** Each project has its own repository, board, agents, sandboxes, documentation, and knowledge. An agent in a project sees that project and nothing else.
- **Crew level.** Crew sees every project. It collects signals from all of them and improves the baseline.
- **The direction is fixed.** Signals go up from projects to Crew. The baseline comes down from Crew to projects. Nothing goes from one project to another.

## Vocabulary

One word for each thing. Where the design uses "task", it means a ticket.

### People and agents

| Term | Meaning |
| --- | --- |
| Operator | The one developer who uses Crew and controls the projects. |
| Admin email | The operator's Google mail address. It is the only one that can sign in to Crew. |
| Agent CLI | The command-line agent that does the work in a run: `agy`, the Antigravity CLI, the only one. |
| Adapter | The one part of Crew that knows a CLI: how it is installed, signed in, started, read, and continued, which models it offers, and what its limits are. Adding a CLI means adding an adapter. |
| Pin | The operator's choice of a model or an effort for one ticket. A project can also set its own for a role. Without either, the agent runs on what Crew chose for it, or on what the operator set for the machine. |
| Lead slot | Room for one lead run. `agy` has 1 to start with. |
| Chat of a ticket | A conversation that belongs to one ticket. The operator asks, and an assistant answers from the ticket's records. It is not the agent that works on the ticket. |
| Assistant | The agent that answers the operator in a conversation and in the chat of a ticket. It can read and try things in a sandbox of its own. It cannot change code, a ticket, or a setting. In a conversation it can propose a ticket. In a chat it can write nothing. |
| Marked text | Text the operator selected with the mouse, to comment on it or to have it improved or reviewed. |
| Planning agent | The agent that helps the operator write a plan before a project exists. It runs as a lead run. |
| Lead | A project's main agent. It makes the breakdown, analyses tickets, looks again at a ticket that cannot get through, writes the final report, and does housekeeping. It does not change code. |
| Builder, reviewer, tester, E2E tester, verifier | The agents that work on one ticket in In progress, Review, Test, E2E, and Verification. |
| Improver | Crew's own agent. It reads the signals from all projects and proposes improvements. |
| Helper | A side job an agent starts for itself in its own sandbox, with the CLI's own subagent feature. |

### Plans and tickets

| Term | Meaning |
| --- | --- |
| Plan draft | A plan being developed before a project exists. |
| Original plan | The plan a project was created from. It is kept unchanged in the project. |
| Amendment | A later change to the plan, kept beside the original. |
| Breakdown | The split of the original plan into tickets, made by the lead. |
| Project OK | The operator's approval of the plan, its breakdown, and its stages. It starts the pipeline. |
| Ticket | The one kind of card on a board. It has a number such as SF-42, a why, a what, a goal, and checks. |
| Why | The reason the ticket exists, traced to the plan, a bug, or what an agent found. |
| What | What will change, and what is out of scope. |
| Goal | The result that must be true when the ticket is done. |
| Check | What proves a goal: a command that Crew runs, the verdict of a review or a verification, or a criterion a separate judge decides. |
| Source plan | The part of the original plan a ticket comes from. |
| New feature | A ticket that adds something the approved plan does not cover. |
| Subtask | One piece of work inside a ticket, done by one agent run. A ticket gets one each time it enters a stage that has an agent. The lead, while it analyses the ticket, and the builder can add at most 6 more. At most 3 run at once, each on its own branch. |
| Join | Bringing the branches of parallel subtasks together into the ticket's branch. Crew does it. |
| Step | One line in the checklist an agent keeps for its subtask. |
| Decision | A choice an agent made by itself where the plan was silent, recorded with its question, alternatives, and reason. Not the same as a decision record. |
| Decision record | A page in the documentation that records one design decision and its reason. |
| Skip, discard | Two outcomes of analysis. A skipped ticket stays in Analysis with a condition to look again. A discarded ticket is closed. |
| Fix round, rework | A ticket that fails a stage stays in that stage. The findings become a fix subtask, and the stage checks again. Each time is one fix round. Rework is the fix rounds a ticket needed. |
| Cancelled and replaced | A ticket the lead closed because it could not get through, with the tickets that replace it. They carry the link `replaces`. It does not stop the project from being Done. |

### The board

| Term | Meaning |
| --- | --- |
| Board | One per project. Its stages are listed above. |
| Fixed stage, optional stage, custom stage | A fixed stage is on every board. An optional stage is switched on per project. A custom stage is one a project adds for itself. |
| Analysis | The stage where an agent analyses a ticket and decides: approve, skip, discard, or give notice of a new feature. |
| Exit check | A check a ticket must pass to leave a stage. |
| Work-in-progress limit | How many tickets a stage holds at once. |
| Project limit | The share of tickets agents may add to the approved plan. Reaching it pauses the project. |
| Hold | A stop on one thing: a ticket, a new feature, or a release. The operator sets and lifts it. Crew itself holds a release that failed its check, and a newer release takes the place of a held one. |
| Notice | Telling the operator that something will go ahead by itself after 24 hours: a new feature, or a release. A release that changes code does not go ahead by itself: its notice says that it waits for Install. |
| Inbox | The one list of what needs the operator and of what is about to go ahead. |

### Runs and sandboxes

| Term | Meaning |
| --- | --- |
| Run | One agent working toward one goal on one subtask, in its own sandbox. It can be several calls of its CLI that continue one conversation. |
| Lead run | A run that takes a lead slot: the lead or the planning agent at planning, breakdown, analysis, looking again at a ticket, the final report, and housekeeping, and also the improver and a turn of a conversation. |
| Subagent run | A run of a builder, reviewer, tester, E2E tester, or verifier on one ticket. |
| Run record | What a run leaves on the board: its outcome, check results, rounds, tokens, and duration. |
| Run home | A directory on the machine for one run, while its sandbox exists. It is removed with the sandbox. |
| Conversation | One thread of turns in Crew. Every run belongs to one, and so does every exchange between the operator and an agent. It has an id at `agy`, and is saved turn by turn. When it moves to another model it may be rebuilt there from its turns. |
| Turn | One message to an agent and everything the agent did before it answered. |
| Trajectory | The full record of one run or one conversation: every message, tool call, and result, in order. |
| Ticket summary | What a ticket did and how it went, written when it ends, as Done or closed, and stored in Crew's database. |
| Check sandbox | A sandbox with no agent and no run token, in which Crew itself runs the checks that decide. |
| Closed | The end of a ticket that is not Done: discarded by Analysis or the operator, or cancelled by the lead and replaced. |
| Setup | The first screen, before any project, where every credential is set up for the machine. |
| Housekeeping | A lead run that handles feedback, memory, and documentation that is behind the code. |
| Final report | What the lead writes when a project is Done: what was built, what was skipped or closed and why, and what was learned. |
| Watchdog | The part of Crew that stops a run that is silent, repeats itself, or runs too long. |
| Fresh sign-in | A sign-in at Google that is at most 15 minutes old, asked for before the actions that could hand over the machine or the credentials. |
| Sandbox | A microVM that exists for one purpose, most often one run, and shares nothing with other sandboxes. It is a Docker container started with the Kata runtime, so it has its own kernel. |
| Runtime image | The one prepared image every sandbox starts from, except a hosted project. It holds `agy`, the harness, the run driver, Docker, and a standard set of command-line tools. |
| Harness | What a CLI reads: the global context, the skills, the CLI subagents, and the rules. The base is a folder in Crew's source repository, copied from styrman-harness, kept once in Claude Code's format, and built into the runtime image for `agy`. |
| Skill, context | A skill is a folder of instructions the CLI loads when it needs them. Context is what every run reads. Both come from the base, and a project can add its own in its repository. |
| Sideload | Putting a project's own skills and context into a sandbox when it starts, without rebuilding the runtime image. |
| Run driver | The program that starts a CLI in a sandbox, follows its output, and stops or continues it. |
| Setup microVM | A small sandbox that exists only for signing in. No project and no agent runs in it. |
| Project terminal | A sandbox the operator opens on one project, with a terminal in the browser and a clone of the project's git repository, which takes no push in diagnostics mode. It has an edit mode. |
| Network mode | What a sandbox may reach: Internet, Model only, or LAN. |
| Model service | The vendor's service that a CLI talks to. It is on the internet. |
| Credential | Something set up once in Setup, for the machine: the git identity, the git SSH key, `gh`, the GitHub App, gcloud, and `agy`. The app password of the mail account is kept the same way. Crew keeps it in the database, encrypted. The SSH key, the `gh` login, and the key of the GitHub App stay with the server. A sandbox gets what R123 lists. |
| Credential store, broker | Crew's store of credentials and secrets, encrypted in the database, and the service that gives a sandbox its credentials when it starts. |
| Login check | Crew's check that a CLI is still signed in, before every run and on a schedule. |
| Quota | The limits a subscription puts on how much its CLI can be used in a period. Each CLI has its own. `agy` counts them in two groups of models, and all runs on it share them. |
| Limit group | The models of one CLI that share a five-hour and a weekly limit. `agy` has two groups: Gemini models, and Claude and GPT models. |
| Model | One model a CLI offers, such as `gemini-4-argon` or `claude-opus-5-5` on `agy`. A model belongs to a CLI. |
| Effort | How hard a model thinks: low, medium, or high, and for some models also fast or max. `agy` puts it at the end of the model's name. |
| Family | The maker of a model: Gemini, Claude, or another. A check uses another family than the build. |

### Storage and git

| Term | Meaning |
| --- | --- |
| `main` | The branch with the project's code and documentation. |
| Database | PostgreSQL in its own container on the machine. Everything Crew saves is in it. |
| Change history | The table that keeps every change to a ticket, a record, or a setting: who, when, and the value before and after. |
| Export | A project written out as a folder of files, to read, to move to another machine, or to keep. |
| Ticket branch, subtask branch | `task/<ticket>-<name>` holds the work of one ticket. `sub/<ticket>.<n>` holds the work of one parallel subtask. |
| Source repository | Crew's code, harness, and baseline (configured per installation via `CREW_SOURCE_REPO`, e.g. `your-org/crew`; Go module `github.com/kesokaj/crew`). Agents work on it. |
| Spool | The file a run's events are written to in its sandbox, until Crew has stored them in the database. |

### Crew level, and versions

| Term | Meaning |
| --- | --- |
| Signal | A fact Crew records about how the agents and the pipeline performed in a project, such as a failed check, a fix round, or the operator stepping in. |
| Baseline | What every project runs on: the harness, and Crew's agent definitions, stage prompts, guides, templates, and pipeline defaults. It belongs to Crew and is versioned with the release. |
| Agent definition, stage prompt | The two files that make an agent's instructions: who it is, and what its stage asks of it. |
| Override | A project's own change on top of the baseline. It stays in that project. |
| Improvement | A change to the baseline, with its evidence and the metric that will show its effect. |
| Memory, bug, feedback, eval, analytics | Five kinds of knowledge each project keeps. Documentation is the sixth. |
| Release, version | One numbered state of all of Crew, built once and tested: the server, the UI, the updater, the runtime image with the CLIs and the harness, the baseline, and the database's schema. |
| Gate | The tests a release must pass on its built images before it is announced. |
| Updater | A small program in its own container that installs a release: it checks the new version beside the running one, switches, watches for 10 minutes, and switches back if needed. The last 5 versions are kept by default (`updates.keep_versions`). |
| Protected part | Since 0.29 this list matters only when `updates.auto_install` is set to `unprotected`, because every release that changes code waits. A part that keeps agents inside their limits or keeps the way back open: the updater, the notice and hold step, the firewall, the roll back path, the credential broker, the rules of the git endpoint, the rules of personal tokens, the database container with its roles, every schema step and the code that applies them (before design 0.58, only a schema change the previous version cannot read), the improver's own definition, and since 0.27 also the ingress and sign-in, sessions, the choice of a sandbox's network and credentials and, since design 0.59, of the image a run starts from, the removal of secrets, and, since design 0.98, the code that holds the operator's logins or hands them on (the server's own use of GitHub, Setup, the mail account that sends notifications, and the test of a login that comes back from a run, with the rule for the paths in a sandbox's home that it goes through), the secrets of hosted projects, the gate and the build with their scripts and, since design 0.59, the code of their sandboxes, the settings of `crew-gate`, and the press of Run the live gate with the diff it shows, and the settings for approvals, network, credentials, stages, the model and effort of the agents, and the numbers by which Crew chooses a model, with, since design 0.59, the code that chooses by them, and, since design 1.02, the eval's reading of which runs measured their case and the comparison that decides an override, an improvement and a release's evals. |
| Machine setup | What the host setup installed on the machine: Docker, Kata, the firewall. It has a number, and a release names the number it needs. |
| Backup | One tar.gz file per day on the machine: a dump of Crew's database, the repositories of projects with no remote, and the data of hosted projects. The events of each month that has ended are in a file of their own. |
| Primary host | The dedicated Linux host Crew runs on (originally benchmarked on a bare-metal host with 12 cores / 16 threads, 62 GiB RAM, and a 1 TB SSD; Setup sizes capacity dynamically per machine). |
| Second choice, next choice | The next entry on an agent's list that can run. A run takes it when the group of its first model is at its limit, or its CLI is signed out. |
| Bracket | Models of one kind, such as pro or flash, across the limit groups, set from the names the CLI lists. Work moves within its bracket. |
| Agent | One role with its own model, effort, prompt, tools, and sandbox: the lead, the builder, the reviewer, and so on. |

### The app, notifications, and hosting

| Term | Meaning |
| --- | --- |
| The app | Crew's web page. There is no other app. |
| Notification | A mail when something needs the operator or has happened. Web push from the app comes later, with the phone. |
| News | A notification about something that happened and needs no answer. |
| Crew MCP | Crew's own MCP server. Agents use it to ask about the project, to create tickets and subtasks, to report bugs, to record decisions, to write their records, and to read the documentation. |
| Personal token | A token the operator creates so that their own tools can use the Crew MCP. With "may answer" switched on, it can also approve or discard a new feature and change a decision. |
| Ingress | The front door on the machine. It serves the app and the hosted projects, and asks for the Google sign-in first. |
| Hosted project | A built project that runs on Crew's machine in a sandbox of its own and has an address. |
| Deploy | One build of a project that was started, checked, and given the address. |
| styrman-crew, styrman-harness, `sh-agent` | The operator's earlier system, its harness, and the installer of that harness. Crew copies parts of the first two and does not use the third. |

## Design principles

- **The database is the brain.** Everything Crew saves is in one database, in its own container: plans, tickets, records, settings, conversations, and trajectories, with the history of every change.
- **Git holds the code.** A project is a git repository, on the machine or on GitHub, with its code, its documentation, and its own skills.
- **A ticket only moves to the right.** It moves on, or it is closed. Work that fails is fixed where it stands.
- **One kind of card.** A project has tickets. A ticket has subtasks, which are the work done inside it, not cards of their own.
- **Checks decide, not agents.** An agent asks for a move. Crew runs the checks and moves the ticket only if they pass.
- **Pull, with limits.** Agents pull work when a stage has room.
- **Everything is linked to its ticket.** What an agent produces can be traced to the ticket it worked on.
- **Nothing is hidden.** The prompt an agent was given is as visible as what it produced.
- **Another agent decides.** A ticket an agent creates is analysed by a different agent before it is built.
- **Agents do not wait.** An agent decides, records the decision, and carries on. Limits bound the work, not questions.
- **Projects are isolated, and so are runs.** An agent sees one project. A run has its own sandbox with its own kernel, files, tools, and network.
- **Signals go up, the baseline comes down.** What Crew learns reaches projects only as a tested, general change to what they all run on.
- **Copy the parts that can be reused.** Eight parts come from styrman-crew, and the harness from styrman-harness. Crew's server and UI are its own.
- **Every version can be undone.** Everything that changes together has one version number, and the version before is one click away.
- **Settings are data.** Every setting is a row in the database, so it can be changed without changing code, from a file that is imported and later from the UI.

## Decisions

What the operator has decided, one line each, by topic.

**The system**

- The name is Crew. Its Apache-2.0 source repository hosts the server, UI, harness, and baseline.
- Crew replaces styrman-crew. It is rebuilt as a new project, so the rules of the old repositories do not bind it.
- It is not a full copy. Eight parts are copied from styrman-crew: the sandbox orchestrator, the host setup, the firewall, the runtime image, the credential store and broker, the setup microVM (written again for Crew's Setup, 12.6; the copied start and stop, which nothing used, are gone since 0.84), the terminal bridge (written again for Setup and the project terminal; the copy, which nothing used, is gone since 0.84), and the ingress. The copied parts and the first runtime image are taken from the latest commits of styrman-crew and styrman-harness.
- One developer, several projects, nothing shared between them.
- One dedicated host running Debian or Ubuntu (originally benchmarked on a bare-metal host with 12 cores / 16 threads, 62 GiB of memory, and a 1 TB SSD). Any prior installation is removed first, and only Crew runs there. A virtual machine with nested virtualization also works.
- Crew is for one developer who has a GitHub account. The owner of new repositories is the person who installs Crew.
- Go for the server and TypeScript for the UI.
- Nothing watches Crew from outside. If it is down, it is down.
- Crew has all the data and gets signals from all projects, to improve the agents and the pipeline.

**Agents**

- No model API. The agent is a command-line agent, signed in inside the sandbox with the operator's subscription.
- In 0.26: only `agy`, the Antigravity CLI. In 0.30: `claude` is added back as an agent CLI, and there must be room for other terminal CLIs such as `codex` and `opencode`. For now the focus is on `agy` and `claude` only. In 0.31: other CLIs come at a later stage, and only the preparations are made now. In 0.42: `claude` is taken out, and `agy` is the only agent CLI.
- No API is used, so the CLI, `agy`, runs signed in as the operator. It has a list of models of its own, and those models are the ones Crew lists.
- The longest-lived sign-in possible is used for `agy`.
- In 0.31: Crew measures results and recommends a model, and the operator chooses. In 0.32: the choice of the model and of the agent CLI is automatic. Crew gives its recommendation, and the operator can override it with a manual choice.
- Crew improves the operator's prompts for better results: it recommends a rewrite of what the operator writes in planning. In 0.32: this is done in the planning stage, and there is a button for text the operator selected with the mouse: improve text, and review text.
- Everything runs in Docker. Crew is built for Docker only.
- Lead runs wait in a queue, and tasks have priorities.
- Every task, done or running, has a chat, where the operator can ask what happened in the task and ask questions while it runs. The operator can mark text and write a comment, which is sent to the chat for the agent to analyse.
- `agy` has several models included: Gemini, Opus, and GPT. Crew finds every model `agy` offers, and all of them can be browsed in Crew. The model and the effort (low, medium, high, and what else `agy` lists) can be changed. Crew is made of several different agents, and each has its own model.
- When one group of models is at its limit, a run moves to another model if that is possible. Otherwise it waits for more quota.
- A reviewer and a verifier use another model than the builder. Crew applies it to `judge:` checks too, and reads "another model" as a model of another family.
- The CLIs, the harness, and the run driver are in one runtime image, kept current. `sh-agent` is removed: the harness is Crew's own.
- styrman-harness is the base, with its skills and context. Anything in it may be changed or dropped. A project can have skills and context of its own, sideloaded. An agent that needs a skill asks Crew to add it.
- Setup comes before any project and is outside the projects. It holds every credential: the git identity, the git SSH key, `gh`, a GitHub App, the gcloud login and its application default credentials, and `agy`. Crew keeps them and copies into each sandbox what it may have when it starts.
- Each sandbox that works on a project gets a temporary GitHub token that can only read. The SSH key and the `gh` login stay with the server.
- Crew itself uses no GitHub token. Git goes over SSH with the one key. `gh` is signed in for what a key cannot do: creating a repository, pull requests, and release pages. Crew keeps working when it is not.
- Crew checks the login and lets the operator sign in again. `agy` is signed in with the operator's personal Google account, the way styrman-crew does it. No other way to sign in is prepared.
- A sandbox uses the operator's own login of `agy` and the git identity, and no gcloud. Crew is an extension of the developer who runs it.
- In 0.29: Crew was planned to become public on GitHub later; in 0.31: public release was deferred; in 2.01: Crew's source repository is open-sourced under Apache-2.0 (`LICENSE`, `THIRD_PARTY.md`). Its builds remain reproducible, the repository holds no secret, and compiled runtime images remain local or in each operator's own private registry (`16.12`).
- Install, uninstall, and update are features that must work. So is bootstrapping: installing Docker and making sure everything is there to deploy. What the installer needs is gathered first, in a `.env` file or when the installer starts.
- The build starts from a zip with all the documents, the plans, and pictures of the simulation, so that the right thing is built.
- Crew is built directly on the bare-metal machine that will host it.
- The board has one level of cards, and each ticket merges on its own. The source repository ships under Apache-2.0 (`LICENSE`).
- A release that changes only prompts and harness text installs by itself. A release that changes code waits for Install.
- Lead slots limit the lead runs: 2 for each agent CLI by default, and this can be changed. Subagents are not under this limit.
- Agents start work on their own once the project has its OK.
- Agents write memory directly, and give feedback on each other's work.

**Conversations and trajectories**

- The operator can also just talk to an agent, with no ticket. Crew does what is asked in an ephemeral sandbox, and every conversation is saved in Crew's database.
- Headless runs of a CLI are stored and can be continued, so an agent knows what it did before. This is a must.
- Trajectory tracking is vital. Every run and every conversation has a trajectory.

**The board**

- The nine-step flow above. One board per project, with tickets as the only card.
- A ticket works as kanban. It only moves to the right, or it is cancelled and replaced by other tickets. Subtasks, and the tickets that replace one, are tagged to the ticket that had to be split.
- The stages are Planning, Backlog, Analysis, In progress, and Done, with optional stages between In progress and Done.
- A ticket an agent creates is analysed by another agent before it is built.
- Crew has its own MCP server. Agents use it to ask, and to create tickets and subtasks. The operator's own tools can use it too.
- Subtasks run in parallel where they can: at most 3 at once per ticket, and at most 6 added per ticket.
- A ticket shows its agent, model and effort, prompt, source plan, and subtasks.

**Autonomy**

- The system is autonomous from the operator as much as possible.
- Agents do not wait for the operator. An agent records its decision in Crew's database and carries on.
- A ticket is bounded by its runs' turns and time and by its fix rounds; one that cannot get through goes to the lead.
- A reviewer, a verifier, and a `judge:` check run on another model family than the builder whose work they check when that family has room; when it is at its limit or unavailable, Crew falls back to a model of the same bracket in the builder's family that has room, records the signal, and shows a notice in the inbox so the operator knows why (12.3).
- What waits for the operator: the project OK, a project limit, a sign-in, and a release that changes code or needs a newer machine setup. Since the audit of 0.27 also a ticket that changes a file that is code for the CLI or for GitHub, and an override of a role that checks.
- A new feature gives notice and goes ahead after 24 hours. Approve now, Discard, and Hold are the alternatives.
- An improvement whose evals are not worse ships in the next release with no approval of its own. Crew rolls it back if it measures worse.
- The operator's own tools may answer two things when a token is allowed to: a new feature, and a recorded decision.

**Sandboxes**

- All agents run in sandboxes, one per run, with full access inside.
- Each sandbox is a Docker container that is a microVM, using Kata with Cloud Hypervisor.
- A network mode per project says what a sandbox may reach: Internet, Model only, or LAN.
- Terminals are for diagnostics and troubleshooting, with an edit mode: one for the whole project, and a jump into the sandbox where an agent is working.

**Git**

- The database is the brain of Crew. Everything that is saved stays in it, and it runs in its own container on the machine.
- Git is a big part of Crew, local or GitHub. A git repository per project is mandatory, and it holds the code. Crew can create it on GitHub when `gh` is signed in.
- Trajectories are saved, and ticket summaries go into Crew's database. Trajectories are kept until feedback has been run on them and evals have been made, so for now they are kept for good.
- Backups stay on the machine for now.
- Every project has documentation, stored with the project and shown in the app.

**Versions**

- Crew updates itself from inside the app. Versions are clear, so it can be rolled back. The last 5 are kept by default (`updates.keep_versions`).
- The updater is designed anew. No signing keys, but real releases.
- Crew gives notice that an update is there. After 24 hours a release that changes only prompts and harness text installs by itself, with Install now and Hold as the alternatives. A release that changes code waits for Install. A new version is watched for 10 minutes and switched back if it turns unhealthy.
- Releases are cut per operator installation: a tag and release notes on the operator's `CREW_SOURCE_REPO`, with built runtime images kept locally (`crew-ctl install --build` / `build`) or in that operator's own private registry (`16.9`, `16.12`).
- Backups are kept on the machine as tar.gz files to start with.
- The next new sandbox uses a new version's image by itself.

**The app and notifications**

- Mail first. The phone is skipped for now, and no mobile app is built. Mail goes through the operator's Google account with an app password.
- There is no Google Workspace today, so Google Chat is off.
- A mail that needs an answer presents the alternatives, and two presses answer it.
- The top bar says Sandboxes, not microVMs, and has the version by the logo. The limits of the subscriptions are not on the top bar or the front page.
- Settings are edited in the UI later.

**Hosting**

- Hosting is built into the app. A built project is made into a container and hosted from the machine with the Host locally button.
- Hosted projects can be reached from the internet. They use the sign-in of the ingress, as in styrman-crew: a Google sign-in against a list of mail addresses. The default is only the admin email. Listed addresses, any Google account, and public are the other choices.
- A hosted project may reach the internet from inside. The last 3 images are kept.
- Agents cannot deploy.
- A preview per ticket is not needed now.

## Requirements

Numbers are never reused. A number that is no longer used is listed at the end.

### Foundation

- **R125. No model API, and agent CLIs.** The agent is a command-line agent, started without a terminal inside the sandbox, using the operator's subscription. Crew runs one: `agy`, the Antigravity CLI. The agent loop and the core tools (read, edit, search, shell) come from the CLI. *(Design 12.1)*
- **R5. A harness adapter.** A thin layer sits between Crew and the agent CLIs, so the orchestrator, the monitor, and the UI can be tested with a fake that replays recorded runs. `agy` has one adapter behind it, and nothing outside the adapter names the CLI. *(Design 12.5)*
- **R129. A run driver.** Crew starts the CLI with the saved prompt, follows everything it does as events, can stop it, and can continue the same conversation later. *(Design 12.4)*
- **R185. Conversations are stored and continued.** Every run belongs to a conversation. Crew stores every turn, every event, and the CLI's own files of the conversation in its database. A conversation is continued over the project sandbox's `agy acp` server (`session/load`) or with a new call in the same sandbox, or in a new or restored sandbox after Crew has put the CLI's files back, or rebuilt from the turns when neither is possible. The same role on the same ticket continues one conversation, so a builder that comes back for a fix knows what it did. A reviewer, a tester, or a verifier starts a new one every time. *(Design 12.10)*
- **R132. Crew owns the harness.** styrman-harness is the base, and anything in it may be changed or dropped. It is copied into a folder in Crew's source repository and adapted. It changes through Crew's own tickets and improvements. `sh-agent` is not used: memory, search, and session metrics are Crew's own. *(Design 17.3)*
- **R180. Skills and context per project.** A project can have skills, context, and rules of its own, beside the base. They live in the project's repository, in `AGENTS.md` and under `.agents/`, and are sideloaded and live-synced into the project's persistent sandbox home (`~/.gemini/config/skills/`) and each run's worktree when it starts or when edited. No image is rebuilt for skills or context. *(Design 12.9)*
- **R181. An agent can ask for a skill.** An agent that misses a skill asks Crew for it and carries on. Crew creates a ticket that finds or writes the skill. It is reviewed as instructions, a skill from outside is pinned with its source and licence, and when the ticket is Done every later run of the project has the skill. A skill that several projects added can be proposed for the base. *(Design 12.9)*
- **R6. Pinned versions.** The CLIs, the harness, and the tools are in one runtime image, and a release names the exact version of each. No CLI updates itself inside a sandbox. *(Design 12.8)*
- **R133. No cost, and no limit in dollars.** No run is billed, and Crew shows no cost and sets no limit in money. The work is bounded by the subscription's five-hour and weekly windows (R130), a run's turns and time, a ticket's fix rounds (R162), and the share of tickets agents may add to a project (R32). *(Design 12.7)*
- **R122. Copy what can be reused from styrman-crew.** Eight parts are copied into Crew's source repository and renamed, and are not built again: the sandbox orchestrator, the host setup, the firewall, the runtime image, the credential store and broker, the setup microVM (written again for Crew's Setup, 12.6; the copied start and stop, which nothing used, are gone since 0.84), the terminal bridge (written again for Setup and the project terminal; the copy, which nothing used, is gone since 0.84), and the ingress. The rest of Crew is its own. *(Design 17.2)*
- **R192. Go and TypeScript.** Crew's server is written in Go and its web UI in TypeScript. *(Design 17.1)*
- **R193. Crew replaces styrman-crew.** Crew is an independent repository and installation. Any prior styrman-crew installation on a target host is removed first so only Crew runs there. The rules of the old repositories do not bind Crew. *(Design 17.6)*
- **R177. What belongs to the installation.** Crew's code, harness, and baseline are in the source repository, which agents work on. This installation's settings, plan drafts, and improvements with their evidence are in the database, which no sandbox can reach. *(Design 10.3)*

### Planning and creating a project

- **R7. Plan drafts.** The operator can start a plan with no project and no repository. A planning agent helps write it: it asks questions, researches, and drafts. Before the first message reaches the planning agent, Crew proposes a better wording of it (R206). The draft is kept in the database. *(Design 7.2)*
- **R8. What a plan holds.** The goal, the why, the scope, the approach, the parts of the work, the stages the project will use, the risks, and what is out of scope. *(Design 6.3)*
- **R9. Only the operator creates a project.** From a plan draft, with a name and a repository choice. Agents cannot create projects. *(Design 7.2)*
- **R10. Crew creates the repository.** On GitHub when `gh` is signed in, with a name the operator chooses, private. Without `gh` the operator names an empty repository on GitHub, and Crew fills it. Or as a local repository on the machine. Or from an existing repository. *(Design 6.6)*
- **R11. GitHub over SSH, with no token.** All git traffic between Crew and GitHub uses one SSH key that is made in Setup. The operator adds its public half to the GitHub account once. `gh` is signed in in Setup, for what a key cannot do: creating a repository, pull requests, and release pages. Crew keeps working when it is not. The key and the `gh` login are kept in the database, encrypted. They stay with the server, and never enter a prompt, a log, a trajectory, or a sandbox that runs an agent, a check, or a project. They are made in the setup microVM, where nothing else runs, and the server collects them from there. *(Design 6.6)*
- **R182. The owner.** New repositories on GitHub go under the account of the person who installed Crew, which is the account the SSH key belongs to. Crew is for one developer, with a GitHub account and one machine: bare metal, or a virtual machine with nested virtualization, running Debian or Ubuntu. *(Design 6.6)*
- **R12. The original plan is added to the project.** It is saved in the database with the project and never changed afterwards. Later changes are amendments beside it. *(Design 7.2)*
- **R13. Breakdown.** The lead analyses the original plan, and the existing code if there is any, and splits the plan into tickets. Each ticket has a why, a what, a goal, a first version of its checks, dependencies, and the part of the plan it belongs to. The tickets wait in Planning. *(Design 7.2)*
- **R14. Project OK.** The operator reads the plan and the breakdown, can comment or change them, and says OK. The OK records the exact versions it covers, with the stages. Nothing is built before it. *(Design 7.2)*
- **R15. Runs until done.** After the OK, agents work the tickets through the stages until every ticket is Done, cancelled, skipped, or discarded (R162). The project is then Done, with a final report that lists what was built, what was skipped or cancelled and why, and what was learned. A ticket added after that, by the operator or by the lead in housekeeping, makes the project run again, and a new final report follows. *(Design 7.2)*
- **R166. After Done.** A project that is Done can still be read, and a hosted project keeps running. A ticket the operator adds opens it again. An amendment to the plan needs the OK. The operator can protect a project (`Protected`, off by default, and always on for Crew's own project) so it is neither archived nor deleted while protected, archive a project, and delete one after typing its name. Deleting removes it from the machine and leaves the repository on GitHub. *(Design 7.13)*

### Tickets and the board

- **R17. One board per project.** With the fixed and the optional stages of R96. *(Design 7.4)*
- **R179. A ticket only moves to the right.** A ticket never goes back a stage. It moves on, or it is closed. A ticket that fails a stage stays in that stage: the findings become a fix subtask, and the stage checks again. A skipped ticket, and a new feature during its notice, stay in Analysis. A ticket that cannot get through is cancelled and replaced by other tickets (R162). *(Design 7.4)*
- **R96. Fixed and optional stages.** Planning, Backlog, Analysis, In progress, and Done are on every board. Review, Test, E2E, and Verification are optional and sit between In progress and Done. Review and Test are on by default. *(Design 7.4)*
- **R97. Chosen with the plan.** The planning agent asks which checks the project needs. The plan lists the stages, and the project OK approves them. They can be changed later, for tickets that have not reached them yet. *(Design 7.4)*
- **R98. Per ticket.** Analysis can mark an optional stage as not needed for one ticket, with a reason, such as E2E for a change to documentation only. Review cannot be skipped for a ticket that an agent created, or for a ticket that the assistant drafted in a conversation or in a chat. *(Design 7.4)*
- **R101. Custom stages.** A project can add a stage of its own, with an agent definition, a prompt, and an exit check. *(Design 7.4)*
- **R99. E2E.** An agent starts the whole application in a fresh sandbox and drives it through the user flows the ticket touches. The logs and screenshots are stored in the ticket. *(Design 7.4)*
- **R100. Verification.** An agent that did not build the ticket checks the result against the ticket's goal and the source plan, and says whether it does what was asked. *(Design 7.4)*
- **R18. Why, what, goal.** Every ticket has all three, plus at least one check. A check is a command that Crew runs, the verdict of a review or a verification, or a criterion that a separate judge model decides. Crew refuses to let a ticket leave Analysis without them. *(Design 7.1)*
- **R163. Who can change a check.** A ticket's checks are set by the breakdown and settled by Analysis. After Analysis nothing but a new analysis changes them. A builder cannot change them, and Crew reads them from the database, never from the ticket's branch. The reviewer is shown every test file a change touches or deletes. *(Design 7.1)*
- **R19. Autonomous progress.** Agents say when their work on a ticket is ready, and Crew moves the ticket. Crew runs the stage's exit checks and applies the move only if they pass. *(Design 11.3)*
- **R20. Independent checks.** The agent that reviews, tests, or verifies a ticket is never the agent that built it, and works in a fresh worktree or check sandbox with a new conversation and a reset environment (`PrepareRun`). A reviewer and a verifier also run on another model than the builder (R191). What decides a stage is in R195. *(Design 11.1)*
- **R21. Work-in-progress limits.** Each stage has a limit, and agents pull new work only when there is room. *(Design 7.4)*
- **R22. Merging.** A ticket that passes its last stage is merged into `main` by Crew, one at a time, after its checks pass on top of the current `main`. *(Design 7.10)*
- **R178. A check that fails only sometimes.** A command check that fails on the merge candidate is run once more. If it then passes, the merge goes ahead and the check is recorded as flaky. *(Design 7.10)*
- **R85. Every piece of work is a ticket.** A ticket has a number made of the project's key and a counter, such as SF-42. It has a kind (feature, fix, chore, or docs), a priority, a reporter, the agents working on it, and a status, which is its stage. A ticket that adds a skill is a chore that is marked as a skill ticket. *(Design 7.12)*
- **R86. Links between tickets.** Depends on, created from, duplicates, fixes a bug, and replaces. An agent can see what a ticket waits for and what waits for it. A subtask is tagged to its ticket, and a replacement to the ticket it replaces. *(Design 7.12)*
- **R87. The ticket number is in git.** The branch, every commit, and the pull request of a ticket carry its number. *(Design 7.12)*
- **R88. Pull requests.** When the project has a GitHub remote and `gh` is signed in, each ticket gets a pull request. Crew opens it when the work leaves In progress and posts the review and the check results on it. When the last stage passes, Crew merges on the machine, pushes `main`, and marks the pull request as merged. Otherwise the ticket is merged with a merge commit, which Crew pushes. *(Design 7.12)*
- **R89. Ticket history.** Every change to a ticket is in its history: stage moves, edits, comments, subtasks, and runs, each with who and when. *(Design 7.12)*

### The Crew MCP, analysis, and deciding

- **R25. Crew's own MCP server.** Every agent run is connected to it. It is limited to the project the agent runs in. *(Design 7.5)*
- **R26. Asking.** An agent can ask it for the plan, the tickets and their state, a ticket's record, the documentation, the recorded decisions, and the project's memory, bugs, feedback, eval results, and analytics. *(Design 7.5)*
- **R27. Creating tickets.** Any agent can create a ticket in its project when it sees fit. The ticket goes to the backlog. It needs a title, what the agent saw, and why it thinks the ticket is needed. *(Design 7.5)*
- **R90. Agents use the ticket system.** Through the Crew MCP, an agent reads, searches, creates, and links tickets, and adds notes and replies to the one it works on. *(Design 7.12)*
- **R153. The Crew MCP is visible.** The operator can see the tools agents have, which role may call each, and every call with the run that made it and what came of it. *(Design 7.5)*
- **R28. Analysis.** An agent analyses every ticket before it is built. It is never the agent that created the ticket: a ticket the lead created, which includes every ticket of the breakdown, is analysed by a reviewer, or by a second lead run when Review is off. *(Design 7.6)*
- **R29. What analysis does.** It sets or confirms the why, the what, the goal, and the checks against the current code, writes a short approach, and then decides. Approve: the ticket is ready for In progress. Skip: it stays in Analysis with a reason and a condition for looking at it again. Discard: it is closed with a reason. New feature: R30. *(Design 7.6)*
- **R30. New features give notice.** When a ticket adds something the approved plan does not cover, the analysing agent marks it as a new feature and the operator is told. It stays in Analysis. After 24 hours it goes ahead by itself. Until then the operator can approve it at once, discard it, or hold it. A project can be set to wait for the operator instead. No default does. *(Design 7.6)*
- **R31. The operator's tickets.** A ticket the operator adds is already approved. Analysis only completes its why, what, goal, and checks. *(Design 7.6)*
- **R160. Agents decide and record.** An agent does not ask the operator and wait. When it meets a choice the plan does not settle, it decides. Crew records the question, the alternatives, the choice, and the reason in its database, and the run carries on. The choice also becomes a memory entry, so later runs follow it. The operator is told, can see every such decision per project, and can change one. A change becomes a ticket. *(Design 7.5)*
- **R156. The MCP for the operator's own tools.** With a personal token the operator's own tools can list the projects the token names, read them, read the inbox, create tickets, and comment. A token can also be allowed to approve or discard a new feature and to change a decision an agent recorded. That is off until the operator switches it on. No token can give a project its OK, install a release, deploy, change settings, or read secrets. *(Design 11.7)*
- **R168. Rules for a personal token.** A personal token runs out after 90 days unless renewed, is limited in calls per minute, is stored only as a hash, shows when it was last used, and can be revoked at any time. It never works from a sandbox. *(Design 11.7)*

### Subtasks

- **R91. Subtasks.** The work inside a ticket is recorded as subtasks. A subtask that an agent works is done by one run, or by a second one when the first stopped. A ticket gets one each time it enters a stage that has an agent. The lead in Analysis and the builder can create more (R151), and an agent can start helpers. Crew adds subtasks of its own for a join, a merge, and the summary. A subtask is queued, running, waiting, done, failed, or cancelled. *(Design 7.11)*
- **R92. Steps.** An agent lists the steps of its subtask and ticks them off as it works. *(Design 7.11)*
- **R151. Agents create subtasks.** Through the Crew MCP, the lead while it analyses a ticket, and the builder, can create more subtasks on that ticket. Each gets its own run in its own sandbox. They stay inside the ticket's what and goal. At most 6 are added per ticket. *(Design 7.5)*
- **R155. Subtasks in parallel.** Subtasks of one ticket that do not depend on each other run at the same time, at most 3 at once, each on its own branch `sub/<ticket>.<n>` and in its own sandbox. Crew checks each against its own check, joins them into the ticket's branch, and then runs the ticket's checks on the result. Work that does not join becomes one more subtask. Later stages see the whole ticket once. *(Design 7.11)*
- **R93. Saved prompts.** The exact prompt of every subtask is saved, with the parts it was built from and their versions: the stage prompt, the agent definition, the ticket, the source plan, the memory, and the comments. *(Design 8.6)*

### Limits, starting work, and throttling

- **R23. Limits.** A run is stopped at its own limits: its turns and its time. A ticket is bounded by its fix rounds, and one that cannot get through goes to the lead (R162). A project pauses when agents added more than their share of tickets (R32). No limit is in dollars (R133). *(Design 7.7)*
- **R162. A ticket that cannot get through.** A ticket whose run stopped twice, or that needed 3 fix rounds, goes to the lead again. The lead tries one new approach with two more rounds. After that it cancels the ticket and creates the tickets that replace it, tagged to it. Nobody is asked. Tickets that depended on it depend on the replacements. A cancelled ticket does not stop the project from being Done, and is listed in the final report. A replacement is not replaced a second time. *(Design 7.7)*
- **R32. A scope guard.** When the tickets agents added, and Analysis approved, pass a share of the tickets of the approved plan, the project pauses and the operator is asked. A replacement of a cancelled ticket, and a ticket the operator made, do not count. *(Design 7.7)*
- **R50. Automatic start.** Once a project has its OK, agents pull tickets on their own. *(Design 7.9)*
- **R51. Concurrent run slots.** `max_concurrent_runs` limits how many agent runs work at once across all projects (default 8 in `crew.yaml`, halved near a subscription group's limit); an `idle` run waiting for the operator or for a check sandbox holds no slot, and interactive `operator_waiting` keyboard turns have reserved capacity up to `conversations.max_sandboxes`. *(Design 7.9)*
- **R52. Active-ticket concurrency, and stage limits.** Active tickets are bounded per project (`limits.max_active_tickets`, default 4) and across the machine (`max_active_tickets`, default 12, halved near a subscription group's limit), alongside each stage's WIP limit (`Analysis` 4, `In progress` 6, `Review` 4, `Test` 4, `E2E` 1, `Verification` 1), and a run starts only when the machine has room for its sandbox. *(Design 7.9)*
- **R53. Priority order for starting work and agent runs.** Work starts in priority order: work the operator is waiting on first (with interactive keyboard reservation), then resuming `continues` runs whose check sandbox completed, then tickets in the stage furthest to the right, then the ticket with the higher operator priority across projects, then the project served least recently, then the ticket's own priority and wait time within a project, and then background reports, housekeeping, retrospectives, evals, and the improver. A ticket has one of four priorities: urgent, high, normal, or low. It starts as normal, the curator and the operator can change it, and an agent cannot set the priority of its own ticket. *(Design 7.9)*
- **R71. Room on the machine, and per-project microVM sizing.** Each project's warm microVM (`crew-proj-<id>`) is sized from the project's `sandbox.{cpus, memory_gb, disk_gb}` settings (default 6 virtual CPUs, 8 GiB memory, 20 GiB disk, with optional per-project `suspend_after_minutes`, `stop_after_minutes`, and `max_snapshots` overrides in `board.yaml`), counted once in machine capacity (`running` = full CPUs and memory, `paused` = `0` CPUs and full memory, `stopped` = `0` CPUs and `0` memory) alongside ephemeral check, build, terminal, conversation, and hosted sandboxes. A run starts only when there is enough memory and CPU allowance for its sandbox. *(Design 7.9, 13.3, 13.10)*
- **R164. Shares of a subscription.** When several projects have runs waiting for the same group of models, the project that was served least recently starts next, so one project cannot use a whole subscription window. *(Design 7.9)*
- **R165. Pause all, and Stop all.** The operator can stop new runs from starting on the whole machine, or end every run at once, and resume later. While Crew is paused, no release installs and no new feature goes ahead. No agent can pause or resume. *(Design 7.9)*

### Agents

- **R46. Project stewardship (`curator`) and planning (`planner`).** The planning agent (`planner`) develops plan drafts interactively with the operator and writes the ticket breakdown on Gemini (`pro`, falling back to `flash`). The curator (`curator`) analyses tickets that need LLM analysis (alongside `reviewer` for curator-created tickets, while ready `OriginPlan` and `OriginOperator` tickets verify their command checks on `main` directly), looks again at a ticket that cannot get through, writes the final report, and does housekeeping and retrospectives. It does not change code. *(Design 7.6, 11.1, 12.3)*
- **R47. Subagents.** Builders, reviewers, testers, E2E testers, and verifiers. Each run has one ticket, its own context, and its own sandbox. *(Design 11.1)*
- **R3. A project microVM with one `agy acp` server and isolated per-run worktrees.** Each active project runs in its own Kata microVM (`crew-proj-<id>`) with one `agy acp --stdio --process-per-session` server, cross-process flock-protected per-run git worktrees (`/home/agent/worktrees/<run-id>`), and per-run credentials/control keys under `/home/agent/.crew/runs/<run-id>/`; the project microVM starts when the project has work, suspends (`ContainerPause`, `0% CPU`) on short idle, and stops to `0 MB` RAM on persistent `<CREW_DATA_DIR>/projects/<id>/home` (with snapshots) when the project is idle, while command checks that decide a move or merge run in a separate check microVM with no agent (R195). *(Design 13.3)*
- **R48. Stage-specific prompts.** Each stage has its own instructions and exit checks. They come from Crew's baseline. *(Design 10.3)*
- **R49. An orchestrator.** It derives the work to do from the board, starts and stops runs, and handles limits and retries. *(Design 15.2)*
- **R127. The CLI, the model, and the effort for each run.** Each agent has a list of entries, each a CLI with a model, and an effort, which install fills from the baseline. A model belongs to a CLI, so the model names the CLI. Which entry runs is chosen by Crew for the agents whose work it can measure (R205), or set by the operator for the machine, for a role in a project, or as a pin on a ticket. The operator's choice always comes first. No improvement and no release changes it. What ran, and where it came from, is recorded on the subtask, and results are measured per role, CLI, model, and effort. *(Design 12.3)*
- **R190. Crew reads the models from the CLIs, classifies and ranks them automatically, and they can be browsed.** Crew lists the models that each CLI lists itself: what `agy models` prints. It reads them after every sign-in, once a day, when the version of a CLI changes, and when the operator asks, and it keeps no list of its own in the code. When a list cannot be read, Crew keeps the last one it read and says so. The operator can add a name. The list is stored in the database with each model, its CLI, its efforts, its family, its `first_seen` time, and its limit group. On every read and at startup, Crew deterministically classifies offered models into `agents.brackets` (`pro`, `flash`, and `oss`, preserving operator bracket overrides and raising an inbox notice from `min(first_seen)` only when a model cannot be classified) and ranks automatic roles within each `(family, bracket)` by `(major.minor version DESC, first_seen DESC)`, respecting operator removals, `Set by you`, and rollback blocklists (R205). The app has a screen where every model can be browsed, with the agents that use it and its results, and where a model and an effort are given to an agent. Every choice of a model or an effort is made from this list. When a model is no longer offered, a pin or a project setting that names it is ignored and the machine's setting applies. *(Design 12.3)*
- **R191. A check uses another model than the build.** A reviewer, a verifier, and a `judge:` check run on a model of another family than the builder whose work they check, whichever CLI offers it. When no CLI of the installation offers another family, the check runs anyway and the run says that the rule could not be met. The rule is a setting that is on, and a project can switch it off. *(Design 12.3)*
- **R130. Quota.** Crew knows when a group of models of a CLI has reached its subscription limit or is close to it, and when the limit resets, and shows this to the operator. Before work goes to a model, Crew reads its group's quota. Every model the CLI lists may be used when chosen by the operator, each classified into a bracket by its kind (`pro`, `flash`, or `oss`) from the names and efforts the CLI lists and never from a hardcoded list in the code (R190). Crew defines automatic model eligibility by `(family, bracket)`: Gemini models (`pro` and `flash`) are used for all non-check roles (`curator`, `planner` for both interactive plan drafts and breakdowns, `builder`, `tester`, `e2e`, `summary`, `prompt`, `assistant`, and `improver`), with Gemini `flash` preferred for fast roles and smaller builder tasks with distinct rules (`part` subtasks and `docs`/`chore` tickets); Anthropic (`claude`) `pro` models are used for code-ticket `reviewer`, `verifier`, and stage checks, and Anthropic `flash` models are used for `docs`/`chore` ticket reviews and `judge:` checks, while any model may run when explicitly chosen by the operator (a ticket pin, a project setting, or an agent set by hand). When the Anthropic (`3p`) group is at its limit or close to it, a reviewer, a verifier, or a `judge:` check falls back to a Gemini model of the same bracket that has room and shows a notice in the inbox so the operator knows why. Non-check roles (including `planner`) stay on Gemini (`pro` and `flash`) and wait when the Gemini group is at its limit rather than moving onto Claude. An operator pin, a project's setting, and an agent set by hand are never moved. Runs on the other groups carry on. When nothing can run, every project waits. The operator can switch moving off. *(Design 12.3, 12.7)*

### Operator

- **R158. Only brakes wait for the operator.** Besides the project OK, the operator is needed only when a project limit is reached, when a sign-in is needed, and for a release that changes code or needs a newer machine setup. Unless a project is set otherwise, nothing on a single ticket waits for the operator, with one exception: a ticket that changes a file that is code for the CLI or for GitHub (R197). *(Design 7.3)*
- **R16. Operator control.** The operator can pause, resume, stop, and archive a project, change priorities, put a ticket on hold, and stop or send a message to a run, at any time. *(Design 15.4)*
- **R54. A web UI.** Plan drafts, project creation, the project OK, the board, a ticket view, an inbox, documentation, and a view of running agents. Agents are not driven from a terminal. The UI has terminals for diagnostics (R140). The top bar shows the version beside the logo, says Sandboxes, and shows no subscription limit, and neither does the front page. *(Design 20.1)*
- **R94. The ticket view.** Clicking a ticket shows the agents that are running, their model and effort with where they came from, their sandbox, and their live log. It shows the prompt that is running, the source plan the ticket comes from, and every subtask the ticket is using or has used, each with its agent, model, effort, and prompt. It also shows the why, what, goal, and checks, the stage history, the branch and commits, the records, the linked tickets, the decisions agents recorded, and the summary. *(Design 20.2)*
- **R95. An activity feed.** A live list of what agents are doing, per project and across all projects. *(Design 20.1)*
- **R55. An inbox.** One list: projects waiting for their OK, projects paused at a limit, a CLI signed out, a release that waits because it changes code or needs a newer machine setup, a ticket that changes a file that is code for the CLI or for GitHub, an override of a role that checks, new features with the time they go ahead, a release with the time it installs, and what Crew could not do by itself. *(Design 7.8)*
- **R56. A comment loop.** The operator can comment on the plan and on a ticket. A comment on the plan wakes the lead. A comment on a ticket with a run that is going is sent into that run. On a ticket with no run it waits, and is in the prompt of the next run on that ticket. A ticket that is Done or closed takes no comment for an agent. A comment on marked text goes to the chat of the ticket (R208). *(Design 11.6)*
- **R184. Conversations.** The operator can talk to an agent with no ticket, in a normal conversation outside a project (with read-only access to the Crew map and every non-archived project's records and repository) or about one project. The agent works in a sandbox of its own, which is removed when the operator stops or saves and comes back with the next message. It can read through the Crew MCP and do anything in its sandbox. It cannot change `main` or the board. What should land can be moved into a project's backlog, converted into a plan for a new project, saved, or discarded. Every turn is saved in Crew's database. *(Design 11.8)*
- **R173. Sessions.** A sign-in to Crew lasts 30 days on a device. A fresh sign-in is asked for before the actions that could hand over the machine or the credentials: a terminal, a secret, a personal token, how releases install, and installing or rolling back a release. Every request that changes something must come from Crew's own pages. What agents wrote is shown as text and cannot act as Crew's page. The operator can sign any device out. *(Design 20.3)*

### Monitoring, trajectories, and summaries

- **R33. Every ticket is monitored.** Its stage, the agents on it, time in the stage against what is usual, and check rounds are visible live. *(Design 8.1)*
- **R34. A watchdog.** Runs that are stuck, silent, or repeating a failing action are flagged. A run is stopped at its own limits. *(Design 8.2)*
- **R35. Trajectories.** Every run and every conversation has a trajectory: every event, in order, with its time: messages, tool calls with their input and result, and usage. Crew writes it to the database while the run happens, from what the CLI prints. Secrets are removed. It cannot be switched off, and a run whose events cannot be written is stopped. A trajectory is kept until feedback has been run on it and eval cases have been made from it, which for now means for good, and it shows whether that was done. A trajectory can be replayed in the UI. *(Design 8.3)*
- **R186. A trajectory can be found.** Every subtask, run, and conversation shows its trajectory. Every project has a list of all its trajectories, with the agent, the model, the events, the turns, the tokens, and the outcome, which can be filtered and searched. Summaries, signals, eval cases, and improvements link to the trajectories they came from. *(Design 8.3)*
- **R36. Ticket summaries.** When a ticket ends, as Done or closed, a summary is written by a separate run, not by the agents that did the work. It holds what was done, the result of each check, problems and rework, files changed, commits, time and tokens per role, the subtasks and runs with links to their trajectories, and the tickets and memory it led to. *(Design 8.4)*
- **R37. Summaries in Crew's database.** Each summary is stored in Crew's database, where it can be searched and compared across tickets. *(Design 8.4)*
- **R38. Run records.** Every run leaves a record with its outcome, check results, rounds, tokens, and duration. *(Design 6.3)*

### The database

- **R187. The database is the brain.** Everything Crew saves is in its database: projects, plans, tickets, subtasks, records, summaries, decisions, knowledge, settings, conversations, trajectories, prompts, signals, improvements, releases, the queue, and credentials. Nothing that must survive is only in a file or only in the server's memory. After a restart the server reads the database and goes on. Git holds the code, the documentation, and a project's own skills. Docker images, the data of hosted projects, and the backup files are files. *(Design 6.5)*
- **R188. The database is its own container.** The database is PostgreSQL in its own container on the machine, part of the Crew release. Only the server and the updater can reach it. No sandbox has a route to it, and an agent reaches its project's data only through the Crew MCP. The operator can look inside with a role that can only read. *(Design 15.3)*

### Git

- **R39. A repository is mandatory.** Every project has exactly one git repository. *(Design 6.6)*
- **R40. Records are rows.** A ticket, each of its records, and each stage move is a row in the database. A record that was written is not changed afterwards, except to mark a comment resolved. *(Design 6.3)*
- **R41. A branch per ticket.** Each run works on its own clone inside its own sandbox. A parallel subtask works on its own branch `sub/<ticket>.<n>`. *(Design 6.1)*
- **R42. Crew is the only writer.** Only the server writes to the database while Crew runs, and agents change records only through the Crew MCP. The updater changes the schema and restores a snapshot during an update. Only Crew merges into `main`, on the machine and never in a sandbox. What the operator pushes is taken in by Crew (R139). *(Design 6.4)*
- **R43. Every change is kept, and code is traced.** Every change to a ticket, a record, or a setting is kept in a history that is only added to: who, when, and the value before and after. Deleting a project removes its history and leaves one row that says so. One logical change is saved as a whole or not at all. Each commit names the operator or the agent role and run, so code can be traced to its ticket and to the plan. A merged ticket can be reverted from the UI. *(Design 6.4)*
- **R44. A project can be moved.** The code is the repository. The rest of a project can be written out as a folder of files and read back on another machine. Its trajectories and conversations stay behind. *(Design 6.2)*
- **R45. Remote sync.** With a remote on GitHub or another git host, Crew pushes after each change and fetches changes made outside Crew. A project with no remote works fully. *(Design 6.6)*
- **R137. Local or GitHub, and changing later.** A project works the same with a repository on the machine or on GitHub. A local project can be connected to GitHub later, and a GitHub project keeps working when GitHub cannot be reached. *(Design 6.7)*
- **R138. Git is visible in the app.** Every project has a Git screen with its branches, history, tags, diffs, comparisons, pull requests, and the state of the remote. Every ticket shows its branch, its commits by subtask, its diff, and its pull request or merge commit. *(Design 6.7)*
- **R139. The operator can use the repository directly.** Every project has a clone address, also when it is local. What the operator pushes to `main` is picked up. The ticket branches can only be changed through Crew, and records are not in the repository at all. *(Design 6.7)*
- **R140. Terminals.** The operator can open a terminal for a whole project, with a clone of the project's git repository, which takes no push in diagnostics mode, and can jump directly into the sandbox of a run. The project terminal is for diagnostics and troubleshooting. An edit mode lets the operator change and push code from it. A push to a ticket's branch is refused while an agent is running on that ticket. What happened in a terminal is kept. *(Design 13.11)*

### Sandboxes and the machine

- **R65. One machine.** The server, the database, the repositories, and all sandboxes run on one dedicated host (originally benchmarked on 12 cores / 16 threads, 62 GiB RAM, and a 1 TB SSD). Setup inspects the target machine's cores and RAM to size how many sandboxes fit at once. *(Design 13.10)*
- **R118. Docker only.** Docker is the only target. Every part of Crew is a Docker container: the server, the database, the ingress, the updater, every sandbox, and every hosted project. A sandbox is a container that Docker starts with the Kata runtime. On the machine outside a container are only: Docker with the Kata runtime, the firewall rules with their timer, the script `crew-ctl` with its files and the timer that lets it restart the machine, the settings folder, the data folder with the updater's socket, the system user, three small systemd files, and the machine's own security updates. Every build and every scheduled job of Crew's own program runs in a container. Another installation gets images and one small archive of deploy files, never a checkout. *(Design 13.1)*
- **R119. A sandbox is a microVM.** Started with the Kata runtime, so it has its own kernel. The hypervisor is Cloud Hypervisor. *(Design 13.2)*
- **R66. Nothing shared between projects, and isolated worktrees and tokens per run.** Each project's sandbox has its own kernel, its own persistent home, its own bare repository mirror and per-run git worktrees, its own network bridge, and its own Docker daemon; each ACP session gets its own worktree, its own scoped MCP bearer token (`NewSessionParams.mcpServers`), and reset dotfiles before every turn and check. *(Design 13.7)*
- **R67. Full access inside.** The agent can run any command, use `sudo`, run containers, and install what it needs inside its project microVM. The project's `/home/agent` persists across start, suspend, and stop/snapshot on `<CREW_DATA_DIR>/projects/<id>/home` (with dotfiles, CLI settings, and `PATH` reset before every session and check), while root changes outside `/home/agent` disappear when the microVM stops unless baked into an optional per-project image (R68). What it can reach on the network is set by the network mode (R121). *(Design 13.5)*
- **R120. Docker inside the sandbox.** Where the role needs it, an agent can build and run containers in its own Docker daemon, which shares nothing with the machine's. *(Design 13.5)*
- **R68. A runtime image, and optional per-project derived images.** A prepared, versioned base image (`crew-runtime:<version>`) with `agy`, Docker, and a standard set of tools, as in styrman-crew: Go, Node.js, Python, `gh`, gcloud, kubectl, and OpenTofu. A project adds what it needs with its setup command (R70) or, for heavy OS packages, an optional per-project `.crew/Dockerfile` derived `FROM crew-runtime:<version>` (`crew-project-runtime:<project>@<sha256>`). *(Design 13.4)*
- **R136. One runtime image, kept current.** The CLIs, the harness, and the run driver are in one image. Crew looks for new versions of the CLIs and the tools every day. A new version becomes a ticket, then a tested release that gives notice and waits for Install, because it changes what the image is built from, which is code (R111). From then on every new sandbox uses the new image by itself, and a running one keeps its own until it ends. *(Design 12.8)*
- **R70. Reproducible setup.** A project declares a setup command that prepares a fresh sandbox. When it fails twice in a fresh sandbox, the project gets a fix ticket and is not paused. *(Design 13.5)*
- **R121. A network mode.** Each project has a network mode, and a ticket can override it. Internet: the internet, but not the machine or the local network. Model only: the agent's model service and Crew, and nothing else. LAN: the local network too, set by the operator on purpose. *(Design 13.6)*
- **R69. The machine is protected.** An agent's sandbox cannot reach the machine's files or the machine's Docker socket. It cannot reach other sandboxes, or the local network except in LAN mode (R121). It never reaches the project's main repository: it clones from a second copy, with a git token that allows only its own branch. Only the operator's project terminal sees the main repository, read-only. *(Design 13.8)*
- **R123. A sandbox gets its credentials when it starts.** Crew keeps every credential in its database, encrypted with a key that is a file on the machine. When a sandbox starts, its credentials are copied into it, not linked, so sandboxes share no file. A run's sandbox gets the login of the one CLI it runs, the git identity, and a temporary GitHub token; no gcloud (AGENTS.md safety rule 8). Nothing has to be chosen per project. The improver, a `judge:` check, a summary writer, a prompt writer, and a login check get only the login of their CLI. A conversation about a project gets what a run of that project gets. The chat of a ticket gets only the login of its CLI and the read-only GitHub token, and no gcloud. A project terminal gets what a run gets, with the login of every CLI that is signed in. A check that runs commands gets only the read-only GitHub token. A hosted project and the machine of the install test get none. *(Design 12.6)*
- **R189. A temporary GitHub token for each sandbox.** The operator creates a private GitHub App once, in Setup, with one permission: reading contents. Each time a sandbox starts that works on a project, Crew asks GitHub for a token that can only read, that names only that project's repository and the others the project lists, and that stops working after one hour. Crew revokes it when the sandbox ends. The SSH key and the `gh` login never enter a sandbox that runs an agent, a check, or a project, so nothing in such a sandbox can push to GitHub. *(Design 12.6)*
- **R195. Crew's own run of the checks decides.** A check that is a command is run by Crew itself, in a sandbox with no agent and no run token, and that result decides. A test or an end-to-end result that an agent writes is a record and does not move a ticket. A review and a verification are verdicts of an agent that did not build, and count beside the commands. A check ends as pass, fail, or error. An error is Crew's own trouble, is tried again, and is never counted against the ticket. *(Design 13.9)*
- **R197. Files that are code wait for the operator.** A ticket may change the files a CLI loads as instructions only where Analysis allowed it, and Crew tests every push against that list. A change to a hook, to a settings file of a CLI, to an MCP configuration, or to a workflow that GitHub runs never merges by itself, and a branch that changes a workflow is not pushed to GitHub before the operator agreed. *(Design 13.8)*
- **R170. Files the CLI reads as instructions.** A ticket's branch may change the files a CLI loads as instructions only when the ticket names them. The agent that judges a ticket takes those files from `main`, not from the branch it judges. A memory entry reaches an agent as a note with its source, marked as data. *(Design 13.8)*
- **R174. Looking after the machine.** Crew guards the disk, lets security updates install by themselves, keeps the firewall closed for IPv4 and IPv6, watches the certificates, and uses one time zone. The operator can build and install a release by hand if no CLI works. *(Design 16.10)*
- **R194. Nothing watches Crew from outside.** No outside service checks whether Crew is up. If Crew is down, the operator finds out by opening it. *(Design 16.10)*

### Logins

- **R126. Logged in beforehand.** The operator signs in to each CLI in the setup microVM. For `agy` a dedicated Google account with no sensitive Google Cloud project access is strongly recommended (`12.6`): Crew keeps the token file of that sign-in and writes it into each sandbox that runs `agy`, with no keyring. No API key is used. *(Design 12.6)*
- **R183. Setup comes before any project.** Every credential is set up once, for the machine, in Setup: the git identity, the git SSH key, `gh`, the GitHub App, the gcloud login with its application default credentials, and `agy`. No project has credentials of its own. A credential that is renewed reaches every sandbox that starts afterwards. *(Design 12.6)*
- **R134. Login check, and the longest sign-in.** Crew uses the longest-lived sign-in each CLI has: for `agy` its token file, which renews itself. Crew checks that each CLI is signed in, before every run and on a schedule, and once a day with a real request, which also keeps the sign-in of `agy` in use. It shows the state of each login and when it was last checked. A login that `agy` renewed is kept only after a test, and only when it is for the same Google account. *(Design 12.6)*
- **R135. Setup, and signing in again.** At first start Crew shows Setup, where the operator signs in to `agy` in the setup microVM. When a login stops working, the runs on that CLI stop, the inbox asks the operator to sign in again, and the runs continue afterwards. Crew saves each run's work in progress before it stops it (R196). *(Design 12.6)*
- **R196. Why a run stops, and what follows.** Every stopped run has one reason from a fixed list. A stop for a sign-in or a limit makes the subtask continue on a model of its bracket in another group that can run, or wait when none can or the model was set by hand. A stop for a restart, a failed setup, or by the operator makes the subtask wait or be tried again. None of these counts against the ticket. A stop for a time limit, silence, too many turns or rounds, an error, a refused tool, or a dead sandbox is retried once and counts. Before Crew stops a run itself, it saves the work in progress, so the continued run starts from it. *(Design 12.4, 12.7)*
- **R198. Install, update, and uninstall.** Crew is installed, updated, and uninstalled with one program, `crew-ctl`, and each of the three works on a fresh machine. The installer first gathers what it needs, from a `.env` file or by asking when it starts, then checks the machine and the answers, and only then installs Docker, Kata, and Crew. It makes the key file and the generated secrets itself. On the main machine it builds Crew from a checkout and needs no GitHub login. Another machine gets the images of a finished release, never a checkout. Every step can be repeated, and a dry run changes nothing. Update and roll back use the same updater as the app. Uninstall has three levels, prints its plan first, and takes out only what the install put in. After the level that keeps the data, a new install opens that data again. All of it is tested on a fresh virtual machine before a release, unless the operator switched that test off, and the release then says so. *(Design 16.11)*
- **R201. Reproducible.** Every version Crew is built from is fixed in one file, `versions.lock`, with its checksum, and the lock files of its packages are kept. The few other places that must name a version are checked against it. The build takes nothing from a private place. What cannot be fixed in advance, such as the version of `agy`, is recorded in the release. A release can be built again from its tag. *(Design 16.12)*
- **R202. Open-source, reproducible, and clean.** Crew's source repository ships publicly under the Apache-2.0 licence (`LICENSE`), with third-party and copied-component attributions in `THIRD_PARTY.md`, while compiled runtime images containing vendor CLIs remain local (`crew-ctl install --build` / `build`) or in each operator's own private registry (`16.12`). The repository holds no secret, and what differs between machines comes from `/etc/crew/crew.env` (seeded from `.env.example`), so the same code installs anywhere. Recordings are scrubbed of tokens, account ids, and mail addresses, and `make check` (`tools/check-clean.py`) enforces public email, host path, secret, and binary cleanliness even on fresh clones without `spike/raw/`. Crew sends nothing about an installation to anyone. Design documents stay in `docs/plan/`. A primary build host creates releases; secondary installations pull finished releases via release card + private registry (`crew-ctl install --from` / `release pull` / `release import`), verify checksums, install only when directed by the operator, run no improver, and share no data with the primary host. *(Design 16.12)*
- **R199. Crew acts as the operator.** Crew is an extension of the developer who runs it. A sandbox works with the operator's own login of the CLI it runs and the git identity. gcloud stays in Setup: no run and no conversation gets it (AGENTS.md safety rule 8). *(Design 12.6)*
- **R200. Agents.** Each role is an agent of its own, with its own CLI, model, effort, next choices, prompt, tools, and sandbox. The app lists the agents and what each runs on, and the operator can change the model, with its CLI, and the effort of each. *(Design 12.3)*
- **R203. One agent CLI.** Crew runs `agy` alone. It has its own sign-in, its own models, and its own limits, in two groups of models. By default every agent has a model of its bracket in each group that has one. When one group is at its limit or close to it, the other takes the work it can, and Crew goes on. When `agy` is signed out or its account is stopped, every project waits. *(Design 12.2)*
- **R204. No other CLI.** Crew is built around `agy` alone (since 0.42). No other terminal CLI, such as `claude`, `codex`, or `opencode`, is added. *(Design 12.2)*
- **R205. Crew chooses the CLI and the model, and the operator can override.** For the builder, the reviewer, the tester, the E2E tester, and the verifier, Crew chooses the CLI, the model, and the effort by itself, from the results it measured on this machine and from newly offered models ranked within each role's `(family, bracket)` slots (R190). For the builder, what decides is the share of tickets whose command checks passed at the first request, and the defects that escaped. For an agent that checks, it is the defects that escaped it, and the share of runs in which it asked for a fix. The fix rounds and the time are shown, and decide nothing. Crew tries the other entries of an agent's list with a small share of the runs. It changes the builder to an entry that has enough runs and did better. It changes an agent that checks only to an entry that let fewer defects escape and asked for a fix about as often, so a model that passes everything, or fails everything, is never chosen for that. When a newly offered model ranks ahead of an automatic role's active model within its primary `(family, bracket)` slot, Crew promotes it with `reason = "newer"`, tells the operator, watches the next `watch_runs` runs just as for an empirical change, and goes back by itself for `retry_days` if the watched runs are worse than the replaced model's measured baseline. Crew tells the operator with the numbers, watches the next runs, and goes back by itself if they are worse. It changes an agent empirically at most once in a set number of days, and never against the rule that a check uses another model than the build. The operator can set an agent by hand, and Crew then leaves that agent alone, makes no tries for it, and only shows what it would choose. A project's setting and a pin on a ticket are never changed by Crew. The whole of it can be switched off. The operator can start a comparison between two models for an agent, which ends by itself. No improvement can change the numbers of these rules. *(Design 12.3)*
- **R206. A better prompt, and Improve text and Review text.** When the operator writes the first message of a plan draft, Crew proposes a better wording before an agent gets it: the rewritten prompt, what was changed, the assumptions that were made, and at most three questions about what is missing. The rewrite keeps every fact and limit the operator wrote and adds nothing of its own except marked assumptions. The operator sees both texts and chooses: use the rewrite, edit it, or send the original. Everywhere the operator writes in Crew, selected text has two actions: Improve text, which proposes a rewrite of the selection that the operator can use, edit, or refuse, and Review text, which changes nothing and says what is unclear or missing. On the plan of a draft both actions go to the planning agent. A rewrite never holds the operator up: when it is late or fails, the original stays. Both texts and the choice are saved, what followed is measured, and no agent of a project can read a rewrite. A change to the prompt writer's rules waits for the operator. The proposal for a plan draft can be switched off. *(Design 7.2)*
- **R207. A chat on every ticket.** Every ticket has a chat, in every stage and after it is closed. The operator asks what happened in the ticket, and asks questions while it runs. An assistant answers from the ticket's records, its trajectories, its diff, and its code, and can try things in a sandbox of its own, which holds only the login of its CLI and a read-only GitHub token. It is not the agent that works on the ticket, so a question never stops or steers a run. It has no tool that writes: it cannot change the ticket, the code, or the board, and it sees no other project. Only the operator and the improver read a chat, and no agent of the project does. From any message the operator, and only the operator, can tell the working agent, make a new ticket, or store a memory entry. On a ticket that is Done or closed there is no working agent to tell. What is sent on keeps its source: that the assistant wrote it, and that the operator confirmed it. A ticket made from a chat cannot skip Review. A done ticket is not opened again: what should change becomes a new ticket that is linked to it. Every turn is saved with the ticket. *(Design 11.9)*
- **R208. Mark text, and comment.** In the ticket view the operator can select text with the mouse, in a log, a trajectory, a prompt, a diff, a record, the summary, or a message, and write a comment on it. The comment goes to the ticket's chat with the marked text quoted and where it came from, and the assistant analyses it and answers. It reaches the working agent only when the operator sends it on. Marked text is data for the assistant and for the agent, and is never followed as an instruction. *(Design 11.9)*
- **R169. No credential left behind.** Ephemeral run homes are removed with their sandbox, and snapshots of a project's persistent `/home/agent` strip OAuth, GitHub, and run tokens before sealing the archive with `/etc/crew/secret.key`. A backup holds credentials only in encrypted form, without the key. *(Design 12.6)*

### Settings

- **R72. Settings in the database.** One set of settings for the machine and one per project, each validated and with defaults. They can be written out as a file and read back. *(Design 14.1)*
- **R73. Changes without restart.** A change applies to what starts after it. *(Design 14.2)*
- **R74. Settings in the UI.** Both sets of settings are edited from the UI since M17, through the same API as the file and `crew-ctl`. *(Design 14.3)*

### Project knowledge

- **R75. One developer, several projects.** The projects share nothing with each other: no code, records, knowledge, or sandboxes. A sandbox's GitHub token opens only its own project's repositories (R189). What they have in common is the operator's logins, the machine, and the baseline. *(Design 10.1)*
- **R57. Per-project isolation.** Each project has its own analytics, evals, memory, bugs, feedback, and documentation. No tool of a project's agent accepts another project. *(Design 9.2)*
- **R58. Memory.** Agents write what they learn directly, with no approval. A short index goes into every run, and entries tied to files appear when an agent touches those files. *(Design 9.3)*
- **R59. Memory stays healthy.** The lead merges duplicates and retires stale entries. The operator can edit or retire any entry. Secrets are rejected. *(Design 9.3)*
- **R60. Bugs.** Any agent, and the operator, can report a bug. A bug report also creates a fix ticket in the backlog, linked to the bug. A bug is fixed only when its ticket is Done and a check covers it. *(Design 9.4)*
- **R61. Feedback.** From the operator, and from agents on each other's work. The lead turns each piece into a memory entry, a ticket, or an override of a prompt for that project, and records what was done. An override is applied when the project's evals are not worse with it, and the operator is told. An override of a role that checks waits for the operator. *(Design 9.5)*
- **R62. Evals.** Cases taken from the project's own finished tickets and fixed bugs. A change to prompts, the model, the agent CLI, or the runtime image can be run against them before it is applied. *(Design 9.6)*
- **R63. Analytics.** Flow, quality, and use (tokens and time), computed from the project's records, and available to agents. *(Design 9.7)*
- **R64. Retrospective.** When the project, or a part of its plan, is done, the lead records what should change. *(Design 9.8)*

### Documentation

- **R102. Every project has documentation.** An overview, the architecture, how to set up and run it, decision records, guides, reference pages, and a changelog. *(Design 9.9)*
- **R103. Stored with the project and in the app.** The pages are files in the project's repository. Crew indexes them in its database, shows them in the app, and lets the operator search and edit them there. *(Design 9.9)*
- **R104. It starts from the plan.** The first pages are written from the original plan, as one of the project's first tickets. *(Design 9.9)*
- **R105. Documentation is part of done.** Analysis names the pages a ticket must update, or says that none need it. The ticket cannot leave In progress until that is done, and the review checks it. *(Design 9.9)*
- **R106. Decisions are recorded.** When an agent makes a design decision that later work must follow, it is written as a decision record. The operator writes one by editing the documentation. *(Design 9.9)*
- **R107. A changelog.** When a ticket is merged, a changelog entry is written in the same commit, from the ticket. *(Design 9.9)*
- **R108. Documentation stays current.** Each page names the code it covers. A page whose code has changed without it is flagged, and the lead creates a ticket to update it. *(Design 9.9)*
- **R109. Agents read the documentation.** Through the Crew MCP and in their clone, before they change the code it describes. *(Design 9.9)*

### Crew level: learning from all projects

- **R76. Crew has all the data.** The records, summaries, trajectories, and analytics of every project are held by Crew in one place. Only the operator and Crew's own improver can see across projects. *(Design 10.1)*
- **R77. Signals.** Crew records signals from every project as it runs: failed checks, rework and its reasons, bugs found after a ticket was Done, analysis decisions, watchdog stops, feedback, tokens and time, what agents had to install, and every time the operator had to step in. Agents do not send signals themselves. *(Design 10.2)*
- **R78. A baseline.** The harness, the agent definitions, the stage prompts, guides, templates, and pipeline defaults that every project runs on belong to Crew and are versioned in git, in Crew's source repository. A project can override parts of it for itself, and its overrides are in the database. *(Design 10.3)*
- **R79. An improver.** An agent at the Crew level reads the signals across projects, finds patterns, and proposes improvements to the baseline. Each proposal names its evidence, the change, and the metric that will show its effect. It also proposes for the base a skill that several projects added. It does not change which model or effort an agent runs on (R205). The improver runs in Model only mode with no repository. *(Design 10.4)*
- **R80. Improvements are general.** An improvement must not carry a project's code, names, or records into the baseline. Crew checks this. *(Design 10.5)*
- **R81. Tested, then applied.** An improvement is run against the eval cases of the projects, each inside its own project. When the evals are not worse it goes ahead without a separate approval. When they are worse it is refused. The operator can read every improvement and take one out before it is installed. *(Design 10.5)*
- **R167. What "not worse" means.** Every eval case is run several times before and after. A difference inside the spread between repeats is noise. Not worse means: the share of passed cases is not lower by more than that, and no case that always passed now always fails. With too few cases nothing ships. *(Design 10.5)*
- **R82. Versions, measurement, and roll back.** An applied improvement ships in a new Crew release (R113), at most one per release, which gives notice like every release. Every run records the version it used. Crew measures the effect afterwards, and takes the improvement out again by itself when the result is worse. *(Design 10.5)*
- **R83. What cannot be improved away.** The improver cannot change the isolation between projects and runs, what needs the operator's approval, network and token rules, the handling of secrets, its own gate, which model and effort an agent runs on, or the numbers by which Crew chooses a model. Crew tests each improvement against the list of what it may change. *(Design 10.4)*
- **R84. Crew analytics.** The operator sees flow, quality, and use across all projects and per version, with how often the pipeline needed the operator as the headline number. *(Design 10.7)*

### Versions and self-update

- **R110. Clear versions.** Every release of Crew has one version number. It covers everything that changes together: the server, the UI, the updater, the runtime image with the CLIs and the harness, the baseline, and the database's schema. The app shows which version is running and what each version changed. *(Design 16.1)*
- **R113. Crew improves Crew.** Crew's own code and baseline are a project on Crew's board. A change to Crew is a ticket, worked by agents in sandboxes like any other, and reaches the machine only as a release. An improvement that passed its evals becomes such a ticket. A release is cut when a set number of tickets have merged, or when the operator asks. *(Design 16.2)*
- **R111. A notice, then it installs or waits.** The operator is told that a new version exists and what it changes. A release that changes only prompts and harness text installs by itself after 24 hours, and until then the operator can install it at once or hold it. A release that changes code waits until the operator presses Install. Which kind a release is, is decided from the paths it changes. *(Design 16.3)*
- **R114. Only what was tested.** A release is installed only if it passed the gate and is exactly what was built and tested. No agent can install a release, shorten the wait, or lift a hold. No signing keys are used. *(Design 16.3)*
- **R115. Checked before it replaces anything.** A new version is started and checked beside the running one. A version that fails changes nothing. A version that turns unhealthy within the 10 minutes it is watched is switched back without the operator. *(Design 16.4)*
- **R112. Roll back from inside the app.** The operator can go back to any kept version. The last five are kept by default, `updates.keep_versions`. One step back needs no restore. *(Design 16.5)*
- **R116. Stored state survives versions.** A version can read what the version before it wrote. A change to the database's schema is first tried on a copy, and a change the older version cannot read is made in two releases, so one step back is always possible. Before an update the database is dumped, as a snapshot. *(Design 16.6)*
- **R117. The way back is protected.** The updater runs apart from the server and never replaces itself. A release that changes code, or needs a newer machine setup, never installs by itself. It waits for the operator. Rolling back uses kept images and rebuilds nothing. Running work is not lost by an update. If the app cannot be opened, the operator can still roll back on the machine with `crew-ctl`. *(Design 16.8)*
- **R141. Published releases.** Every release is a tag in the operator's `CREW_SOURCE_REPO`, pushed over SSH. With `gh` signed in, it also creates a release on GitHub with release notes, while built runtime images stay local or in that operator's own private registry (`16.9`, `16.12`). *(Design 16.9)*
- **R142. Backups.** A dump of Crew's database, the repositories of projects that have no remote, and the data of hosted projects are written to one tar.gz file per day on the machine. The events of each month that has ended are dumped once, into a file of their own, and kept. Backups stay on the machine for now. The tag of every release is on GitHub, and with `gh` signed in its images are too, so Crew can be installed again at a chosen version with `crew-ctl`. A dead disk loses everything in the database, unless the backups are copied off the machine, which is one setting. *(Design 16.9)*

### Notifications

- **R157. Mail first, no mobile app.** Crew notifies by mail to start with. The phone is skipped for now, and no app from a store is built. Web push from Crew's own web page is designed and switched off. *(Design 18.2)*
- **R143. Notify the operator.** When something needs the operator, or something important has happened, Crew sends a mail through the operator's Google account with an app password. What needs the operator is one mail for each item. Google Chat can be added when there is a Google Workspace account. *(Design 18.1)*
- **R144. The alternatives are in the message.** A notification that needs an answer shows the same alternatives as the inbox. Choosing one leads into Crew with that choice ready, and the operator confirms it there, signed in. A link or a notification alone never decides. *(Design 18.3)*
- **R152. A quick answer.** The operator can answer a mail in two presses: the alternative in the mail, then Confirm in Crew. Crew is reachable from the internet behind its sign-in for this. *(Design 18.3)*
- **R145. The inbox stays complete.** Everything that was notified is also in the inbox, and nothing depends on a notification having arrived. A message holds no secrets. *(Design 18.1)*
- **R175. Reminders, and news once a day.** What needs the operator is sent once, and once more if it is still open after four hours. News is collected into one mail a day, except four kinds that are sent at once: a project that is Done, an update or a roll back that Crew made, a hosted project that stopped answering, and a high-severity bug. *(Design 18.1)*

### Hosting

- **R146. Host a built project.** The operator can host a project from Crew's machine with the Host locally button. Crew builds it into a container image, runs it, and gives it an address. *(Design 19.1)*
- **R147. Checked before it is shown.** A new version of a hosted project is built once, started, and checked before the address switches to it. The operator can roll back to a kept version. The last 3 images are kept. *(Design 19.2)*
- **R148. Its own sandbox.** A hosted project runs in a sandbox of its own, apart from Crew, from agents, and from other hosted projects. *(Design 19.3)*
- **R154. What a hosted project reaches.** A hosted project may reach the internet from inside, with a switch per project. Nothing reaches a hosted project except through the ingress. *(Design 19.3)*
- **R176. Data, secrets, and health.** A hosted project has a data directory that is kept across deploys and is part of the backups. Its settings and secrets are entered in the app, kept in the database, encrypted, and never stored in git. Crew checks its health and restarts it when it stops answering. *(Design 19.3)*
- **R171. A copy before every deploy.** Before a deploy switches, Crew copies the project's data. A roll back can put that copy back. *(Design 19.3)*
- **R149. Addresses and access.** A hosted project gets an address under the machine's apps domain, or under a domain of the operator's, and can be reached from the internet. It is opened through the sign-in of the ingress, as in styrman-crew: a Google sign-in whose mail address must be on a list. The default is only the admin email. The operator can list more addresses, allow any Google account, or make it public. The project can learn from the ingress who signed in, so it needs no sign-in of its own. *(Design 19.4)*
- **R172. Crew and hosted projects are kept apart.** A hosted project cannot read or set Crew's sign-in cookie. Crew's inner services, which hand out logins and serve git and the MCP to runs, are not reachable from the internet. *(Design 19.4)*
- **R150. The operator decides when.** Agents cannot deploy. A project is redeployed when the operator asks, or, if the operator switched that on, when tickets reach Done: one deploy at a time of what `main` holds, which lists those tickets and is checked before it serves. A project that cannot be hosted yet gets a ticket that makes it so. *(Design 19.5)*

### Software Factory evolution (M21 to M26)

- **R209. Two-tier spec planning outside Backlog (with autonomous `@agent -> @planner` feature decisions).** Project Planning creates `000-foundation.md` and seeds `WORKFLOW.md` (with operator Project OK); Feature Planning (`.crew/specs/NNN-<slug>.md`) runs outside `Backlog` and can be initiated either by `@user` or by any peer agent asking `@planner` in `#<project>` / a feature thread, where `@planner` and peer agents can deliberate and decide autonomously within `000-foundation.md` and `13.8` guardrails before `@coordinator` emits YAML tickets. *(Design 6.8, 7.14, 11.10)*
- **R210. Base and per-project `WORKFLOW.md`.** New factories copy `baseline/WORKFLOW.md` into the repo root; the orchestrator reads it exclusively from `main` under the instruction-file approval guard (`13.8`). *(Design 6.8)*
- **R211. YAML tickets, Backlog gate, `@coordinator`, and Global Queue.** Every ticket uses structured YAML frontmatter and enters `Backlog`; `@coordinator` sequences work per factory while Crew's Global Queue schedules capacity and quota across factories. *(Design 6.8, 7.14)*
- **R212. Multi-agent ACP wrapper (`agy` first, open to `pi` and `claude`).** Crew exposes a multi-session ACP Peer Agent API (`1 session = 1 peer agent`, internal subagents disabled) on `agy`, keeping `internal/cli/` open for future minimal CLIs such as `pi` and `claude`. *(Design 11.10, 12.2)*
- **R213. `max_tries` circuit breaker to clean-context `@researcher`.** Runs hitting `max_tries` stop and hand off to `@researcher` in a separate diagnostic prompt without failed-run transcript noise. *(Design 7.14)*
- **R214. Lightweight internal Slack-like chat (`#<project>` + per-feature threads).** Each project has one canonical channel `#<project>` and one thread per feature (`feat/<NNN>-<slug>`) rooted in `#<project>`, where `@user` and peer `@agent`s share messages, `@planner` Q&A, check verdicts, and handoffs bounded by `max_agent_hops` and `internal/fence` provenance tags. *(Design 6.8, 11.10, 20.4)*
- **R215. Trajectory Store search and Dreamstore.** Trajectories remain searchable across peer sessions; `Memory API` Dreamstore organizes `episodic`, `semantic`, and `procedural` memory with commit-drift and pass-hit validity scoring. *(Design 5.1, 8.7, 9.10)*
- **R216. Scoped Dreaming.** Autonomous Dreaming focuses strictly on harness context micro-adjustments, synthesizing new `SKILL.md` skills, and downloading SHA-pinned vetted external skills behind `@user` approval. *(Design 10.8)*
- **R217. Tasks API — zero work outside trackable tasks.** Every unit of agent compute (`spec`, `feature`, `fix`, `chore`, `docs`, `research`, `dream`, and actionable `@agent` work spawned from `#chat`) creates a trackable Task with a key, YAML contract, budget/token ledger, sandbox binding, and Trajectory trace; only code work flows across Kanban `Backlog` to `Done`. *(Design 5.1, 6.8, 7.14, 11.10)*
- **R218. Cross-project Knowledge API.** Installation-wide `Knowledge API` collects secret-scrubbed (`internal/redact`) and provenance-fenced (`internal/fence`) Shared Skills, bug/issue advisories, and reusable specs across all factories without sharing sandboxes or credentials; acting on an advisory creates a Task in `Tasks API`. *(Design 5.1, 9.11)*
- **R219. ComputeRuntime with Kata suspend/resume and Kubernetes seam.** Formalizes Crew's existing Docker + Kata warm suspend/resume (`PauseSandbox`/`UnpauseSandbox`) and credential-stripped sealed home snapshots behind `ComputeRuntime` (`Start`, `Exec`, `Suspend`, `Resume`, `Snapshot`, `Restore`, `Stats`, `Destroy`), keeping a clean seam for future Kubernetes (`RuntimeClass: kata` + `BlobStore` snapshot offload). *(Design 5.1, 13.12)*
- **R220. Lightweight split storage (`StateDB` + `BlobStore`).** Repository interfaces separate relational `StateDB` (`PostgreSQL` `crew-db` default or embedded `SQLite` WAL for lightweight installs) from immutable `BlobStore` (`Local FS` or optional `GCS`) holding compressed `.jsonl.zst` trajectory event archives and sealed `512 MB` VM snapshots. *(Design 5.1, 6.8)*
- **R221. Skills Registry across base, shared, project, dream, and vendored tiers.** Unifies base harness skills, cross-project `Knowledge API` shared skills, Dream-synthesized skills, and external SHA-pinned skills (`VENDORED.md`) behind static vetting, `request_skill` deduplication, per-factory activation via `Tasks API`, live warm-VM sync, and usage/validity telemetry. *(Design 5.1, 9.11, 12.9, 12.11, 20.4)*

### Numbers that are no longer used

| Number | What happened |
| --- | --- |
| R1 | Merged into R125 |
| R2 | Merged into R125 |
| R4 | Merged into R125 |
| R24 | Merged into R160 |
| R124 | Merged into R140 |
| R131 | Merged into R6 |
| R159 | Replaced by R160 |
| R161 | Dropped in 0.53 with the budgets: no limit is in dollars (R133) |
| R128 | Dropped in 0.26, when Crew became `agy` only. Two CLIs were back in 0.30 as R203, with the CLI as a setting, and `agy` is the only one again since 0.42. Since 0.32 Crew chooses the entry for five agents by itself (R205) |

## What is saved where

| Data | Where |
| --- | --- |
| Code, documentation, `WORKFLOW.md`, and a project's own skills and context | Git, the project's repository |
| Projects, specs, tasks/tickets, subtasks, chat rooms, Dreamstore, cross-project Knowledge advisories, Skills Registry (`skills_registry`, `skill_installs`), records, summaries, decisions, bugs, feedback, evals, and overrides | `StateDB` (`PostgreSQL` `crew-db` default, or embedded `SQLite` WAL in lightweight single-node mode) |
| Settings, of the machine and of each project | `StateDB` |
| The history of every change | `StateDB` |
| Conversations, turns, and live Trajectory index | `StateDB` |
| Compressed trajectory event archives (`.jsonl.zst`), sealed `512 MB` microVM snapshots, E2E media, and release bundles | `BlobStore` (`Local FS` or optional `GCS`, referenced from `StateDB`) |
| The CLI's own conversation files | `StateDB` / `BlobStore`, for a set number of days, so a conversation can be continued |
| Prompts, signals, improvements with their evidence, and releases with their notices | `StateDB` |
| The job queue, run status, and timers | `StateDB` |
| Credentials and secrets | Crew's database, encrypted: the SSH key, the `gh` login, the key of the GitHub App, gcloud, the login of `agy`, the app password of the mail account, and the settings and secrets of hosted projects |
| Personal tokens, signed-in devices, the log of calls to the Crew MCP, and what happened in a terminal | Crew's database |
| The key that encrypts the secrets | A file on the machine |
| Hosted projects | Their images, data directories, and logs are files on the machine. The deploy records are in the database. |
| Release folders | Files on the machine, the last five by default (`updates.keep_versions`). Each release is also published. |
| Backups | tar.gz files on the machine: one per day with a dump of the database, and one for the events of each month that has ended |
| A run's home | A directory on the machine, until its sandbox is removed |

The baseline is in Crew's source repository. The improvements and their evidence are in the database. Neither is in any project's repository.

## Example project layout

Branches: `main`, one `task/<ticket>-<name>` per ticket in work, and `sub/<ticket>.<n>` for a parallel subtask.

On `main`, with the code:

```
README.md              # overview
CHANGELOG.md           # written from ticket summaries
AGENTS.md              # the project's own context, read by every run
.agents/
  skills/              # the project's own skills, with VENDORED.md for those from outside
  rules/
docs/
  architecture.md
  setup.md
  decisions/           # one page per decision record
  guides/
  reference/
src/ ...
```

Everything else is in the database. An export of a project writes it out like this:

```
board.yaml             # the project's settings, with its stages
project.md             # name, key, goal, phase
plan/
  original.md          # the plan the project was created from, never edited
  breakdown.md         # how the plan was split into tickets
  amendments/          # later changes to the plan
tasks/
  0042-lock-account/   # ticket SF-42
    task.md            # why, what, goal, checks, stage, origin, analysis, links
    subtasks/          # one file per subtask: agent, model, effort, state, steps, result
    notes/
    reviews/
    tests/
    comments/
    assets/            # logs and screenshots
    runs/              # one record per agent run, with the parts of its prompt
    summary.md         # written when the ticket ends
decisions/             # the choices agents recorded
deploys/               # one record per deploy of the hosted project
memory/
bugs/
feedback/
evals/
overrides/             # overrides the lead proposed, with their eval results
agents/                # this project's overrides of the baseline, usually empty
stages/
```

## Example layout of Crew's source repository

`CREW_SOURCE_REPO` (for example `your-org/crew`; Go module `github.com/kesokaj/crew`):

```
cmd/                   # the server, the harness builder, the run driver, the updater
internal/              # the server's packages, some copied from styrman-crew
  db/                  # the database's schema and its numbered changes
  settings/            # the machine's and each project's settings defaults (crew.yaml, board.yaml) and validation
ingress/               # the Caddy ingress module (github.com/kesokaj/crew/ingress)
web/                   # the UI
deploy/                # host setup, Kata configuration, firewall, ingress, crew-ctl
harness/               # what the CLI loads: context, skills, CLI subagents, rules
baseline/
  agents/              # lead, builder, reviewer, tester, e2e, verifier, judge, stage, summary, prompt-writer
  stages/              # the prompt for each stage and each kind of lead run
  guides/              # planning, breakdown
  templates/           # plan, task
  roles.yaml           # the first model and effort per role, which install copies into the machine's settings
  tools.yaml           # the words of the Crew MCP's tools, and who may call each
image/                 # the one runtime image: agy, the harness, the run driver, the tools
improver.md            # the improver's own definition
versions.lock          # every version Crew is built from
.env.example           # every key of the install file, explained
testdata/, spike/      # the fixture project, the scrubbed recordings, the pin fixtures, and the scripts of the spike
docs/                  # architecture.md, setup.md, plan/ (these documents), and testing/
```

This installation's settings, plan drafts, improvements, and release notices are in the database, not in a repository.
