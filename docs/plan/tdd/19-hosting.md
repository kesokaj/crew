## 19. Hosting a project on the machine

### 19.1 What it is

When a project is built, the operator can host it from Crew's own machine with one button: **Host locally** (R146 to R150). Crew builds the project into a container image, runs it on the machine, and gives it an address through the ingress. It is the place to use and show what was built. "Locally" means on Crew's machine. Other places to host can come later.

### 19.2 From the button to an address

| Step | What happens | If it fails |
| --- | --- | --- |
| 1. Ask | The operator presses Host locally on the project and confirms the address and who may open it. | |
| 2. Make it hostable | If the project has no `Dockerfile` yet, Crew creates a ticket for it: a Dockerfile, the port, and a health address. Agents work it like any ticket. Hosting continues by itself when it is Done. | The ticket shows why. |
| 3. Build | In a sandbox with no agent, from a commit on `main`, the image is built once and kept on the machine. | Nothing is running yet, or the previous version keeps serving. |
| 4. Start | The image is started as a sandbox of its own. | The same. |
| 5. Check | The health address must answer. | The same. The operator is told what failed. |
| 6. Switch | The ingress sends the address to the new version. The previous one is stopped. The last three images are kept. | |
| 7. Record | A deploy record is written to the database: the commit, the image, who asked, when, and the result. A line of news says it is live. | |

This is the same pattern as Crew's own updater (16.4): build once, check beside the running version, then switch.

### 19.3 How it runs

A hosted project is one more kind of sandbox (13.3).

| | Hosted project |
| --- | --- |
| Isolation | A Kata microVM, like every sandbox. It has its own kernel and its own network, and cannot reach the machine, the local network, or other sandboxes. |
| One container | The project's image runs directly as the microVM. |
| Several containers | With a `compose.yaml`, a microVM with its own Docker runs them together. |
| Lives | Until the operator stops it. It starts again with the machine. |
| Holds | The settings and secrets the operator entered for it. No CLI login, and no agent. |
| Reaches | The internet, from inside. A switch per project turns that off. Never the machine, the local network, or other sandboxes. (R154) |
| Is reached | Only through the ingress, which asks for the sign-in first (19.4) |
| Data | A data directory for the project on the machine, mounted where the project says. It is kept across deploys and is part of the backups (16.9). |
| Size | A fixed size, set per project. Hosted projects get their room before agent sandboxes do. |
| Logs | What it prints is kept for 14 days by default (`log_days`) and shown in the app. |

- **Settings and secrets (R176).** Values such as a password are entered in the app, kept in Crew's database, encrypted, and given to the container as environment variables. They are never in git.
- **Health.** Crew keeps asking the health address. A project that stops answering is restarted. If that does not help, the operator is notified.

- **Data before a deploy (R171).** Before a deploy switches, Crew copies the data directory. The last 3 copies are kept. Roll back asks whether to put back the copy from before that deploy, because a newer version may have changed the data's format.

### 19.4 Addresses and who may open it

A hosted project can be reached from the internet. The ingress copied from styrman-crew already does most of this. There it reads labels on a container. In Crew the ingress has one fixed route, to the server, from its environment. From M19 it also asks the server for the routes of hosted projects every 10 seconds, on the door that is only for the ingress and with the secret the two share. The server answers from the database. So a label inside an image that an agent built decides nothing, and the ingress needs no access to Docker. The ingress makes the address and asks for the sign-in before a request reaches the project. The project uses the ingress's sign-in and needs none of its own.

| | How |
| --- | --- |
| The address | `https://<project>.<apps domain>`. The apps domain is set once for the machine. |
| The certificate | One wildcard certificate for the apps domain, fetched through the DNS provider, as in styrman-crew |
| An own domain | Later, with hosting several containers (plan M19 D19): `hosting.hostname` is a setting that reaches nothing yet. Then the operator adds a hostname and points its DNS at the machine, and the ingress fetches a certificate for it. |
| From the internet | The apps domain points at the machine's public address, and ports 80 and 443 reach the machine. This is set up with the machine in M1. Verifying it (section 24, item 23) waits for the operator's domain: M19 was accepted without it (section 23, since 1.17). |

Who may open it is one choice per project. It works as in styrman-crew: what is exposed asks for a Google sign-in, and the signed-in mail address must be on a list.

| Choice | Who gets in |
| --- | --- |
| Only the admin email (the default) | Google sign-in, and only the operator's own address |
| Listed addresses | Google sign-in, and the Google mail addresses on the project's list |
| Any Google account | Google sign-in, with any account |
| Public | Anyone, with no sign-in. A limit on requests per second applies. |

