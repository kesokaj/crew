## 6. Data model: the database, and git

**The database is the brain of Crew (R187).** Everything Crew saves is in it. You decided this in 0.25, and it replaces the earlier rule that records are files in git.

| Where | What | Why |
| --- | --- | --- |
| Crew's database (15.3) | Everything Crew saves: projects, plans, tickets, subtasks, records, summaries, decisions, knowledge, settings, conversations, trajectories, prompts, signals, improvements, releases, the queue, credentials, and the history of every change | One place to look, one thing to back up, and all the server needs to know after a restart |
| Git, one repository per project | What is source: the code and the documentation on `main`, the work on ticket branches, and the project's own skills and context | It is what you clone, read, review, and push |
| Git, Crew's source repository | Crew's code, the harness, and the baseline (10.3) | Crew is built like any project |
| Files on the machine | Docker images, the data of hosted projects, the backup files, and the key that encrypts secrets | They are large, or they belong to another program, or they must stay outside the database to protect it |

Four rules follow.

- **If Crew saves it, it is a row.** Nothing that must survive is only in a file or only in the server's memory.
- **The server keeps no state of its own.** After a restart it reads the database, finds the running sandboxes by their Docker labels, and goes on.
- **Agents never reach the database.** No sandbox has a route to it. An agent asks the Crew MCP, which answers only for its own project (R25).
- **Git and the database point at each other.** A commit names its ticket and its run. A ticket names its branch and its commits.

The full list of what is where is in 6.5. The tables are in 15.3.

### 6.1 Repositories and branches

Each project has two bare repositories on the machine, and optionally a remote. They hold the code, the documentation, and the project's own skills and context, and nothing else.

- **Canonical repo:** the source of truth for the code. No sandbox can reach it, the operator's project terminal neither: since 0.40 that terminal clones the quarantine repo through the git endpoint like every sandbox (13.11).
- **Quarantine repo:** the copy that sandboxes clone from and push to, over HTTP through the backend. It is never mounted into a sandbox.

On the machine both have the project's name: the canonical one is `<data>/canonical/<project>.git`, and the quarantine one `<data>/repos/<project>.git`, where the git endpoint serves it. The backend writes `main` to the canonical repository first and then to the quarantine one. It cuts a task's branch from `main` in the canonical repository; the builder pushes its work to the branch of that name in the quarantine repository, and the checks of the stages read it there.
- **Remote (optional):** on GitHub or any git host. Only the backend talks to it.

Each run gets a git token that allows:

| Run | May read | May push |
| --- | --- | --- |
| Builder | `main`, its own task branch | Its own task branch only |
| Builder on a parallel subtask | `main`, the task branch, its own subtask branch | Its own subtask branch only |
| Reviewer, tester, E2E tester, verifier | `main`, the task branch under review | Nothing |
| Lead | `main`, and a task's branch when it analyses a task that has one | Nothing |
| The assistant in a ticket's chat | `main`, that ticket's branch, and the branches of its subtasks | Nothing |
| The assistant in a conversation about a project | `main`, and the branches of that project | Nothing |

| Branch | Content | Who writes |
| --- | --- | --- |
| `main` | Project code and documentation | Backend only, when a task is merged |
| `task/<ticket>-<slug>` | Work for one task, code and documentation | The task's builder. The backend, when it joins parallel subtasks. |
| `sub/<ticket>.<n>` | Work of one parallel subtask, cut from the task branch | That subtask's builder. The backend merges it into the task branch and removes it. |
| `eval/<run>` | A scratch branch for one eval case (9.6). Never merged, and removed after the run. | The agents of that eval run |
| `subtask/<n>` | The work of a subtask with no ticket, such as a run of M3 on the fixture project | That subtask's runs |
| `refs/crew/wip/<run>` | The work in progress of one run, kept by `crew-run` after every turn that left anything (11.2). Not a branch. | `crew-run`, for its own run only |

There is no `board` branch any more. What it held is in the database.

