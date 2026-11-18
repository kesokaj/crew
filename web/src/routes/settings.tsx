import { createFileRoute, Link } from '@tanstack/react-router'
import { useState, type ReactNode } from 'react'
import { usePoll } from '../lib/poll'
import { shown, valueAt } from '../lib/settings'
import { Brackets, Field, machineAPI, Problems, useSettings, type Settings } from '../components/settingfield'
import { ago, Badge, button, Card, PageHead, Rows, when } from '../components/ui'
import { ApiError, send } from '../lib/api'
import { deviceName, freshFor, goSignIn, type Device, type SessionView } from '../lib/session'
import { agentName } from '../lib/models'

export const Route = createFileRoute('/settings')({ component: MachineSettings })

type Room = { cores: number; memMB: number; room: { cpus: number; memMB: number } }
type Disk = { disk?: { totalBytes: number; percent: number } }

const gb = (mb: number) => `${Math.round(mb / 1024)} GB`

// The machine's settings (design 14.1 to 14.3, gsettings.png): every key of crew.yaml, in the
// picture's cards, each with its history and Put back. The picture is of the simulation: its claude
// slot and its budgets are gone (0.42, 0.53). A key 20.3 guards asks for a fresh sign-in when it is
// changed (plan D15), and the Devices card lists the devices signed in, which the picture has not.
function MachineSettings() {
  const s = useSettings(machineAPI)
  return (
    <div className="mx-auto max-w-6xl p-6">
      <PageHead title="Machine settings">
        These are the machine's settings. They are rows in Crew's database, and every change is kept. They apply to every project,
        to what starts after the change.
      </PageHead>
      <Problems s={s} />
      <div className="grid gap-4 md:grid-cols-2 lg:grid-cols-3">
        <LeadSlots s={s} />
        <Machine s={s} />
        <GitHub s={s} />
        <Networks s={s} />
        <Models s={s} />
        <Updates s={s} />
        <Improvements s={s} />
        <Kept s={s} />
        <Care s={s} />
        <Devices />
        <Conversations s={s} />
        <Notifications s={s} />
        <Hosting s={s} />
      </div>
      <InstallText />
    </div>
  )
}

function LeadSlots({ s }: { s: Settings }) {
  return (
    <Card title="Lead slots, for each CLI">
      <Field s={s} path="max_active_tickets" label="Active tickets at once across all projects" />
      <Field s={s} path="max_concurrent_runs" label={<>Concurrent agent runs at once on <span className="font-mono">agy</span></>} />
      <p className="mt-2 text-xs text-text-dim">
        Active tickets across all projects default to 12 (and each project defaults to 4). Concurrent agent runs default to 8 across
        all roles, while interactive turns you are waiting on bypass background ticket contention.
      </p>
    </Card>
  )
}

