import { useState } from 'react'
import { getJSON, send } from '../lib/api'
import { usePoll } from '../lib/poll'
import { button, Failed, mainButton, when } from './ui'

type Override = {
  number: number
  path: string
  version: number
  text: string
  why: string
  status: string
  proposedBy: string
  eval?: { verdict?: string; why?: string; passedBefore?: number; passedAfter?: number } | null
  measure?: { verdict?: string; why?: string } | null
  decidedWhy?: string | null
  appliedAt?: string | null
  createdAt: string
  waits: boolean
}
type Overrides = { overrides: Override[]; files: { path: string; waits: boolean }[] }

const statusWords: Record<string, string> = {
  proposed: 'proposed',
  waiting: 'waits for you',
  testing: 'being tested on the evals',
  applied: '✓ applied',
  refused: 'refused',
  taken_back: 'taken back',
  withdrawn: 'withdrawn',
  replaced: 'replaced by a later version',
}

// This project's own versions of files of the baseline (10.3, 9.6): what the lead proposed and how
// its evals decided, what applies now, and your own, which applies at once. A change of how work is
// checked, or of the prompt writer's rules, waits for you.
export function OverridesSection({ project }: { project: string }) {
  const base = `/api/projects/${encodeURIComponent(project)}/overrides`
  const { data, error, reload } = usePoll<Overrides>(base, 10000)
  const [failed, setFailed] = useState('')
  const [open, setOpen] = useState(false)
  const [path, setPath] = useState('stages/in_progress.md')
  const [text, setText] = useState('')
  const [why, setWhy] = useState('')
  const [shown, setShown] = useState<number | null>(null)
  const loadDefaultForPath = (targetPath: string) => {
    const applied = (data?.overrides ?? []).find((o) => o.path === targetPath && o.status === 'applied')
    if (applied) {
      setText((prev) => prev || applied.text)
      return
    }
    getJSON<{ content: string }>(
      `/api/projects/${encodeURIComponent(project)}/skills/file?path=${encodeURIComponent('baseline/' + targetPath)}&base=1`,
    )
      .then((res) => setText((prev) => prev || res.content))
      .catch(() => {})
  }
  const decide = (n: number, choice: string) =>
    send('POST', `${base}/${n}`, { choice }).then(
      () => (setFailed(''), reload()),
      (e: Error) => setFailed(e.message),
    )
  const save = () =>
    send('POST', base, { path, text, why }).then(
      () => (setOpen(false), setText(''), setWhy(''), setFailed(''), reload()),
      (e: Error) => setFailed(e.message),
    )
  const list = data?.overrides ?? []
  return (
    <section className="mt-6" data-overrides>
      <h2 className="mb-1 text-xl font-bold">This project's own baseline</h2>
      <p className="mb-3 max-w-3xl text-sm text-text-dim">
        A project can replace a file of Crew's baseline for itself: a stage prompt, an agent's definition, a guide, or a template. The lead proposes
        one in housekeeping or a retrospective, and it applies when the project's evals are not worse; yours applies at once. Crew measures each one
        afterwards and takes it back when the project's results get worse. A change of how work is checked, or of the prompt writer's rules, waits
        for you.
      </p>
      <Failed error={error || failed} />
      {data && list.length === 0 && <p className="mb-3 text-sm text-text-dim">No override: every run reads Crew's baseline.</p>}
      {list.length > 0 && (
        <table className="mb-3 w-full text-sm">
          <thead className="text-left text-xs tracking-wide text-text-dim uppercase">
            <tr className="border-b border-border">
              <th className="py-1"></th>
              <th>File</th>
              <th>From</th>
              <th>Status</th>
              <th>Evals and measure</th>
              <th></th>
            </tr>
          </thead>
          <tbody>
            {list.map((o) => (
              <tr key={o.number} className="border-b border-border/60 align-top" data-override={o.status}>
                <td className="py-2 pr-2 font-mono text-xs text-text-dim">o_{o.number}</td>
                <td className="py-2 pr-2">
                  <button className="font-mono text-xs text-accent underline" onClick={() => setShown(shown === o.number ? null : o.number)}>
                    {o.path}
                  </button>{' '}
                  <span className="text-xs text-text-dim">v{o.version}</span>
                  {o.why && <p className="text-text-dim">{o.why}</p>}
                  {shown === o.number && <pre className="mt-1 max-h-72 overflow-auto rounded-md bg-bg-sunk p-2 text-xs whitespace-pre-wrap">{o.text}</pre>}
                </td>
                <td className="py-2 pr-2">
                  {o.proposedBy === 'operator' ? 'you' : `the ${o.proposedBy.split(' · ')[0]}`}
                  <p className="text-xs text-text-dim">{when(o.createdAt)}</p>
                </td>
                <td className="py-2 pr-2">
                  {statusWords[o.status] ?? o.status}
                  {o.decidedWhy && <p className="text-xs text-text-dim">{o.decidedWhy}</p>}
                </td>
                <td className="py-2 pr-2 text-xs">
                  {o.eval?.why && <p>{o.eval.why}</p>}
                  {o.measure?.why && <p className="text-text-dim">Measured: {o.measure.why}</p>}
                </td>
                <td className="py-2 whitespace-nowrap">
                  {(o.status === 'waiting' || o.status === 'testing') && (
                    <>
                      <button className={mainButton} onClick={() => decide(o.number, 'apply')}>
                        Apply
                      </button>{' '}
                      <button className={button} onClick={() => decide(o.number, 'refuse')}>
                        Refuse
                      </button>
                    </>
                  )}
                  {o.status === 'applied' && (
                    <button className={button} onClick={() => decide(o.number, 'take_back')}>
                      Take back
                    </button>
                  )}
                </td>
              </tr>
            ))}
          </tbody>
        </table>
      )}
      {!open && (
        <button
          className={button}
          onClick={() => {
            setOpen(true)
            loadDefaultForPath(path)
          }}
        >
          Write this project's own version of a file
        </button>
      )}
      {open && (
        <div className="grid gap-2 rounded-lg border border-border bg-bg-raised p-3 text-sm">
          <label className="grid gap-1">
            <span className="text-text-dim">The file of the baseline</span>
            <select
              className="w-80 rounded-md border border-border bg-bg px-2 py-1"
              value={path}
              onChange={(e) => {
                const next = e.target.value
                setPath(next)
                setText('')
                loadDefaultForPath(next)
              }}
            >
              {(data?.files ?? []).map((f) => (
                <option key={f.path} value={f.path}>
                  {f.path}
                  {f.waits ? ' (checks, or the prompt writer)' : ''}
                </option>
              ))}
            </select>
          </label>
          <label className="grid gap-1">
            <span className="text-text-dim">The whole new file</span>
            <textarea className="h-48 rounded-md border border-border bg-bg p-2 font-mono text-xs" value={text} onChange={(e) => setText(e.target.value)} />
          </label>
          <label className="grid gap-1">
            <span className="text-text-dim">Why</span>
            <input className="rounded-md border border-border bg-bg px-2 py-1" value={why} onChange={(e) => setWhy(e.target.value)} />
          </label>
          <div className="flex gap-2">
            <button className={mainButton} onClick={save} disabled={!text.trim()}>
              Apply it to this project
            </button>
            <button className={button} onClick={() => setOpen(false)}>
              Cancel
            </button>
          </div>
        </div>
      )}
    </section>
  )
}
