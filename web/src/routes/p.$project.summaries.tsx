import { createFileRoute, Link } from '@tanstack/react-router'
import { useState } from 'react'
import type { Summary } from '../lib/api'
import { usePoll } from '../lib/poll'
import { tokenCount } from '../lib/tokens'
import { plainText } from '../lib/words'
import { Failed, PageHead, when } from '../components/ui'

export const Route = createFileRoute('/p/$project/summaries')({ component: Summaries })

export type SummarySort = 'newest' | 'tokens' | 'time' | 'rework'

type FactSubtask = { run?: string; minutes?: number; tokens?: number }

export function summaryRuns(s: Summary): number {
  const subs = s.facts?.subtasks
  if (!Array.isArray(subs)) return 0
  const withRun = (subs as FactSubtask[]).filter((x) => x && x.run).length
  return withRun || subs.length
}

export function summaryMinutes(s: Summary): number {
  const subs = s.facts?.subtasks
  if (Array.isArray(subs) && subs.length > 0) {
    const sum = (subs as FactSubtask[]).reduce((n, x) => n + (typeof x?.minutes === 'number' ? x.minutes : 0), 0)
    if (sum > 0) return sum
  }
  return typeof s.facts?.minutes === 'number' ? s.facts.minutes : 0
}

export function formatAgentMinutes(mins: number): string {
  if (mins <= 0) return '0 min'
  const h = Math.floor(mins / 60)
  const m = mins % 60
  if (h === 0) return `${m} min`
  return m === 0 ? `${h} h` : `${h} h ${m} min`
}

export function summaryFixRounds(s: Summary): number {
  return typeof s.facts?.fix_rounds === 'number' ? s.facts.fix_rounds : 0
}

export function summaryTokens(s: Summary): number {
  return typeof s.facts?.tokens === 'number' ? s.facts.tokens : 0
}

export function summaryEnded(s: Summary): string | undefined {
  return typeof s.facts?.ended === 'string' ? s.facts.ended : s.writtenAt
}

export function summaryText(s: Summary): string {
  if (s.howItWent && s.whatWasDone) return plainText(`${s.whatWasDone} — ${s.howItWent}`)
  if (s.howItWent) return plainText(s.howItWent)
  if (s.whatWasDone) return plainText(s.whatWasDone)
  if (s.missing) return s.outcome !== 'done' && !s.writtenAt ? 'Withdrawn in Planning: the facts only.' : 'Being written…'
  return ''
}

export function sortSummaries(list: Summary[], by: SummarySort): Summary[] {
  const copy = [...list]
  if (by === 'tokens') return copy.sort((a, b) => summaryTokens(b) - summaryTokens(a))
  if (by === 'time') return copy.sort((a, b) => summaryMinutes(b) - summaryMinutes(a))
  if (by === 'rework') return copy.sort((a, b) => summaryFixRounds(b) - summaryFixRounds(a))
  return copy
}

// Summaries (design 8.4, 20.1, summaries.png): every ended ticket's summary in the project,
// searchable across the written sections and sortable by tokens, time, and rework.
function Summaries() {
  const { project } = Route.useParams()
  const [q, setQ] = useState('')
  const [search, setSearch] = useState('')
  const [sort, setSort] = useState<SummarySort>('newest')
  const path = `/api/projects/${encodeURIComponent(project)}/summaries${search ? `?q=${encodeURIComponent(search)}` : ''}`
  const { data, error } = usePoll<{ summaries: Summary[] }>(path, 10000)
  const rows = sortSummaries(data?.summaries ?? [], sort)
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
        title="Summaries"
      >
        When a ticket ends, Crew writes its summary from the records and trajectories and stores it in Crew&apos;s database. The facts are
        computed. The text is a model&apos;s reading.
      </PageHead>
      <Failed error={error} />
      <div className="mb-3 flex flex-wrap items-center gap-2">
        <div className="inline-flex overflow-hidden rounded-md border border-border text-sm" role="tablist" aria-label="Sort summaries">
          {(
            [
              ['newest', 'Newest'],
              ['tokens', 'Tokens'],
              ['time', 'Time'],
              ['rework', 'Rework'],
            ] as const
          ).map(([id, label]) => (
            <button key={id} role="tab" aria-selected={sort === id} className={'px-3 py-1 ' + (sort === id ? 'bg-text text-bg' : 'bg-bg-raised')} onClick={() => setSort(id)}>
              {label}
            </button>
          ))}
        </div>
        <form
          className="ml-auto flex gap-2"
          onSubmit={(e) => {
            e.preventDefault()
            setSearch(q.trim())
          }}
        >
          <input
            aria-label="Search summaries"
            value={q}
            onChange={(e) => setQ(e.target.value)}
            placeholder="Search what was done, how it went, or next time"
            className="w-80 rounded-md border border-border bg-bg-raised px-2 py-1 text-sm"
          />
        </form>
      </div>
      <SummariesTable project={project} summaries={rows} loading={!data && !error} />
    </div>
  )
}

export function SummariesTable({ project, summaries, loading }: { project: string; summaries: Summary[]; loading?: boolean }) {
  return (
    <div className="overflow-x-auto rounded-lg border border-border bg-bg-raised">
      <table className="w-full text-sm">
        <thead className="text-left text-xs tracking-wide text-text-dim uppercase">
          <tr className="border-b border-border">
            <th className="px-3 py-2">Ticket</th>
            <th className="px-3 py-2">What happened</th>
            <th className="px-3 py-2">Done</th>
            <th className="px-3 py-2">Agent runs</th>
            <th className="px-3 py-2">Agent time</th>
            <th className="px-3 py-2">Fix rounds</th>
            <th className="px-3 py-2">Tokens</th>
          </tr>
        </thead>
        <tbody>
          {summaries.map((s) => {
            const fixes = summaryFixRounds(s)
            const text = summaryText(s)
            return (
              <tr key={s.ticket} className="border-b border-border/60 align-top" data-summary={s.ticket}>
                <td className="px-3 py-2.5 font-mono text-xs whitespace-nowrap">
                  <Link to="/p/$project/tickets/$key" params={{ project, key: s.ticket }} className="text-accent">
                    {s.ticket}
                  </Link>
                </td>
                <td className="px-3 py-2.5 break-words [overflow-wrap:anywhere]">
                  <div className="font-semibold" data-free-text>
                    {s.title}
                  </div>
                  {text && (
                    <p className="mt-0.5 text-text-dim" data-free-text>
                      {text}
                    </p>
                  )}
                </td>
                <td className="px-3 py-2.5 whitespace-nowrap text-text-dim">{when(summaryEnded(s))}</td>
                <td className="px-3 py-2.5 tabular-nums">{summaryRuns(s)}</td>
                <td className="px-3 py-2.5 whitespace-nowrap tabular-nums">{formatAgentMinutes(summaryMinutes(s))}</td>
                <td className="px-3 py-2.5 tabular-nums">{fixes > 0 ? fixes : ''}</td>
                <td className="px-3 py-2.5 whitespace-nowrap tabular-nums">{tokenCount(summaryTokens(s))}</td>
              </tr>
            )
          })}
          {!loading && summaries.length === 0 && (
            <tr>
              <td colSpan={7} className="px-3 py-6 text-center text-text-dim">
                No summary yet.
              </td>
            </tr>
          )}
        </tbody>
      </table>
    </div>
  )
}
