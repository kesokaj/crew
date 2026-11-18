import { Link } from '@tanstack/react-router'
import { useState, type ReactNode } from 'react'
import { chatAuthor, send, type Bug, type MemoryEntry } from '../../lib/api'
import { button, Failed, when } from '../ui'

export function Head({ children }: { children: ReactNode }) {
  return <thead className="text-left text-xs tracking-wide text-text-dim uppercase">{children}</thead>
}

export function TicketLink({ project, ticket }: { project: string; ticket?: string }) {
  if (!ticket) return null
  return (
    <Link to="/p/$project/tickets/$key" params={{ project, key: ticket }} className="font-mono text-xs text-accent underline">
      {ticket}
    </Link>
  )
}

export const who = (author: string) =>
  author === 'operator' ? 'you' : author === 'system' ? 'Crew' : author === chatAuthor ? 'the assistant wrote it, you confirmed it' : `the ${author}`

export const num = (n: number) => String(n).padStart(4, '0')

// The Memory tab (9.3): short hints agents write for later runs, active at once. You retire any of
// them; an entry you write is yours, and no agent changes it.
export function MemoryTab({ project, entries, error, reload }: { project: string; entries?: MemoryEntry[]; error: string; reload: () => void }) {
  const [failed, setFailed] = useState('')
  const [showRetired, setShowRetired] = useState(false)
  const retire = (n: number) => {
    const reason = window.prompt(`Why is memory ${num(n)} retired?`, 'wrong')
    if (!reason) return
    send('PATCH', `/api/projects/${encodeURIComponent(project)}/memory/${n}`, { retire: reason }).then(
      () => (setFailed(''), reload()),
      (e: Error) => setFailed(e.message),
    )
  }
  const shown = (entries ?? []).filter((e) => showRetired || e.status === 'active')
  return (
    <>
      <p className="mb-4 text-text-dim">
        Short hints agents write for later runs, with no approval. The index goes into every prompt. Agents treat them as hints that may be out of date.
      </p>
      <Failed error={error || failed} />
      {entries && shown.length === 0 && <p className="text-text-dim">No memory yet.</p>}
      {shown.length > 0 && (
        <table className="w-full text-sm">
          <Head>
            <tr className="border-b border-border">
              <th className="py-1"></th>
              <th>Entry</th>
              <th>Kind</th>
              <th>For</th>
              <th>Written by</th>
              <th></th>
            </tr>
          </Head>
          <tbody>
            {shown.map((e) => (
              <tr key={e.number} className={'border-b border-border/60 align-top ' + (e.status === 'retired' ? 'opacity-60' : '')} data-memory={e.number}>
                <td className="py-2 pr-2 font-mono text-xs text-text-dim">{num(e.number)}</td>
                <td className="py-2 pr-2">
                  <p className="font-medium">{e.title}</p>
                  {e.body && <p className="text-text-dim">{e.body}</p>}
                  {(e.paths.length > 0 || e.keywords.length > 0) && (
                    <p className="text-xs text-text-dim">Shown for {[...e.paths, ...e.keywords.map((k) => `“${k}”`)].join(', ')}</p>
                  )}
                  {e.status === 'retired' && <p className="text-xs text-text-dim">Retired: {e.retiredReason}</p>}
                </td>
                <td className="py-2 pr-2">{e.kind}</td>
                <td className="py-2 pr-2">{e.appliesTo.join(', ')}</td>
                <td className="py-2 pr-2">
                  {who(e.author)} {e.sourceTicket && <>on <TicketLink project={project} ticket={e.sourceTicket} /></>}
                  <p className="text-xs text-text-dim">{when(e.createdAt)}</p>
                </td>
                <td className="py-2">
                  {e.status === 'active' && (
                    <button className={button} onClick={() => retire(e.number)}>
                      Retire
                    </button>
                  )}
                </td>
              </tr>
            ))}
          </tbody>
        </table>
      )}
      {entries && entries.some((e) => e.status === 'retired') && (
        <label className="mt-3 flex items-center gap-1.5 text-sm text-text-dim">
          <input type="checkbox" checked={showRetired} onChange={(e) => setShowRetired(e.target.checked)} /> Show the retired entries
        </label>
      )}
    </>
  )
}

const bugState = (b: Bug) =>
  b.status === 'closed' ? `closed: ${(b.closedReason ?? '').replaceAll('_', ' ')}` : b.status === 'fixed' ? '✓ fixed' : b.status === 'in_work' ? 'in work' : 'open'

