## 9. Project knowledge

Each project keeps six kinds of knowledge beside its board: documentation, memory, bugs, feedback, evals, and analytics (R57 to R64, R102 to R109). They belong to one project, and its agents reach them through the Crew MCP. Crew's own level reads all of it as signals (section 10).

### 9.1 Overview

| Area | What it holds | Stored | Agents read | Agents write | Operator |
| --- | --- | --- | --- | --- | --- |
| Documentation | What the project is, how it is built, and why (9.9) | `docs/` on `main`, indexed in the database | In every clone, and by search | The builder of each task, for the pages the task names | Read, search, edit |
| Memory | Short hints for agents: conventions, facts, lessons | The database | Index in every run, search, entries tied to files | Any role adds entries. The lead curates. | Edit, retire |
| Bugs | Observed defects | The database | List and read | Any role reports. A fix task is created in the backlog. | Report, close |
| Feedback | Judgments of someone's work that should change future work | The database | List and read | Any role gives feedback on another role's work. The lead records what was done about it. | Give, dismiss |
| Evals | Cases and results that measure the agents | The database | Read results | The lead proposes a case, also from what other roles reported | Add cases, start runs, compare |
| Analytics | Flow, quality, and use (tokens and time) | Derived, cached in the database | Query | None. It is derived. | Dashboard |

```
 operator and agents --+
 discards, rework -----+--> [Feedback] ---+
                                          |
 agents find defects -----> [Bugs] -------+--> lead: housekeeping and retrospective
                                          |                 |
 summaries, run records --> [Analytics] --+                 |
                                                            |
        +-------------------+-------------------+-----------+-------+
        v                   v                   v                   v
    [Memory]           [Eval cases]        new tasks in       proposed changes
  used by every       guard later          the backlog        to prompts, checked
  later run           changes                                 by evals
```

### 9.2 Isolation between projects

- **Storage:** the documentation lives on `main` in the project's own repository. The other areas are rows in the database, and every row carries its project.
- **Tools:** the per-run token names one project. No tool an agent can call takes a project as a parameter. Only the operator's own tools name a project (11.7).
- **Sandbox:** each run has its own sandbox, which can clone only its own project's repository.
- **The CLI's own memory is not used:** every run has a fresh home, so nothing a CLI remembers by itself is carried from one run or one project to another.
- **Above the projects:** the operator and Crew's improver see all projects. No project agent can. What Crew learns comes back to a project only as a new version of the baseline, in general words (10.6).

### 9.3 Memory

An entry, as an export writes it:

```markdown
---
id: "0007"
kind: convention            # convention | fact | decision | lesson
title: Run tests with make test, not pytest directly
applies_to: [builder, tester]   # roles, or all
paths: []                   # optional globs: shown when an agent touches a matching file
keywords: []                # optional: shown when a message contains one
source: {run: r_8f3a2c, task: SF-42, feedback: null}
author: builder             # an agent role, or operator
status: active              # active | retired
created: 2026-10-07T11:40:00Z
---

`make test` starts the test database first. Calling pytest directly fails with a connection error.
```

The backend generates the memory index: one line per active entry, capped at `index_max_chars`.

**How memory reaches a run.** Memory is one of the three things a run inherits from earlier runs, since sandboxes share nothing. The others are the code in git and, for the same role on the same ticket, its own conversation (12.10).

| Memory | Delivered as | Effect |
| --- | --- | --- |
| The index, filtered by the run's role | A part of the run's prompt | In every run |
| Entries with `paths` | Named in the prompt when the ticket's files match | Shown before the agent touches those files |
| Everything else | `search_memory` and `get_memory` on the Crew MCP | Read on demand |

Memory is presented to agents as hints that may be out of date, not as instructions.

**Writing.** Any role calls `remember`, and the entry is active at once, with no approval step. The backend requires a kind, a title, and a source, rejects text that looks like a secret, rejects near-duplicates, and saves the entry. A near-duplicate is an active entry of the project whose words of title and text overlap the new one's by 0.8 or more (Jaccard, on lower-cased words); the refusal names it, so the agent can update it instead. Search over memory and over the documentation is PostgreSQL's full-text search, with the `simple` configuration (since 0.46).

