## 24. To verify in the spike (M0)

These are things the documentation and the two repositories did not settle. They are about `agy`, the one CLI Crew runs since 0.42 (12.2). Each one affects a design choice above.

**M0 changes nothing on the reference host.** The spike reads, measures, and starts throwaway containers of its own from styrman-crew's runtime image. It does not stop, restart, or reconfigure styrman-crew, its projects, Docker, or Kata. A question that needs that is answered in M1. Four M0 items can still disturb what runs there, and each runs only after your yes, late in the spike: items 2 and 17 let several copies renew one sign-in of `agy`, which can sign styrman-crew out, and items 6 and 26 fill the machine with microVMs, so they stop at a floor of free memory.

**What happened in 0.33.** styrman-crew was uninstalled from the reference host on 2026-10-12, before M0 began. With your approval the spike ran against a stand-in of styrman-crew's runtime image built from `5c9e6fb`, with the logins already present there. Its answers, with the commands and their output, are in `spike/answers.md`.

**When each question is answered.** M0 runs beside what is there. Its scripts are in `spike/`, and its tests run in throwaway containers of its own, which it names `crew-spike-…` and starts from styrman-crew's runtime image. It reads from styrman-crew's own sandboxes only what the opening check needs: that `agy` is signed in. It answers the questions that M1 to M3 rest on. A question that needs a part of Crew is the first task of the milestone that builds that part.

| Answered in | Items |
| --- | --- |
| M0, the spike | 1, 2, 3, 5 to 9, 10 with a small stand-in for the Crew MCP, 11 to 15, 17, 18, 26 to 28, 33, 35, 37 to 39, 41, 45, 47 to 54, 56 to 58, 60 to 63, 78, and 79 (items 65 to 77 and 80 were retired in 0.42). Items 5 and 16 are answered with styrman-crew's image as a stand-in, and again in M3 with Crew's own, when the first run exists. Of item 13 only the advisories are read in M0. |
| M1, on the test virtual machine | 34, 64, and the Kata 4 half of item 13. The machine starts with an empty Docker, which is what item 64 measures. |
| M1, on the primary host | 16, when the image is first built with Crew's own build target for the harness, and 29, when Crew's firewall exists. Both moved from M0 in 0.33: styrman-crew's firewall was gone before the spike began. |
| M1, on the primary host, after styrman-crew is removed | 59. It restarts Docker, so it waits until nothing else runs there. |
| M2 | 32, 42 |
| M3 | 36, 40, 43, 44, 46 |
| M9 | 24, 25 |
| M10 | 4 |
| M14 | 19, 20, 31 |
| M18 | 22, and 21 when the phone is taken up |
| M19 | 23, 30, 55 |
| M20 | 81 |

**If the answer is no.** These answers could undo a choice that M1 to M3 are built on. Each has a way out that is named here and built only if it is needed.

| Item | If the spike says no | What Crew does then |
| --- | --- | --- |
| 2, 17 | The current `agy` reads the sign-in only from a keyring, or Google ends a login that several copies renewed | Crew puts the sign-in into a keyring in the sandbox, as styrman-crew did before it changed to the file. For renewing: only the login check renews, each sandbox gets the newest file at its start, and no run writes one back. |
| 1, 35 | The plan carries fewer runs at once than ten | The number of runs at once becomes what the spike found, and the lead slots follow it |
| 7 | `agy` updates itself in a sandbox in spite of the setting | Its files in the image belong to root and cannot be written by the user `agent` |
| 10 | `agy` cannot use an MCP server over HTTP with a bearer header | A small program in the image speaks MCP to `agy` on standard input and passes the calls to the server |
| 79 | A rewrite takes longer than you want to wait | The prompt writer's sandbox is kept warm while a plan draft is open, or the rewrite is shown after the message was sent, as a proposal for the next one (7.2) |
| 12, 53 | Reading the spool through the shared folder is late or loses lines | `crew-run` also sends each line to the server's door for sandboxes. The spool stays as what is read again after a restart. |
| 56 | The account of a login file cannot be learned | No run writes a login back, so a file for another account cannot get in that way |
| 59 | A detached command does not outlive a restart of the server or of Docker | Such a run ends with the reason `restart` and continues its conversation in a new sandbox, which 12.10 already has |
| 13 | Cloud Hypervisor under Docker does not work on Kata 4 | Crew stays on Kata 3.31 and watches its advisories. M1 found that it works, with `ConfigPath`; since 0.68 Crew runs Kata 4.3.1 (13.2) |

