import { useState } from 'react'
import { send } from '../lib/api'
import { button, Failed, mainButton } from './ui'
import { WriteBox } from './writebox'

// Add a ticket (design 7.1, 7.3): the operator's ticket is approved by being added, and Analysis
// looks at it like any other. No proposal is made by itself; select text for Improve text (7.2).
export function AddTicket({
  project,
  onAdded,
  open: controlledOpen,
  onOpenChange,
  hideTrigger,
}: {
  project: string
  onAdded: () => void
  open?: boolean
  onOpenChange?: (open: boolean) => void
  hideTrigger?: boolean
}) {
  const [uncontrolledOpen, setUncontrolledOpen] = useState(false)
  const open = controlledOpen !== undefined ? controlledOpen : uncontrolledOpen
  const setOpen = (next: boolean) => {
    setUncontrolledOpen(next)
    onOpenChange?.(next)
  }
  const [f, setF] = useState({ title: '', kind: 'feature', why: '', what: '', goal: '', checks: '' })
  const [error, setError] = useState('')
  const set = (k: keyof typeof f) => (v: string) => setF({ ...f, [k]: v })
  const add = () => {
    const checks = f.checks
      .split('\n')
      .map((l) => l.trim())
      .filter(Boolean)
      .map((l) => (l.startsWith('judge:') ? { kind: 'judge', spec: l.slice(6).trim() } : { kind: 'cmd', spec: l.replace(/^cmd:\s*/, '') }))
    send('POST', `/api/projects/${encodeURIComponent(project)}/tasks`, {
      title: f.title,
      kind: f.kind,
      why: f.why,
      what: f.what,
      goal: f.goal,
      checks,
      docsReason: 'added by the operator',
    }).then(
      () => {
        setF({ title: '', kind: 'feature', why: '', what: '', goal: '', checks: '' })
        setOpen(false)
        setError('')
        onAdded()
      },
      (e: Error) => setError(e.message),
    )
  }
  if (!open) {
    if (hideTrigger) return null
    return (
      <button className="mt-4 w-full rounded-lg border border-border bg-bg-raised p-4 text-left text-accent" onClick={() => setOpen(true)}>
        Add a ticket
      </button>
    )
  }
  const input = 'w-full rounded-md border border-border bg-bg-raised p-2 text-sm'
  return (
    <section className="mt-4 rounded-lg border border-border bg-bg-raised p-4 text-sm" aria-label="Add a ticket">
      <h2 className="mb-3 font-semibold">Add a ticket</h2>
      <div className="grid gap-2 md:grid-cols-[8rem_1fr]">
        <label htmlFor="t-title">Title</label>
        <input id="t-title" className={input} value={f.title} onChange={(e) => set('title')(e.target.value)} />
        <label htmlFor="t-kind">Kind</label>
        <select id="t-kind" className={input} value={f.kind} onChange={(e) => set('kind')(e.target.value)}>
          {['feature', 'fix', 'chore', 'docs'].map((k) => (
            <option key={k}>{k}</option>
          ))}
        </select>
        <label htmlFor="t-why">Why</label>
        <WriteBox id="t-why" rows={2} value={f.why} onChange={set('why')} />
        <label htmlFor="t-what">What</label>
        <WriteBox id="t-what" rows={2} value={f.what} onChange={set('what')} />
        <label htmlFor="t-goal">Goal</label>
        <WriteBox id="t-goal" rows={2} value={f.goal} onChange={set('goal')} />
        <label htmlFor="t-checks">Checks</label>
        <WriteBox
          id="t-checks"
          rows={2}
          placeholder={'One a line: a command, or judge: a criterion'}
          value={f.checks}
          onChange={set('checks')}
          className="font-mono"
        />
      </div>
      <Failed error={error} />
      <div className="mt-3 flex gap-2">
        <button className={mainButton} onClick={add} disabled={!f.title.trim()}>
          Add the ticket
        </button>
        <button className={button} onClick={() => setOpen(false)}>
          Cancel
        </button>
      </div>
      <p className="mt-2 text-text-dim">Your ticket goes to Backlog, and Analysis looks at it before it is built.</p>
    </section>
  )
}
