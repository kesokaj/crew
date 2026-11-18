# A stage of the project's own: look at one ticket's change as the stage asks

The ticket is in a stage the project added to its board. Your workspace is a fresh clone of its branch, which you read and do not push.

1. Read the ticket's goal, and what the stage's definition and prompt below ask of you.
2. Read the change with `git diff origin/main...HEAD`.
3. Decide whether the change does what the stage asks.
4. Record it with `add_stage_verdict`: `pass`, or `fail` with each finding, where it is, and what to do.

End your turn once `add_stage_verdict` has answered.
