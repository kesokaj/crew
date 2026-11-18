## 7. Flow

### 7.1 Why, what, goal, and checks

Every task has four things before it can leave Analysis (R18).

| Field | Answers | Example |
| --- | --- | --- |
| Why | Why does this task exist? It traces to the plan, a bug, or what an agent saw. | The plan says a stolen password must not be tried without limit. |
| What | What will change, and what will not? | Count failed sign-ins per account and lock for 15 minutes after five. Telling the owner is not included. |
| Goal | What must be true when it is done? | Five failed sign-ins lock the account for 15 minutes. |
| Checks | How is the goal proven? | `pytest tests/auth/test_lockout.py` |

| Check | Passes when |
| --- | --- |
| `cmd: "<command>"` | The command exits 0 in a sandbox, on the branch being checked. |
| `docs_done` | Every page named in the task's `docs` field was changed on the branch, or the task has a reason why none is needed. Every task has this check. |
| `skills_vendored` | A branch that changes the project's skills has every row of its `.agents/skills/VENDORED.md` complete: a source, the pin of a commit, and a licence. A skill written in the project has no row; whether a skill came from outside is the reviewer's to read, and a change of skills merges only on the operator's Allow (12.9, 13.8; since 0.46). The file is read through the links on its way, and a change where a link of `.agents/skills`, or of a skill's folder, leads, the branch's or `main`'s, is a change of skills, every change when such a link leads to the root (since 1.06). |
| `review_verdict_approve` | The latest review of the task has the verdict `approve`. Added by the Review stage. |
| `test_result_pass` | Crew's own run of the ticket's checks and the project's tests passed, in a check sandbox, after the tester finished. The tester's entry is a record. Added by the Test stage. |
| `e2e_result_pass` | Crew's own run of the end-to-end tests passed, in a check sandbox. The E2E tester writes those tests and attaches its logs and screenshots as a record. Added by the E2E stage. |
| `verify_pass` | The verifier's entry says the result does what the goal and the source plan ask. Added by the Verification stage. |
| `judge: "<criterion>"` | A separate judge model, given the diff and the record, says the criterion is met. For goals no command can prove. |

Command checks come first, because they are cheap and cannot be argued with.

**Who can change a check (R163).**

- A ticket's checks are in its record in the database. Analysis sets them. A builder can read them and cannot change them.
- The backend reads the checks from the database, never from the ticket's branch.
- A command check still runs the test files on the branch. So the reviewer is shown every test file the diff changes or deletes, and its verdict must say why each change is right.
- With Review off, the backend refuses the move when the diff deletes a test file that a check names.
- **What a test file is** (since 0.47): a file named `*_test.go`, `test_*.py`, `*_test.py`, `*.test.*`, or `*.spec.*`, a file under a folder `test/`, `tests/`, `__tests__/`, `e2e/`, or `testdata/`, and every path a command check of the ticket names. The reviewer's prompt lists each one the diff changes or deletes, with its diff, and an `approve` is refused unless it gives a reason for each. Package files are listed beside them, without a reason asked for (13.8, since 0.90). With Review off, the exit check `named_tests_kept` of In progress fails when the diff deletes a file a command check names; that is a fix round in place. A revert is not held by it: it takes a change out by design, its tests with it, and its checks prove they are gone (7.13).
- **A check proves the goal only if it fails before the work** (since 0.39). When Analysis approves a ticket, Crew runs its command checks on `main` as it is, in a check sandbox. A command check that passes there is refused, and the analysing agent gets one more round to write one that fails until the goal is met. A check that must keep passing, such as the project's whole test suite, is marked `keeps` and may pass on the base; at least one command check of a ticket is not marked `keeps`. Every ticket that Analysis approves has such a command check. A `judge:` check is in addition to it, never instead of it (since 0.43): a judge reads the work, and before the work there is nothing to read.
- **How a `judge:` check runs** (since 0.43). When a ticket's command checks pass on its branch's head in In progress, or on its joined work after the last join (7.11), one judge reads the head: one call with no tools, in a Judge sandbox, which is Model only, on another model family than the builder, given the ticket, the builder's notes, its criteria numbered, and the diff from `main`. It answers every criterion at once, at most ten a ticket. Its answer is a check run of the purpose `judge`, and the In progress exit check `task_judge_checks_pass` reads it. A criterion not met goes back to the builder with the judge's reason, as one more round, or to a builder that joins the work. A judge is not run again at the merge, where the command checks run on top of `main` (7.10).

### 7.2 From plan to running project

| Step | Who | What happens | Recorded as |
| --- | --- | --- | --- |
| 1. Develop a plan | Operator and a planning agent | The operator describes what they want. Crew first proposes a better wording of that prompt, and the operator chooses which one is sent (below). The agent asks questions, researches on the internet, reads an existing repository if one is given, and drafts the plan. It asks which checks the project needs, and the plan lists the stages (7.4). They go back and forth until the operator is satisfied. | A plan draft in the database |
| 2. Create the project | Operator | The operator names the project, gives it a short key for its ticket numbers, and picks how its repository is made (6.6). Crew creates it. | A repository with `main`, and the project in the database |
| 3. Add the plan | Backend | The draft is saved as the project's original plan and is never changed again. | A row in `plans` |
| 4. Break it down | Lead | The lead analyses the plan and the code and writes the breakdown and one ticket per piece of work, each with a why, a what, a goal, checks, dependencies, a part, and the place in the plan it comes from. One of the first tasks writes the project's first documentation (9.9). The tasks are in Planning. | The breakdown and the tickets, as rows |
| 5. OK | Operator | The operator reads the plan and the tasks, comments, edits, or removes tasks, and asks the lead to revise if needed. Then says OK. Every task in Planning moves to Backlog. | The approved versions, recorded in the database |

