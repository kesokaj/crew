---
version: 1
project: "{{PROJECT_NAME}}"
key: "{{PROJECT_KEY}}"
harness: agy
max_tries: 3
max_agent_hops: 3
approvals:
  feature_specs: autonomous
  workflow_edits: operator
roles:
  planner: "@planner"
  coordinator: "@coordinator"
  builder: "@builder"
  reviewer: "@reviewer"
  security: "@security"
  researcher: "@researcher"
  tester: "@tester"
  dreamer: "@dreamer"
stages:
  - backlog
  - in_progress
  - review
  - verify
  - done
---

# {{PROJECT_NAME}} Workflow

Crew reads `WORKFLOW.md` from `main` only (design 6.8, 13.8). Edits on a branch never alter orchestration rules until reviewed and merged to `main` with operator approval.

## Peer Execution Rules

1. Every agent session is one peer role (`1 session = 1 peer agent`); CLI internal subagents are disabled.
2. Executable tickets (`feature`, `fix`, `chore`, `docs`) live in `.crew/tickets/<KEY>.md` and move left-to-right across the board on independent check sandboxes.
3. When a ticket reaches `max_tries`, execution stops and a clean-context `@researcher` session diagnoses root cause before retrying.
