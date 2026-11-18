## 11. Agents

### 11.1 Roles

| Role | Works on | Produces | Changes code |
| --- | --- | --- | --- |
| Curator (`curator`, replacing `lead` since 1.42) | The analysis of each task (when not fast-pathed by Crew). Looking at a stuck task again. The final report. Housekeeping and retrospectives. | Analysis decisions, new approaches or replacement tickets, the report, memory curation, documentation tasks, proposed eval cases and overrides | No |
| Planning agent (`planner`) | A plan draft with the operator, and the plan breakdown (on Gemini since 1.42) | The plan draft, the breakdown, and its tasks in Planning with a why, a what, a goal, and checks | No |
| Builder | One task in In progress | Commits on the task branch, with the documentation the task names, and a note | Yes |
| Reviewer | One task in Review. The analysis of tasks the curator created. | A review with a verdict | No |
| Tester | One task in Test | A test entry with a result and output | No |
| E2E tester | One task in E2E | An end-to-end entry with a result, logs, and screenshots | No |
| Verifier | One task in Verification | A verdict on whether the result does what the goal and the source plan ask | No |
| Improver (Crew level) | The signals from all projects | Improvements to the baseline | No |
| Prompt writer (Crew level) | A prompt the operator wrote in planning, or text the operator selected | A rewrite, with what it changed, its assumptions, and its questions, or notes on the text (7.2) | No |
| Assistant | A conversation with the operator, or the chat of a ticket | Answers, things tried in its own sandbox, and proposed tickets (11.8, 11.9) | No |

Every project role can ask the Crew MCP about the project and create a task. A task is never reviewed, tested, or verified by the run that built it (R20). A role exists in a project only when its stage is on. With Review off, a ticket the lead created is analysed by a second lead run that did not create it.

Roles are markdown files in Crew's baseline, so a role is changed with one commit and every project gets the change. A project can override a role for itself (10.3). Each role has its own model and effort settings (12.3), and the task view shows which model and effort did which subtask. The improver is not a project role: it runs at the Crew level and never works on a task (10.4).

### 11.2 Run lifecycle

A run is one agent conversation with one goal, inside its project's warm Kata microVM (`crew-proj-<project>`, with an isolated git worktree `/home/agent/worktrees/<run-id>` and an `agy acp --stdio --process-per-session` session; since 1.37) or in its own ephemeral microVM for non-project runs. The steps of starting and following the CLI are in 12.4.

1. The orchestrator creates the subtask (7.11) and its run record, with the Crew version, the CLI and its version, and the model and the effort the run will use (12.3). The CLI's version is what its login check read in the running release's image; a run in a candidate's runtime image, a gate's real ticket or a release suite's, records what that release's build read of the image (its `release.json` pins), and none when no release names the image (since 0.57). That is the candidate image's own report of its CLI, checked for a version's shape and nothing more: the image is built from the candidate's `image/Dockerfile`, which agents may have written, and a change there waits for the operator's press before any live step (16.2 step 5). It checks the limits: a lead slot for lead runs, the stage's WIP limit, room on the machine, that the run's CLI is signed in, and that the group of the run's model is not at its subscription limit (12.7).
2. It builds the prompt from Crew's stage prompt with the project's overrides applied, the task's why, what, goal, and checks, the part of the plan the task comes from, the relevant records, the project's memory index for that role, the documentation pages the task names, and any unresolved comments. The prompt is saved with its parts (8.6). Everything Crew wrote for the run is in the prompt: the stage prompt and the agent definition of the role, so the saved text and its hash cover them. The skills, the global context, and the CLI's own subagents are not in the prompt: the CLI loads them from the harness in the run's home. An override replaces one whole file of the baseline for a project. It is not a patch.
3. The run driver prepares the run's control files and isolated git worktree (`/home/agent/worktrees/<run-id>`), boots or unpauses the project's microVM (`EnsureProjectSandbox`), and starts the ACP session through `crew-run acp-turn` (or `crew-run turn` in an ephemeral microVM, 12.4). The project's setup command runs first for a role that builds or runs the project's code: the builder, the tester, and the E2E tester (since 0.47). The roles that only read, the lead, the reviewer, the verifier, the judge, the summary writer, and housekeeping, do not run it.
4. Every line or ACP `session/update` notification the CLI writes becomes an event. The event sink writes the trajectory, feeds the monitor, and pushes to the UI.
5. When the agent finishes, the goal loop in 11.3 runs.
6. The backend fetches the run's branch from the quarantine repo, writes the run record, saves the CLI's own conversation files in the database, closes the ACP session, and removes the run's worktree (`/home/agent/worktrees/<run-id>`). The project's microVM stays warm for the next session, suspends (`ContainerPause`, `0% CPU`) after `sandbox.suspend_after_minutes`, and stops to `0 MB` RAM on persistent `<CREW_DATA_DIR>/projects/<id>/home` when the project is idle (13.3, since 1.37). A later run on the ticket continues the conversation via `session/load` on the project's persistent home or from the database (12.10).

