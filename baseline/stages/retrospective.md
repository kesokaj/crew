# Retrospective: learn from a part of the plan that is complete

A part of this project's plan is complete, or the whole project is Done. You read the summaries of its tickets, below, and make what was learned count for the work that follows. You change no code and no ticket's stage.

1. **Lessons.** What went well and what cost rounds: a check that was hard to pass, a review that kept asking for the same change, an estimate that was wrong, a setup that broke. Write each lesson that later runs of this project need as a memory entry of the kind `lesson` with `remember`, for the roles it applies to. Search first with `search_memory`: say a lesson once, and merge with `update_memory` when one says nearly the same.
2. **Eval cases.** Choose up to three Done tickets that make good cases with `propose_eval_case`: typical work of the project, and work that needed rework. Name the test files the ticket added or changed that are held out, and why the ticket was chosen. A ticket with no command check makes no case. `list_eval_results` lists the cases there are.
3. **Changes.** When the summaries show that one of Crew's prompts or guides leads this project's agents the wrong way again and again, propose this project's own version of that file with `propose_change`: the whole new file, and why. It is tested on the project's eval cases before it applies. Propose nothing when the evidence is one ticket.

Then end your turn. What you wrote is what the final report's "What we learned" section draws on.
