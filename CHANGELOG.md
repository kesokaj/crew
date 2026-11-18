# Changelog

## CREW-19: An eval's held-out checks run with the project's network, so the branch's code can send the held-out tests out

2026-10-31 · fix

In: for an eval's held-out check only. (1) The check sandbox starts in the project's check mode, as today, with the branch's head alone: the tree without the held-out files. Where a held-out file goes, the branch's own file is still there. (2) The project's setup runs on that tree in that mode. (3) Crew then cuts every route out of the sandbox from outside the microVM: the container is disconnected from its bridge, and the server's door drops the sandbox so its broker and proxy refuse it. Crew shows the cut holds before it goes on. Nothing inside the guest is trusted to do this. (4) Only then are the held-out files written in: what the branch has at a held-out path gives way to the held-out file, as 9.6 says since 1.02. (5) The case's commands run with no route out. A cut that fails, or that cannot be shown to hold, gives no result, and the commands never run with a route. The same goes for held-out files that cannot be placed after the cut. Either way the run measured nothing (register row 8). How a held-out check is read (evalend.go, readCheck) does not change. The design changes in the same commit: 9.6 (How a case runs), 13.6 (Which mode the other sandboxes have), the 13.9 table row for held-out checks, and register row 22. The design's version goes up (1.20 to 1.21, with its change history heading) in the design, the requirements header and docs/plan/README.md, and python3 tools/plan-copies.py is run again. Out: a ticket's own checks, a merge check, the setup check on mai

The builder's last note: Merged `origin/main` (`38f3beb`, design 1.23) into `task/CREW-19-an-eval-s-held-out-checks-run-with-the-p` (`3cde1fe`), resolved the conflict in `docs/plan/agent-kanban-tdd.md` at design version 1.24 (`### From 1.23 to 1.24` in section 26, and `since 1.24` in 9.6, 13.6, 13.9, and register row 22), updated `docs/plan/agent-kanban-requirements.md` and `docs/plan/README.md` to version 1.24, and regenerated `docs/plan/build-plan.md`, `docs/plan/open-questions.md`, and `docs/plan/spike-checklist.md` with `python3 tools/plan-copies.py`.

### Commands run
- `python3 tools/plan-copies.py --check` -> `the copies match the design`
- `git diff origin/main -- docs/plan/agent-kanban-tdd.md | wc -c` -> `62471` (< 65,536 bytes)
- `make check-plan check-versions check-clean check-tools check-compose test-firewall` -> PASS (53/53 firewall tests, 2792 clean files)
- `gofmt -l */` -> empty
- `grep -q 'func TestAnEvalsHeldOutFilesComeAfterTheSetupWithNoRouteOut(' internal/pipeline/*_test.go && grep -q 'func TestAHeldOutCheckCutsItsRouteBeforeTheHeldOutFiles(' internal/harness/*_test.go` -> exit 0
- `! grep -q '^| 22 | .* | Planned | ' docs/plan/agent-kanban-tdd.md` -> exit 0

## CREW-20: A subtask that waits on a group's limit continues when the quota table reads that group free again, not only at the reset it stored

2026-10-31 · fix

In: wakeWaiting (internal/runs/monitor.go) also continues a subtask that waits on 'quota:<cli>' before its wait_until, when both of these hold: (a) the quota guard (models.LoadGuard, Guard.Limited) no longer holds the group, or the model's own limit, of the model the subtask's last run used; (b) Crew's own login check (quota rows with source 'login check') has read that group's windows after the subtask began to wait (read_at > subtasks.updated_at, which endSubtask sets when the wait begins, as the sign-in wait already uses). A window a run reported (source 'run', such as the 'limit' window that Reached writes) is never read again by the login check, and frees nothing by itself. A run's report never ends a wait early, since it is the sandbox's word (12.4). The continue is the same one the reset gives now: the same conversation, auto_continues counted as now (MaxAutoContinues applies), and nothing for a paused project, a ticket on hold, or Crew held. Design 12.7 (Running subtasks) and register row 71 in section 28 say what was done, in the same commit. The row's state is changed when this ticket is Done, not by this ticket. Out: no change to how a limit is recorded, to RecordLimits or Reached, to NextChoice's choice, to the login check's interval, or to a wait for a sign-in or a refused model.

