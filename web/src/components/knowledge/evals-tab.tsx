import { useEffect, useState } from 'react'
import { getJSON, send } from '../../lib/api'
import { fitNote, suiteFor, suiteNote, withChange } from '../../lib/evals'
import { button, Failed, when } from '../ui'
import { Head, num, TicketLink } from './memory-bugs-tabs'

export type EvalCase = {
  number: number
  title: string
  origin: string
  ticket?: string | null
  bug?: number | null
  why: string
  what: string
  goal: string
  heldOut: string[]
  chosenWhy: string
  status: string
  retiredWhy?: string | null
  fit?: { state?: string; list?: string[]; why?: string; builder?: string; judge?: string } | null
}
export type EvalRun = {
  id: number
  case: number
  repeat: number
  side: string
  state: string
  goalMet?: boolean | null
  checkRounds: number
  rework: number
  model?: string | null
  why: string
  failedOutput?: string | null
}
export type SideResult = { cases: number; passed: number }
export type EvalSuite = {
  id: number
  purpose: string
  override?: number | null
  improvement?: number | null
  gate?: string | null
  repeats: number
  state: string
  stoppedWhy?: string | null
  result?: { baseline: SideResult; change?: SideResult; release?: SideResult; decision?: { verdict: string; why: string } } | null
  createdAt: string
  endedAt?: string | null
  runs: number
  runsDone: number
  unmeasured?: string | null
}
export type Evals = { cases: EvalCase[]; suites: EvalSuite[] }

const percent = (v?: number) => (v === undefined ? '—' : `${Math.round(v * 100)}%`)

