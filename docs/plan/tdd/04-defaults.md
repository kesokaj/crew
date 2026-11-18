## 4. Defaults I chose (change any of these)

These were not discussed, so I picked the option that fits your description and keeps v1 small.

| Area | Default | Reason |
| --- | --- | --- |
| One kind of card | The plan is split straight into tasks. A task names the part of the plan it belongs to, but parts are not cards. Subtasks are records inside a task, not cards. | Your nine steps describe plan, then tasks. A second level of cards would add a second set of stages. |
| What Planning holds | The breakdown before your OK, and nothing else | A ticket only moves to the right, so nothing comes back to Planning. |
| Optional stages | Review and Test are on unless you turn them off. E2E and Verification are on when you ask for them in the plan. | You named verification, test, and end-to-end as things to add on request. Review and Test stay on because without them no other agent looks at the work. |
| Ready tasks | An analysed and approved task waits in the Analysis column, marked ready, until In progress has room | It avoids one more column. |
| Every task is analysed | Tasks from the plan too, just before they are built | The code has changed since the breakdown, and the analysis is where the approach and the documentation pages are set. |
| What a subtask is | One piece of work inside a task, done by one agent run: one per stage visit, subtasks an agent creates for its own ticket, and helpers an agent starts | It gives the task view one list of everything the task is using or has used. |
| Subtasks an agent creates | Subtasks that do not depend on each other run in parallel, each on its own branch and in its own sandbox, at most 3 at once per ticket. The backend joins them. At most 6 are added per ticket. | You want the system to run without you as much as possible. A branch per subtask means two runs never push to the same branch (7.11). |
| Saved prompts | The full prompt of every subtask is kept for good, and shown in its parts | You asked to see the prompt running in a task. |
| Ticket numbers | A short key per project and a counter, such as SF-42 | The usual form in a development workflow. |
| Pull requests | One per ticket when the project has a GitHub remote. Crew opens and merges it. | It makes the work visible where developers already look. Without a remote, a merge commit. |
| Where documentation lives | Files in the project's repository on `main`, indexed and shown in the app | It is reviewed and versioned with the code it describes, and the app can still show and search it. |
| Who writes documentation | The builder of each task, for the pages the analysis names. The lead creates tasks for pages that fall behind. | Documentation written with the change stays closer to the code than documentation written later. |
| Who analyses | The lead, one task per run. A reviewer does it for tasks the lead created. | The lead knows the plan. The creator never analyses its own task. |
| New features from agents | A notice, then they go ahead by themselves after 24 hours. Approve now, Discard, and Hold are the alternatives. | My recommendation. It is the same rule as for a release of prompt text. The scope guard still stops a project that grows too much (7.7). |
| An open choice | The agent decides, records the decision in Crew with the alternatives and its reason, and carries on. You can change any decision afterwards, and Crew creates a ticket that carries the change out. | You said: do not wait for the user, record the decision, carry on (7.5). |
| A ticket that cannot get through | The lead looks at it again: one new approach, then it is cancelled and replaced by other tickets. Nobody is asked. | You decided this in 0.21: a ticket works as kanban. |
| Your own tasks | Approved by adding them. Analysis only completes the why, what, goal, and checks. | You wrote it, so asking you to approve it again adds nothing. |
| Skipped tasks | Stay in Analysis as skipped, with a reason and a condition to look again. They do not block the project from being Done, and are listed in the final report. | A skipped task is a decision, not unfinished work. |
| What still stops work | A run's turns and time, a ticket's fix rounds, and the share of tickets agents may add to the approved plan. No limit is in dollars (since 0.53, 7.7). | With one OK, limits are the only thing that bounds the pipeline. |
| Merging | Each task is merged into `main` when it passes its last stage, one at a time, after its checks pass on top of the current `main` | No long-lived branches, and `main` always passes its checks. |
| Who writes the task summary | A separate summary step by the backend, not the agents that did the work | A summary by the agent that did the work tends to flatter the work. |
| Trajectories | Rows in the database, one per event, kept for good. | You decided this in 0.26. Feedback is run on them and eval cases are made from them. |
| GitHub repositories | Private, under the account the SSH key belongs to | The safe default. You are the owner of every repository Crew creates. |
| Plan drafts | Kept in the database, also after a project was created from one (10.3) | A draft has no project repository yet. |
| Backend | Go, written for Crew | The parts copied from styrman-crew are in Go, so they fit without a second language (17.2). |
| Frontend | A web app written for Crew, in TypeScript with the same framework as styrman-crew (TanStack Start) | The terminal pane can then be copied as it is. |
| Database | PostgreSQL, in its own container, `crew-db`. It holds everything Crew saves (6.5, 15.3). | You decided in 0.25 that the database is the brain. I chose PostgreSQL: many sandboxes write at once, and it has transactions, text search, and a dump for backups. |
| Records | Rows in the database. A project can be written out as files and read back (6.2). | You decided this in 0.25. The export keeps a project movable and readable outside Crew. |
| Model access | The CLI's own subscription login, given to each run by the credential broker | You cannot use APIs for the agents, and the subscription works well for you today. The price is that a login is inside the sandbox (13.8). |
| Login check | Before every run, and every 30 minutes | Crew should notice a login that stopped working before an agent fails on it. |
| Harness | A folder in Crew's source repository, built into the runtime image | One image to keep current, and no separate installer. |
| Keeping the image current | A daily look for new versions of the CLIs and the tools, as a ticket on Crew's own board | An update is then tested and released like any other change. |
| What a run sandbox may hold | The login of the one CLI it runs, the git identity, and a temporary GitHub token that can only read. No gcloud. | You chose my recommendation in 0.25; gcloud left out in 0.79 (safety rule 8). What it costs is in 12.6. |
| Hypervisor | Cloud Hypervisor | styrman-crew runs it. Firecracker does not work with plain Docker Engine (13.2). |
| Limits on a new machine | Read from the machine at Setup: its cores and memory give how many sandboxes fit | Host hardware specs and prior notes can differ, so the target machine is measured directly (13.10). |
| One microVM per run | No long-lived microVM per project | Your rule is that runs share nothing. The project terminal starts its own microVM when you open it (13.11). |
| Terminals | A project terminal with a clone of the repository that the git endpoint takes no push from, an edit mode that can push, and a jump into a running run's microVM | Diagnostics is the normal use. Edit mode is a deliberate switch, and a push to a ticket's branch is refused while an agent is running there (13.11). |
| The ingress | Copied from styrman-crew, with its Google sign-in for the accounts you allow | You confirmed this. Crew is opened from another machine. |
| The updater | New: a small container that checks a release beside the running version, then switches, and switches back by itself (16.4) | The old one worked only so-so. Its bug records say why. |
| "Internet off" | Model only: the sandbox reaches the CLI's model service and Crew, and nothing else | With no network at all the agent has no model. |
| Budgets | None since 0.53: no limit is in dollars, and Crew estimates no cost. The real limits are the subscriptions': a five-hour and a weekly limit for each group of models of each CLI, handled by a quota guard. | No run is billed, and `agy` reports no cost. You chose this in 0.53 (7.7, 12.7). |
| Versions | One number for the server, the runtime image, the CLIs, the harness, the database's image, and its schema. A release is a folder of images built once on the machine, and it is published on GitHub. | A roll back then restores all of them together, without rebuilding. |
| Where releases are published | A tag and a release on GitHub with notes in the operator's source repo, with compiled runtime images stored locally (`crew-ctl install --build` / `build`) or in that operator's own private registry (`16.9`, `16.12`) | Source is open-source Apache-2.0 (`LICENSE`); compiled runtime images carry vendor CLIs and stay private to each operator. |
| Where backups are kept | On the machine, as tar.gz files, to start with | You decided this in 0.21. |
| How the operator is reached | By mail, where a link chooses and Confirm in Crew decides | You decided mail first. The phone is skipped for now. |
| Which account sends the mail | Your Google account with an app password, entered once in Setup. The mail is shown as coming from Crew. | You said this is fine. It needs no other service. A mail service on your own domain is the alternative (18.4). |
| Who may open a hosted project | Only the admin email, until you choose otherwise: a list of Google mail addresses, any Google account, or fully public | You confirmed this. It is how styrman-crew protects what it exposes (19.4). |
| What a hosted project may reach | The internet, from inside. A switch per project turns it off. | You said yes. It still cannot reach the machine, the local network, or other sandboxes. |
| What your own tools may do through the MCP | Read, create tickets, and comment. A token can also be allowed to answer: approve or discard a new feature, and change a decision an agent recorded. That is off until you switch it on for that token. | My recommendation. The tool on your laptop is itself an agent. What it may decide should be a choice you make per token, and the large decisions stay in the app (11.7). |
| How a hosted project runs | As a microVM of its own, like every sandbox | It is code that agents wrote. It should not share a kernel with Crew. |
| When a hosted project is redeployed | When you press Deploy again. Following `main` is a switch. | You decide when what you show changes. |
| Which image a sandbox uses | Always the runtime image of the running version, but a run of `crew-gate` while a gate's live steps run, which starts from the candidate's (16.2 step 5, since 0.52) | A new microVM then uses the new image by itself. A project adds its own setup on top, in each sandbox (13.5). |
| When a release is installed | A release that changes only prompts and harness text: by itself, 24 hours after the notice. Every release that changes code: when you press Install. Hold is the alternative for the first kind. (16.3) | You chose my recommendation in 0.29. The server is one program with the rights of all its parts, so a list of protected parts could not bound what agent-written code may do. The check before the switch and the automatic switch back still catch a bad release. No signing keys. |
| Code in a sandbox | Cloned over HTTP from the server, which knows the run by its network and a secret given to that run | No other run's working copy is visible, and a run can push only its own branch. |
| Local network | Sandboxes reach the internet, but not the machine's other services or the local network. The LAN mode is the exception, set by the operator on purpose (13.6). | Internet access does not need it. |
| When the machine is full | A run waits until there is room for its sandbox | Subagents have no count limit, so the machine is the real limit. |
| Order of the lead queue | Six classes: work you are waiting on, analysis, final reports, housekeeping, evals, and the improver. Within a class: by the priority you set, across projects, then projects take turns, then by the ticket's own priority within its project, then by age. (7.9) | You said in 0.32 that the queue goes by priority. New tasks should not wait long, and one project cannot take every slot unless you made its tickets the urgent ones. Since 0.42 a priority an agent set counts only within its project. |
| Memory delivery | Crew's own memory in the database, reached through the Crew MCP. The index is part of every prompt. | It works across runs that share nothing. |
| What all projects run on | A baseline owned by Crew: agent definitions, stage prompts, guides, templates, and pipeline defaults, versioned in Crew's source repository. A project can override parts of it for itself. | Improving the agents for every project needs one place to change. |
| Who improves the baseline | An improver agent at the Crew level proposes. A proposal whose evals are not worse ships in the next release without a separate approval. The notice of that release is where you stop it. Crew rolls a change back by itself if it measures worse. | My recommendation. One notice per release instead of two approvals, and every step has a way back. |
| What may flow down to projects | Only general text. The backend refuses an improvement that carries a project's names, paths, or copied text. | Otherwise the baseline becomes a path from one project to another. |
| The improver's sandbox | Model only mode and no repository. It reaches only Crew's data and the model service. | It is the one agent that sees every project. |
| What the improver cannot change | Isolation, what needs your approval, network and token rules, and its own gate | An agent should not be able to loosen its own limits. |
| Improvements per release | At most one | A measured effect can then be tied to one change. |

