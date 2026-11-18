import { Link } from '@tanstack/react-router'
import type { ActiveTicketsSummary, OverviewSandbox } from '../lib/api'
import { ago } from './ui'

export type Room = {
  cores: number
  memMB: number
  room: { cpus: number; memMB: number }
  used: {
    cpus: number
    memMB: number
    runs: number
    large: number
    small: number
    projectVMs?: number
    pausedVMs?: number
    checks: number
    terminals: number
    hosted: number
  }
  waiting: { key: string; size: string; priority: number; project?: string; ticket?: string; what: string; since: string }[]
}

export type LeadPanel = {
  slots: { cli: string; slots: number; set: number; runs: { run: string; project?: string; ticket?: string; title: string }[] }[]
  queue: { key: string; class: string; keyboard: boolean; priority: number; ownPriority: number; project?: string; ticket?: string; cli: string; what: string; since: string }[]
}

const classWords: Record<string, string> = {
  operator_waiting: 'You are waiting',
  ticket: 'Ticket',
  analysis: 'Analysis',
  final_report: 'Final report',
  housekeeping: 'Housekeeping',
  retrospective: 'Retrospective',
  evals: 'Evals',
  improvement: 'Improvement',
}

const gb = (mb: number) => `${Math.round(mb / 1024)} GB`

// The active-ticket concurrency, lead slots, and the runs that wait (7.9): one queue for all projects.
export function Lead({
  lead,
  activeTickets,
  room,
  sandboxes,
}: {
  lead: LeadPanel
  activeTickets?: ActiveTicketsSummary
  room: Room | null
  sandboxes: OverviewSandbox[]
}) {
  const total = lead.slots.reduce((n, c) => n + c.slots, 0)
  const used = lead.slots.reduce((n, c) => n + c.runs.length, 0)
  return (
    <div className="mb-8 grid gap-3 md:grid-cols-2 lg:grid-cols-3">
      <section className="rounded-lg border border-border bg-bg-raised p-4" aria-label="Lead slots">
        <h2 className="text-xs tracking-wide text-text-dim uppercase">Lead slots</h2>
        <p className="mt-1 text-2xl font-semibold">
          {used} of {total}
        </p>
        {activeTickets && (
          <p className="mt-1 text-xs text-text-dim" data-active-tickets>
            Active tickets: <b>{activeTickets.active} of {activeTickets.max}</b> across all projects
            {activeTickets.max < activeTickets.set && <span className="text-amber-text"> (lowered from {activeTickets.set})</span>}
          </p>
        )}
        {lead.slots.map((c) => (
          <div key={c.cli} className="mt-2">
            <p className="text-sm text-text-dim">
              <code>{c.cli}</code> · {c.runs.length} of {c.slots}
              {c.slots < c.set && <span className="text-amber-text"> · lowered from {c.set}: a group is close to its limit</span>}
            </p>
            {c.runs.map((r) => (
              <Link
                key={r.run}
                to={r.project && r.ticket ? '/p/$project/tickets/$key' : r.project ? '/p/$project/agents' : '/runs/$id'}
                params={r.project && r.ticket ? { project: r.project, key: r.ticket } : r.project ? { project: r.project } : { id: r.run }}
                className="mt-1 block rounded-md border border-border px-2 py-1 text-sm transition-colors hover:border-accent hover:bg-accent-soft/40"
              >
                <span className="text-accent">●</span> <b>{r.project || 'Conversation'}</b> {r.ticket} <span data-free-text>{r.title}</span>
              </Link>
            ))}
            {Array.from({ length: Math.max(0, c.slots - c.runs.length) }).map((_, i) => (
              <p key={i} className="mt-1 rounded-md border border-border px-2 py-1 text-sm text-text-dim">
                Free slot
              </p>
            ))}
          </div>
        ))}
      </section>
      <section className="rounded-lg border border-border bg-bg-raised p-4" aria-label="Waiting for a lead slot">
        <h2 className="text-xs tracking-wide text-text-dim uppercase">Waiting for a lead slot</h2>
        <p className="mt-1 text-2xl font-semibold">{lead.queue.length}</p>
        <ol className="mt-2 space-y-1">
          {lead.queue.map((w) => (
            <li key={w.key} className="rounded-md border border-border px-2 py-1 text-sm">
              {w.project ? (
                <Link
                  to={w.ticket ? '/p/$project/tickets/$key' : '/p/$project/agents'}
                  params={w.ticket ? { project: w.project, key: w.ticket } : { project: w.project }}
                  className="hover:text-accent hover:underline"
                >
                  <b>{w.project}</b> · <span data-free-text>{w.what}</span>
                </Link>
              ) : (
                <>
                  <b>Crew</b> · <span data-free-text>{w.what}</span>
                </>
              )}
              <span className="block text-xs text-text-dim">
                {classWords[w.class] ?? w.class}
                {w.ticket ? ` · priority ${['urgent', 'high', 'normal', 'low'][w.ownPriority] ?? w.ownPriority}` : ''}
                {w.ticket && w.priority !== 2 ? ` · yours: ${['urgent', 'high', 'normal', 'low'][w.priority] ?? w.priority}` : ''} · <code>{w.cli}</code> · waiting{' '}
                {ago(w.since)}
              </span>
            </li>
          ))}
        </ol>
        <p className="mt-2 text-xs text-text-dim">
          Order: work you are waiting on, then resuming turns, tickets furthest right on the board, final reports, housekeeping, retrospectives, evals, and the improver. Within
          each: by the priority you set, then projects take turns, then by the ticket's own priority in its project.
        </p>
      </section>
      {room && <Machine room={room} sandboxes={sandboxes} />}
    </div>
  )
}

