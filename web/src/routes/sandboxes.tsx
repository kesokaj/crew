import { createFileRoute, Link } from '@tanstack/react-router'
import { useState } from 'react'
import { send, type AllSandboxesView, type ProjectSandboxOverview } from '../lib/api'
import { usePoll } from '../lib/poll'
import { ago, button, Card, Failed, mainButton, PageHead, Tag } from '../components/ui'
import { roleName } from '../lib/words'

export const Route = createFileRoute('/sandboxes')({ component: SandboxesScreen })

const gb = (mb: number) => `${(mb / 1024).toFixed(mb % 1024 === 0 ? 0 : 1)} GB`
const snapSize = (bytes: number) => (bytes <= 0 ? '0 MB' : bytes < 1024 * 1024 ? `${Math.max(1, Math.round(bytes / 1024))} KB` : `${(bytes / (1024 * 1024)).toFixed(1)} MB`)

function SandboxesScreen() {
  const { data, error, reload } = usePoll<AllSandboxesView>('/api/sandboxes', 5000)
  const [busyKey, setBusyKey] = useState('')
  const [actionErr, setActionErr] = useState('')

  const actOnProject = (project: string, subpath: string, body: unknown = {}) => {
    const key = `${project}:${subpath}`
    setBusyKey(key)
    setActionErr('')
    send('POST', `/api/projects/${encodeURIComponent(project)}/sandbox${subpath}`, body)
      .then(() => reload())
      .catch((e: Error) => setActionErr(e.message))
      .finally(() => setBusyKey(''))
  }

  const saveMaxTickets = (max: number) => {
    setBusyKey('machine:max-tickets')
    setActionErr('')
    send('PUT', '/api/capacity/active-tickets', { max })
      .then(() => reload())
      .catch((e: Error) => setActionErr(e.message))
      .finally(() => setBusyKey(''))
  }

  const saveMaxRuns = (slots: number) => {
    setBusyKey('machine:max-runs')
    setActionErr('')
    send('PUT', '/api/models/slots', { cli: 'agy', slots })
      .then(() => reload())
      .catch((e: Error) => setActionErr(e.message))
      .finally(() => setBusyKey(''))
  }

  return (
    <div className="mx-auto max-w-6xl p-6">
      <PageHead title="Sandboxes">
        Each active project has one warm Kata microVM (<code>crew-proj-&lt;key&gt;</code>) running <code>agy acp --stdio --process-per-session</code>{' '}
        with its persistent <code>/home/agent</code> on disk. Check runs, builds, terminals, hosted apps, and conversations run in throwaway microVMs.
      </PageHead>
      <Failed error={error || actionErr} />
      {data && (
        <SandboxesScreenView
          data={data}
          busyKey={busyKey}
          onProjectAction={actOnProject}
          onSaveMaxTickets={saveMaxTickets}
          onSaveMaxRuns={saveMaxRuns}
        />
      )}
    </div>
  )
}