**Added in 0.20, after the audit.** None of these was discussed. Each closes a gap the audit found (section 28).

| Area | Default | Reason |
| --- | --- | --- |
| An override the lead proposes for its project | Applied by itself when the project's evals are not worse. An override of a role that checks, or of a file no eval runs, waits for you. (9.6, since 0.49) | The same rule as an improvement to the baseline. |
| A project's setup fails twice | Crew creates a `fix` ticket for the setup instead of pausing the project (section 21) | Agents can usually repair a setup script. |
| What belongs to this installation | It is in the database, not in Crew's source repository: the settings, the drafts, and the evidence. No sandbox can reach it. (10.3) | An agent that works on Crew's code must not read every project's evidence or change a machine setting. |
| Protected parts | The list in 16.8, with the credential broker, the git endpoint's rules, and the MCP token rules added | These are the parts that keep agents inside their limits. Since 0.29 the list matters only when `updates.auto_install` is `unprotected` (16.3). |
| Who can change a check | Only Analysis. The reviewer is shown every changed test file. (7.1) | A builder must not be able to pass by weakening its own test. |
| Shares of a subscription | The project served least recently starts next (7.9) | One project must not use a whole window. |
| Pause all, and Stop all | Two switches for the whole machine (7.9) | Autonomy needs one brake that always works. |
| What "not worse" means | The definition in 10.5, with the spread between repeats as the margin | Without a definition the gate is an opinion. |
| Sessions | 30 days on a device. A fresh sign-in for a terminal's edit mode, secrets, tokens, and a release that waits. (20.3) | Crew is reachable from the internet. |
| Personal tokens | Run out after 90 days, and are limited in calls per minute (11.7) | It is the one path that is not behind the Google sign-in. |
| Files the CLI reads as instructions | Changed only by a ticket that names them, and judged from `main` (13.8) | A builder must not be able to instruct its own reviewer. |
| Data of a hosted project | Copied before every deploy (19.3) | A new version can change the data's format. |
| Looking after the machine | Disk guard, security updates, one time zone, and a build by hand (16.10) | A machine that runs unattended has to look after itself. |
| After Done | Reopen with more work, archive, delete (7.13) | The design stopped at Done. |

