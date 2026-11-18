## 10. How Crew improves

### 10.1 Two levels

Crew is used by one developer who has several projects. The projects share nothing with each other. Crew sits above them, has all the data, and receives signals from every project. It uses them to evolve the agents and the pipeline that every project runs on (R75 to R84).

```
                      CREW LEVEL: sees every project

  signals from      +----------+     +-------------+     +----------+
  all projects ---> | improver | --> | improvement | --> | baseline |
  (database)        | (agent)  |     | evals, then |     | release  |
                    +----------+     | a notice    |     | 1.4.0    |
                                     +-------------+     +----+-----+
      ^               ^               ^                       |
      | signals up    | signals up    | signals up            | baseline down,
      |               |               |                       | the same for all
 +----+--------+ +----+--------+ +----+--------+              |
 | Project A   | | Project B   | | Project C   | <------------+
 | own repo    | | own repo    | | own repo    |
 | own board   | | own board   | | own board   |
 | own agents  | | own agents  | | own agents  |
 | own memory  | | own memory  | | own memory  |
 +-------------+ +-------------+ +-------------+
        no path between projects
```

| Level | Who acts there | Sees | Can change |
| --- | --- | --- | --- |
| Project | The project's lead, builders, reviewers, and testers | One project | That project's tasks, records, and knowledge |
| Crew | The improver | The signals, summaries, and trajectories of every project | The baseline, through an improvement that passed its checks and its evals |
| Operator | The developer | Everything | Everything |

The rule in one line: signals go up, the baseline comes down, and nothing goes sideways.

### 10.2 Signals

A signal is a fact about how the agents and the pipeline performed. The backend records each one from events and records as they arrive, with the project, task, run, role, model, effort, and baseline version. Agents do not send signals, so an agent cannot leave one out or make one look better.

| Signal | Recorded when | What it can point at |
| --- | --- | --- |
| Operator intervention | The operator sends a breakdown back, edits a task before the OK, discards a new feature, changes a decision an agent made, raises a project limit, or reverts a merge | A place where the pipeline did not run by itself, or decided differently than the operator would. These count most. |
| Decided alone | An agent recorded a choice with `decide`, or the lead looked at a ticket again. Since 0.53 no ticket passes a budget: an old `over_budget` signal keeps its label and is not counted. | Where the plan left a gap, or an approach did not work |
| Check rounds | A run asks for a move and its checks fail | Builders asking for a move too early, or goals that are unclear |
| Rework, with the reason and the first line of what was found that is not a heading | A review, a test, an end-to-end run, a verification, or the merge check asks for a fix | A kind of defect that keeps coming back |
| Escaped defect | A bug is in a task that is already Done: the operator reported it so, or the analysis of its fix task found it so (since 0.54) | Something the review and the test both missed |
| Analysis decision | A created task is approved, skipped, discarded, or given notice as a new feature | A role that creates tasks nobody needs, or misses ones that are needed |
| Watchdog warning or stop | A run is silent, stuck, repeating, or over its limits | A prompt or a tool that leads agents into loops |
| Feedback | An agent or the operator gives feedback on a role's work | What roles need from each other |
| Asked about a ticket | The operator asks in a ticket's chat, or comments on marked text (11.9) | A summary or a note that did not say what the operator wanted to know, and the kinds of work that raise questions |
| Repeated lesson | The same kind of memory entry is written in several projects | Something that belongs in a role's definition instead |
| Override | A project overrides part of the baseline, and how the project does afterwards | A change that may help every project |
| Stale documentation | Code changes and the page that covers it does not | A builder prompt that lets documentation slip |
| Flaky check | A command check fails on the merge candidate and passes when it is run once more | A test that fails only sometimes |
| Installed tool | A run installs a package | A tool that belongs in the runtime image |
| Tokens and time of a ticket (`cost_and_time`, a name kept in stored rows; since 0.53 it holds tokens and minutes) | A run or a task ends | Stages, roles, or models that use more than they return |
| Rewrite choice | The operator sends a prompt after Crew proposed a rewrite: the rewrite, an edited rewrite, or the operator's own text (7.2) | Whether the prompt writer's rules help, and what the operator keeps changing in its rewrites |
| Sandbox start-up | A sandbox starts, clones, and runs setup | Where waiting time goes |
| Eval result | An eval run ends | Whether a change helped or hurt |

Operator intervention is recorded for a breakdown sent back, a ticket edited before its OK, a new feature discarded, a decision changed, a project limit raised, a merge reverted, and work merged outside Crew. Asking about a ticket in its chat is recorded too, as the signal `asked_about`, which holds no word of the question (11.9, since 0.75).

