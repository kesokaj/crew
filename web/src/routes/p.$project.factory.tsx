import { createFileRoute, Link } from '@tanstack/react-router'
import { usePoll } from '../lib/poll'
import { roleName, stageName } from '../lib/words'
import { Card, Failed, PageHead, Tag, ago, button } from '../components/ui'

export const Route = createFileRoute('/p/$project/factory')({ component: FactoryLine })

type FactoryLineView = {
  project: { id: string; name: string; key: string; goal: string; state: string; protected: boolean }
  stageCounts: Record<string, number>
  specs: { specKey: string; tier: string; title: string; state: string; emittedKeys: string[] }[]
  recentDecisions: { key: string; specKey?: string; question: string; choice: string; decidedBy: string; state: string; createdAt: string }[]
  offBoardTasks: { id: string; key: string; kind: string; role: string; title: string; state: string }[]
  researchBriefs: { id: number; ticketId: number; artifactPath: string; triggerReason: string; resolved: boolean }[]
  sessions: { id: string; role: string; state: string; taskKey?: string; ticket?: string; readOnly: boolean; tokensIn: number; tokensOut: number }[]
  roomSummary: { channel: string; threadsCount: number; maxAgentHops: number; messages: number }
  trajectorySeries: { runId: string; role: string; model: string; events: number; tokens: number; durationSeconds: number; outcome: string; blobKey?: string; at: string }[]
  storage: { stateDbMode: string; blobDriver: string }
}

const pipelineNodes = [
  { id: 'specs', label: 'Specs', sub: '000 + NNN', to: 'specs' },
  { id: 'backlog', label: 'Backlog', sub: '@coordinator', to: 'board' },
  { id: 'in_progress', label: 'Build', sub: '@builder', to: 'board' },
  { id: 'review', label: 'Review', sub: 'Judge family', to: 'board' },
  { id: 'test', label: 'Checks', sub: 'Throwaway VM', to: 'board' },
  { id: 'done', label: 'Done', sub: 'main', to: 'git' },
] as const

