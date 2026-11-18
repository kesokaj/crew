## 14. Settings

### 14.1 Two sets of settings

| Settings | Scope | Where |
| --- | --- | --- |
| The machine's settings | The machine | The database |
| A project's settings | One project | The database |

The listings below show every setting, as `crew-ctl settings export` prints them. The names `crew.yaml` and `board.yaml` are the names of those two exports. No such file is read while Crew runs. Since 0.53 the settings of a budget, a price, and a cost are gone; `crew-ctl settings import` of a file that still holds one, and a project's import, take the key out.

`crew.yaml`:

```yaml
version: 1
max_concurrent_runs: 8          # agent runs working at once across all projects (7.9, R53). Halved while a group of models is close to its limit
max_active_tickets: 12          # tickets with active runs at once across all projects (7.9, R52)
runtime_image: crew-runtime:<version>   # set by the release, never a moving tag
sandbox_sizes:                  # Kata adds these to its base of 1 virtual CPU and 2 GB
  small: {cpus: 1, memory_gb: 0, docker: false, disk_gb: 20}   # lead, reviewer, verifier: 2 virtual CPUs and 2 GB in all. In styrman-crew's code 0 means the whole machine; Crew's orchestrator reads it as the base only
  large: {cpus: 3, memory_gb: 4, docker: true, disk_gb: 20}    # builder, tester, e2e, a conversation, and the chat of a ticket: 4 virtual CPUs and 6 GB in all
  build: {cpus: 3, memory_gb: 6, docker: true, disk_gb: 20}    # the build of a release and the gate's make check: 4 virtual CPUs and 8 GB in all, since the export of the runtime image needs more than 4 GB
machine_reserve:
  cpus: 2
  memory_gb: 8                  # the core services' ceilings: the database 2, the ingress 1, the server 4 with its pushes and fetches, and 1 for the rest (13.10, since 1.08)
cpu_overcommit: 2.0             # CPU allowances may add up to this many times the cores; memory is never overcommitted
# Crew's address, the apps domain, the admin email, and the time zone are not settings. They are in /etc/crew/crew.env, changed with crew-ctl edit, and shown in the app (16.11)
disk:
  clean_at: 85                  # percent full: remove what retention allows
  stop_at: 95                   # percent full: start no new run
machine_care:
  restart_hours: "02:00-05:00"  # when the machine may restart for security updates, in CREW_TIMEZONE; after 7 days of waiting Crew holds new runs and restarts once the runs finished (16.10)
sessions:
  days: 30                      # 1 to 30: a sign-in older is over; the ingress's token lasts 30 days
  fresh_minutes: 15             # 1 to 15: for terminals, secrets, tokens, release actions, and the settings listed in 20.3
mcp_tokens:
  days: 90
  calls_per_minute: 60
conversations:
  idle_minutes: 30              # 5 to 240: a conversation's sandbox is removed after this long without a message
  max_sandboxes: 3              # 1 to 20: sandboxes of conversations and of ticket chats at once; the one idle longest goes first
  keep_cli_files_days: 30       # the CLI's own files are kept this long after the last turn
  summary_every_turns: 20       # 5 to 100: Crew's own summary, used when a conversation is rebuilt (12.10)
network:
  mode: internet                # internet | model_only | lan; a project can set its own
  model_only_hosts:             # what a sandbox may reach in model_only mode, on port 443 through the server's proxy: what agy cannot do without, measured in M10 (spike item 4)
    - daily-cloudcode-pa.googleapis.com   # the model
    - aicode.googleapis.com
    - www.googleapis.com                  # the account's userinfo, which agy checks before a turn
    - lh3.googleusercontent.com           # the account's picture, which the same check reads
    - oauth2.googleapis.com               # the login's renewal
sandbox:
  suspend_after_minutes: 5      # 1 to 240: a project's warm microVM is paused (ContainerPause, 0% CPU) after this long with 0 active sessions (13.3, since 1.37)
  stop_after_minutes: 30        # 1 to 1440: a paused project microVM is stopped (0 MB RAM on persistent home) after this long idle
  max_snapshots: 5              # 1 to 50: home snapshots kept per project
  sources:                      # what the build of a release may reach in sources mode, on port 443 through the server's proxy (13.6, since 0.51): the package sources and image registries the Dockerfiles read. The first build on the machine measures it, as M10 measured model_only_hosts
    - proxy.golang.org                    # Go's modules, which a Sources sandbox gets through the server's module proxy; the server follows its redirects into storage.googleapis.com/proxy-golang-org-prod/, which is not on this list (13.6, since 0.61)
    - sum.golang.org                      # and their checksums
    - registry.npmjs.org                  # the UI's packages, and the browser tools of the runtime image
    - deb.debian.org                      # Debian's packages
    - registry-1.docker.io                # the base images; the Dockerfiles name them by their hashes
    - auth.docker.io
    - production.cloudflare.docker.com
    - production.cloudfront.docker.com    # Docker Hub serves the layers from either; a probe of a check sandbox in Sources met this one (0.54)
    - download.docker.com                 # Docker in the runtime image and the updater
    - cli.github.com                      # gh
    - packages.cloud.google.com           # gcloud and kubectl
    - go.dev                              # Go and Node in the runtime image; the Dockerfile checks their checksums
    - dl.google.com
    - nodejs.org
    - github.com                          # tofu, yq, hadolint; the Dockerfile checks their checksums. The proxy checks no hash, and these hosts also take uploads (13.6)
    - objects.githubusercontent.com
    - release-assets.githubusercontent.com
agents:
  clis: [agy]                   # the CLIs this installation uses: agy is the only one since 0.42. A CLI that is not signed in is switched off (12.2)
  roles:                        # what each agent runs on, on this machine, as CLI/model, in order. Install seeds it from the baseline; Crew syncs automatic roles from offered models by (family, bracket) and empirical results while keeping Set by you, your edits, and your removals untouched, and no improvement touches it. The first entry that can run is used. The next one is the choice at a limit or a lost sign-in (12.3)
    curator:  {model: [agy/gemini-4-carbon, agy/gemini-4-argon, agy/gemini-3.8-flash], effort: high}
    planner:  {model: [agy/gemini-4-carbon, agy/gemini-4-argon, agy/gemini-3.8-flash], effort: high}    # the planning agent
    assistant: {model: [agy/gemini-4-carbon, agy/gemini-4-argon, agy/gemini-3.8-flash], effort: high}   # conversations, and the chat of a ticket (11.8, 11.9)
    builder:  {model: [agy/gemini-4-carbon, agy/gemini-4-argon, agy/gemini-3.8-flash], effort: high}
    reviewer: {model: [agy/claude-opus-5-5, agy/gemini-4-carbon, agy/gemini-4-argon, agy/claude-sonnet-5-5, agy/gemini-3.8-flash], effort: high}
    tester:   {model: [agy/gemini-3.8-flash, agy/gemini-4-carbon, agy/gemini-4-argon], effort: medium}
    e2e:      {model: [agy/gemini-3.8-flash, agy/gemini-4-carbon, agy/gemini-4-argon], effort: medium}
    verifier: {model: [agy/claude-opus-5-5, agy/gemini-4-carbon, agy/gemini-4-argon, agy/gemini-3.8-flash], effort: high}
    summary:  {model: [agy/gemini-3.8-flash, agy/gemini-4-carbon, agy/gemini-4-argon], effort: low}
    prompt:   {model: [agy/gemini-3.8-flash, agy/gemini-4-carbon, agy/gemini-4-argon], effort: medium}   # the prompt writer (7.2)
    judge:    {model: [agy/claude-sonnet-5-5, agy/gemini-3.8-flash, agy/gemini-4-carbon, agy/gemini-4-argon], effort: low}   # for `judge:` checks
    improver: {model: [agy/gemini-4-carbon, agy/gemini-4-argon, agy/gemini-3.8-flash], effort: high}
  brackets:                     # models of the same kind across limit groups (12.3, 12.7). At a limit or slow_down_at, work moves within the entry's bracket to a group with room when allowed by the model rule
    pro:   [agy/gemini-4-carbon, agy/gemini-4-argon, agy/claude-opus-5-5]
    flash: [agy/gemini-3.8-flash, agy/claude-sonnet-5-5]
    oss:   [agy/gpt-oss-120b]
  extra_models: {agy: []}       # names you added, by CLI, beside the ones a CLI lists, such as a full name to stay on one version (12.3)
  auto:                         # Crew chooses the CLI and the model for the builder, the reviewer, the tester, the E2E tester, and the verifier (12.3)
    on: true                    # false: every agent is Set by you, and Crew only recommends
    set_by_you: []              # the agents you set yourself in the Models screen. Crew leaves them alone
    explore_share: 0.1          # one run in ten of an automatic agent tries another entry of its list
    min_runs: 20                # runs of an agent on an entry before its numbers are compared
    min_days_between: 7         # at most one automatic change for an agent in this many days
    watch_runs: 20              # after a change Crew watches this many runs, and goes back if they are worse
    retry_days: 30              # an entry Crew went back from is not tried again for this long
    hide_days: 30               # how long Not now hides a recommendation
    compare_runs: 20            # a comparison you start ends when each entry has this many runs
    better_points: 8            # better: this many points more passed the first time
    same_points: 5              # an agent that checks: its share of runs that asked for a fix is within this many points (12.3)
    window_days: 90             # the numbers are taken over this many days
  checks_use_another_family: true   # a reviewer, a verifier, and a judge run on another family than the builder when that family has room; when it is at its limit, they fall back to a model of the same bracket in the builder's family that has room, with a notice (12.3)
  at_limit: move                # move: at a limit or slow_down_at or a lost sign-in a run takes a model of the same bracket in another group with room when allowed by the model rule, or the agent's next entry that can run (and for a check, falls back to the builder's family with a notice when the separate family is at its limit) | wait (12.7)
  models_refresh: daily         # also after a sign-in, after a new version of a CLI, and from the Models screen
planning:
  rewrite: suggest              # suggest: Crew proposes a better wording of your first message in a plan draft (7.2) | off. Improve text and Review text on a selection work either way
  rewrite_timeout_seconds: 45   # after this your own text is sent as it is

logins:
  check_every_minutes: 30
image_updates: daily            # look for new versions of the CLIs and the tools, as tickets on Crew's own board
quota:
  slow_down_at: 0.8             # move automatic work to a model of the same bracket in another group with room when allowed by the model rule at or above this share of its group's limit (12.7)
updates:
  auto_install: text            # text: only a release that changes nothing but prompts and harness text installs by itself | unprotected: every release that touches no protected part (the rule before 0.29) | never; another installation, whose source_repo names no repository, takes never (16.12)
  install_after_hours: 24       # such a release installs by itself this long after the notice; 0 = at once
  watch_minutes: 10             # switch back by itself if the new version turns unhealthy in this time
  keep_versions: 5
  install_test: local           # the gate's install test: local = on a virtual machine that is started on this machine | off (16.11)
  release_after_tickets: 5      # cut a release when this many tickets have merged on Crew's own board, or when asked
  source_repo: <owner>/crew     # the repository this installation builds from and pushes to. Asked for in Setup, where a push of an empty branch checks it. CREW_SOURCE_REPO in the install file is its first value (16.11)
  publish: github               # github: tag, release with notes, and images in the configured registry (kept private by default for runtime images, 16.9) | off: none are copied
  backup_path: /var/lib/crew/backups   # one tar.gz per day, and one file for the events of each ended month; on the machine itself for now
  backup_keep: 14
  backup_copy_to: null          # null: backups stay on the machine, as you decided. Or a place each day's file is also copied to
notifications:
  web_push: false               # later, with the phone
  mail_to: you@example.com      # the admin email of the install file, until you change it here
  mail_from_name: Crew
  mail_smtp: smtp.gmail.com:587 # the account and its app password are with the credentials, not here
  chat_webhook: none            # Google Chat needs a Google Workspace account
  needs_you: [mail]             # one mail for each item
  news: digest                  # one mail a day; four kinds are sent at once (18.1)
  remind_after_hours: 4
  quiet_hours: "22:00-07:00"    # news waits, decisions are still sent
hosting:
  # a hosted project gets <project>.<CREW_APPS_DOMAIN>, from the install file (16.11)
  keep_images: 3
  default_access: private       # private (only the admin email) | listed | google | public
github:
  owner: <account or organisation>   # where new repositories are created
  visibility: private
  # the SSH key, the gh login, and the key of the GitHub App are with the credentials, not here
keep_trajectories_days: null    # null: for good. A number removes trajectories older than that, and only when feedback was run and eval cases were made; none is marked so yet (8.3), so a number removes nothing
improvements:
  approval: auto                # auto (ship when the evals are not worse) | operator (wait for you)
  run_every_days: 7
  run_after_tasks: 25           # or sooner, after this many finished tasks across all projects
  measure_after_tasks: 20       # finished tasks on a new version before its effect is measured: an improvement's, and a release's (16.8, since 0.57)
  crew_project: null            # the project that holds Crew's source (16.2), made by Make Crew's project; its remote must be updates.source_repo. A passed improvement becomes a ticket there
keep_cli_files_days: 7          # a run's conversation can be continued by its CLI for this long (12.10)
terminal:
  idle_minutes: 30              # a project terminal is removed after this long without input
  edit_mode: true               # the operator may switch a project terminal to edit mode
```

