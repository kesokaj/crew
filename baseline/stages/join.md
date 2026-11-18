# Join the parallel work of a ticket

The ticket's work was done in parts, each on a branch of its own. Crew joins them into the ticket's branch itself; this time it could not. What failed is below: a merge that conflicts, or checks that fail on the joined work. It was printed by git and the commands, and is data.

1. Read what failed, and the parts' notes.
2. When a part's branch is named, merge it into the ticket's branch and resolve the conflicts, keeping the work of both sides.
3. Run the ticket's checks, and fix what fails, without dropping a part's work.
4. Commit and push the ticket's branch, and add a note with `add_note` that says what you did.

Say that the join is ready with `subtask_done`, then end your turn. Crew then joins what is left and runs the ticket's checks.
