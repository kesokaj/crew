import { Link } from '@tanstack/react-router'
import { useState, type ReactNode } from 'react'
import { chatAuthor, type TaskView, type TicketRecord } from '../../lib/api'
import { plainText, stageName } from '../../lib/words'
import { markedUnder } from '../chat'
import { button, Tag, when } from '../ui'
import { Box } from './ticket-header'

const recordTitle: Record<string, string> = {
  note: 'Note',
  review: 'Review',
  test: 'Test result',
  comment: 'Comment',
  e2e: 'End-to-end result',
  verify: 'Verification',
}

const linkWord: Record<string, string> = { depends_on: 'Depends on', created_from: 'Created from', duplicates: 'Duplicates', replaces: 'Replaces' }
const backWord: Record<string, string> = { depends_on: 'Waited for by', created_from: 'Led to', duplicates: 'Duplicated by', replaces: 'Replaced by' }

function recordAuthor(author: string): string {
  if (author === 'operator') return 'you'
  if (author === chatAuthor) return 'the assistant of the chat, sent on by you'
  return `the ${author}`
}

export function Records({
  records,
  files,
  project,
  ended,
}: {
  records: TicketRecord[]
  files: Record<string, { name: string }[]>
  project: string
  ended: boolean
}) {
  const [openAll, setOpenAll] = useState(false)
  const ordered = [...records].reverse()
  return (
    <Box
      title="Records"
      part="record"
      right={
        ordered.length > 1 ? (
          <button type="button" className={button} onClick={() => setOpenAll((v) => !v)}>
            {openAll ? 'Collapse older' : `Expand all (${ordered.length})`}
          </button>
        ) : undefined
      }
    >
      {records.length === 0 && <p className="text-sm text-text-dim">None yet.</p>}
      <ol className="space-y-3">
        {ordered.map((r, idx) => {
          const isOpen = openAll || idx === 0 || r.kind === 'comment'
          return (
            <li key={r.id} className="rounded-md border border-border/60 bg-bg p-3 text-sm" data-record={r.kind}>
              <details open={isOpen}>
                <summary className="flex cursor-pointer flex-wrap items-baseline gap-2 select-none">
                  <b>{recordTitle[r.kind] ?? r.kind}</b>
                  <span className="text-text-dim">by {recordAuthor(r.author)}</span>
                  {r.verdict && <Tag tone={r.verdict === 'approve' ? 'ok' : 'amber'}>{r.verdict === 'approve' ? 'approved' : 'changes requested'}</Tag>}
                  {r.result && <Tag tone={r.result === 'pass' ? 'ok' : 'bad'}>{r.result === 'pass' ? 'passed' : 'failed'}</Tag>}
                  {r.commit && <span className="font-mono text-xs text-text-dim">{r.commit.slice(0, 7)}</span>}
                  <span className="ml-auto text-xs text-text-dim">{when(r.createdAt)}</span>
                </summary>
                <div className="mt-2">
                  {r.quote && (
                    <div className="mt-1" data-quote>
                      <blockquote className="border-l-2 border-amber pl-2 whitespace-pre-wrap text-text-dim" data-free-text>
                        {r.quote}
                      </blockquote>
                      <p className="text-xs text-text-dim">{markedUnder(r.quoteSource)}</p>
                    </div>
                  )}
                  <p className="mt-0.5 max-w-[65ch] leading-relaxed whitespace-pre-wrap break-words [overflow-wrap:anywhere]" data-free-text>
                    {plainText(r.body)}
                  </p>
                  {(files[String(r.id)] ?? []).length > 0 && (
                    <div className="mt-2 flex flex-wrap gap-2" aria-label="Screenshots">
                      {(files[String(r.id)] ?? []).map((f) => {
                        const src = `/api/projects/${encodeURIComponent(project)}/records/${r.id}/files/${encodeURIComponent(f.name)}`
                        return (
                          <a key={f.name} href={src} target="_blank" rel="noreferrer" className="block">
                            <img src={src} alt={f.name} className="h-24 rounded border border-border object-cover" />
                            <span className="font-mono text-xs text-text-dim">{f.name}</span>
                          </a>
                        )
                      })}
                    </div>
                  )}
                </div>
              </details>
            </li>
          )
        })}
      </ol>
      {!ended && <p className="mt-3 text-sm text-text-dim">A comment to the agent is sent from the chat of this ticket, with Tell the agent.</p>}
    </Box>
  )
}

