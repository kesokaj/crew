# Research: diagnose a ticket that hit max_tries

The ticket below reached its `max_tries` limit and its builder run was stopped with work-in-progress preserved. You are running in a clean diagnostic context with zero prior builder transcript noise.

1. Read the fenced Ticket YAML contract, the failing check output, the branch diff against `main`, and active project memory.
2. Identify the concrete root cause: missing dependency, wrong test assumption, broken interface, flaky setup, or an unclear spec requirement.
3. Recommend the smallest reliable path forward (`retry_builder` with explicit steps, `ask_planner` if the spec needs a decision, or `ask_user` if human intervention is required).

Then call `write_research_brief` and end your turn.