**Keeping it healthy (R59).** The lead merges duplicates and retires stale entries in housekeeping. When the index is over its cap, the lead must consolidate before new entries are accepted. The operator can edit or retire any entry, and agents cannot override an entry the operator wrote.

**Risk.** An agent can record something wrong, or something it was tricked into by text it read, and every later run would read it. Direct writes accept this risk. What limits it is the provenance on each entry and the operator's priority over agent entries.

### 9.4 Bugs

A bug, as an export writes it:

```markdown
---
id: "0012"
title: Lockout counter resets when the email has capital letters
severity: medium            # low | medium | high
status: open                # open | in_work | fixed | closed
found_by: tester            # an agent role, or operator
found_in: {task: SF-42, run: r_a2c4e1}   # the tester's run
fix_task: SF-51            # the task created in the backlog for it
in_task: SF-40             # the Done task whose change the defect is in, when known (12.3; since 0.46)
in_confirmed: true         # in_task is more than its reporter's word: the operator's, or what Analysis found (since 0.54)
regression_check: null      # the check that proves it stays fixed
closed_reason: null         # duplicate | not_reproducible | wont_fix | discarded (Analysis discarded its fix task)
created: 2026-10-07T13:20:00Z
---

## What happens

## Steps to reproduce

## Expected
```

| Step | Who | What happens |
| --- | --- | --- |
| Report | Any role with `report_bug`, or the operator | The bug is saved, and a `fix` task is created in the backlog as `waiting`, linked to it. A defect inside the agent's own task is simply fixed there. |
| Analysis | Another agent (7.6) | It tries to reproduce the bug. Approve: the fix task is ready to build, and the lead says which Done task's change the defect is in, as it found it in main's history, or none (`bugIn`, since 0.54): the reporter's word is not taken. The task must have been Done before the bug was reported. A reviewer, which analyses a fix task the lead created, places no bug: it is an agent that checks, whose model the escapes choose, and such a bug stays unconfirmed. A decision that does not make the fix task ready, a proposal or a notice that waits, confirms nothing, and such a bug stays unconfirmed: it counts no escape. Once confirmed, or reported by the operator with its task, the bug's task is settled: a later analysis, of a replacement fix task, does not change it. Discard: the bug is closed with the reason. |
| Fix | The fix task | It must have a check that fails before the fix and passes after it. The base check of Analysis (7.6) is that proof: it runs the ticket's command checks on `main` before any work and refuses a decision whose checks that are not marked keeps pass there. The first of them that failed there is recorded as `regression_check` (since 0.46). |
| Close | Backend | The bug becomes `fixed` when its task is Done (R60). |
| Learn | Lead | The lead can propose an eval case from the fixed bug. |

### 9.5 Feedback

Feedback is a judgment of someone's work that should change how that work is done from now on. It comes from the operator and from agents on each other's work. A comment or a review is about one piece of work and gets it fixed now. Feedback is about future work.

```markdown
---
id: "0005"
from: builder               # operator, an agent role, or system for captured events
about: lead                 # the role whose work it concerns
run: r_8f3a2c
kind: correction            # praise | correction | rule (rule: operator only)
target: {task: SF-42, record: ticket}
status: new                 # new | applied | dismissed
outcome: null               # memory 0009 | task 0055 | override o_03 | none, with a reason
created: 2026-10-07T10:05:00Z
---

The goal did not say what happens for an unknown email. I had to guess.
```

**Where it comes from (R61).**

- The operator: a feedback box on the Knowledge screen, with Make it a rule, and a "remember this for the project" tick on a ticket's comment. Boxes on the plan and on every other record come with M17 (since 0.46).
- Agents, with `give_feedback`: any role can give feedback on another role's work, naming the role and the task or record.
- Captured by the backend, with `from: system`: a breakdown the operator sent back, a task discarded in Analysis, and a task the lead had to look at again after too many fix rounds.

