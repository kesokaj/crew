import { Link } from '@tanstack/react-router'
import { useState } from 'react'
import { send, type Decision, type FeedbackPiece } from '../../lib/api'
import { button, Failed, when } from '../ui'
import { Head, num, TicketLink, who } from './memory-bugs-tabs'

const roles = ['builder', 'reviewer', 'tester', 'e2e', 'verifier', 'curator', 'planner']

// The Feedback tab (9.5): how work should be done from now on, from you and from agents on each
// other's work. The lead turns each piece into a memory entry, a ticket, or a proposed change; a rule
// of yours is a memory entry at once.
export function FeedbackTab({ project, list, error, reload }: { project: string; list?: FeedbackPiece[]; error: string; reload: () => void }) {
  const [body, setBody] = useState('')
  const [about, setAbout] = useState('builder')
  const [rule, setRule] = useState(false)
  const [failed, setFailed] = useState('')
  const give = () =>
    send('POST', `/api/projects/${encodeURIComponent(project)}/feedback`, { about, body, kind: rule ? 'rule' : 'correction' }).then(
      () => (setBody(''), setRule(false), setFailed(''), reload()),
      (e: Error) => setFailed(e.message),
    )
  return (
    <>
      <p className="mb-4 text-text-dim">
        Feedback is about how work should be done from now on. It comes from you and from agents on each other's work. The lead turns each piece into a memory entry, a ticket, or
        a proposed change.
      </p>
      <div className="mb-4 rounded-lg border border-border bg-bg-raised p-4">
        <div className="grid grid-cols-2 gap-3">
          <label className="text-sm">
            Your feedback
            <input
              aria-label="Your feedback"
              value={body}
              onChange={(e) => setBody(e.target.value)}
              placeholder="Reviews must check that the note says how the work was tested."
              className="mt-1 w-full rounded border border-border bg-bg px-2 py-1.5"
            />
          </label>
          <label className="text-sm">
            About the
            <select aria-label="About the" value={about} onChange={(e) => setAbout(e.target.value)} className="mt-1 w-full rounded border border-border bg-bg px-2 py-1.5">
              {roles.map((r) => (
                <option key={r}>{r}</option>
              ))}
            </select>
          </label>
        </div>
        <div className="mt-3 flex items-center gap-3">
          <label className="flex items-center gap-1.5 text-sm">
            <input type="checkbox" checked={rule} onChange={(e) => setRule(e.target.checked)} /> Make it a rule: store it in memory now
          </label>
          <button className={button} disabled={!body.trim()} onClick={give}>
            Give feedback
          </button>
        </div>
        <Failed error={failed} />
      </div>
      <Failed error={error} />
      {list && list.length === 0 && <p className="text-text-dim">No feedback yet.</p>}
      {list && list.length > 0 && (
        <table className="w-full text-sm">
          <Head>
            <tr className="border-b border-border">
              <th className="py-1"></th>
              <th>Feedback</th>
              <th>From</th>
              <th>About</th>
              <th>What was done</th>
            </tr>
          </Head>
          <tbody>
            {list.map((f) => (
              <tr key={f.number} className="border-b border-border/60 align-top" data-feedback={f.number}>
                <td className="py-2 pr-2 font-mono text-xs text-text-dim">{num(f.number)}</td>
                <td className="py-2 pr-2">
                  {f.body}
                  {f.ticket && (
                    <p className="text-xs text-text-dim">
                      on <TicketLink project={project} ticket={f.ticket} />
                    </p>
                  )}
                </td>
                <td className="py-2 pr-2">{who(f.from)}</td>
                <td className="py-2 pr-2">the {f.about}</td>
                <td className="py-2 pr-2">{f.status === 'new' ? <span className="text-text-dim">waits for the lead</span> : f.outcome}</td>
              </tr>
            ))}
          </tbody>
        </table>
      )}
    </>
  )
}

