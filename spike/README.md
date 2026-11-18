# The spike (milestone M0 — historical record)

> Historical record from milestone M0, kept in the public Apache-2.0 repository so the design's empirical findings (`spike/answers.md` and `docs/plan/spike-checklist.md`) remain reproducible and verifiable. References to `styrman-crew` below reflect the operator's predecessor host setup before Crew replaced it.

The spike answers what the design could not settle from documentation. Its questions are in `docs/plan/spike-checklist.md`. The first table there says which questions belong to M0.

## What goes where

- A script per question, or per group of questions, goes here.
- Every answer goes into `answers.md`, with the command that was run and the lines of its output that show the answer, scrubbed.
- Raw recordings go into `raw/`, which git ignores. A recording reaches `testdata/agy/` only through the scrub script, which is the first thing to write.
- `spike/` stays in the repository. It shows how each answer was found.

## Rules of the spike

These hold in every session of M0. The prompt that starts the spike says the same.

1. **Nothing on the machine is changed.** styrman-crew, its projects, its containers, Docker, Kata, and the firewall stay as they are. Nothing is stopped, restarted, removed, or reconfigured.
2. **The spike's own containers** are started from styrman-crew's runtime image with the Kata runtime. Each container and each Docker network is named `crew-spike-…`, carries none of styrman-crew's labels, and is removed when its test ends.
3. **Paid credits are off.** In every such container, `useG1Credits` of `agy` is written as off before the first run. The operator confirms that paid extra use is off in both accounts before the first run.
4. **Logins and tokens** are put into `raw/` by the operator, never by a paste into the chat. `raw/` has mode 0700. No login and no token is printed or read into the agent's conversation. They are copied by path, and removed when the spike ends. The report lists the tokens to revoke.
5. **The scrub script holds no secret.** It reads the values to remove from `raw/scrub-values.txt`, one on each line, which the operator fills in: tokens, account ids, and mail addresses. It also removes what has the shape of a token. Its check runs over the staged files before every commit, and a commit that fails it is not made.
6. **The operator says yes first** to every test that runs many agent runs at once, runs for an hour or more, or can run a subscription to its limit: items 1, 2, 14, 17, 26, 35, 50, 54, 67, 68, 72, and 78.
7. **Items 2 and 17** can sign the operator out of `agy` in styrman-crew. They run late, and with a fresh sign-in made in a throwaway container where that is possible.
8. **Items 6 and 26** fill the machine with microVMs. They need a yes, and stop at a floor of free memory of at least 16 GB.
9. **Tests that can reach a limit come last,** while the operator is present, after every answer and a draft of the report are committed and pushed. A limit test the operator refuses is written as "not tested".
10. **No real `ANTHROPIC_API_KEY` is ever set.** The quirk of item 70 is taken from the vendor's documentation, or tested with a made-up value in a throwaway container.