A tree a push brings is bounded (since 1.03; the security review of 1.02, round two, finding 1, and the operator's popup answers of session 70, "200k paths and 512 MiB", "At push and in Archive" and "Eval branch's, rest error", and after the change's review "Bound names, count trees", "Every new commit", "Name them, carry" and "Protect listtree.go", and after its second round "Distinct trees + timeout" and "Move Archive"): at most 200,000 paths, its trees counted, at most 512 MiB of files, what a check sandbox takes, no path longer than 4,096 bytes, as git quotes it for the hook, and at most 64 MiB of the listing's text. A pack of a few hundred bytes, of trees that point at each other, of one file named many times, of an empty tree named many times, or of long names, lists millions of paths or gigabytes: on dave a pack of 668 bytes listed 16.8 million paths, 1.09 GB that the server read whole, and `git diff` of it took 5.2 GiB. The git endpoint's hook bounds the tree of every commit a push brings, which is at most 1,000, and of each ref's tip, before its path rules (13.8) and before anything lists them whole: it lists each distinct tree of the push once, each listing is cut at the bound of its text and stops at the first path past the others, and the whole check runs within two minutes. The pushing run reads "crew: a tree of this push holds more than 200000 paths, 536870912 bytes of files or a path of 4096 bytes, more than Crew checks", "crew: the push of <ref> brings more than 1000 commits", or "crew: the trees of this push could not be checked in 120 seconds". A ref set to an object that is not a commit, or a listing git cannot give, is refused; git's own check of the objects refuses a name longer than its limit before the hook runs. So no commit that a run pushed holds such a tree, on a branch, a subtask branch, an eval branch or a run's work in progress, and every later diff, merge and copy of it is bounded too. The archive a check sandbox gets is listed the same way, for every caller, reading each path through a buffer of the bound's size, since a remote's `main` and a tree stored before 1.03 did not come through the hook: past the bound it is an error with a fixed text, "the tree holds more than 200000 paths, 536870912 bytes of files or a path of 4096 bytes, more than a check sandbox takes". For an eval's case it is the branch's tree too large (9.6); for a ticket's check, the check of the setup on `main` and a remote's `main` it is a check error, as a tree past 512 MB was before (13.9). The hook's bound is in `internal/gitendpoint/` and the archive with its bound in `internal/gitstore/listtree.go`, both protected (16.8). The test of the files a push changes (13.8), which the server's own program runs as the hook's next step, runs within two minutes of its own, and is killed five seconds after them, since the program takes the signal to stop as a request and its own work watches no time (since 1.07; the gap 1.06 carried, and the operator's popup answers of session 74, "timeout -k 5, same 2 min" and "No: KILL + bounds"): the pushing run reads "crew: the test of the files of this push did not end within 120 seconds, or was stopped", also when the kernel stopped it, as for its memory. A push that is refused can be sent again at once: each costs the hook at most its two minutes for its trees and two for its files. The endpoint works on at most two pushes of a repository at once and four of the server, and on at most four fetches of a repository and eight of the server, counted apart, so a fetch never holds up a push; a repository is a project's one, which the schema holds (`git_repos_one_per_project`, step 81), so a project takes at most half the server's slots (since 1.08; the security review of 1.07, round one, finding 2, carried, and the operator's popup answers of session 75, "2 per repo, 4 per server", "Wait 5 min, then 503", "mem_limit 8g, pushes killed first" and "Own counts: 4/repo, 8/server", after them "4g ceiling, reserve 8 GB" and "Spool every push first", and after the change's review and security review "Score 500 for fetches too", "10 min per request", "2× slots per repo, then 503", "Push's after-work", "Count ref advertisements", "memswap_limit: 4g" and "One repo per project, pinned", and after their second round "Refuse GET with body + deadline on all", "Per-read timeouts + give main back", "UNIQUE(project), schema step 81", "'after' row asserts the log" and "Fetch test write time 3 s"). Every request takes its slots, the advertisement of refs before a push or a fetch too, before its body is read, and an advertisement that says it has a body is refused at once with HTTP 400, "a request for refs takes no body", since git would wait for that body; and holds them until git has ended, its hook with it; one that finds none waits for them at most five minutes, with at most twice a repository's slots waiting beside it, and else, or past the wait, is refused with HTTP 503 and "crew: the git endpoint works on as many pushes as it takes at once; push again in a few minutes", or the same of fetches, which git shows the run when its advertisement is refused and as "RPC failed; HTTP 503" when its request is, and the server logs. Once it has its slots, what it sends arrives within two minutes, or it is refused, "the request's body did not arrive whole in time", and its answer is written within ten minutes, or git is stopped: a client that sends or reads slowly holds no slot. A push's body is read whole into a file first, within its bound of 512 MiB, since git http-backend spins on a core without end on a push's body that ends before its length (measured in session 75, and older than 1.08), and a fetch's git ends when its body does. A push's slots go back when git has ended, before the server records which refs it changed (6.7), and the refs are read before and after it whatever becomes of the client's connection, each read within a minute of its own; when either read fails the push is recorded for no ref, never for every ref as new, and a request that may push `main`, the operator's terminal in edit mode, has `main` heard of with no value: what it brought to `main` is not taken, Crew's `main` is given back to the git endpoint, and the project's history says "the refs of its push could not be read" (13.11). So what the requests at once hold of the server is bounded: at most four pushes and eight fetches, each with its body on the disk and git's and the hook's memory. The server, with everything it starts, has a ceiling of 4 GB of memory and no swap beyond it (`mem_limit` and `memswap_limit` in the Compose file), which the machine's reserve holds (13.10); `git http-backend` and the child gits, hooks, and server program a hook starts run with a score of 500 for the kernel's choice of what to stop when the ceiling is reached (`internal/gitendpoint/oomscore.go`), so they are stopped before the server, whose score stays 0, and a push stopped in its hook reads "… or was stopped", while the git endpoint's own short git reads (`rev-parse`, `for-each-ref`, `update-ref`, `symbolic-ref`, `cat-file`) and the import fetch do not set an OOM score (13.10, clarified in 1.26, register row 33). Every git command started by `internal/gitstore/` and `internal/gitendpoint/` runs with a clean environment (`gitstore.CleanEnv`, `gitendpoint.CleanEnv`) that strips `GIT_*` variables from the caller's environment (since 1.26, register row 12). A process may lower its own score to 0 again without a privilege; nothing the endpoint starts does, and none of it is a program a run brought. A request whose score cannot be set is not taken. The ceiling holds once an install has started the server with it. Every other read of git's output by the server is bounded too (since 1.04; the security review of the tree's bound, round one, finding 3, carried by the popup answer "Name them, carry", and the operator's popup answers of session 71, "One bounded reader", "64 MiB", "An error, guards closed" and "Protect it, move output in", and after the change's review and security review "Bound them, refuse", "Every error an error", "Cap, move, pin by test" and "Carry, name it"). The hook bounds trees, not the rest: a commit's message is no part of a tree, and on dave a pack of 285 KB carried a message of 300 MB that `git fsck --strict` accepted and `git log` printed whole (since 1.09 the hook bounds each commit, and a history cuts its fields, below); a branch may hold one file of 512 MiB; and a remote's `main` and a tree stored before 1.03 did not come through the hook. One reader takes every such read: the listing of `main`'s files (`docs.go`), the diff and the listing of a remote's `main` (12.8), the files a commit changed and their counts (`read.go`), the histories and a commit's message (since 1.09 streamed, each field kept to its cut, below), the files of `main`, of a branch and of a commit, and the list of conflicts of a merge or a revert (`merge.go`). It reads at most 64 MiB, the bound of a tree's listing, within which every tree the hook passes lists, though a diff of two trees near the bound can list more; past it, git is stopped and the read is an error with a fixed text, "git's answer is longer than 67108864 bytes, more than Crew reads", never a list cut short and never "no such commit". Of what git says on its error side, the first 64 KiB are kept, for every command of the server's git; the header a project's repository is brought in with is taken out of an error also where that cut leaves only its start, and so is the header of the git endpoint's fetch for an administrator's run (since 1.05). Each reader takes the error as it takes a failure of git: a page answers with its error, an agent's tool says that Crew could not do this now, and a merge, a judge, a review and a remote's `main` are tried again on the next pass. A file of a commit is read only after its listing names that path itself: a path the commit does not have is no file, and every failure of git, the bound among them, is an error, never a file that is not there. So an index file of the agent CLI on `main` (13.8) that cannot be read, past the bound or by any other failure, is never read as one that names nothing: it is an error for the merge's check, a pull request's push and a branch that moved on GitHub, and since 1.05 it holds a remote's `main` and the project terminal's for the operator with every file they change counted as code, so a `main` that repairs it can be taken (13.11; in 1.04 these two were an error each pass and not taken, which no `main` could end; the operator's popup answers of session 72 after the security review, "Hold for the operator" and "Hold on every error"). The file of `main` that Crew writes over in a merge (9.9) is an error, never written from nothing. A project's own stage's agent is an error, not a file `main` does not have; Crew's project's `versions.lock`, and the remote's, are a line of the look that says it could not be read and makes no ticket, and a release's baseline file one that skips its eval suites with the same words (16.2). The git endpoint's hook reads what a push changes within 64 MiB and each index file of the push within 1 MiB, its size taken from the listing first: past either, or when git cannot read an index file, the push is refused, "crew: the files of this push could not be read: an index file of this push is longer than 1048576 bytes", "… the files this push changes list longer than 67108864 bytes", "… an index file of this push could not be read" or, since 1.05, "… an index file, of this push or of main, is reached through more links than Crew follows" and "… an index file, of this push or of main, names a folder through /proc or /dev", and since 1.06, when the tree of a side or of `main` is listed for its links (13.8), "… a tree of this push or of main lists longer than 67108864 bytes", and since 1.07, for an index file of either side or of `main` past the folders Crew reads (13.8), "… an index file, of this push or of main, names more than 1024 folders, or a folder longer than 4096 bytes", and for one that is not JSON Crew reads exactly (13.8) "… an index file, of this push or of main, is not JSON Crew reads: an object whose entries and inherits are lists of objects with a path, each key once and in its own case". Since 1.07 the server's guards read an index file within the hook's 1 MiB too, by its size in the tree before it is read, "an index file of the agent CLI is longer than 1048576 bytes, more than Crew reads", where they read one within the bound of every read. The server lists a tree for its links once, through the same bounded reader, within 64 MiB of listing, and keeps of each entry only what a way needs, a link's object and size and of every other entry its mode (since 1.06, after its review's second round). The reader, with every command of the server's git through it, its bundle and its cut read of a diff's patches among them, is in `internal/gitstore/readbound.go`, protected (16.8); the store's own bound only lowers it, and a test fails when another file of `internal/gitstore/` but the listing of a tree names a function that starts a program, or `os/exec`'s `Cmd` (a call into another package that starts git is not seen). The patches of a diff keep their own cut of 1 MiB in all (6.7), never more than the bound, and a patch cut short is marked cut. Since 1.05 (the security review of the read's bound, round two, findings 3 and 6, and the gap it named; the operator's popup answers of session 72, "Follow on main, refuse a pushed link", "Every error an error" and "Redact, one redact in gitendpoint"): a branch's file and its base's, which the check of `.agents/skills/VENDORED.md` reads (7.1), are read after their listing too, so every failure of git is an error and the check is run again, and only a branch with no base on `main`, which git says by its exit status 1 alone, has no base file, and since 1.06 each is read through the links on its way (13.8); an index file is read as the agent CLI opens it, through the links on its way (13.8); and the git endpoint's fetch for an administrator's run takes its header out of an error, with the store's fetch, by one redaction in `internal/gitendpoint/`. A commit is bounded at push and cut at read (since 1.09; the "Still not covered" of 1.04 to 1.08, carried by the popup answer "Carry, name it", and the operator's popup answers of session 76, "Both: hook + read cut", "1 MiB a commit", "Subject 1K, body 16K; LastChange 1M" and "Refusal, '…'+cut, 1000/ticket"). Each commit a push brings, and each ref's tip, is at most 1 MiB, its headers and its message, as `git cat-file --batch-check` gives its size without reading it, before the server's program or a history reads it (git's own `rev-parse` and `rev-list` of the hook have read it, in git's memory); past it the pushing run reads "crew: the commit <id> of <ref> is <n> bytes, more than 1048576 that Crew takes", and a size git cannot give, one fewer than asked, or a check that does not say it passed refuses the push as "crew: the commits of <ref> cannot be read". A ref's tip is a commit, never a tag of one: "crew: <ref> is not a commit Crew can read". Each of those commits is in UTF-8, with no `encoding` header or one that says UTF-8, since git turns another encoding into UTF-8 as it prints a history, up to three times as long as the hook measured: "crew: the commit <id> of <ref> is not in UTF-8". A history streams git's output and keeps each commit's subject to 1 KiB, its body to 16 KiB, its author, email, id, time and ref to 1 KiB each and its parents to 4 KiB, the last whole ones of them (`internal/gitstore/history.go`): a cut field ends in "…" and its commit says `cut`, so a page shows it cut with no change of its own, and a read of 500 commits keeps at most about 12 MiB, whatever a remote's `main` or a commit stored before 1.09 holds; what it keeps in all stays within the bound of a read, and what it reads of git's output within eight times it, 512 MiB, past which it is an error, never a list cut short; 500 commits a push brings, each at most 1 MiB in UTF-8, print within it. The message of the last change of a path, which a documentation page and a skill read for their ticket's trailer, is cut at 1 MiB, so every message a push brings is read whole there. A ticket's Git part keeps at most 1,000 commits over its pushes and the commits no push brought, the newest, as its pushes are the newest 500: a push read once it is full, or once a push's history was past the bound of a read, is read for its commits' ids alone (`CommitIDs`, at most 500 as a push's history), so one view makes at most one read that fails, none is shown as one no push brought, and the rest are counted as `omitted`, which the page shows as "and N older commits, not listed here". One view reads at most about 2 GB of git's output, the messages of up to 1,499 commits of its pushes, of the 500 it reads for the commits no push brought and of one push past the bound. No agent's tool shows a commit's message: the chat's `get_diff` reads the merge Crew made and leaves its message out, and a release's notes take a subject cut to 120 characters. A ticket's Git part caches its read by the ticket's branch, `main`'s head, the branch's head, and the ticket's pushes (`internal/httpapi/taskgit.go`, up to 64 entries, since 1.31, register row 43), so polling the page while neither ref nor push has moved reads only the two refs. Still not covered: register rows 39 to 42 (section 28). Until 1.08 the pushes the hook worked on at once were not bounded either, nor was the server's memory; until 1.09 a commit's message under the bound was read whole by each history that showed it, up to about three times 64 MiB of the server's memory for one read, and a ticket's Git part held every push it read.