| From | About | Typical feedback |
| --- | --- | --- |
| Builder | Lead | The goal was ambiguous, a check was missing, or two tasks overlapped. |
| Reviewer | Builder | The same kind of problem keeps coming back across tasks. |
| Tester | Builder, reviewer | The tests missed a case. The review approved something that then failed. |
| Builder | Reviewer, tester | Changes were requested outside the task's goal. |
| Lead | Any subagent | A created task had no evidence. Notes were too thin. |

Rules that keep it useful: an agent cannot give feedback on its own role, a run can give at most `knowledge.feedback.max_per_run` pieces, and feedback does not change the current task.

**What happens to it.** A `rule` from the operator becomes a memory entry at once. The lead handles the rest in housekeeping: each piece becomes a memory entry scoped to the role it is about, a task in the backlog, a proposed override of a prompt for this project, or is dismissed with a reason. The lead cannot dismiss feedback about the lead. Every piece of feedback is also a signal to Crew, whatever the lead does with it.

### 9.6 Evals

Evals measure how well this project's agents do known work, and whether a change to them helps or hurts. The project's own tests are checks, not evals.

```markdown
---
id: "0003"
title: Lock account after failed attempts
origin: {task: SF-42}      # or a bug, or operator
base_commit: 9a1c4e7        # the code before the task was done
why: The plan says a stolen password must not be tried without limit.
what: Count failed sign-ins per account and lock for 15 minutes after five.
goal: Five failed sign-ins lock the account for 15 minutes.
checks:
  - cmd: "pytest tests/auth/test_lockout.py"
held_out: [tests/auth/test_lockout.py]   # copied in at check time, hidden from the agent
status: active              # active | waiting | retired (waiting: the fit check listed something, or has not run yet; since 0.51)
---
```