The builder's last note: Merged `origin/main` (`8ac21ab`, which includes `c56e8cd`'s fix for `BUG-031` / `CREW-33` where `a31da9f` had changed section 14.1's `crew.yaml` listing in `docs/plan/agent-kanban-tdd.md` without `internal/settings/crew.yaml`) into `task/CREW-20-a-subtask-that-waits-on-a-group-s-limit` (`c36af0d`):

- Verified `make check-plan check-versions check-clean check-tools check-compose test-firewall` (`PASS=53 FAIL=0`).
- Verified `go test -v -run TestDefaultsAreTheDesignsListing ./internal/settings/` (`PASS`).
- Verified ticket check 1 (`TestAQuotaWaitEndsWhenALoginCheckReadsTheGroupFree` with `-race` in `./internal/runs/`, `PASS` in `0.48s`).
- Pushed `c36af0d` to `origin/task/CREW-20-a-subtask-that-waits-on-a-group-s-limit`.

## CREW-24: dbtest copies a template database made once per test process, instead of applying every schema step for each test

2026-10-31 · chore

In: internal/db/dbtest/dbtest.go: the first New(t, false) in a test process makes a template database (grants plus all 81 schema steps), once. Every New(t, false) after that does CREATE DATABASE <name> TEMPLATE <tpl> OWNER crew_migrate and runs Grants() on the copy again. A template does not copy the database-level grants. New(t, true) does not change. One comment says why. A new internal/db/dbtest/dbtest_test.go with TestNewCopiesTemplate shows three things. Two copies have the same schema_steps rows, applied_at included, so the second copy applied no step. A row written in one copy is not seen in the other. The server role crew can write in a copy, so the grants arrived. docs/testing/guide.md, item 1: one sentence that dbtest copies a template database made once per test process. Out: no test, subtest or assertion is removed, skipped or weakened. No t.Parallel (that is the next ticket). No production code. No Makefile change, so no new target and no change to the timeouts. The design document does not change: it does not describe dbtest at this level.

The builder's last note: ### Merge check failure diagnosis (`TestDefaultsAreTheDesignsListing`)
- The failure in the merge check (`internal/settings`: `TestDefaultsAreTheDesignsListing`, `settings_test.go:41: internal/settings/crew.yaml differs from the listing in section 14.1 of the design`) is on `origin/main` itself, introduced by external commit `a31da9f` ("Design 1.22 and REQUIREMENTS R130, R203..."), which updated the `crew.yaml` listing in `docs/plan/agent-kanban-tdd.md` section 14.1 for `CREW-32` without updating `internal/settings/crew.yaml`.
- Crew's remote-main check already detected this on `main` and created `CREW-33` ("Fix main from the remote: its tests fail").
- Reported Bug #7 (`CREW-34`) for `mergeQueue` / `remote` (`internal/pipeline/remote.go:50`) running `PurposeMerge` and starting `kindFix` rounds on unrelated tickets while `main`'s remote check is failing and its fix ticket (`CREW-33`) is still open.
- Every database test across the repository passed in the merge check (`internal/db`: `17.642s`, `internal/db/dbtest`: `2.492s`, `internal/httpapi`: `584.536s`).

### Changes (`30d9396`)
- `internal/db/dbtest/dbtest_test.go`: Addressed the reviewer's note on `TestNewCopiesTemplate` so that the `slices.Equal(gotA, gotB)` failure message prints `len(gotA), len(gotB)` instead of indexing `gotA[0], gotB[0]` (which could panic if `gotB` were empty).
- Re-ran `gofmt`, `go vet ./internal/db/...`, `python3 tools/check-clean.py`, and both ticket `cmd` checks: all passed.

## CREW-26: TestABuilderWhoseRunEndedWhileItWaitedIsTakenUp/long_check checks TakenUp without k.until after SF-1 reaches Done, racing markMovedOn

2026-10-31 · fix

In: in internal/httpapi/m16_test.go, the "pause" and "long check" case of TestABuilderWhoseRunEndedWhileItWaitedIsTakenUp waits with k.until("the run taken up is marked so", ...) for the builder's run to have stop_detail 'no next turn came; Crew took up its turn after', in place of the one-shot k.count and its t.Fatal. The check that the builder had exactly one run and the judge passed stays. Out: no change to internal/pipeline or internal/board (the product code is right and takeup_test.go proves it); no change to the "not met" and "round limit" cases; no other test.

