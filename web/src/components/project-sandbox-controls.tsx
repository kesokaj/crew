import { Link } from '@tanstack/react-router'
import { useState } from 'react'
import { send, type ProjectSandboxStatus } from '../lib/api'
import { usePoll } from '../lib/poll'
import { ago, button, Card, Failed, mainButton, Tag } from './ui'

export function ProjectSandboxBar({ project }: { project: string }) {
  const base = `/api/projects/${encodeURIComponent(project)}/sandbox`
  const { data, reload } = usePoll<ProjectSandboxStatus>(base, 5000)
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState('')

  if (!data) return null

  const act = (subpath: string, body: unknown = {}) => {
    setBusy(true)
    setError('')
    send('POST', base + subpath, body)
      .then(() => reload())
      .catch((e: Error) => setError(e.message))
      .finally(() => setBusy(false))
  }

  return (
    <ProjectSandboxBarView
      project={project}
      data={data}
      busy={busy}
      error={error}
      onStart={() => act('/start')}
      onSuspend={() => act('/suspend')}
      onStop={() => act('/stop')}
      onSnapshot={() => act('/snapshot', { label: 'board-snapshot' })}
    />
  )
}

export function ProjectSandboxBarView({
  project,
  data,
  busy,
  error,
  onStart,
  onSuspend,
  onStop,
  onSnapshot,
}: {
  project?: string
  data: ProjectSandboxStatus
  busy: boolean
  error: string
  onStart: () => void
  onSuspend: () => void
  onStop: () => void
  onSnapshot: () => void
}) {
  const stateBadge =
    data.state === 'running' ? (
      <Tag tone="ok">
        UP · {data.activeSessions} ACP {data.activeSessions === 1 ? 'session' : 'sessions'}
      </Tag>
    ) : data.state === 'paused' ? (
      <Tag tone="amber">SUSPENDED · 0% CPU</Tag>
    ) : (
      <Tag>DOWN · 0 MB</Tag>
    )

  return (
    <div
      data-project-sandbox={data.state}
      className="mb-3 flex flex-wrap items-center gap-2 rounded-md border border-border bg-bg-raised px-3 py-2 text-xs"
    >
      <span className="font-semibold text-text">Project Sandbox (agy acp):</span>
      {stateBadge}
      {data.cpus !== undefined && data.memoryGB !== undefined && (
        <span className="font-mono text-text-dim">
          {data.cpus} vCPUs · {data.memoryGB} GB RAM{data.diskGB ? ` · ${data.diskGB} GB disk` : ''}
        </span>
      )}
      {data.sandboxId && <span className="font-mono text-text-dim">{data.sandboxId}</span>}
      {data.imageTag && <span className="font-mono text-text-dim">image: {data.imageTag}</span>}
      <div className="ml-auto flex flex-wrap items-center gap-1.5">
        {data.state !== 'running' && (
          <button className={mainButton} disabled={busy} onClick={onStart}>
            {data.state === 'paused' ? 'Resume VM' : 'Start VM'}
          </button>
        )}
        {data.state === 'running' && (
          <button
            className={button}
            disabled={busy || data.activeSessions > 0}
            title={data.activeSessions > 0 ? 'Wait for active ACP sessions to finish before suspending' : 'Pause microVM at 0% CPU'}
            onClick={onSuspend}
          >
            Suspend
          </button>
        )}
        {data.state !== 'stopped' && (
          <button className={button} disabled={busy} onClick={onStop}>
            Stop · 0 MB
          </button>
        )}
        <button className={button} disabled={busy} onClick={onSnapshot}>
          Snapshot
        </button>
        {project && (
          <Link to="/p/$project/settings" params={{ project }} hash="sandbox" className={button}>
            Configure
          </Link>
        )}
      </div>
      {error && <span className="w-full text-bad">{error}</span>}
    </div>
  )
}