function Machine({ s }: { s: Settings }) {
  const { data } = usePoll<{ room: Room | null }>('/api/overview', 0)
  const { data: db } = usePoll<Disk>('/api/database', 0)
  const room = data?.room
  const size = (n: string) => {
    const v = valueAt(s.doc, `sandbox_sizes.${n}`) as { cpus?: number; memory_gb?: number } | undefined
    return v ? `${1 + (v.cpus ?? 0)} CPU, ${2 + (v.memory_gb ?? 0)} GB in all` : ''
  }
  return (
    <Card title="Machine">
      <Rows
        narrow
        rows={[
          ['Address', location.host],
          ['CPU', room ? `${room.cores} cores; ${room.room.cpus} virtual CPUs are given out` : '…'],
          ['Memory', room ? `${gb(room.memMB)}; ${gb(room.room.memMB)} given out, never twice` : '…'],
          ['Disk', db?.disk ? `${Math.round(db.disk.totalBytes / 1e9)} GB, ${db.disk.percent.toFixed(0)}% used` : 'not read yet'],
          ['Kind', 'bare metal or a VM with nested virtualisation, Debian or Ubuntu, Docker, used only for Crew'],
          ['Isolation', 'the Kata runtime with Cloud Hypervisor'],
          ['Sign-in', 'Google sign-in at the ingress, for the admin email'],
          ['Runtime image', <span className="font-mono text-xs">{shown(valueAt(s.doc, 'runtime_image'))}</span>],
        ]}
      />
      <p className="mt-3 text-xs tracking-wide text-text-dim uppercase">Sandbox sizes, beside the 1 CPU and 2 GB every microVM has</p>
      {(['small', 'large', 'build'] as const).map((n) => (
        <div key={n}>
          <p className="mt-2 text-sm font-semibold">
            {n.charAt(0).toUpperCase() + n.slice(1)} sandbox <span className="font-normal text-text-dim">· {size(n)}</span>
          </p>
          <Field s={s} path={`sandbox_sizes.${n}.cpus`} label="Virtual CPUs" />
          <Field s={s} path={`sandbox_sizes.${n}.memory_gb`} label="Memory" unit="GB" />
          <Field s={s} path={`sandbox_sizes.${n}.disk_gb`} label="Disk" unit="GB" />
          <Field s={s} path={`sandbox_sizes.${n}.docker`} label="Own Docker" readOnly="What the size is for: it comes with Crew." />
        </div>
      ))}
      <Field s={s} path="machine_reserve.cpus" label="CPUs kept for the machine" />
      <Field s={s} path="machine_reserve.memory_gb" label="Memory kept for the machine" unit="GB" />
      <Field s={s} path="cpu_overcommit" label="CPU allowances may add up to" unit="times the cores" />
      <Field s={s} path="sandbox.suspend_after_minutes" label="Idle warm project microVM suspended after" unit="min" />
      <Field s={s} path="sandbox.stop_after_minutes" label="Suspended project microVM stopped after" unit="min" />
      <Field s={s} path="sandbox.max_snapshots" label="Snapshots of /home/agent kept per project" />
    </Card>
  )
}

function GitHub({ s }: { s: Settings }) {
  return (
    <Card title="GitHub">
      <Rows
        narrow
        rows={[
          ['Git', 'over SSH, with the one key from Setup. The key stays with the server.'],
          ['In a sandbox', 'neither the key nor gh. Each sandbox gets a token from the GitHub App that can only read and stops working after one hour.'],
        ]}
      />
      <Field s={s} path="github.owner" label="Owner of new repositories" />
      <Field s={s} path="github.visibility" fresh label="New repositories are" />
      <Link to="/setup" className={button + ' mt-2 inline-block'}>
        Setup
      </Link>
    </Card>
  )
}

const modeWords = { internet: 'Internet', model_only: 'Model only', lan: 'LAN' }

function Networks({ s }: { s: Settings }) {
  return (
    <Card title="Network modes">
      <Rows
        narrow
        rows={[
          ['Internet', 'Reaches the internet. This machine, the local network, and other sandboxes are blocked.'],
          ['Model only', "Reaches the agent's model service and Crew, and nothing else."],
          ['LAN', 'Reaches the internet and the local network. You set this on purpose.'],
          ['The improver', 'Model only, always'],
        ]}
      />
      <Field s={s} path="network.mode" label="Default; each project sets its own" words={modeWords} fresh />
      <Field s={s} path="network.model_only_hosts" label="What Model only reaches, through the server" fresh />
      <Field s={s} path="sandbox.sources" label="What a release's build reaches in Sources" fresh />
    </Card>
  )
}