### 6.2 Layout of the repository, and of an export

The code and the documentation are on `main`:

```
README.md                     # the overview page
CHANGELOG.md                  # written by the backend, one entry per merged ticket
AGENTS.md                     # the project's own context, read by every run (12.9)
.agents/
  skills/                     # the project's own skills
  rules/
.crew/
  board.yaml                  # the project's settings mirror
  plans/
    original.md               # the original plan and any amendments
  tickets/
    <KEY>.md                  # each ticket's board metadata and text
docs/
  architecture.md
  setup.md
  decisions/
    0003-sessions-on-the-server.md
  guides/
  reference/
src/ ...
```

Everything else is in the database. A project can be written out as a folder of files with `crew-ctl project export`, and read back with `crew-ctl project import`. The export is for reading, for moving a project to another machine (R44), and for keeping a copy. It is a copy: Crew never reads it while it runs. It has this layout:

```
board.yaml                    # the project's settings, with its stages (section 14)
project.md                    # name, key, goal, phase
plan/
  original.md                 # the plan the project was created from, never edited
  breakdown/
    01.md                     # how the plan was split into tasks, one file per version
  sent-back/
    01.md                     # the comments the operator sent the breakdown back with, before the OK
  amendments/
    01.md                     # later changes to the plan
stages/                       # this project's overrides of the baseline's stage prompts (10.3)
agents/                       # this project's overrides of the baseline's agent definitions
tasks/
  0042-lock-account/          # ticket SF-42
    task.md                   # why, what, goal, checks, stage, origin, analysis, links
    subtasks/
      01-analysis.md          # one file per subtask: agent, model, effort, state, steps, result
      02-build.md
      03-review.md
    notes/
      20261007T120501Z-builder.md
    reviews/
      01.md
    tests/
      01.md
    comments/
      0001.md
    assets/
      e2e-sign-in-locked.png
    runs/
      r_8f3a2c.md             # one record per agent run, with the parts of its prompt
    summary.md                # written when the task ends
memory/
  index.md
  entries/
    0007-run-tests-with-make.md
bugs/
  0012-lockout-counter-resets.md
feedback/
  0005.md
evals/
  cases/
    0003-lockout/
      case.md
  runs/
    e_20261007_a1b2.md
overrides/
  o_03.md                     # an override the lead proposed, with its eval result
decisions/
  d_0014.md                   # a choice an agent recorded with `decide` (7.5)
deploys/
  dp_0006.md                  # one deploy of the hosted project, with the tickets in it
repo.bundle                   # every branch and tag of the code, as one git bundle
```