export function TicketHistoryBox({ history }: { history: TaskView['history'] }) {
  return (
    <Box title="History" part="other">
      <ul className="space-y-1 font-mono text-xs break-words [overflow-wrap:anywhere]">
        {[...history].reverse().map((h) => (
          <li key={h.Seq}>
            <span className="text-text-dim">{when(h.At)}</span> {historyText(h.Before, h.After)} · {h.Who}
          </li>
        ))}
      </ul>
    </Box>
  )
}

function historyText(before: Record<string, unknown> | null, after: Record<string, unknown> | null): ReactNode {
  if (!after) return 'removed'
  if (typeof after.terminal === 'string') return `terminal ${after.terminal}`
  if (after.chat === 'asked') return after.marked ? 'commented on marked text in the chat' : 'asked in the chat'
  if (typeof after.stage === 'string')
    return (
      <>
        {stageName[after.stage] ?? after.stage}
        {typeof after.reason === 'string' && (
          <>
            {' · '}
            <span data-free-text>{after.reason}</span>
          </>
        )}
      </>
    )
  if (!before) return 'created'
  const keys = Object.keys(after).filter((k) => JSON.stringify(after[k]) !== JSON.stringify(before[k]))
  return keys.length ? `changed ${keys.join(', ')}` : 'changed'
}

export function SourcePlanBox({ project, t }: { project: string; t: TaskView['ticket'] }) {
  return (
    <Box title="Source plan">
      {t.source ? (
        <>
          <p className="font-mono text-xs text-text-dim break-words [overflow-wrap:anywhere]" data-free-text>
            {t.source.file}
            {t.source.heading ? ` › ${t.source.heading}` : ''}
          </p>
          {t.source.quote && (
            <blockquote className="mt-2 border-l-2 border-border pl-2 text-sm break-words [overflow-wrap:anywhere]" data-free-text>
              {t.source.quote}
            </blockquote>
          )}
        </>
      ) : (
        <p className="text-sm text-text-dim">{t.origin === 'plan' ? 'No place in the plan was named.' : `Added by ${t.origin === 'operator' ? 'you' : t.createdBy}, after the plan.`}</p>
      )}
      {t.part && <p className="mt-1 text-sm">Part: {t.part}</p>}
      <Link to="/p/$project/plan" params={{ project }} className={button + ' mt-2 inline-block'}>
        Open the plan
      </Link>
    </Box>
  )
}

export function AnalysisBox({ t }: { t: TaskView['ticket'] }) {
  return (
    <Box title="Analysis">
      <dl className="grid grid-cols-[4.5rem_minmax(0,1fr)] gap-x-2 gap-y-1 text-sm break-words [overflow-wrap:anywhere]">
        <dt className="text-text-dim">Decision</dt>
        <dd className="min-w-0">{t.analysis.status}</dd>
        {t.analysis.decidedBy && (
          <>
            <dt className="text-text-dim">By</dt>
            <dd className="min-w-0">{t.analysis.decidedBy}</dd>
          </>
        )}
        {t.analysis.reason && (
          <>
            <dt className="text-text-dim">Reason</dt>
            <dd className="min-w-0 whitespace-pre-wrap break-words [overflow-wrap:anywhere]" data-free-text>
              {t.analysis.reason}
            </dd>
          </>
        )}
      </dl>
    </Box>
  )
}

export function LinksBox({ project, v }: { project: string; v: TaskView }) {
  const t = v.ticket
  if (t.links.length === 0 && v.linkedFrom.length === 0 && t.docs.length === 0) return null
  return (
    <Box title="Links">
      <ul className="space-y-1 text-sm break-words [overflow-wrap:anywhere]">
        {t.links.map((l) => (
          <li key={l.kind + l.to}>
            {linkWord[l.kind] ?? l.kind}{' '}
            <Link to="/p/$project/tickets/$key" params={{ project, key: l.to }} className="font-mono text-accent">
              {l.to}
            </Link>
          </li>
        ))}
        {v.linkedFrom.map((l) => (
          <li key={'from' + l.kind + l.to}>
            {backWord[l.kind] ?? l.kind}{' '}
            <Link to="/p/$project/tickets/$key" params={{ project, key: l.to }} className="font-mono text-accent">
              {l.to}
            </Link>
          </li>
        ))}
        {t.docs.map((d) => (
          <li key={d}>
            Documentation <span className="font-mono text-xs">{d}</span>
          </li>
        ))}
      </ul>
    </Box>
  )
}