**Added in 0.26.** These follow from your answers, but you did not say them.

| Area | Default | Reason |
| --- | --- | --- |
| The model for a run | Each agent has a list of entries, from the baseline. For the builder, the reviewer, the tester, the E2E tester, and the verifier, Crew chooses which entry and effort runs, from what it measured. You can set an agent yourself, set another for a project, or pin one on a ticket, and those win. (12.3) | You said in 0.32: automatic choice of model and CLI, with a recommendation, and a manual choice that overrides. |
| When the model of an agent changes | When Crew's numbers say another entry is better, or when you change it. Crew tells you, watches the change, and goes back by itself if it was worse. (12.3) | Results are measured per role, CLI, model, and effort. |
| A reviewer's independence | A fresh sandbox, a new conversation, and its own prompt. Since 0.27 also a model of another family than the builder's. (12.3) | You said yes to parked question 80. `agy` runs models of more than one family, and the rule counts the family. |
| Lead slots | Counted for each CLI: 1 for `agy`, and you can change it (7.9) | You said in 0.31 that each can be changed. Since the spike `agy` has 1. A lead run takes a slot of the CLI it runs on. |
| When a group or the CLI cannot run | At the limit of a group of models, or above `quota.slow_down_at`: a run moves to a model of its bracket in another group that has room, or waits (since 1.22). Signed out, or its account stopped: every project waits. The mail about a lost sign-in goes at once. (12.6, 12.7) | `agy` has two groups of models, and one can take over from the other. |
| The harness | One source, in Claude Code's format, as styrman-harness keeps it. The image build writes it out translated for `agy` (12.1) | It is the format styrman-harness keeps. |
| The harness adapter | Kept, with one driver (12.5) | The tests replay recorded runs through it. |
| Marks on a trajectory | Whether feedback was run on it, and the eval cases made from it (8.3) | You keep trajectories until that is done. The marks say which are done. |
| Trajectories in the backups | Each month that has ended is dumped once and kept. A day's file holds the rest. (16.9) | They are kept for good, and a day's file must not grow without end. |

**Added in 0.27.** These follow from your answers, but you did not say them.

| Area | Default | Reason |
| --- | --- | --- |
| Where the list of models comes from | From the CLI's own list. `agy models` prints it. Read after every sign-in, once a day, when the version of a CLI changes, and when you press Refresh. Stored in the database. (12.3) | You said in 0.31: the CLI lists its models, and those are the models Crew lists. |
| How a model is named | The CLI, the model, and the effort. Crew passes the name exactly as the CLI takes it. (12.3) | `agy` puts the effort in the name, as in `claude-opus-5-5-high`. |
| The default models | All through `agy`, ranked automatically by `(family, bracket)` as `agy` offers them (since 1.44): Gemini `pro` (falling back to Gemini `flash`) for the curator, the planning agent (`planner`), the assistant, the builder, and the improver; Gemini `flash` (falling back to Gemini `pro`) for the tester, the E2E tester, the prompt writer, summaries, and smaller builder tasks (`part` subtasks and `docs`/`chore` tickets); Anthropic `pro` (falling back to Gemini `pro`, then Gemini `flash`) for code-ticket `reviewer`, `verifier`, and stage checks; and Anthropic `flash` (falling back to Gemini `flash`, then Gemini `pro`) for `docs`/`chore` reviews and `judge:` checks. Each default is a ranked list across the eligible `(family, bracket)` slots, and the first entry that can run is used. (12.3) | Non-check roles (including all planning) stay on Gemini so scarce `3p` quota goes to independent checks; code checks get Anthropic `pro` while `docs`/`chore` reviews and `judge:` checks get fast Anthropic `flash`, each with a same-bracket Gemini fallback. You can change each one in the Models screen (12.3). |
| The default effort | High for the lead, the builder, the reviewer, the verifier, and the improver. Medium for the tester, the E2E tester, and the prompt writer. Low for summaries and `judge:` checks. (12.3) | More effort where a mistake costs most. |
| What "another model" means for a check | Another family: Gemini, Claude, or another maker. It applies to the reviewer, the verifier, and `judge:` checks. (12.3) | Two models of one family make the same kind of mistake more often. |
| When no other family is offered, or none can run | The check runs on a model of its bracket in the builder's family that has room, and the run, a signal, and the inbox say that the rule could not be met (12.3, 12.7, since 1.26) | Work does not wait while a model of the bracket has room, and you are told when a check ran on the builder's family. |
| Limits | Counted per CLI, in the groups its vendor has: for `agy`, Gemini models, and Claude and GPT models. At the limit of its group, or at or above `quota.slow_down_at`, a run moves to a model of its bracket in another group that has room; a pin, a project's setting and an agent you set are never moved. Otherwise it waits. (12.3, 12.7) | You decided this in 0.28: move if possible, or wait for the reset; in session 92, as a rule: never wait while a model of the bracket has room. |
| A model that a CLI no longer offers | A pin or a project's setting that names it is ignored, the machine's entry for that agent applies, which is Crew's choice or yours, and you get news (12.3) | Work should not stop because the vendor removed a model. |