// The Decisions tab (7.5, 20.1, decisions.png): every choice an agent made by itself, with its
// alternatives and its reason. You can change one, and Crew creates a ticket that carries the change
// out.
export function DecisionsTab({ project, list, error, reload }: { project: string; list?: Decision[]; error: string; reload: () => void }) {
  return (
    <>
      <p className="mb-4 text-text-dim">
        Choices the plan did not settle. An agent does not wait for you: it decides, records the choice here with its reason, and carries on. You can change any of them. Crew then
        creates a ticket that carries the change out.
      </p>
      <Failed error={error} />
      {list && list.length === 0 && <p className="text-text-dim">No agent has decided anything alone yet.</p>}
      <table className="w-full text-sm">
        <Head>
          <tr className="border-b border-border">
            <th className="py-1"></th>
            <th>The choice</th>
            <th>Decided</th>
            <th>By</th>
            <th>When</th>
            <th></th>
          </tr>
        </Head>
        <tbody>
          {list?.map((d) => (
            <DecisionRow key={d.id} project={project} d={d} onChanged={reload} />
          ))}
        </tbody>
      </table>
    </>
  )
}

function DecisionRow({ project, d, onChanged }: { project: string; d: Decision; onChanged: () => void }) {
  const [other, setOther] = useState<string | null>(null)
  const [error, setError] = useState('')
  const [busy, setBusy] = useState(false)
  const change = (choice: string) => {
    setBusy(true)
    send('POST', `/api/projects/${encodeURIComponent(project)}/decisions/${d.id}/change`, { choice }).then(
      () => {
        setBusy(false)
        setOther(null)
        onChanged()
      },
      (e: Error) => {
        setBusy(false)
        setError(e.message)
      },
    )
  }
  const others = d.alternatives.filter((a) => a !== d.choice)
  return (
    <tr className="border-b border-border/60 align-top" data-decision={d.id}>
      <td className="py-2 pr-2 font-mono text-xs text-text-dim">d_{String(d.id).padStart(4, '0')}</td>
      <td className="py-2 pr-2">
        <p className="font-medium">{d.question}</p>
        <p className="text-text-dim">{d.reason}</p>
        {d.alternatives.length > 0 && <p className="text-xs text-text-dim">Alternatives it saw: {d.alternatives.join(' · ')}</p>}
      </td>
      <td className="py-2 pr-2 font-semibold">
        {d.changedTo ? (
          <>
            <s className="font-normal text-text-dim">{d.choice}</s> {d.changedTo}
          </>
        ) : (
          d.choice
        )}
      </td>
      <td className="py-2 pr-2">
        the {d.role}
        {d.ticket && (
          <>
            {' '}
            on{' '}
            <Link to="/p/$project/tickets/$key" params={{ project, key: d.ticket }} className="font-mono text-xs text-accent underline">
              {d.ticket}
            </Link>
          </>
        )}
      </td>
      <td className="py-2 pr-2 whitespace-nowrap">{when(d.createdAt)}</td>
      <td className="py-2">
        {d.changedTo ? (
          <span className="text-sm">
            Changed by you {when(d.changedAt)} ·{' '}
            {d.changeTicket && (
              <Link to="/p/$project/tickets/$key" params={{ project, key: d.changeTicket }} className="font-mono text-xs text-accent underline">
                {d.changeTicket}
              </Link>
            )}
          </span>
        ) : (
          <div className="flex flex-col items-start gap-1">
            {others.map((a) => (
              <button key={a} className={button} disabled={busy} onClick={() => change(a)}>
                Use “{a}” instead
              </button>
            ))}
            {other === null ? (
              <button className="text-sm text-accent" onClick={() => setOther('')}>
                Something else…
              </button>
            ) : (
              <form
                className="flex gap-1"
                onSubmit={(e) => {
                  e.preventDefault()
                  change(other)
                }}
              >
                <input
                  aria-label="Your choice"
                  value={other}
                  onChange={(e) => setOther(e.target.value)}
                  className="w-48 rounded border border-border bg-bg-raised px-2 py-1 text-sm"
                />
                <button className={button} disabled={busy || !other.trim()}>
                  Change
                </button>
              </form>
            )}
            <Failed error={error} />
          </div>
        )}
      </td>
    </tr>
  )
}