Work that is not pushed is lost when a microVM dies by itself. The builder's prompt tells it to commit and push as it goes, and "the branch is pushed" is an exit check. When Crew itself stops a run, for a limit, a sign-in, or because you asked, `crew-run` first commits what is in the workspace and pushes it to a place only the backend reads, `refs/crew/wip/<run>`. The run that continues starts from it.

### 11.3 Goal loop

1. The agent works, then requests a move.
2. The backend runs the exit checks.
3. If they pass, the move is applied and the run ends.
4. If they fail, the failing checks and their output are sent back into the same conversation, and the agent continues.
5. After `max_goal_rounds` failed rounds, or on a limit from the watchdog, the run ends. The subtask is retried once, and after that the lead looks at the ticket again.

The loop is Crew's own, driven by its checks, because commands and independent reviews are stronger evidence than a transcript. A failed round is a new call of the CLI that continues the same conversation, in the same sandbox (12.4, 12.10). A `judge:` check is one headless call of a CLI with no tools. It runs in a small sandbox of its own, which gets the login of that CLI and nothing else (13.3).

While Crew runs the exit checks and the judge, the builder's run waits for its next turn, and it ends after its idle time as any run does (ten minutes), also while its project is paused. A builder whose turn asked to move and whose run then ended so is taken up as if that turn had just ended, once: its checks and its judge start, the ticket moves when they pass, and a failed round continues the same conversation in a new microVM (12.10). At `max_goal_rounds` such a run is ended for its rounds, as one that waits is, and its subtask is retried once (since 0.55, the operator's answer 1 of report 10: on dave a pause between the builder's `request_move` and its checks left CREW-1 waiting for a judge that never started, and a check and a judge that take longer than the idle time did the same). Once Crew acted on its turn, the run is marked taken up, so a ticket that waits for room or for its merge is not taken up again; it is marked also when its ticket moved on from In progress before Crew came back to the run, by the merge that the judge's answer let start or by the board's move to a later stage, and a ticket that was closed leaves it as it ended (since 1.16, BUG-028).

**How each kind of run ends.** A tool call only records what the agent says. Crew acts when the CLI has printed its `result`, so a run is never cut off by its own call.

| Run | The call that reports | What Crew does after the result | When the call is missing |
| --- | --- | --- | --- |
| Builder, the first build | `request_move` | Runs the exit checks. Pass: the ticket moves. Fail: one more round. | One more turn that asks for the call. If it is still missing, the run has failed. |
| Builder, fixing in place | `fix_done` | Runs the check that failed, then starts the stage's agent again. The ticket stays where it is. | The same |
| Builder of one parallel subtask | `subtask_done` | Runs that subtask's own check and joins its branch (7.11) | The same |
| Reviewer, verifier | `write_review`, `write_verification` | Approved: the ticket moves; a review before the merge in a later stage lets the merge go ahead (7.4). Changes asked for: a fix subtask for the builder, and the ticket stays. | The same |
| Tester, E2E tester | `add_test_result`, `add_e2e_result` | Crew runs the ticket's checks itself, in a check sandbox (13.9). That result decides, not the entry. | The same |
| Lead, in Analysis | `analyse_ticket` | Applies the decision | The same. The ticket then stays queued, and the lead queue goes on. |
| Lead, looking at a ticket again | `new_approach` or `cancel_and_replace` | Applies it (7.7) | The same |
| Lead, other work | `write_breakdown`, `write_report`, or the housekeeping tools | Stores it | The same |

### 11.4 Parallelism

Tasks run in parallel: each task run has its own conversation, its own ACP session (`agy acp --stdio --process-per-session`), and its own git worktree (`/home/agent/worktrees/<run-id>`) inside the project's Kata microVM (since 1.37), within the WIP limits of the stages and the room on the machine. Subagent runs are not counted against the global limit on lead runs.

Subtasks run in parallel inside one task too. Those that do not depend on each other each get their own run, ACP session, worktree, and branch, up to `max_parallel_subtasks`, and the backend joins them (7.11).

