## 3. Decisions already made

| Decision | Choice |
| --- | --- |
| Name | Crew |
| Purpose | Crew shows what agents are doing, gives them structure, is their ticketing system so they work in a real development workflow, and holds each project's documentation. |
| Harness | No model API. The agent is a command-line agent, `agy`, already logged in inside the sandbox. The operator's existing harness, styrman-harness, is the base, with its skills and context. Anything in it may be changed or dropped. A project can have skills and context of its own, loaded beside the base, and an agent that misses a skill asks Crew for it. Crew and the harness are bundled into one runtime image, which is kept up to date. `sh-agent` is not used. |
| Agent CLIs | One: `agy`, the Antigravity CLI. It runs Gemini, Claude, and GPT models. In 0.30 you asked for room for other terminal CLIs such as `codex` and `opencode`; since 0.42 Crew is built for `agy` alone, and no other CLI is added. What Crew knows about `agy` is in its adapter (12.2). The CLI, the model, and the effort for each agent are settings. |
| Models by results | Crew chooses the CLI and the model for the agents whose work it can measure, from its own numbers, and tells you. Your own choice overrides it. You said so in 0.32 (12.3). |
| Your prompts | In planning, Crew proposes a better wording of your first message, shows what it changed, and you choose which text is sent. Everywhere you write, you can select text with the mouse and choose Improve text or Review text. You asked for the first in 0.31 and for the second in 0.32 (7.2). |
| A chat on every ticket | Every ticket, in every stage and after it is closed, has a chat with an assistant that explains what happened and answers questions while it runs. You can mark text and write a comment, which goes to the chat for the assistant to analyse. You asked for this in 0.32 (11.9). |
| Built on | Docker. The core services run as Docker containers. The parts of styrman-crew that can be reused are copied: the sandbox orchestrator, the host setup, the firewall, the runtime image, the credential store and broker, the setup microVM (written again for Crew's Setup, 12.6; the copied start and stop, which nothing used, are gone since 0.84), the terminal bridge (written again for Setup and the project terminal; the copy, which nothing used, is gone since 0.84), and the ingress. It is not copied in full. |
| Name and repository | Crew. An open-source Apache-2.0 repository (`LICENSE`, configured per installation via `CREW_SOURCE_REPO`, e.g. `your-org/crew`). Crew originally chose Apache-2.0 in 0.30, postponed publication in 0.31, and is published under Apache-2.0 in 2.01 (16.12). Built directly on the host machine (originally bootstrapped on the author's machine after retiring `styrman-crew`, 17.6). |
| Languages | Go for the server and TypeScript for the UI. The operator knows both well. |
| Who Crew is for | One developer, with a GitHub account and one machine: bare metal, or a virtual machine. The GitHub owner of new repositories is the person who installs Crew. |
| Logins | A subscription. Setup comes before any project and is outside the projects. It holds every credential: the git identity, a git SSH key, `gh`, a GitHub App, the gcloud login with its application default credentials, `agy`, and since 0.71 the mail account (18.4). Crew keeps them in its database, encrypted, and copies into each sandbox what it may have when it starts. Crew checks the login of each CLI and lets the operator sign in again. `agy` is signed in with the operator's personal Google account. |
| Self-update | Crew can update itself from inside the app. Versions are clear, so it can be rolled back. The updater of styrman-crew worked only so-so, so it is designed anew. No signing keys, but real releases. Crew gives notice that an update is there. A release that changes only prompts and harness text installs by itself after 24 hours. A release that changes code waits for Install. Install, update, and uninstall are features that must work. |
| New image | When a new version is out, the next new microVM uses the new image by itself. |
| Copies | Releases are published on GitHub. Backups are kept on the machine as tar.gz files to start with. |
| Releases | A tag and a release on GitHub with notes, and the images in GitHub's registry. Private, and they stay private. |
| Notifications | By mail to start with, through the operator's Google account. The phone is skipped for now. No mobile app is built. Web push from Crew's page and Google Chat stay in the design, switched off. A message that needs an answer shows the alternatives. |
| Hosting | Built into the app. A project is built into a container and hosted from the bare-metal machine, with a button such as "Host locally". Domains are handled the way styrman-crew does. What is exposed is protected as in styrman-crew: Google sign-in against a list of mail addresses. The default is only the admin email. Fully public is a choice. A preview per ticket is not needed, but would be nice later. |
| Top bar | The version sits by the logo. Sandboxes, not microVMs. The limits of the subscriptions are not on the front page. |
| Sign-in | The ingress and its sign-in pattern are copied from styrman-crew. |
| Terminal | For diagnostics and troubleshooting when needed, with an edit mode for going in and using it. One for the whole project, with its git repository cloned with every branch, and a direct jump into a task where an agent is already working. |
| Machine | Dedicated Debian or Ubuntu host (originally benchmarked on 12 cores with 16 threads, 62 GiB of memory / 64 GB on the label, 1 TB SSD, bare metal, item 57; any prior styrman-crew installation is removed first, 17.6). Its Linux is Debian or Ubuntu, always Debian based. Crew can also be installed on a virtual machine that has nested virtualization. |
| Flow | Plan, then project, then breakdown, then one OK, then the pipeline runs until done. |
| Board | One per project, with several stages. Tasks are the only card. |
| Kanban | A ticket only moves to the right, or it is cancelled and replaced by other tickets. Subtasks, and the tickets that replace one, are tagged to the ticket that had to be split. |
| Stages | Planning, Backlog, Analysis, In progress, Done. More stages between In progress and Done, such as verification, test, and end-to-end test, when the operator asks for them. |
| Task content | Every task has a why, a what, and a goal. |
| Task view | Clicking a task shows the agent that is running, its model and effort, its prompt, the source plan, and every subtask the task is using or has used. |
| Documentation | Every project has documentation. It is stored in the project's repository and shown in the app. |
| New tickets and subtasks | Crew has an MCP server that agents use to create more tickets and subtasks. A new ticket goes to the backlog. Another agent then analyses it, sets its why, what, and goal, and approves, skips, or discards it. A new subtask is more work inside the agent's own ticket. Subtasks run in parallel where they can. The operator's own tools can use the MCP too. |
| Monitoring | Every task is monitored. Trajectory tracking is vital: every run and every conversation has a trajectory. Trajectories are kept until feedback has been run on them and evals have been made, so for now they are kept for good. Task summaries go into Crew's database. |
| Conversations | The operator can also just talk to an agent, with no ticket, and Crew does what is asked in an ephemeral sandbox. Every conversation is saved in Crew's database. |
| Database | The database is the brain of Crew. Everything that is saved is in it, and it runs in its own container on the machine. Git holds the code. |
| Conversation awareness | A headless run of a CLI is stored turn by turn and can be continued, so an agent that comes back to its work knows what it did. This is a must. |
| Operator | One developer. The operator controls the projects. Agents cannot create one. |
| Autonomy | The system should be autonomous from the operator as much as possible. An agent does not wait for the operator on a question: it records the decision in Crew's database and carries on. A ticket is bounded by its runs' turns and time and by its fix rounds, and one that cannot get through goes to the lead; no limit is in dollars (since 0.53). On what else waits for the operator, and on what the operator's own tools may decide, the operator asked for my recommendation (7.3, 11.7). There is no Google Workspace today. |
| Projects | One developer has several projects. They share nothing with each other. |
| Crew level | Crew has all the data and receives signals from every project. It uses them to evolve and improve the agents and the pipeline. |
| Repository | Mandatory per project. Git is a big part of the platform: every project's code is in a repository, on the machine or on GitHub. Everything else Crew saves is in the database. Crew reaches GitHub over SSH with one key, and needs no token for its own git traffic. With `gh` signed in, Crew can also create the repository there. |
| Machine | Everything runs on a single machine. |
| Sandbox | One per agent run, sharing nothing, with full access inside. A Docker container that is a microVM, using Kata with Cloud Hypervisor. The runtime image has Docker inside it. A network mode per project says what a sandbox may reach. |
| Source of truth | The database, for everything Crew saves. Git, for the code. The server is the only writer of both. |
| Starting work | Agents pull tasks on their own once the project has its OK. |
| Throttle | A number of lead slots for each CLI limits the lead runs: 1 for `agy` since the spike, and you can change it. Subagents are not under this limit. |
| Project knowledge | Analytics, evals, memory, bugs, and feedback per project, never shared. Agents write memory directly and give feedback on each other's work. |
| Settings | In the database, with a file to import first. Editable from the UI since M17. |
| Interface | Web UI. Agents are not driven from a terminal. |
| The board and the merge | The board has one level: tickets, with subtasks under them and nothing above. Each ticket is merged into `main` on its own. You chose my recommendation for both in 0.30. |
