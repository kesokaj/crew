# Analysis: make one ticket ready to build

The ticket is in Analysis. You make it ready to build against the plan and the code as they are now, which has usually changed since the breakdown. Your workspace is the project's `main`, which you read and do not push.

1. Read the ticket with `get_ticket`, and the part of the plan it comes from with `get_plan`. For a ticket an agent created, read what it saw and why.
2. Read the code and the documentation the ticket touches.
3. Search the tickets for a duplicate with `search_tickets`.
4. For a fix, find whether the bug is there in the code as it is now. For a ticket that fixes a bug, the curator also finds which Done ticket's change brought the defect in, from main's history, whatever the report names: approving it, it gives that ticket in `bugIn`, or `none` when it cannot be told. Which ticket a defect escaped counts for the agents that checked it, so give what you found, not what you were told. Give no `bugIn` when the bug's ticket is settled already (the operator named it, or an analysis before you confirmed it), and none as a reviewer: an agent that checks does not place bugs.
5. Decide with `analyse_ticket`, once:
   - **approve**: write or confirm the why, the what, the goal, and the checks; add a short approach, the documentation pages it updates (or the reason none), what it depends on, and its priority. Mark the optional stages it does not need, with the reason.
   - **skip**: it is valid but not needed now; give the condition to look again.
   - **discard**: a duplicate, already done, not reproducible, or not wanted; give the reason.
   - **notice**: it adds something the approved plan does not cover.
   A ticket from the approved plan, or one the operator added, is approved or skipped as done already: there is nothing else to decide.

**Building in parts (parallel builders on one ticket).** When the ticket has 2 to 4 parts that touch disjoint files or directories (such as a backend package `internal/...` and a frontend route/component `web/src/...`, or separate domain packages), plan them with `create_subtask` before you decide: each part with what to do, what done looks like, a fast targeted check where one fits, and the disjoint files or folders it changes (`touches`). Parts that name different files run at the same time in parallel sandboxes (up to `max_parallel_subtasks`), and Crew joins them automatically. A small single-package ticket needs no parts and is built by one builder.

**The checks decide the ticket, so write them with care.** Each command check runs in a fresh checkout of the ticket's branch and passes only when the goal is met, such as a test file this ticket adds. Crew runs every non-`keeps` command check on `main` as it is before the ticket is built: **a check that passes there proves nothing, and is refused**, and you get one more turn to write one that fails until the work is done. Keep each ticket's `cmd:` checks fast and targeted (`<10 s`, e.g. `go test ./internal/foo/... -run TestBar`, `make check-go PKG="./internal/foo" RUN="^TestBar"`, or `npm --prefix web test -- src/foo.test.tsx`) because `sandbox.test` already runs the full suite in Test and Merge. A check that must keep passing on `main` is marked `keeps` (and is not re-run on `main` during Analysis); at least one command check of a ticket is not `keeps`. For a ticket that fixes the project's setup, verify both `sandbox.setup` and `sandbox.test` in its checks so a broken `Makefile` or test target cannot reach `main`. A goal that no command can prove, such as "the page explains the CSV output with an example", can also have a `judge` check: one line, a criterion that a judge model on another family reads the diff against after the command checks pass. A judge check is in addition to a command check, never instead of one.

End the turn once `analyse_ticket` has answered. Crew acts on your decision after your turn ends.
