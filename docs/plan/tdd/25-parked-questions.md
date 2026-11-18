## 25. Parked questions

Collected while brainstorming, for when you want to decide them. Each has a default in the design.

1. **Answered.** A new feature gives notice and goes ahead by itself after 24 hours (7.3).
2. **Answered in 0.30 with my recommendation.** The board has one level: the plan is split straight into tickets. Features or milestones as cards above tickets are not built.
3. **Answered in 0.30 with my recommendation.** Each ticket is merged into `main` when it passes its last stage, through a pull request when there is a GitHub remote. There is no merge per part of the plan.
4. Should a skipped task stop the project from being Done? I let the project finish and list skipped tasks in the report.
5. The task summary is written by a separate step, not by the agents that did the work. Is that what you meant?
6. **Answered in 0.26.** Until feedback has been run on them and evals have been made. For now that means for good (8.3).
7. **Answered.** The owner is the person who installs Crew. New repositories are private.
8. Is there anything on your local network that agents need, such as a private package server?
9. **Answered.** Yes. The tool set in 13.4 stays as it is in styrman-crew: Go, Node.js, Python, Docker, `gh`, gcloud, kubectl, and OpenTofu.
10. **Answered.** The ingress and its Google sign-in pattern are copied from styrman-crew.
11. Review and Test are on by default, and E2E and Verification only when you ask. Is that the right split, or should all four be off until asked for?
12. **Answered (revisited in 2.01).** Crew started as an independent codebase (originally private under `kesokaj/crew`) and ships publicly under Apache-2.0 (`LICENSE`, `THIRD_PARTY.md`), replacing styrman-crew.
13. **Answered, changed in 0.26, in 0.30, and again in 0.42.** Crew runs `agy`, with room for other CLIs. The CLI, the model, and the effort for a role are settings (12.2, 12.3).
14. Should evals also run before your own edits to prompts are applied?
15. **Answered in 0.32, changed in 0.42.** The queue goes by the priority of the tickets, across projects. Among equal priorities the projects take turns (7.9). Since 0.42 only a priority the operator set is compared across projects; an agent's orders its own project's tickets.
16. **Answered.** Yes, when the evals are not worse. The release that carries it gives notice (10.5).
17. May the improver read full trajectories from every project, or only summaries and numbers?
18. Should a new baseline version reach every project at once (my default), or one project first?
19. When a project is deleted, should what Crew collected from it be deleted too? I delete it and keep the improvements already made.
20. **Answered.** Agents create subtasks on their own ticket through the Crew MCP, and subtasks that do not depend on each other run in parallel (7.5, 7.11).
21. Documentation lives in the project's repository on `main` and is shown in the app. Would you rather keep it apart from the code, in the database?
22. Should every project with a GitHub remote get a pull request per ticket, or only when you switch it on?
23. Should a task be allowed to reach Done with every optional stage off, on its command checks alone?
24. **Answered, then changed in 0.25.** Crew has a database, and it is the brain: everything Crew saves is in it.
25. **Answered.** You accept the risk with Antigravity's terms. It works well for you today, and `agy` stays on.
26. Is Model only the right meaning of "internet off", given that the agent's model is on the internet?
27. **Answered.** No signing keys. You want releases: each one is a tag and a release on GitHub with notes (16.9).
28. **Answered by 13, dropped in 0.26, back in 0.30, and dropped again in 0.42.** A review is kept independent by a fresh sandbox, a new conversation, and a model of another family (12.3).
29. **Answered by 13, then dropped in 0.26.** Crew does not choose a model by itself. The CLI and the model for a role are a setting (12.3). In 0.31 you decided that Crew recommends, and in 0.32 that it chooses automatically, with your own choice above it (question 89).
30. **Answered, then changed in 0.21, in 0.25, in 0.30, and in 0.79.** A sandbox gets the login of the one CLI it runs, the git identity, and a temporary GitHub token; since 0.79 no gcloud. The SSH key and `gh` stay with the server (12.6).
31. **Answered.** Cloud Hypervisor. Firecracker is not planned.
32. **Answered, then changed in 0.29.** Crew gives notice. A release of prompt text installs by itself after 24 hours, and a release that changes code waits for Install (16.3).
33. **Answered.** Not in full. Crew copies the sandbox, the runtime image, and the credential store. The terminals are described in 41 and 45, which replace what this answer first said.
34. **Answered, then dropped in 0.26.** It was about the login of `claude`.
35. **Answered.** The repository is created when the build starts. Only parts are copied, so it starts with its own history.
36. **Answered by 33.** The user handling is not copied. See question 10 for how you sign in.
37. **Answered.** styrman-harness is the base, and anything in it may be changed or dropped. The first cut is in 17.3. A project adds its own (12.9).
38. **Answered, then changed in 0.28.** Crew runs on a dedicated host; any prior styrman-crew installation is removed from it first (17.6).
39. **Answered.** The ingress is copied. The updater is designed anew (section 16).
40. **Answered, then changed by 45.** A terminal is for diagnostics and troubleshooting. An edit mode can push.
41. **Answered.** Yes: a project terminal with the git repository mounted, and a direct jump into the sandbox where an agent is working on a ticket.
42. For a local project, Crew serves the repository to you over HTTPS behind the sign-in. Do you also want SSH?
43. **Answered in part.** Bare metal with Docker, dedicated to Crew. Its size is read at Setup, and the limits follow from it.
44. **Answered, then changed by 46.** Releases go to GitHub. Backups stay on the machine to start with (16.9).
45. **Answered.** Read-only for diagnostics, plus an edit mode for going in and using it (13.11).
46. **Answered.** On the machine, as tar.gz files, to start with (16.9).
47. **Answered.** Yes, and private to start with.
48. **Answered by 32.**
49. **Answered.** There is no Google Workspace, so Google Chat is off for now. Notifications come by mail first. Notifications from Crew's web app are designed and switched off (18.2).
50. **Answered.** Your Google account with an app password. The alternatives are in 18.4.
51. **Answered.** Not needed now, nice to have later. It is not in the build plan.
52. **Answered.** From the internet, with a Google account or fully public, using the sign-in of the ingress (19.4).
53. **Answered.** Yes, a hosted project may reach the internet. What is exposed is protected as in styrman-crew: Google sign-in against a list of mail addresses (19.4).
54. **Answered.** Two presses is enough: the notification, then the alternative.
55. **Answered.** Yes. Your own tools use the Crew MCP with a personal token (11.7).
56. **Answered.** Yes. The system should be autonomous from you as much as possible, so subtasks run in parallel where they can (7.11).
57. **Answered by 53.**
58. **Answered, and changed in 0.21.** Mail first. The phone is skipped for now, and there is no mobile app. When the phone is taken up: iPhone first, then Android, from the web page.
59. **Answered.** Only the admin email by default.
60. **Answered with my recommendation.** New features and improvements go ahead after a notice. Limits, the project OK, sign-ins, protected releases, and since 0.27 a change to a file that is code for the CLI or for GitHub still wait for you (7.3).
61. **Answered with my recommendation.** Yes, as a switch per token, for approving or discarding a new feature and changing a recorded decision. The large decisions stay in the app (11.7).
62. **Answered.** Not today. Google Chat stays switched off.
63. **Answered.** An agent does not wait at all. It records the decision in Crew's database and carries on (7.5).
64. **Answered.** A ticket that passes its budget carries on, and the event is recorded (7.7).
65. **Answered.** A ticket works as kanban: it only moves to the right, or it is cancelled and replaced by other tickets (7.4, 7.7).
66. **Answered.** Nobody. If it is down, it is down.
67. **Answered.** No. `agy` is signed in with your personal Google account.
68. **Answered.** Reference host: 12 cores with 16 threads, 62 GiB, a 1 TB SSD, Linux, bare metal (measured in 0.33 by the spike, item 57).
69. **Answered with my recommendation.** An override is a change to one of Crew's prompts for one project only. It is applied by itself when the project's evals are not worse, and you are told (9.6). Since the audit of 0.27 an override of a checking role waits for you (9.6).
70. **Answered.** Yes. A project's own setup command that fails twice in a fresh sandbox gets a `fix` ticket, not a pause.
71. **Answered.** The phone is skipped for now. Mail first.
72. **Answered in 0.90.** Yes: Crew lists the package files a branch changes to the reviewer, as data, and a ticket that changes one is reviewed on its head before it merges, also when Analysis skipped Review (13.8, 7.4).
73. **Answered, then added to in 0.25.** Crew's own git traffic uses one SSH key and no token, and `gh` is signed in for what a key cannot do (6.6). Each sandbox gets a temporary token (77).
74. **Answered, then changed in 0.25 and in 0.79.** The credentials are kept in Crew. Every run sandbox gets the same set; since 0.79 it holds no gcloud, and no setting leaves anything out. Since 77 that set no longer holds the SSH key or `gh` (12.6).
75. **Answered, then changed in 0.26 and in 0.31.** 2 lead slots for each CLI. You raise them later (7.9).
76. **Answered.** Yes, `gh` is signed in.
77. **Answered with my recommendation.** Each sandbox gets a temporary GitHub token that can only read. The SSH key and the `gh` login stay with the server (12.6).
78. **Answered.** No. The backups stay on the primary host for now.
79. **Answered by your decision on trajectories.** They are kept for good, so each month that has ended is backed up once, and a day's file holds the rest (16.9).
80. **Answered.** Yes. A reviewer, a verifier, and a `judge:` check run on a model of another family than the builder (12.3).
81. **Answered.** Yes, if possible, or wait for the reset. A run moves to its agent's second choice in the other group, where the rule for checks allows it (12.7).
82. **Answered in part.** You want every model to be browsable and each agent to have its own. The Models screen does that (12.3). The defaults I chose are where each agent starts, and since 0.32 Crew chooses from there for the five agents it can measure, unless you set one yourself: Opus for the lead, the reviewer, the verifier, and the improver, and Gemini 4 for the builder.
83. **Answered.** It is the same machine. styrman-crew is removed from it, and only Crew runs there (17.6). Its real size is read in the spike (item 57).
84. **Answered.** No. A sandbox uses your own logins. Crew is an extension of the developer who runs it (section 2, rule 9).
85. **Answered with my recommendation.** Yes. Only changes to prompts and harness text install by themselves. Every change to code waits for Install (16.3).
86. **Answered.** Crew signs in the way styrman-crew does, with the token file of your Google sign-in, and prepares no other way (12.6).
87. **Answered in 0.30, dropped in 0.31, and reopened for public release in 2.01.** Crew is published under Apache-2.0 (`LICENSE`) with complete attribution in `THIRD_PARTY.md` (16.12).
88. **Answered in 0.30, changed in 0.31, and updated in 2.01.** Public open-source repository under Apache-2.0 (configured per installation via `CREW_SOURCE_REPO`, e.g. `your-org/crew`), while runtime images remain locally built or stored in each operator's private registry (16.12).
89. **Answered in 0.31, changed in 0.32.** Crew chooses the CLI and the model automatically for the agents whose work it can measure, and your own choice overrides it (12.3).
90. **Answered in 0.31.** 2 lead slots for each CLI, and each can be changed (7.9).
91. **Answered in 0.31.** At a later stage. The adapters are the preparation, and nothing else is built for another CLI now (12.2).
92. **Answered in 0.31.** Yes: the longest-lived sign-in each CLI has. For `agy` it is the token file, which renews itself (12.6).
93. **Answered in 0.31.** You confirmed that each CLI runs signed in as you, and that this is what you want. The split stays where every agent starts. Since 0.32 Crew chooses from there by itself, for the agents it can measure (12.3).
94. **Answered in 0.32 with my default.** One primary host builds Crew from a checkout; other installations get the built release images and deploy bundle from that operator's private registry, because everything is Docker (16.12).
95. **Answered in 0.32.** You said that everything runs in Docker. So a client is another machine with Docker, and every installation is the same. Each is signed in in its own Setup, and whose accounts those are is yours to decide for that machine (12.6).
96. **Answered in 0.32.** Yes. The choice is automatic: one run in ten of an agent tries another entry, and Crew changes by itself. You can still start a comparison (12.3).
97. **Answered in 0.32, updated in 2.01.** Outbound Apache-2.0 `LICENSE` and `THIRD_PARTY.md` ship in the public source repository; built runtime images stay operator-private (16.12).
98. **Answered in 0.32.** In planning, the first message of a plan gets a proposal by itself. Everywhere else you select text and choose Improve text or Review text (7.2).
99. **Answered in 0.32.** It waits in the queue, and the queue goes by the tickets' priorities (7.9).
100. **Answered in 0.41.** Yes: the reviewer and the verifier get `agy` with `gemini-3.8-flash` as their last entry, which the judge already had, so a Sonnet build is checked by another family (12.3).
101. **Answered in 0.33 with my recommendation.** The sizes and `cpu_overcommit: 2.0` stay, so about 10 sandboxes fit on a 16-thread / 64 GB host. A working run used about a tenth of its memory and little CPU, but the subscription bounds the work long before the machine does: `agy` carries about one run (13.10, section 24, items 26 and 35).