The export travels as a tar stream: `crew admin project export` writes it in the server's container, which has the repositories, and `crew-ctl project export NAME DIR` turns it into the folder; `crew-ctl project import DIR [--name NAME] [--key KEY]` sends a folder back, and the project is made again with the same rows and the same refs, under its own or another name and key. A ticket's moves, with the checks that let each pass, are in its `task.md`. Everything an import reads is checked as an API request is: only the files of this layout are read, a path that is not a plain file is refused, a `depends_on` circle is refused, and a refused import leaves neither rows nor repositories. The board's own rules are checked too: a ticket's moves go from where it began to where it is, each to the stage the board would have chosen, with the ticket's skips and the stages on in the export, or to closed; a ticket past Analysis was approved; and its skips are ones Analysis could make. Since 0.53 a ticket file's `budget_usd` and `over_budget`, and a key of `board.yaml` that is no longer a setting, are left out on import, so an export written before still reads (14.1). What a file cannot prove, it does not decide: an imported project lands paused, and runs only when the operator resumes it, and every move that came with it is marked imported. The rows are written out from one snapshot of the database. The files a later milestone adds join the export with it.

Notes, comments, reviews, tests, and run records are one row per entry in the database, and one file per entry in an export. They are not changed after they are written, except to mark a comment resolved. Conversations, the chats of the tickets, trajectories, and credentials are not part of a project's export; a ticket made from a conversation keeps its origin, not the conversation.