`agy` can start subagents inside a session, and the harness ships seven of them. A subagent shares that run's worktree and sandbox, since it is part of the same run. Each one is recorded as a helper subtask (7.11).

### 11.5 Crew MCP tools

Each call carries the run's token, which limits it to that run's project. A write to a ticket's record is limited to that run's own ticket.

| Group | Tool | Effect | Allowed for |
| --- | --- | --- | --- |
| Ask | `get_project`, `get_plan` | The project, its goal and phase, the original plan, and amendments | All |
| Tickets | `list_tickets`, `search_tickets`, `get_ticket` | Tickets by stage, kind, part, link, or text, and one ticket with its record, subtasks, and history | All |
| Tickets | `create_ticket` | A new ticket in the backlog, with a title, what was seen, and why (7.5) | All, and the assistant in a conversation |
| Tickets | `link_tickets` | A link between two tickets: depends on, duplicates, created from. A link that would close a circle of dependencies is refused. A bug's fix ticket is linked by `report_bug`, and a ticket that replaces another by `cancel_and_replace` (since 0.42). | All |
| Tickets | `report_bug` | A bug, and a linked fix ticket in the backlog. It comes with the bug records, in M12; until then a bug outside the ticket is a `create_ticket` of kind fix (since 0.42). | All |
| Ask | `get_summary`, `search_summaries` | Summaries of finished tasks | All |
| Docs | `search_docs`, `get_doc` | The project's documentation pages | All |
| Docs | `record_decision` | A decision record. It is a row at once. Crew adds its page to the documentation in the ticket's merge commit. | All |
| Skills | `list_skills`, `request_skill` | The skills this run has, from the base and from the project. A request for one that is missing, which becomes a ticket (12.9). | All |
| Ask | `search_memory`, `get_memory` | Memory entries | All |
| Ask | `list_bugs`, `list_feedback`, `list_eval_results`, `list_decisions`, `get_metrics` | The rest of the project's knowledge | All |
| Subtasks | `set_steps`, `tick_step` | The checklist of the run's own subtask | All |
| Subtasks | `create_subtask` | A queued subtask on the run's own ticket, with a title, what to do, what done looks like, a role, the files it touches, and what it comes after. It gets its own run in its own sandbox, in parallel with others where it can (7.5). | Curator in Analysis, builder |
| Subtasks | `add_helper` | A helper subtask under the run's own subtask, for a helper sub-agent in the same sandbox | All |
| Record | `add_note`, `attach_asset`, `reply_comment` | A note, a file, or a reply on the run's own task | All |
| Record | `write_review` | A review with a verdict | Reviewer |
| Record | `add_test_result` | A test entry with a result and output | Tester |
| Record | `add_e2e_result` | An end-to-end entry with a result, logs, and screenshots | E2E tester |
| Record | `write_verification` | A verdict against the goal and the source plan | Verifier |
| Record | `remember` | A memory entry | All |
| Record | `give_feedback` | Feedback on another role's work | All |
| Move | `request_move` | Says the build is ready to leave In progress (11.3) | Builder |
| Move | `fix_done`, `subtask_done` | Says a fix in place, or one parallel subtask, is ready to be checked (11.3) | Builder |
| Curator | `new_approach`, `cancel_and_replace` | For a ticket that cannot get through: one new approach as subtasks on the same ticket, or the ticket closed as cancelled with the tickets that replace it (7.7) | Curator |
| Decide | `decide` | Records a choice the plan does not settle: the question, the alternatives, the choice, and the reason. The run carries on, and the operator can change the decision later (7.5). | All |
| Plan | `write_plan_draft`, `write_breakdown`, `propose_tickets` | The plan draft, the breakdown, and its tasks in Planning. When the breakdown is revised, `propose_tickets` also changes a task the planning agent proposed in place, keeping its number and what depends on it, and withdraws one with a reason (since 0.38; moved from `lead` to `planner` on Gemini in 1.42) | Planning agent (`planner`) |
| Analysis | `analyse_ticket` | Sets the why, what, goal, checks, approach, the paths the ticket may change, documentation pages, skipped stages, part, and priority, and approves, skips, discards, or gives notice of a new feature. Approving a ticket that fixes a bug, it names the Done ticket the defect is in, or none (`bugIn`, since 0.54) | Curator, or a reviewer for the curator's tasks. Never the creator. |
| Curator | `write_report`, `update_memory`, `retire_memory`, `resolve_feedback`, `propose_eval_case`, `propose_change` | The final report and housekeeping | Curator |
| Map | `get_crew_map`, `list_projects`, `get_inbox` | The live Crew map across projects (active projects, stage counts, running sandboxes and runs, the lead queue, and the inbox), the non-archived projects, and the inbox items | The assistant in a normal conversation outside a project, a personal token |
| Chat | `get_subtask` | A subtask of the chat's ticket, such as SF-8.2, with its runs and the prompt of each in its parts, as saved: its ten newest runs, each prompt at most 64 KB | The assistant in a ticket's chat, and in a normal conversation outside a project (with `project`) |
| Chat | `get_trajectory` | A run's events, up to the last of a run that is going: at most 500 a page, each at most 4000 characters, with `after` for the next. For a chat, only a run of a subtask of its ticket; for a normal conversation outside a project, a run of a subtask of the project it names; for the improver, any run of a project or of Crew, never a conversation's or a chat's (10.4) | The assistant in a ticket's chat or a normal conversation, the improver |
| Chat | `get_checks` | The ticket's checks, and the newest run of each purpose of its check runs, with each command's exit code and the last 8000 characters of its output; a run's detail and its commands' errors as 13.9 says, Crew's own error as its fixed reason (since 0.93) | The assistant in a ticket's chat, and in a normal conversation outside a project (with `project` and `ticket`) |
| Chat | `get_diff` | The ticket's branch against `main`, or its merge commit once Done: every file with its lines added and removed, and the patches, at most 200 KB in all; `path` gives one file's | The assistant in a ticket's chat, and in a normal conversation outside a project (with `project` and `ticket`) |