function FactoryLine() {
  const { project } = Route.useParams()
  const enc = encodeURIComponent(project)
  const { data, error } = usePoll<FactoryLineView>(`/api/projects/${enc}/factory-line`, 5000)

  if (!data) {
    return (
      <div className="mx-auto max-w-7xl p-6">
        <PageHead title="Factory Line" />
        <Failed error={error} />
        {!error && <p className="text-sm text-text-dim">Loading factory line…</p>}
      </div>
    )
  }

  const series = data.trajectorySeries.slice().reverse()
  const maxEvents = Math.max(1, ...series.map((s) => s.events))
  const openTasks = data.offBoardTasks.filter((t) => t.state !== 'done' && t.state !== 'cancelled')

  return (
    <div className="mx-auto max-w-7xl space-y-5 p-6">
      <PageHead
        crumbs={
          <>
            <Link to="/" className="text-accent">
              All projects
            </Link>{' '}
            / <span data-free-text>{data.project.name}</span>
          </>
        }
        title="Factory Line"
        actions={
          <div className="flex flex-wrap gap-2">
            <Link to="/p/$project/board" params={{ project }} className={button}>
              Board
            </Link>
            <Link to="/p/$project/specs" params={{ project }} className={button}>
              Specs &amp; WORKFLOW
            </Link>
            <Link to="/p/$project/chat" params={{ project }} className={button}>
              Project Chat
            </Link>
            <Link to="/p/$project/skills" params={{ project }} className={button}>
              Skills Registry
            </Link>
            <Link to="/p/$project/trajectories" params={{ project }} className={button}>
              Trajectories
            </Link>
            <Link to="/storage" className={button}>
              Runtimes &amp; Storage
            </Link>
          </div>
        }
      >
        <span data-free-text>{data.project.goal}</span>
      </PageHead>

      <Failed error={error} />

      <div className="grid gap-3 sm:grid-cols-2 lg:grid-cols-4">
        <div className="rounded-lg border border-border bg-bg-raised p-4">
          <p className="text-xs tracking-wide text-text-dim uppercase">Specs &amp; Decisions</p>
          <p className="mt-1 text-2xl font-semibold">{data.specs.length}</p>
          <p className="text-xs text-text-dim">{data.recentDecisions.length} recent DEC records</p>
        </div>
        <div className="rounded-lg border border-border bg-bg-raised p-4">
          <p className="text-xs tracking-wide text-text-dim uppercase">Off-board Tasks API</p>
          <p className="mt-1 text-2xl font-semibold">{data.offBoardTasks.length}</p>
          <p className="text-xs text-text-dim">{openTasks.length} active · {data.researchBriefs.length} @researcher briefs</p>
        </div>
        <div className="rounded-lg border border-border bg-bg-raised p-4">
          <p className="text-xs tracking-wide text-text-dim uppercase">Unified Channel</p>
          <p className="mt-1 text-2xl font-semibold" data-free-text>
            {data.roomSummary.channel}
          </p>
          <p className="text-xs text-text-dim">
            {data.roomSummary.threadsCount} threads · hop cap {data.roomSummary.maxAgentHops}
          </p>
        </div>
        <div className="rounded-lg border border-border bg-bg-raised p-4">
          <p className="text-xs tracking-wide text-text-dim uppercase">Split Storage</p>
          <p className="mt-1 text-2xl font-semibold uppercase">{data.storage.stateDbMode}</p>
          <p className="text-xs text-text-dim">BlobStore: {data.storage.blobDriver}</p>
        </div>
      </div>

      <Card title="7-Module Pipeline Flow">
        <div className="grid gap-2 sm:grid-cols-3 lg:grid-cols-6">
          {pipelineNodes.map((n, idx) => {
            const count = n.id === 'specs' ? data.specs.length : (data.stageCounts[n.id] ?? 0)
            return (
              <Link
                key={n.id}
                to={`/p/$project/${n.to}`}
                params={{ project }}
                className="flex flex-col justify-between rounded-md border border-border bg-bg p-3 transition-colors hover:border-accent"
              >
                <div className="flex items-center justify-between gap-2">
                  <span className="text-xs font-semibold text-accent">
                    {idx + 1}. {stageName[n.id] ?? n.label}
                  </span>
                  <span className="rounded bg-bg-sunk px-1.5 py-0.5 font-mono text-xs font-semibold">{count}</span>
                </div>
                <span className="mt-2 text-xs text-text-dim">{n.sub}</span>
              </Link>
            )
          })}
        </div>
        <p className="mt-3 text-xs text-text-dim">
          Clean-context circuit breaker: reaching <code>max_tries</code> routes blocked tickets to <code>@researcher</code> (
          <code>{data.researchBriefs.length}</code> briefs recorded).
        </p>
      </Card>

      <div className="grid gap-4 lg:grid-cols-2">
        <Card title="Trajectory Throughput (Last 12 Runs)">
          {series.length === 0 ? (
            <p className="text-sm text-text-dim">No indexed trajectories recorded yet.</p>
          ) : (
            <div className="space-y-2">
              <svg
                viewBox="0 0 480 120"
                className="h-32 w-full rounded border border-border bg-bg p-2"
                role="img"
                aria-label="Trajectory events per run bar chart"
              >
                {series.map((pt, i) => {
                  const slot = 460 / Math.max(1, series.length)
                  const barW = Math.max(12, slot - 8)
                  const barH = Math.max(8, Math.round((pt.events / maxEvents) * 84))
                  const x = Math.round(10 + i * slot)
                  const y = 96 - barH
                  const fill =
                    pt.outcome === 'pass'
                      ? 'var(--chart-2)'
                      : pt.outcome === 'max_tries' || pt.outcome === 'error'
                        ? 'var(--chart-3)'
                        : 'var(--chart-1)'
                  return (
                    <g key={pt.runId}>
                      <rect x={x} y={y} width={barW} height={barH} rx="3" fill={fill} />
                      <text x={x + barW / 2} y="112" textAnchor="middle" fill="var(--muted-foreground)" fontSize="9">
                        {pt.role.slice(0, 5)}
                      </text>
                    </g>
                  )
                })}
              </svg>
              <div className="flex flex-wrap gap-3 text-xs text-text-dim">
                <span>Runs plotted: {series.length}</span>
                <span>Latest role: {series[series.length - 1]?.role ?? '—'}</span>
                <span>Outcome: {series[series.length - 1]?.outcome ?? '—'}</span>
              </div>
            </div>
          )}
        </Card>

        <Card title="ACP Peer Sessions">
          {data.sessions.length === 0 ? (
            <p className="text-sm text-text-dim">No ACP peer sessions active or recently recorded.</p>
          ) : (
            <ul className="space-y-2 text-sm">
              {data.sessions.slice(0, 6).map((s) => (
                <li key={s.id} className="flex flex-wrap items-center justify-between gap-2 rounded border border-border bg-bg px-3 py-2">
                  <div>
                    <span className="font-medium">{roleName(s.role)}</span>
                    {s.taskKey && <span className="ml-2 font-mono text-xs text-text-dim">{s.taskKey}</span>}
                    {s.ticket && <span className="ml-2 font-mono text-xs text-accent">{s.ticket}</span>}
                  </div>
                  <div className="flex items-center gap-1.5">
                    {s.readOnly && <Tag tone="amber">ReadOnly</Tag>}
                    <Tag tone={s.state === 'ended' ? undefined : 'blue'}>{s.state}</Tag>
                  </div>
                </li>
              ))}
            </ul>
          )}
        </Card>
      </div>

      <Card title="Recent Autonomous Decisions (DEC-*)">
        {data.recentDecisions.length === 0 ? (
          <p className="text-sm text-text-dim">No spec decisions recorded yet.</p>
        ) : (
          <ul className="space-y-2 text-sm">
            {data.recentDecisions.map((d) => (
              <li key={d.key} className="rounded border border-border bg-bg p-3">
                <div className="flex flex-wrap items-center justify-between gap-2">
                  <span className="font-mono text-xs font-semibold text-accent">
                    {d.key} {d.specKey ? `· ${d.specKey}` : ''}
                  </span>
                  <span className="text-xs text-text-dim">
                    {d.decidedBy} · {ago(d.createdAt)}
                  </span>
                </div>
                <p className="mt-1 font-medium" data-free-text>
                  {d.choice}
                </p>
                <p className="text-xs text-text-dim" data-free-text>
                  {d.question}
                </p>
              </li>
            ))}
          </ul>
        )}
      </Card>
    </div>
  )
}