| Topic | Design |
| --- | --- |
| Where cases come from | Completed tasks, chosen from their summaries (favouring tasks that needed rework), fixed bugs, and the operator. A proposed case gets a fit check first (since 0.51), whoever proposed it: a judge on the other model family than the project's builder, as every judge (13.8), reads the case's text and its held-out files, and lists what the files need that the text does not state, such as a name, a constant, or a behaviour. A case is `waiting` from its proposal while its check is to run or runs. A case whose list is empty is active at once, unless its judge read only part of its held-out files. One with a list is `waiting`, with the list, on the Evals tab, until the operator accepts it or changes its text; a changed text is checked again. While the builder's model cannot be read, or no judge of another family is offered, the case is `waiting` with an empty list and the reason "not checked", and the check runs when it can, as a check at a limit does (12.7). A case whose held-out files cannot be read, or whose judge's answer cannot be read three times, waits for the operator with why. A case that measures guessing measures nothing. The judge avoids the family of the project's builder, not also the family that wrote the held-out files: with two families, avoiding both can leave no judge (since 0.52). |
| How a case runs | A scratch branch, `eval/<run>`, is created from `base_commit` and removed after the run. A builder, a reviewer, and a tester work on it as on a real task, each in its own sandbox. The reviewer and the tester read the branch with tools and push nothing, as a ticket's do, with a ticket's run time; neither is a judge, and a reviewer whose run ends before it gives a verdict measured nothing (since 0.50). A run of an eval that ended for Crew's own reason measured nothing: a restart, the operator, a CLI that refused, its sandbox only when its microVM never started, before any of the agent's code ran, the project's setup only for the eval run's first builder's run, before any head was recorded or round spent, while its branch is still at `base_commit`, a workspace `crew-run` could not make, read by the step the server took from `exit.json` and kept in the run's `end_step`, never by its detail, and a sign-in only when Crew's own login check, recorded after the run ended, found the CLI signed out. Until a check after the end is recorded, or while one refused once and not yet twice, the eval run waits in its state; a check that could not run is read by the state it left (13.9). A limit and a model are the run's own word, which no check of Crew's confirms for `agy`, whose check reads no limits and whose adapter gives no model end: such an end is the agent's, as an error is, so a builder's is a goal not met, and a reviewer's with no verdict still measured nothing (since 1.01; 13.9). A microVM that ended during a turn may be the agent's, which can be root in it, and a setup that failed on the builder's work is the branch's: a builder's such end is a goal not met, a reviewer's or a tester's setup end on the builder's work, which is the home's word, goes on to the case's checks, which run the setup on the head themselves, and a reviewer whose microVM ended gave no verdict (since 1.02; 13.9). They take the files that instruct agents from `base_commit`, as a judge takes them from `main` (13.8), and a branch whose history no longer holds `base_commit` has not met its goal (since 0.49). The held-out files are copied in only when the checks run, and a file or a folder of the branch where one goes gives way to it (since 1.02). Since 1.24 the check sandbox starts in the project's check mode with the branch's head alone, without the held-out files, and runs the project's setup on that tree; Crew then cuts every route out of the sandbox from outside the microVM, disconnecting the container from its bridge and dropping the sandbox from the server's door, proves from the guest that a connection to the former door and to a public address fails, and only then writes and unpacks the held-out files over the tree and runs the case's commands with no network, so what the commands need must come from the project's setup; the cut has not been tried live on Kata from a sandbox (13.6, 13.9, register row 22). How the case's check ended is read by where an error came from (13.9, since 1.02): a setup that failed or ran past its time on the branch's tree, a command past its time or ending with 124, a check sandbox that ended under them, and a tree larger than a check sandbox takes are the branch's, a failed round as a failed check is, which spends a fix round and at the last one is a goal not met; only a check sandbox that gave no result, because it could not start or take the tree, or because its cut or the placement of the held-out files failed after the setup (since 1.24; register row 8), and Crew's own steps before it measured nothing. Nothing is merged. |
| What is measured | Goal met or not, check rounds, rework, and duration, compared with the original task's summary. |
| Where results go | A row in `eval_runs`, with the baseline version, the project's overrides, the CLI and its version (its builder's run's, since 0.57), the model, the effort, and the Crew version. The output of a failed held-out check is kept with the row, redacted as a run's detail is and cut to its last 64 KB, and the Evals tab shows it to the operator; the builder learns only which commands failed (since 0.51), or that the project's setup failed on its branch or that its tree is larger than a check sandbox takes, never what the setup printed (since 1.02); the kept output names each command's error (since 1.02). A suite that ended having measured no case shows "nothing measured" and why, never a share of 0%, and Analytics counts it beside the shares rather than drawing it (since 0.50). A suite keeps the kind of what its next agent run waits for: a model, a sign-in, a pause of everything, or room on the machine; a model's wait while no CLI is signed in is a sign-in's (since 0.52). |
| When it runs | On demand, before a proposed change is applied, and in a release's gate when the release changes what agents see (16.2 step 5, since 0.52). |
| The case's code | `base_commit` is the first parent of the ticket's merge commit. The held-out files are test files the ticket added or changed, checked against the merge's tree. The checks are the ticket's command checks, without its `judge:` checks (since 0.49). |
| Limits | The runs have the scope `eval` and no ticket. `eval/<run>` is only in the quarantine repository, never on a remote. The reviewer runs on the other model family than the builder, also when it moves to its next choice at a limit (12.7); the tester is no role that checks, as a ticket's is not, and runs on its own choice (12.3; since 1.14, where this row named the tester with the reviewer: the operator's popup answer of session 81, "Fix the design text"). A reviewer starts only once the builder's model was read and is not empty, since with no family to avoid it could run on the builder's: a model that reads empty, or no builder's run, makes it wait as at a limit, and a read that fails starts no run on that pass (since 1.13; the operator's popup answers of session 80, "Seven + eval's model read", and after the change's security review "Fix: wait as atlimit"). An eval run that ends, or whose suite stops, ends its subtasks that wait, so nothing continues them by itself after its branch is gone (since 0.50), and stops its runs that are still going, as a ticket's runs are stopped (since 0.51). At most one suite per project and one on the machine at a time, and none while the project is paused. A suite is bounded by its cases times its repeats, each run under its own limits; since 0.53 it has no cap in dollars. |
| A release's suite (since 0.52) | A suite of purpose `release` belongs to a release's gate (16.2 step 5) and runs the cases it is given once each, on side `release`. Its builder, reviewer, and tester start from the candidate's runtime image, which the suite names by its id and each run's subtask keeps, so a run that continues the subtask starts there too; the run and its sandbox record it, and the held-out checks run in the running release's image. Its prompts lay the candidate's six eval files, the builder's, the reviewer's, and the tester's stage and agent files from the release's commit, over the baseline, and name them as the release's; a file the project has an applied override of keeps the override. Its runs record the release's version. It is compared with an earlier suite's baseline side by the rule an override is judged by, on the cases the earlier suite measured and the release ran, with each case's runs in the order of their repeats, since the earlier suite ran each case more often; a run of the release that measured nothing keeps its case in the comparison and counts as a goal not met for its bound (10.5, since 1.10), and one of the earlier suite is left out; its result holds that side, its own side `release`, the earlier suite, and the decision. It sends no signal, and Analytics and `list_eval_results` leave it out: it measures a candidate, not the project as it runs. The Evals tab names it by its gate and shows its own side where a change shows. A release suite waits behind the operator's own queued suites, and is refused while one of the project's suites waits or runs, as every suite is. It runs only while its gate runs it: the evals' pass first stops a release suite whose gate is not live, whose suites step ended, or whose gate does not name its project with it, or with no suite yet, as between its start and its record. |

