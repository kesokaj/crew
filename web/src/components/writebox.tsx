import { createContext, useContext, useRef, useState, type TextareaHTMLAttributes } from 'react'
import { send, type Rewrite } from '../lib/api'
import { usePoll } from '../lib/poll'
import { Marked } from './proposal'
import { button, mainButton } from './ui'

// WriteScope says where what the operator writes belongs: a project, or a plan draft. The prompt
// writer gets that project's context and nothing of another (7.2, 10.6).
export const WriteScope = createContext<{ project?: string; draft?: string }>({})

// WriteBox is a box where you write. Selecting text in it shows a small menu with Improve text and
// Review text (7.2): the prompt writer answers, and nothing is sent by it. Use it puts the rewrite
// in the place of the selection; Review text changes nothing.
export function WriteBox({
  value,
  onChange,
  ...rest
}: { value: string; onChange: (v: string) => void } & Omit<TextareaHTMLAttributes<HTMLTextAreaElement>, 'value' | 'onChange'>) {
  const scope = useContext(WriteScope)
  const box = useRef<HTMLTextAreaElement>(null)
  const [sel, setSel] = useState<{ start: number; end: number } | null>(null)
  const [asked, setAsked] = useState<{ id: number; start: number; end: number } | null>(null)
  const [error, setError] = useState('')
  const can = !!(scope.project || scope.draft)
  const readSelection = () => {
    const el = box.current
    if (!el || !can) return
    const start = el.selectionStart
    const end = el.selectionEnd
    setSel(end > start && value.slice(start, end).trim() ? { start, end } : null)
  }
  const ask = (kind: 'improve' | 'review') => {
    if (!sel) return
    setError('')
    send<Rewrite>('POST', '/api/rewrites', { kind, text: value.slice(sel.start, sel.end), project: scope.project ?? '', draft: scope.draft ?? '' }).then(
      (r) => {
        if (r) setAsked({ id: r.id, ...sel })
        setSel(null)
      },
      (e: Error) => setError(e.message),
    )
  }
  return (
    <div className="relative">
      <textarea
        {...rest}
        ref={box}
        value={value}
        onChange={(e) => {
          onChange(e.target.value)
          setSel(null)
        }}
        onSelect={readSelection}
        onMouseUp={readSelection}
        onKeyUp={readSelection}
        className={'w-full rounded-md border border-border bg-bg-raised p-2 text-sm ' + (rest.className ?? '')}
      />
      {sel && !asked && (
        <div role="menu" aria-label="On the selection" className="absolute right-1 -bottom-9 z-10 flex gap-1 rounded-md border border-border bg-bg-raised p-1 shadow">
          <button role="menuitem" type="button" className={button} onMouseDown={(e) => e.preventDefault()} onClick={() => ask('improve')}>
            Improve text
          </button>
          <button role="menuitem" type="button" className={button} onMouseDown={(e) => e.preventDefault()} onClick={() => ask('review')}>
            Review text
          </button>
        </div>
      )}
      {error && <p className="mt-1 text-sm text-bad">{error}</p>}
      {asked && (
        <SelectionAnswer
          id={asked.id}
          onUse={(text) => {
            onChange(value.slice(0, asked.start) + text + value.slice(asked.end))
            setAsked(null)
          }}
          onClose={() => setAsked(null)}
        />
      )}
    </div>
  )
}

