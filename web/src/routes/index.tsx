import { createFileRoute, Link, Navigate } from '@tanstack/react-router'
import { useEffect, useState } from 'react'
import { getJSON, send, type ActiveTicketsSummary, type CLIState, type HoldState, type OverviewSandbox } from '../lib/api'
import { usePoll } from '../lib/poll'
import { button, Failed, mainButton, PageHead, Tag, when } from '../components/ui'
import { tokens } from '../lib/tokens'
import { Feed, stripBackticks } from '../components/activity'
import { Lead, type LeadPanel, type Room } from '../components/overview-panels'
import { modeName, stageName } from '../lib/words'

export function formatCardModels(models: string[]): { shown: string; extra: number } {
  if (models.length === 0) return { shown: 'no run yet', extra: 0 }
  if (models.length <= 2) return { shown: models.join(' + '), extra: 0 }
  return { shown: models.slice(0, 2).join(' + '), extra: models.length - 2 }
}

export const Route = createFileRoute('/')({ component: Home })

type OverviewProject = {
  name: string
  key: string
  goal: string
  state: string
  pauseReason?: string
  byStage: Record<string, number>
  working: number
  tokens: number
  models: string[]
  github: boolean
  network: string
  protected?: boolean
  locked?: boolean
}

const stageOrder = ['planning', 'backlog', 'analysis', 'in_progress', 'review', 'test', 'e2e', 'verification', 'done']

type Hold = { data: HoldState | null; reload: () => void }

function HoldButtons({ hold }: { hold: Hold }) {
  const { data, reload } = hold
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState('')
  const act = (path: string) => {
    setBusy(true)
    setError('')
    send('POST', path)
      .catch((e: Error) => setError(e.message))
      .finally(() => {
        setBusy(false)
        reload()
      })
  }
  if (!data) return null
  const failed = error && <span className="self-center text-sm text-bad">{error}</span>
  const paused = !!data.state.pausedAt
  return (
    <>
      {failed}
      {paused ? (
        <button className={mainButton} disabled={busy} onClick={() => act('/api/crew/resume')}>
          Resume
        </button>
      ) : (
        <button className={button} disabled={busy} onClick={() => act('/api/crew/pause')}>
          Pause all
        </button>
      )}
      {data.state.why === 'stop' ? null : (
        <button
          className={button}
          disabled={busy}
          onClick={() => window.confirm('Stop all: every run ends now, and its conversation is kept. Resume continues them.') && act('/api/crew/stop')}
        >
          Stop all
        </button>
      )}
    </>
  )
}

function HoldBanner({ hold }: { hold: Hold }) {
  const { data } = hold
  if (!data?.held) return null
  const s = data.state
  return (
    <section data-hold className="mb-4 rounded-lg border border-bad bg-bg-raised p-3 text-sm">
      {s.pausedAt ? (
        <p>
          <b>{s.why === 'stop' ? 'Crew is stopped' : 'Crew is paused'}</b> since {when(s.pausedAt)}
          {s.pausedBy === 'operator' ? ' by you' : ''}.{' '}
          {s.why === 'stop' ? 'Every run was ended with its conversation kept; Resume continues them.' : 'No new run starts, and running runs finish.'} New features and Crew's own
          changes of a model wait, and their clocks move on by the pause.
        </p>
      ) : s.bootFailed ? (
        <p>
          <b>MicroVMs do not start</b>: {s.bootFailed}. No new run starts until one boots, or until you resume in the{' '}
          <Link to="/inbox" className="underline">
            inbox
          </Link>
          .
        </p>
      ) : s.diskFull ? (
        <p>
          <b>The disk is too full</b>: {s.diskFull}. No new run starts until there is room; Crew says so here and in the inbox.
        </p>
      ) : s.restarting ? (
        <p>
          <b>The machine restarts for security updates.</b> No new run starts while it goes down; Crew starts again with it, and runs continue from their pushed branches.
        </p>
      ) : (
        <p>
          <b>A restart for security updates waits</b>
          {s.rebootWanted ? ` since ${when(s.rebootWanted)}` : ''}: it found no quiet hour in 7 days, so no new run starts, and the machine restarts once the running ones finished.
        </p>
      )}
    </section>
  )
}

