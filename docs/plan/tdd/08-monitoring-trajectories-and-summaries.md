## 8. Monitoring, trajectories, and summaries

### 8.1 What is monitored

Every task is watched from the moment it leaves the backlog (R33).

| Per task | Per run |
| --- | --- |
| Stage, and time in it against the usual time for that stage | State: starting, working, waiting for quota or a sign-in, checking, ended |
| The agent on it, if any | Sandbox, and its CPU and memory use |
| Tokens and time so far | Steps and tokens so far |
| Check results, and how many rounds it took | The last action, and how long ago |
| Fix rounds, and why | Errors from tools |

The UI shows this live on the board, on the task, and in the list of running agents.

### 8.2 Watchdog

The monitor applies rules to every run and task (R34). A warning is shown on the card. A stop ends the run.

| Signal | Warning | Stop |
| --- | --- | --- |
| No event from the run | 5 minutes | 15 minutes |
| The same action failing in a row | 3 times | 6 times |
| Turn time | 80% of the timeout | The timeout. The limit is a turn's, as `crew-run` applies it: the clock starts again with every turn |
| Run turns | 80% of the turn limit | The turn limit |
| Time in a stage | 3 times the usual time for that stage | None. It is sent as news and counted as a signal. |
| Check rounds in one run | 3 | `max_goal_rounds` |

A stopped run is retried once in a new sandbox. After a second stop the lead looks at the ticket again (7.7). It gets Crew's summary of what the stopped runs did, not their transcripts.

Since 0.44: a project's `watchdog` settings give these numbers for its runs. The usual time of a stage is the median of the project's past stays in it, and is known after three; a ticket's long stay is told once. Since 0.53 there is no task spend and no run budget to watch: no limit is in dollars (7.7).

### 8.3 Trajectories

A trajectory is the full record of one run, or of one conversation with the operator (R35): every message, model response, tool call, tool result, and usage figure, in order, with timestamps. Tracking it is not a feature that can be left out. It is how Crew knows what an agent did, and everything in sections 8 to 10 is computed from it.

- **How it is captured.** Each CLI prints every step as one line of JSON (`--output-format stream-json`). `crew-run` writes every line to a spool file in the run's home. The backend reads the spool as it grows and writes each event to the database (12.4). Nothing depends on the agent choosing to log. The spool is written inside the sandbox, where the agent can become root, so a trajectory is the run's own account and not proof. What proves something is what Crew recorded itself: the calls to the Crew MCP, the pushes, and the results of the checks it ran. The spike tries capturing the CLI's output outside the sandbox (section 24, item 53).
- **No run without one.** If the events of a run cannot be written, the run is stopped. Trajectory tracking cannot be switched off, by a setting or by an agent.

| One event holds | |
| --- | --- |
| Its number in the run, and the time | So that order and timing can be read back |
| Its kind | Message, tool call, tool result, status, usage, quota, or error (12.5) |
| For a tool call | The tool, its input, how long it took, and its result. A large result is stored in full, compressed. |
| Usage | Tokens in, out, and read from the cache |
| Where it came from | The run, the conversation (12.10), and for a helper the call that started it. `agy` marks the events of its own subagents with `subagent_info` (12.2). |
| The original line | What the CLI printed, unchanged except for removed secrets |

