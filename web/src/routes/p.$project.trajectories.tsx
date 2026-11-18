import { createFileRoute, Link } from '@tanstack/react-router'
import { useState } from 'react'
import { send } from '../lib/api'
import { usePoll } from '../lib/poll'
import { roleName } from '../lib/words'
import { button, Failed, PageHead, Tag, when } from '../components/ui'
import { CLIModel, duration, reasons } from '../components/runs'

export const Route = createFileRoute('/p/$project/trajectories')({ component: Trajectories })

type Row = {
  id: string
  subtask: string
  ticket?: string
  title: string
  ticketTitle?: string
  role: string
  cli: string
  model?: string
  effort?: string
  way: string
  conversation: string
  continues?: string
  events: number
  turns: number
  tokens: number
  state: string
  stopReason?: string
  outcome?: string
  startedAt: string
  endedAt?: string
}

type IndexedTrajectory = {
  runId: string
  ticket?: string
  taskKey?: string
  title: string
  role: string
  cli: string
  model: string
  toolVerbs: string[]
  touchedPaths: string[]
  events: number
  turns: number
  tokens: number
  durationSeconds: number
  outcome: string
  blobKey?: string
  startedAt: string
}

type TrajectoryStoreResponse = {
  trajectories: IndexedTrajectory[]
  events: number
  tokens: number
  offloaded: number
  facets: { roles: string[]; tools: string[]; outcomes: string[] }
}

const roles = ['', 'curator', 'lead', 'coordinator', 'builder', 'reviewer', 'tester', 'researcher', 'summary', 'planner']