The builder's last note: ### Summary of changes

- In `internal/httpapi/m16_test.go` (`TestABuilderWhoseRunEndedWhileItWaitedIsTakenUp`, `"pause"` and `"long check"` cases), replaced the one-shot `k.count` check and `t.Fatal("the run taken up is not marked so")` after `SF-1` reaches `"done"` with `k.until("the run taken up is marked so", ...)`, matching `internal/httpapi/takeup_test.go:139-141`. Added a comment noting that the board can move `SF-1` to `Done` before the pipeline's next pass runs `markMovedOn` (`BUG-028`, `internal/pipeline/takeup.go`). Kept the check before it asserting one builder run and `judged == "passed"`.

### Verification commands and output

1. Fast check targets:
   `make check-plan check-versions check-clean check-tools check-compose test-firewall` -> all passed (`check-clean: ok, 2784 file(s)`, `scrub_test.py` 7 tests OK, `tools_test.py` 33 tests OK, `firewall/test.sh` `PASS=53 FAIL=0`).
2. `crew26keep` check (`gofmt` and `go vet` in check image):
   `make --eval 'crew26keep: check-image ; $(GO_RUN) $(CHECK_IMAGE) sh -c "out=\$$(gofmt -l internal/httpapi); test -z \"\$$out\" && go vet ./internal/httpapi/"' crew26keep` -> exited 0.
3. `crew26` check (grep assertions + 5x `-race` run of `TestABuilderWhoseRunEndedWhileItWaitedIsTakenUp|TestABuilderTakenUpIsMarkedWhenItsTicketLeftInProgressFirst` in check image):
   `grep -q 'k.until("the run taken up is marked so"' internal/httpapi/m16_test.go && ! grep -q 't.Fatal("the run taken up is not marked so")' internal/httpapi/m16_tes

## CREW-18: board.advance counts cancelled and superseded part subtasks as open and unjoined in in_progress

2026-10-31 · fix

In: the open-subtask count in Board.advance (internal/board/reconcile.go, In progress only) no longer counts a part or join whose state is 'cancelled' or 'superseded'. A part that is 'done' but not joined yet still holds the ticket, and so does any part or join in another state that is not done. The comment above the query says the same. One regression test in internal/board that fails on main and passes after the fix. Out: no change to NewApproach, to the pipeline's partsOpen, or to how parts are joined; no recovery of tickets already stuck this way; no new subtask state.

The builder's last note: ### Fix Round 2 (`test` stage)

#### Cause of the `make check` failure on `75413fb`
- Every package in `make check` passed except `internal/httpapi`, which failed on `TestABuilderWhoseRunEndedWhileItWaitedIsTakenUp/long_check` (`internal/httpapi/m16_test.go:1594: the run taken up is not marked so`).
- In `internal/httpapi/m16_test.go:1584-1595`, after `k.until("SF-1 Done", func() bool { return k.stageOf("SF-1") == "done" })`, line 1593 immediately reads `r.stop_detail` once without `k.until`. When the merge check completes and `Board.Reconcile` moves `SF-1` to `done` before the pipeline's next pass runs `markMovedOn` (`internal/pipeline/takeup.go`), line 1593 races ahead of `markMovedOn` (unlike `internal/httpapi/takeup_test.go:139-141`, which waits with `k.until`).
- Reported this test race outside `CREW-18` with `report_bug` (Bug 4, `CREW-26`).

#### What changed
- Merged `origin/main` (`ed7e7e6`, `CREW-17`) into `task/CREW-18-board-advance-counts-cancelled-and-super` as commit `5986f9f` and pushed to `origin`, keeping the branch diff against `origin/main` unchanged (`internal/board/reconcile.go` and `internal/board/lookagain_test.go`, +47 -2).

#### Verification
- Fast repository checks (`make check-plan check-versions check-clean check-tools check-compose test-firewall`): passed (`PASS=53 FAIL=0`).
- Ticket check `crew18` (`go vet ./internal/board/ && go test -race -count=1 -timeout 20m -v -run TestASupersededOrCancelledPartDoesNotHoldInProgress ./internal/board/`): `PASS