The agent definitions and stage prompts themselves are Crew's baseline and live in Crew's source repository (10.3). A project holds only its overrides, and most projects have none.

### 6.3 Records, and how they look

A record is a row in the database. Its fields are columns, and its long texts are markdown. The examples below show each record as an export writes it and as an agent gets it from the Crew MCP: the fields first, then the text. The file names are those of the export (6.2).

`plan/original.md`, copied from the plan draft when the project is created:

```markdown
---
title: Storefront accounts and checkout
key: SF                     # the prefix of every ticket number in this project
created_from: draft d_20261007_01
parts: [Accounts, Sign-in, Checkout]
stages: [review, test, e2e]  # the optional stages the operator asked for
---

## Goal

## Why

## Scope

## Approach

## Parts

## Stages and checks

## Risks

## Out of scope
```

`task.md`:

```markdown
---
id: "0042"
key: SF-42                # the ticket number: the project's key and the id
title: Lock account after failed attempts
kind: feature             # feature | fix | chore | docs (revert is the backend's own, 7.13)
part: Sign-in             # the part of the plan it belongs to, or null
source:                   # where in the plan the task comes from
  file: plan/original.md
  heading: "Parts > Sign-in"
  quote: "A stolen password must not be usable without limit."
why: The plan says a stolen password must not be tried without limit.
what: Count failed sign-ins per account and lock the account for 15 minutes after five. Telling the owner is not included.
goal: Five failed sign-ins lock the account for 15 minutes.
checks:
  - cmd: "pytest tests/auth/test_lockout.py"
approach: Add a counter to the account, reset it on success, and check it before the password. Written in Analysis.
docs: [docs/reference/auth.md]   # the pages this task must update, or [] with a reason
docs_reason: null
stage: in_progress        # planning | backlog | analysis | in_progress | review | test | e2e | verification | done | closed
closed_reason: null       # for closed: discarded (by Analysis or by you) | cancelled (by the lead, and replaced)
skips: []                 # optional stages this task does not need, set in Analysis
skips_reason: null
origin: plan              # plan | agent | operator | token | conversation | chat (made by the operator from the chat of a ticket, 11.9)
created_by: lead          # the role that created it, or operator
seen: null                # for a task an agent created: what it saw that made it create the task
analysis:
  status: ready           # proposed | waiting | analysing | ready | skipped | new_feature_notice
  decided_by: lead · r_2b7c1e   # operator for the project OK, or the analysing role and run
  reason: null            # for skip, discard, a new feature's notice, and cancelled
  look_again: null        # for skip: the condition for analysing it again
  may_change: ["**"]      # the paths this ticket may change. Instruction files only when they are listed (13.8)
links:
  depends_on: ["SF-40"]
  created_from: null      # the task whose run created this one
  duplicates: null
  fixes_bug: null
priority: 2               # 0 urgent | 1 high | 2 normal | 3 low. The lower number goes first
hold: false
notice_until: null        # a new feature goes ahead at this time, unless it is held or discarded
agents: {}                # a pin of the model, with its CLI, or of the effort per role, for this ticket
network: inherit          # an override of the network mode, for this ticket
branch: task/SF-42-lock-account
pull_request: 57          # with a GitHub remote
fix_rounds: 0
replaces: null            # the cancelled ticket this one replaces
replaced_by: []           # for a cancelled ticket: the tickets that replace it
created: 2026-10-07T10:12:05Z
updated: 2026-10-07T12:30:00Z
---

## Notes for the builder
```

A subtask file, such as `subtasks/03-review.md` (7.11):

```markdown
---
id: SF-42.3
title: Review the change
kind: review              # analysis | build | review | test | e2e | verification | helper | join | look_again | merge | summary
stage: review
created_by: pipeline      # pipeline, backend, or the role and run that added it
after: []                 # subtasks that must be done first
touches: []               # the files or folders it may change
branch: null              # sub/SF-42.3 for a parallel subtask
parent: null              # for a helper: the subtask that started it
state: done               # queued | running | waiting | done | failed | cancelled. waiting: stopped for a reason that does not count, and it will continue (12.4)
role: reviewer
cli: agy                  # the CLI that ran it (12.2)
cli_version: <version>
model: claude-opus-5-5    # as the CLI lists it, without the effort (12.3)
effort: high
model_name: claude-opus-5-5-high   # what was passed to the CLI as --model. agy has the effort in the name
model_resolved: claude-opus-5-5   # the full name the CLI said it ran, when it says one
model_from: check_rule    # ticket | project | operator (set for the machine) | automatic (Crew's choice) | list (an agent that follows its list) | try | comparison | check_rule | limit (the next choice, because the first one's CLI was signed out or its group was at its limit)
effort_from: automatic    # ticket | project | operator | automatic | try | nearest (the model does not offer the effort that was asked for)
crew_version: 1.4.0       # the release pins the CLI, the harness, and the prompts
runs: [r_91bd07]          # more than one when a run was stopped and retried
sandbox: sbx-4c1e
started: 2026-10-07T11:22:00Z
ended: 2026-10-07T11:31:00Z
tokens: 88000
result: changes_requested
steps:
  - {text: Read the diff, done: true}
  - {text: Run the task's checks, done: true}
  - {text: Check that the documentation was updated, done: true}
---

One paragraph on what happened.
```

