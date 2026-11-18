import { useEffect, useState } from 'react'
import { send, type Rewrite } from '../lib/api'
import { usePoll } from '../lib/poll'
import { marks } from '../lib/textdiff'
import { button, mainButton } from './ui'

// Marked shows a rewrite with what it added marked, and its assumptions underlined (7.2: "the
// changes marked ... each assumption is marked in the rewrite").
export function Marked({ original, rewrite, assumptions }: { original: string; rewrite: string; assumptions: string[] }) {
  const added = new Array<boolean>(rewrite.length).fill(false)
  let at = 0
  for (const p of marks(original, rewrite)) {
    if (p.added) added.fill(true, at, at + p.text.trimEnd().length)
    at += p.text.length
  }
  const assumed = new Array<boolean>(rewrite.length).fill(false)
  for (const a of assumptions) {
    const i = rewrite.indexOf(a)
    if (i >= 0) assumed.fill(true, i, i + a.length)
  }
  const spans: { text: string; added: boolean; assumed: boolean }[] = []
  for (let i = 0; i < rewrite.length; i++) {
    const last = spans[spans.length - 1]
    if (last && last.added === added[i] && last.assumed === assumed[i]) last.text += rewrite[i]
    else spans.push({ text: rewrite[i]!, added: added[i]!, assumed: assumed[i]! })
  }
  return (
    <>
      {spans.map((s, i) => (
        <span
          key={i}
          className={(s.added ? 'bg-amber-soft ' : '') + (s.assumed ? 'underline decoration-amber decoration-dotted decoration-2 underline-offset-2' : '')}
          data-added={s.added || undefined}
          data-assumed={s.assumed || undefined}
        >
          {s.text}
        </span>
      ))}
    </>
  )
}