function Models({ s }: { s: Settings }) {
  const roles = (valueAt(s.doc, 'agents.roles') ?? {}) as Record<string, { model?: string[]; effort?: string }>
  return (
    <Card title="The model for each role">
      <dl className="grid grid-cols-[7rem_1fr] gap-x-3 gap-y-1 text-sm">
        {Object.entries(roles).map(([r, v]) => (
          <div key={r} className="contents">
            <dt className="text-text-dim">{agentName(r)}</dt>
            <dd className="font-mono text-xs">
              {(v.model ?? [])[0] ?? '—'} · {v.effort}
            </dd>
          </div>
        ))}
      </dl>
      <p className="mt-2 text-xs text-text-dim">
        The first model of each role's list, and its effort. You change them in Models, which also keeps the agents you set yourself
        and the names you added. A project can set others for a role, and a ticket can pin them.
      </p>
      <Field s={s} path="agents.at_limit" label="When a CLI or a group of models cannot run" words={{ move: 'Move', wait: 'Wait' }} />
      <Field s={s} path="agents.checks_use_another_family" label="A check runs on another family than the builder" readOnly="Always on: rule 15 of the rules that keep you safe. A project's Settings can allow it for that project." />
      <Field s={s} path="agents.clis" label="CLIs" readOnly="agy alone, since 0.42." />
      <Field s={s} path="agents.models_refresh" label="The lists of models are read" />
      <Brackets s={s} />
      <p className="mt-3 text-xs tracking-wide text-text-dim uppercase">Crew's own choice of a model</p>
      <Field s={s} path="agents.auto.on" label="Crew chooses for the builder, reviewer, tester, E2E tester and verifier" />
      <Field s={s} path="agents.auto.explore_share" label="Share of runs that try another entry" />
      <Field s={s} path="agents.auto.min_runs" label="Runs on an entry before it is compared" />
      <Field s={s} path="agents.auto.min_days_between" label="Days between two changes of an agent" />
      <Field s={s} path="agents.auto.watch_runs" label="Runs watched after a change" />
      <Field s={s} path="agents.auto.retry_days" label="Days before an entry gone back from is tried again" />
      <Field s={s} path="agents.auto.hide_days" label="Days Not now hides a recommendation" />
      <Field s={s} path="agents.auto.compare_runs" label="Runs of each entry in a comparison" />
      <Field s={s} path="agents.auto.better_points" label="Points more passed the first time that are better" />
      <Field s={s} path="agents.auto.same_points" label="Points within which a check's share of fixes is the same" />
      <Field s={s} path="agents.auto.window_days" label="Days the numbers are taken over" />
      <div className="mt-2 flex gap-2">
        <Link to="/models" className={button}>
          Models
        </Link>
        <Link to="/clis" className={button}>
          Agent CLIs
        </Link>
      </div>
    </Card>
  )
}

function Updates({ s }: { s: Settings }) {
  // Another installation names no repository it builds from, and every release waits for Install there,
  // whatever auto_install says (16.12): a stored source_repo is empty or owner/name, as the PUT checks.
  const another = s.doc !== null && !valueAt(s.doc, 'updates.source_repo')
  return (
    <Card title="Updates">
      <Field
        s={s}
        path="updates.auto_install"
        label="A release installs by itself when"
        words={{ text: 'it changes only prompts and harness text', unprotected: 'it touches no protected part', never: 'never' }}
        note={another ? 'Never on this installation: it names no repository it is built from, so every release waits for Install.' : undefined}
        fresh
      />
      <Field s={s} path="updates.install_after_hours" label="Such a release installs after" unit="hours" fresh />
      <Field s={s} path="updates.install_test" label="The gate's install test" words={{ local: 'on a VM on this machine', off: 'off' }} fresh />
      <Field s={s} path="updates.source_repo" label="Built from and pushed to" fresh />
      <Field s={s} path="updates.publish" label="Published" words={{ github: 'on GitHub, private', off: 'off' }} fresh />
      <Field s={s} path="updates.watch_minutes" label="Switched back by itself if unhealthy within" unit="minutes" />
      <Field s={s} path="updates.keep_versions" label="Versions kept, with their images" />
      <Field s={s} path="updates.release_after_tickets" label="A release is cut after this many merged tickets" />
      <Field s={s} path="image_updates" label="New versions of the CLIs and tools are looked for" />
      <Field s={s} path="updates.backup_path" label="Backups go to" />
      <Field s={s} path="updates.backup_keep" label="Daily backups kept" />
      <Field s={s} path="updates.backup_copy_to" fresh label="Each day's backup is also copied to" note="None: the backups stay on this machine." />
      <Link to="/versions" className={button + ' mt-2 inline-block'}>
        Versions
      </Link>
    </Card>
  )
}

