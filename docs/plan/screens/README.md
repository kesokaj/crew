# Screens of the simulation

> **Design 1.47 Software Factory Screenshots:** See [factory/README.md](factory/README.md) (`docs/plan/screens/factory/`) for all 20 captured screenshots covering the `shadcn/ui` Software Factory control plane, `Skills Registry`, autonomous `@planner` decisions, `#<project>` channel + per-feature threads, mobile `390px`, and the 40 runtime-switchable themes.

One full-page picture per screen, in the light theme, 1280 wide. They are preserved historical screenshots from the simulation at design version 0.32 (predating `claude` CLI removal in 0.42, USD cost removal in 0.53, warm project VMs in 1.37, and M24 `#<project>` chat). The walkthrough has its own, smaller set of pictures, at window size and in both themes, in `../walkthrough/shots/`. To click through the screens, open `../simulation/crew-simulation.html` (and `../simulation/ui-sim/index.html` for the locked `1.47` control plane).

The top bar with Resume, the speeds, and Restart, and the word SIMULATION, belong to the simulation. The real app has the logo, the version, the lead slots of each CLI, the sandboxes, and the inbox there.

| File | Screen | Where | What it shows |
| --- | --- | --- | --- |
| `overview.png` | All projects | Crew › All projects | The front page. Every project with its goal, its tickets by stage, the agents working on it, and what it has cost. Pause all and Stop all are here. |
| `convs.png` | Conversations | Crew › Conversations | Talk to an agent without a ticket. Every conversation is listed here and saved in Crew's database. |
| `conv.png` | A conversation | Crew › Conversations › one | Your messages and the agent's answers, with what it did in its sandbox in each turn. Its trajectory is below the messages. |
| `inbox.png` | Inbox | Crew › Inbox | Everything that needs you, and everything that is about to go ahead by itself. |
| `plans.png` | Plans | Crew › Plans | Where a project starts. The plan is on one side, and the conversation with the planning agent on the other. |
| `rewrite.png` | Improve text, on a selection | Crew › Plans | You select text with the mouse and choose Improve text or Review text. On your own writing the prompt writer shows a better wording beside it, and nothing is sent until you send it. In planning, the first message gets a proposal by itself. |
| `notify.png` | Notifications | Crew › Notifications | Mail, to start with, and every message that was sent. |
| `board.png` | Board | project › Board | One column per stage. Agents move the cards, not you. Amber means it needs you. Blue means an agent is working. |
| `task.png` | A ticket | project › Tickets › SF-12 | The view that answers four questions: what is happening now, what was the agent told, where does this come from, and what has been done. |
| `task-parallel.png` | A ticket with parallel subtasks | project › Tickets › SF-15 | The lead split the build of this ticket. Three builders work at the same time, each in its own sandbox and on its own branch. |
| `tickets.png` | Tickets | project › Tickets | The same tickets as a list, with the stage, the state, the agent, the cost, and how often each was sent back. |
| `chat.png` | The chat of a ticket | project › Tickets › a ticket | Every ticket, running or done, has a chat. An assistant answers from the ticket's records, and text you mark comes in as a comment with a quote. |
| `run.png` | A run | project › ticket › subtask | One subtask in full: how the agent was started, which model it uses and where that was set, what its sandbox holds and may touch, the conversation it belongs to, the prompt, and its trajectory. |
| `term.png` | Terminal | project › Board › Terminal | A terminal in the browser, for looking into things. The repository is read-only until you switch on edit mode. From a ticket you jump straight into the sandbox where an agent is working. |
| `plan.png` | Plan | project › Plan | The original plan and its parts, with the tickets that came from each. The plan is never edited after the OK. |
| `activity.png` | Activity | project › Activity | A live feed for one project: runs starting and ending, moves, commits, reviews, created tickets, and decisions. |
| `agents.png` | Agents | project › Agents | Every running sandbox in the project with its agent, model, ticket, and last step, and what waits for a slot, for room, or for a limit. |
| `docs.png` | Documentation | project › Documentation | The pages every project keeps about itself. Agents write them with the change they describe, and they live in the repository. |
| `traj.png` | Trajectories | project › Trajectories | The full record of every agent run in the project: every message, tool call, and result, kept in the database. Each row says which conversation the run continues, and opens the run. |
| `summaries.png` | Summaries | project › Summaries | What each finished ticket did and how it went. A separate step writes them, not the agent that did the work. |
| `task-done.png` | A finished ticket | project › Tickets › a Done ticket | The same view after the work. The outcome and the summary replace the running agent, and every subtask still has its prompt and its log. |
| `decisions.png` | Decisions | project › Knowledge › Decisions | Every choice an agent made by itself, with the alternatives and its reason. Agents do not wait for you. You can change any decision here, and Crew creates a ticket that carries the change out. |
| `knowledge.png` | Knowledge | project › Knowledge | Memory, bugs, feedback, and evals. They belong to this project only. No other project can read them. |
| `knowledge--bugs.png` | Knowledge: bugs | storefront-api › Knowledge | The Bugs tab of a project's knowledge. |
| `knowledge--decisions.png` | Knowledge: decisions | storefront-api › Knowledge | The Decisions tab: what agents decided alone, and Change. |
| `knowledge--evals.png` | Knowledge: evals | storefront-api › Knowledge | The Evals tab. |
| `knowledge--feedback.png` | Knowledge: feedback | storefront-api › Knowledge | The Feedback tab. |
| `analytics.png` | Analytics | project › Analytics | Flow, cost, and quality: tickets done, how many passed the first time, time in each stage, cost by agent. |
| `repo.png` | Git | project › Git | The code: branches with how far each is ahead, the history with its trailers, tags, and pull requests. Tickets and records are not here. A local project can be connected to GitHub later. |
| `settings.png` | Settings | project › Settings | The stages and their limits, the model and the effort for each role, the rule for checks, the project's own skills and context, the network mode, and who decides what. |
| `host.png` | Hosting | project › Hosting | Host locally, the address, who may open it, whether it may reach the internet, the running version and its health, the deploys with roll back, and the logs. |
| `crew.png` | Crew level | Crew › Crew level | What Crew sees across all projects: how often you stepped in, how many checks passed the first time, and the signals by kind. |
| `crew--baseline.png` | Crew level: baseline | Crew › Crew level | The Baseline tab: the prompts and defaults every project starts from. |
| `improvements.png` | Improvements | Crew › Crew level › Improvements | What the improver proposed, the release it shipped in, and the measured effect. Each one opens to its evidence, its diff, and its evals. |
| `versions.png` | Versions | Crew › Versions | The running version, the next release and whether it waits for you, every kept release with roll back, and the steps of the updater. |
| `clis.png` | Agent CLIs | Crew › Agent CLIs | One card for each CLI, agy and claude: signed in or not, the last check, and the version. Beside each, the limits of its groups of models. Below: what happens when a CLI cannot run, the rule for checks, and what a sandbox is given. |
| `models.png` | Models | Crew › Models | At the top, What Crew chose: for the builder, the reviewer, the tester, the E2E tester, and the verifier, who chooses (Automatic or Set by you), what Crew changed with its numbers, and Go back, Set it myself, Use it, and Back to Automatic. Then a comparison. Below, every model the CLIs list themselves, by CLI and family, and the agents with the model, the effort, who chooses, and the next choices of each. |
| `setup.png` | Setup | Crew › Setup | The first screen of a new installation, and where you sign in again: the machine check, the mail account, and each credential with its state. On the right, what each kind of sandbox gets when it starts, and how Crew notices a login that stopped working. |
| `db.png` | Database | Crew › Database | The brain of Crew: the container, every group of tables with its rows and how long they are kept, what is not in it, the backups, and the latest changes from the history. |
| `mcp.png` | Crew MCP | Crew › Crew MCP | The tools agents have, every call as it happens, what agents created, and the tokens for your own tools, each with the day it runs out. |
| `gsettings.png` | Machine settings | Crew › Machine settings | Lead slots for each CLI, the host machine and its sandbox sizes, network modes, GitHub over SSH and gh, the default model and effort for each role, updates, backups, and how Crew looks after the machine. |
| `board-parallel.png` | Board, with parallel subtasks | A project › Board | The board while one ticket runs several subtasks at once. |
