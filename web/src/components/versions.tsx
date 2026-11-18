import { useState } from 'react'
import { send } from '../lib/api'
import { clean } from '../lib/gate'
import { usePoll } from '../lib/poll'
import { type Copyable, copied, type Ended, lastChange } from '../lib/updates'
import { SaidView } from './said'
import { ago, button, mainButton, Tag } from './ui'

// Parts of the Versions screen (16.1 to 16.5) that show text from outside the app: a release's notes,
// which agents and the operator wrote, its title, and what the updater or a command said. Each such
// text is marked data-free-text, so the gate's no-dollar check reads the app's own words and not
// these (0.53, the operator's choice of report 9).

const kindWords = { text: 'text', code: 'code', setup: 'machine setup' }

// commits are the lines of a release's notes that name what changed.
function commits(notes: string): string[] {
  return notes
    .split('\n')
    .map((l) => l.trim())
    .filter((l) => l.startsWith('- '))
    .map((l) => l.slice(2))
}

// NextCommits are the first six commits of the next release's notes, each with the release's kind.
export function NextCommits({ notes, kind }: { notes: string; kind: keyof typeof kindWords }) {
  return (
    <ul className="space-y-1 text-sm">
      {commits(notes)
        .slice(0, 6)
        .map((c) => (
          <li key={c}>
            <span data-free-text>{c}</span> <Tag mono>{kindWords[kind]}</Tag>
          </li>
        ))}
    </ul>
  )
}

// nextWhy says whether the next release installs by itself. Its clock says so: there is none under
// auto_install never, which another installation takes (16.12), nor when its gate skipped a step (16.3).
export function nextWhy(n: { kind: keyof typeof kindWords; held: boolean; heldWhy?: string; installAfter?: string }): string {
  if (n.kind === 'setup') return 'It needs a newer machine setup: run crew-ctl install --upgrade-runtime on the machine first.'
  if (n.kind === 'text') {
    if (n.held) return `It changes only prompts and harness text, and is held: ${n.heldWhy ?? ''}`
    return n.installAfter
      ? 'It changes only prompts and harness text, so it installs by itself unless you hold it.'
      : 'It changes only prompts and harness text, and does not install by itself here. It waits until you press Install now.'
  }
  return n.installAfter
    ? 'It changes code and touches no protected part, so it installs by itself unless you hold it.'
    : 'It changes code, so it does not install by itself. It waits until you press Install now.'
}

export type ReleaseLine = Copyable & { commit: string; title: string; failure?: string; state: string }

// ReleaseWhat is what changed in a release, on its row: its title, why the updater stopped it, and
// its copy on GitHub.
export function ReleaseWhat({ r, publish }: { r: ReleaseLine; publish: boolean }) {
  return (
    <>
      {r.title ? <span data-free-text>{r.title}</span> : `Built from ${r.commit.slice(0, 12)}`}
      {r.failure && (
        <div className="text-bad">
          Stopped by the updater{r.state === 'failed' ? "'s check" : ''}: <span data-free-text>{r.failure}</span> Nothing had to be done.
        </div>
      )}
      <div className="text-xs text-text-dim">
        <SaidView said={copied(r, publish)} />
      </div>
    </>
  )
}

export type EndedRequest = Ended & { id: number; askedAt: string; endedAt?: string }

// LastChanges are the five newest requests to the updater that ended.
export function LastChanges({ requests }: { requests: EndedRequest[] }) {
  return (
    <ul className="space-y-2 text-sm" data-last-changes>
      {requests.length === 0 && <li className="text-text-dim">None yet.</li>}
      {requests
        .filter((q) => q.state !== 'running')
        .slice(0, 5)
        .map((q) => (
          <li key={q.id}>
            <span className={q.state === 'done' ? 'text-green-700 dark:text-green-400' : 'text-bad'}>{q.state === 'done' ? '✓' : '×'}</span>{' '}
            <span className="font-mono text-xs">
              {q.from ?? '—'} → {q.version}
            </span>{' '}
            <span className="text-text-dim">{ago(q.endedAt ?? q.askedAt)}</span>
            <div className="text-text-dim">
              <SaidView said={lastChange(q)} />
            </div>
          </li>
        ))}
    </ul>
  )
}

export type UpdaterStep = { step: number; name: string; ok: boolean; detail: string }