- **Where it lives.** In the `events` table of the database: one row per event, with the run or the conversation it belongs to. The table is split by month. A month that has ended changes only when you delete a project or a conversation.
- **The index.** The database has one row per trajectory: the run or the conversation, the project, the ticket, the subtask, the role, the model, the effort, the number of events and turns, the tokens, the start and the end, the outcome, when feedback was run on it, and the eval cases made from it. This is the `trajectories` table.
- **Secrets.** Anything matching a secret pattern is removed before an event is stored or shown. The redaction rules copied from styrman-harness are the starting point. Crew also removes by value: it knows every secret it handed to a sandbox, and takes exactly those values out. This holds for everything Crew stores from a sandbox: events, turns, prompts, MCP calls, terminal logs, the CLI's own files, work folders, attached files, and text it posts to GitHub.
- **Retention.** Trajectories are kept for good. You decided this in 0.26: a trajectory is what feedback is run on and what eval cases are made from, so it stays until that is done, and for now nothing is removed. Every trajectory shows whether feedback was run on it and how many eval cases were made from it. The setting `keep_trajectories_days` is empty. If you set a number later, Crew removes only trajectories that are older than that and done: feedback was run on them, and their eval cases were made. Not built yet (M17 phase 2, the operator's answer of 2026-10-24): the marks and the `trajectories` index are not there, so no trajectory is marked done, and a number removes nothing; a test holds it, and Machine settings says so.

**Where a trajectory can be found (R186).**

| Place | What it shows |
| --- | --- |
| Every subtask and every run | Its trajectory, live while the run is going, with the events counted by kind |
| The Trajectories screen of a project | Every run of the project, newest first: the agent, the model and the effort, the conversation it continues, events, turns, tokens, time, and outcome. It can be filtered by role and searched for a text, a tool, or a file. |
| Every conversation with the operator | Its trajectory, under the messages |
| A summary, a signal, an eval case, and the evidence of an improvement | A link to the trajectories they were made from |

- **Replay.** The UI can play a trajectory back step by step, live or after the run.
- **Who can read one.** The operator, any of them. An agent, only its own run's. The improver, through its own tool (10.4). The assistant in the chat of a ticket reads the trajectories of that ticket's runs, and is the one exception (11.9).
- **Use.** Debugging a failed task, writing the task summary, creating eval cases, comparing models (12.3), and answering "what did the agent actually do".

### 8.4 Task summaries

When a task ends, as Done or closed, the summary writer produces its summary (R36, R37).

- **Who writes it.** A separate run of the summary writer, in a small sandbox of the Judge kind that holds the login of its CLI and nothing else (13.3). It has no tools. It reads the task's record and its trajectories. The agents that did the work do not write it.
- **What it holds.** The format in 6.3: the facts in the frontmatter, computed from the records, and three short sections written by the model: what was done, how it went, and what to know next time.
- **A ticket withdrawn in Planning** (since 0.39) gets the facts only, marked as withdrawn, and no summary writer: it never ran, so there is nothing to write about.
- **Where it goes.** A row in the `summaries` table of Crew's database, with the fields as columns and the text searchable. An export writes it as `summary.md` in the task's folder.
- **What it is for.** The operator's overview, searching across tasks ("which tasks touched sign-in"), the lead's planning of later work, analytics, and choosing eval cases.

Facts in a summary come from the records, not from the model, so times, tokens, and check results are exact. The written sections are a model's reading of the trajectory and are marked as such.

### 8.5 Where it shows

| Place | What |
| --- | --- |
| Board | A live state on every card, and a warning mark from the watchdog |
| Task view | The running agent with its model, effort, and prompt, the subtasks, checks, time and tokens, and the summary once it exists (20.2) |
| Agents view | Every running sandbox across the project, with its last action |
| Summaries view | All task summaries of a project, searchable and sortable by tokens, time, and rework |
| Inbox | Only what needs the operator (7.8). Stops and long waits are news. |

### 8.6 Saved prompts

The operator can read the exact prompt of every subtask, while it runs and afterwards (R93).

- **What is saved.** The full text the agent was started with, and the parts it was built from, in order: the stage prompt and the agent definition from the baseline with their version, the project's override if there is one, the task with its why, what, goal, and checks, the part of the plan the task comes from, the records the agent needs (the last review, the note), the memory index for the role, the documentation pages named by the task, and unresolved comments.
- **Where.** The full text is the first event of the run's trajectory. It is also stored in the `run_prompts` table of the database, compressed. The run record lists the parts with their versions and a hash of the full text (6.3), so the text can be built again from the baseline and checked.
- **Messages during the run.** A comment from the operator, or the output of failed checks sent back by the goal loop, is a later message in the same conversation. The task view shows these after the prompt, in order.
- **No secrets.** A prompt never contains a key or a login.
- **What the CLI adds.** The CLI also loads the content harness from the run's home: the global context, and the skills and subagents it decides to use. The saved prompt names the harness version, and the app shows that context, so the operator sees everything the agent was given.
- **How it is shown.** In parts, each with its source and version, and as one text that can be copied (20.2).

### 8.7 Trajectory Store indexing and visual workflow stream (M25, since 1.47)

The **Trajectory Store** completes the searchable `trajectories` index over monthly `events` partitions (`8.3`) across every peer agent session and Slack-like chat turn:

- **Full-text & facet search:** Queries trajectories by peer agent (`@coordinator`, `@builder`, `@researcher`, `@reviewer`), tool verb, touched file path, exit outcome (`pass`, `max_tries`, `research`, `error`), and token/duration buckets for Dreaming and debugging.
- **Visual workflow streaming:** Every ACP `session/update` (tool start/finish, diff patch summary, `max_tries` counter, check result) streams live over SSE to update the visual Factory Line graph and inline `#<KEY>` ticket chat cards without polling raw logs.