function Improvements({ s }: { s: Settings }) {
  return (
    <Card title="Improvements">
      <Field s={s} path="improvements.approval" fresh label="Goes ahead" words={{ auto: 'by itself, when its evals are not worse', operator: 'when you say so' }} />
      <Field s={s} path="improvements.run_every_days" label="The improver runs every" unit="days" />
      <Field s={s} path="improvements.run_after_tasks" label="Or sooner, after this many finished tickets" />
      <Field s={s} path="improvements.measure_after_tasks" label="Effect measured after" unit="tickets" />
      <Field s={s} path="improvements.crew_project" label="Crew's own project" note="Made by Make Crew's project; its remote must be what releases are built from." />
    </Card>
  )
}

function Kept({ s }: { s: Settings }) {
  return (
    <Card title="Kept in the database">
      <Rows narrow rows={[['Tickets, records, turns', 'for good'], ['Prompts and summaries', 'for good'], ['Snapshots', 'one dump per kept version']]} />
      <Field s={s} path="keep_trajectories_days" label="Trajectories" unit="days" note="None: for good. A number removes older ones, and only those marked done: feedback ran and eval cases were made. No trajectory is marked yet, so it removes nothing." />
      <Field s={s} path="keep_cli_files_days" label="A run's own CLI files, so its conversation can be continued" unit="days" />
      <Field s={s} path="conversations.keep_cli_files_days" label="A conversation's own CLI files, after its last turn" unit="days" note="For the conversations of the operator and the chats of the tickets (12.10)." />
      <Field s={s} path="hosting.keep_images" label="Images kept of each hosted project" note="For rolling back; the one that serves is always kept." />
      <Link to="/database" className={button + ' mt-2 inline-block'}>
        Database
      </Link>
    </Card>
  )
}

function Care({ s }: { s: Settings }) {
  return (
    <Card title="Looking after the machine">
      <Field s={s} path="disk.clean_at" label="The disk is cleaned up at" unit="% full" />
      <Field s={s} path="disk.stop_at" label="No new run starts at" unit="% full" />
      <Rows
        narrow
        rows={[
          ['Security updates', 'install by themselves'],
          ['Firewall', 'the same rules for IPv4 and IPv6'],
          ['Build by hand', <span className="font-mono text-xs">crew-ctl build, crew-ctl install</span>],
        ]}
      />
      <Field
        s={s}
        path="machine_care.restart_hours"
        label="Restarts for security updates come in the hours"
        note="Hours of the machine's time zone, CREW_TIMEZONE, as from-to. The restart comes when no run, check, build or terminal is going. After 7 days of waiting Crew holds new runs and restarts once the running ones finished. Each restart is news."
      />
      <Field s={s} path="sessions.days" label="A session lasts, on a device" unit="days" fresh />
      <Field s={s} path="sessions.fresh_minutes" label="A fresh sign-in is at most" unit="minutes old" fresh />
      <Field s={s} path="mcp_tokens.days" label="Personal tokens run out after" unit="days" />
      <Field s={s} path="mcp_tokens.calls_per_minute" label="Calls a minute of a personal token" />
      <Field s={s} path="logins.check_every_minutes" label="The logins are checked every" unit="minutes" />
      <Field s={s} path="quota.slow_down_at" label="Fewer runs start on a group of models above this share of its limit" />
    </Card>
  )
}