The improver has none of these tools. It uses a separate tool set at the Crew level, with its own kind of token (10.4).

**The assistant is not one of "All".** In a conversation about one project its token carries the read tools (the Ask group, `list_tickets`, `search_tickets`, `get_ticket`, `search_docs`, `get_doc`, and `list_skills`) and `create_ticket`, which makes a ticket of origin `conversation` that names the conversation and the run; one run of the assistant creates at most ten, counted with an agent's. In the chat of a ticket its token carries the same read tools and the four of the Chat group, each bound to that one ticket and refusing another's subtask or run. It carries no tool that writes (11.9). In a normal conversation outside a project (since 1.33, 11.8) its token carries `get_crew_map`, `list_projects`, `get_inbox`, and the read tools and Chat inspection tools with `project` named (`ticket` too for `get_checks` and `get_diff`), reading any non-archived, non-hidden project and refusing every tool that writes (`create_ticket` included). `get_ticket` returns nothing of a chat.

- **One server, one address.** The server speaks MCP over HTTP at `/mcp` on the sandbox's bridge. `agy` is given it in its configuration file (12.2).
- **Every call is recorded**: the run, the tool, the arguments without large bodies, the result, and how long it took. The Crew MCP screen shows them live, and they are part of the trajectory.
- **No tool an agent has reaches outside its project.** No tool creates a project, installs or holds a release, deploys a hosted project, or changes settings.

### 11.6 Comment loop

1. The operator comments on the plan, or sends a comment to the agent of a ticket with Tell the agent (11.9).
2. The backend saves the comment as a row, with its row in `changes`.
3. On a ticket with a run that is going, the comment is sent into the running conversation. In the project's `agy acp` server (`crew-run acp-steer`, since 1.37) the comment is delivered mid-turn as a live `session/prompt` steer on the active `sessionId`, and is also queued for the next turn when a turn ends before the steer arrives (since 0.39).
4. On a ticket with no run, the comment waits with the ticket. It is a part of the prompt of the next run on that ticket (8.6). Crew starts no run only for a comment on a ticket. A comment on the plan or on the breakdown is different: it starts the lead, which continues its last conversation in a new sandbox, with the CLI's own files put back from the database (12.10).
5. The agent answers with `reply_comment`, and acts on it if needed.
6. A ticket that is Done, cancelled, or discarded takes no comment for an agent. What should change there becomes a new ticket (11.9).
7. A comment on marked text goes to the chat of the ticket, where the assistant answers. It reaches the working agent only when the operator sends it on (11.9).

### 11.7 The Crew MCP for the operator's own tools

The same server can be used from outside Crew, by the operator's own tools, such as an agent on a laptop (R156). It is a way to hand Crew work without opening the app.