Signals are rows in the `signals` table of Crew's database. Most can be computed again from the records and the trajectories. Task summaries and trajectories are the evidence behind them. The event sink adds the shape of every session: tools, command verbs, tokens, timings, and errors (12.4).

### 10.3 The baseline

The baseline is what every project runs on. It has two parts, and both are in Crew's source repository. The first is the harness: the global context, the skills, the subagents, and the rules that a CLI loads by itself (12.1). The second is Crew's own stage prompts and pipeline defaults, shown below. Both are part of the Crew release (16.1), so a baseline version is a Crew release, and has its number.

What belongs to this installation is kept apart from Crew's source (R177).

- **The configured source repository** (`CREW_SOURCE_REPO`, e.g. `your-org/crew`). It holds the code, the harness, and the baseline (15.1). It is a project in Crew like any other, so agents work on it and clone it.
- **The database.** It holds what belongs to this installation: the machine's settings, the plan drafts, the improvements with their evidence from every project, and the release notices and holds. No sandbox can reach it. So an agent that works on Crew's code cannot read another project's evidence, and a merged ticket cannot change a machine setting.

The baseline in the source repository:

```
improver.md               # the improver's own definition, not part of the baseline, and a protected part (16.8)
baseline/
  agents/                 # agent definitions: markdown with YAML frontmatter
    lead.md  builder.md  reviewer.md  tester.md  e2e.md  verifier.md
    judge.md  stage.md  summary.md
    prompt-writer.md      # the rules the prompt writer follows (7.2)
  stages/                 # the prompt for each stage and each kind of lead run
    analysis.md  in_progress.md  review.md  test.md  e2e.md  verification.md
    fix.md  join.md  judge.md  look_again.md  part.md  stage.md  summary.md
    housekeeping.md  report.md  conversation.md  rewrite.md  review-text.md
  guides/
    planning.md           # how to develop a plan with the operator
    breakdown.md          # how to split a plan into tasks
  templates/
    plan.md
    task.md
  roles.yaml              # the first model and effort of each agent, which install copies into the machine's settings (12.3)
  tools.yaml              # the words of the Crew MCP's tools, and who may call each (11.5). The server holds their code and input
  baseline.go             # the Go package that builds the baseline into the server (since 0.38)
image/
  tools.txt               # the tool set of the runtime image
```

Since 0.49 this is the tree as it is. The pipeline defaults are in `internal/settings/`, which is protected (16.8), so an improvement cannot change them until they move into `baseline/`.



- **Versions.** Every change to `harness/` or `baseline/` is one commit and ships in one Crew release (16.1). The release number is the baseline version.
- **Following.** Every project runs the baseline of the running Crew release. A project changes it for itself with overrides (below), not by staying on an older version, because one release pins everything together (16.1).
- **Runs.** A run reads the baseline when it starts and records the version in its run record. A running run is not interrupted by a new version.
- **Overrides.** A project can replace a baseline file for itself, with an override that is saved in the database for that project, and can set its own values in its settings. An override stays in that project. It is also a signal.

### 10.4 The improver

**What a patch may touch is a list, and Crew tests it.** Each improvement is checked against the files and the settings keys it may change before it is built into a release. The keys for approvals, the network mode, credentials, and which stages are on are not on the list, and neither are the prompts of the roles that check: the reviewer, the tester, the verifier, and the judge. A change to any of those waits for you. Since 0.31 the model, the effort, and the list of entries of every agent are not on the list either, and neither is the model line of a harness agent file. The test refuses a patch that touches one: a model is chosen by the backend or by you, never by a patch (12.3). The numbers under `agents.auto` are refused the same way. Since 0.49 the list is the `.md` files of `baseline/agents/`, `baseline/stages/`, `baseline/guides/`, and `baseline/templates/`, without the files of the roles that check (`agents/{reviewer,tester,e2e,verifier,judge}.md`, `stages/{review,test,e2e,verification,judge}.md`). The prompt writer's files (`agents/prompt-writer.md`, `stages/{rewrite,review-text}.md`) wait for you (7.2), and so does a change of any file the evals do not run: they measure only the builder's, `agents/builder.md` and `stages/in_progress.md`. A patch gives whole new texts of files.

The improver is Crew's own agent. It is the only agent that sees more than one project, and it never works on a task.