- **The project knows who signed in.** With a sign-in, the ingress passes the account's address to the project in a request header. It removes that header from every incoming request first, so it cannot be faked from outside. In styrman-crew the header is `X-Caddy-Email`.
- **Nothing reaches the project around the ingress.** The hosted sandbox has no published port. It is on a bridge of a kind of its own, `app-…`, which the ingress can reach and from which the machine and the local network cannot be reached. styrman-crew's rules drop everything between a sandbox bridge and the machine, so this is a new bridge kind with its own rules.
- Changing the choice takes effect at once and is recorded in the project's history.

- **Crew and hosted projects share no sign-in cookie (R172).** Crew's own address is not under the apps domain, and the apps domain is not under Crew's own address (for example, `apps.example.com` beside `crew.example.com`, or another domain). Its session cookie is bound to its own host name (`crew.example.com`), so a hosted project under `apps.example.com` cannot read it, and its portal signs with a separate HKDF-derived key (`CREW_APPS_JWT_KEY`). A hosted project gets the signed-in address in the header, and nothing else. styrman-crew's ingress also passes its own shared secret on such a request, and sets its cookie for the whole base domain. Crew's ingress does neither.
- **One path of Crew's own is open.** The server's health check, `/healthz`, is served without the sign-in, so that install and `crew-ctl status` can see that the server answers behind the ingress. The ingress removes every identity header, the shared secret, and its own cookies from such a request, and the server answers it with its version and whether the database answers, nothing more.
- **Crew's inner doors are not on the internet.** The credential broker, the git endpoint for runs, and the MCP for runs listen only on the sandbox networks. The ingress serves the app, the git endpoint for the operator, and the MCP for personal tokens.
- **The ingress, as built (since 0.72, M19).** Every 10 seconds the ingress asks `GET /ingress/routes` of the server, by the name `crew-server-door`, which only `crew-ingress` has, with the shared token and no identity header; the server answers only such a request, never from a sandbox's bridge, and Crew's own route answers `/ingress/…` with 404 itself. The answer names each hosted project that serves: its name, its address and port on its bridge, its access, its listed addresses, and its requests per second. The ingress leaves out a name that is not a DNS label, `crew` or `auth`, an address outside the machine's pool of bridges, an unknown access, and an address that is not one mail address; it loads a Caddyfile only when it changed; until the server first answers it serves Crew's route alone, and after, a failed ask keeps the last routes. Hosted projects sign in at `auth.<apps domain>`, a portal of their own, with its own cookie for the apps domain under its own prefix, signed with a key the ingress derives from `CREW_JWT_KEY` by HKDF and keeps in its environment, never in the Caddyfile: a token of one domain is no token on the other. Who may open a hosted project is in its policy, by mail address, the admin's and the listed ones, or any Google account, so a change holds at the next reload, within 10 seconds, not at the next sign-in; the project gets the signed-in address in `X-Caddy-Email` and the role `user`, never the ingress's token or a portal cookie, and every `Set-Cookie` it sends loses its `Domain`. A page's script can still set a cookie for the apps domain, and every hosted project is one site with one sign-in, so the ingress refuses a change (any method but GET, HEAD and OPTIONS) that a browser says comes from another site, before the sign-in is read (since 0.73, the security review of M19); and since 0.74 also a WebSocket a browser says another site asks, and a change from a browser that does not say where it comes from (no `Sec-Fetch-Site`) whose `Origin` is not the project's own; since 0.77 a WebSocket whose `Origin` is not the project's own is refused whatever `Sec-Fetch-Site` says, since a browser sends `Origin` on every handshake. A public project has no sign-in to ride, and takes what other sites send it. With a DNS token, the hosted projects are one site of `*.<apps domain>`, whose wildcard certificate comes through DNS, a handle for each and 404 for any other name, and the ingress writes `*.<apps domain>` and `auth.<apps domain>` in the apps domain's own zone. Without an apps domain of its own, or without `CREW_JWT_KEY`, the ingress serves no hosted project; `APPS_DOMAIN` no longer defaults to Crew's address. The Google sign-in client lists a second redirect address, `https://auth.<apps domain>/oauth2/google/authorization-code-callback`, which `crew-ctl doctor` checks and names, and which the Hosting screen's Address card shows when sign-in is on.

### 19.5 Keeping it current

| | |
| --- | --- |
| Deploy again | A button. It builds and checks the newest `main`, then switches. |
| Follow `main` | A switch, off by default. When it is on, `main`'s head is deployed once a ticket reached Done since the deploy that serves began: one deploy at a time, which lists the tickets Done since, checked before the switch. A head whose deploy failed is not tried again until a newer one (since 0.73, plan M19 D16). |
| Roll back | Any of the kept deploys can be switched back to. Nothing is rebuilt. |
| Stop | The address stops answering. The data is kept. |

- Agents cannot deploy. A deploy is asked for by the operator, or by the Follow `main` switch that the operator turned on.
- Every deploy is in the project's history, with the tickets it contains.

### 19.6 What it is not