// The Evals tab (9.6, knowledge--evals.png): cases from this project's own finished tickets, each
// replayed from the code as it was, and the results of its suites.
export function EvalsTab({ project, evals, error, reload }: { project: string; evals: Evals | null; error: string; reload: () => void }) {
  const [failed, setFailed] = useState('')
  const [ticket, setTicket] = useState('')
  const [heldOut, setHeldOut] = useState('')
  const base = `/api/projects/${encodeURIComponent(project)}/evals`
  const run = () =>
    send('POST', `${base}/run`, {}).then(
      () => (setFailed(''), reload()),
      (e: Error) => setFailed(e.message),
    )
  const add = () =>
    send('POST', `${base}/cases`, {
      ticket: ticket.trim(),
      heldOut: heldOut.split(/[\s,]+/).filter(Boolean),
      why: 'Chosen by you',
    }).then(
      () => (setTicket(''), setHeldOut(''), setFailed(''), reload()),
      (e: Error) => setFailed(e.message),
    )
  const retire = (n: number) => {
    const why = window.prompt(`Why is case ${num(n)} retired?`, 'no longer typical')
    if (!why) return
    send('POST', `${base}/cases/${n}/retire`, { why }).then(
      () => (setFailed(''), reload()),
      (e: Error) => setFailed(e.message),
    )
  }
  const accept = (n: number) =>
    send('POST', `${base}/cases/${n}/accept`).then(
      () => (setFailed(''), reload()),
      (e: Error) => setFailed(e.message),
    )
  const active = (evals?.cases ?? []).filter((c) => c.status === 'active')
  const [editing, setEditing] = useState<number | null>(null)
  const [openSuite, setOpenSuite] = useState<number | null>(null)
  return (
    <>
      <p className="mb-4 text-text-dim">
        Cases come from this project's own finished tickets. A case replays the ticket from the code as it was, and measures whether the agents meet the goal. Changes to the
        baseline are tested on these cases, inside this project.
      </p>
      <button className={button} onClick={run} disabled={active.length === 0}>
        Run the eval suite
      </button>
      <Failed error={error || failed} />
      {evals && evals.suites.length === 0 && <p className="my-6 text-text-dim">No eval run yet.</p>}
      {evals && evals.suites.length > 0 && (
        <table className="my-5 w-full text-sm" data-suites>
          <Head>
            <tr className="border-b border-border">
              <th className="py-1">Suite</th>
              <th>For</th>
              <th>Runs</th>
              <th>Goals met</th>
              <th>With the change</th>
              <th>Decision</th>
              <th></th>
            </tr>
          </Head>
          <tbody>
            {evals.suites.map((s) => {
              const side = withChange(s)
              return (
                <tr key={s.id} className="border-b border-border/60 align-top" data-suite={s.state} data-suite-id={s.id}>
                  <td className="py-2 pr-2">
                    {when(s.createdAt)} <span className="text-xs text-text-dim">{s.state}</span>
                  </td>
                  <td className="pr-2">{suiteFor(s)}</td>
                  <td className="pr-2 tabular-nums">
                    {s.runsDone} of {s.runs}
                    {s.repeats > 1 && <span className="text-xs text-text-dim"> · {s.repeats} repeats</span>}
                  </td>
                  <td className="pr-2 tabular-nums" data-goals>
                    {s.unmeasured ? <span className="text-text-dim">nothing measured</span> : s.result ? percent(s.result.baseline.passed) : '—'}
                  </td>
                  <td className="pr-2 tabular-nums">
                    {side ? (side.cases === 0 ? <span className="text-text-dim">nothing measured</span> : percent(side.passed)) : s.purpose === 'on_demand' ? '' : '—'}
                  </td>
                  <td className="pr-2">{suiteNote(s)}</td>
                  <td className="pl-2 text-right">
                    <button className="text-xs text-accent underline" onClick={() => setOpenSuite(openSuite === s.id ? null : s.id)} data-suite-runs={s.id}>
                      {openSuite === s.id ? 'Hide runs' : 'Runs'}
                    </button>
                  </td>
                </tr>
              )
            })}
          </tbody>
        </table>
      )}
      {openSuite !== null && <SuiteRuns base={base} suite={openSuite} />}
      {evals && evals.cases.length > 0 && (
        <table className="mt-4 w-full text-sm" data-cases>
          <Head>
            <tr className="border-b border-border">
              <th className="py-1"></th>
              <th className="pr-3">Case</th>
              <th className="pr-3">From</th>
              <th className="pr-3">Why it was chosen</th>
              <th></th>
            </tr>
          </Head>
          <tbody>
            {evals.cases.map((c) => (
              <tr key={c.number} className={'border-b border-border/60 align-top ' + (c.status === 'retired' ? 'opacity-60' : '')} data-case={c.number} data-case-status={c.status}>
                <td className="py-2 pr-2 font-mono text-xs text-text-dim">{num(c.number)}</td>
                <td className="py-2 pr-2">
                  <p className="font-medium">{c.title}</p>
                  <p className="text-text-dim">{c.goal}</p>
                  {c.heldOut.length > 0 && <p className="text-xs text-text-dim">Held out: {c.heldOut.join(', ')}</p>}
                  {c.status === 'retired' && <p className="text-xs text-text-dim">Retired: {c.retiredWhy}</p>}
                  {fitNote(c) && (
                    <div className="mt-1 text-xs" data-fit={c.fit?.state ?? ''}>
                      <p className={c.status === 'waiting' ? 'text-amber-text' : 'text-text-dim'}>{fitNote(c)}</p>
                      {(c.fit?.list?.length ?? 0) > 0 && (
                        <ul className="ml-4 list-disc" data-fit-list>
                          {c.fit?.list?.map((m, i) => (
                            <li key={i}>{m}</li>
                          ))}
                        </ul>
                      )}
                      {c.fit?.judge && (
                        <p className="text-text-dim">
                          Judged by {c.fit.judge}, of another family than the builder's {c.fit.builder}
                        </p>
                      )}
                    </div>
                  )}
                  {editing === c.number && (
                    <CaseEditor
                      c={c}
                      save={(t) =>
                        send('PUT', `${base}/cases/${c.number}`, t).then(
                          () => (setEditing(null), setFailed(''), reload()),
                          (e: Error) => setFailed(e.message),
                        )
                      }
                      cancel={() => setEditing(null)}
                    />
                  )}
                </td>
                <td className="py-2 pr-3">
                  <TicketLink project={project} ticket={c.ticket ?? undefined} />
                  {c.bug ? <span className="ml-1 text-xs text-text-dim">bug {num(c.bug)}</span> : null}
                </td>
                <td className="py-2 pr-2">{c.chosenWhy}</td>
                <td className="py-2">
                  <div className="flex flex-wrap justify-end gap-1">
                    {c.status === 'waiting' && (
                      <>
                        <button className={button} onClick={() => accept(c.number)} data-accept={c.number}>
                          Accept
                        </button>
                        <button className={button} onClick={() => setEditing(c.number)} disabled={editing === c.number} data-edit={c.number}>
                          Change the text
                        </button>
                      </>
                    )}
                    {c.status !== 'retired' && (
                      <button className={button} onClick={() => retire(c.number)}>
                        Retire
                      </button>
                    )}
                  </div>
                </td>
              </tr>
            ))}
          </tbody>
        </table>
      )}
      {evals && evals.cases.length === 0 && <p className="mt-4 text-text-dim">No case yet: the lead proposes cases in its retrospectives.</p>}
      <div className="mt-6 flex flex-wrap items-end gap-2 text-sm">
        <label className="grid gap-1">
          <span className="text-text-dim">A case from a Done ticket</span>
          <input className="rounded-md border border-border bg-bg px-2 py-1" placeholder="SF-12" value={ticket} onChange={(e) => setTicket(e.target.value)} />
        </label>
        <label className="grid gap-1">
          <span className="text-text-dim">Its test files to hold out</span>
          <input className="w-80 rounded-md border border-border bg-bg px-2 py-1" placeholder="tests/test_lockout.py" value={heldOut} onChange={(e) => setHeldOut(e.target.value)} />
        </label>
        <button className={button} onClick={add} disabled={!ticket.trim()}>
          Make the case
        </button>
      </div>
    </>
  )
}