**When it runs.** Every 7 days, or sooner when 25 tasks have finished across all projects since its last run, or when the operator starts it. It uses a lead slot, in the last class of the queue. Since 0.53 it has no weekly budget: its schedule bounds it, and `improvements.run_every_days`, `run_after_tasks`, and `measure_after_tasks` are each 1 or more.

**What it does.**

1. Reads the metrics by role, stage, model and effort, project, and baseline version.
2. Finds where operator interventions, rework, stops, and the use of tokens and time concentrate.
3. Opens the task summaries behind a pattern, and then the trajectories, to find the cause.
4. Reads earlier improvements and what they measured, so it does not repeat one that failed.
5. Proposes the smallest change to the baseline that addresses the cause.
6. Leaves a note for the operator when the cause lies in one project, such as a slow setup command. It cannot change a project.

**Its sandbox.** No repository, and Model only mode (13.6), always: there is no setting for it (since 0.49). Its runs have the subtask scope `improver`, and its token the MCP scope `crew`, made only when it starts. It reaches only Crew's data and the model service. Every other sandbox has internet access, but this agent sees every project, so a tricked improver with a way out could send out more than any other agent.

**Its tools.** A Crew-level tool set, reached with a token of its own kind. No project run can obtain one.

| Tool | Effect |
| --- | --- |
| `get_metrics` | Flow, quality, and use (tokens and time), by project, role, stage, model and effort, and baseline version |
| `list_signals` | Signals, filtered by kind, role, project, and time |
| `search_summaries`, `get_summary` | Task summaries of every project |
| `get_trajectory` | One run's trajectory |
| `get_baseline`, `list_improvements` | The current baseline, and earlier improvements with their measured effect |
| `propose_improvement` | A new improvement: evidence, a patch to the baseline, the expected effect, and the metric that will show it |
| `list_rewrites` | Rewrites of the operator's prompts: the original, the rewrite, the choice, and what the operator edited (7.2) |
| `note_for_operator` | A note about one project, sent to the operator as news |

**What an improvement may change.**

| Part of the baseline | Example |
| --- | --- |
| Agent definitions | The builder is told to run the task's checks before asking for a move. |
| Stage prompts | The build prompt gains a step for a defect that keeps escaping. |
| Guides | The breakdown guide asks for smaller tasks, because large ones need more fix rounds. |
| Templates | The task template gains a line for what is out of scope. |
| Pipeline defaults | A watchdog threshold, a work-in-progress limit, or an added check. |
| The model or the effort for a role | No longer, since 0.31. The backend chooses a model from its numbers (12.3). The patch test refuses a change to these keys. |
| The runtime image | A tool that most runs install is added to the image. That is code, so its release waits for Install (16.3). |

**What it may not change.** The isolation between projects and between runs. What needs the operator's approval. Network rules and what tokens allow. How secrets are handled. Its own definition, and the steps in 10.5. The files of any single project.

### 10.5 From proposal to baseline

