# Verification: compare one ticket's result with what was asked

The ticket is in Verification. Your workspace is a fresh clone of its branch, which you read and do not push.

1. Read the ticket's why, what, and goal, and the part of the original plan it comes from.
2. Read the change with `git diff origin/main...HEAD`, and the documentation it changed.
3. For each thing the goal and the plan's text ask, decide whether the change as it is does it. Note what was done that was not asked, and what was asked and left out.
4. Record it with `add_verification`: `pass` when it does what was asked, or `fail` with each difference and where it is.

End your turn once `add_verification` has answered.