// The sandboxes on the machine (7.9, R71; 21, "The machine is full"): what is given out of the room,
// what of it, and what waits for room, in the order freed room goes to it.
function Machine({ room, sandboxes }: { room: Room; sandboxes: OverviewSandbox[] }) {
  const u = room.used
  const runs = u.large + u.small
  const share = room.room.memMB ? Math.min(100, (100 * u.memMB) / room.room.memMB) : 0
  return (
    <section className="rounded-lg border border-border bg-bg-raised p-4" aria-label="Sandboxes on the machine">
      <div className="flex items-center justify-between gap-2">
        <h2 className="text-xs tracking-wide text-text-dim uppercase">Sandboxes on the machine</h2>
        <Link to="/sandboxes" className="text-xs text-accent underline">
          Manage sandboxes
        </Link>
      </div>
      <p className="mt-1 text-2xl font-semibold">{runs} running</p>
      <div className="mt-2 h-1.5 rounded bg-bg-sunk">
        <div className="h-1.5 rounded bg-accent" style={{ width: `${share}%` }} />
      </div>
      <dl className="mt-3 grid grid-cols-[auto_1fr] gap-x-4 gap-y-1 text-sm">
        <dt className="text-text-dim">Memory</dt>
        <dd>
          {gb(u.memMB)} of {gb(room.room.memMB)} given out
        </dd>
        <dt className="text-text-dim">CPU</dt>
        <dd>
          {u.cpus} of {room.room.cpus} virtual CPUs · {room.cores} cores
        </dd>
        <dt className="text-text-dim">Project VMs</dt>
        <dd>
          {u.projectVMs ?? 0} running, {u.pausedVMs ?? 0} suspended (0% CPU)
        </dd>
        <dt className="text-text-dim">Agent runs</dt>
        <dd>
          {u.runs} · of all sandboxes {u.large} large with Docker inside, {u.small} small
        </dd>
        <dt className="text-text-dim">Checks</dt>
        <dd>{u.checks}</dd>
        <dt className="text-text-dim">Hosted projects</dt>
        <dd>{u.hosted}</dd>
        <dt className="text-text-dim">Terminals</dt>
        <dd>{u.terminals}</dd>
        <dt className="text-text-dim">Waiting for room</dt>
        <dd>
          {room.waiting.length} {room.waiting.length === 1 ? 'run' : 'runs'}
        </dd>
      </dl>
      {sandboxes.length > 0 && (
        <ul className="mt-3 space-y-1" aria-label="Live sandboxes">
          {sandboxes.map((sb) => (
            <li key={sb.id}>
              <Link
                to={sb.project && sb.ticket ? '/p/$project/tickets/$key' : sb.project ? '/p/$project/agents' : sb.run ? '/runs/$id' : '/runs'}
                params={sb.project && sb.ticket ? { project: sb.project, key: sb.ticket } : sb.project ? { project: sb.project } : sb.run ? { id: sb.run } : {}}
                className="flex items-center justify-between gap-2 rounded-md border border-border px-2 py-1 text-xs transition-colors hover:border-accent hover:bg-accent-soft/40"
              >
                <span className="truncate">
                  <span className={sb.state === 'idle' ? 'text-text-dim' : 'text-accent'}>●</span> <b>{sb.project || sb.kind}</b> {sb.ticket ? `${sb.ticket} ` : ''}
                  <span className="text-text-dim" data-free-text>
                    {sb.title || sb.role || sb.kind}
                  </span>
                </span>
                <span className="shrink-0 font-mono text-[11px] text-text-dim">
                  {sb.size} · {sb.state} · {ago(sb.startedAt)}
                </span>
              </Link>
            </li>
          ))}
        </ul>
      )}
      {room.waiting.length > 0 && (
        <ol className="mt-2 space-y-1" aria-label="Waiting for room">
          {room.waiting.map((w) => (
            <li key={w.key} className="rounded-md border border-border px-2 py-1 text-sm">
              <b>{w.project ?? 'Crew'}</b> · {w.ticket ? `${w.ticket} ` : ''}
              <span data-free-text>{w.what}</span>
              <span className="block text-xs text-text-dim">
                a {w.size} sandbox · waiting {ago(w.since)}
              </span>
            </li>
          ))}
        </ol>
      )}
    </section>
  )
}