A run record, such as `runs/r_91bd07.md`. It lists the parts the prompt was built from, so the prompt can be rebuilt and checked (8.6):

```markdown
---
kind: run
run: r_91bd07
subtask: SF-42.3
role: reviewer
cli: agy
cli_version: <version>
model: claude-opus-5-5
effort: high
model_name: claude-opus-5-5-high   # what was passed to the CLI as --model
model_resolved: claude-opus-5-5
model_from: check_rule
effort_from: automatic
crew_version: 1.4.0
image: crew-runtime@sha256:<digest>
prompt:                     # the parts, in the order they appear in the prompt
  - {part: stage prompt, from: baseline/stages/review.md, version: 1.4.0}
  - {part: agent definition, from: baseline/agents/reviewer.md, version: 1.4.0}
  - {part: project override, from: null}
  - {part: task, from: ticket SF-42, change: c_000171}
  - {part: source plan, from: plan/original.md, heading: "Parts > Sign-in"}
  - {part: records, from: [notes/20261007T110501Z-builder.md]}
  - {part: memory index, entries: 9, change: c_000166}
  - {part: documentation, pages: [docs/reference/auth.md]}
  - {part: comments, count: 0}
prompt_sha256: <hash of the full text>
outcome: changes_requested
rounds: 1
tokens: 88000
started: 2026-10-07T11:22:00Z
ended: 2026-10-07T11:31:00Z
---
```

`summary.md`, written when the task ends:

```markdown
---
task: SF-42
outcome: done             # done | discarded | cancelled
started: 2026-10-07T10:40:00Z
ended: 2026-10-07T12:02:00Z
minutes_in_stage: {analysis: 14, in_progress: 41, review: 9, test: 11}
fix_rounds: 1
checks:
  - {name: "pytest tests/auth/test_lockout.py", passed: true}
  - {name: review, passed: true}
  - {name: test in a fresh sandbox, passed: true}
subtasks:
  - {id: SF-42.2, kind: build, role: builder, run: r_8f3a2c, minutes: 24, tokens: 324000}
  - {id: SF-42.3, kind: review, role: reviewer, run: r_91bd07, minutes: 9, tokens: 88000}
tokens: 412000
commits: [9a1c4e7, 3f9c2e1]
files_changed: 4
docs_changed: [docs/reference/auth.md]
led_to: {tasks: ["SF-51"], memory: ["0007"], bugs: [], decisions: []}
---

## What was done

## How it went

## What to know next time
```

An entry file (note, comment, review, test):

```markdown
---
kind: review                # note | comment | review | test
author: reviewer            # operator, or an agent role
run: r_91bd07
subtask: SF-42.3
in_reply_to: null           # comments
resolved: false             # comments
verdict: changes_requested  # reviews: approve | changes_requested
result: null                # tests: pass | fail
created: 2026-10-07T13:02:11Z
---

Body in markdown.
```

A documentation page on `main` (9.9):

```markdown
---
title: Sign-in and sessions
kind: reference             # overview | architecture | setup | decision | guide | reference
covers: ["src/auth/**"]     # the code this page describes
last_task: SF-42            # the ticket that last changed it
---

Body in markdown.
```

The project's settings are shown in section 14.

### 6.4 Write rules

- Only the server writes to the database while Crew runs. The updater changes its schema and restores a snapshot, during an update (16.4). Only the git store writes to `main`, under a lock per project.
- **One logical change is one transaction.** A stage move, its check results, and its history row are saved together or not at all.
- **Every change is kept.** A change to a ticket, a record, or a setting adds a row to the `changes` table: who, when, what, and the value before and after. Rows in `changes` are never changed. They are removed only when you delete a project: its rows go, and one row that says so stays. This is the audit trail, and it is what the app shows as history. The server writes each row with one helper, in the same transaction as the change it describes. There are no database triggers.

```
change    c_000184
at        2026-10-07T13:02:11Z
project   storefront-api
ticket    SF-42
what      stage: in_progress -> review
actor     builder
subtask   SF-42.2
run       r_8f3a2c
checks    passed
```

- **Code is still a commit.** A push to a ticket's branch and a merge into `main` are commits, with trailers that name the ticket and the run. The database keeps the commit of every push and every merge.
- **When both stores change,** the database goes first. A merge is recorded as intended, git does it, and the row gets its commit. After a crash the orchestrator compares the two and finishes what was half done.
- **Undo.** Putting back an earlier value of a setting or a memory entry is a new change with the old value. Nothing is removed from the history.

### 6.5 What is where

| Data | Where | Kept |
| --- | --- | --- |
| Code, documentation, and the project's own skills and context | Git, the project's repository | With the repository |
| Projects, plans, tickets, subtasks, stage moves, and checks | The database | For good |
| Notes, comments, reviews, test results, screenshots from E2E runs, run records, summaries, and decisions | The database | For good |
| Memory, bugs, feedback, eval cases and runs, and a project's overrides | The database | For good |
| The settings of the machine and of each project | The database | For good, with every earlier value |
| The history of every change | The database, `changes` | For good |
| Conversations with the operator, and the turns of every run | The database | For good, or until the operator deletes a conversation |
| Trajectories: every event of every run and conversation | The database, `events` | For good |
| The CLI's own conversation files | The database, `cli_files`, sealed with the key file (since 0.78) | `keep_cli_files_days` (7) after a run, `conversations.keep_cli_files_days` (30) after the last turn of a conversation with the operator |
| The work folder of a conversation with the operator | The database, `work_folders`, as an archive of at most 200 MB, sealed with the key file (since 0.76) | With the conversation |
| The full prompt of every run | The database | For good |
| Signals, improvements with their evidence, and releases with their notices and holds | The database | For good |
| Plan drafts | The database | For good |
| The queue, run status, timers, and the list of sandboxes | The database | While they apply |
| Credentials: the SSH key, the `gh` login, the key of the GitHub App, gcloud, the login of `agy`, the app password of the mail account. The settings and secrets of hosted projects. | The database, encrypted with a key that is a file on the machine (15.3) | Until replaced |
| Personal tokens as hashes, signed-in devices, and every notification that was sent | The database | For good |
| The calls your own tools made to the Crew MCP, and what was typed and printed in a terminal. The calls of a run are in its trajectory. | The database | 180 days |
| The documentation index, and analytics | The database, as caches | Rebuilt when needed |
| Docker images: the release folders, the runtime image, and the images of hosted projects | Files on the machine | The last `updates.keep_versions` (5) releases |
| Hosted projects: their data directories and logs | Files on the machine | While the project is hosted |
| Backups | tar.gz files on the machine (16.9) | The last 14 day files. The month files of events, for good |
| The key that encrypts secrets | A file on the machine, readable by the server only | For good |
| A run's home and workspace, with the spool of its events | A directory on the machine | Until the sandbox is removed. Nothing in it is the only copy for longer than seconds. |