// The Bugs tab (9.4): any agent reports a bug, which makes a fix ticket that another agent analyses
// before anyone builds it. You report one too, and close one that is not one.
export function BugsTab({ project, bugs, error, reload }: { project: string; bugs?: Bug[]; error: string; reload: () => void }) {
  const [failed, setFailed] = useState('')
  const [title, setTitle] = useState('')
  const [happens, setHappens] = useState('')
  const [severity, setSeverity] = useState('medium')
  const [open, setOpen] = useState(false)
  const report = () =>
    send('POST', `/api/projects/${encodeURIComponent(project)}/bugs`, { title, happens, severity }).then(
      () => (setTitle(''), setHappens(''), setOpen(false), setFailed(''), reload()),
      (e: Error) => setFailed(e.message),
    )
  const close = (n: number) => {
    const reason = window.prompt('Why: duplicate, not_reproducible, or wont_fix', 'duplicate')
    if (!reason) return
    send('POST', `/api/projects/${encodeURIComponent(project)}/bugs/${n}/close`, { reason }).then(
      () => (setFailed(''), reload()),
      (e: Error) => setFailed(e.message),
    )
  }
  return (
    <>
      <p className="mb-4 text-text-dim">
        Any agent can report a bug. A report also creates a fix ticket in the backlog, and another agent analyses it before anyone builds it.
      </p>
      <Failed error={error || failed} />
      {bugs && bugs.length === 0 && <p className="text-text-dim">No bug yet.</p>}
      {bugs && bugs.length > 0 && (
        <table className="w-full text-sm">
          <Head>
            <tr className="border-b border-border">
              <th className="py-1"></th>
              <th>Bug</th>
              <th>Severity</th>
              <th>Status</th>
              <th>Found by</th>
              <th>Fix ticket</th>
              <th></th>
            </tr>
          </Head>
          <tbody>
            {bugs.map((b) => (
              <tr key={b.number} className="border-b border-border/60 align-top" data-bug={b.number}>
                <td className="py-2 pr-2 font-mono text-xs text-text-dim">{num(b.number)}</td>
                <td className="py-2 pr-2">
                  {b.title}
                  {b.regressionCheck && <p className="font-mono text-xs text-text-dim">stays fixed: {b.regressionCheck}</p>}
                  {b.repeats > 1 && <p className="text-xs text-text-dim">seen {b.repeats} times</p>}
                </td>
                <td className={'py-2 pr-2 ' + (b.severity === 'high' ? 'text-amber' : '')}>{b.severity}</td>
                <td className={'py-2 pr-2 ' + (b.status === 'closed' ? 'text-text-dim' : '')}>{bugState(b)}</td>
                <td className="py-2 pr-2">
                  {who(b.foundBy)} {b.foundIn && <>on <TicketLink project={project} ticket={b.foundIn} /></>}
                </td>
                <td className="py-2 pr-2">
                  <TicketLink project={project} ticket={b.fixTicket} />
                </td>
                <td className="py-2">
                  {(b.status === 'open' || b.status === 'in_work') && (
                    <button className="text-sm text-accent" onClick={() => close(b.number)}>
                      Close
                    </button>
                  )}
                </td>
              </tr>
            ))}
          </tbody>
        </table>
      )}
      {open ? (
        <div className="mt-4 rounded-lg border border-border bg-bg-raised p-4">
          <label className="mb-1 block text-sm">What is wrong, in one line</label>
          <input aria-label="The bug" value={title} onChange={(e) => setTitle(e.target.value)} className="mb-2 w-full rounded border border-border bg-bg px-2 py-1" />
          <label className="mb-1 block text-sm">What happens</label>
          <textarea aria-label="What happens" value={happens} onChange={(e) => setHappens(e.target.value)} rows={3} className="mb-2 w-full rounded border border-border bg-bg px-2 py-1" />
          <div className="flex items-center gap-2">
            <select aria-label="Severity" value={severity} onChange={(e) => setSeverity(e.target.value)} className="rounded border border-border bg-bg px-2 py-1">
              <option>low</option>
              <option>medium</option>
              <option>high</option>
            </select>
            <button className={button} disabled={!title.trim() || !happens.trim()} onClick={report}>
              Report the bug
            </button>
            <button className="text-sm text-text-dim" onClick={() => setOpen(false)}>
              Cancel
            </button>
          </div>
        </div>
      ) : (
        <button className={button + ' mt-4'} onClick={() => setOpen(true)}>
          Report a bug
        </button>
      )}
    </>
  )
}
