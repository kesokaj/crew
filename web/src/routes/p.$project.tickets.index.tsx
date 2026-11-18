import { createFileRoute, Link } from '@tanstack/react-router'
import { useState } from 'react'
import { usePoll } from '../lib/poll'
import { agentDoing, stageName } from '../lib/words'
import { Failed, PageHead, Tag } from '../components/ui'
import { tokenCount } from '../lib/tokens'

export const Route = createFileRoute('/p/$project/tickets/')({ component: Tickets })

type Row = {
  key: string
  title: string
  goal: string
  kind: string
  stage: string
  closedReason?: string
  analysis: string
  waiting?: string
  stuck?: string
  hold: boolean
  origin: string
  createdBy: string
  from?: string
  subtasks: number
  fixRounds: number
  // Its runs' tokens in and out (design 0.53: no cost, and no budget to pass).
  tokens: number
  role?: string
  cli?: string
  model?: string
  effort?: string
}

const stageOrder = ['planning', 'backlog', 'analysis', 'in_progress', 'review', 'test', 'e2e', 'verification', 'done', 'closed']

// cameFrom says who made a ticket, as the board's history does (design 6.3).
function cameFrom(r: Row): string {
  const by = r.createdBy.split(' · ')[0]
  switch (r.origin) {
    case 'plan':
      return 'the plan'
    case 'operator':
      return r.kind === 'revert' && r.from ? `you, reverting ${r.from}` : 'you'
    case 'token':
      return 'your token'
    case 'conversation':
    case 'chat':
      return 'you, from a ' + r.origin
  }
  return r.from ? `the ${by}, from ${r.from}` : `the ${by}`
}

// now is what the ticket is doing: a run that works, why it waits, or how it ended.
function Now({ r }: { r: Row }) {
  if (r.stage === 'done') return <span className="text-ok whitespace-nowrap">✓ Merged</span>
  if (r.stage === 'closed') return <span className="text-text-dim">{r.closedReason === 'cancelled' ? 'Cancelled' : 'Discarded'}</span>
  if (r.role)
    return (
      <span>
        <span className="text-accent">● {agentDoing(r.role, 'working')}</span>
        <span className="mt-0.5 block font-mono text-xs text-text-dim">
          {r.cli} · {r.model}
          {r.effort ? '-' + r.effort : ''}
        </span>
      </span>
    )
  if (r.hold) return <span className="text-amber-text">On hold</span>
  if (r.stuck) return <span className="text-amber-text">{r.stuck}</span>
  if (r.waiting) return <span className="text-text-dim">{r.waiting}</span>
  if (r.stage === 'backlog') return <span className="text-text-dim">Waiting for analysis</span>
  return <span className="text-text-dim">—</span>
}

export function filterTickets<T extends Pick<Row, 'key' | 'title' | 'goal'> & Partial<Pick<Row, 'stage' | 'kind' | 'origin'>>>(
  tickets: T[],
  filters: string | { query?: string; stage?: string; kind?: string; from?: string },
): T[] {
  const opts = typeof filters === 'string' ? { query: filters } : filters
  const q = (opts.query ?? '').trim().toLowerCase()
  return tickets.filter((r) => {
    if (opts.stage && r.stage !== opts.stage) return false
    if (opts.kind && r.kind !== opts.kind) return false
    if (opts.from && r.origin !== opts.from) return false
    if (q) {
      const hay = `${r.key} ${r.title} ${r.goal}`.toLowerCase()
      if (!hay.includes(q)) return false
    }
    return true
  })
}