type CaseText = { title: string; why: string; what: string; goal: string }

function CaseEditor({ c, save, cancel }: { c: EvalCase; save: (t: CaseText) => void; cancel: () => void }) {
  const [t, setT] = useState<CaseText>({ title: c.title, why: c.why, what: c.what, goal: c.goal })
  const field = (key: keyof CaseText, rows: number) => (
    <label className="grid gap-1">
      <span className="text-xs text-text-dim">{key.charAt(0).toUpperCase() + key.slice(1)}</span>
      {rows === 1 ? (
        <input className="rounded-md border border-border bg-bg px-2 py-1" value={t[key]} onChange={(e) => setT({ ...t, [key]: e.target.value })} />
      ) : (
        <textarea className="rounded-md border border-border bg-bg px-2 py-1" rows={rows} value={t[key]} onChange={(e) => setT({ ...t, [key]: e.target.value })} />
      )}
    </label>
  )
  return (
    <div className="mt-2 grid gap-2" data-case-editor>
      {field('title', 1)}
      {field('why', 3)}
      {field('what', 5)}
      {field('goal', 3)}
      <div className="flex gap-2">
        <button className={button} onClick={() => save(t)} disabled={!t.title.trim() || !t.goal.trim()}>
          Save, and check again
        </button>
        <button className={button} onClick={cancel}>
          Cancel
        </button>
      </div>
    </div>
  )
}

function SuiteRuns({ base, suite }: { base: string; suite: number }) {
  const [runs, setRuns] = useState<EvalRun[] | null>(null)
  const [error, setError] = useState('')
  useEffect(() => {
    let gone = false
    setRuns(null)
    getJSON<{ runs: EvalRun[] }>(`${base}/runs?suite=${suite}`).then(
      (r) => gone || (setRuns(r.runs), setError('')),
      (e: Error) => gone || setError(e.message),
    )
    return () => {
      gone = true
    }
  }, [base, suite])
  return (
    <div className="mb-6" data-eval-runs={suite}>
      <Failed error={error} />
      {runs && runs.length === 0 && <p className="text-text-dim">No eval run in this suite.</p>}
      {runs && runs.length > 0 && (
        <table className="w-full text-sm">
          <Head>
            <tr className="border-b border-border">
              <th className="py-1">Case</th>
              <th>Repeat</th>
              <th>Side</th>
              <th>Ended</th>
              <th>Check rounds</th>
            </tr>
          </Head>
          <tbody>
            {runs.map((r) => (
              <tr key={r.id} className="border-b border-border/60 align-top" data-eval-run={r.id}>
                <td className="py-2 pr-2 font-mono text-xs">{num(r.case)}</td>
                <td className="pr-2 tabular-nums">{r.repeat}</td>
                <td className="pr-2">{r.side}</td>
                <td className="pr-2">
                  {r.state}
                  {r.goalMet === true ? ', goal met' : r.goalMet === false ? ', goal not met' : ''}
                  {r.why && <p className="text-xs text-text-dim">{r.why}</p>}
                  {r.failedOutput && (
                    <details className="mt-1" data-failed-output>
                      <summary className="cursor-pointer text-xs text-accent">The failed held-out checks' output</summary>
                      <pre className="mt-1 max-h-96 overflow-auto rounded-md bg-bg-sunk p-2 text-xs whitespace-pre-wrap">{r.failedOutput}</pre>
                    </details>
                  )}
                </td>
                <td className="pr-2 tabular-nums">
                  {r.checkRounds}
                  {r.rework > 0 && <span className="text-xs text-text-dim"> · {r.rework} rework</span>}
                </td>
              </tr>
            ))}
          </tbody>
        </table>
      )}
    </div>
  )
}
