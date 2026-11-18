# The plan

These are Crew's planning documents, at design version 2.01. They are kept with the Apache-2.0 open-source codebase for architectural transparency (`LICENSE`). Note that `screens/` (except `screens/factory/`) and `walkthrough/index.html` are preserved pre-build v0.32 simulation artifacts that predate `claude` CLI removal (0.42), dollar-cost removal (0.53), warm project VMs (1.37), and M24/M25 updates.

| Document | What it is |
| --- | --- |
| `agent-kanban-tdd.md`, `tdd/*.md` | **The technical design (TDD).** The source of truth. `agent-kanban-tdd.md` holds the header and the manifest of sections 1 to 28 (`tdd/01-purpose-and-scope.md` to `tdd/28-register.md`). Section 2 is how Crew works, section 3 what was decided, section 4 the defaults, sections 5 to 21 the design, 22 the tests, 23 the build plan, 24 the spike, 25 the parked questions, 26 the change history, and 28 what the reviews found. |
| `agent-kanban-requirements.md` | **The requirements (PRD).** R1 to R221, each with the section of the design that answers it. |
| `build-plan.md` | Section 23 of the design on its own: what is needed before the build, and the milestones M0 to M26. |
| `spike-checklist.md` | Section 24 of the design on its own: the questions of milestone M0, which milestone answers each, and what Crew does if an answer is no. |
| `open-questions.md` | What is still the operator's to decide, what the design leaves open on purpose, and what is closed only in part. |
| `software-factory-plan.md` | **Software Factory master blueprint (M21–M26, R209–R221).** End-to-end architecture, 7-module split, autonomous `@agent -> @planner` feature decisions, `#<project>` + per-feature threads, `Skills Registry`, all `40` themes, and complete build plan. |
| `screens/` | Full-page screenshots of the simulations (`screens/README.md` for classic v0.32 screens; `screens/factory/` for the v1.47 Software Factory suite across desktop, mobile, Skills Registry, `#<project>` chat, and themes). |
| `simulation/ui-sim/index.html` | Interactive `shadcn/ui` control-plane simulation (`Direction A` inset sidebar locked in `DECISION.md`, **all `40` shipping themes** + Swatch Gallery, light/dark, mobile/desktop, `10` views incl. `Skills Registry`, `#<project>` + feature threads). |
| `simulation/crew-simulation.html` | The live state-machine simulation with a simulated clock, `#<project>` + per-feature threads, autonomous `@planner` decisions, `Skills Registry`, and all `40` themes. Open it in a browser. |
| `walkthrough/index.html` | How Crew works, in pictures: the parts, a project from plan to hosted, one ticket, and every screen. |

## How to use them

1. The design decides. The simulation and the pictures show how it should look and behave. Where they differ from the design, the design is right, and the difference is worth a note.
2. `build-plan.md`, `spike-checklist.md`, and `open-questions.md` are made from the design. When the design changes, change the design, and run `python3 tools/plan-copies.py` in the same commit. `AGENTS.md` says how the version is raised.
3. Numbers such as "12.6" point at sections of the design. Numbers such as "R198" point at requirements.
