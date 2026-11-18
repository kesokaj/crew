import { createFileRoute } from '@tanstack/react-router'
import { useState } from 'react'
import { send } from '../lib/api'
import { GatePanel } from '../components/gate'
import { SaidView } from '../components/said'
import { BuildRelease, DailyLook, type DeployFiles, DeployFilesRow, LastChanges, NextCommits, type ReleaseMeasure, nextWhy, ReleaseMeasureLine, ReleaseWhat, UpdaterSteps } from '../components/versions'
import { clean } from '../lib/gate'
import { usePoll } from '../lib/poll'
import { copied } from '../lib/updates'
import { ago, button, Card, Failed, mainButton, PageHead, Rows, Tag, when } from '../components/ui'

export const Route = createFileRoute('/versions')({ component: Versions })

type Gate = { passed: boolean; at: string; steps: { name: string; ok: boolean; detail?: string }[]; installTest: string }
type Step = { step: number; name: string; at: string; ok: boolean; detail: string }
type Request = {
  id: number
  version: string
  from?: string
  action: string
  askedBy: string
  state: string
  step: number
  steps: Step[] | null
  error?: string
  askedAt: string
  endedAt?: string
}
export type Release = {
  version: string
  commit: string
  state: string
  kind: 'text' | 'code' | 'setup'
  protected: string[]
  changed: number
  byHand: boolean
  gate?: Gate
  builtAt: string
  noticeAt?: string
  installAfter?: string
  held: boolean
  heldWhy?: string
  replacedBy?: string
  installedAt?: string
  failure?: string
  published: { tag?: string; page?: string; images?: Record<string, string>; done?: string }
  publishError?: string
  schema: number
  readBy: number
  pins: Record<string, string>
  images: Record<string, string>
  notes: string
  title: string
  tickets: number
  firstRequest: number
  rollBack: boolean
  restores: boolean
  measure?: ReleaseMeasure | null
}
type Screen = {
  running?: Release
  next?: Release
  updater: { answers: boolean; error?: string; version: string; running: string; checking?: string; current?: Request }
  releases: Release[]
  requests: Request[]
  onImage: number
  onOldImage: number
  candidate?: string
  publish: boolean
  building?: string
  canBuild: boolean
  deployFiles: DeployFiles
  measureAfter: number
}

