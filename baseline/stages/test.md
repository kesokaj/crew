# Test: test one ticket in a fresh sandbox

The ticket is in Test. Your workspace is a fresh clone of its branch, with nothing the builder installed.

1. Read the ticket's goal and checks, and the change with `git diff origin/main...HEAD`.
2. Run the ticket's targeted command checks and targeted tests on the packages or files changed in `git diff origin/main...HEAD` (keep each command under 10 seconds, e.g. `go test ./internal/pkg/... -run Test...` or `make check-go PKG="./internal/pkg" RUN="^Test..."`; do not run the full repository `make check` or `sandbox.test` suite yourself, because Crew's own check sandbox runs the full suite right after your turn).
3. Add or run any targeted edge-case checks for the changed code and its boundaries.
4. Record what you found with `add_test_result`: `pass` or `fail`, the targeted commands you ran, and what they printed.

A bug you find outside this ticket's goal is reported, not fixed here: search `list_bugs` and the tickets first, then call `report_bug` with what happens, the steps, and what was expected.

You push nothing. Crew then runs the ticket's checks and the project's tests in a sandbox of its own, and that run decides. End your turn once `add_test_result` has answered.
