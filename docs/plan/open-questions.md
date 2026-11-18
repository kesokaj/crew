# Open questions

> Made from sections 25 and 28 of `agent-kanban-tdd.md`, version 2.01, by `tools/plan-copies.py`.

## Yours to decide

None is open now.

## Smaller questions, each with a default in the design

The number is the number of the parked question in section 25 of the design.

| Question | What is asked |
| --- | --- |
| Q4 | Should a skipped task stop the project from being Done? I let the project finish and list skipped tasks in the report. |
| Q5 | The task summary is written by a separate step, not by the agents that did the work. Is that what you meant? |
| Q8 | Is there anything on your local network that agents need, such as a private package server? |
| Q11 | Review and Test are on by default, and E2E and Verification only when you ask. Is that the right split, or should all four be off until asked for? |
| Q14 | Should evals also run before your own edits to prompts are applied? |
| Q17 | May the improver read full trajectories from every project, or only summaries and numbers? |
| Q18 | Should a new baseline version reach every project at once (my default), or one project first? |
| Q19 | When a project is deleted, should what Crew collected from it be deleted too? I delete it and keep the improvements already made. |
| Q21 | Documentation lives in the project's repository on `main` and is shown in the app. Would you rather keep it apart from the code, in the database? |
| Q22 | Should every project with a GitHub remote get a pull request per ticket, or only when you switch it on? |
| Q23 | Should a task be allowed to reach Done with every optional stage off, on its command checks alone? |
| Q26 | Is Model only the right meaning of "internet off", given that the agent's model is on the internet? |
| Q42 | For a local project, Crew serves the repository to you over HTTPS behind the sign-in. Do you also want SSH? |

## Open by design

The reviews named these, and nothing was added for them. The rows come from the review tables of section 28, in their order there.

| # | What | State | Where, and what remains |
| --- | --- | --- | --- |
| B15 | A day's backup and each update snapshot hold the CLI's files and the work folders | Open | 16.9. They could be left out, and a conversation rebuilt after a restore. Since 0.76 the work folders in them are sealed with the key file, and since 0.78 the CLI's files too, in backups made from then on (plan M20 D41, plan of 2026-10-26). |
| J9 | Crew cannot measure whether one model plans, analyses, or judges better than another | Open | 12.3. The lead agents, the assistant, the summary writer, the judge, and the prompt writer follow their list. |
| L5 | One in ten, 20 runs, and 90 days together mean that tries alone rarely change the builder | Open | 12.3 says so. Runs that moved at a limit and comparisons are what give numbers. `explore_share` and `window_days` are the settings to raise. |
| G5 | Whose subscriptions sign in on a machine that is not yours | Yours to judge, for each machine | 12.6. It changes nothing in the build. |

## Closed in part

A rule was added for each of these, and something remains. The last column says what.

| # | What | State | Where, and what remains |
| --- | --- | --- | --- |
| 1 | A builder could change its own checks or tests | Partly | Checks are read from the database, and the reviewer sees changed tests (7.1). With Review off, only a deleted test is caught. |
| 10 | An agent could be instructed through files in the workspace, or through memory | Partly | 13.8. Which files `agy` loads was answered by the spike, item 28, on `agy` 1.3.4, in M0, M5 and M6, and `crew-run` takes them out of the workspace of a run that does not build; a run that builds loads what its branch holds, which never merges by itself (13.8). It is tried again at the next raise of `agy` (section 24, item 28; since 1.21). |
| 11 | What a leaked login reaches, and logins left in run homes | Partly | Removed after a run (12.6). Since 0.25 a sandbox holds the login of its CLI and a GitHub token that can only read for one hour; gcloud until 0.79. What that costs, and what limits it, is in 12.6. |
| 14 | Checks that fail only sometimes, and a busy merge queue | Partly | One more try, recorded as flaky (7.10). A queue that is slow because many tickets finish at once is not addressed. |
| 20 | Looking after the machine | Partly | 16.10. Logs of the machine itself, and patching of Docker and Kata outside a Crew release, are not addressed. |
| 22 | Packages that agents install | Partly | Since 0.90 Crew lists the package files a branch changes to the reviewer, and such a branch is reviewed before it merges (13.8, parked question 72). A package installed outside a manifest, and a project with Review off, are not covered. |
| 25 | Small things | Partly | The time zone is set (16.10). A search across projects is not designed. |
| A2 | The code under test ran beside the token that wrote the verdict | Closed for tests, Partly for reviews | 11.3, 13.9. A reviewer or a verifier that runs the branch's code still does so beside its token. |
| J2 | A builder's numbers rest partly on other models' verdicts | Closed in part | 12.3: the passed share comes from Crew's own checks, and the card names the family that did the checks. The fix rounds still come from models. |

## Settled only by the spike

See `spike-checklist.md`. The second table there says what Crew does if an answer is no.