**Guarding changes (R62).** A project's lead can propose an override: a new version of one stage prompt or one agent definition, saved in the database and applied to this project only. An override of a role that checks, which is the reviewer, the tester, the E2E tester, the verifier, or the judge, waits for you, and so does one of a file no eval runs: only the builder's stage and agent files are measured (since 0.49). The project's eval suite runs with the override applied. When the result is not worse the override is applied, and the operator is told and sees the result next to the previous one. Crew takes it back if the project's results get worse by the measure of 10.5, over the project's next 20 tickets. An override is the whole file, with versions, one row per project and path. The lead proposes it with `propose_change`, the operator can write one, and one of the prompt writer's files waits for the operator like a checking role's. With fewer cases than `knowledge.evals.min_cases` it waits in `testing`, and the operator can apply it by hand (since 0.49). So does one whose change measured nothing in runs that, counted as goals not met, would make it worse (10.5), and the inbox's item says the evals' why (since 1.10). The same suites guard improvements to the baseline, which are tested in every project that has cases (10.5).

**Caveat.** A project starts with no cases, and a small suite gives noisy results because models do not behave the same way twice. That is why a change that passed its evals is still measured after it is applied, and rolled back by Crew when the measured result is worse (10.5). `knowledge.evals.repeats` sets how often each case runs, so a suite is its cases times its repeats. A release's suite is compared with an earlier suite whose models and overrides may differ from its own, since the backend's choice of a model moves with its numbers: the comparison mixes those in, and the eval runs keep both for the operator (since 0.52).

### 9.7 Analytics

Analytics are computed from task summaries, run records, stage moves, bugs, feedback, and eval runs. They are cached in the database and can be computed again.

| Group | Metric |
| --- | --- |
| Flow | Tasks done per day, time in each stage, work in progress against the limits |
| Flow | Time waiting: for the operator, for a lead slot, for room on the machine, for analysis |
| Quality | Tasks whose checks pass on the first request, rework per task, tasks the lead looked at again, and cancelled tasks |
| Quality | Tasks created by agents, and how many were approved, skipped, discarded, or given notice as a new feature, per creating role |
| Quality | Bugs opened and fixed, feedback per role, eval results over time |
| Quality | Watchdog warnings and stops |
| Quality | Documentation pages that are current, and tasks that changed code without the page that covers it |
| Use | Tokens and time per task, role, model and effort, and part of the plan |
| Use | Tickets added by agents, as a count and as a share of the plan's tickets |
| Use | Sandbox start-up and setup time per run |