// Devices (20.3): every device signed in, this one marked, each with Sign out, which asks for a fresh
// sign-in and refuses that device for good. A device is one Google sign-in; gsettings.png has no such
// card, as the simulation kept no sessions.
function Devices() {
  const { data, error, reload } = usePoll<{ devices: Device[]; this: SessionView }>('/api/sessions', 15000)
  const [failed, setFailed] = useState('')
  const left = freshFor(data?.this ?? null)
  const out = async (d: Device) => {
    try {
      await send('POST', `/api/sessions/${d.id}/signout`)
      setFailed('')
      reload()
    } catch (e) {
      // A sign-in that is not fresh is offered by the page's dialog.
      if (!(e instanceof ApiError && e.auth)) setFailed(String(e instanceof Error ? e.message : e))
    }
  }
  return (
    <Card title="Devices">
      {data && (
        <p className="mb-2 text-xs text-text-dim" data-fresh-state>
          {left > 0 ? (
            `Your sign-in is fresh for ${left} more ${left === 1 ? 'minute' : 'minutes'}.`
          ) : (
            <>
              A terminal, Install, Resume, a personal token, and the settings marked so ask for a sign-in at most {data.this.freshMinutes} minutes old.{' '}
              <button
                className="text-accent underline"
                onClick={() => void goSignIn({ reason: 'FRESH_SIGN_IN', signIn: data.this.signIn, message: 'a fresh sign-in' })}
              >
                Sign in again now
              </button>
            </>
          )}
        </p>
      )}
      <ul className="divide-y divide-border/60 text-sm" aria-label="Devices signed in">
        {data?.devices.map((d) => (
          <li key={d.id} className="flex flex-wrap items-center gap-x-2 py-1.5" data-device={d.id}>
            <span className="min-w-0 flex-1" title={d.userAgent}>
              {deviceName(d.userAgent)} {d.this && <Badge tone="blue">This device</Badge>}
              <span className="block text-xs text-text-dim">
                signed in {when(d.authAt)} · last seen {ago(d.lastAt)} ago · first {when(d.firstAt)}
              </span>
            </span>
            <button className={button + ' py-0.5 text-xs'} onClick={() => void out(d)}>
              Sign out
            </button>
          </li>
        ))}
        {data && data.devices.length === 0 && <li className="py-1.5 text-text-dim">No device has signed in since sessions came, in 0.66.</li>}
      </ul>
      {(failed || error) && <p className="mt-2 text-xs text-bad">{failed || error}</p>}
      <p className="mt-2 text-xs text-text-dim">
        A device is one Google sign-in, and keeps its place in the list when it signs in again. Signed out, it is refused for good, and
        signing in on it again makes a new device.
      </p>
    </Card>
  )
}

function Conversations({ s }: { s: Settings }) {
  return (
    <Card title="Conversations, terminals and plans">
      <Field s={s} path="conversations.idle_minutes" label="A conversation's sandbox goes after" unit="minutes without a message" />
      <Field s={s} path="conversations.max_sandboxes" label="Sandboxes of conversations and ticket chats at once" />
      <Field s={s} path="conversations.summary_every_turns" label="Crew's summary is written every" unit="turns" />
      <Field s={s} path="terminal.idle_minutes" label="A project terminal goes after" unit="minutes without input" />
      <Field s={s} path="terminal.edit_mode" label="A project terminal may be switched to edit mode" fresh />
      <Field s={s} path="planning.rewrite" label="A better wording of your first message in a plan draft" words={{ suggest: 'proposed', off: 'off' }} />
      <Field s={s} path="planning.rewrite_timeout_seconds" label="Your own text is sent after" unit="seconds" />
    </Card>
  )
}

function Notifications({ s }: { s: Settings }) {
  return (
    <Card title="Notifications">
      <Field s={s} path="notifications.mail_to" label="Mail goes to" fresh />
      <Field s={s} path="notifications.mail_from_name" label="Mail comes from the name" />
      <Field s={s} path="notifications.mail_smtp" fresh label="Mail server" note="The account and its app password are with the credentials, not here." />
      <Field s={s} path="notifications.needs_you" fresh label="What needs you is sent by" />
      <Field s={s} path="notifications.news" fresh label="News is sent as" />
      <Field s={s} path="notifications.remind_after_hours" label="A reminder after" unit="hours" />
      <Field s={s} path="notifications.quiet_hours" label="Quiet hours, when news waits" />
      <Field s={s} path="notifications.chat_webhook" fresh label="Google Chat" />
      <Field s={s} path="notifications.web_push" label="Web push" note="Later, with the phone." />
    </Card>
  )
}