export function SandboxesScreenView({
  data,
  busyKey,
  onProjectAction,
  onSaveMaxTickets,
  onSaveMaxRuns,
}: {
  data: AllSandboxesView
  busyKey: string
  onProjectAction: (project: string, subpath: string, body?: unknown) => void
  onSaveMaxTickets: (max: number) => void
  onSaveMaxRuns?: (slots: number) => void
}) {
  const m = data.machine
  const runningVMs = data.projectSandboxes.filter((p) => p.state === 'running').length
  const pausedVMs = data.projectSandboxes.filter((p) => p.state === 'paused').length
  const stoppedVMs = data.projectSandboxes.filter((p) => p.state === 'stopped').length
  const memShare = m.roomMemMB > 0 ? Math.min(100, (100 * m.usedMemMB) / m.roomMemMB) : 0
  const cpuShare = m.roomCPUs > 0 ? Math.min(100, (100 * m.usedCPUs) / m.roomCPUs) : 0

  return (
    <div className="space-y-6" data-sandboxes-screen>
      <div className="grid gap-3 md:grid-cols-3">
        <Card title="Machine capacity">
          <p className="text-sm">
            <b>{gb(m.usedMemMB)}</b> of <b>{gb(m.roomMemMB)}</b> memory given out
            {m.totalMemMB > 0 && <span className="text-text-dim"> ({gb(m.totalMemMB)} total)</span>}
          </p>
          <div className="mt-1.5 h-1.5 rounded bg-bg-sunk">
            <div className="h-1.5 rounded bg-accent" style={{ width: `${memShare}%` }} />
          </div>
          <p className="mt-3 text-sm">
            <b>{m.usedCPUs}</b> of <b>{m.roomCPUs || '—'}</b> virtual CPUs
            {m.cores > 0 && <span className="text-text-dim"> · {m.cores} physical cores</span>}
          </p>
          <div className="mt-1.5 h-1.5 rounded bg-bg-sunk">
            <div className="h-1.5 rounded bg-accent" style={{ width: `${cpuShare}%` }} />
          </div>
        </Card>

        <Card title="Active-ticket concurrency">
          <p className="text-2xl font-semibold">
            {m.activeTickets} of {m.maxActiveTickets} <span className="text-sm font-normal text-text-dim">active tickets</span>
          </p>
          {m.maxActiveTickets < m.setMaxActiveTickets && (
            <p className="mt-1 text-xs text-amber-text">
              Lowered from {m.setMaxActiveTickets}: a model group is close to its limit.
            </p>
          )}
          <div className="mt-3 flex flex-wrap items-center gap-2 text-xs">
            <span className="text-text-dim">Machine limit:</span>
            <button
              className={button + ' min-w-8 px-2 py-0.5'}
              disabled={busyKey !== '' || m.setMaxActiveTickets <= 1}
              aria-label="Decrease machine active tickets limit"
              onClick={() => onSaveMaxTickets(Math.max(1, m.setMaxActiveTickets - 1))}
            >
              −
            </button>
            <span className="font-mono font-semibold" data-machine-max-tickets>
              {m.setMaxActiveTickets}
            </span>
            <button
              className={button + ' min-w-8 px-2 py-0.5'}
              disabled={busyKey !== '' || m.setMaxActiveTickets >= 64}
              aria-label="Increase machine active tickets limit"
              onClick={() => onSaveMaxTickets(Math.min(64, m.setMaxActiveTickets + 1))}
            >
              +
            </button>
            <Link to="/settings" className="ml-auto text-accent underline">
              Machine settings
            </Link>
          </div>
          {m.setMaxConcurrentRuns !== undefined && onSaveMaxRuns && (
            <div className="mt-2 flex flex-wrap items-center gap-2 text-xs">
              <span className="text-text-dim">Concurrent runs ({m.runningRuns ?? 0}/{m.maxConcurrentRuns ?? m.setMaxConcurrentRuns}):</span>
              <button
                className={button + ' min-w-8 px-2 py-0.5'}
                disabled={busyKey !== '' || m.setMaxConcurrentRuns <= 1}
                aria-label="Decrease concurrent runs limit"
                onClick={() => onSaveMaxRuns(Math.max(1, (m.setMaxConcurrentRuns ?? 8) - 1))}
              >
                −
              </button>
              <span className="font-mono font-semibold" data-machine-max-runs>
                {m.setMaxConcurrentRuns}
              </span>
              <button
                className={button + ' min-w-8 px-2 py-0.5'}
                disabled={busyKey !== '' || m.setMaxConcurrentRuns >= 64}
                aria-label="Increase concurrent runs limit"
                onClick={() => onSaveMaxRuns(Math.min(64, (m.setMaxConcurrentRuns ?? 8) + 1))}
              >
                +
              </button>
            </div>
          )}
          <p className="mt-2 text-xs text-text-dim">
            Each project also caps its own concurrent tickets (default 4) in its project settings.
          </p>
        </Card>

        <Card title="Sandboxes summary">
          <dl className="grid grid-cols-[1fr_auto] gap-x-3 gap-y-1 text-sm">
            <dt className="text-text-dim">Warm project microVMs (running)</dt>
            <dd className="font-mono font-semibold">{runningVMs}</dd>
            <dt className="text-text-dim">Suspended project microVMs (0% CPU)</dt>
            <dd className="font-mono font-semibold">{pausedVMs}</dd>
            <dt className="text-text-dim">Stopped project microVMs (0 MB)</dt>
            <dd className="font-mono">{stoppedVMs}</dd>
            <dt className="text-text-dim">Ephemeral &amp; check sandboxes</dt>
            <dd className="font-mono font-semibold">{data.ephemeralSandboxes.length}</dd>
            <dt className="text-text-dim">Waiting for room</dt>
            <dd className="font-mono">{data.waiting.length}</dd>
          </dl>
        </Card>
      </div>

      <section aria-label="Project microVMs">
        <h2 className="mb-3 text-xl font-semibold">
          Project microVMs <span className="text-sm font-normal text-text-dim">one persistent-home Kata VM per project</span>
        </h2>
        {data.projectSandboxes.length === 0 ? (
          <p className="text-sm text-text-dim">No projects yet.</p>
        ) : (
          <div className="space-y-3">
            {data.projectSandboxes.map((p) => (
              <ProjectSandboxRow key={p.projectId} p={p} busy={busyKey !== ''} onAction={onProjectAction} />
            ))}
          </div>
        )}
      </section>

      <section aria-label="Ephemeral sandboxes">
        <h2 className="mb-2 text-xl font-semibold">
          Ephemeral, check, terminal &amp; hosted sandboxes{' '}
          <span className="text-sm font-normal text-text-dim">{data.ephemeralSandboxes.length} active</span>
        </h2>
        {data.ephemeralSandboxes.length === 0 ? (
          <p className="text-sm text-text-dim">No ephemeral sandbox is running right now.</p>
        ) : (
          <div className="overflow-x-auto rounded-lg border border-border bg-bg-raised">
            <table className="w-full text-sm" aria-label="Active ephemeral sandboxes">
              <thead className="border-b border-border text-left text-xs text-text-dim uppercase">
                <tr>
                  <th className="px-3 py-2 font-normal">Container</th>
                  <th className="px-3 py-2 font-normal">Kind</th>
                  <th className="px-3 py-2 font-normal">Project</th>
                  <th className="px-3 py-2 font-normal">Working on</th>
                  <th className="px-3 py-2 font-normal">CPU</th>
                  <th className="px-3 py-2 font-normal">Memory</th>
                  <th className="px-3 py-2 font-normal">Started</th>
                </tr>
              </thead>
              <tbody>
                {data.ephemeralSandboxes.map((ep) => (
                  <tr key={ep.id} className="border-b border-border last:border-b-0" data-ephemeral-sandbox={ep.id}>
                    <td className="px-3 py-2 font-mono text-xs">{ep.container}</td>
                    <td className="px-3 py-2">
                      <Tag>{ep.kind}</Tag>
                    </td>
                    <td className="px-3 py-2 font-semibold">{ep.project || '—'}</td>
                    <td className="px-3 py-2">
                      <span data-free-text>{ep.detail || ep.kind}</span>
                    </td>
                    <td className="px-3 py-2 font-mono text-xs">
                      {ep.cpuPercent > 0 ? `${ep.cpuPercent.toFixed(1)}% · ` : ''}
                      {ep.cpus} vCPUs
                    </td>
                    <td className="px-3 py-2 font-mono text-xs">
                      {ep.memUsedMB} / {ep.memLimitMB} MB
                    </td>
                    <td className="px-3 py-2 text-xs text-text-dim">{ago(ep.startedAt)}</td>
                  </tr>
                ))}
              </tbody>
            </table>
          </div>
        )}
      </section>

      {data.waiting.length > 0 && (
        <section aria-label="Waiting for room on the machine">
          <h2 className="mb-2 text-lg font-semibold">Waiting for room</h2>
          <ol className="space-y-1">
            {data.waiting.map((w) => (
              <li key={w.key} className="rounded-md border border-border bg-bg-raised px-3 py-2 text-sm">
                <b>{w.project ?? 'Crew'}</b> · {w.ticket ? `${w.ticket} ` : ''}
                <span data-free-text>{w.what}</span>
                <span className="ml-2 text-xs text-text-dim">
                  ({w.size} sandbox · waiting {ago(w.since)})
                </span>
              </li>
            ))}
          </ol>
        </section>
      )}
    </div>
  )
}