Agents query these through the Crew MCP (R63).

### 9.8 Housekeeping and retrospective

- **Housekeeping run.** A lead run that starts when there is feedback to handle, a memory index over its cap, or documentation marked as out of date. It has lower priority than analysis, and since 0.53 no weekly budget. At most one is going per project, at most one starts in 24 hours, and none starts while the project is paused (since 0.46). It is built in M12, which brings its three inputs (since 0.44).
- **Retrospective (R64).** It is built in M15, with the eval cases and the changes it proposes (since 0.46). When a part of the plan is complete, and when the project is Done, the lead reads the summaries of its tasks. It writes lessons as memory entries, proposes eval cases, and proposes changes. The final report has a "What we learned" section. A part is complete when every ticket of it is Done or cancelled and at least one is Done. The retrospective is a lead run in the class of housekeeping, once per completed part and once at Done, with no weekly cap (since 0.53); at project Done it runs before the final report (since 0.49). A hidden project, the gate's `crew-gate`, has none: it is Done after each gate's real ticket, which measures the gate, not the work, and a lead run there would use the subscription outside the gate's real ticket and propose eval cases on a project with no screen (since 0.52, from the gate of 0.2.15 on dave). Its housekeeping stays: it is due only when there is feedback, the memory index is past its cap, or a page is marked, at most once a day, and it keeps the memory that each real ticket's runs read under its cap.

### 9.9 Documentation

Every project has documentation, and Crew stores and shows it (R102 to R109).

**The pages.**

| Page | Path on `main` | Holds | Written |
| --- | --- | --- | --- |
| Overview | `README.md` | What the project is, and how to run it in one minute | From the plan, then by tasks |
| Architecture | `docs/architecture.md` | The parts and how they fit together | From the plan, then by tasks |
| Setup | `docs/setup.md` | How to install, run, and test. The same steps as the sandbox setup command. | From the plan, then by tasks |
| Decision records | `docs/decisions/NNNN-title.md` | One design decision each: the context, the decision, and the reason. Never edited. A later decision replaces an earlier one. | By `record_decision` |
| Guides | `docs/guides/` | How to do one thing with the product | By tasks |
| Reference | `docs/reference/` | Interfaces, commands, settings | By tasks |
| Changelog | `CHANGELOG.md` | One entry per finished ticket | By the backend, in the merge commit |

**Where it is stored.** The pages are markdown files in the project's repository, on `main`, beside the code they describe. A change to a page is made on the task's branch and is reviewed and merged with the code. Crew indexes every page in its database when `main` changes, so the app can show, search, and link them.

**In the app.** A Documentation screen per project: the pages as a tree, each page rendered, search across pages, and for each page the tickets that changed it, when it was last changed, and whether it is current. The operator can edit a page there. The backend commits the edit to `main` with the operator as author. The edit names the commit of the page it started from, and is refused when the page changed on `main` since, so the operator reads the newer page first (since 0.46). Markdown is rendered without raw HTML.

**How it starts (R104).** The breakdown always includes a task of kind `docs` that writes the overview, the architecture, and the setup page from the original plan. For a project made from an existing repository, the existing pages are indexed and the task fills what is missing.

**Documentation is part of done (R105).**

1. In Analysis, the agent names the pages the task must update in the task's `docs` field, or leaves it empty with a reason.
2. The builder changes those pages on the task's branch.
3. The `docs_done` check is one of the exit checks of In progress.
4. The reviewer reads the pages against the change.

**Decisions (R106).** When an agent makes a design decision that later work must follow, it calls `record_decision` with the context, the decision, and the reason. The decision is a row at once, and the backend adds its page to the documentation in the task's merge commit. A choice an agent made by itself with `decide` is listed in Crew (7.5). When it shapes the design, the agent writes it as a decision record too.