export function ProjectSandboxCard({ project }: { project: string }) {
  const base = `/api/projects/${encodeURIComponent(project)}/sandbox`
  const { data, error, reload } = usePoll<ProjectSandboxStatus>(base, 5000)
  const [busy, setBusy] = useState(false)
  const [failed, setFailed] = useState('')
  const [snapLabel, setSnapLabel] = useState('')

  const act = (subpath: string, body: unknown = {}) => {
    setBusy(true)
    setFailed('')
    send('POST', base + subpath, body)
      .then(() => {
        setSnapLabel('')
        reload()
      })
      .catch((e: Error) => setFailed(e.message))
      .finally(() => setBusy(false))
  }

  return (
    <Card title="Project warm microVM & snapshots (agy acp)">
      <p className="mb-2 text-xs text-text-dim">
        One persistent-home Kata microVM runs <code>agy acp --stdio --process-per-session</code> for this project, multiplexing concurrent
        turns in isolated git worktrees. Idle sandboxes suspend (`0% CPU`) and scale to `0 MB` RAM while keeping `/home/agent` on disk.
      </p>
      {data && (
        <div className="space-y-2 text-sm" data-sandbox-settings={data.state} data-project-sandbox-card={data.state}>
          <p className="flex flex-wrap items-center gap-2">
            <span>State:</span>
            <b>{data.state}</b>
            <span className="text-xs text-text-dim">({data.activeSessions} active ACP sessions)</span>
          </p>
          <p className="font-mono text-xs text-text-dim">
            {data.cpus ?? 4} vCPUs · {data.memoryGB ?? 6} GB RAM · {data.diskGB ?? 20} GB disk · suspend {data.suspendAfterMinutes ?? 5}m · stop{' '}
            {data.stopAfterMinutes ?? 30}m
          </p>
          {data.sandboxId && (
            <p className="text-xs text-text-dim">
              Container: <code className="font-mono">{data.sandboxId}</code>
            </p>
          )}
          {data.imageTag && (
            <p className="text-xs text-text-dim">
              Derived image: <code className="font-mono">{data.imageTag}</code>
            </p>
          )}
          <div className="flex flex-wrap gap-1.5 pt-1">
            {data.state !== 'running' && (
              <button className={mainButton} disabled={busy} onClick={() => act('/start')}>
                {data.state === 'paused' ? 'Resume microVM' : 'Start microVM'}
              </button>
            )}
            {data.state === 'running' && (
              <button className={button} disabled={busy || data.activeSessions > 0} onClick={() => act('/suspend')}>
                Suspend (0% CPU)
              </button>
            )}
            {data.state !== 'stopped' && (
              <button className={button} disabled={busy} onClick={() => act('/stop')}>
                Stop · 0 MB
              </button>
            )}
            <button className={button} disabled={busy} onClick={() => act('/build-image', {})}>
              Build Project Image
            </button>
          </div>

          <div className="mt-3 border-t border-border pt-2">
            <p className="text-xs font-semibold uppercase text-text-dim">Home Snapshots</p>
            <div className="mt-1 flex gap-1.5">
              <input
                type="text"
                aria-label="Snapshot label"
                placeholder="Snapshot label (optional)"
                value={snapLabel}
                onChange={(e) => setSnapLabel(e.target.value)}
                className="min-w-0 flex-1 rounded border border-border bg-bg px-2 py-1 text-xs"
              />
              <button className={button} disabled={busy} onClick={() => act('/snapshot', { label: snapLabel })}>
                Snapshot
              </button>
            </div>
            {data.snapshots.length === 0 ? (
              <p className="mt-1.5 text-xs text-text-dim">No snapshots yet.</p>
            ) : (
              <ul className="mt-2 space-y-1 text-xs">
                {data.snapshots.map((sn) => (
                  <li key={sn.id} className="flex items-center justify-between gap-2 rounded border border-border px-2 py-1">
                    <span className="truncate">
                      <b>{sn.label}</b> · <code className="font-mono">{sn.sha256.slice(0, 10)}</code> · {ago(sn.createdAt)}
                    </span>
                    <button
                      className={button}
                      disabled={busy}
                      onClick={() => act(`/snapshots/${encodeURIComponent(sn.id)}/restore`)}
                    >
                      Restore
                    </button>
                  </li>
                ))}
              </ul>
            )}
          </div>
        </div>
      )}
      <Failed error={error || failed} />
    </Card>
  )
}