### 6.6 A repository per project

Every project has exactly one git repository, and a project cannot be created without it (R39). The repository holds the project's code and documentation, on `main`. Its plan, tickets, records, knowledge, and settings are in the database.

**How the repository is created (R10).**

| Choice | What Crew does |
| --- | --- |
| New repository on GitHub (from M5) | With `gh` signed in, Crew creates it, private, under your account, through GitHub's API with `gh`'s login. Without `gh`, you create an empty repository on GitHub and give Crew its SSH address. Either way Crew sets it as the remote and pushes `main` over SSH, against GitHub's host keys pinned in Crew's source. |
| New local repository | Creates a repository on the machine with a first commit. A remote can be connected later. |
| Existing repository | Clones an `https` address, with a token of the GitHub App that can only read when the address is on GitHub, revoked after. Its history, its branches, and its tags are kept. It must have a `main`. A project that was exported from another Crew is brought back with `crew-ctl project import` (6.2). |

**GitHub access (R11).**

- **Git goes over SSH.** One key is made in Setup, and you add its public half to your GitHub account once. Crew uses it to clone, fetch, and push `main`, the ticket branches, and tags. Crew's own git traffic needs no token. Since 1.26 (CREW-30, register row 75), every SSH connection to GitHub with Setup's key goes to `ssh.github.com` on port `443` with `-o HostKeyAlias=github.com`, verified against GitHub's host keys pinned in Crew's source, while a project's remote URL stays `git@github.com:owner/name.git`.
- **What a key cannot do.** A key carries git and nothing else. Creating a repository, a pull request, and a release page are functions of GitHub itself. For those Crew uses `gh`, signed in once in Setup with a code in the browser, as `agy` is. `gh` keeps its own login, and you never handle it. You decided to sign it in. Crew keeps working when it is not, as the table shows.

| | With the key only | With `gh` signed in as well |
| --- | --- | --- |
| A new repository | You create an empty one on GitHub and give Crew its address | Crew creates it |
| A ticket's merge | Crew merges on the machine and pushes `main` | A pull request per ticket, with the verdicts on it, merged by Crew (7.12) |
| A release of Crew | A tag, pushed. The images stay on the machine. If dave itself is replaced, the new main machine builds the release again from the tag. Any other machine gets images (16.12). | Also a release page on GitHub with notes, and the images in GitHub's registry (16.9) |

- **The owner** of new repositories is the account the key belongs to: the person who installs Crew (R182). New repositories are private.
- The key and the `gh` login are kept in the database, encrypted (15.3). They can be replaced in Setup at any time.
- Neither is copied into a sandbox that runs an agent, a check, or a project. They are made in the setup microVM, where nothing else runs (12.6). A sandbox gets a temporary token that can only read (12.6), so nothing in a sandbox can push to GitHub.
- Neither enters a prompt, a log, or a trajectory. The event sink removes them (8.3).

**With a remote connected (R45).**

- The backend pushes `main` after each merge, and task branches while they are in work.
- The backend fetches `main` on a schedule and before each merge, so commits made outside Crew are picked up and become the new base.
- If the remote cannot be reached, work continues on the machine and the pushes wait.

**What heavy git integration means here (R43).**

