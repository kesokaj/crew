## 21. Failure handling

| Failure | Response |
| --- | --- |
| Backend restart | Read the database, adopt the running sandboxes from their labels, read on in each run's spool from the last stored event, then reconcile. |
| The database container stops | Docker starts it again. Meanwhile the server shows a page that says so and starts no run. Running sandboxes keep working, and their events wait in their spools. |
| The database cannot be written, or its disk is full | No new run starts, and the inbox gets an item when it can be written again. The disk guard acts before that (16.10). A run whose events cannot be stored is stopped (8.3). How it is built (since 0.65, M17): the server writes `crew_state`'s row to itself every 30 seconds; two writes in a row that fail are a time it could not write, from the first, kept in memory and in `<data>/backups/.db-unwritable`, the server's own folder, so a restart keeps it. The first write after it stores from and to (schema step 71), and the inbox says "The database can be written again", for how long and how long ago, with Read it. A server that starts while it cannot write and finds no file says the from is not known. Check mode writes no probe. |
| The key that encrypts secrets is lost | The credentials in the database cannot be read. The operator signs in again in Setup, and enters the secrets of hosted projects again. The work folders of conversations are lost: the next sandbox starts without its folder, and the Sandbox card says so (since 0.76). Everything else is unaffected. |
| GitHub refuses to create the repository | The project is not created. The error is shown, such as a name already taken or `gh` not signed in. A local repository can be chosen instead. |
| GitHub refuses the SSH key, or the `gh` login has run out | With the key refused, syncing stops. With `gh` out, no repository, pull request, or release page is created, and tickets are merged with a merge commit. Projects keep running on the machine. Setup shows the problem. |
| An agent tries to push to GitHub from its sandbox | It cannot. The token it holds can only read, and stops working after one hour (12.6). |
| GitHub gives no token for a sandbox | The run starts without one. Reading another private repository fails with a clear error, and the rest works. A signal is recorded, with a fixed sentence of the cause, and the error goes to the server's log (13.9). |
| A skill from outside carries instructions that send code or data away | The reviewer of the skill ticket reads the skill for exactly that, its source is pinned, and the provenance is recorded (12.9). |
| The remote cannot be reached, or rejects a push | Work continues on the machine. Pushes wait and are retried. Nothing is forced. |
| `main` on the remote has commits Crew does not have | They are fetched and become the new base. Tasks in work are brought up to date before they merge. |
| A run crashes, hangs, or loops | The watchdog stops it. It is retried once in a new sandbox. After a second stop the lead looks at the ticket again (7.7). The trajectory shows what happened. |
| A sandbox dies | The run fails. Pushed commits are in the quarantine repo. Unpushed work is lost. |
| A project's setup fails | The run fails before the agent starts. After two failures in a row Crew creates a `fix` ticket for the setup and sends news, once the setup failed on `main` too (since 0.47; otherwise the ticket's branch broke it, and the failures count as its stops). The project is not paused. Runs that need the setup wait for that ticket, because they would fail the same way. |
| The machine restarts | All sandboxes are gone. Runs are restarted from their pushed branches and, where kept, their conversation state. |
| A restart for security updates cannot be asked about | The server is down, the database cannot be written, or its news cannot be stored: the machine is not restarted, and `crew-ctl` asks again in 15 minutes. A restart that found no quiet hour in 7 days holds new runs until it comes (16.10, since 0.67). |
| The machine is full | Runs wait. The Projects screen shows what is waiting and why. |
| Work passes in the builder's sandbox only | It fails at Test, in the tester's fresh sandbox, and is fixed there. With Test off, it fails in the checks before merging. |
| Two tasks work alone but not together | The second one fails its checks on top of `main` before merging, and gets a fix subtask. |
| Agents create many tasks | Each is reviewed by another agent. Discards become feedback to the creating role. The scope guard pauses the project when the tickets agents added pass their share of the plan's tickets. |
| The analysing agent approves too much | Every decision and its reasoning is on the task. Set `approvals.created_task` to `operator` to decide yourself. |
| A task has no clear why, what, or goal | It cannot leave Analysis. The analysing agent completes it or discards it. |
| The end-to-end run cannot start the application | The E2E subtask fails with the output. After two failures in a row the lead looks at the ticket again (7.7), because the cause is usually the project's setup and not the ticket. Since 0.47 Crew's own E2E check decides, never the agent's word: the stage's `run` command ending with exit code 3 is "could not start"; any other failure is a failing test, a fix round, and starts the count again. A setup that fails in the check sandbox takes the way of a project's setup that fails (above). The builder's and the E2E tester's prompts say what exit code 3 means. |
| End-to-end tests pass or fail at random | A failed run is repeated once in a new sandbox before a fix is asked for. Tests that keep changing their result become a bug: the second run of the same command in 30 days that failed and then passed opens one bug, found by `system`, with its fix ticket; later ones are added to that bug while it is open (since 0.46). |
| Documentation falls behind the code | `docs_done` stops a task that names pages and does not change them. Pages whose code changed without them are marked, and the lead creates tasks for them. |
| The operator and an agent edit the same page | The operator's edit is a commit on `main`. The task's branch is brought up to date before it merges, and a conflict becomes a fix subtask. |
| A pull request is changed or merged on GitHub by someone else | The backend fetches the change. A merge made outside Crew becomes the new base, and the backend marks the ticket Done with a note. It then runs the ticket's checks on `main` and creates a fix ticket if they fail (7.12). How it is built (since 0.47): every `git.fetch_minutes`, with `gh` signed in, the server reads each open pull request of the project with its own token. Merged: the ticket's runs stop, all but its summary's, it is Done with the note and news, and the remote's `main` takes the path of 7.12; once `main` has the merge, the ticket's command checks and the project's tests run on it, and a fix ticket follows if they fail (schema step 49). Closed without a merge: a note, and nothing moves. Its branch moved on GitHub: a fast-forward becomes the ticket's new head, not taken as checked, so its stage's checks run again and a head that changes code waits for your Allow (13.8); a branch that went another way is not taken, and the inbox says so, with Keep Crew's. |
| A prompt can no longer be rebuilt because an old baseline version was removed | Baseline versions are tags and are never deleted. The full text is also in the database. |
| The breakdown is poor | The operator sends it back with a comment before the OK. After the OK, agents correct it by creating tasks. |
| A summary cannot be written | The facts are still stored from the records. The written sections are marked missing and retried. |
| The disk fills with trajectories | Crew removes no trajectory by itself. The Database screen shows how many months of room are left, and a warning appears before the disk is full. At 95% no new run starts. You can then add disk, delete projects or conversations, or set `keep_trajectories_days`. |
| The events of a run cannot be written | The run is stopped. No run goes on without a trajectory (8.3). |
| The CLI's own files of a conversation are no longer kept, do not open with the key file (since 0.78), a newer version of the CLI cannot read them, or the next turn runs on another CLI | The conversation is rebuilt from the turns, and the turn is marked as rebuilt (12.10). |
| A conversation with the operator is left open | Its sandbox is removed after `conversations.idle_minutes` (30) without a message. The conversation and its work folder stay. |
| An agent is tricked by something it read on the internet | It holds the login of the one CLI it runs and a GitHub token that can only read for one hour; no gcloud since 0.79. It cannot push to GitHub. Its work is reviewed and tested by other agents, and its trajectory records what it did. |
| A memory entry is wrong or harmful | The operator retires it. Its source run is on the entry. |
| An improvement makes the agents worse | Its effect is measured after it is applied. On a worse result Crew takes the improvement out again and sends news. |
| An improvement carries one project's content into the baseline | The generic check refuses it. The evidence stays in the database, which no sandbox can reach. |
| The improver is misled by text in a trajectory | It has no internet access and can only propose. A proposal must pass the generic check and the evals, the release gives notice, and Crew rolls back a worse result. |
| Crew changed an agent to a model that does worse | The watched runs show it. Crew goes back by itself and tells you. Until then the checks still decide every ticket, so worse work costs fix rounds, not a wrong merge (12.3). |
| A chat or a conversation gets no lead slot | It waits, first in the queue of its CLI, and the chat says so. You can raise the lead slots of that CLI (7.9). |
| The prompt writer is late, or fails | Your own text is sent, or stays as it is, and the screen says so (7.2). |
| The assistant in a chat is misled by text in a trajectory | It can write nothing: its token has no tool that writes. Only your buttons send something on, and what they send keeps its source (11.9). |
| Too few tasks to tell whether an improvement helped | The result is marked inconclusive, with its numbers. The change stays, and the operator can take it out. |
| A CLI reaches the limit of one group of models | A run on that group moves to a model of its bracket in another group that can run, if there is one (and for a reviewer, a verifier, or a `judge:` check, prefers another family when it has room and falls back to a model of the same bracket in the builder's family that has room when the separate family is at its limit, recording an inbox item and a `check_rule_not_met` signal); a pin, a project's setting and an agent you set are never moved. Otherwise it waits until the reset time. Runs on the other groups carry on. A run that was stopped continues its conversation, on a model of its bracket in another group that can run or when the limit resets or Crew's login check reads that group free (12.7). |
| A CLI no longer offers a model | A pin or a project's setting that names it is ignored, the machine's entry for that agent applies, which is Crew's choice or yours, and you get news. If every model on a role's default list is gone, runs of that role wait and the inbox gets an item (12.3). |
| No CLI in `agents.clis` offers a model of another family, or the separate family is at its limit | A check runs on a model of the same bracket in the builder's family that has room. The run record, the Models screen, the inbox, and a `check_rule_not_met` signal say that the rule could not be met and why (12.3, 12.7). |
| `agy models` fails, or prints something Crew cannot read. The CLI refuses a name on its list. | Crew keeps the last list and tries again at the next check. After two failures you get news. A refused name is passed over for 15 minutes, then tried again. |
| The login of a CLI stops working | The check before a run, or the check on a schedule, notices it. The runs on that CLI stop, keep their conversations, and wait. The inbox asks the operator to sign in again, and the waiting runs continue afterwards (12.6, 12.7). |
| A CLI returns nothing with exit code 0 | The driver reads the result's status and the refused actions, and records the run as `refused`, not as done. |
| A vendor changes the flags or the output of its CLI | The version of each CLI is pinned per release. A new version arrives only with a release that passed the gate. |
| The vendor's account is suspended, or its plan no longer allows this use | `agy` is signed out, and no agent runs until you sign in with an account that works (12.6). Everything Crew saved stays. |
| A microVM does not start | The run fails before the agent starts. Two failures in a row hold new runs and show the Kata and KVM check. It is in the inbox, because only the operator can repair the machine. How it is built (since 0.65, M17): a boot is counted at the one place every microVM starts, the orchestrator's start of a sandbox. Docker's start that fails and a guest that is not ready in 90 seconds are a microVM that did not boot; a guest that is ready is a boot, also when the firewall's proof fails after it. The network, a missing image, a cancelled start, and a project's setup that fails in a booted guest count nothing. Each failure adds one to `crew_state.boot_failures`, and each boot sets it to 0; at two the hold "MicroVMs do not start" holds new runs with the error and the machine check's line (schema step 71), after Stop all and Pause all and before the disk. The top bar says "MicroVMs do not start". While it holds, the monitor starts a probe every 5 minutes, a sandbox of its own kind (13.3) that asks no hold, except while the operator paused or stopped Crew or the disk is full; one that boots lifts it. Docker's error is cut to 400 bytes where the start makes it, so the run, the log and the inbox get the short form, and the hold keeps at most 600 with the machine check's line whole. The inbox's item has Run the check, a probe now, and Resume, which sets the count to 0 and has its row in the history, and since 0.66 asks for a fresh sign-in. Check mode counts nothing. |
| A release cannot start, cannot read the stored state, or cannot start a microVM | The updater finds it in its check, beside the running version. Nothing was replaced, and the inbox says what failed (16.4). |
| A new version turns unhealthy after the switch | Within ten minutes the updater switches back by itself. Later, the operator rolls back from the app or with `crew-ctl rollback`. |
| A release changes the schema in a way the previous version cannot read | Such a change is made in two releases (16.6). Every release that changes the schema waits for Install (16.3). A roll back across a change the older version cannot read restores the snapshot of the database. |
| The server is broken and the app cannot be opened | The updater is its own container and does not need the server. `crew-ctl rollback` on the machine asks it directly. |
| The operator and an agent would change the same branch | In diagnostics mode the project terminal cannot push. In edit mode a push to a ticket's branch is refused while an agent is running on that ticket. |
| The machine's disk dies | The code and the tags are on GitHub. With `gh` signed in, so are the release pages, and the images are in its registry. The database and its backups are on the same disk, so tickets, records, conversations, and trajectories are lost unless the backups are copied off the machine (`updates.backup_copy_to`, 16.9). `crew-ctl install` brings Crew back on a new disk at a chosen version. What was only on that host is lost: local-only projects, hosted data, and Crew's own state. The operator signs in again (16.9). |
| A project terminal is forgotten | It is removed after 30 minutes without input, and its room on the machine is freed. |
| A notification does not arrive | Crew tries again and marks the channel on the inbox. The item is in the inbox either way. |
| The mail login is refused, for example after the Google account's password changed | Mail stops, and the inbox says so. Until it is fixed you see new items only in the app. The operator enters a new app password in Setup. Since 0.71 what came meanwhile is held, and goes then; news older than a day is left to Activity. |
| Two parallel subtasks changed the same code | The second merge into the ticket's branch conflicts, or the checks fail on the result. One builder subtask joins the work, with both sides. Nothing reaches `main` before that. |
| A personal MCP token leaks | Without "may answer" it can read, create tickets, and comment. With it, also new features and recorded decisions. Never the project OK, releases, deploys, settings, or secrets. The operator revokes it in Crew, and every call it made is listed. |
| Joined subtasks still fail | The builder subtask that joins them gets one more try. Then the lead looks at the ticket again (7.7). |
| The lead's own look at a ticket stops | It is retried once. If the agent's own turn stops again, the ticket is cancelled with that reason, and the final report lists it; an end of `crew-run`'s own step before the CLI ran never counts as a stop of the lead's look (7.7; since 1.26, CREW-21, register row 72). |
| The ticket that makes a project hostable is cancelled | Its replacements make it hostable. Until one is Done, hosting does not start, and the Hosting screen says why. |
| A release changes code | It does not install by itself. It is in the inbox with what it changes, and waits for Install. |
| The install stops half way on a fresh machine | Every step looks first, so `crew-ctl install` is run again and goes on from where it stopped. `doctor` sees that it is a repair, and says what is wrong. |
| Uninstall leaves something behind | The install test fails, because the machine is not as it was found (16.11). |
| A release that installs by itself fails its check | Nothing was replaced. The release is put on hold, and the operator is told what failed. |
| A new feature went ahead that you did not want | It is a ticket like any other. Discard it, or revert it if it is merged (6.7). Set `approvals.new_feature` to `operator` for that project. |
| An agent decided an open choice the wrong way | It is in the Decisions list with its alternatives and its reason. Change it there. Crew creates a ticket that carries the change out, and later runs read the new decision. |
| A ticket takes far more runs than it should | A run has its own limits, its turns and its minutes, the fix rounds are limited, and the lead cancels and replaces a ticket that cannot get through. A replacement is not replaced again. No limit is in dollars (since 0.53). |
| The lead cancels a ticket you wanted | It is Closed, as cancelled, with the lead's reason and the tickets that replace it, and in the final report. Add a ticket of your own that says what should change. |
| An agent keeps adding subtasks to its ticket | `create_subtask` is refused after `max_added_subtasks`, and every subtask runs under a run's own limits. |
| A notification is pressed by accident, or a link is opened by a scanner or twice | A press only chooses. Nothing is decided until the operator confirms in Crew, signed in. A used link shows what was decided. |
| A deploy of a hosted project fails its build or its check | The address keeps serving the previous version. The operator is told what failed. |
| A hosted project stops answering | Crew restarts it. If it still does not answer, the operator is notified, and can roll back to a kept deploy. |
| A hosted project uses too much | It has a fixed size, as every sandbox. It cannot take room from Crew itself. |
| Agent runs queue up | The Projects screen shows the queue. Raise `max_concurrent_runs` if the wait is too long and the subscriptions have room. |
| A setting is changed to an invalid value | The change is refused and the old value stays. |

Added in 0.20:

| Failure | What Crew does |
| --- | --- |
| The disk is almost full | The disk guard removes what retention allows. At 95% no new run starts, and the inbox gets an item (16.10). |
| A check fails only sometimes | It is run once more on the merge candidate, and recorded as flaky (7.10). |
| A builder weakens its own test | The reviewer is shown every changed test file. With Review off, a deleted test file that a check names stops the move (7.1). |
| A builder writes instructions for its reviewer into the repository | The push is refused unless the ticket names the file, and the judging run takes such files from `main` (13.8). |
| A deploy damages the data of a hosted project | The copy made before that deploy can be put back with Roll back (19.3). |
| No CLI works, so Crew cannot build Crew | The operator builds and installs a release by hand with `crew-ctl` (16.10). |
| Crew itself is down | Docker starts the services again. Nothing tells the operator, by the operator's own choice. |

Added in 0.58, for Crew building Crew (16.2):

| Failure | What Crew does |
| --- | --- |
| The build of a release fails | Its folder is removed, and its row says why; the running release is untouched. A build whose `agy` tarball is not the commit's pin fails before its sandbox starts (12.8). |
| Two builds at once | The second is refused: one at a time. A build waits for an updater that installs, and stops before a second improvement's merge (16.2 step 3). |
| A candidate's own `release.sh`, or its own Compose file | Neither is run: the build runs the running release's `release.sh`, and the updater writes the check server's spec itself, with no Docker socket, whatever the candidate's `compose.yaml` names (16.2 step 4, 16.4 step 3). |
| The candidate's runtime inputs changed | No live step, and so no login, reaches its image before the operator presses Run the live gate, which names the diff by its hash (16.2 step 5). |
| The gate's real ticket fails, its CLI is signed out, or it waits at a limit | The step fails, or is skipped with why: signed out, or past six hours at a limit, when the ticket is cancelled; the release waits for Install (16.2 step 5). |
| A release's eval suite measures worse | The release is held until Install, and Versions has the numbers (16.2 step 5). |
| The server restarts during a build or a gate | The build is failed and its files removed; a gate is taken up where it stood, and its eval suites with it (16.2). |
| The daily look gets a bad answer | That source gives nothing, with a line in Crew's words; the others go on, and the look is bounded in time (12.8). |
| Crew's project's `main` lags its remote | The look makes no ticket of what the remote's main pins already, while it follows `main` and the operator has not refused it (since 0.62), and says what it read; every value comes from `main` (12.8, since 0.58). |
| An eval case does not fit | It waits for its fit check, and a case whose held-out checks fail is kept for the operator (9.6). |
| A release measures worse than the one it replaced | One line of news; never a roll back (16.8). |

Added in 0.59, from the review of the whole of M16:

| Failure | What Crew does |
| --- | --- |
| A gate's row fails while its `gate.json` waits for the live result | The updater writes the live part failed, with the server's reason, so the release is not held pending; `crew-ctl gate` can gate it again. Ending it needs the updater; adopting another release's gate does not wait for it (16.2 step 5). |
| A candidate's runtime inputs give no diff to press on | A path that is never the same runtime is in the diff; with none, the gate says why (the running release unread, no inputs recorded, a recorded hash that is not its inputs'), and the press is refused for a candidate whose hash is not its inputs' (16.2 steps 5 and 6). |

Added in 0.61:

| Failure | What Crew does |
| --- | --- |
| Go's proxy redirects a module out of its bucket, sends more than 512 MiB, answers another error, or cannot be reached | The module proxy answers the sandbox 502, or cuts the file short, and logs the sandbox, the path, and why; Go's command fails at that module, and so does the build or the check. Nothing falls back to another source (13.6). |