// The Versions screen (design 16.1 to 16.5, versions.png): the running version, the next release and
// whether it waits for you, every kept release with roll back, and the steps of the updater, live.
function Versions() {
  const { data: d, error, reload } = usePoll<Screen>('/api/crew/versions', 3000)
  const [busy, setBusy] = useState(false)
  const [failed, setFailed] = useState('')
  const [confirming, setConfirming] = useState('')
  const [typed, setTyped] = useState('')
  const act = (method: 'POST' | 'DELETE', path: string, body?: Record<string, unknown>) => {
    setBusy(true)
    setFailed('')
    send(method, path, body ?? {}).then(
      () => {
        setBusy(false)
        setConfirming('')
        setTyped('')
        reload()
      },
      (e: Error) => {
        setBusy(false)
        setFailed(e.message)
      },
    )
  }
  const running = d?.running
  const next = d?.next
  const current = d?.updater.current
  return (
    <div className="mx-auto max-w-6xl p-6">
      <PageHead title="Versions">
        Crew collects signals from every project, so Crew is also where Crew is improved. Every change to it ships as a numbered release,
        built once on this machine. You get a notice when one is ready. A release that changes only prompts and harness text installs by
        itself 24 hours later. A release that changes code, or needs a newer machine setup, waits until you press Install: the server is
        one program with the rights of all its parts, so code is installed when you say so. Any kept version is one click away.
      </PageHead>
      <Failed error={error || failed} />
      {d && (
        <>
          <div className="grid gap-3 md:grid-cols-3">
            <Card title="Running now" right={d.updater.answers ? <span className="text-green-700 dark:text-green-400">✓ Healthy</span> : <span className="text-bad">The updater does not answer</span>}>
              <p className="mb-3 text-3xl font-bold" data-running>
                Crew {running?.version ?? d.updater.running ?? '—'}
              </p>
              {running ? (
                <Rows
                  narrow
                  rows={[
                    ['Since', running.installedAt ? ago(running.installedAt) : '—'],
                    ['Server and UI', <span className="font-mono text-xs">{running.images['crew-server']}</span>],
                    ['Runtime image', <span className="font-mono text-xs">{running.images['crew-runtime']}</span>],
                    [
                      'agy',
                      <span className="font-mono text-xs">
                        <span data-free-text>{running.pins.agy ?? '—'}</span> in the image
                      </span>,
                    ],
                    ['Harness', 'built into the image, from harness/ at this version'],
                    ['Database schema', String(running.schema)],
                    ['Published', <SaidView said={copied(running, d.publish)} />],
                    ['Snapshot', 'in the updater\'s folder, before every update'],
                    ['Sandboxes', `${d.onImage} on this image${d.onOldImage > 0 ? `, ${d.onOldImage} still on an older one until they end` : ''}`],
                    ['crew-ctl', <DeployFilesRow f={d.deployFiles ?? {}} />],
                  ]}
                />
              ) : (
                <p className="text-sm text-text-dim">This version was built from a checkout and has no release folder.</p>
              )}
              <p className="mt-3 text-xs text-text-dim">
                One number covers all of these, so an update or a roll back changes them together. Every new microVM uses this image by itself.
              </p>
            </Card>
            <section className={'rounded-lg border bg-bg-raised p-4 ' + (next ? 'border-amber' : 'border-border')} data-next={next?.version}>
              <div className="mb-3 flex items-center gap-2">
                <h2 className="text-xs tracking-wide text-text-dim uppercase">Next release</h2>
                {next && (
                  <span className="ml-auto text-xs font-semibold text-amber-700 dark:text-amber">
                    ■{' '}
                    {next.kind === 'setup'
                      ? 'Needs a newer machine setup'
                      : next.held
                        ? 'Held'
                        : next.installAfter
                          ? `Installs by itself ${when(next.installAfter)}`
                          : 'Waits for Install'}
                  </span>
                )}
              </div>
              {next ? (
                <>
                  <p className="mb-2 text-3xl font-bold">Crew {next.version}</p>
                  <NextCommits notes={next.notes} kind={next.kind} />
                  <ul className="mt-3 space-y-1 text-sm text-text-dim">
                    <li>✓ Built once into images, from {next.commit.slice(0, 12)} on this machine.</li>
                    <li>
                      {next.gate?.passed ? '✓' : '·'} The gate ran on those images: {next.gate?.steps.map((s) => s.name).join('; ') || 'not yet'}. The install
                      test: {next.gate?.installTest ?? '—'}.
                    </li>
                    <li>
                      ✓ <SaidView said={copied(next, d.publish)} />. You got the notice {ago(next.noticeAt)}.
                    </li>
                    <li>
                      ›{' '}
                      {nextWhy(next)}
                    </li>
                    {next.protected.length > 0 && (
                      <li className="text-bad">
                        It touches protected parts: <span data-free-text>{next.protected.join(', ')}</span>.
                      </li>
                    )}
                    <li>· Installed by the updater: checked beside the running version, then switched.</li>
                  </ul>
                  <div className="mt-3 flex flex-wrap gap-2">
                    <button className={mainButton} disabled={busy || !!current} onClick={() => act('POST', `/api/crew/versions/${next.version}/install`)}>
                      Install now
                    </button>
                    {next.kind === 'text' &&
                      (next.held ? (
                        <button className={button} disabled={busy} onClick={() => act('DELETE', `/api/crew/versions/${next.version}/hold`)}>
                          Lift the hold
                        </button>
                      ) : (
                        <button className={button} disabled={busy} onClick={() => act('POST', `/api/crew/versions/${next.version}/hold`)}>
                          Hold
                        </button>
                      ))}
                  </div>
                  <p className="mt-3 font-mono text-xs text-text-dim">
                    agy <span data-free-text>{next.pins.agy ?? '—'}</span> · schema {next.schema}
                  </p>
                </>
              ) : (
                <p className="text-sm text-text-dim">No release waits.</p>
              )}
              <BuildRelease canBuild={d.canBuild} building={d.building ?? ''} onDone={reload} />
            </section>
            <Card title="Updater" right={current ? <span className="text-amber-700 dark:text-amber">Working</span> : <span className="text-text-dim">Idle</span>}>
              <Rows
                narrow
                rows={[
                  ['Runs as', 'a small container of its own, beside the server'],
                  ['Installs', 'a release that passed the gate, checked by its content hash. A release of prompts by itself, 24 hours after the notice. A release of code when you press Install.'],
                  ['How', 'it checks the new version beside the running one, then switches'],
                  ['If it fails', 'before the switch, nothing changes. After it, the updater switches back by itself.'],
                ]}
              />
              {d.updater.error && (
                <p className="mt-2 text-sm text-bad" data-free-text>
                  {clean(d.updater.error)}
                </p>
              )}
              {current && (
                <div className="mt-3" data-current={current.id}>
                  <p className="text-xs tracking-wide text-text-dim uppercase">
                    {current.action === 'gate' ? 'The gate of' : current.action === 'check' ? 'Checking' : 'Installing'} {current.version}
                  </p>
                  <UpdaterSteps steps={current.steps ?? []} />
                  {current.step < 4 && (
                    <button className={button + ' mt-2'} disabled={busy} onClick={() => act('POST', '/api/crew/updater/stop')}>
                      Stop
                    </button>
                  )}
                </div>
              )}
              <p className="mt-4 mb-1 text-xs tracking-wide text-text-dim uppercase">Last changes</p>
              <LastChanges requests={d.requests} />
            </Card>
          </div>
          {d.candidate && <GatePanel key={d.candidate} version={d.candidate} current={current} onPress={reload} />}
          <DailyLook />
          <h2 className="mt-6 mb-2 text-xl font-semibold">
            Releases <span className="text-sm font-normal text-text-dim">newest first</span>
          </h2>
          <table className="w-full text-sm">
            <thead className="text-left text-xs tracking-wide text-text-dim uppercase">
              <tr className="border-b border-border">
                <th className="py-1">Version</th>
                <th>What changed</th>
                <th>Pins</th>
                <th>Released</th>
                <th>Results on it</th>
                <th />
              </tr>
            </thead>
            <tbody>
              {d.releases.map((r) => (
                <tr key={r.version} className="border-b border-border/60 align-top" data-release={r.version}>
                  <td className="py-2 pr-2 font-mono text-xs whitespace-nowrap">
                    {r.version} {r.version === running?.version && <Tag>running</Tag>} {r.byHand && <Tag tone="amber">by hand</Tag>}
                  </td>
                  <td className="py-2 pr-2">
                    <ReleaseWhat r={r} publish={d.publish} />
                  </td>
                  <td className="py-2 pr-2 font-mono text-xs">
                    agy <span data-free-text>{r.pins.agy ?? '—'}</span> · schema {r.schema}
                  </td>
                  <td className="py-2 pr-2 whitespace-nowrap">{ago(r.builtAt)}</td>
                  <td className="py-2 pr-2">
                    {r.tickets > 0 ? `${r.tickets} tickets · ${Math.round((100 * r.firstRequest) / r.tickets)}% first request` : '—'}
                    <ReleaseMeasureLine m={r.measure} installed={!!r.installedAt} window={d.measureAfter} />
                  </td>
                  <td className="py-2 text-right whitespace-nowrap">
                    {r.version === running?.version ? (
                      <span className="text-green-700 dark:text-green-400">✓ running</span>
                    ) : r.rollBack ? (
                      confirming === r.version ? (
                        <span className="inline-flex flex-col items-end gap-1">
                          <span className="max-w-xs text-xs text-bad">
                            {r.version} cannot read the database as it is now: its snapshot is read back, and everything written since is lost. Type{' '}
                            {r.version} to go on.
                          </span>
                          <span className="flex gap-1">
                            <input className="w-24 rounded border border-border bg-bg px-1 font-mono text-xs" value={typed} onChange={(e) => setTyped(e.target.value)} />
                            <button
                              className={button}
                              disabled={busy || typed !== r.version}
                              onClick={() => act('POST', `/api/crew/versions/${r.version}/deploy`, { confirm: typed })}
                            >
                              Roll back
                            </button>
                          </span>
                        </span>
                      ) : (
                        <button
                          className={button}
                          disabled={busy || !!current}
                          onClick={() => (r.restores ? setConfirming(r.version) : act('POST', `/api/crew/versions/${r.version}/deploy`))}
                        >
                          Roll back to this
                        </button>
                      )
                    ) : r.state === 'waiting' ? (
                      <span className="text-amber-700 dark:text-amber">waits</span>
                    ) : (
                      <span className="text-text-dim">{r.installedAt ? '' : 'never installed'}</span>
                    )}
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
          <div className="mt-6 grid gap-3 md:grid-cols-3">
            <Card title="What an update does">
              <ol className="list-decimal space-y-1 pl-5 text-sm">
                <li>Load the images of the new version and check their content hashes</li>
                <li>Dump the database, as the snapshot. Start the new version beside the running version, on a scratch copy</li>
                <li>Health check, and one smoke run in a microVM</li>
                <li>Hold new runs, apply the schema changes in one transaction, and switch over</li>
                <li>Watch the new version for ten minutes. Switch back by itself if it turns unhealthy</li>
                <li>Done. Every new microVM uses the new version's image from now on</li>
              </ol>
            </Card>
            <Card title="What a roll back does">
              <ol className="list-decimal space-y-1 pl-5 text-sm">
                <li>Load the kept images of the chosen version. Nothing is rebuilt</li>
                <li>Restore the copy of the stores, if the stored format differs</li>
                <li>Start the chosen version beside the running version and check it</li>
                <li>Hold new runs and switch over</li>
                <li>Watch the chosen version for ten minutes</li>
                <li>Done. Every new microVM uses the chosen version's image from now on</li>
              </ol>
            </Card>
            <Card title="What protects the way back">
              <Rows
                narrow
                rows={[
                  ['The updater', 'is its own container. It does not need the server, and it never replaces itself.'],
                  ['Checked first', 'a release that cannot start, read the database, or start a microVM never replaces anything'],
                  ['Kept images', 'a roll back switches to them. Nothing is rebuilt.'],
                  ['Running work', 'continues. Each run writes its events to a spool, and the new server reads on and stores them.'],
                  ['Database schema', 'a change is tried on a scratch copy first, and made in two releases'],
                  ['Off the machine', 'the tag of every release is on GitHub, and with gh its notes and images. Backups are on this machine only.'],
                  ['If the app is down', 'one command on the machine rolls back: crew-ctl rollback'],
                ]}
              />
            </Card>
          </div>
        </>
      )}
    </div>
  )
}