After step 5 the project is running. It is Done when every approved ticket is Done, cancelled, skipped, or discarded, and no ticket is waiting for analysis. The lead then writes a final report, which lists what was built, what was skipped or cancelled and why, and what was learned. Since 0.44 the report is due whenever the project's newest Done is newer than its newest report: the lead writes five sections with `write_report` (what was built, skipped, cancelled, missing, and learned) in a run of its own in the lead queue's class `final_report`, beside the facts Crew counts itself (each ticket's end with its reason and replacements, what the lead recorded as missing, the runs, and the fix rounds). A run that stops twice leaves the facts, with the sections marked not written and why. The reports are kept in `reports` (schema step 40), numbered, `GET /projects/{p}/report` gives them, and the Plan screen shows the newest. A ticket that is added after that, by you or by the lead in housekeeping, makes the project run again. A new final report follows when that work is Done. A hidden project, `crew-gate`, gets no final report: its Done is a gate's real ticket (16.2, since 0.52). Crew's project is never Done (16.2, since 0.55).

A plan draft needs no project. The planning agent runs in a sandbox like any other run and uses a lead slot of its CLI, in the class of work the operator is waiting on. A plan draft can also start from a normal conversation outside a project (`POST /conversations/{c}/plan`, 11.8), with the operator's messages as theirs and the assistant's answers fenced as data, even when earlier turns of that conversation moved tickets into an existing project's backlog.

**Crew proposes a better prompt (R206).** You asked for this in 0.31: Crew should improve what you write in planning, so that the results get better.

A planning agent works with what it is given. A prompt that says the goal, the reason, what is in and what is out, the limits, and how to see that the work is done gives a better plan and fewer questions. A first prompt often leaves some of that out. So before your prompt reaches the planning agent, Crew writes a better version of it, shows you both, and you choose which one is sent.

| | |
| --- | --- |
| Where | In planning: the first message of a plan draft gets a proposal by itself. Everywhere else it is on request: you select text and choose Improve text or Review text (below). You decided this in 0.32. |
| Who writes it | The prompt writer. It is one call of a CLI with no tools, in a sandbox of the Judge kind, which holds the login of its CLI and nothing else (13.3). It has a model and an effort of its own, like every agent (12.3). It takes no lead slot. |
| What it gets | Your text and its rules. In a project it also gets the project's goal, the titles of the parts of its plan, and the rewrites of that project that you changed before, at most five. It gets nothing from another project (10.6). |
| What it gives back | Four things in a fixed form: the rewritten prompt, a short list of what it changed, the assumptions it made, and at most three questions about what is missing and cannot be guessed. |
| What you see | Your text and the rewrite side by side, with the changes marked. Each assumption is marked in the rewrite and can be struck out. Each question has a field for your answer. |
| What you can do | Use the rewrite. Edit it and send. Send mine. Or answer the questions and press Rewrite again, at most twice. |
| What is sent | The text you chose. The planning agent gets that text and nothing about the other one. |
| A ticket you add | No proposal by itself, since 0.32. You select what you wrote in the form and choose Improve text or Review text. Analysis still analyses the ticket like any other (7.6). |
| What is kept | Both texts, the list of changes, your choice, and what you edited, in `prompt_rewrites`. The draft shows which text was sent, and your original is one click away. |

**The rules the prompt writer follows.** They are its agent file in the baseline, `baseline/agents/prompt-writer.md`.

- It keeps every fact, name, number, and limit that you wrote. It takes out only what is said twice.
- It adds no wish of its own. What it fills in is an assumption, marked as one.
- It asks, and does not guess, when the answer would change the plan: who uses it, which data, what must not change.
- It orders the prompt the way a ticket is ordered (7.1): the goal, the why, what is in and what is out, the limits, how to check that it is done, and what is known already.
- It writes in the language you wrote in, and no longer than needed.
- It rewrites the request. It does not answer it and does not start on it.
- A prompt that already has all of this comes back as "good as it is", and is sent without a stop.

**Improve text and Review text, on what you select.** You asked for this in 0.32: mark text with the mouse, and choose what Crew does with it.

| | |
| --- | --- |
| Where | In every box where you write, and on the plan of a draft. The table below lists the places. |
| How | You select text. A small menu appears beside the selection, with two choices: Improve text and Review text. |
| Improve text, on your own writing | The prompt writer rewrites what you selected, by the rules above. You see both, with what changed, and choose: Use it, which puts the rewrite in the place of the selection, Edit, or Keep mine. Nothing is sent by this. You send the message yourself when it is right. |
| Review text, on your own writing | The prompt writer changes nothing. It gives back at most five notes: what is unclear, what is missing, and what an agent could misunderstand. You change the text yourself. |
| On the plan in a draft | The plan is written by the planning agent. So both choices go to that agent, as a message with the selected part quoted: "Improve this part" or "Review this part". The quoted part is the text to work on. It is data, not an instruction, also when it holds something the agent once fetched from the internet (13.8). It changes the plan, or answers in the conversation. You can add a line of your own before it is sent. |
| What is kept | For your own writing: the selection, the result, and what you chose, in `prompt_rewrites`, with the kind `improve` or `review`. |
| When it is late | The same 45 seconds. When the call is late or fails, the menu says so, and your text stays as it is. |

**Where the menu appears.**

| Place | Whose text | The menu |
| --- | --- | --- |
| A box where you are writing: a message in a plan draft, a ticket you add, a comment, a message in a chat, feedback, an amendment, a change to a decision, and an edit of a documentation page or a memory entry | Yours, not sent yet | Improve text, Review text |
| The plan of a draft, in the Plans screen | The planning agent's | Improve text, Review text, as a message to the planning agent |
| The task view: a log, a trajectory, a prompt, a diff, a record, the summary, and a message that was sent | Anyone's | Comment (11.9) |
| Everywhere else | | No menu in the first version |

**It never holds you up.**

- Send mine is there from the first second, and sends your text as it is.
- The call has 45 seconds (`planning.rewrite_timeout_seconds`). When it is late, fails, or its CLI is at a limit with no next choice, your text is sent as it is, and the draft says so.
- `planning.rewrite` switches it off for the machine, and since 0.32 the only proposal by itself is for a plan draft, which has no project yet and uses the machine's value. Improve text and Review text still work.

**It is measured, and it improves.**

- The signal "rewrite choice" records which text you sent: the rewrite, an edited rewrite, or your own (10.2).
- Crew analytics shows what followed for each of the three: how many questions the planning agent asked, how often you sent the breakdown back, and how many of the tickets passed their checks the first time (10.7). With one developer these numbers are few. They are shown with their counts, and Crew draws no conclusion from fewer than ten plans of a kind.
- What you edit in a rewrite is the clearest signal. The improver reads those edits, with its tool `list_rewrites`, when it proposes a change to the prompt writer's rules, with the generic check as for every prompt (10.4, 10.6).
- No eval case measures a rewrite, since an eval builds a ticket. So a change to the prompt writer's rules waits for your OK in the inbox, like an override of a checking role (9.6). The proposal shows three of your earlier prompts rewritten by the old rules and by the new ones. After it ships, Crew compares how often you used, edited, and refused rewrites before and after.

**What a rewrite is, in Crew's records.**

| | |
| --- | --- |
| A run | Each call of the prompt writer is a run with no ticket, of the kind `rewrite`, like a judge or a summary. It has a run record, its saved prompt, and its trajectory. |
| Limits | It counts in the limits of its CLI. The quota guard does not slow it down, because you are waiting. At a limit it takes its next choice, and with none your own text is sent. |
| Room | It passes the capacity check like every sandbox. It is small and lives for seconds. |
| Use | Its tokens and time are on its run record. Like a chat, it counts toward no limit of a ticket or a project, and since 0.53 no cost is shown. |
| Kept | As long as the draft or the project. Like the chats and the trajectories, rewrites are not in a project export (6.2), and deleting the project deletes them. |
| Who reads it | You, and the improver. No agent of a project can read a rewrite: the planning agent and the lead get the text you chose and nothing else. |

**Where there is no proposal by itself.** Everywhere except the first message you type into a plan draft. A draft that was started from a conversation has an agent's text as its first message, and gets none. A ticket from a personal MCP token, a ticket made from a conversation or a chat, and an amendment are sent as they are.

**What it is not.** Only what you write is rewritten. What agents write for each other is shaped by their role files and by the ticket form, and the improver already works on those (section 10).

### 7.3 What needs the operator

| What | Default | Setting |
| --- | --- | --- |
| The project OK: the plan, its stages, and its breakdown | You | `approvals.project` |
| A task an agent creates that fits the plan, or fixes a bug | Another agent, in Analysis. With `operator` (since 0.47) the analysing agent still analyses it, and its decision is a proposal: the ticket waits in Analysis, not counted against its limit, and the inbox has it with Approve and Discard. It covers every ticket the operator did not make: of agents, of Crew itself (fix tickets), replacements, and tickets from a conversation or a token without the answer switch. | `approvals.created_task` (`agent`, `operator`) |
| A task an agent creates that is a new feature | A notice, then it goes ahead by itself after 24 hours. You can approve it at once, discard it, or hold it. | `approvals.new_feature` (`notice`, `operator`, `agent`) |
| A task you add in the app | Approved by adding it | None |
| A task from a conversation (11.8), or from your own tools through the MCP (11.7) | Analysed like a task an agent created. It is your own task, approved by adding it, when you press the button in the app or the token has the answer switch. | The answer switch of a token |
| A task move | Automatic, on checks | The stage's `exit` checks |
| Merging a task to `main` | Automatic, on checks | `git.merge` |
| An override of a prompt or agent definition for this project, proposed by its lead | Nobody, when the project's evals are not worse with it. The operator is told, and Crew takes it back if the results get worse. An override of a role that checks, or of a file no eval runs, waits for you (9.6, since 0.49). | `approvals.agent_change` (`auto`, `operator`) |
| A skill or context a project needs | An agent asks, and a ticket adds it (12.9) | None |
| Which model and effort a run uses | Crew, for the builder, the reviewer, the tester, the E2E tester, and the verifier, from its own numbers. The other agents follow their list. What you set for an agent, for a project, or on a ticket comes first (12.3). | `agents` in the settings |
| An improvement to the baseline, proposed by Crew's improver | Nobody, when its evals are not worse. It ships in the next release, and that release gives notice (16.3). | `improvements.approval` in the machine's settings (`auto`, `operator`). An improvement that changes more than text is in a release that waits for Install (16.3). |
| A choice the plan does not settle | The agent. It records the decision and carries on. You can change it afterwards (7.5). | None |
| A release of Crew that changes only prompts and harness text | Nobody. A notice, then it installs after 24 hours (16.3). | `updates.auto_install`, `updates.install_after_hours` |
| A release of Crew that changes code | You. It waits for Install (16.3). | `updates.auto_install` |

Setting `approvals.new_feature` to `agent` lets a project extend itself with no notice at all. Setting it to `operator` makes every new feature wait for you.

**What still waits for the operator, and why.** The system should need the operator as little as possible. A few things still stop and wait, because each is a brake or something only the operator has. One of them is on a single ticket: a change to a file that is code for the CLI or for GitHub (13.8). The others are:

| Waits for you | Why |
| --- | --- |
| The project OK | It sets the scope that everything after it runs inside. |
| A project limit that was reached: the share of tickets agents may add to the approved plan | It bounds the whole project. Raising it is the operator's choice. |
| Signing in to a CLI again | Only the operator has the subscriptions. Meanwhile the runs on that CLI wait (12.6). |
| A release that changes code, or needs a newer machine setup | The server is one program with the rights of all its parts, so code is installed when you say so (16.3). |

Everything else gives notice where it matters and goes on.

These are the defaults. A setting can make Crew wait more for one project, such as `approvals.new_feature: operator`. No default does.

### 7.4 Stages

A board has five fixed stages and, between In progress and Done, the optional stages the project uses (R17, R96).

| Stage | Kind | Agent | What happens | A task leaves it when |
| --- | --- | --- | --- | --- |
| Planning | Fixed | None | The tasks from the breakdown wait here for the project OK. | The operator gives the OK and the tasks move to Backlog. |
| Backlog | Fixed | None | Accepted work waits to be analysed. Tasks that agents create land here. | Analysis has room. The orchestrator pulls it, by priority. |
| Analysis | Fixed | Curator | The task is analysed against the plan and the current code (7.6). A skipped task, and a new feature during its notice, wait here too. They are marked and do not count in the stage's limit. Neither does a ticket that is ready and waits only for a dependency, so waiting tickets cannot fill Analysis. | It is approved, with a why, a what, a goal, and a check. It then waits as ready until its dependencies are Done, In progress has room, and the machine has room. |
| In progress | Fixed | Builder | The builder works on the task's branch. | The branch is pushed and has commits, a note exists, the task's command checks pass, and `docs_done` passes. |
| Review | Optional, on by default | Reviewer | Another agent reads the change against the goal, and checks the documentation. | The verdict is `approve`. |
| Test | Optional, on by default | Tester | Another agent reads the change, adds the tests that are missing, and runs them in a fresh sandbox. | Crew's own run of the checks passes. |
| E2E | Optional, off by default | E2E tester | An agent starts the whole application in a fresh sandbox and drives it through the user flows the task touches. | The result is `pass`, with logs and screenshots attached to the task. |
| Verification | Optional, off by default | Verifier | An agent that did not build the task compares the result with the task's goal and the source plan. | The verifier says it does what was asked. |
| Done | Fixed | None | The task is merged (7.10), its summary is written (8.4), and the changelog is updated (9.9). | It does not leave. A revert creates a new task. |
| Closed | Fixed, not a column | None | Closed without being done: discarded by Analysis or by the operator, or cancelled by the lead and replaced by other tickets (7.7). | It does not leave. |

**Choosing the optional stages (R97).**

- While the plan is drafted, the planning agent asks which checks the project needs: a review, tests, end-to-end tests, a verification against the plan. The plan lists them.
- The project OK approves that list. It becomes the `stages` block of the project's settings.
- Stages can be switched on or off later in the settings. A change applies to tasks that have not reached that stage yet.
- With no optional stage on, a task goes from In progress to Done on its command checks alone. The board says so at the top, because then no other agent looks at the work.

**Per task (R98).** In Analysis the agent can mark an optional stage as not needed for one task, with a reason, in the task's `skips` field. Review cannot be skipped for a task that an agent created, or for one that the assistant drafted in a conversation or in a chat (origin `agent`, `conversation`, or `chat`; 13.8). A task that only changes documentation skips E2E, for example. A ticket whose branch changes a package file (13.8) is reviewed all the same, on its head before it merges: Analysis decides its skips before the branch exists, so the review runs in place in its last stage, and its skip stays recorded (since 0.90).

**A ticket only moves to the right (R179).** A ticket never goes back a stage. It moves on, or it is closed. Subtasks, and the tickets that replace a cancelled ticket, are tagged to the ticket that had to be split.

**When a stage fails.** A task that fails Review, Test, E2E, Verification, or the merge check stays in its stage. The findings become a fix subtask for a builder, on the task's branch. When the fix is pushed, the stage checks the task again. Each fix increases `fix_rounds`. When it reaches `max_fix_rounds`, the lead looks at the ticket again (7.7). Nobody is asked (R23).

- The task keeps its place in the stage while it is fixed. A stage that is full of failing work takes no new work, and that is the signal a kanban board is meant to give.
- The stages a task has passed are not repeated. The merge check runs every command check again on top of `main`, so a fix cannot slip through unchecked (7.10).
- **A head is reviewed before it merges** (since 0.90, the operator's answers to parked question 72 and row B14 of 28). The stages a ticket passed are not repeated, but a review is: before the move to Done, the exit check `review_covers_head` holds the ticket until its newest review is about its head and approves it, whenever Review is on and either the ticket has an approving review from an earlier commit (a fix in Test, E2E, Verification, a stage of the project's own, or the merge check, or the lead's new approach, pushed code after Review) or its branch changes a package file (13.8). The merge queue then starts the reviewer on the head, in the ticket's stage, as Review does: on another family than the builder (12.3), told the commit Review approved, with the files changed since then and the package files listed, and reminded once when it ends its turn without `write_review`. Changes asked for are a fix round of that stage, counted in `fix_rounds` (7.7). The ticket still only moves to the right. With Review off, or with the ticket in Review, nothing is owed.

**Custom stages (R101).** A project can add a stage of its own, such as a security review: an entry in the `stages` block with an agent definition, a prompt, a work-in-progress limit, and an exit check.

- **What the entry holds** (since 0.43): `id`, `optional: true`, `on`, `title` (its name on the board), `role` (a word that names its agent), `agent` (the path of its agent's definition on `main`, under `.agents/agents/`), `prompt` (what the stage asks, written by the operator), `wip`, and `exit`. It stands anywhere between In progress and Done.
- **Its exit list** holds `stage_verdict_pass`, which every stage of a project's own has, and any of `cmd: <command>`, `judge: <criterion>`, `task_checks_pass`, `task_cmd_checks_pass`, and `docs_done`. A name Crew does not evaluate is refused when the stage is saved, so no ticket waits on a check nobody answers. A stage of Crew's own cannot name a command or a criterion.
- **How it runs.** Its agent reads the branch as it is, with no push, on the reviewer's model and on another family than the builder. Its definition is read from `main`, never from the branch it judges: a file under `.agents/agents/` is code for `agy` (13.8), so it changes only through the operator's Allow. A stage whose definition `main` does not have waits and says so. The agent records `add_stage_verdict`, pass or fail. After a pass, Crew runs the stage's commands in a check sandbox (a check run of the purpose `stage`) and one judge answers its criteria (`stage_judge`). A verdict that fails, a command that fails, and a criterion not met are each a fix round in place.
- **Adding one.** `PUT /api/projects/{p}/stages/{id}` adds or replaces it, after the stage it names, with its row in changes; `DELETE` removes it while no ticket is in it. The board shows it as a column with its title, and Settings as a row. Its form in the app is M17's.

### 7.5 Crew MCP: asking, and creating tickets and subtasks

Crew has its own MCP server. Every run is connected to it with a token for that run. The token names one project, so an agent can only see and change the project it runs in (R25). It is how an agent adds work: more tickets on the board, and more subtasks on its own ticket (R27, R151).

**Asking (R26).** An agent can ask for the plan, the tickets and their state, any ticket's record and summary, the documentation, and the project's memory, bugs, feedback, eval results, and analytics. A builder that wonders whether something is already planned can look before creating a duplicate.

**Ticket or subtask?** The ticket's what and goal decide.

| The agent sees | It calls | What happens |
| --- | --- | --- |
| Work outside its ticket: a bug elsewhere, a missing feature, a page that is wrong | `create_ticket` | A new ticket in Backlog. Another agent analyses it before it is built (7.6). |
| A bug | `report_bug` | The bug record and a `fix` ticket in Backlog, linked to each other |
| More work inside its own ticket, large enough for a run of its own | `create_subtask` | A queued subtask on the same ticket. It gets its own run in its own sandbox. |
| A choice the plan does not settle | `decide` | The agent chooses. Crew records the choice, and the run carries on. |
| A small side job for itself, such as finding every caller | `add_helper` | A helper in the same sandbox, started with the CLI's own subagent feature (7.11) |

**Deciding (R160).** An agent never waits for the operator. When it meets a choice the plan does not settle, such as which of two currencies is the default, it decides and calls `decide`. It gives the question, the alternatives it saw, the one it chose, and why.

- Crew records it as a row in its database, with the ticket, the subtask, and the run. The choice also becomes a memory entry, so later runs follow it.
- The run carries on at once. The operator gets news of it, not a question.
- The project's Decisions list shows every one. The operator can change a decision there. Crew then creates a ticket that carries the change out, and the memory entry is replaced.
- An agent should choose what is easiest to change later, and say so in its reason.
- `decide` is for a choice an agent had to make by itself. `record_decision` is the documentation page for a design decision (9.9). A choice that shapes the design gets both.

**Creating a ticket (R27).** Any agent can call `create_ticket` whenever it sees fit. It gives:

- a title,
- what it saw (`seen`): the evidence, such as a failing command or a file and line,
- why it thinks the ticket is needed,
- optionally a first idea of the what, the goal, and a check.

The ticket is saved in Backlog with the creating role, the run, and a link to the ticket that run was working on. It is marked in the UI as created by an agent. The creating agent then carries on with its own ticket. It does not wait, and it does not do the new work itself.

**Creating a subtask (R151).** The lead, while it analyses a ticket, and the builder, while it builds one, can call `create_subtask` for their own ticket. It gives:

- a title, what to do, and what done looks like, optionally as a command that checks it,
- the role that should do it: builder, tester, or E2E tester,
- the files or folders it will touch (`touches`),
- optionally the subtasks it must come after (`after`).

The subtask is queued on the ticket and shown at once in the task view, marked with who created it. It gets its own run in its own sandbox.

- It has no why, what, and goal of its own, and it is not analysed. It takes them from its ticket, which was analysed. So a subtask must stay inside the ticket's what. Work outside it is a ticket.
- A ticket may get at most `max_added_subtasks` subtasks this way (7.7).

**Subtasks in parallel (R155).** Crew should need the operator as little as possible, and should not wait where it does not have to. So the work of a ticket is split and run side by side where it can be.

- During Analysis the lead can plan the build of a ticket as several subtasks instead of one.
- Subtasks that do not come after each other, and do not name the same files, start at the same time. At most `max_parallel_subtasks` run at once on one ticket.
- Each parallel subtask works on its own branch, `sub/<ticket>.<n>`, cut from the ticket's branch. Two runs never push to the same branch.
- When a subtask ends, the backend runs that subtask's own check on its branch and merges it into the ticket's branch, one at a time. The ticket's checks run after the last join (7.11).
- Two subtasks that name the same file are not run together. The second waits for the first.
- A merge that conflicts, or checks that fail after a merge, become one more builder subtask: join the work, with both sides and the output.
- The ticket stays in In progress until every subtask is done and joined. Review, Test, and the later stages then look at the whole ticket once.
- Parallel subtasks are subagent runs. Room on the machine and the subscriptions' limits bound them, not the lead slots (7.9).

**What the operator sees.** The Crew MCP screen lists the tools and every call as it happens: which run called what, and what came of it (R153, 20.1). A ticket or subtask an agent created links back to the run that created it.

### 7.6 Analysis

Every task is analysed before it is built (R28, R29). Analysis has two jobs: it makes the task ready to build, and for a task an agent created it decides whether to build it at all.

**Who.** The curator (`curator`), or the backend directly for a ready task from the approved plan or added by the operator in the app (since 1.42). A task the curator created is analysed by a reviewer instead, or by a second curator run when Review is off. The run that created a task never analyses it.

**Zero-LLM fast-path for ready plan and operator tasks (since 1.42).** When a ticket has `origin` `plan` or `operator` (and is not an unconfirmed adopted ticket or an unconfirmed bug fix), already carries a non-empty `why`, `what`, `goal`, and at least one command check, and has no unresolved operator comments or prior failed `PurposeBase` check run, Crew runs the `PurposeBase` command check verification on `main` directly without starting an LLM analysis run: if the check fails on `main` as required, Crew marks the ticket approved and ready for `In progress`; if a check already passes on `main` or fails to run, Crew falls back to a normal curator LLM `Analysis` run. Tickets of origin `agent`, `token`, `chat`, or `conversation`, and any ticket missing `why`, `what`, `goal`, or a command check, always go through an LLM `Analysis` run.

**What an LLM analysis run does, per task.**

1. Reads the task, the part of the plan it comes from, and for a created task what the creating agent saw and why.
2. Reads the code and the documentation the task touches, as they are now. The code has usually changed since the breakdown.
3. Searches the tickets for a duplicate.
4. For a fix, tries to reproduce the bug on the current code.
5. Writes or confirms the why, the what, the goal, and the checks, and adds a short approach, the pages of documentation to update, and the dependencies. Its command checks are run on `main` before the decision is applied, and one that passes there is refused (7.1, since 0.39).
6. Marks the optional stages the task does not need, with a reason.
7. Decides.

| Decision | When | Result |
| --- | --- | --- |
| Approve | The task is part of the approved plan, or is needed for it, or repairs a real bug | It is ready. It moves to In progress when there is room. |
| Skip | The task is valid but not needed now | It stays in Analysis as `skipped`, with the reason and a condition to look again, such as "when part Checkout is done". The condition is one of four kinds, so that Crew can test it: a part of the plan is done, a ticket is done, a date, or everything else is done. It is analysed again when the condition is met. |
| Discard | It is a duplicate, not reproducible, already done, or not wanted | It is closed as Discarded with the reason. The reason goes back to the creating role as feedback when a pattern forms. |
| New feature: give notice | It adds something the approved plan does not cover (R30) | It stays in Analysis, marked as a new feature, with the why, what, and goal already written, and the operator is told. After 24 hours it is ready. Until then the operator can approve it at once, discard it, or hold it. |

For a task from the approved plan, and for a task the operator added, there is nothing to decide (R31). Analysis only makes it ready. It can still skip a plan task whose work turns out to be done already, with the reason.

The analysis is recorded on the task and as its first subtask: who analysed it, the decision, and the reasoning.

### 7.7 Limits

With one OK, limits are what bound the pipeline (R23, R32).

| Limit | Set by | When it is reached |
| --- | --- | --- |
| Run turns and timeout | Settings: `run_max_turns` and `run_timeout_minutes` | The run stops. The subtask is retried once. If it stops again, the lead looks at the ticket again. |
| Fix rounds on one ticket | `max_fix_rounds` | The lead looks at the ticket again. Nobody is asked (R162). |
| Failures on one ticket | `max_ticket_failures` (default `6`, `>= 1`) | Crew puts the ticket on hold, records `ticket_failure_hold`, and shows `failed/<project>/<key>` in the inbox before starting another fix round, `look_again`, or `cancel_and_replace`. Lifting the hold as operator resets the count since release; raising `max_ticket_failures` above the count lifts Crew's hold automatically while leaving an operator hold untouched. |
| Work added by agents | `max_added_share` of the tickets of the approved plan, or `max_added_tickets` (default `10`, `0` disables) open added tickets on a project with no `plan` tickets | The project pauses and the operator is asked. Running tasks finish their run. |
| Subtasks agents add to one ticket | `max_added_subtasks` | `create_subtask` is refused with the reason. The agent finishes with what it has, or creates a ticket. |
| Subtasks of one ticket running at once | `max_parallel_subtasks` | The others wait in the ticket's queue. |

A pause is a question about a project limit, not a review of the work. Raising the limit resumes the project where it stopped. Since 0.44: the backend pauses with its reason in words (since 0.53, "agents added 5 tickets, over 25% of the plan's 16"), and its next pass resumes a project it paused once the limit is no longer passed, as when you raise `max_added_share`. A Resume of such a project while it is still over its limit is refused with the reason, and the inbox offers Raise the limit instead, which opens the project's settings. A project you paused yourself is never resumed by the backend.

**What counts as work added by agents** (since 0.44, as a count of tickets since 0.53; `max_added_tickets` for planless projects since 1.46). Tickets of origin `agent` once Analysis approved them, each as one, against the tickets of the approved plan (origin `plan`). A replacement (it carries `replaces`) and a discarded ticket do not count. A ticket you made, also through your token, a conversation, or a ticket's chat, is yours and does not count. On a project with no tickets of origin `plan`, such as Crew's own project and `crew-gate`, `max_added_share` has no denominator, so `limits.max_added_tickets` (default `10`; `0` disables) caps how many open (`stage NOT IN ('done', 'closed')`) approved agent-added tickets may accumulate at once before Crew pauses the project with `"Agents have N open added tickets, over the limit of M"`; closing tickets or raising the limit resumes it. A ticket the backend makes itself (`created_by` backend: a fix of the project's setup or of a fetch of the remote's main, tests that pass or fail at random, a merge on GitHub) is no work agents added (since 0.53, from the review of 0.53). Before 0.53 a project with no budget, every project on dave, had no scope guard; since 0.53 every project with tickets of its plan has it, from the first pass after the update.

**A ticket that cannot get through.** A ticket whose run stopped twice, or that needed `max_fix_rounds` fix rounds, does not wait for the operator, and it does not go back.

| Step | What happens |
| --- | --- |
| 1. The lead looks again | A lead run reads every round: the notes, the findings, and the diff. It finds why the ticket keeps failing: the approach, the goal, the check, or its size. |
| 2. Once: a new approach | The lead writes a new approach as subtasks on the same ticket. The ticket stays where it is and gets two more fix rounds. |
| 3. Otherwise: cancelled and replaced | The lead cancels the ticket and creates the tickets that replace it: smaller ones, or one that is cut differently. Each carries the link `replaces`, starts in Backlog, and is analysed by another agent. |
| 4. Recorded | Either way the lead's call, `new_approach` or `cancel_and_replace`, records its choice and its reason, and the operator gets news of it. |

How it runs (since 0.44). Looking again is a subtask of the kind `look_again` on the ticket, a lead run in the queue's class `analysis`, at most one at a time, in a clone of the ticket's branch that it only reads (or of `main` when the ticket's branch was never pushed to the quarantine repository, since 1.26, register row 5; a continued read-only run's base is also kept at `refs/heads/<branch>` only when that ref exists in the quarantine repository, register row 48). It starts when a run of the ticket stopped twice (its analysis, its build, a fix, a part, or a join), when the ticket used its fix rounds, or when its parallel work did not join after two tries. When In progress is the last stage, checks that fail on a head a fix pushed are a fix round again, once its parallel work is joined. It gets the notes, the reviews and test results, and Crew's summary of what the stopped runs did. Its tools work only in that run and only on its own ticket. `new_approach {reason, approach}` is refused in Analysis and the second time; it ends the stopped subtasks and the parts that wait as `superseded`, so they wait for nothing, gives the ticket its two more fix rounds (`approaches`), and a builder carries the approach out in a subtask of its own, in the ticket's stage, in a new conversation; in In progress that subtask is a build that hands over with `request_move` and goes through In progress's exit command checks and judge, while in a later stage it is a fix with `fix_done` (since 1.26, CREW-16; register row 83); the tries before it no longer count, and only the new approach's stopped build counts as a fix round alongside a fix subtask after a check or merge failure (since 1.28, register rows 82 and 83). When Review approved the ticket before, the new approach is reviewed again before the merge (7.4, since 0.90). `cancel_and_replace {reason, tickets}` closes the ticket as cancelled and creates at most six replacements with origin `agent` and the link `replaces`. A look again whose own agent turns stopped twice cancels the ticket with that reason; an end of `crew-run`'s own step (`ReasonSandbox`, `ReasonSetup`) never counts as one of those stops (since 1.26, register row 5).

What cancel and replace means in detail:

- The replacements do not count as work agents added (7.7), because they are the same work, cut differently.
- A ticket that depended on the cancelled one now depends on its replacements.
- The cancelled ticket's branch is kept and never merged. A replacement can take commits from it.
- A cancelled ticket does not stop the project from being Done. The final report lists it with what replaced it.
- A replacement that cannot get through either is cancelled and not replaced a second time. The lead records what is missing as feedback on the plan, and the final report lists it.

So nothing on one ticket can run without end: a run has a limit, the fix rounds have a limit, the lead cancels a ticket the second time, and a replacement is not replaced again. Above all tickets, a project is bounded by the tickets approved with the OK, and by the share agents may add to them. The subscriptions' five-hour and weekly windows bound the rate of all work (12.7).

No limit is in dollars (since 0.53, the operator's choice): Crew counts no money and estimates no cost (12.7). A run is bounded by its turns, its minutes, its goal rounds, and the watchdog. A ticket is bounded by its fix rounds, one new approach with two more, one cancel and replace, and at most `max_added_subtasks` added subtasks. A project is bounded by the tickets approved with the OK and by the scope guard. A subscription limit is not a project limit. At the limit of its group of models, or above `quota.slow_down_at`, a run moves to a model of its bracket in another group that can run, where the rule for checks allows it and its model was not set by hand, or waits for the reset (12.7). No project is paused.

### 7.8 What reaches the operator

The inbox is the one list of what needs the operator and of what is about to go ahead. Every other mention of the inbox in this document means this list.

- projects waiting for their OK,
- projects paused at a project limit,
- a CLI signed out (12.6),
- a release that waits for Install, because it changes code or needs a newer machine setup (16.3),
- a ticket that changes a hook, a settings file of a CLI, an MCP configuration, or a workflow, with the diff (13.8),
- an override of a role that checks (9.6),
- new features, each with the time it goes ahead by itself,
- a release that changes only prompts and harness text, with the time it installs by itself (16.3),
- what Crew could not do by itself: an update or a roll back that failed, a machine that cannot start sandboxes, a channel that does not deliver (18.2), a ticket Crew put on hold after `limits.max_ticket_failures` failures (`failed/<project>/<key>`, 7.7, since 1.46), a check that waits because its model or its builder's model is of a family Crew does not know (12.3, since 1.26), and a notice when a check ran on the builder's family because no model of another family had room (`check_rule_not_met`, 12.3, 12.7, since 1.26).

The first two come from the board. The others come from Crew itself. Since 0.40 every item is read from the state that causes it, when the inbox is shown, and it goes when its cause does: a project that waits for its OK is one in planning whose newest breakdown is newer than its newest sending back, while no lead writes one; a project paused at a limit is one that Crew paused, not you; a CLI signed out is one the login check marked so. The list therefore cannot say what is no longer true. Where an item was sent comes with the notifications (M18), which keep what they sent.

Not in the inbox, because nobody has to act: a decision an agent made, and a ticket the lead looked at again or cancelled and replaced. They are sent as news and are listed in the project.

### 7.9 Automatic start and throttling

**Dependencies cannot lock the board.**

- The backlog is pulled by priority. A ticket that an approved ticket waits for goes first.
- A dependency that was discarded counts as met. A cancelled one passes its place to the tickets that replace it (7.7). A skipped one sends the waiting ticket back to Analysis, because its plan no longer holds.
- `link_tickets` refuses a link that would close a circle.
- A hold on a ticket lets a run that is going finish, and starts nothing new on that ticket.

**When room on the machine frees up,** it goes to a resuming `continues` run first (so a run whose check sandbox just finished never deadlocks mid-stage), then to the waiting run in the stage furthest to the right, then to the project that was served least recently, then by priority and by age. Room is kept back for a waiting large sandbox, so small ones cannot starve it.

**What a priority is.** A ticket has one of four priorities: urgent, high, normal, or low. A ticket starts as normal: from the breakdown, from an agent, and from you. Within one priority, tickets go in the order of the backlog, where a ticket you add goes to the top. The curator can change a priority in Analysis, and you can change it at any time. An agent cannot set the priority of a ticket it creates.

**Automatic start (R50).** Once a project has its OK, agents do not wait for the operator. The orchestrator pulls the highest-priority task from Backlog into Analysis whenever Analysis has room, and a ready task into In progress whenever that has room. The operator steers with priority and can put a task on hold.

**One unified concurrent-run queue and active-ticket limits (R51, R52, since 1.42).**

| Limit | Default | Scope | Reason |
| --- | --- | --- | --- |
| `max_concurrent_runs` | `8` (`1..64` in `crew.yaml`) | Across all projects (`starting` and `working` agent runs, excluding child `helper` subtasks; an `idle` run waiting for the operator or for a check sandbox holds no slot) | Replaces the retired single-file `lead_slots` (`1`) and `max_subagent_runs` settings with one enforced run-slot pool across all roles, halved (`max(1, n/2)`) when any model group of `agy` is at or above `quota.slow_down_at`. |
| Interactive reservation (`conversations.max_sandboxes`) | `3` (`1..10` in `crew.yaml`) | Interactive `operator_waiting` keyboard turns (plan drafts, ticket chats, conversations) | Keyboard turns bypass background ticket slot contention while non-ticket working runs are below `min(slots, conversations.max_sandboxes)` so the operator at the keyboard never waits behind ticket runs. |
| Active-ticket concurrency (`limits.max_active_tickets`, `max_active_tickets`) | `4` per project (`1..32` in `board.yaml`), `12` across the machine (`1..64` in `crew.yaml`) | Per project and across all projects | Bounds concurrent tickets with active runs or checks while warm project microVMs (`sandbox.cpus: 6`, `sandbox.memory_gb: 8` default) run multiple isolated ACP sessions concurrently (`Analysis` 4, `In progress` 6, `Review` 4, `Test` 4, `E2E` 1, `Verification` 1). |

**Who gets a free concurrent-run slot (R53, since 1.42).** All agent runs wait in one unified scheduler queue (`lead_waits`), ordered deterministically:

1. `operator_waiting`: keyboard turns first (`draft`, `conversation`, `chat`, which also have interactive reservation up to `conversations.max_sandboxes`), then `breakdown`.
2. Ticket work (`ticket` and `analysis`), ordered by:
   - resuming `continues` runs first (`continues = true`, e.g. a builder or reviewer continuing after its check sandbox completed, preventing mid-stage deadlock),
   - stage furthest right (`Verification → E2E → Test → Review → In progress → Analysis`),
   - operator priority across projects (`urgent > high > normal > low`; a priority set by an agent orders within its own project only),
   - fair project turn (the project served least recently),
   - the ticket's own priority within its project, then longest waiting (`since`).
3. Low-priority maintenance: `final_report`, `housekeeping`, `retrospective`, `evals`, and `improvement` (Crew's improver).

**Room on the machine (R71).** Every run needs a sandbox with a CPU and memory allowance. Before starting any run, the orchestrator checks that the allowances of the running sandboxes plus the new one fit within the machine's capacity, minus a reserve. If not, the run waits. The check is strict for memory. A CPU allowance is a ceiling, not a reservation, so the allowances may add up to more than the cores, up to `cpu_overcommit`. A run whose group of models is at its subscription limit moves to its second choice or waits (12.7).

How it is built (since 0.47; per-project microVM sizing and capacity accounting since 1.38; raised warm-VM defaults and `continues` priority since 1.42; `RepeatableRead` snapshot and non-sandbox `check_runs` exclusion in `capacity.Used` since 1.45). Each project's warm microVM (`crew-proj-<id>`) is sized from the project's `sandbox.{cpus, memory_gb, disk_gb}` settings in `board.yaml` (default 6 virtual CPUs, 8 GiB memory, 20 GiB disk, with optional per-project `suspend_after_minutes`, `stop_after_minutes`, and `max_snapshots` overrides), and `capacity.Used` reads `project_sandboxes`, `runs`, and `check_runs`/`sandboxes` inside one `RepeatableRead` read-only transaction when its querier supports `BeginTx`, counting each `project_sandboxes` row once (`running` = configured CPUs and memory, `paused` = `0` CPUs and configured memory, `stopped` = `0` CPUs and `0` memory) without double-counting the ACP sessions running inside it; starting another ACP session inside an already-`running` project microVM requires `0` additional host CPUs or memory, and unpausing a `paused` project microVM requires only the project's configured CPUs and `0` additional memory. Every ephemeral sandbox Crew starts is counted at its size: small for a standalone judge, the improver, the setup microVM, and a login check; large for a check sandbox, which runs the project's tests (`check_runs` excludes `judge` and `stage_judge`, which run inside the small `judge` agent run, and non-sandbox merge conflict or fast-forward `Test`-reuse records are written directly in their finished `passed`/`failed` state via `Board.RecordCheckRun` without entering `running`), a project terminal, and, from M20, a standalone conversation; and, since 0.60, a size of its own, build, for the build of a release and the gate's make check (16.2 steps 4 and 5): a large sandbox's CPUs with 6 GB over Kata's base, since the export of the runtime image needs more than a large sandbox's 4 GB, whose memory cgroup killed the guest's Docker daemon at L5. A project's checks, Crew's project's `make check` among them, stay large: `make check` passed in a large check sandbox on dave (the remote's `main`, 2026-10-22 and 23), and the gate's make check takes the build's size only because it shares the build's way to room. After a roll back to a release before 0.60, a save of the machine's settings that holds `sandbox_sizes.build` is refused, as one that holds `check_network` is (16.2), so the key is taken out first. The machine's cores and memory are read from Docker when the server starts, and `machine_reserve` is taken off. Memory is counted strictly, and CPU up to `cpu_overcommit` times the cores. A short-lived sandbox, a login check or the setup microVM, is counted and never held back. A run idle between its turns is not counted: its microVM waits, mostly for the checks of its own work, which would otherwise wait for its room; an idle microVM uses little (13.10), and Kata gives back the memory its guest frees. Its next turn in the same microVM is not held back either, so memory is counted strictly for what starts, and an idle run is the one allowance that may be over it for a while (review of M13). Crew's own check sandboxes on `main`, of the project's setup and of the remote's `main`, are counted as large and not held back: they are short, and work waits for their answer. A run that does not fit waits, as waiting for room, on its card and in the Projects screen. Freed room goes in the order above: a resuming `continues` run first, then the stage furthest right, then the project served least recently, then the priority, then the age. When the first in that order is a large sandbox that does not fit, it keeps its room, and smaller ones start only in what is left beyond it. An agent run takes its concurrent-run slot first and then room, and releases its `lead_waits` entry as soon as `TakeSlot` grants a slot so a run that then waits in `room_waits` holds no slot and blocks no other waiter in `lead_waits`.

**Shares of a subscription (R164).** The subscriptions' limits are what bounds the rate of the work, and every project shares them. When several projects have runs waiting for the same group of models, the project that was served least recently starts next. So one project cannot use a whole window while the others wait. Since 0.47: while a group is close to its limit (12.7), one new run on that group starts in each pass of the orchestrator, and it goes to the project that was served least recently on that group. While the group is free, room and the stage limits bound the runs.

**Pause all, and Stop all (R165).** Two switches on the Projects screen, and `crew-ctl pause` on the machine.

| Switch | What it does |
| --- | --- |
| Pause all | No new run starts. Running runs finish. |
| Stop all | Every run ends now, and its conversation is kept. |
| Resume | Work starts again where it stopped. |

While Crew is paused, no release installs and no new feature goes ahead. Their clocks wait. No agent can pause or resume.

Since 0.44: the state is one row (`crew_state`), and every start of a run, a turn, and a check run asks it, so a chat, a summary, and a final report wait too. Stop all ends every run with the reason `operator`; its subtask waits for Resume, which continues it without counting it as one of Crew's own continues, unless its project is paused or its ticket on hold: then it waits on until that ends too (since 0.63, 12.4). Resume moves the clock of every notice on by the time Crew was paused. Automatic model changes wait as well. Since 0.65 the same row holds one more reason beside Pause all and the disk guard: two microVMs in a row that did not boot, until one boots or Resume in the inbox (21); nothing new starts while it holds. Since 0.67 two more, a restart for security updates (16.10): for ten minutes after the server told `crew-ctl` the machine may restart, so nothing starts while it goes down, and from the day a restart has waited 7 days until the machine restarted, so the running runs finish first.

### 7.10 Merging to main

Tasks are merged one at a time per project (R22).

1. A task passes its last stage before Done. A head that Review did not approve, or a branch that changes a package file, is first read by a reviewer in that stage (7.4, since 0.90); this does not take the project's turn to merge.
2. The backend merges the current `main` into the task branch, in the canonical repository on the machine, which no sandbox can reach, and with no work tree: `git merge-tree` makes the merged tree, and the merge candidate is a merge commit whose first parent is `main` and whose second is the task branch's head, with the task's trailers (since 0.39). A check sandbox then gets the merged tree and runs the task's checks and the project's test command. A sandbox never merges and never pushes `main`.
3. If they pass, the backend moves `main` to the merge candidate, only from the commit it was made on, and the task is Done. One task of a project merges at a time. With a GitHub remote the task's pull request is then marked as merged (7.12).
4. If the merge conflicts or a check fails, the task stays in its last stage. The output becomes a fix subtask for a builder, the stage checks the task again, and the task returns to the queue.
5. If Crew cannot try a task's merge, because any step of its own returned an error (a read of the branch or of a check, a write, the merge candidate), the task waits with the fixed reason "Crew could not try its merge; the error is in Crew's server log", the error goes to that log, and the queue goes on to the next task (since 0.92). The reason stays, as every reason of the pipeline, until the task moves or a run of it starts. An error of the board on one task does not stop the pipeline's steps of another either.

So `main` always passes its checks, and two tasks that each work alone but not together are caught before they land.

**A check that fails only sometimes (R178).** A command check that fails on the merge candidate is run once more. If it passes the second time, the merge goes ahead and the check is recorded as flaky, which is the signal Flaky check (10.2). If it fails twice, step 4 applies.

### 7.11 Subtasks

The work inside a task is recorded as subtasks (R91). A subtask is one piece of work done by one agent run. The task view lists every subtask the task is using or has used (20.2).

| Created by | When | Example |
| --- | --- | --- |
| The pipeline | Each time the task enters a stage that has an agent | `SF-42.1` Analyse the task. `SF-42.2` Build. `SF-42.3` Review the change. |
| The pipeline, after a failed stage | The ticket stays in its stage, and the builder fixes it there | `SF-42.4` Build: address the review. |
| An agent, for its own ticket | It sees more work inside the ticket that deserves a run of its own, or the lead splits the build into parts (`create_subtask`, 7.5) | `SF-42.2` Build: the API endpoint. `SF-42.3` Build: the page. Both at once. |
| The backend, after parallel subtasks | The subtask branches are merged into the ticket's branch | `SF-42.4` Join the parallel subtasks. |
| An agent, for itself | It starts a helper for part of its work (`add_helper`) | `SF-42.2.1` Find every caller of the old function. |
| The backend | Work with no agent, or with a small model | Merge into `main`. Write the summary. |

Each subtask has:

- a number under its ticket, a title, and a kind,
- a state: queued, running, waiting, done, failed, or cancelled,
- who created it: the pipeline, the backend, or an agent, with the run that asked for it,
- the agent role, the model and the effort with where they came from, the baseline version, the run, and the sandbox,
- the prompt it was given (8.6),
- its steps: a checklist the agent writes at the start and ticks off (`set_steps`), so progress inside a long run is visible (R92),
- when it started and ended, its tokens, and its result.

**Created subtasks.** A subtask an agent creates with `create_subtask` waits in the queue of its ticket and then runs like any other: one agent run, its own sandbox, its own prompt. The prompt holds the ticket, and what the creating agent wrote for this subtask.

**Parallel subtasks and joining.**

| Step | What happens |
| --- | --- |
| 1. Start | Every queued subtask whose `after` is done, and whose files no running subtask names, starts, up to `max_parallel_subtasks`. Each gets a branch `sub/<ticket>.<n>` cut from the ticket's branch as it is then. The git endpoint refuses a push from a subtask outside the files it named. |
| 2. Work | Each run pushes only to its own subtask branch. It can read the ticket's branch, but not the branches of the other subtasks. |
| 3. Check | When a run ends, the backend runs that subtask's own check on its branch, if it has one. The ticket's checks cannot pass on one part, so they run after the last join. |
| 4. Join | The backend merges the subtask branch into the ticket's branch, one at a time, in a work tree on the machine. A check sandbox runs the checks. After the last one it runs the ticket's checks on the result. This is recorded as a join subtask. |
| 5. If it does not join | A conflict, or checks that fail on the result, create one builder subtask with both sides and the output. It runs alone. It does not count toward `max_added_subtasks`. If the joined work fails twice, the lead looks at the ticket again (7.7). |
| 6. Done | When nothing is queued or running, the ticket asks for its move, and the next stage sees one branch. |

A subtask that the lead planned to run alone, or that an agent added while another was running on the same files, simply runs after the others on the ticket's branch.

**Helpers.** A helper subtask is run by a subagent that the agent starts with its CLI's own subagent feature, such as the harness's `explorer`. It runs inside the same sandbox as its parent and finishes before the parent continues. It is shown under its parent, and stage recovery inspects the newest non-helper subtask (`lastNonHelper`) so finished child helpers never mask a stopped parent build or fix (since 1.46).

**Retries.** A run that the watchdog stops is retried once. The retry is a second run of the same subtask, not a new subtask. A `build` or `fix` subtask that already pushed a commit on the ticket's branch and recorded `request_move` or `fix_done` before hitting its turn, quota, or timeout limit is completed by `reportedDone` instead of burning a retry on already-handed-over work (since 1.46).

Subtasks are records, not cards. They do not move through stages, and they have no why, what, and goal of their own. They take those from their task.

### 7.12 Tickets and the development workflow

**Who does what in a merge.**

- Crew merges on the machine. The changelog entry, the decision pages, and the documentation index are part of the same merge commit, so `main` moves once for a ticket and a merge that waits behind it is not made stale by Crew's own commits.
- With a GitHub remote, Crew pushes `main` and then marks the pull request as merged. It does not ask GitHub to merge. While GitHub cannot be reached, the merge is made on the machine and pushed later.
- When `main` moved on GitHub, because you pushed or a pull request was merged there, Crew fetches it first and takes it as the new base. It then runs the ticket's checks on that `main`. If they fail, Crew creates a fix ticket. Work that came in from outside is not taken as checked.
- **How it is built** (since 0.43). Crew fetches the remote's `main` every `git.fetch_minutes`, and before a merge at most once a minute, into its canonical repository. A `main` there that follows Crew's is taken as the base, and the project's setup and test command run on it in a check sandbox; a failure makes a fix ticket, which an agent analyses like a ticket an agent created. While a remote `main`'s check is running or its fix ticket (`remote_mains.ticket`) is open, `mergeQueue` waits for it (except to merge that fix ticket itself), so unrelated tickets do not run `PurposeMerge` or take fix rounds from a broken `main` (since 1.26, register row 19). Similarly, when the setup's fix ticket (`CREW-35`, register row 10) merges to Done, `watchSetup` starts the check of the setup on `main` again on every pass until the check runs and clears `setup_failures`, even if the first pass's check sandbox could not start (since 1.26). One that changes files that are code (13.8) is not taken by itself: the inbox has it, with Take it as main and Leave it. One that went another way than Crew's (Crew merged, and someone pushed there) is merged with Crew's main without a work tree; the merge is checked first, then `main` moves there and is pushed, which the remote takes as it follows its `main`. Crew's merges wait while that check runs. When the two do not join, by a conflict or by failing tests, Crew makes a ticket that joins them: its builder reads the remote's `main` as the branch `remote/main`, and Crew's other merges wait for that ticket. What was found is in `remote_mains`, and the Git screen says when `main` was last fetched and what came in.
- A branch that changes a file GitHub runs, such as a workflow under `.github/workflows/`, is not pushed to GitHub and is not merged until you agreed in the inbox (13.8). On a repository that Crew creates, GitHub Actions is off until you switch it on.

A task is a ticket (R85 to R90). Crew is the ticketing system the agents work in, the way a team works in an issue tracker.

| Part of the workflow | In Crew |
| --- | --- |
| Ticket number | The project's key and a counter: `SF-42`. The key is chosen when the project is created. |
| Kind | `feature`, `fix`, `chore`, or `docs`. |
| Reporter and assignee | Who created it: the breakdown, an agent role and run, or the operator. Who is on it now: the agent role and run of each running subtask. |
| Status | The stage. |
| Links | `depends_on`, `created_from`, `duplicates`, `fixes_bug`, `replaces`. A ticket shows what it waits for and what waits for it. |
| Branch | `task/SF-42-lock-account`, created by the backend when the task enters In progress. |
| Commits | Every commit message starts with the ticket number and carries it as a trailer. |
| Pull request | With a GitHub remote: one per ticket (R88). |
| Review | The Review stage. Its verdict and findings are a record on the ticket. |
| Continuous integration | The exit checks, run by the backend in fresh sandboxes. |
| Merge | By the backend, after the checks pass on top of `main`. The ticket is then Done. |
| Changelog | An entry written in the merge commit, from the ticket (9.9). |
| History | Every change to the ticket, read from the database: stage moves, edits, comments, subtasks, and runs. Its commits are joined in from git. |

**Pull requests.** When the project has a GitHub remote and `gh` is signed in, the backend opens a pull request for the task's branch when the task leaves In progress. It posts the review verdict, the test and end-to-end results, and the check results on it as comments and statuses. When the last stage passes and the checks pass on top of `main`, the backend merges on the machine, pushes `main`, and marks the pull request as merged. Agents work on the branch, and the backend mirrors their records to the pull request. Without `gh`, or without a remote, the same flow ends in a merge commit, which Crew pushes.

- **How it is built** (since 0.43). `git.merge` decides: `auto` opens pull requests with a GitHub remote and `gh` signed in, `pull_request` with a GitHub remote always (they wait for `gh`), and `merge_commit` never. The ticket's branch is pushed to the remote from the quarantine repository, where its runs pushed it, never forced, and again at every new head while it is open; a head that changes files that are code waits until the operator allowed that head (13.8), and the pull request is not opened before its branch is on the remote. Crew opens it through GitHub's API with `gh`'s token, and finds the one that is open already when an earlier answer was lost. Every entry of a review, a test, an E2E tester, a verifier, or a stage of the project's own is a comment, and every check run of Crew's a status of the head it is about (`crew/<purpose>`; a merge candidate's status is about the head that merged; the checks that let it leave In progress are posted when it opens). At the merge the branch is pushed at the head that merged before `main` is pushed, so the push of `main` marks the pull request merged: Crew never asks GitHub to merge. A ticket that is closed closes its pull request. What Crew tells GitHub waits in a queue of its own and is tried again, as a push is (`github_posts`), so GitHub that cannot be reached holds up no ticket. The Git screen lists them, and the ticket's view links its own.

**How agents use tickets.** Through the Crew MCP (11.5): they read and search tickets, create them, link them, and add notes and replies to the one they work on. An agent cannot move another ticket or change its fields.

**Replaces.** A ticket that replaces a cancelled ticket carries the link `replaces`, and the cancelled ticket lists what replaced it. A subtask is tagged to its ticket in the same way, so the board always shows which ticket had to be split.

### 7.13 After Done, and removing a project

The design used to stop at Done (R166).

| | What happens |
| --- | --- |
| Done | The project stays in the list. Its board, records, and documentation can be read. A hosted project keeps running. |
| More work | A ticket the operator adds opens the project again and needs no new OK (7.3). An amendment to the plan is broken down like the plan and needs the OK. Since 0.44: the lead's breakdown of an amendment proposes its tickets into Planning while the project runs on; the inbox asks for the OK as for the plan, and the OK moves them to Backlog. |
| Revert | Since 0.44: a revert is a new ticket of the kind `revert`, linked to the Done ticket by `reverts`. The backend writes its analysis, so no lead run is spent, and in In progress makes the commit `git revert -m 1` of the merge on its branch. It then passes the project's checks and stages like any ticket. A revert that does not apply gets a builder. The Done ticket stays Done. |
| A cancelled ticket | It stays closed. To try the work again, add a ticket. It can name the cancelled one and take commits from its branch. |
| Protected | Since 1.34: a project has `Protected` (`projects.protected`, `PUT /api/projects/{p}/protected`), off by default, and always on and locked for Crew's own project (16.2). While `Protected` is on, a blue `Protected` bubble shows on its card on Projects and in the header of its Board and Settings screens, and both Archive and Delete are refused and disabled in Settings (`Archive or delete this project`) until `Protected` is switched off. |
| Archive | Hides the project from the lists. Nothing is deleted. A hosted project is stopped first. Refused while the project is `Protected` or is Crew's own project (since 1.34). |
| Delete | Asks for the project's name to be typed. It removes the repositories and the hosted data from the machine, and the project's rows from the database. Crew offers an export first (6.2). The repository on GitHub is not touched. The improvements already made stay (parked question 19). Since 0.44: the project is archived first, so nothing new starts, and its runs are stopped; a run that does not end within a minute refuses the delete. Its rows go in one transaction, its history included, through one database function the server may call; the row in `deleted_projects` says it existed. The repositories go after that. Since 1.34: refused (both in `Board.MayDelete` and in `crew_delete_project`) while `Protected` is on or for Crew's own project (`improvements.crew_project`), as well as for the fixture project. |

### 7.14 Two-tier spec planning, Coordinator backlog scheduling, and stop-and-research (M22 to M23, since 1.47)

- **Two-tier Planning outside Backlog (manual foundation + autonomous peer-to-`@planner` feature planning):**
  1. **Project Planning (`.crew/specs/000-foundation.md` + initial `WORKFLOW.md`):** interactive with `@user`, and requires explicit operator Project OK before the factory starts (Safety Rule 12).
  2. **Feature Planning (`.crew/specs/NNN-<slug>.md`) — human or autonomous peer-to-`@planner`:** not always manual from `@user`. Whenever `@coordinator`, `@builder`, `@researcher`, or another peer hits a feature design question or missing contract, it posts `@planner <question>` in the feature's thread under `#<project>` (`ask_planner` / `post_chat`, spawning or resuming a tracked `spec` task via `Tasks API`). `@planner` (Gemini `pro` -> `flash`, Rule 15) and the asking peer deliberate in that thread within `max_agent_hops`, write or update `.crew/specs/NNN-<slug>.md`, record the decision in `decisions/` (`decided_by`, alternatives, rationale), mark the Feature Spec `approved (autonomous)`, and hand it to `@coordinator` to emit linked `.crew/tickets/<KEY>.md` YAML tasks into `Backlog` — escalating to `@user` in `#<project>` and the Inbox **only** if the proposal alters `000-foundation.md` scope/non-goals, touches `13.8` protected files (`WORKFLOW.md`, `.agents/skills/`, `.github/workflows/`), exceeds budget/network limits, or `approvals.feature_specs = manual`.
- **Zero untracked work (`Tasks API`, `R217`) & Backlog gate:** No agent session runs without a tracked `Tasks API` record linking its YAML contract, token ledger, sandbox, and Trajectory trace. Executable code work (`feature`, `fix`, `chore`, `docs`) enters `Backlog`, ranked per project by `@coordinator` (`depends_on`, spec order, risk, blocked retries) while Crew's Global Queue (`7.9`) allocates `max_concurrent_runs`, Kata VM room, and `agy` subscription quota fairly across factories. Non-code work (`spec`, `research`, `dream`, and actionable `@agent` work spawned from `#<project>`) mints typed trackable tasks outside the code `Backlog`.
- **`max_tries` circuit breaker to clean-context `@researcher`:** When a build or fix agent reaches `max_tries` (default `3`), Crew stops the run, keeps WIP on `refs/crew/wip/<run>`, and spawns `@researcher` in a **fresh ACP session with a separate diagnostic prompt**. `@researcher` sees only the ticket YAML (`why`, `what`, `goal`), failing check output, branch diff against `main`, and high-validity Dreamstore entries (never the failed builder's noisy conversation). It writes `<KEY>-research.md` and shares its diagnosis in the feature's thread under `#<project>` so `@coordinator` can resume a fresh `@builder` run, escalate a design question to `@planner`, or ask `@user`.