// FirstProposal is the prompt writer's proposal for the first message of a plan draft (7.2): your
// text and the rewrite side by side, its changes, its assumptions to strike out, and its questions to
// answer. Use the rewrite, Edit, Send mine, and Rewrite again, at most twice. Send mine is there from
// the first second.
export function FirstProposal({ id, onChosen, onAgain }: { id: number; onChosen: () => void; onAgain: (id: number) => void }) {
  const { data: r } = usePoll<Rewrite>(`/api/rewrites/${id}`, 1000)
  const [edit, setEdit] = useState<string | null>(null)
  const [answers, setAnswers] = useState<Record<number, string>>({})
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState('')
  const chosen = !!r?.choice
  useEffect(() => {
    if (chosen) onChosen()
  }, [chosen, onChosen])
  if (!r || r.choice) return null
  const choose = (choice: string, text = '') => {
    setBusy(true)
    send('POST', `/api/rewrites/${id}/choice`, { choice, text }).then(
      () => onChosen(),
      (e: Error) => {
        setBusy(false)
        setError(e.message)
      },
    )
  }
  const again = () => {
    setBusy(true)
    const given = r.questions.map((q, i) => ({ question: q, answer: answers[i] ?? '' })).filter((a) => a.answer.trim())
    send<Rewrite>('POST', '/api/rewrites', { kind: 'first', previous: id, answers: given }).then(
      (x) => {
        setBusy(false)
        setEdit(null)
        setAnswers({})
        if (x) onAgain(x.id)
      },
      (e: Error) => {
        setBusy(false)
        setError(e.message)
      },
    )
  }
  const who = r.cli ? `Prompt writer · ${r.cli} · ${r.model ?? ''}${r.effort ? ' · ' + r.effort : ''}` : 'Prompt writer'
  const strike = (a: string) => setEdit((edit ?? r.rewrite ?? '').replace(a, '').replace(/[ \t]{2,}/g, ' ').replace(/\n{3,}/g, '\n\n').trim())
  return (
    <section className="rounded-lg border border-amber bg-bg-raised p-4 text-sm" aria-label="Crew's rewrite of your message" data-proposal={r.state}>
      <p className="mb-3 flex text-xs tracking-wide text-text-dim uppercase">
        Crew's rewrite of your first message{r.round > 1 ? `, round ${r.round}` : ''}
        <span className="ml-auto normal-case">{who}</span>
      </p>
      {r.state === 'writing' && (
        <p className="mb-3 text-text-dim">
          The prompt writer is writing a better wording of your message. You can send yours as it is now; the planning agent gets only the text you
          choose.
        </p>
      )}
      <div className="grid gap-4 md:grid-cols-2">
        <div>
          <p className="mb-1 text-text-dim">Your text</p>
          <p className="whitespace-pre-wrap" data-original>
            {r.original}
          </p>
        </div>
        <div>
          <p className="mb-1 text-text-dim">{edit === null ? "Crew's rewrite" : "Crew's rewrite. You are editing it."}</p>
          {r.state === 'writing' && <p className="text-text-dim">…</p>}
          {r.state === 'ready' && r.rewrite && edit === null && (
            <p className="whitespace-pre-wrap" data-rewrite>
              <Marked original={r.original} rewrite={r.rewrite} assumptions={r.assumptions} />
            </p>
          )}
          {edit !== null && (
            <textarea aria-label="Your edit of the rewrite" className="h-48 w-full rounded-md border border-border bg-bg p-2" value={edit} onChange={(e) => setEdit(e.target.value)} />
          )}
        </div>
      </div>
      {r.state === 'ready' && (
        <div className="mt-3 grid gap-4 md:grid-cols-3">
          <div>
            <p className="mb-1 text-text-dim">What it changed</p>
            <ul className="list-disc space-y-0.5 pl-5">
              {r.changes.map((c, i) => (
                <li key={i}>{c}</li>
              ))}
              {r.changes.length === 0 && <li className="list-none text-text-dim">nothing listed</li>}
            </ul>
          </div>
          <div>
            <p className="mb-1 text-text-dim">What it assumed. Strike it out of the rewrite if it is wrong.</p>
            <ul className="space-y-1">
              {r.assumptions.map((a, i) => (
                <li key={i} className="flex items-start gap-2">
                  <span className="rounded-full border border-border px-1.5 text-xs">assumption</span>
                  <span className="flex-1">{a}</span>
                  <button className="text-xs text-accent" onClick={() => strike(a)}>
                    Strike out
                  </button>
                </li>
              ))}
              {r.assumptions.length === 0 && <li className="text-text-dim">none</li>}
            </ul>
          </div>
          <div>
            <p className="mb-1 text-text-dim">What it asks</p>
            {r.questions.map((q, i) => (
              <label key={i} className="mb-2 block">
                <span>{q}</span>
                <input
                  className="mt-1 w-full rounded-md border border-border bg-bg px-2 py-1"
                  value={answers[i] ?? ''}
                  onChange={(e) => setAnswers({ ...answers, [i]: e.target.value })}
                />
              </label>
            ))}
            {r.questions.length === 0 && <p className="text-text-dim">no questions</p>}
          </div>
        </div>
      )}
      <div className="mt-4 flex flex-wrap gap-2">
        {r.state === 'ready' && edit === null && (
          <>
            <button className={mainButton} disabled={busy} onClick={() => choose('rewrite')}>
              Use the rewrite
            </button>
            <button className={button} disabled={busy} onClick={() => setEdit(r.rewrite ?? '')}>
              Edit
            </button>
          </>
        )}
        {edit !== null && (
          <button className={mainButton} disabled={busy || !edit.trim()} onClick={() => choose('edited', edit)}>
            Send my edit
          </button>
        )}
        <button className={button} disabled={busy} onClick={() => choose('mine')}>
          Send mine
        </button>
        {r.state === 'ready' && r.questions.length > 0 && (
          <button
            className={button}
            disabled={busy || r.round >= 3 || !Object.values(answers).some((a) => a.trim())}
            onClick={again}
            title={r.round >= 3 ? 'Rewrite again works twice' : 'Answer a question first'}
          >
            Rewrite again
          </button>
        )}
      </div>
      {error && <p className="mt-2 text-bad">{error}</p>}
    </section>
  )
}