// UpdaterSteps are the steps of the updater's request that runs; a step's detail is what its command
// said.
export function UpdaterSteps({ steps }: { steps: UpdaterStep[] }) {
  return (
    <ol className="mt-1 space-y-0.5 text-sm">
      {steps.map((s, i) => (
        <li key={i}>
          <span className={s.ok ? 'text-green-700 dark:text-green-400' : 'text-bad'}>{s.ok ? '✓' : '×'}</span> {s.step}. {clean(s.name)}{' '}
          <span className="text-text-dim" data-free-text>
            {clean(s.detail)}
          </span>
        </li>
      ))}
    </ol>
  )
}

// Bumps are the numbers a build may take: the next patch, minor, or major (16.2 step 3).
const bumps = ['patch', 'minor', 'major'] as const
export type Bump = (typeof bumps)[number]

// sendBuild asks the server to build a release of Crew's project's main with the bump; it gives the
// server's words when it refuses, "" when it took it.
export function sendBuild(bump: Bump, post: (method: 'POST', path: string, body: Record<string, unknown>) => Promise<unknown>): Promise<string> {
  return post('POST', '/api/crew/versions/build', { bump }).then(
    () => '',
    (e: Error) => e.message,
  )
}

export type BuildReleaseProps = {
  canBuild: boolean
  building: string
  bump: Bump
  confirming: boolean
  busy: boolean
  failed: string
  onBump: (b: Bump) => void
  onOpen: () => void
  onPress: () => void
  onCancel: () => void
}

// BuildReleaseView is Build a release (16.2 step 3, since 0.54): nothing on a server that builds no
// release, the version of a build that runs, or the button with the number it takes; a press asks
// once, naming the bump.
export function BuildReleaseView({ canBuild, building, bump, confirming, busy, failed, onBump, onOpen, onPress, onCancel }: BuildReleaseProps) {
  if (!canBuild) return null
  return (
    <div className="mt-3 text-sm" data-build-release>
      {building ? (
        <p className="text-text-dim">A build of {clean(building)} runs: its log is in the gate's panel.</p>
      ) : confirming ? (
        <div className="rounded border border-amber p-3">
          <p className="mb-2">Build the next {bump} release from the newest commit of Crew&apos;s project&apos;s main?</p>
          <div className="flex gap-2">
            <button className={mainButton} disabled={busy} onClick={onPress}>
              Build it
            </button>
            <button className={button} disabled={busy} onClick={onCancel}>
              Cancel
            </button>
          </div>
        </div>
      ) : (
        <div className="flex flex-wrap items-center gap-2">
          <select className="rounded border border-border bg-bg px-1 py-1 text-sm" value={bump} onChange={(e) => onBump(e.target.value as Bump)} aria-label="The number it takes">
            {bumps.map((b) => (
              <option key={b} value={b}>
                next {b}
              </option>
            ))}
          </select>
          <button className={button} disabled={busy} onClick={onOpen}>
            Build a release
          </button>
        </div>
      )}
      {failed && (
        <p className="mt-2 text-bad" data-free-text>
          {clean(failed)}
        </p>
      )}
    </div>
  )
}

// BuildRelease sends a press of Build a release and reads the screen again after it.
export function BuildRelease({ canBuild, building, onDone }: { canBuild: boolean; building: string; onDone: () => void }) {
  const [bump, setBump] = useState<Bump>('patch')
  const [confirming, setConfirming] = useState(false)
  const [busy, setBusy] = useState(false)
  const [failed, setFailed] = useState('')
  const press = () => {
    setBusy(true)
    setFailed('')
    sendBuild(bump, send).then((why) => {
      setBusy(false)
      setConfirming(false)
      setFailed(why)
      onDone()
    })
  }
  return (
    <BuildReleaseView
      canBuild={canBuild}
      building={building}
      bump={bump}
      confirming={confirming}
      busy={busy}
      failed={failed}
      onBump={setBump}
      onOpen={() => setConfirming(true)}
      onPress={press}
      onCancel={() => setConfirming(false)}
    />
  )
}

export type DeployFiles = { state?: '' | 'same' | 'differ' | 'unrecorded'; differ?: number; paths?: string[]; command?: string; error?: string }