| Area | How git is used |
| --- | --- |
| Records | Every plan, ticket, subtask, note, review, test result, summary, memory entry, bug, and piece of feedback is in the database, and every change to one is kept (6.4). Documentation pages are files on `main`. |
| Identity | Every commit is made with the operator's git identity (12.6). A commit by an agent also names its role and run in the trailers. A code commit carries its task and run, so any line can be traced to the task and from there to the plan. |
| Branches | One branch per task, named with its ticket number, created and removed by the backend. |
| Merging | A merge commit per task on `main`. A tag when a part of the plan is complete (`part-<name>`, the part's name in small letters and dashes, on the part's last merge) and when the project is Done (`done`, and `done-2` and on when it is Done again; Crew's project, which is never Done, gets none: 16.2, since 0.55). A tag is never moved. With a GitHub remote, a pull request per ticket (7.12). |
| Undo | Reverting a merged task is a `git revert` of its merge commit, started from the UI. Putting back a setting or a memory entry is a new change in the database (6.4). |
| History in the UI | Diffs and the answer to "why does this line exist" are read from git. A ticket's timeline is read from the database, and joined with its commits. |
| Portability | The code is the repository. The rest of a project is moved with `crew-ctl project export` and `import` (6.2), without its trajectories and conversations (R44). |

### 6.7 Git in the app

Git is where the code is, and it is a large part of what the operator sees and uses, whether the repository is on the machine or on GitHub (R137 to R139).

| Where | What the operator sees and can do |
| --- | --- |
| The project's Git screen | The branches, each with how far it is ahead of `main` and the ticket it belongs to. The commit history of any branch. Tags. The diff of any commit, and a comparison of any two points. The pull requests. The remote and whether it is in sync. The clone address. |
| A ticket | Its branch, its commits grouped by the subtask that made them, the diff against `main` file by file, the pull request or the merge commit, and Revert. |
| A subtask | The commits that run pushed, and the diff of only that run. |
| Pages and records | Every documentation page shows its history from `main`. Every plan and record shows its history and who changed it, from the database. |
| A terminal | The project terminal has a clone of the repository, with every branch (13.11). |

**Local or GitHub, and changing later.**

- A project works the same with a repository on the machine or one on GitHub. Nothing in the flow needs GitHub.
- A local project can be connected to GitHub later. Crew creates the repository, or uses one you name, and pushes `main` and the open task branches.
- A GitHub project keeps working while GitHub cannot be reached (6.6). The remote can also be removed, and the project stays local.
- Pull requests exist only with GitHub. A local project merges with a merge commit, after the same checks.

**Working on the repository yourself.**

- Every project has a clone address. For a GitHub project it is GitHub's. For a local project Crew serves the repository over HTTPS. `git` cannot do a Google sign-in, so it uses a personal token that has the git scope (11.7).
- You can clone, commit, and push as in any repository. Crew fetches what you pushed to `main` and uses it as the new base (R45).
- The `task/*` branches belong to Crew. A push to them from outside is refused. Records are not in the repository at all, so nothing can change them around the backend (6.4).

### 6.8 WORKFLOW.md, specs, YAML ticket contract, and Dreamstore (M21 to M26, since 1.47)

In the Software Factory model, declarative factory contracts, foundational and feature specs, and structured YAML ticket mirrors sit cleanly alongside the database brain:

- **Base and per-project `WORKFLOW.md`:** When a project factory is initialized from Project Planning, Crew copies `baseline/WORKFLOW.md` into the repository root as `WORKFLOW.md`. Its YAML frontmatter declares factory identity, CLI (`agy`), `max_tries`, `max_agent_hops`, `#<project>` channel and per-feature threads, `approvals.feature_specs` (`autonomous` default), check commands, and peer agent pipeline topology; its Markdown body explains project architecture, conventions, and done criteria. The orchestrator reads `WORKFLOW.md` exclusively from `main` (never from an unmerged task branch) and guards edits to it under `13.8` (operator approval for instruction files).
- **Two-tier specs (`.crew/specs/`):** Project Planning writes `.crew/specs/000-foundation.md` (requires explicit operator Project OK per Rule 12) before tickets exist; subsequent Feature Planning drafts `.crew/specs/NNN-<slug>.md` outside `Backlog`, decided either interactively with `@user` or autonomously between peer agents and `@planner` (`decided_by`, recorded in `decisions/`) unless altering `000-foundation.md` scope/non-goals, touching `13.8` protected files, or configured with `approvals.feature_specs: manual`.
- **Tasks API, `#<project>` chat tables, & structured YAML ticket contract (`.crew/tickets/<KEY>.md`, `R214`, `R217`):** No agent work runs outside a tracked Task (`id`, `key`, `title`, `kind IN ('spec', 'feature', 'fix', 'chore', 'docs', 'research', 'dream')`, `spec`, `origin_room`, `origin_msg`, `why`, `what`, `goal`, `checks`, `stage`, `priority`, `depends_on`, `max_tries`, `tries_used`, `assigned_agent`, `budget_tokens`, `may_change`). Stored transactionally in `StateDB` (`PostgreSQL` or `SQLite` WAL) alongside `chat_rooms` (`1` `#<project>` channel per project), `chat_threads` (`1` thread per feature `feat/<NNN>-<slug>` linked to `spec_id` and child `tasks`), and `chat_messages` (`room_id`, `thread_id`, `task_id`, `sender_role`, `mentions[]`, `hop_count`, `fenced`), and mirrored into `.crew/tickets/<KEY>.md`. Only executable code work (`feature`, `fix`, `chore`, `docs`) enters Kanban `Backlog`; non-code tasks remain trackable outside the code board with full sandbox and Trajectory linkage.
- **Dreamstore (`Memory API`), cross-project `Knowledge API`, & `Skills Registry` (`9.10`, `9.11`, `12.11`, `R218`, `R221`):** Typed per-project memory (`kind IN ('episodic', 'semantic', 'procedural')`, `validity_score` `0.0` to `1.0`, `verified_commit`, `evidence_runs[]`, `hit_pass_count`, `contradiction_count`, `paths[]`, `status IN ('active', 'drift', 'quarantined', 'retired')`) pairs with installation-wide `knowledge_entries` and `skills_registry` / `skill_installs` holding secret-scrubbed (`internal/redact`) base, cross-factory shared, Dream-synthesized, and SHA-pinned external skills (`SKILL.md` + `VENDORED.md`), cross-factory bug advisories, and reusable specs.
- **Lightweight Split Storage (`StateDB` + `BlobStore`, `R220`):** Repository interfaces separate hot transactional metadata (`StateDB`: `PostgreSQL` `crew-db` default or optional embedded `SQLite` WAL) from heavy immutable artifacts (`BlobStore`: `Local FS` or optional `GCS`) holding compressed `.jsonl.zst` trajectory event logs, `512 MB` sealed microVM snapshots (`13.12`), E2E videos/screenshots, and release bundles.