// Trajectories (design 8.3, 20.1, traj.png): the full record of every agent run in the project.
// Each row says which conversation the run continues, and opens the run.
function Trajectories() {
  const { project } = Route.useParams()
  const enc = encodeURIComponent(project)
  const [role, setRole] = useState('')
  const [q, setQ] = useState('')
  const [search, setSearch] = useState('')
  const [toolFilter, setToolFilter] = useState('')
  const [pathFilter, setPathFilter] = useState('')
  const [outcomeFilter, setOutcomeFilter] = useState('')
  const [archiveErr, setArchiveErr] = useState('')
  const [busyRun, setBusyRun] = useState('')

  const path = `/api/projects/${enc}/runs?role=${role}&q=${encodeURIComponent(search)}`
  const { data, error } = usePoll<{ runs: Row[]; events: number; tokens: number }>(path, 8000)
  const storePath =
    `/api/projects/${enc}/trajectories?q=${encodeURIComponent(search)}` +
    `&role=${encodeURIComponent(role)}&tool=${encodeURIComponent(toolFilter)}` +
    `&path=${encodeURIComponent(pathFilter)}&outcome=${encodeURIComponent(outcomeFilter)}`
  const store = usePoll<TrajectoryStoreResponse>(storePath, 8000)

  const archiveRun = async (runId: string) => {
    setBusyRun(runId)
    setArchiveErr('')
    try {
      await send('POST', `/api/projects/${enc}/trajectories/${encodeURIComponent(runId)}/offload`, {})
      store.reload()
    } catch (e) {
      setArchiveErr((e as Error).message)
    } finally {
      setBusyRun('')
    }
  }

  const live = data?.runs.filter((r) => r.state !== 'ended').length ?? 0
  return (
    <div className="mx-auto max-w-7xl p-6">
      <PageHead
        crumbs={
          <>
            <Link to="/" className="text-accent">
              All projects
            </Link>{' '}
            / {project}
          </>
        }
        title="Trajectories"
      >
        The full record of every agent run in {project}: every message, tool call, and result, in order. Crew writes it while the run
        happens. Nothing depends on the agent choosing to log.
      </PageHead>
      <Failed error={error || store.error || archiveErr} />
      <div className="mb-4 grid gap-3 md:grid-cols-3">
        <div className="rounded-lg border border-border bg-bg-raised p-4">
          <p className="text-xs tracking-wide text-text-dim uppercase">Runs recorded</p>
          <p className="text-3xl font-semibold">{data?.runs.length ?? '…'}</p>
          <p className="text-sm text-text-dim">{live} live now</p>
        </div>
        <div className="rounded-lg border border-border bg-bg-raised p-4">
          <p className="text-xs tracking-wide text-text-dim uppercase">Events</p>
          <p className="text-3xl font-semibold">{data?.events.toLocaleString() ?? '…'}</p>
          <p className="text-sm text-text-dim">{data?.tokens.toLocaleString()} tokens</p>
        </div>
        <div className="rounded-lg border border-border bg-bg-raised p-4 text-sm">
          <p className="mb-1 text-xs tracking-wide text-text-dim uppercase">BlobStore Offload</p>
          <p>
            Indexed runs: <b>{store.data?.trajectories.length ?? 0}</b> · Archived (.jsonl.zst):{' '}
            <b>{store.data?.offloaded ?? 0}</b>
          </p>
          <p className="mt-1 text-xs text-text-dim">Secrets are scrubbed before indexing or archiving.</p>
        </div>
      </div>
      <div className="mb-3 flex flex-wrap items-center gap-2">
        <div className="inline-flex flex-wrap overflow-hidden rounded-md border border-border text-sm" role="tablist">
          {roles.map((r) => (
            <button key={r} className={'px-3 py-1 ' + (role === r ? 'bg-text text-bg' : 'bg-bg-raised')} onClick={() => setRole(r)}>
              {r ? roleName(r) : 'All'}
            </button>
          ))}
        </div>
        <select
          aria-label="Filter by tool"
          value={toolFilter}
          onChange={(e) => setToolFilter(e.target.value)}
          className="rounded-md border border-border bg-bg-raised px-2 py-1 text-sm"
        >
          <option value="">All tools</option>
          {(store.data?.facets.tools ?? []).map((tl) => (
            <option key={tl} value={tl}>
              tool: {tl}
            </option>
          ))}
        </select>
        <input
          aria-label="Filter by touched path"
          placeholder="Path facet (e.g. store.go)"
          value={pathFilter}
          onChange={(e) => setPathFilter(e.target.value)}
          className="w-48 rounded-md border border-border bg-bg-raised px-2 py-1 text-sm"
        />
        <select
          aria-label="Filter by outcome"
          value={outcomeFilter}
          onChange={(e) => setOutcomeFilter(e.target.value)}
          className="rounded-md border border-border bg-bg-raised px-2 py-1 text-sm"
        >
          <option value="">All outcomes</option>
          <option value="pass">pass</option>
          <option value="max_tries">max_tries</option>
          <option value="research">research</option>
          <option value="error">error</option>
          <option value="live">live</option>
        </select>
        <form
          className="ml-auto flex gap-2"
          onSubmit={(e) => {
            e.preventDefault()
            setSearch(q)
          }}
        >
          <input
            aria-label="Search"
            value={q}
            onChange={(e) => setQ(e.target.value)}
            placeholder="Search runs, tools, paths"
            className="w-64 rounded-md border border-border bg-bg-raised px-2 py-1 text-sm"
          />
        </form>
      </div>
      <div className="overflow-x-auto rounded-lg border border-border bg-bg-raised">
        <table className="w-full text-sm">
          <thead className="text-left text-xs tracking-wide text-text-dim uppercase">
            <tr className="border-b border-border">
              {['Run', 'Subtask', 'Agent', 'Conversation', 'Events', 'Turns', 'Tokens', 'Started', 'Outcome'].map((h) => (
                <th key={h} className="px-2 py-2">
                  {h}
                </th>
              ))}
            </tr>
          </thead>
          <tbody>
            {data?.runs.map((r) => (
              <tr key={r.id} className="border-b border-border/60 align-top" data-run={r.id}>
                <td className="px-2 py-1.5 font-mono text-xs whitespace-nowrap">
                  <Link to="/runs/$id" params={{ id: r.id }} className="text-accent">
                    r_{r.id.slice(0, 6)}
                  </Link>
                  {r.subtask.includes('-') && <div className="text-text-dim">{r.subtask}</div>}
                </td>
                <td className="px-2 py-1.5">
                  {r.title}
                  {r.ticketTitle && <div className="text-xs text-text-dim">{r.ticketTitle}</div>}
                </td>
                <td className="px-2 py-1.5">
                  {roleName(r.role)}
                  <div>
                    <CLIModel r={r} />
                  </div>
                </td>
                <td className="px-2 py-1.5 font-mono text-xs">
                  {r.way === 'new' ? 'new' : 'continues'}
                  <div className="text-text-dim">{r.conversation.slice(0, 8)}</div>
                </td>
                <td className="px-2 py-1.5">{r.events}</td>
                <td className="px-2 py-1.5">{r.turns}</td>
                <td className="px-2 py-1.5">{r.tokens.toLocaleString()}</td>
                <td className="px-2 py-1.5 whitespace-nowrap">
                  {when(r.startedAt)}
                  <div className="text-xs text-text-dim">{duration(r.startedAt, r.endedAt)}</div>
                </td>
                <td className="px-2 py-1.5">
                  {r.state !== 'ended' ? (
                    <span className="text-accent">● live</span>
                  ) : r.stopReason === 'ok' ? (
                    <span className="text-ok">✓ {r.outcome ? r.outcome.replaceAll('_', ' ') : 'finished'}</span>
                  ) : (
                    <span className="text-amber-text">{reasons[r.stopReason ?? '']?.text ?? r.stopReason}</span>
                  )}
                </td>
              </tr>
            ))}
          </tbody>
        </table>
        {data && data.runs.length === 0 && <p className="p-3 text-sm text-text-dim">No run matches.</p>}
      </div>

      <div className="mt-5 rounded-lg border border-border bg-bg-raised p-4">
        <div className="mb-3 flex flex-wrap items-center justify-between gap-2">
          <h2 className="text-sm font-semibold">Indexed Trajectory Store &amp; BlobStore Archive</h2>
          <span className="text-xs text-text-dim">
            Matches: {store.data?.trajectories.length ?? 0} · Offloaded: {store.data?.offloaded ?? 0}
          </span>
        </div>
        {!store.data || store.data.trajectories.length === 0 ? (
          <p className="text-sm text-text-dim">No indexed trajectories match these filters.</p>
        ) : (
          <div className="space-y-2">
            {store.data.trajectories.map((tr) => (
              <div
                key={tr.runId}
                className="flex flex-wrap items-center justify-between gap-3 rounded-md border border-border bg-bg p-3 text-sm"
              >
                <div className="min-w-0 space-y-1">
                  <div className="flex flex-wrap items-center gap-2">
                    <Link to="/runs/$id" params={{ id: tr.runId }} className="font-mono text-xs font-semibold text-accent">
                      r_{tr.runId.slice(0, 8)}
                    </Link>
                    <Tag>{tr.role}</Tag>
                    <Tag tone={tr.outcome === 'pass' ? 'ok' : tr.outcome === 'max_tries' || tr.outcome === 'error' ? 'bad' : 'blue'}>
                      {tr.outcome}
                    </Tag>
                    {tr.ticket && <Tag mono>{tr.ticket}</Tag>}
                    {tr.taskKey && <Tag mono>{tr.taskKey}</Tag>}
                  </div>
                  <p className="truncate font-medium" data-free-text>
                    {tr.title || tr.runId}
                  </p>
                  <p className="truncate font-mono text-xs text-text-dim" data-free-text>
                    tools: {tr.toolVerbs.join(', ') || 'none'} · paths: {tr.touchedPaths.join(', ') || 'none'} · {tr.tokens} tokens
                  </p>
                </div>
                <div className="flex items-center gap-2">
                  {tr.blobKey ? (
                    <span data-blob-key={tr.blobKey} className="inline-flex items-center gap-1.5">
                      <Tag tone="ok">Archived (.jsonl.zst)</Tag>
                    </span>
                  ) : (
                    <button
                      type="button"
                      data-offload-run={tr.runId}
                      disabled={busyRun === tr.runId}
                      className={button}
                      onClick={() => void archiveRun(tr.runId)}
                    >
                      {busyRun === tr.runId ? 'Archiving…' : 'Archive to BlobStore'}
                    </button>
                  )}
                </div>
              </div>
            ))}
          </div>
        )}
      </div>

      <p className="mt-3 text-sm text-text-dim">
        Open a run to read its trajectory, its prompt, and the conversation it belongs to. A fix round continues the builder's
        conversation, so it remembers what it did. A reviewer or a tester starts new every time.
      </p>
    </div>
  )
}