// DeployFilesRow says whether crew-ctl and its files on the machine are the running release's, as
// install recorded them (16.11, since 0.55): an update replaces the containers, not them, and the
// command for root, which checks the archive's hash first, puts the release's own in place. The update
// is not held for it. The paths come from the release's archive, which Crew's project's commits make:
// they are marked as text from outside. An archive that does not check gets no command.
export function DeployFilesRow({ f }: { f: DeployFiles }) {
  if (!f.state) return <span className="text-text-dim">{f.error ? clean(f.error) : 'no deploy archive to compare with'}</span>
  if (f.state === 'same') return <span>the running release&apos;s, as install recorded them</span>
  return (
    <span className="text-amber-700 dark:text-amber" data-deploy-files>
      {f.state === 'differ' ? (
        <>
          not the running release&apos;s, as install recorded them: {f.differ} {f.differ === 1 ? 'file differs' : 'files differ'} (
          {(f.paths ?? []).map((p, i) => (
            <span key={p}>
              {i > 0 && ', '}
              <span data-free-text>{clean(p)}</span>
            </span>
          ))}
          {(f.differ ?? 0) > (f.paths ?? []).length && ', and more'}).
        </>
      ) : (
        <>not recorded: installed before Crew recorded them.</>
      )}{' '}
      {f.command ? (
        <>
          As root: <code className="font-mono text-xs break-all">{f.command}</code>
        </>
      ) : (
        f.error && <span>{clean(f.error)}: Crew offers no command.</span>
      )}
    </span>
  )
}

export type PinLine = { source: string; text: string; ok: boolean }
export type PinLook = { at: string; lines: PinLine[]; made: string[] }

export type DailyLookProps = {
  look: PinLook | null
  looks: boolean
  now: number
  confirming: boolean
  busy: boolean
  failed: string
  onOpen: () => void
  onPress: () => void
  onCancel: () => void
}

// DailyLookView is the daily look's newest look (12.8, since 0.56): when it was, what it read of each
// publisher, and the tickets it made on Crew's project. A look older than two days is said: a server
// that was down at the hour looks the next day only. Look now reads the publishers at once and makes
// the tickets, after a confirmation. The lines hold versions and digests the publishers gave, which
// passed their shapes: they are marked as text from outside.
export function DailyLookView({ look, looks, now, confirming, busy, failed, onOpen, onPress, onCancel }: DailyLookProps) {
  const old = look !== null && now - new Date(look.at).getTime() > 2 * 24 * 3600 * 1000
  return (
    <section className="mt-6 rounded-md border border-border p-4" data-daily-look>
      <h2 className="mb-1 text-lg font-semibold">The daily look</h2>
      <p className="text-sm text-text-dim">
        Once a day, at the hour of the backup, Crew reads what the publishers of Go, Node, chrome-devtools-mcp, agy, gh, and the pinned
        images offer, against versions.lock on main of Crew&apos;s project, and makes a ticket there for each new version, once.
      </p>
      {look === null ? (
        <p className="mt-2 text-sm">No look yet.</p>
      ) : (
        <>
          <p className="mt-2 text-sm">
            The last look was {ago(look.at, now)} ago
            {old && <span className="text-amber-700 dark:text-amber">, more than two days ago</span>}.{' '}
            {look.made.length === 0 ? (
              'It made no ticket.'
            ) : (
              <>
                It made tickets for <span data-free-text>{look.made.map(clean).join(', ')}</span>.
              </>
            )}
          </p>
          <ul className="mt-2 space-y-0.5 text-sm" data-look-lines>
            {look.lines.map((l, i) => (
              <li key={i} className={l.ok ? '' : 'text-bad'}>
                <span className="font-mono text-xs">{clean(l.source)}</span>: <span data-free-text>{clean(l.text)}</span>
              </li>
            ))}
          </ul>
        </>
      )}
      {!looks ? (
        <p className="mt-3 text-sm text-text-dim">The daily look does not run on this server.</p>
      ) : confirming ? (
        <div className="mt-3 rounded-md border border-amber p-3 text-sm" data-look-confirm>
          <p>Look now reads the publishers at once and makes a ticket on Crew&apos;s project for each new version it has not made one for.</p>
          <div className="mt-2 flex gap-2">
            <button className={mainButton} disabled={busy} onClick={onPress}>
              {busy ? 'Looking…' : 'Look now'}
            </button>
            <button className={button} disabled={busy} onClick={onCancel}>
              Cancel
            </button>
          </div>
        </div>
      ) : (
        <button className={button + ' mt-3'} onClick={onOpen}>
          Look now…
        </button>
      )}
      {failed && (
        <p className="mt-2 text-sm text-bad" data-free-text>
          {clean(failed)}
        </p>
      )}
    </section>
  )
}