function ProjectSandboxRow({
  p,
  busy,
  onAction,
}: {
  p: ProjectSandboxOverview
  busy: boolean
  onAction: (project: string, subpath: string, body?: unknown) => void
}) {
  const badge =
    p.state === 'running' ? (
      <Tag tone="ok">
        UP · {p.activeSessions} ACP {p.activeSessions === 1 ? 'session' : 'sessions'}
      </Tag>
    ) : p.state === 'paused' ? (
      <Tag tone="amber">SUSPENDED · 0% CPU</Tag>
    ) : (
      <Tag>DOWN · 0 MB</Tag>
    )

  const diskLimitMB = p.diskGB * 1024
  const diskPct = diskLimitMB > 0 ? Math.min(100, (100 * p.diskUsedMB) / diskLimitMB) : 0

  return (
    <div
      data-project-vm={p.project}
      data-vm-state={p.state}
      className="rounded-lg border border-border bg-bg-raised p-4 text-sm"
    >
      <div className="flex flex-wrap items-center gap-2">
        <Link
          to="/p/$project/board"
          params={{ project: p.project }}
          className="text-base font-semibold hover:text-accent hover:underline"
        >
          {p.project}
        </Link>
        <span className="font-mono text-xs text-text-dim">{p.projectKey}</span>
        {badge}
        <span className="font-mono text-xs text-text-dim">{p.container}</span>
        {p.imageTag && <span className="font-mono text-xs text-text-dim">image: {p.imageTag}</span>}
        <div className="ml-auto flex flex-wrap items-center gap-1.5">
          {p.state !== 'running' && (
            <button className={mainButton} disabled={busy} onClick={() => onAction(p.project, '/start')}>
              {p.state === 'paused' ? 'Resume VM' : 'Start VM'}
            </button>
          )}
          {p.state === 'running' && (
            <button
              className={button}
              disabled={busy || p.activeSessions > 0}
              title={p.activeSessions > 0 ? 'Wait for active ACP sessions to finish before suspending' : 'Pause microVM at 0% CPU'}
              onClick={() => onAction(p.project, '/suspend')}
            >
              Suspend
            </button>
          )}
          {p.state !== 'stopped' && (
            <button className={button} disabled={busy} onClick={() => onAction(p.project, '/stop')}>
              Stop · 0 MB
            </button>
          )}
          <button className={button} disabled={busy} onClick={() => onAction(p.project, '/snapshot', { label: 'sandboxes-screen' })}>
            Snapshot
          </button>
          <Link to="/p/$project/settings" params={{ project: p.project }} hash="sandbox" className={button}>
            Configure
          </Link>
        </div>
      </div>

      <div className="mt-3 grid gap-3 sm:grid-cols-2 lg:grid-cols-4">
        <div className="rounded border border-border/70 bg-bg px-3 py-2">
          <p className="text-xs text-text-dim uppercase">CPU</p>
          <p className="mt-0.5 font-mono text-sm font-semibold">
            {p.state === 'running' ? `${p.cpuPercent.toFixed(1)}%` : '0%'} · {p.usedCPUs} / {p.cpus} vCPUs
          </p>
        </div>
        <div className="rounded border border-border/70 bg-bg px-3 py-2">
          <p className="text-xs text-text-dim uppercase">Memory</p>
          <p className="mt-0.5 font-mono text-sm font-semibold">
            {p.memUsedMB} / {p.memLimitMB} MB <span className="font-normal text-text-dim">({p.memoryGB} GB)</span>
          </p>
        </div>
        <div className="rounded border border-border/70 bg-bg px-3 py-2">
          <p className="text-xs text-text-dim uppercase">Persistent /home/agent disk</p>
          <p className="mt-0.5 font-mono text-sm font-semibold">
            {p.diskUsedMB} MB / {p.diskGB} GB
          </p>
          <div className="mt-1 h-1 rounded bg-bg-sunk">
            <div className="h-1 rounded bg-accent" style={{ width: `${diskPct}%` }} />
          </div>
          <p className="mt-1 text-[11px] text-text-dim">
            {p.snapshotsCount} / {p.maxSnapshots} snapshots ({snapSize(p.snapshotsBytes)})
          </p>
        </div>
        <div className="rounded border border-border/70 bg-bg px-3 py-2">
          <p className="text-xs text-text-dim uppercase">Concurrency &amp; idle timers</p>
          <p className="mt-0.5 font-mono text-sm font-semibold">
            {p.activeTickets} / {p.maxActiveTickets} active tickets
          </p>
          <p className="mt-1 text-[11px] text-text-dim">
            suspend {p.suspendAfterMinutes}m · stop {p.stopAfterMinutes}m
            {p.lastActiveAt ? ` · active ${ago(p.lastActiveAt)} ago` : ''}
          </p>
        </div>
      </div>

      {p.runs.length > 0 && (
        <div className="mt-3 border-t border-border pt-2">
          <p className="mb-1 text-xs text-text-dim uppercase">Active runs in this project</p>
          <div className="flex flex-wrap gap-1.5">
            {p.runs.map((r) => (
              <Link
                key={r.run}
                to={r.ticket ? '/p/$project/tickets/$key' : '/runs/$id'}
                params={r.ticket ? { project: p.project, key: r.ticket } : { id: r.run }}
                className="inline-flex items-center gap-1.5 rounded border border-border bg-bg px-2 py-1 text-xs hover:border-accent"
              >
                <span className="text-accent">●</span>
                <b>{roleName(r.role)}</b>
                {r.ticket && <span className="font-mono">{r.ticket}</span>}
                <span className="text-text-dim" data-free-text>
                  {r.title}
                </span>
              </Link>
            ))}
          </div>
        </div>
      )}
    </div>
  )
}