// SelectionAnswer is the prompt writer's answer on a selection: a rewrite to use, edit, or not; or
// at most five notes. When it is late or fails, it says so, and the text stays as it is.
function SelectionAnswer({ id, onUse, onClose }: { id: number; onUse: (text: string) => void; onClose: () => void }) {
  const { data: r } = usePoll<Rewrite>(`/api/rewrites/${id}`, 1000)
  const [edit, setEdit] = useState<string | null>(null)
  const [error, setError] = useState('')
  const choose = (choice: string, text = '') =>
    send<Rewrite>('POST', `/api/rewrites/${id}/choice`, { choice, text }).then(
      (x) => {
        if (choice === 'use' || choice === 'edited') onUse(x?.sent ?? text)
        else onClose()
      },
      (e: Error) => setError(e.message),
    )
  if (!r) return null
  const who = r.cli ? `Prompt writer · ${r.cli} · ${r.model ?? ''}${r.effort ? ' · ' + r.effort : ''}` : 'Prompt writer'
  return (
    <section className="mt-2 rounded-lg border border-amber bg-bg-raised p-3 text-sm" aria-label={r.kind === 'review' ? 'Review text' : 'Improve text'}>
      <p className="mb-2 flex text-xs tracking-wide text-text-dim uppercase">
        {r.kind === 'review' ? 'Review text' : 'Improve text'}
        <span className="ml-auto normal-case">{who}</span>
      </p>
      {r.state === 'writing' && <p className="text-text-dim">The prompt writer is reading it… your text stays as it is until you choose.</p>}
      {(r.state === 'late' || r.state === 'failed') && (
        <>
          <p className="text-amber-text">
            {r.state === 'late' ? 'The prompt writer was late' : 'The prompt writer could not answer'}
            {r.detail ? `: ${r.detail}` : ''}. Your text stays as it is.
          </p>
          <button className={button + ' mt-2'} onClick={onClose}>
            Close
          </button>
        </>
      )}
      {r.state === 'good' && (
        <>
          <p>Good as it is: nothing to change.</p>
          <button className={button + ' mt-2'} onClick={() => void choose('keep')}>
            Keep mine
          </button>
        </>
      )}
      {r.state === 'ready' && r.kind === 'review' && (
        <>
          <ul className="list-disc space-y-1 pl-5" data-notes>
            {r.notes.map((n, i) => (
              <li key={i}>{n}</li>
            ))}
          </ul>
          <p className="mt-2 text-xs text-text-dim">Nothing was changed. Change the text yourself.</p>
          <button className={button + ' mt-2'} onClick={() => void choose('seen')}>
            Close
          </button>
        </>
      )}
      {r.state === 'ready' && r.kind === 'improve' && r.rewrite && (
        <>
          <div className="grid gap-3 md:grid-cols-2">
            <div>
              <p className="mb-1 text-text-dim">What you selected</p>
              <p className="whitespace-pre-wrap">{r.original}</p>
            </div>
            <div>
              <p className="mb-1 text-text-dim">Crew's rewrite{edit !== null ? '. You can edit it.' : ''}</p>
              {edit === null ? (
                <p className="whitespace-pre-wrap" data-rewrite>
                  <Marked original={r.original} rewrite={r.rewrite} assumptions={r.assumptions} />
                </p>
              ) : (
                <textarea className="h-32 w-full rounded-md border border-border bg-bg p-2" value={edit} onChange={(e) => setEdit(e.target.value)} />
              )}
            </div>
          </div>
          {r.changes.length > 0 && (
            <>
              <p className="mt-2 text-text-dim">What it changed</p>
              <ul className="list-disc pl-5">
                {r.changes.map((c, i) => (
                  <li key={i}>{c}</li>
                ))}
              </ul>
            </>
          )}
          {r.assumptions.length > 0 && <p className="mt-2 text-xs text-text-dim">Underlined: what it assumed. Edit to strike one out.</p>}
          <div className="mt-3 flex flex-wrap gap-2">
            {edit === null ? (
              <>
                <button className={mainButton} onClick={() => void choose('use')}>
                  Use it
                </button>
                <button className={button} onClick={() => setEdit(r.rewrite ?? '')}>
                  Edit
                </button>
              </>
            ) : (
              <button className={mainButton} onClick={() => void choose('edited', edit)}>
                Use my edit
              </button>
            )}
            <button className={button} onClick={() => void choose('keep')}>
              Keep mine
            </button>
          </div>
          <p className="mt-1 text-xs text-text-dim">Use it puts the rewrite in the place of what you selected. Nothing is sent: you send it yourself when it is right.</p>
        </>
      )}
      {error && <p className="mt-1 text-bad">{error}</p>}
    </section>
  )
}