export function DailyLook() {
  const { data, reload } = usePoll<{ look: PinLook | null; looks: boolean }>('/api/crew/pins', 60000)
  const [confirming, setConfirming] = useState(false)
  const [busy, setBusy] = useState(false)
  const [failed, setFailed] = useState('')
  if (!data) return null
  const press = () => {
    setBusy(true)
    setFailed('')
    send('POST', '/api/crew/pins/look').then(
      () => {
        setBusy(false)
        setConfirming(false)
        reload()
      },
      (e: Error) => {
        setBusy(false)
        setFailed(e.message)
      },
    )
  }
  return (
    <DailyLookView
      look={data.look}
      looks={data.looks}
      now={Date.now()}
      confirming={confirming}
      busy={busy}
      failed={failed}
      onOpen={() => setConfirming(true)}
      onPress={press}
      onCancel={() => setConfirming(false)}
    />
  )
}

export type MeasureResult = { metric: string; before: number; after: number; ticketsBefore: number; ticketsAfter: number; margin: number; verdict: string; why: string }
export type ReleaseMeasure = { before?: string; verdict: string; why: string; ticketsBefore: number; ticketsAfter: number; window: number; results?: MeasureResult[] }

// The six metrics a release is measured on, in the screen's words; a share is shown in percent
// (internal/measure, Metrics).
const metricWords: Record<string, { label: string; share: boolean }> = {
  first_request_pass_rate: { label: 'Checks passed on the first request', share: true },
  passed_first_time_rate: { label: 'Passed the first time', share: true },
  fix_rounds_per_ticket: { label: 'Fix rounds per ticket', share: false },
  tokens_per_ticket: { label: 'Tokens per ticket', share: false },
  interventions_per_ticket: { label: 'Times you stepped in per ticket', share: false },
  escaped_defects_per_ticket: { label: 'Bugs found after Done per ticket', share: false },
}
const verdictWords: Record<string, string> = { better: 'better', same: 'the same', worse: 'worse', inconclusive: 'too few tickets' }

function shown(metric: string, v: number) {
  const w = metricWords[metric]
  if (w?.share) return `${Math.round(v * 100)}%`
  return metric === 'tokens_per_ticket' ? String(Math.round(v)) : v.toFixed(2)
}

// ReleaseMeasureLine is a release compared with the release it replaced, under its results on Versions
// (10.7, since 0.57): once it was measured, the verdict in Crew's words and the six metrics with their
// numbers; before, the number of tickets it waits for. A release never installed is not measured.
export function ReleaseMeasureLine({ m, installed, window }: { m?: ReleaseMeasure | null; installed: boolean; window: number }) {
  if (!m) {
    return installed ? (
      <div data-measure className="text-xs text-text-dim">
        measured once {window} tickets ran on it
      </div>
    ) : null
  }
  let text = m.why
  if (m.verdict === 'not_compared') text = `not compared: ${m.why}`
  if (m.verdict === 'worse') {
    const worse = (m.results ?? []).filter((r) => r.verdict === 'worse').map((r) => (metricWords[r.metric]?.label ?? r.metric).toLowerCase())
    text = `worse than ${m.before}: ${worse.join(', ')}`
  }
  const tone = m.verdict === 'worse' ? 'text-amber-700 dark:text-amber' : 'text-text-dim'
  return (
    <div data-measure className={`text-xs ${tone}`}>
      {text}
      {m.verdict !== 'not_compared' && (m.results ?? []).length > 0 && (
        <details data-measure-numbers className="text-text-dim">
          <summary className="cursor-pointer">the six numbers</summary>
          <ul className="list-disc pl-4">
            {(m.results ?? []).map((r) => (
              <li key={r.metric}>
                {metricWords[r.metric]?.label ?? r.metric}: {shown(r.metric, r.before)} → {shown(r.metric, r.after)}, {verdictWords[r.verdict] ?? r.verdict}
              </li>
            ))}
          </ul>
        </details>
      )}
    </div>
  )
}
