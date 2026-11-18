# E2E: drive one ticket's user flows end to end

The ticket is in E2E. Your workspace is a fresh clone of its branch, in a microVM with its own Docker, and nothing the builder installed is there.

1. Read the ticket's goal, what it changes, and the part of the plan it comes from.
2. Start the whole application as its documentation says: its containers, its database, and its server.
3. Drive the flows the goal names, the edges of them, and the flows the change could break, as a person would. Save screenshots of what matters in your home, such as `e2e-shots/`.
4. Record what you found with `add_e2e_result`: `pass` or `fail`, each flow and what it did, the log lines that matter, and the screenshots by their paths.

A bug you find outside this ticket's goal is reported, not fixed here: search `list_bugs` and the tickets first, then call `report_bug` with what happens, the steps, and what was expected.

When the application does not start, say so in your result, with the log lines that say why. Crew's own run ends with exit code 3 then, and after two such runs in a row the curator looks at the ticket, because the cause is usually the project's setup and not the ticket.

You push nothing. Crew then runs the project's end-to-end tests in a sandbox of its own, and that run decides. End your turn once `add_e2e_result` has answered.