| | |
| --- | --- |
| Address | `https://<Crew's address>/mcp`, through the ingress |
| Sign-in | A personal token, created in Crew while signed in, shown once, and sent as a bearer header. A tool cannot do a browser sign-in, so this one path is not behind the Google sign-in. The server checks the token itself. |
| What a token names | The projects it may use, and whether it may answer |
| Git | A token can also get the git switch. With it, `git` on your own machine can clone, fetch, and push a local project's repository over HTTPS (6.7). It is off for a new token. Not built in M9: a push of `main` from outside needs a held `main` of its own, as the terminal's (13.11); the operator deferred it (since 0.42). |
| The tools | The read tools of 11.5 with a project named, and `get_crew_map`, `list_projects`, `get_inbox`, `create_ticket`, and `comment`. With "may answer": `answer_new_feature` and `change_decision`. |
| What it can do | List the projects. Read the plan, the tickets, their records and summaries, and the documentation. Create a ticket. Comment: the comment reaches the agent as data with the token as its author, never as your instruction, since the tool is itself an agent. Read the inbox. |
| With "may answer" switched on | Also approve or discard a new feature before it goes ahead, and change a decision an agent recorded. It is off for a new token. |
| A ticket created this way | Is analysed like a ticket an agent created, and marked as created through the MCP (origin `token`). It is approved by adding it (7.3) only when the token has the answer switch; even then it cannot skip Review, and the files that instruct agents in its `may_change` wait for your yes (13.8). A leaked token can then add work, and cannot approve it. A token has at most ten tickets waiting for analysis in a project, and creates at most fifty a day; on one ticket, or on the plan, at most twenty of its comments wait for an answer. A cap's count and what it allows are one step, so calls at once cannot pass it together. No token covers Crew's own project unless you name it. |
| What it can never do | Give a project its OK. Install, hold, or roll back a release. Deploy, or change who may open a hosted project. Change settings. Sign in. Create tokens. Read any credential. These stay in the app. |
| Revoking | Any time, in Crew. Every call is recorded with the token's name, on the Crew MCP screen. |

Adding it to a tool: give the tool the address and the header.

```
server:  https://<Crew's address>/mcp
header:  Authorization: Bearer <token>
```

For `agy` on a laptop that is an entry in `~/.gemini/config/mcp_config.json`, with `serverUrl` and `headers` (12.2). Any tool that speaks MCP over HTTP works the same way.

Why answering is a switch and not the default: the tool that holds the token is itself an agent. With the switch on, an answer through the MCP is an answer by that agent in your name. It is recorded with the token's name, so it can be told apart from an answer you gave in the app.

A token for agents and a token for the operator are different kinds. A run's token never works on this path, and a personal token never works from a sandbox.

**Rules for a personal token (R168).**

- It runs out after 90 days unless it is renewed, and it is stored only as a hash.
- It is limited to 60 requests a minute, every request counted.
- The list shows when it was last used, and from which address.
- Five wrong tokens from one address block that address for an hour: its wrong tokens are refused at once. A right token is not guessed, so it still works from there (since 0.42).
- The rules of this path are a protected part (16.8).

### 11.8 Conversations with the operator

Not everything is a ticket. A talk about one ticket is its chat (11.9). Sometimes the operator just wants to talk: run a normal conversation outside any single project with full read-only access to everything running in Crew and the map, ask about one project, think something through, or have something tried. A conversation is that, with an agent that can really do things, in a sandbox of its own (R184).