function Home() {
  const [clis, setClis] = useState<CLIState[] | null>(null)
  const { data, error } = usePoll<{ projects: OverviewProject[]; lead: LeadPanel; activeTickets?: ActiveTicketsSummary; room: Room | null; sandboxes?: OverviewSandbox[] }>(
    '/api/overview',
    10000,
  )
  const hold = usePoll<HoldState>('/api/crew/hold', 5000)
  useEffect(() => {
    getJSON<{ clis: CLIState[] }>('/api/clis').then(
      (r) => setClis(r.clis),
      () => setClis([]),
    )
  }, [])
  if (clis && clis.length > 0 && !clis.some((c) => c.setUp && c.state === 'signed_in')) return <Navigate to="/setup" />
  return (
    <div className="mx-auto max-w-6xl p-6">
      <PageHead
        title="All projects"
        actions={
          <>
            <HoldButtons hold={hold} />
            <Link to="/plans" className="rounded-md bg-text px-3 py-1.5 text-sm font-semibold text-bg">
              Start from a plan
            </Link>
          </>
        }
      >
        Each project runs the agent CLI <code>agy</code> inside its own warm Kata microVM (<code>crew-proj-&lt;key&gt;</code>) on your subscription. Projects share nothing with each
        other.
      </PageHead>
      <Failed error={error} />
      <HoldBanner hold={hold} />
      {data && <Lead lead={data.lead} activeTickets={data.activeTickets} room={data.room} sandboxes={data.sandboxes ?? []} />}
      <h2 className="mb-3 text-xl font-semibold">Projects</h2>
      {data && data.projects.length === 0 && <p className="text-text-dim">No projects yet. A project starts from a plan.</p>}
      <div className="grid gap-3 md:grid-cols-2 lg:grid-cols-3">
        {data?.projects.map((p) => {
          const total = Object.entries(p.byStage).reduce((n, [s, c]) => n + (s === 'closed' ? 0 : c), 0)
          const done = p.byStage.done ?? 0
          const waitsOK = p.state === 'planning' && (p.byStage.planning ?? 0) > 0
          const m = formatCardModels(p.models)
          return (
            <Link
              key={p.name}
              to="/p/$project/board"
              params={{ project: p.name }}
              className={'block rounded-lg border bg-bg-raised p-3 text-sm ' + (waitsOK || p.state === 'paused' ? 'border-amber' : 'border-border')}
            >
              <p className="flex items-center font-mono text-xs text-text-dim">
                <span>
                  {p.name} · {p.key}
                  {p.state !== 'running' && <span className="ml-2">{p.state}</span>}
                </span>
                {p.protected && (
                  <span className="ml-auto" data-protected={p.locked ? 'locked' : 'on'}>
                    <Tag tone="blue">Protected</Tag>
                  </span>
                )}
              </p>
              <p className="mt-1">{p.goal ? <span data-free-text>{stripBackticks(p.goal)}</span> : <span className="text-text-dim">No goal written yet.</span>}</p>
              {p.working > 0 && (
                <p className="mt-1 text-accent">
                  ● {p.working} {p.working === 1 ? 'agent' : 'agents'} working
                </p>
              )}
              {waitsOK && <p className="mt-1 text-amber-text">■ {p.byStage.planning} tickets wait for your OK</p>}
              {p.state === 'paused' && <p className="mt-1 text-amber-text">Paused: {p.pauseReason}</p>}
              <div className="mt-2 flex flex-wrap gap-1">
                {stageOrder
                  .filter((s) => p.byStage[s] !== undefined)
                  .map((s) => (
                    <Tag key={s}>
                      <b className="text-text">{p.byStage[s]}</b> {stageName[s]}
                    </Tag>
                  ))}
              </div>
              <div className="mt-2 h-1 rounded bg-bg-sunk">
                <div className="h-1 rounded bg-accent" style={{ width: total ? `${(100 * done) / total}%` : '0' }} />
              </div>
              <p className="mt-1 flex justify-between text-text-dim">
                <span>
                  {done} of {total} tickets done
                </span>
                <span className="tabular-nums" data-tokens>
                  {tokens(p.tokens)}
                </span>
              </p>
              <p className="mt-1 text-text-dim">
                {p.github ? 'GitHub repository' : 'local repository'} · {modeName[p.network] ?? p.network}
              </p>
              <p
                className={'mt-1 font-mono text-xs text-text-dim ' + (p.models.length ? 'text-left' : 'text-right')}
                title={p.models.length > 2 ? p.models.join(' + ') : undefined}
              >
                {m.shown}
                {m.extra > 0 && <span className="ml-1.5 rounded bg-bg-sunk px-1.5 py-0.5 text-[11px] font-semibold text-text-dim">+{m.extra}</span>}
              </p>
            </Link>
          )
        })}
      </div>
      <h2 className="mt-8 mb-2 text-xl font-semibold">
        What agents are doing <span className="text-sm font-normal text-text-dim">live, across all projects</span>
      </h2>
      <Feed allProjects />
    </div>
  )
}