function Hosting({ s }: { s: Settings }) {
  return (
    <Card title="Hosting">
      <Field
        s={s}
        path="hosting.default_access"
        label="A new hosted project is reached by"
        words={{ private: 'the admin email', listed: 'listed addresses', google: 'any Google account', public: 'everyone' }}
        fresh
      />
      <p className="mt-2 text-xs text-text-dim">A hosted project gets its own name under the apps domain of the install file, and its own sign-in there.</p>
    </Card>
  )
}

function Section({ title, children }: { title: string; children: ReactNode }) {
  return (
    <Card title={title}>
      <div className="text-sm">{children}</div>
    </Card>
  )
}

// The two text parts of gsettings.png, in today's words: agy is the one CLI, pinned by its tarball.
function InstallText() {
  return (
    <>
      <h2 className="mt-8 mb-3 text-xl font-semibold">
        Install, update, and uninstall{' '}
        <span className="text-sm font-normal text-text-dim">one program on the machine, crew-ctl. Each of the three is tested on a fresh machine before every release.</span>
      </h2>
      <div className="grid gap-4 md:grid-cols-3">
        <Section title="Install">
          <ol className="list-decimal space-y-1 pl-5">
            <li>It gathers what it needs first: from the install file, or one question at a time. The address, the admin email, the Google sign-in client, and the DNS token.</li>
            <li>crew-ctl doctor checks the machine and the answers: Debian or Ubuntu, KVM, room, ports 80 and 443, and the names. It changes nothing.</li>
            <li>It installs Docker, Kata, and the firewall at the fixed versions, unless they are there.</li>
            <li>It makes the key file and the secrets, builds the images from the checkout, and starts the database, the server, the ingress, and the updater.</li>
            <li>It proves it: one microVM starts and reaches the Crew MCP. Then Setup is the first screen.</li>
          </ol>
          <p className="mt-2 text-xs text-text-dim">Every step looks first, so it can be run again. A dry run prints each command and runs none.</p>
        </Section>
        <Section title="Update">
          <Rows
            narrow
            rows={[
              ['From the app', 'Install now, in Versions'],
              ['On the machine', <span className="font-mono text-xs">crew-ctl update, crew-ctl rollback</span>],
              ['The same path', 'the updater checks the new version beside the running one, switches, and switches back if it turns unhealthy'],
              ['When the app is down', 'both commands still work: the updater does not need the server'],
            ]}
          />
        </Section>
        <Section title="Uninstall">
          <Rows
            narrow
            rows={[
              ['1. Stop', 'stops everything, removes nothing'],
              ['2. Remove Crew', 'the services, the sandboxes, the firewall rules, and the images. Keeps your data, the answers, the key and the secrets that open it, Docker, and Kata.'],
              ['3. Everything', "also the data, the backups, and the key. It asks for the machine's name, and cannot be undone."],
              ['Docker and Kata', 'removed only when you ask, and only if Crew installed them'],
            ]}
          />
          <p className="mt-2 text-xs text-text-dim">It prints its plan first, and takes out only what the install put in.</p>
        </Section>
      </div>
      <div className="mt-4">
        <Section title="Open-source, reproducible, and clean">
          <Rows
            rows={[
              ['Fixed versions', 'one file, versions.lock, with every version and checksum Crew is built from'],
              ['No secret', 'nothing secret is in the repository, and the gate scans for it. What differs between machines comes from the install file.'],
              ['Recorded', 'what cannot be fixed in advance, such as what a package source gives; agy is pinned by its tarball and checksum'],
              ['Docker only', 'every part of Crew is a container: the server, the database, the ingress, the updater, and every sandbox. On the machine itself are only Docker with Kata, the firewall, and crew-ctl.'],
              ['Open source', 'licensed under Apache 2.0. Crew runs on your own machines and on the clients you choose, with nothing reported home.'],
              ['This machine', 'you put a checkout on it, and crew-ctl install --build builds Crew there. It needs no GitHub login.'],
              ['Another machine', 'gets the images of a finished release of this installation, never a checkout. It builds no Crew, has its own sign-ins, and shares no data with this one.'],
              ['Nothing sent home', 'Crew reports nothing about this installation to anyone'],
            ]}
          />
        </Section>
      </div>
    </>
  )
}