| Step | Who | What happens |
| --- | --- | --- |
| 1. Propose | Improver | A row in `improvements` with the evidence, the patch, the expected effect, and the metric to measure. |
| 2. Generic check | Backend | The patch is refused if it contains a project's name, repository, file path, or identifier, or text copied from a project's code or records. Since 0.49: any project's name, ticket prefix or ticket number, repository name or address, a path changed by a cited ticket, or a run of 10 words shared with any project's summaries, records, or memory, or with the files the cited tickets changed. A changed path that the baseline's own text names whole, such as `AGENTS.md` or `docs/architecture.md`, is in every project and is no project's fact, so a patch may name it whole; a longer path that holds it, such as `services/payments/AGENTS.md`, and a path below a folder the baseline names, such as `.agents/skills/<name>/SKILL.md`, still refuse the patch, and the file's words count either way (since 0.50). The evidence stays in the improvement's row, which never enters a project. |
| 3. Evals | Eval runner | The baseline with the patch applied is run against the eval cases of every project that has cases. Each project's cases run inside that project, in its own sandboxes. Only the numbers come back. |
| 4. Decide | Backend | Evals that are not worse let the improvement go ahead. Evals that are worse refuse it. Nobody has to approve. The operator can read the diff, the evidence, and the eval results at any time, and take an improvement out of a release that is not installed yet. |
| 5. Apply | Updater | The patch becomes a ticket on Crew's own board (16.2), the project `improvements.crew_project` names; while it names none, the improvement waits as `passed` with an inbox item (since 0.49), and ships in a Crew release (section 16). A release that holds only such changes to prompt text gives notice and installs after 24 hours, unless the operator holds it. Runs that start afterwards use it. |
| 6. Measure | Backend | After 20 tasks (`improvements.measure_after_tasks`, which also sets a release's window since 0.57, 16.8) have finished on the new version, the named metric is compared with the version before. The result is written into the improvement. A task counts on a version when every run of it used that version; one that used two counts on neither. The metric is one of a fixed list, each with the direction that is better (since 0.49). |
| 7. Roll back | Backend | A result that is worse makes Crew take the improvement out again, by itself: back to the release before it when nothing was installed since, through the updater, and otherwise with a new release that leaves it out (16.5), from a revert ticket on Crew's project. The operator gets news of it. |

One release holds at most one improvement, so a measured effect can be tied to one change: a build takes the commit before a second improvement's merge, and the rest waits for the next release (16.2, since 0.51). When the same release also carries another change, such as a new CLI version, the measurement names it, and the result is marked inconclusive if that change could explain it. Several improvements can be waiting or in testing at once.

An improvement, shown as its fields and its text:

```markdown
---
id: i_0007
title: Builders ask for a move before running the task's checks
status: applied            # proposed | refused | testing | passed | applied | removed | rolled_back
target: baseline/stages/in_progress.md
evidence:
  signal: check_rounds
  window: 2026-09-07 to 2026-10-07
  projects: 4 of 5
  tasks: 37 of 112
expected: More tasks pass their checks on the first request.
measure: first_request_pass_rate
evals:
  projects: 4
  cases: 31
  passed_before: 24
  passed_after: 27
decided_by: evals
release_from: 1.3.0
release_to: 1.4.0
result:
  tasks: 20
  before: 0.58
  after: 0.74
  verdict: better            # better | same | worse | inconclusive
---

## What the signals show

## The change

## Risk
```

**What "not worse" means (R167).**

| Question | Answer |
| --- | --- |
| How is it measured? | Every eval case runs `knowledge.evals.repeats` times on the baseline, and the same number of times with the change. |
| The margin | The spread between repeats of the same baseline: the highest share of passed cases in one repeat less the lowest; zero with one repeat. A difference inside it is noise. |
| Not worse | The share of passed cases is not lower than before by more than the margin. No case that passed every time before fails every time now. Results alone decide: since 0.53 no cost is compared. |
| Too little to tell | With fewer than 10 cases across all projects, the change waits in `testing` until there are more. Nothing ships on no evidence. |
| Runs that measured nothing | A run of the change that measured nothing, for Crew's own reason or by a route the agent's work can shape (13.9), may have left out a case the change failed. So the change is not worse only when it is not worse with each such run on a case the baseline measured counted as a goal not met, and the decision names how many it counted; it is worse when its measured runs alone are worse; otherwise it is too little to tell. An override then waits in `testing` for the operator, whose inbox item says the evals' why; an improvement waits in `testing`, its list saying "waits: its evals could not tell", until the operator removes it, since more cases do not run its suites again; and a release's gate holds the release (16.2 step 5). A run of Crew's own that could not change the verdict changes nothing, and a case that measured nothing on both sides, as when the base's setup fails, is left out of both. The decision's share after and its lost cases are of the side so counted when the change is not worse or could not tell, and of its measured runs when those alone decide, while the suite's own numbers of the side are of its measured runs. An improvement whose cases fall under the minimum later keeps its last evals, so its list can still say "could not tell" while it waits for cases. The baseline's runs that measured nothing are left out, since no change's text reaches the baseline's runs, and the margin is the spread of its measured repeats (since 1.10; the operator's popup answers of session 77, "Bound: decide only if it holds" and "Change side only, named", and after the change's review "Only cases baseline measured", "Hold, like worse", "Label says why + name it" and "All three"). |
| Worse, after it is applied | Over 20 tickets, the named metric is lower than before by more than the margin. |

### 10.6 Keeping projects apart while Crew learns

- **Up:** signals, summaries, and trajectories go from each project to Crew.
- **Down:** the baseline comes from Crew to every project, the same for all.
- **Never sideways:** a project's agents cannot read the Crew level, and no tool they have accepts another project.
- **The baseline is generic.** A lesson that several projects recorded in their memory can become a line in a role's definition, in general words. The memory entries themselves stay in their projects.
- **Evals stay inside.** An improvement is tested in each project with that project's own cases. The cases do not leave it.
- **Deleting a project** deletes its signals, summaries, and trajectories from Crew. Improvements already made from them stay.

**What limits this.** One developer with a handful of projects produces few finished tasks, projects differ from each other, and a model does not behave the same way twice. A difference between two baseline versions is often noise. So an improvement ships only when its evals are not worse, its effect is measured after it is applied, and Crew rolls it back by itself when the measured result is worse. A result from too few tickets is marked inconclusive.

The improver also reads trajectories, and those contain text from the internet that an earlier agent read. Some of that text may try to give instructions. What limits the damage: the improver has no internet access, it can only propose, the generic check and the evals stand between a proposal and the baseline, and the operator can read the diff and take the change out before it is installed.

### 10.7 Crew analytics

The operator has one view across all projects. Agents in projects cannot see it.

| Group | Metric |
| --- | --- |
| Autonomy | Operator interventions per 100 tasks, by kind. This is the headline number: how often the pipeline needed the operator. |
| Quality | Tasks that pass their checks on the first request, rework per task, escaped defects, discarded created tasks, by role and by baseline version |
| Use | Tokens and time per task, by role, stage, model and effort, and baseline version |
| Comparison | The same metrics per project, side by side |
| Improvements | Each improvement with its evidence, its eval results, and what it measured after it was applied |
| Prompts | Plans that started from a rewrite, from an edited rewrite, and from your own text: the questions the planning agent asked, the breakdowns sent back, and the tickets that passed the first time, each with its count. And how often you used, edited, or refused Improve text (7.2) |
| Models | For each agent, its results on each entry it ran on, and every change of a model with who made it, Crew or you, and what it measured afterwards (12.3) |

Since 0.49 the numbers are computed in SQL when read and kept in `metrics` for five minutes, under the Crew version that computed them, so a new version, and the gate's copy of the database, computes its own. Use, the projects side by side, and prompts are sections of the Signals tab of the Crew level; Models is the Models screen. The signals by kind and project add up to All in every row: a signal of no project, such as a plan's choice of a rewrite, has a column "No project", shown when there is one, and the fixture and an archived project, which have no column, are not counted. The headline leaves out the fixture too, as its finished tickets do. A hidden project, the gate's `crew-gate` (16.2), is in no number, row, column, or line of Latest signals, in the improver's `list_signals` and `get_metrics`, in the inbox, in a personal token's projects, or in the tickets around two versions (10.5), since 0.52: it runs a ticket for every release, which measures the gate, not the work. The headline counts an archived project's interventions with its finished tickets, so its count can be above the rows' when an archived project had some. Latest signals leaves out the measurements: a sandbox's start-up, which Analytics shows as an average, and a ticket's tokens and time, which the Use section shows. Each of its lines says what its signal holds: its own text, or else what its detail says, such as the choice of an analysis and whose ticket it was, the rewrite sent, the role and the family of a rule for checks not met, and an open choice's question and what the agent chose. A fix round says what sent the work back and the first line of what was found that is not a heading and holds a letter or a digit, such as the command that failed, cut to one plain line of at most 200 characters. A line of Latest signals shows at most 200 characters of What, with the whole text on hover; the API and the improver's `get_metrics` keep the whole text.

### 10.8 Dreaming: scoped context and skill evolution (M26, since 1.47)

**Dreaming** (`Memory API`, `5.1`) focuses autonomous self-improvement strictly on **harness context and skills**, leaving pipeline gates, model selection rules, and security boundaries untouched (`R216`). Every dreaming pass runs as a trackable `dream` task in `Tasks API` (`R217`), reading the Trajectory Store (`8.7`) and Dreamstore (`9.10`) over idle or scheduled intervals to propose three updates and optionally promote sanitized winners to `Knowledge API` (`9.11`, `R218`):

1. **Harness context micro-adjustments:** Small, evidence-backed edits to `AGENTS.md`, `WORKFLOW.md`, or peer agent definitions tied directly to trajectory failures or high-frequency `@researcher` findings.
2. **Skill creation & cross-project sharing:** Distilling high-validity `procedural` Dreamstore entries and repeated trajectory tool sequences into new `.agents/skills/<name>/SKILL.md` files, and promoting secret-scrubbed generic skills into the cross-project `Knowledge API` registry (`9.11`).
3. **Vetted external skill download:** Fetching missing skills requested via `request_skill` from a configured skills repository, statically scanning every file for prompt injection or exfiltration patterns, pinning the commit SHA and licence in `.agents/skills/VENDORED.md` (`7.1`), and creating a reviewable `dream`/`chore` ticket that pings `@user` for approval before activation.