1. Does `agy -p` work for hours in ten microVMs at once on one subscription, and what does it print and return when the five-hour limit is reached?
2. Does `agy -p` stay signed in when ten short-lived microVMs each get a copy of the same token file and some of them renew it? Does keeping the newest file from each finished run (12.6) remove the need to sign in again, or does the scheduled login check keep it fresh by itself? The vendor says the sign-in is in the keyring: does the token file styrman-crew copies today still work on the current version?
3. Which hosts does `agy` contact during a run? The Model only allowlist needs the list. Does `agy` honour `HTTPS_PROXY`?
4. Does Model only mode work for `agy` through a proxy that lets through only the hosts found in item 3?
5. How long does a run microVM take from start to the first event, with the harness already in the image's home skeleton?
6. How many run microVMs fit on the machine at the sizes in 14.1, with the Docker daemon on and off?
7. **Answered by the vendor's documentation: no.** There is no numbered download. A release records the version its image got, and the image is kept (12.8). To confirm: `AGY_CLI_DISABLE_AUTO_UPDATE=true` stops it from updating in a sandbox. **Answered again in 0.56: yes.** The install script reads a release manifest with the version, a versioned tarball, and its sha512, and `versions.lock` pins those (12.8).
8. Does `--conversation` continue a conversation in the same microVM after the first call has ended, and in a new microVM whose home got the CLI's files back from the database?
9. Does the `agy` settings file with `command(*)` avoid every refused tool, as the guest user?
10. Does the Crew MCP server over HTTP with a bearer header work in `agy`, and does a failed connection stop the run or only remove the tools?
11. What is the cheapest way to check an `agy` login without starting a run, and what does a login look like that is present but no longer accepted?
12. Is reading `events.jsonl` through the shared directory reliable while the CLI is writing it, and how late do lines arrive?
13. Which of Kata's advisories from 2026 touch 3.31.0 with Cloud Hypervisor? Does Cloud Hypervisor under Docker work on Kata 4, whose release notes say Docker is tested with QEMU only, or must Crew stay on Kata 3.31 for now? Answered: the advisories in M0, Kata 4 in M1, yes with `ConfigPath` (`spike/answers.md`); read again in M17, when Crew moved to 4.3.1 (13.2, section 26, From 0.69 to 0.70).
14. How large is a trajectory for a typical one-hour run of `agy`?
15. **Answered by the vendor's documentation, to confirm.** `step_update` carries `subagent_info` with each subagent's own conversation id, so a helper can be recorded as a helper subtask. Confirmed in M0 on `agy` 1.3.4 (`spike/answers.md`, item 15; since 1.21).
16. Does the harness, built for `agy` when the image is built, give the same result as `sh-agent install` gives today?
17. How often does `agy` need a new sign-in when many sandboxes share one login, and does keeping the renewed login (item 2) prevent it?
18. Does a terminal inside a running run's microVM disturb the CLI, and is a tmux session enough to save what was typed and printed?
19. Can two versions of the server run side by side during the check without touching the same sandboxes, and how long do the dump of the database, its restore into the scratch copy, and the smoke run take?
20. How large is a release folder with its five images, and how much disk do five kept releases need?
21. Later, when the phone is taken up. On your iPhone, with Crew added to the Home Screen: do notifications arrive, does pressing one open the right item, and how long does the device keep getting them? Then the same on Android.
22. Does mail sent through a private Google account with an app password arrive from the machine, and what does a refused login look like? Accepted as not shown since 1.17 (section 23): it waits for the operator's mail account.
23. Can the machine be reached from the internet on ports 80 and 443 at the apps domain, and does the wildcard certificate arrive through the DNS provider? Accepted as not shown since 1.17 (section 23): it waits for the operator's domain.
24. Do three subtasks of one ticket, each on its own branch, join cleanly on a real repository, and how often does a join need a builder?
25. Does an agent on your laptop reach the Crew MCP through the ingress with a personal token?
26. On the reference 64 GB host: how much memory and CPU does a run of each role really use, how many sandboxes fit, and how long does a project's setup take in a fresh sandbox?
27. What does an `agy` login grant beyond the CLI? Can it be used against other services of the same Google account?
28. **Partly answered by the vendor's documentation.** The list is in 13.8. To confirm it on the current version, and to find out: can a run be told not to load them from the workspace? Answered in M0, and again in M5 and M6, on `agy` 1.3.4, and confirmed again on `agy` 1.4.12 and `agy` 1.5.3: the list holds, with `.agents/plugins/` and `.agents/plugins.json` added in M6, and neither a flag nor leaving the workspace out of `trustedWorkspaces` keeps them out, so `crew-run` takes them out of the workspace of a run that does not build (13.8; `spike/answers.md`, items 28 and 41). The reference host ran 1.5.3: `spike/agy-load-check.sh` was run again on 1.4.12 and 1.5.3 with the same result (`paths`, `midturn`, `nextturn`), and is run again at the next raise of `agy` (since 1.21, updated in 1.29 and 1.40).
29. Is a sandbox without any IPv6 route, and do the IPv4 rules hold, tested from inside a sandbox?
30. Can a hosted project under the apps domain read or set a cookie of Crew's own address?
31. How long does a restore take on an empty machine, from the release on GitHub and a backup file?
32. Does `gh auth login` in the setup microVM work together with git over SSH? May that login push images to GitHub's registry?
33. Where does `agy` read skills and context when it starts, and can `crew-run` put a project's `.agents/` there? The vendor's documentation puts the CLI's global skills in `~/.gemini/antigravity-cli/skills/`, and rules, agents, and the MCP configuration in `~/.gemini/config/`. styrman-harness puts all of it under `~/.gemini/config/`.
34. Does the host setup run on a current Ubuntu LTS as it does on Debian 13, with the same Docker and Kata versions?
35. How many lead runs can the `agy` subscription carry at once before it reaches its limit? This sets the lead slots.
36. Dropped in 0.26, back in 0.30, and retired in 0.42: Crew runs `agy` alone (12.2).
37. Does `agy -p --conversation <id>` still continue a conversation after `agy` was updated to a newer version?
38. Where does `agy` keep a conversation on disk, and does `agy -p --conversation <id>` continue it from a new sandbox whose home got those files back?
39. **Partly answered by the vendor's documentation.** In a session with several turns, the usage adds up over the turns. Still to try: does the process stay open on a subscription, and is one open process worth it over a new call for each turn (12.10)? Answered in M0 on `agy` 1.3.4: the usage adds up, the process stays open on the subscription, and a second turn in it answers in 2.4 seconds against 4 to 13 for a new call; 12.10 keeps one call per turn in the first version (`spike/answers.md`, the two entries of item 39; since 1.21).
40. How much does a conversation lose when it is rebuilt from the turns instead of continued by the CLI? Measured on the fixture project.
41. **Answered by the vendor's documentation: yes.** `.agents/hooks.json` and `.agents/mcp_config.json` are loaded from the workspace, and no step asks first. Crew keeps them to what is on `main` and never merges a change to them by itself (13.8). To confirm on the current version. Confirmed in M0, and again in M5, on `agy` 1.3.4, and again on `agy` 1.4.12 and `agy` 1.5.3, as item 28 says (`spike/answers.md`, items 28 and 41; since 1.21, updated in 1.29 and 1.40).
42. Does a one-hour installation token of a GitHub App, with read-only contents, work for `git` over HTTPS and for `gh` in a sandbox? Can Crew revoke it when the sandbox ends?
43. How many events and how many bytes does one run write to the database, and how fast does the database grow on a primary host when trajectories are kept for good?
44. How long do a dump and a restore of the database take at that size? This is the time of step 3 of an update, and the size of a day's backup.
45. Which files does `agy` need back in its home to continue a conversation, and how large are they?
46. How much memory does the database need while 16 sandboxes write events at once?
47. What does `agy models` print on your account, headless, in a sandbox? Is the output stable enough to read, since no JSON form is documented? Does every name end in its effort?
48. Does `--effort` change anything when the name already holds an effort? Crew does not use it, and this decides whether it ever should.
49. Do the Claude models inside `agy` behave the same as the Gemini models in a headless run: the same events, the tools, the Crew MCP, and continuing a conversation?
50. Does `agy -p "/usage"` work headless in a sandbox, in its JSON form, and does it give what is left and the reset time for each group? When a limit is reached, what does the error say?
51. Can a conversation that started on one model be continued on another, for example when a pin changes between two fix rounds?
52. How does `agy` take its prompt in a headless run today: on standard input with `--input-format stream-json`, as styrman-harness does, or as an argument, as the vendor's examples show? Record every quirk in 12.2 again on the current version.
53. Can the CLI's output be captured outside the sandbox, for example as the container's own output, so that an agent cannot change what was already recorded?
54. With paid credits switched off, does a run stop at its limit and spend nothing?
55. Can the ingress reach a hosted sandbox on a bridge of its own kind, while the machine stays out of reach from that sandbox?
56. What does a login-check sandbox learn about the Google account of a login file, so that a file for another account can be refused?
57. What did the reference host really have: how many cores or threads, and how much memory? Prior notes said 16 threads and 62 GB (verified in `spike/answers.md`).
58. When a run moves to a model of the other family at a limit, does `agy` continue its conversation, and what does a rebuilt one lose?
59. Does a command that was started detached in a microVM keep running, and can its end be read, after the server's container restarts and after Docker restarts with live restore?
60. What can bound the size of a run's whole home and workspace? In styrman-crew only the disk of the sandbox's own Docker has a size.
61. Which files does a fresh home need besides the token file, so that headless `agy` starts with no first-run question and no question about trusting the folder?
62. Which secrets and ids are in what `agy` prints: tokens, account ids, the mail address? The scrub script is written from this (16.12). In 2.01, `tools/check-clean.py` also enforces personal email domain and workstation home-path hygiene even without `spike/raw/scrub-values.txt`.
63. What does the reference host run today: the versions of Docker, Kata, and the kernel, and Docker's settings file? `versions.lock` starts from it.
64. How long does it take, and how much disk, to build every image on a machine with an empty Docker? This is what `install --build` costs.
65. Retired in 0.42: Crew runs `agy` alone (12.2).
66. Retired in 0.42: Crew runs `agy` alone (12.2).
67. Retired in 0.42: Crew runs `agy` alone (12.2).
68. Retired in 0.42: Crew runs `agy` alone (12.2).
69. Retired in 0.42: Crew runs `agy` alone (12.2).
70. Retired in 0.42: Crew runs `agy` alone (12.2).
71. Retired in 0.42: Crew runs `agy` alone (12.2).
72. Retired in 0.42: Crew runs `agy` alone (12.2).
73. Retired in 0.42: Crew runs `agy` alone (12.2).
74. Retired in 0.42: Crew runs `agy` alone (12.2).
75. Retired in 0.42: Crew runs `agy` alone (12.2).
76. Retired in 0.42: Crew runs `agy` alone (12.2).
77. Retired in 0.42: Crew runs `agy` alone (12.2).
78. How long does a sign-in of `agy` last when it is used every day: what is known to end it, and does the daily login check with one real request keep it alive? Does `agy` have a longer-lived sign-in for a consumer plan than the token file?
79. How long does one call with no tools take on `agy`, from the request to the answer, with the start of its sandbox? This is what you wait for a rewrite of a prompt (7.2).
80. Retired in 0.42: Crew runs `agy` alone (12.2).
81. How long does a turn in the chat of a ticket take, from the question to the answer, when its sandbox has to start? Does a small sandbox do for most questions, so that the large one is needed only to run the code (11.9)?