`board.yaml`:

```yaml
version: 1
project: example
key: EX                        # the prefix of ticket numbers: EX-1, EX-2
planning:
  rewrite: inherit             # kept for later use: since 0.32 a proposal by itself is made only for a plan draft, which has no project (7.2)
hosting:                       # how this project is hosted on the machine (section 19)
  dockerfile: Dockerfile       # or compose: compose.yaml
  port: 8080
  health: /healthz
  data: /data                  # where the data directory is mounted
  size: small
  access: private              # private | listed | google | public
  internet: true               # the hosted project may reach the internet from inside
  allowed: []                  # the Google mail addresses for access: listed
  hostname: null               # an own domain, beside <project>.<apps domain>
  public_rps: 20               # requests per second from one address, for access: public
  log_days: 14
  data_copies: 3               # copies of the data directory kept, one made before each deploy
  follow_main: false           # true: once a ticket reaches Done, deploy main's head, one deploy at a time
approvals:
  project: operator            # the plan and its breakdown
  created_task: agent          # tasks agents create that fit the plan, and fixes
  new_feature: notice          # notice (go ahead after new_feature_wait_hours) | operator | agent
  new_feature_wait_hours: 24
  agent_change: auto           # overrides the curator proposes: auto (applied when the evals are not worse; a checking role always waits) | operator
  feature_specs: autonomous    # autonomous (@planner decides within 000-foundation.md and 13.8) | manual
limits:
  max_active_tickets: 4        # tickets of this project with active runs at once (7.9, R52)
  run_timeout_minutes: 60
  run_max_turns: 200
  max_goal_rounds: 5           # check-and-continue rounds inside one run
  check_timeout_minutes: 30    # a check that runs longer ends as an error, not as a failure (13.9)
  max_fix_rounds: 3            # fix rounds on one ticket before the curator looks at it again
  max_ticket_failures: 6       # fix rounds and stopped subtasks on one ticket since its last hold release before Crew holds it (7.7)
  max_added_share: 0.25        # work agents may add, as a share of the tickets of the approved plan
  max_added_tickets: 10        # open tickets agents may add on a project with no plan tickets (0 disables; 7.7)
  max_added_subtasks: 6        # create_subtask calls on one ticket (7.5); the backend's join subtasks do not count
  max_parallel_subtasks: 3     # subtasks of one ticket that may run at once (7.11)
watchdog:
  silent_warn_minutes: 5
  silent_stop_minutes: 15
  repeat_fail_warn: 3
  repeat_fail_stop: 6
  slow_stage_factor: 3
agents:                        # this project's own model, with its CLI, or effort for a role; empty means the machine's entry, which is Crew's choice or yours (12.3). A ticket can pin again.
  builder: {effort: low}       # an example: this project's builders think less
  reviewer: {model: agy/claude-opus-5-5}   # an example: this project's reviews run on Claude Opus
  # checks_use_another_family: false     # only when this project should allow a check on the builder's family
  # at_limit: wait                         # only when this project's runs should wait for their own group at a limit (12.7)

sandbox:
  cpus: 6                       # virtual CPUs for the project's warm microVM (crew-proj-<id>): 2 to 32 (13.3, 13.10)
  memory_gb: 8                  # GiB of memory for the project's warm microVM: 2 to 64
  disk_gb: 20                   # GiB of disk budget for the project's persistent /home/agent: 5 to 500
  suspend_after_minutes: null   # idle minutes before ContainerPause (0% CPU); null uses crew.yaml (1 to 240)
  stop_after_minutes: null      # idle minutes before stopping to 0 MB RAM; null uses crew.yaml (1 to 1440)
  max_snapshots: null           # snapshots of /home/agent kept; null uses crew.yaml (1 to 50)
  setup: "make setup"
  test: "make test"            # the project's test command, run before a merge
  network: inherit             # inherit | internet | model_only | lan
  check_network: inherit       # the mode of its check sandboxes: inherit (the project's own) | internet | model_only | lan | sources (the package sources alone, no model: a check runs no agent)
  github_read: []              # other repositories of yours that this project's sandboxes may read, beside its own (12.6)
git:
  remote: git@github.com:<owner>/example.git   # over SSH, or null for a local-only project
  push: [main, task_branches]   # sub/* branches are never pushed to the remote
  fetch_minutes: 10
  merge: auto                  # auto | merge_commit | pull_request. auto: a pull request with a GitHub remote and gh signed in
stages:                        # the five fixed stages, and the optional ones with "on"
  - id: planning
  - id: backlog
  - id: analysis
    role: curator
    wip: 4
    exit: [has_why_what_goal, has_check, analysis_approved]
  - id: in_progress
    role: builder
    wip: 6
    enter: [dependencies_done]
    exit: [branch_pushed, has_commits, note_exists, task_cmd_checks_pass, task_judge_checks_pass, docs_done, skills_vendored]   # with Review off, also named_tests_kept (7.1); before Done, review_covers_head (7.4)
  - id: review
    optional: true
    on: true
    role: reviewer
    wip: 4
    exit: [review_verdict_approve]
  - id: test
    optional: true
    on: true
    role: tester
    wip: 4
    exit: [test_result_pass, task_checks_pass]
  - id: e2e
    optional: true
    on: false                  # on when the plan asks for end-to-end tests
    role: e2e
    wip: 1
    run: "make e2e"            # how the application is started and driven; exit code 3 means the application could not start (since 0.47)
    exit: [e2e_result_pass]
  - id: verification
    optional: true
    on: false                  # on when the plan asks for it
    role: verifier
    wip: 1
    exit: [verify_pass]
  - id: done
    enter: [merges_and_passes_on_main]
docs:
  root: docs                   # where the pages are on main
  changelog: CHANGELOG.md
  required: true               # every task names the pages it updates, or gives a reason
knowledge:
  memory:
    index_max_chars: 6000
  feedback:
    max_per_run: 3
  evals:
    repeats: 3                 # each case runs this often; the spread is the margin (10.5)
    min_cases: 10              # fewer cases than this in the project is too little to tell
```