- It is one machine. If the machine is off, the project is off.
- It is one hosted version per project. A preview per ticket would be nice, but is not needed now. It is not in the build plan. Other places to host are a later idea too.
- It is one container per project in M19: a project whose `hosting.dockerfile` names a compose file is told "not yet", and several containers and an own domain come together later (plan M19 D3, D19).

### 19.7 How it is built (since 0.73, M19)

- **The build.** A project's image is built from a commit of `main` in a sandbox of the kind `host-build`: no agent, no grant, its own Docker daemon, in the network mode of the project's checks, at the size of a build, waiting for room as a check does. It runs Crew's own line, which takes the Dockerfile as its argument and never as a word of it, and saves the image to a tar. The server copies the tar out as a plain file, cleans it of every name (no `RepoTags`, no `repositories`, no name of containerd's or OCI's in `index.json`), refuses one that is not one image as `docker save` writes it, refuses an image whose `VOLUME` is not `hosting.data` or which exposes ports without `hosting.port`, and loads it; Docker must give it the config's digest (the overlay2 store) or the digest of the manifest `index.json` names (containerd's), and nothing else. It is tagged `crew-host/<project id>:<deploy>`; the newest `hosting.keep_images` that served are kept, and never the one that serves.
- **The start.** Each deploy has a bridge of its own, `app-` or `apo-` (13.6). Before the app starts, a probe of the runtime image on that bridge must find the server, the ingress and the machine silent, two private addresses unreachable, and on `app-` the internet answering; otherwise the deploy fails, "the firewall of the hosted bridge is not in place". The app runs as `crew`'s uid and gid, every capability dropped, no new privilege, at most 4096 processes, a `/tmp` of 256 MB, no published port, the data folder as its one bind, `PORT` and the operator's values as its environment and no `CREW_` value, restarted by Docker, counted at its `hosting.size` and given room before every agent.
- **Check and switch.** The new sandbox starts beside the one that serves; its health address must answer 2xx within 2 minutes, a redirect being no answer. Then it serves, and the one before is removed 20 seconds later. Any failure leaves what served serving, with the deploy failed in Crew's words. Before the switch the data folder is copied with what serves paused; the newest `hosting.data_copies` copies are kept. A roll back starts a kept image; with its data it first copies the data of now, stops what serves, and puts back the data as the deploy rolled back to left it; when that fails it puts back the data and the version of before. A change of a live project's internet, size, port, health address, data folder or values starts its image again at once.
- **Running.** Every 15 seconds the health address is asked, at the port and path of what serves, and never while a deploy of the project runs; after three misses the sandbox is restarted, with 60 seconds of grace; after three more the project is failing, which is news sent at once, once per episode, and it is restarted every 10 minutes while it does not answer. What it prints is copied every minute into a file a day under `<data>/hosted/<project id>/logs/`, at most 256 MiB a day, kept `hosting.log_days`; the copy of a sandbox goes on after a restart of Crew from its last line copied, and a new sandbox's from its first. At the server's start every hosted sandbox that does not serve is removed first; then a roll back with data that had stopped what served has the data put back and what served started again, every other deploy that ran is failed, "interrupted by a restart of Crew", and a live sandbox Docker lost is started again from its image. A server changes only the sandboxes it started: one another server on the same Docker started is never listed, adopted, removed, stopped, started, paused, or joined to the ingress by it, also when its microVM is gone and only its bridge, which carries the same labels, is left, and since 0.78 neither is its log read nor whether it runs, and since 0.79 no command is run in it, a terminal's included; since 0.80 each of these reads whose the container is and acts on the container it read, by its ID, so one made under the name after the check is not touched; since 0.81 its bridge too has its own labels read, also when its container is this server's, and is joined, left, removed, and given a microVM or a probe by the ID that check read, and an adoption counts only an address on that bridge; a start that fails removes what its creates made, by their IDs, and under the name only a container or bridge from before whose labels name this server, never one with no label, which every action that takes only a sandbox's name and every use of its bridge leave too, whoever asks, and since 0.82 a command in a sandbox too, a terminal's keystrokes included, and the harness's read of whether its run's container runs, and since 0.84 no command at all counts a container with no sandbox label as this server's, the commands in a project's container, a setup VM and the server's own container having gone with the copied code that ran them; a sandbox's own labels are not a caller's to change; one whose owner Docker cannot say, as when it does not answer, is left as it is, its doors to the server kept. An unpause undoes a pause by the ID of the container the pause's check read, with no check of its own, so it never leaves a live app paused (since 0.77) and goes on by no name (since 0.81). A roll back with data that fails names its copy until the data of before is back and what served runs again, so a restart in between still finishes it (since 0.77).
- **The operator's flows.** Host locally on a project whose `main` has no Dockerfile makes the ticket that makes it hostable once, and hosting goes on by itself when `main` has it. Archive stops hosting, Pause leaves it, Delete removes its data, copies, logs, images and values. No tool of the Crew MCP deploys or changes a setting.