**Added in 0.27, after the audit.** Six independent reviews read version 0.27 before the build (section 28). These rules close what they found. They are my choices, for you to confirm.

| Area | Default | Reason |
| --- | --- | --- |
| The GitHub token of a sandbox | Names the project's own repository, and others only when the project lists them (12.6) | A token for every repository let any sandbox read every project. |
| What decides a stage | Crew's own run of the checks, in a sandbox with no agent and no run token. A tester's entry does not move a ticket. (11.3, 13.9) | The code under test ran beside the token that wrote the verdict. |
| How a run ends | A tool call only records. Crew acts after the CLI has printed its result. A missing call gets one more turn, then the run has failed. (11.3) | It was not written down. |
| Why a run stops | One list of reasons, with what follows each, and which count toward "stopped twice" (12.4) | A sign-in, a limit, an update, and a stop by you must not count against a ticket. |
| Work in progress | Saved to a place only Crew reads before Crew stops a run (11.2) | "Without losing its work" was promised and had no mechanism. |
| Continuing a conversation | A new call of the CLI in the same sandbox. One open process over many turns is a later step. (12.10) | The design said three different things. This is the one styrman-harness already runs. |
| A check's result | Pass, fail, or error. An error is Crew's own trouble, is tried again, and is never a fix round. (13.9) | A sandbox that would not start counted against the ticket. |
| A closed ticket | One stage, Closed, with a reason: discarded or cancelled (6.3) | The states did not agree with each other. |
| Dependencies | A waiting ticket does not fill Analysis. A discarded dependency is met. No circles. (7.9) | The board could lock itself. |
| A merge | Made by Crew on the machine, with the changelog in the same commit. A sandbox only runs the checks. Work merged outside Crew is checked afterwards. (7.12) | Who merged, and where, was not said. |
| Files that instruct | Analysis lists the paths a ticket may change, and the git endpoint tests every push. Hooks, a CLI's settings files, MCP configuration, and workflows wait for you. (13.8) | The rule was prose that nothing could test. |
| Text from one agent to another | Marked as data, with who wrote it (13.8) | Only memory was marked. |
| A ticket made with a token, or by a conversation | Analysed like an agent's ticket, unless you add it in the app or the token may answer (11.7, 11.8) | A leaked token could add approved work. |
| A renewed login from a run | Taken only after a test, and only for the same Google account (12.6) | One sandbox could replace the login of all later ones. |
| What the improver may change | A list of files and keys, tested by Crew. Not approvals, network, credentials, stages, or the prompts of roles that check. (10.4) | The limit was only stated. |
| Protected parts of a release | A longer list, decided from the changed paths (16.8). Since 0.29 it matters only when `updates.auto_install` is `unprotected`. | Most of what guards the machine was not on it. You then went further: every release that changes code waits (16.3). |
| Requests to the app | Must come from Crew's own pages. Agent-written text is shown without raw HTML. The apps domain is another domain. (20.3, 19.4) | Nothing was said, and a hosted project could have set Crew's cookie. |
| The ingress | Gets its routes from Crew's database, holds no Docker socket, and passes no secret or cookie to a hosted project (19.4) | styrman-crew's ingress does all three. |
| The firewall | Lists every range it drops, drops all IPv6 from sandboxes, and holds from the first packet (13.6, 16.10) | styrman-crew's IPv6 rules are best effort. |
| Reading the limits | With `/usage`, every 30 minutes. Paid credits stay off. (12.7) | Crew waited for an error to learn a limit. |
| The first install | One file with the host names, the admin email, the sign-in client, and the DNS token (16.11) | Setup is behind the sign-in they configure. |
| A run's disk | A fixed size, 20 GB (12.4) | One run could fill the disk the database is on. |

**Added in 0.28.** These follow from your answers, but you did not say them.