### 14.2 How a change takes effect

- **Validated.** Each set has a schema with a default for every setting. A change that does not validate is refused. Since 0.65 (M17) what retention keeps is bounded: `updates.keep_versions` from 2 to 20, both `keep_cli_files_days` from 1 to 365, and `updates.backup_keep`, `hosting.keep_images` and a set `keep_trajectories_days` each 1 or more.
- **Defaults come from the settings schema and `baseline/roles.yaml`.** The defaults for a project's settings are in `internal/settings/board.yaml` (and the machine's in `internal/settings/crew.yaml`, with `baseline/roles.yaml` for the first model and effort of each role; 10.3). Every project that has not set its own value gets the default. The model and the effort of an agent are the machine's own setting, and no improvement and no release changes them (12.3).
- **A history.** Every change is a row in `changes` with the value before and after (6.4), so there is a history and any change can be put back. Since 0.64 (M17): a key's history is the lines of its set's rows where its value differs, a key the set does not hold being its default, newest first, at most 50; Put back names a change and a key, and stores the value before it, or takes the key out of the set when it was not set then, so its default applies again; it is validated as every change, and is a change of its own. `GET /api/settings/schema` gives the screens the words each setting takes.
- **No restart (R73).** The orchestrator reads the settings at each scheduling step. A change applies to runs that start after it.
- **Running work is not interrupted.** A run keeps the settings it started with.
- **Prompts and agent definitions** are not settings in this sense. They are Crew's baseline (10.3). The operator edits them directly. Agents change them only through an improvement, or an override for one project, that passed its evals. For the roles that check, both also wait for you.

### 14.3 From a file to the UI

The settings are reachable in three ways, built in this order (R74):

1. **A file, imported.** From the first milestone, `crew-ctl settings import` reads a YAML file into the database, and `export` writes one. Both are done by `crew admin` (16.11).
2. **API**, from the first milestone: `GET` and `PUT` for both sets, with validation.
3. **UI**, from M17: two settings screens that use the same API. Machine settings, the last entry of the menu's first group, and a project's Settings put every key in cards as gsettings.png and settings.png lay them out, without a second CLI and without budgets, each key with its history and Put back (14.2). A project's stage table has each stage's limit as a stepper, and the tickets in it now. The facts of the machine and the keys a release sets are read only, the models of each role are the Models screen's, and the keys that 20.3 asks a fresh sign-in for say so, and are changed behind it (since 0.66). A key read only on a screen has its history there and no Put back, which would change it as its control would. A setting that takes a fraction is a text field, also while it holds a whole number: the schema's view names those settings.

The credentials are the exception. They are set up in Setup, in the UI, from the start. They are encrypted, and they are never part of an export (12.6).