## CREW-16: new_approach on a ticket in in_progress starts kindFix and never runs in_progress exit cmd or judge checks

2026-10-31 · fix

In: the new approach's builder on a ticket in In progress hands over with request_move and goes through In progress's exit: Crew's command checks (PurposeExit) on the branch's head, the judge, and reconcile, so the ticket moves to its next stage (Review, Test, or the merge when In progress is last). This also works when the head was already pushed by the stopped run before the new approach began. A new approach in a later stage (Review, Test, E2E, Verification, a project stage) keeps working as it does now (a fix run with fix_done). One regression test in internal/pipeline that fails on main and passes after the fix, and the existing httpapi m11 tests updated to the new behaviour.

Out: no recovery of tickets that are already stuck this way (CREW-13 is the operator's to unblock, see Why). No change to fix rounds, to the merge queue's rule of which tickets it merges, or to the design document. No new test in internal/httpapi beyond updating the existing ones: that package is already too slow (CREW-13, CREW-15).

The builder's last note: ## Review fix round

1. `internal/pipeline/parts.go` (`hasParts`, Finding 1):
   - Updated `hasParts(subs)` to `s.kind == kindPart && s.state != "cancelled" && s.state != "superseded" && (s.id > build || !(s.state == "done" && s.joined != nil))` and updated its doc comment, so parts from before the latest `kindBuild` that still owe a join (such as `running`, `idle`, or `done` with `joined == nil`) continue to route through `parts()`, while cancelled, superseded, and already-done-and-joined parts from before `build` are ignored.
2. `internal/pipeline/m11_test.go` (`TestHasParts`):
   - Added `TestHasParts` table test covering old part done+joined (`false`), old part superseded (`false`), old part cancelled (`false`), old part done but not joined (`true`), old part running (`true`), part after the build (`true`), and no build with a part (`true`). Verified it failed before the `parts.go` change on the two unjoined old-part cases and passed after.
3. Ran `TestHasParts` and `TestANewApproachInInProgressRunsItsExitChecks` (`PASS`, `1.731s`), committed `5bebd62`, and pushed to `task/CREW-16-new-approach-on-a-ticket-in-in-progress`.

## CREW-17: Move the pinned image GO_IMAGE to golang:1.27.3-trixie's current digest (after CREW-13)

2026-10-31 · chore

In: set GO_IMAGE in versions.lock, and ARG GO_IMAGE on line 7 of deploy/Dockerfile.server and image/Dockerfile, to golang:1.27.3-trixie@sha256:c65ecfe613b539ac74c62a4c3b2c27e8f8a6076384c8f27e8a3c0f3be17cc385. Three one-line edits in one commit, on a branch from current main, after CREW-13 is merged. Out: any other pin, the Go version, ingress/Dockerfile (names no GO_IMAGE), testdata/fixture-project/Dockerfile, tools/check-image/Dockerfile (takes GO_IMAGE as a build-arg from the Makefile, no default), the Makefile (CREW-13's) and docs.

The builder's last note: Updated `GO_IMAGE` in `versions.lock:45` and `ARG GO_IMAGE` on line 7 of `deploy/Dockerfile.server` and `image/Dockerfile` from `golang:1.27.3-trixie@sha256:fbb7ae40b62ff8afe31662df570d39b4f5052ac32b90dae8e60d7657a20a74f4` to `golang:1.27.3-trixie@sha256:c65ecfe613b539ac74c62a4c3b2c27e8f8a6076384c8f27e8a3c0f3be17cc385` (commit `0fc6e22` on branch `task/CREW-17-move-the-pinned-image-go-image-to-golang`, branched from `854c24b` with `CREW-13`'s `-timeout 20m` on `main`).

### Commands run and output

1. Verified `registry-1.docker.io` manifest digest for `library/golang:1.27.3-trixie` and 1.27.x tag list:
   - `Docker-Content-Digest: sha256:c65ecfe613b539ac74c62a4c3b2c27e8f8a6076384c8f27e8a3c0f3be17cc385`
   - `1.27.x tags: ['1.27.0', '1.27.1', '1.27.2', '1.27.3']` (no `1.27.4+` tag exists)
2. Verified the ticket's `grep` checks failed in the must-fail state before the edit, and passed after the edit:
   - `grep -qxF 'GO_IMAGE=golang:1.27.3-trixie@sha256:c65ecfe613b539ac74c62a4c3b2c27e8f8a6076384c8f27e8a3c0f3be17cc385' versions.lock && grep -qxF 'ARG GO_IMAGE=golang:1.27.3-trixie@sha256:c65ecfe613b539ac74c62a4c3b2c27e8f8a6076384c8f27e8a3c0f3be17cc385' deploy/Dockerfile.server && grep -qxF 'ARG GO_IMAGE=golang:1.27.3-trixie@sha256:c65ecfe613b539ac74c62a4c3b2c27e8f8a6076384c8f27e8a3c0f3be17cc385' image/Dockerfile` -> exit 0
   - `! grep -qF 'fbb7ae40b62ff8afe31662df570d39b4f5052ac32b90dae8e60d7657a20a74f4' versions.lock deploy/Dockerfile.server image/Dockerfile` -> exit 0
3. Ran 

## CREW-11: Update gh to 2.103.3, from gh's apt index

2026-10-31 · chore

In versions.lock set RUNTIME_GH_VERSION=2.103.3 (was 2.103.2). In image/Dockerfile set ARG RUNTIME_GH_VERSION=2.103.3, the one other place tools/check-versions.py lists for this key. Nothing else changes: no other pin, no code, no docs.

The builder's last note: Fix round in `test` stage:
- Inspected the `PurposeTest` check sandbox failure on `cd5ffef23538`. All three ticket checks (`grep -qxF 'RUNTIME_GH_VERSION=2.103.3' versions.lock`, `grep -qxF 'ARG RUNTIME_GH_VERSION=2.103.3' image/Dockerfile`, `python3 tools/check-versions.py`) and every `make check` step passed except `github.com/kesokaj/crew/internal/httpapi`, which hit Go's default 10-minute package test timeout (`FAIL github.com/kesokaj/crew/internal/httpapi 600.099s`) under host load.
- This `internal/httpapi` 10m timeout is a known issue on `main` recorded in design 1.17 Section 28 register row 38 and is already tracked and being fixed by `CREW-13` ("Fix main from the remote: its tests fail", which adds `-timeout 20m` to `Makefile`); no duplicate bug or ticket was created, and no files outside `versions.lock` and `image/Dockerfile` may be modified in `CREW-11`.
- Advanced `task/CREW-11-update-gh-to-2-103-3-from-gh-s-apt-index` to `d1d9b0b603a0bad9f4c3b18c61a0fb6a8b819877` (keeping the diff from `main` strictly limited to `versions.lock` and `image/Dockerfile`), re-verified all ticket checks (`grep -qxF 'RUNTIME_GH_VERSION=2.103.3' versions.lock`, `grep -qxF 'ARG RUNTIME_GH_VERSION=2.103.3' image/Dockerfile`, `python3 tools/check-versions.py`, `python3 tools/check-clean.py`), and pushed to `origin` so `fix_done` can complete and re-run the test stage checks.

## CREW-1: README: read a running service's version without running its binary

2026-10-22 · docs

In README.md, in the Releases section after the code block with build, gate, update and rollback, add one short paragraph: the running release and the updater's version are read with `sudo crew-ctl status`, and a container's image and its tag with `docker inspect` of the container; never by running a service's binary inside its running container, which starts a second instance of the service. Nothing else changes.

The builder's last note: Pushed an empty commit (`bac3529`) on `task/CREW-1-readme-read-a-running-service-s-version` now that `CREW-1`'s `network` has been changed from `model_only` to `inherit` (so `CheckNetworkMode` gives the merge check sandbox `sources` instead of `model_only`). The tree and diff against `origin/main` are unchanged (`README.md | 8 ++++++++`).

### Commands run
- `git commit --allow-empty -m "CREW-1: re-trigger merge check after ticket network set to inherit"` -> `bac3529`
- `git push origin task/CREW-1-readme-read-a-running-service-s-version` -> pushed `df33f57..bac3529`
- `git diff --stat origin/main...HEAD` -> `README.md | 8 ++++++++` (1 file changed, 8 insertions)
- `grep -q 'docker inspect' README.md && grep -q 'second instance' README.md` -> exited with code 0 (`CHECK_OK`)