**Changelog (R107).** When a task is merged, the backend adds an entry to `CHANGELOG.md` in the merge commit, with the ticket number. It is written from the ticket's title, its what, and the builder's last note. The summary is written afterwards and is not needed for it. The entry and the pages of the ticket's decision records are written into the candidate merge commit as it is built, before the checks on top of `main` run, so the tree that was checked is the tree that lands on `main`. Crew writes `CHANGELOG.md` itself, so a branch's change to it never conflicts: Crew's entry goes on top of main's file (since 0.46).

**Keeping it current (R108).** Each page names the code it covers in its `covers` field, a list of path patterns in the page's front matter. When a merge changes covered code and not the page, the page is marked as possibly out of date. In housekeeping the lead reads those pages against the code and creates a `docs` task for each one that is wrong. The share of pages that are current is a metric of the project, and a signal to Crew.

**How agents use it (R109).** The pages are in every clone. The prompt of a subtask names the pages its task touches. The Crew MCP has `search_docs` and `get_doc` for the rest.

**What is not documentation.** The plan, the tickets, and the records are in the database and have their own screens. Memory holds short hints for agents. Documentation is what a person, or an agent new to the project, reads to understand it.

### 9.10 Dreamstore and validity scoring (M26, since 1.47)

**Dreamstore** evolves project memory (`9.3`) inside PostgreSQL into three cognitive kinds scored continuously for validity (`R215`):

- **Three typed memory tiers:** `episodic` (timestamped run experiences: what failed, what `@researcher` found, and what fixed it), `semantic` (declarative facts, domain contracts, and invariants anchored to `paths[]`), and `procedural` (step-by-step execution recipes eligible for `SKILL.md` synthesis).
- **Validity scoring (`0.0` to `1.0`):** Each entry stores `validity_score`, `verified_commit`, `evidence_runs[]`, `hit_pass_count`, and `contradiction_count`. Merges on `main` touching linked `paths[]` decay `validity_score` (`drift`); subsequent runs that apply the entry and pass checks on the first request boost it; contradictions or failed verification checks quarantine entries below `0.4` so stale or incorrect hints drop out of agent prompts.

### 9.11 Cross-project Knowledge API: shared skills, issues, and specs (M26, since 1.47)

Unlike project-scoped `Dreamstore` (`9.10`), the installation-wide **Knowledge API** (`R218`) collects validated engineering knowledge across *all* factories on the Crew machine without breaking per-project sandbox or credential isolation (`9.2`, `13.7`):

- **Collected across factories, isolated on execution:** `Knowledge API` aggregates three shared artifact classes into `knowledge_entries` inside `StateDB`:
  1. **Shared Skills Registry (`12.11`, `R221`):** Vetted `.agents/skills/<name>/SKILL.md` skills distilled from high-validity `procedural` Dreamstore entries or SHA-pinned external skills (`VENDORED.md`), indexed in `skills_registry` with static AST vetting, live warm-VM sync, and per-factory install state.
  2. **Sanitized bug & issue advisories:** Root causes, failing signatures, and fixed regression checks promoted from resolved factory `bugs` and `@researcher` briefs so another factory hitting the same class of failure reuses the fix pattern before wasting `max_tries`.
  3. **Reusable specs & docs patterns:** Approved foundational architecture patterns and checklists across stacks.
- **Secret scrub & provenance fence before sharing:** Nothing enters cross-project `Knowledge API` raw. Every promoted entry passes `internal/redact` (stripping project-specific hosts, tokens, internal paths, and credentials), carries source project/commit provenance, and is injected into sibling factories strictly inside `internal/fence` untrusted-data framing (`Rule 14` of "Rules that keep the operator safe").
- **Actionable through `Tasks API`:** Applying a skill update or acting on a cross-project bug advisory in a factory never mutates `main` directly—it creates a trackable `Tasks API` item (`chore`, `fix`, or `dream` task, `R217`) with explicit checks and Trajectory linkage.
