## 2. How Crew works

```
 OPERATOR                               AGENTS
 --------                               ------
 1  develops a plan  <----------------> planning agent asks, researches, drafts
        |
 2  creates the project                 Crew creates the repository
        |                               (GitHub over SSH, or local)
 3  the plan is added to the project as the original plan
        |
        |                            4  lead analyses the plan and splits it
        v                               into tasks: why, what, goal, checks
   [Planning]  the tasks wait here for the operator
        |
 5  says OK  ------------------------>  pipeline runs until done
        v
   [Backlog] --> [Analysis] --> [In progress] --> optional stages --> [Done]
        ^                                          [Review] [Test]
        |                                          [E2E] [Verification]
        |
 8  any agent creates a task through the Crew MCP -> it lands in Backlog
 9  in Analysis another agent sets why, what, goal, then approves it,
    skips it for now, discards it, or gives notice of a new feature

 6  every task is monitored, every prompt and trajectory is saved,
    and each finished task gets a summary in Crew's database
 7  no task leaves Analysis without a why, a what, and a goal
```

Ten rules hold everywhere:

1. **One OK, then it runs.** Nothing is built before the operator's OK. After it, the pipeline runs until every ticket is Done, cancelled, skipped, or discarded.
2. **Why, what, goal.** A task without all three, and a check, cannot leave Analysis.
3. **Checks decide.** An agent requests a move. The backend runs the checks and applies it.
4. **Another agent decides.** A task an agent creates is analysed by a different agent.
5. **The board implies the work.** The orchestrator derives what should run from the board. It keeps no private plan, so a restart loses nothing.
6. **Signals go up, the baseline comes down.** Projects share nothing with each other. Crew sees all of them and improves what they all run on (section 10).
7. **Nothing is hidden.** The prompt an agent was given, its model and effort, and the work it did are saved and shown for every subtask of every task.
8. **A ticket only moves to the right.** It never goes back a stage. Work that fails is fixed where it stands. A ticket that cannot get through is cancelled and replaced by other tickets, which are tagged to it.
9. **Crew acts as you.** Crew is an extension of the developer who runs it. Its agents work with your own login of `agy` and your git identity. What that costs is said in 12.6, and you accepted it. Since 0.79 no run and no conversation gets gcloud (AGENTS.md safety rule 8, your choice of session 43).
10. **It runs in Docker, installs cleanly, and is open source and reproducible.** Docker is the only target: every part of Crew is a container, and a sandbox is a container that Docker starts with the Kata runtime (13.1). Crew's source repository is public under Apache-2.0 (`LICENSE`), built cleanly without secrets on any operator's machine or run from releases built in your own private registry (`16.12`). Every version it is built from is fixed, one command installs it on a fresh machine and another takes it away again, and the repository holds no secret (16.11, 16.12).