// The Tickets screen (design 20.1, tickets.png): every ticket of the project, newest first, with what
// it is doing now, where it came from, its subtasks and fix rounds, and the tokens its runs used.
function Tickets() {
  const { project } = Route.useParams()
  const { data, error } = usePoll<{ tickets: Row[] }>(`/api/projects/${encodeURIComponent(project)}/tickets`, 10000)
  const [query, setQuery] = useState('')
  const [stage, setStage] = useState('')
  const [kind, setKind] = useState('')
  const [from, setFrom] = useState('')
  const all = data?.tickets ?? []
  const shown = filterTickets(all, { query, stage, kind, from })
  const kinds = [...new Set(all.map((r) => r.kind))]
  const origins = [...new Set(all.map((r) => r.origin))]
  const select = 'min-h-8 rounded-md border border-border bg-bg-raised px-2 py-1.5 text-sm focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-accent'
  return (
    <div className="mx-auto max-w-6xl p-6">
      <PageHead
        crumbs={
          <>
            <Link to="/" className="text-accent">
              All projects
            </Link>{' '}
            / {project}
          </>
        }
        title="Tickets"
      >
        Every piece of work in {project} is a ticket with a number, a kind, a history, and a branch. {all.length}{' '}
        {all.length === 1 ? 'ticket' : 'tickets'} so far.
      </PageHead>
      <Failed error={error} />
      <div className="mb-3 flex flex-wrap items-center gap-2">
        <input
          type="search"
          aria-label="Filter tickets"
          placeholder="Search key, title, or goal…"
          value={query}
          onChange={(e) => setQuery(e.target.value)}
          className="min-h-8 w-60 rounded-md border border-border bg-bg-raised px-2.5 py-1.5 text-sm focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-accent"
        />
        <select aria-label="Stage" className={select} value={stage} onChange={(e) => setStage(e.target.value)}>
          <option value="">Any stage</option>
          {stageOrder.map((s) => (
            <option key={s} value={s}>
              {stageName[s] ?? s}
            </option>
          ))}
        </select>
        <select aria-label="Kind" className={select} value={kind} onChange={(e) => setKind(e.target.value)}>
          <option value="">Any kind</option>
          {kinds.map((k) => (
            <option key={k}>{k}</option>
          ))}
        </select>
        <select aria-label="Came from" className={select} value={from} onChange={(e) => setFrom(e.target.value)}>
          <option value="">From anywhere</option>
          {origins.map((o) => (
            <option key={o} value={o}>
              {o === 'plan' ? 'the plan' : o === 'agent' ? 'an agent' : o === 'operator' ? 'you' : o}
            </option>
          ))}
        </select>
        <span className="text-sm text-text-dim">{shown.length} shown</span>
      </div>
      <div className="overflow-x-auto">
        <table data-tickets className="w-full min-w-[56rem] text-sm">
          <thead>
            <tr className="border-b border-border text-left text-xs tracking-wide text-text-dim uppercase">
              {['Ticket', 'Title and goal', 'Kind', 'Stage', 'Now', 'Came from', 'Subtasks', 'Fix rounds', 'Tokens'].map((h) => (
                <th key={h} className="px-2 py-2 font-normal">
                  {h}
                </th>
              ))}
            </tr>
          </thead>
          <tbody>
            {shown.map((r) => (
              <tr key={r.key} className="border-b border-border align-top">
                <td className="px-2 py-2 font-mono text-xs">
                  <Link to="/p/$project/tickets/$key" params={{ project, key: r.key }} className="text-accent">
                    {r.key}
                  </Link>
                </td>
                <td className="px-2 py-2">
                  <span className="font-medium" data-free-text>
                    {r.title}
                  </span>
                  <span className="block text-text-dim" data-free-text>
                    {r.goal}
                  </span>
                </td>
                <td className="px-2 py-2">
                  <Tag tone={r.kind === 'fix' ? 'amber' : r.kind === 'docs' || r.kind === 'revert' ? 'ok' : undefined}>{r.kind}</Tag>
                </td>
                <td className="px-2 py-2">{stageName[r.stage] ?? r.stage}</td>
                <td className="px-2 py-2">
                  <Now r={r} />
                </td>
                <td className="px-2 py-2">{cameFrom(r)}</td>
                <td className="px-2 py-2">{r.subtasks}</td>
                <td className="px-2 py-2">{r.fixRounds || ''}</td>
                <td className="px-2 py-2 tabular-nums">{r.tokens ? tokenCount(r.tokens) : ''}</td>
              </tr>
            ))}
          </tbody>
        </table>
      </div>
    </div>
  )
}