| Area | Default | Reason |
| --- | --- | --- |
| Browsing the models | A Models screen: every model the CLIs offer, by CLI and family, with its efforts, its limit group, the agents that use it, and its results. From there a model is given to an agent. (12.3, 20.1) | You want all of them to be browsable. |
| The agents | Each role is an agent with its own model, effort, prompt, tools, and sandbox. The Models screen lists them and what each runs on. (11.1, 12.3) | Different agents use different models. |
| The next choice at a limit | The next entry on the agent's own list whose group can run. Every agent's list has entries in both of `agy`'s groups of models. (12.3, 12.7) | A list per agent already existed for a model that is gone. |
| A check at a limit | Prefers another family than the builder's when that family has room; when the other family is at its limit or out of quota, it falls back to a model of its bracket in the builder's family that has room, and records an inbox notice and a signal (12.3, 12.7, since 1.26). A check whose model or whose builder's model is of a family Crew does not know waits, and the inbox says why (since 1.26). | You updated this rule in session 92: prefer another family when available, and fall back with a notice instead of waiting when it is not. |
| A run that was stopped by a limit | Continues on a model of its bracket in another group that can run (since 1.22; the agent's next choice before), in a new sandbox, and keeps it until it ends (12.7) | Changing back in the middle would cost a second rebuild of its conversation. |
| Taking over a prior host | The spike ran first; on any host with styrman-crew, styrman-crew's own uninstall removes it while keeping Docker and Kata, and Crew's host setup runs. (17.6) | Its uninstall has a level that does exactly this. |
| Reference host's real size | The spike read 12 cores with 16 threads, and 62 GiB RAM. (13.10) | Default sandbox counts derive from that benchmark, scaled to each target host at Setup (section 24, item 26). |

**Added in 0.29.** These follow from your answers, but you did not say them.

| Area | Default | Reason |
| --- | --- | --- |
| The installer | `crew-ctl`, a shell script like styrman-crew's `styrman-ctl`. It does the machine's part: `doctor`, `install`, `status`, `update`, `rollback`, `edit`, `uninstall`, `logs`, and `build`. What touches data it passes to the server's own program, `crew admin`. (16.11) | Proven on the reference host, with a dry run and three levels of uninstall. A script runs on a machine that has nothing yet. |
| What the installer asks for | A few values, in a `.env` file or asked one by one when it starts. Everything else is generated or has a default. Logins for the agents come later, in Setup in the app. (16.11) | You said: gather what is needed first. |
| Before anything is changed | `crew-ctl doctor` checks the machine and the answers, and install stops on a failure (16.11) | So that a half-installed machine does not happen. |
| What uninstall leaves | Level 2 keeps your data and the key that opens it. Level 3 removes them. Docker and Kata are removed only with a flag, and only if Crew installed them. (16.11) | Uninstall takes out what install put in, and nothing else. |
| Proof that it works | Install, run, update, roll back, uninstall, and install again on a fresh virtual machine, before every release (16.11, section 22) | Install and uninstall are features, so they are tested like features. |
| Fixed versions | One file, `versions.lock`, with every version and checksum Crew is built from. A release records what could not be fixed in advance, such as the version of `agy`. (16.12) | The same tag must give the same Crew on another machine. |
| No secret in the repository | No secret, ever. What differs between machines comes from the `.env` file. The gate scans for secrets. (16.12) | The images and the deploy files of a release go to every machine Crew is installed on. |
| A clean start | The copied parts come in as files. The history, the plans, and the bug records of styrman-crew and styrman-harness are not copied. (16.12) | Clean provenance under `THIRD_PARTY.md` and `LICENSE`. |
| These documents | They are in `docs/plan/` of the repository, with the pictures of the simulation (16.12) | You asked for them to be with the code. |
| Where an installation gets its code | A primary build host builds Crew from the repository named in Setup, seeded from `CREW_SOURCE_REPO` (e.g. `your-org/crew`). Secondary installations get finished releases from it and do not build Crew from a checkout. (16.12) | One place changes Crew's code. |
| Licence | Apache-2.0 (`LICENSE`), with third-party and provenance attribution in `THIRD_PARTY.md` (16.12). Originally chosen in 0.30, deferred in 0.31, and shipped for public open-source publication in 2.01. | Enables public open-source distribution of Crew's source repository. |

**Added in 0.29, after the review before the build.** Two independent reviews read 0.29 as the builder of the first milestones would. These are the choices I made from what they found (section 28).

| Area | Default | Reason |
| --- | --- | --- |
| Installing the primary host | You put a checkout on the machine, and `crew-ctl install --build` builds the images there. The installer stores no GitHub credential. (16.11) | At install time the machine has no GitHub login yet, and the first install has no release to download. |
| The order of an install | The answers first, then `doctor` on the machine and the answers, then the first change (16.11) | `doctor` cannot check answers it does not have yet. |
| `doctor` on a machine that has Crew | It knows three cases by itself: a fresh machine, a repair, and an update. Crew's own containers and ports are expected in the last two. (16.11) | Otherwise it fails on every machine it is meant for. |
| Where the secrets are | Everything in `/etc/crew/`: the answers, the key file, the generated secrets, and the record of the install. Level 2 of uninstall keeps all of it. (16.11) | An install after level 2 must open its own data again. |
| Who runs Crew on the machine | A system user `crew`. No account of a person joins the `docker` group. In every sandbox the user is `agent`. (16.11, 13.2) | styrman-crew uses the operator's own account for both, so that name is in every path. |
| What install does before the updater exists | A table per milestone (16.11). Until M14, `crew-ctl install --build` from a newer checkout is the update. | M1 cannot show what M14 builds. |
| The files between the backend and `crew-run` | A control folder `.crew/` in the run's home, with fixed names. One call of `crew-run` per turn. (12.4) | It was prose, and two parts are built against it. |
| A run's disk | Only the disk of the sandbox's own Docker has a fixed size in the first version. The disk guard bounds the rest. (12.4) | That is what styrman-crew's 20 GB is. A fixed size for the whole home is a question for the spike. |
| The sign-in page | At `auth.<CREW_HOSTNAME>`, as the copied ingress has it. Both names must point at the machine. (16.11) | The redirect address of the Google sign-in client needs it. |
| The ingress before hosting | One fixed route to the server, from its environment. From M19 it asks the server for its routes. (19.4) | It holds no Docker socket, so it needs another source. |
| The values of the install file | They are in `/etc/crew/crew.env` only, changed with `crew-ctl edit`, and shown in the app. `CREW_SOURCE_REPO` is only a first value for Setup. (16.11) | One value had two places. |
| `versions.lock` | `KEY=VALUE` lines at the root of the repository. A fixed list of places must equal it. Its first values are styrman-crew's. (16.12) | "Nothing else names a version" cannot be checked: `go.mod` and a Dockerfile must name one. |
| Names in the code | The Go modules keep `github.com/kesokaj/crew` and `github.com/kesokaj/crew/ingress`. Personal email domains, personal `/home/` paths, secret shapes, and binaries are blocked by `tools/check-clean.py`; recordings are scrubbed of tokens, account ids, and mail addresses before they become test files. (16.12) | Safe for a public Apache-2.0 repository even without gitignored scrub-value lists. |
| The first schema, and the plan | The plan documents are in `docs/plan/` of the repository, as you asked. (16.12) | They are the plan the build follows. |
| The spike | Each question names the milestone that answers it. M0 answers the ones M1 to M3 rest on. A second table says what Crew does if an answer is no. (section 24) | Many questions need parts of Crew that do not exist at M0. |
| The first schema | Only the tables M1 to M3 use. Each milestone adds its own. (15.3) | About sixty tables were named and ten were specified. |
| The install test | Runs with certificates of the ingress's own, a fake harness, and `crew-ctl selftest`, so it needs no DNS name, no browser sign-in, and no login of a CLI. (16.11) | A test on a fresh virtual machine cannot sign in to Google. |

**Added in 0.32.** You answered the five questions, asked for a chat on every ticket, and said you are ready to build. These are the choices I made from that.

| Area | Default | Reason |
| --- | --- | --- |
| Which agents Crew chooses for | The builder, the reviewer, the tester, the E2E tester, and the verifier. The lead agents, the assistant, the summary writer, the judge, and the prompt writer follow their list. (12.3) | Crew can measure the first five: checks pass or fail. It has no measure yet for a plan or a judgement. |
| How Crew gets numbers | One run in ten of an automatic agent tries another entry of its list. (12.3) | Without tries there is nothing to choose from. One in ten keeps the cost of a worse model small. |
| The brakes | At most one change for an agent in 7 days. Crew watches the next 20 runs and goes back if they are worse. (12.3) | With 20 runs a difference can be chance. |
| Your own choice | Choosing a model in the Models screen sets that agent to Set by you, and Crew then only recommends for it. (12.3) | You said that a manual choice overrides. |
| The lead queue | By class, then by the priority you set, then projects take turns, then by the ticket's own priority, then by age. Only your priorities are compared across projects (since 0.42). (7.9) | You said: a queue, and tasks have priorities. |
| Who answers in a ticket's chat | An assistant of its own, never the agent that works on the ticket. (11.9) | A question must not stop or steer a run. |
| From a chat to the work | Three buttons that only you press: Tell the agent, Make a ticket of this, and Remember this. (11.9) | An answer of the assistant is not an instruction to anyone. |
| A done ticket | A chat does not open it again. What should change becomes a new ticket, linked to it. (11.9) | A ticket only moves to the right. |
| Marked text | Goes to the assistant as a quote with its source. Also tell the agent is a switch, and it is off. (11.9) | You said the comment goes to the chat for the agent to analyse. |
| Improve text and Review text on the plan | They go to the planning agent, with the selection quoted. On your own writing the prompt writer answers. (7.2) | The plan is the planning agent's text. |
| A ticket you add | No proposal by itself any more. The menu on a selection is there. (7.2) | You said: in the planning stage, and a button on selected text. |
| Docker only | The daily dump is a job of the server's container. Every other installation gets images, never a checkout. (13.1, 16.12) | You said: everything runs in Docker. |
| When the chat is built | With conversations, in M20, which could be built any time after M8; built in 0.75 and 0.76. (section 23) | It uses the same sandbox and the same way to continue a conversation, and the lead slots of M8. |
| What decides for each agent | The builder: the share of its tickets whose command checks passed at the first request, from Crew's own check sandbox. An agent that checks: fewer escaped defects, and about as many fixes asked for. Since 0.53 no rule uses a cost. (12.3) | A checker that passes everything would otherwise look best. Found by the audit of 0.32. |
| A try | For the builder a whole ticket, for a checking agent one run. No tries during a comparison or the watched runs, and none for an agent you set yourself. (12.3) | A fix round should continue the same conversation. |
| A comment on a ticket with no run | It waits, and is in the prompt of the next run on that ticket. Crew starts no run for it. A comment on the plan still starts the lead. (11.6) | Two sections said two things. A run that starts only to read a comment costs quota. |
| What the chat's sandbox holds | The login of its CLI and the read-only GitHub token. No gcloud. Large, with Docker inside. (11.9) | It runs code that is not reviewed yet, and it is the one agent that reads other runs' trajectories. |
| Who reads a chat | You and the improver. No agent of the project. (11.9) | Otherwise asking would steer a run. |
| What a button carries | Text the assistant wrote keeps that mark when you send it to the agent, to memory, or into a ticket. A ticket made from a chat cannot skip Review. (11.9) | The assistant read trajectories that can hold fetched pages. Found by the audit. |
| Priorities | Urgent, high, normal, low. Everything starts as normal. The lead and you can change it, and an agent cannot set it for its own ticket. (7.9) | The queue goes by priority, so who sets it matters. |
| The deploy files of a release | One small archive in the release folder, with `crew-ctl` and what it needs. It is what you copy to another machine first. (16.12) | A machine with no checkout still needs the installer. |
| The test machine for the install test | A KVM virtual machine on the build host itself. (16.11) | No cloud account is needed. |
| What the spike may touch | Nothing on the running host. It starts throwaway containers of its own. Items 59 and 64, and the Kata 4 half of 13, moved to M1. (section 24) | Keeps any existing installation undisturbed. Found by the audit of the build kit. |

**Added in 0.31.** You answered six questions and asked for one new feature. These are the choices I made from that.

| Area | Default | Reason |
| --- | --- | --- |
| Open-source vs runtime images | Historically deferred in 0.31 and reinstated at design 2.01: Crew ships its source repository under Apache-2.0 (`LICENSE`, `THIRD_PARTY.md`) with automated cleanliness gates (`tools/check-clean.py`) and complete install/uninstall docs (`docs/install-and-uninstall.md`), while built runtime images stay local or in each operator's own private registry. (16.12) | Matches the public Apache-2.0 source release model while keeping vendor CLIs private. |
| Other installations | A primary build host builds Crew. Secondary installations get finished releases from it, build no Crew, and share nothing with it. (16.12) | One place changes Crew's code. |
| Lead slots when a CLI is full | A lead run waits for a slot of its own CLI. It does not change CLI to get one. (7.9) | A full CLI should not change which model does lead work. You confirmed it in 0.32: the run waits in the queue, which goes by priority. |
| When Crew changes a model | When another entry has at least 20 finished runs for the agent and did better. At most once in 7 days for an agent. (12.3) | Fewer runs say little. Every number is a setting. |
| Trying other entries | One run in ten of an automatic agent goes to another entry of its list. You can also start a comparison, which sends every second run. (12.3) | An agent runs on its first entry, so without this Crew has no numbers for the others. Changed in 0.32, when you chose automatic. |
| The improver and models | It proposes no model and no effort, and its patches may not touch one. The backend chooses from the numbers. (10.4, 12.3) | One place decides, and it is not an agent. |
| The sign-in of `agy` | The token file as before, used once a day by the login check and stored when renewed. (12.6) | The CLI documents no longer-lived sign-in for a consumer plan. Spike item 78. |
| Where prompts are rewritten | By itself: only the first message of a plan draft. On request: any text you select, with Improve text or Review text. A ticket you add no longer gets a proposal by itself. (7.2) | You said in 0.32: in the planning stage, and a button on selected text. A stop at every message would slow you down. |
| Who rewrites | A prompt writer of its own, with no tools, on a fast model: Gemini `flash` on `agy` first (falling back to Gemini `pro`; since 1.40/1.44). It takes no lead slot. (7.2, 12.3) | You wait for it. It only reads and writes text. |
| What a rewrite may do | It keeps every fact and limit, adds nothing of its own except marked assumptions, and asks at most three questions. You always choose what is sent, and your own text is kept. (7.2) | A rewrite that changes what you meant would be worse than none. |
| When a rewrite is late | After 45 seconds your own text is sent as it is. (7.2) | It must never hold you up. |
| Rewrites and other projects | The prompt writer sees earlier rewrites of the same project only. (7.2, 10.6) | Projects share nothing with each other. |
| What runs after a release | Your pins, project settings, `Set by you` roles, edits, removals, and Crew's watched or kept choices stay as they were; automatic roles sync offered models by `(family, bracket)` and `(version, first_seen)` order (since 1.44). (12.3) | A release never overwrites your model choices or empirical wins. Found by the review of 0.31. |
| What "better" means | For the builder: 8 points more passed the first time, and no more defects escaped. For an agent that checks: 8 points fewer of its runs let a defect escape, and its share of fixes asked for within 5 points. Each is a setting. Since 0.53 "as well, for less" is gone. (12.3) | Plain rules that can be built in M8. Crew watches every change it makes and goes back if it was worse. |
| A release on another installation | Always waits for Install, and is checked against the hashes it came with. The improver does not run there. (16.12) | That machine has no source to tell text from code by. |
| A client's machine | Gets images only, never a checkout. (16.12) | A checkout holds your plan and your notes. |
| A change to the prompt writer's rules | Waits for your OK. (7.2) | No eval measures a rewrite. |
| When the prompt writer is built | In M7, with the Plans screen. (section 23) | It needs the call with no tools of M6 and the screens of M7. |

**Added in 0.30.** You asked for room for other CLIs. These are the choices I made from that. Since 0.42 Crew runs `agy` alone, and the rows say what holds now.

| Area | Default | Reason |
| --- | --- | --- |
| How a CLI is added | One adapter per CLI, and nothing outside it names a CLI. `agy` is built. `codex`, `opencode`, and others are an adapter each, and none is built now. (12.2) | You said: make room, and focus on `agy`. |
| Which CLI an agent uses | It comes with the model. Each agent has a short list of entries, each a CLI with a model. (12.3) | You already give models to agents in the Models screen. A second choice of CLI on top would be one more thing to set. |
| The default lists | Gemini and Claude models through `agy`, ranked per `(family, bracket)`: Gemini (`pro`/`flash`) for planning, building, testing, and helper roles, and Claude (`pro` for code/verification checks, `flash` for `docs`/`chore` reviews and `judge:` checks) with same-bracket Gemini fallback (since 1.44). (12.3) | One group can be at its limit and those agents still run. You confirmed in 0.31 that the CLI runs signed in as you. |
| Choosing by results | Automatic since 0.32, for the five agents whose work is measured. In 0.31 Crew only recommended. (12.3) | You said so in 0.32. |
| What a sandbox holds | The login of the one CLI it runs. A project terminal holds the login of every CLI that is signed in. (12.6) | A tricked agent can copy what its sandbox holds. |
| A CLI that is signed out | Its runs move to their agent's next choice on a CLI that is signed in, or wait, exactly as at a limit. With `agy` alone they wait. (12.7) | It is the same situation: this CLI cannot run now. |
| The check rule | Counts the family, not the CLI or the group of models. Claude Opus and Claude Sonnet are one family, and GPT is another in the same group. (12.3) | The rule is about a different model looking at the work. |
| Lead slots | Counted for each CLI, changeable: 1 for `agy`. (7.9) | You said so in 0.31. |
| The harness | One source in Claude Code's format, built for `agy`. (12.1) | It is the format styrman-harness keeps. |
| The module's name | `github.com/kesokaj/crew`. (16.12) | You confirmed the repository's name, so the code can carry it. |
| What others wrote | Listed in `THIRD_PARTY.md` with its source and licence under Crew's Apache-2.0 outbound `LICENSE`, with third-party licence compliance maintained before release. (16.12) | Copied code, packages, and skills include MIT, ISC, BSD, and Apache-2.0 notices. |

**Added in 0.25.** These follow from your answers, but you did not say them.

| Area | Default | Reason |
| --- | --- | --- |
| Which database | PostgreSQL at a fixed version, in the container `crew-db` (15.3) | You asked for a database in its own container. |
| The `board` branch and the instance repository | Removed. Their content is rows in the database. (6.1, 10.3) | You said everything that is saved stays in the database. Two stores for the same thing would have to be kept equal. |
| What stays in git | The code, the documentation, and a project's own skills and context (6.5) | They are source. You clone, review, and push them. |
| What stays as files | Docker images, the data of hosted projects, the backup files, and the key that encrypts secrets (6.5) | They are large, belong to another program, or protect the database. |
| Trajectories | Every event is a row, kept for good. The table is split by month. (8.3) | Trajectory tracking is vital, so it is in the brain and not beside it. |
| The CLI's own conversation files | Saved in the database after every turn, and put back into the next sandbox. A run's home is removed with its sandbox. (12.10) | A conversation must be continued from what the database holds. |
| Credentials | In the database, encrypted with a key that is a file on the machine (15.3) | A copy of the database must not be a copy of your logins. |
| History | Every change is a row in `changes`, never changed or removed (6.4) | It replaces the commit history of the `board` branch. |
| Moving a project | `crew-ctl project export` and `import`, as a folder of files (6.2) | The repository alone no longer holds the project. |
| Looking inside | `crew-ctl db shell`, with a role that can only read, and a Database screen in the app (15.3) | You know databases. Nothing writes around the server. |
| Memory for the database | 2 GB, out of the machine's reserve of 8 GB (13.10; 4 GB before 1.08) | The sandbox count stayed the same on a 64 GB host; since 1.08 the server's ceiling takes one (13.10). The spike measures it. |
| A snapshot before an update | A dump of the database, tried on a scratch copy first (16.4) | It replaces copying files. |
| A roll back that needs an old snapshot | Only by itself inside the ten-minute watch. Later, the operator types the version's name after Crew has shown what would be lost. (16.6) | Since 0.25 the snapshot holds the tickets and the records too. |
| The GitHub App | One permission, reading contents. Each sandbox gets a token for one hour, for its own project's repository and for the others that project names. (12.6) | You chose my recommendation. Since the audit of 0.27 the token no longer opens every repository: projects share nothing unless you say so. |

**Added in 0.22.** These follow from your answers, but you did not say them.

| Area | Default | Reason |
| --- | --- | --- |
| `gh` | Signed in. You decided this in 0.23. It stays with the server. Crew keeps working when it is not signed in: you then create the empty repository on GitHub yourself, tickets are merged with a merge commit, and a release is a tag. (6.6) | A key carries git and nothing else. |
| How a credential reaches a sandbox | Copied in at start, not linked (12.6) | A link would be a file that sandboxes share, and they share nothing. |
| Which Linux | Debian 13 or a current Ubuntu LTS. The host setup refuses anything else. (13.2) | You said Debian or Ubuntu. |

**Added in 0.21.** These follow from your answers, but you did not say them.

| Area | Default | Reason |
| --- | --- | --- |
| A stage that fails | The ticket stays in its stage. A fix subtask runs there, and the stage then checks again. (7.4) | A ticket only moves to the right. |
| Skipped tickets, and new features during their notice | They stay in Analysis, marked, and do not count in its limit (7.4) | The same rule: neither can go back to Backlog or Planning. |
| Replacement tickets | They do not count as added work. A replacement is not replaced again. (7.7) | They are the same work, cut differently, and the chain must end. |
| Lead slots | 2 for each CLI; 1 for `agy` since 0.33 | Your number: in 0.22 for one CLI, in 0.31 for each. The spike found that your `agy` plan carries about one run without a break (section 24, item 35), so its slots followed that, as section 24 says. You raise one when its subscription has room. |
| Sandbox sizes (reference 64 GB host) | Small: 2 virtual CPUs and 2 GB. Large: 4 and 6 GB. About 15 at once (16 before 1.08). The build of a release: 4 and 8 GB, since 0.60. (13.10) | 64 GB RAM bounds that host's concurrent count. |
| Which credentials a sandbox gets | The login of the one CLI it runs, the git identity, and a temporary GitHub token that can only read; no gcloud since 0.79. The improver gets only the login of its CLI. (12.6) | Changed in 0.25: the SSH key and the `gh` login stay with the server. |
| Where a project keeps its skills and context | In its own repository, under `.agents/` and in `AGENTS.md`. A skill is added by a ticket. (12.9) | styrman-harness already works this way, so a project behaves the same inside and outside Crew. |
| News by mail | One mail a day, and four kinds at once (18.1) | With mail as the only channel, one mail per event would be too many. |
| Backups | One tar.gz per day in `/var/lib/crew/backups`, the last 14 kept (16.9) | You said local, as tar.gz. |
| What a conversation may do | Read its project, do anything in its own sandbox, and propose a ticket when you ask, which is analysed like any ticket an agent created. It never changes `main` or the board. (11.8) | You said Crew should do things from a conversation. Work that lands still goes through the board, so the same checks apply. |
| A conversation's sandbox | Removed after 30 minutes without a message. At most 3 at once, conversations and chats of tickets together (11.8, 11.9) | The 64 GB reference host has room for about 15 sandboxes (16 before 1.08, 13.10). |
| How a conversation is continued | With a new call in the same sandbox, else in a new sandbox with the CLI's own files put back from the database, else rebuilt from the turns (12.10) | The first two are what the CLI offers. The third is what makes it work when it cannot. |
| Which runs share a conversation | The same role on the same ticket. A check always starts new. (12.10) | A builder must remember. A reviewer must not. |
| How long turns and trajectories are kept | For good. Both are in the database. | You said conversations must be saved in the database, and in 0.26 that trajectories stay until feedback and evals have used them. |