| | A conversation |
| --- | --- |
| About | No single project (a normal conversation across Crew and the map, the default since 1.33), or one project. |
| The agent | The assistant, on the first entry of `agents.roles.assistant` that can run, unless the operator picks another. The picker lists what the CLIs offer (12.3). |
| The sandbox | Ephemeral. It starts with a message and is removed after `conversations.idle_minutes` (30) without one, or when the operator closes or saves the conversation. The next message starts a new one, and the conversation goes on (12.10). |
| What is in the sandbox | About a project: a read-only copy of that project's `main`, and a work folder of the conversation's own, `/home/agent/conversation`. About no project (normal conversation): the work folder, the Crew MCP for the whole machine (`get_crew_map`, `list_projects`, `get_inbox`, and every read and inspection tool by naming `project`), and read-only git access to clone `main` of any non-archived, non-hidden project from Crew's git endpoint into `/home/agent/conversation/<repo>` (since 1.33). The work folder is saved in the database with the conversation after every turn that changed it, as an archive of at most 200 MB of files and 20000 entries, and put back when the conversation continues. Only folders and regular files are kept: a link or a special file is left out, and the Sandbox card says so. The values of the credentials the sandbox was given are taken out of its files before it is saved, so a login the agent copied there is not stored, and what is saved is sealed with the key file, so a login copied in another form is not readable in the database or a backup either; one that does not open with the key file is not put back, and the Sandbox card says so (since 0.76). A folder over its limits is not saved, and the one saved before stays. A folder swapped for a link while it is read is not saved. |
| Network and credentials | About a project: what a run of that project gets. About no project: `internet` by default or `model_only` when chosen at start (bounded by the machine's network ceiling, 13.6), the login of its CLI, the git identity, and its run token configured for Crew's git endpoint and MCP. No gcloud (since 0.79, when the setting `conversations.credentials` was retired). |
| Saved | Every turn in Crew's database, until the operator deletes the conversation. Every event in its trajectory (8.3). Pressing Save marks `saved_at` (`Saved` filter on `/conversations`) and closes an idle sandbox so its memory is freed while keeping every turn, the CLI's files, and the work folder. Conversations are in the backups (16.9). |
| A turn | Takes a lead slot while it runs, first in the queue, because the operator is waiting (7.9) |

**What it can do, and what it cannot.**

| It can | It cannot |
| --- | --- |
| In a normal conversation outside a project: read the live Crew map (`get_crew_map`), the non-archived projects (`list_projects`), the inbox (`get_inbox`), and any non-archived project's plan, tickets, records, subtasks, trajectories, checks, diffs, documentation, and memory by naming `project`, and clone `main` of any non-archived project read-only into `/home/agent/conversation/<repo>` (since 1.33). About one project: read that project's code, plan, tickets, documentation, and memory, and see no other project. | Push to `main` or to any branch of a project, or change a board by itself |
| Do anything inside its sandbox: clone, build, run, test, write files, search the internet if the network mode allows | Move a ticket, give a project its OK, deploy, install a release, or change settings |
| Move work into a project's backlog: in a conversation about a project, propose a ticket (`create_ticket`) or press Make a ticket of this; in a normal conversation outside a project, press Move to project backlog (`POST /conversations/{c}/ticket` with `project`) and choose which project's backlog receives the ticket. It is your ticket, approved by adding it (7.3), with origin `conversation`, so its Review cannot be skipped (7.4). | Start runs on tickets. The pipeline does that. |
| Draft a comment for a ticket, or a memory entry. About a project, it is written when you press Tell the agent or Remember this, as in the chat of a ticket (11.9); the `/tell` and `/remember` API routes also accept `project` for a normal conversation. | Create a project. Only the operator does. |
| Become a plan: a normal conversation outside a project can be turned into a plan draft (`POST /conversations/{c}/plan`), with the conversation as its source (7.2), even if earlier turns also moved tickets into an existing project's backlog | |
| Be saved (`POST /conversations/{c}/save`) or discarded in one step (`DELETE /conversations/{c}`, which stops any running turn and removes the sandbox and conversation at once, since 1.33) | |

- **Work that should land goes through the board.** A conversation never changes a project by itself. What it finds or prototypes becomes a ticket, and the ticket gets a builder, a review, and the checks like any other. So talking is free, and `main` is still protected by the same rules.
- **How many.** At most `conversations.max_sandboxes` sandboxes are open at once. When one more is needed, the one that has been idle longest is removed. Its conversation stays.
- **Where they are.** The Conversations screen lists every conversation with what it is about, its agent, its turns, and whether its sandbox is running. The list can be searched.
- **What the assistant is told.** Its instructions are the baseline's `agents/assistant.md`, then `stages/talk.md` for a conversation or `stages/chat.md` for a ticket's chat, a project's own overrides of them where it has them (10.3), then the operator's messages. A text the operator marked reaches it in a fence, as data, with where it was marked; the operator's own words follow it.

### 11.9 The chat of a ticket

You asked for this in 0.32. Every ticket, running or done, has a chat where you can ask what happened, and ask questions while it runs. You can also mark text and write a comment that goes to the chat, for the agent to analyse (R207, R208).

| | The chat of a ticket |
| --- | --- |
| Where | In the task view (20.2), for every ticket, in every stage, and after it is closed: Done, cancelled, or discarded. There is one chat per ticket, and it is kept as long as the ticket. Before a ticket was analysed, the assistant has little to read. |
| Who answers | The assistant: an agent of its own, in a conversation that belongs to the ticket. It is never the agent that works on the ticket. So a question does not stop, slow, or steer a run. |
| What it reads | This ticket, through the Crew MCP: the why, the what, the goal, and the checks, every subtask with its prompt, the trajectories up to the last event of a run that is going, the notes, the reviews, the test and check results, the decisions, the commits and the diff, and the summary. And the project's code at the ticket's branch, or at its merge commit when the ticket is Done. It is the one project agent that reads the trajectories of other runs (8.3). |
| Its tools | The read tools every project role has (11.5), and four more that only the token of a chat carries, each bound to this one ticket: `get_subtask` with the saved prompt, `get_trajectory`, `get_checks`, and `get_diff`. Its token has no tool that writes. |
| Its sandbox | A conversation sandbox (13.3), large, with Docker inside, like a builder's, because it builds and runs the code and the tests to find out why something failed. It has a read-only copy of the ticket's branch, fetched again at every turn, and a work folder. A ticket with no branch yet gets `main`. The branches of parallel subtasks can be fetched too. Its git token can only read. The sandbox is removed after `conversations.idle_minutes` (30) without a message and comes back with the next one (12.10). |
| Network and credentials | The network mode of the project. The login of its CLI, and the read-only GitHub token for the project's repositories. No gcloud. It runs code of a branch that is not reviewed yet, so it holds less than a run (12.6). |
| Its model | The assistant's, `agents.roles.assistant`, which starts as the lead's list. You can pick another in the chat (12.3). |
| A turn | A lead run of the class `operator_waiting`. It takes a lead slot of its CLI, and goes first in its class whatever the ticket's priority, because you are waiting (7.9). |
| Saved | Every turn, in the database, with the ticket. The ticket's history shows that you asked. Like the trajectories, a chat is not in a project export (6.2), and it is deleted with its project. |
| Who can read it | You, and the improver. No agent of the project can: `get_ticket` does not return a chat, and neither does a personal token. So asking cannot steer a run. |
| Use | It counts toward no limit of the ticket or of the project, and since 0.53 no cost is shown. |

**What it can do, and what it cannot.** The same as a conversation about a project (11.8), narrowed to one ticket.

| It can | It cannot |
| --- | --- |
| Explain: what was done, why a check failed, why there was a fix round, what an agent decided and why, and what it used in tokens and time | Change the ticket, its branch, `main`, or the board |
| Try things in its own sandbox | Push anything |
| Draft a ticket, a comment, or a memory entry in its answer | Create a ticket, write a comment or a memory entry, or send anything to the working agent. Only your buttons do that. |
| Read the documentation and the memory of the project | See another project, or the chat of another ticket |

**From the chat to the work.** There are three buttons on every message of the chat. Only you press them.

| Button | What happens |
| --- | --- |
| Tell the agent | The text becomes a comment on the ticket and reaches the working agent through the comment loop (11.6): into the conversation of the run that is going. When no run is going, it waits with the ticket, and the next run on the ticket gets it in its prompt. You see the text first and can change it. On a ticket that is Done, cancelled, or discarded the button is not there. |
| Make a ticket of this | A form opens with a title, a why, a what, and a goal, drafted from the message. You change what you want and confirm. The new ticket is linked to this one as created from it, and its origin is `chat`. For a Done ticket this is the way to change something, because a ticket never moves back (7.4). It is your ticket, approved by adding it (7.3). Because the assistant drafted it, its Review cannot be skipped, as for a ticket an agent created (7.4). |
| Remember this | You see the text first and can change it. It becomes a memory entry of the project that says the assistant wrote it and you confirmed it (9.3). |

- An answer of the assistant is not an instruction to anyone until you send it with one of these.
- **What a button carries.** The assistant wrote its answer after reading trajectories, and those can hold pages a run fetched. So the text keeps its source when you send it on. The working agent gets it as a comment that says: from the chat, written by the assistant. A quote is marked as a quote. Only words you typed yourself are your instruction. The rest is data with its source, like every record (13.8).
- **A comment and a chat message are two things.** A comment (11.6) is addressed to the agent that works on the ticket, and can change what it does. A chat message is a question to the assistant, and changes nothing. The task view has one box for both, with two ways to send: Ask, which is the default, and Tell the agent.

**Mark text, and comment (R208).**

1. In the task view you select text with the mouse: in the live log, a trajectory, the prompt, a diff, a note, a review, a test result, or the summary.
2. A small menu appears beside the selection, with Comment. In the box where you are writing, the menu is Improve text and Review text (7.2).
3. You write your comment. It is posted to the ticket's chat with the marked text quoted, and with where it came from: the run and the event, or the file and its lines.
4. The assistant analyses it. It says what the marked text means, whether it is a problem, and what it would do, and answers in the chat.
5. The three buttons then apply. The comment box also has a switch, Also tell the agent, which sends your comment to the working agent at once, with the quote marked as a quote. It is off unless you switch it on.

- **Marked text is data.** A log or a page that a run fetched can hold text that reads like an instruction. The assistant gets a quote as text to explain, never as something to follow, like everything an agent reads (13.8).
- **No secret comes back.** Trajectories are stored with secrets removed (8.3), so a quote cannot bring one into a chat.

**Limits.**

- Chats and conversations share `conversations.max_sandboxes`. When one more sandbox is needed, the one that has been idle longest is removed, and its chat stays.
- Pause all and Stop all hold a chat like every other run (7.9).
- The signal "asked about a ticket" is recorded when you ask or mark text (10.2). Many questions about Done tickets point at summaries that do not say enough.

### 11.10 Peer agents over ACP and Slack-like project chat (M21 and M24, since 1.47, hardened in 2.0)

- **Multi-agent `Agent API` over ACP (`1 session = 1 peer agent`):** Crew wraps `agy acp --stdio --process-per-session` (`12.2`) behind the **Agent API** (`POST /agents/sessions`, `/turn`, `/steer`, `/abort`, `/stream`, health & slot coordination) inside each project's warm Kata microVM (`crew-proj-<id>`). Every peer agent (`@coordinator`, `@planner`, `@researcher`, `@builder`, `@reviewer`, `@security`, `@tester`, `@dreamer`, and bounded custom specialist factory agents declared in canonical `main` `WORKFLOW.md` via `slug`, `promptFile`, `modelBracket`, `readOnly`, `docker`, and `skills[]` with fail-closed `13.8` git symlink refusal and Gemini defaults under `Rule 15`) gets its own ACP session, isolated git worktree, scoped MCP token, and a `Tasks API` Task ID minted **before** any compute starts (`R217`, closing the `TicketID == 0` path), with `agy`'s internal CLI subagents disabled so every delegation is an inspectable peer run.
- **Lightweight internal Slack-like chat (`#<project>` channel + per-feature threads, `@user`, `@crew`, and `@<role>`):** Replaces standalone 1:1 `Conversations` (`11.8`) and read-only `Ticket chat` (`11.9`) with **one canonical project channel (`#<project>`, e.g. `#crew`)** and **one thread per feature (`feat/<NNN>-<slug>`)** rooted inside `#<project>` grouping that feature's `SPEC-NNN` and all child `<KEY>` tickets. Peer agents and `@user` share messages via `read_chat`, `post_chat`, `respond_choice_prompt`, and `ask_planner` Crew MCP tools:
  - **CommonMark prose mentions & co-addressees (`internal/rooms/mentions.go`):** `ExtractProseMentions` skips `@tokens` inside fenced code blocks, inline code spans, and `>` blockquote lines, caps active recipients at `MaxMentionRecipients = 10`, and instructs prompts to name passive bystanders without `@`.
  - **Orthogonal envelope, per-addressee delivery, and no-wake posts (`chat_message_addressees`):** Separates `kind` (`text | event | choice_prompt | choice_response`) from `intent` (`inform | request | question`), records per-recipient delivery state (`pending | queued | delivered | suppressed | failed`), includes co-addressee `to="..."` inside `<crew_data_fence>` headers, enforces `O(1)` same-thread `reply_to_id` and fail-closed cross-project boundary checks (`ErrCrossProjectBoundary`), and treats `0`-addressee or `inform` posts as `posted_nobody_woken` with zero spawned compute.
  - **Queue-by-default vs `@user`-only interrupt:** Routine chat deliveries to an already-active peer ACP session enqueue via next-turn (`turn`, moving to `queued` on `ErrBusy`), while live mid-turn `steer` is restricted exclusively to `@user` with `interrupt=true`.
  - **Keyed Choice Prompts (`internal/rooms/choices.go`):** Structured decision cards (`id`, atomic `version`, `choices[2..10]`, `default`, `update`) rendered as 1-click UI buttons and persisted into `spec_decisions` with `provenance: direct | agent_interpreted` (requiring `sourceMessageId` on `agent_interpreted`, rejecting stale `v1` answers after `v2`, and letting `@user` override interpreted selections directly).
  - **Ingress metadata hygiene & large-body agent offload (`internal/rooms/offload.go`):** Strips reserved Crew metadata keys on ingress (`StripReservedMetadata`) and, while retaining full bodies in `StateDB` for UI display, offloads `> 4000`-rune bodies in agent `<crew_data_fence>` prompt deliveries to a UTF-8 `768`-byte preview + `body_sha256` + `read_chat(messageId=...)` fetch hint.
  - Bounded by `max_agent_hops` (default `3` per trigger), `internal/fence` provenance tags across agents (`Rule 14`), read/comment chat tokens, and strict single-operator identity (`@user` only — never multi-user ACLs).
