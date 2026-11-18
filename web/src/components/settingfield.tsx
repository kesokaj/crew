import { useCallback, useEffect, useState, type ReactNode } from 'react'
import { ApiError, getJSON, send } from '../lib/api'
import { usePoll } from '../lib/poll'
import { freshFor, type SessionView } from '../lib/session'
import { lineText, problemFor, problemsAt, shown, valueAt, withValue, type Doc, type History, type SchemaView, type Schemas } from '../lib/settings'
import { button, when } from './ui'

export type SettingsAPI = { doc: string; history: string; putback: string; schema: 'machine' | 'project' }

export const machineAPI: SettingsAPI = {
  doc: '/api/settings/machine',
  history: '/api/settings/machine/history',
  putback: '/api/settings/machine/putback',
  schema: 'machine',
}

export function projectAPI(project: string): SettingsAPI {
  const base = `/api/projects/${encodeURIComponent(project)}/settings`
  return { doc: base, history: base + '/history', putback: base + '/putback', schema: 'project' }
}

export type Settings = {
  api: SettingsAPI
  doc: Doc | null
  schema: SchemaView | null
  error: string
  problems: Record<string, string>
  saved: number
  save: (path: string, value: unknown) => Promise<void>
  change: (f: (d: Doc) => Doc) => Promise<void>
  putBack: (seq: number, key: string) => Promise<void>
  session: SessionView | null
}

export function useSettings(api: SettingsAPI): Settings {
  const { data: doc, error, reload } = usePoll<Doc>(api.doc, 0)
  const { data: schemas } = usePoll<Schemas>('/api/settings/schema', 0)
  const { data: session } = usePoll<SessionView>('/api/sessions/this', 15000)
  const [problems, setProblems] = useState<Record<string, string>>({})
  const [saved, setSaved] = useState(0)
  const done = useCallback(() => {
    setProblems({})
    setSaved((n) => n + 1)
    reload()
  }, [reload])
  const refused = (e: unknown) => setProblems(problemsAt(e instanceof ApiError || e instanceof Error ? e.message : String(e)))
  const change = async (f: (d: Doc) => Doc) => {
    try {
      const fresh = await getJSON<Doc>(api.doc)
      await send('PUT', api.doc, f(fresh))
      done()
    } catch (e) {
      refused(e)
    }
  }
  const save = (path: string, value: unknown) => change((d) => withValue(d, path, value))
  const putBack = async (seq: number, key: string) => {
    try {
      await send('POST', api.putback, { seq, key })
      done()
    } catch (e) {
      refused(e)
    }
  }
  return { api, doc, schema: schemas ? schemas[api.schema] : null, error, problems, saved, save, change, putBack, session }
}

type FieldProps = {
  s: Settings
  path: string
  label: ReactNode
  words?: Record<string, string>
  readOnly?: string
  note?: ReactNode
  unit?: string
  order?: boolean
  fresh?: boolean
}

export function Field({ s, path, label, words, readOnly, note, unit, order, fresh }: FieldProps) {
  const value = valueAt(s.doc, path)
  const problem = problemFor(s.problems, path)
  const choices = s.schema?.enums[path]
  const [open, setOpen] = useState(false)
  const labelText = typeof label === 'string' ? label : path
  return (
    <div className="py-1.5" data-setting={path}>
      <div className="flex flex-wrap items-center justify-between gap-x-2 gap-y-1">
        <span className="min-w-0 flex-1 text-sm text-text-dim">{label}</span>
        <div className="flex items-center gap-1.5 text-sm">
          {s.doc === null ? null : readOnly ? (
            <span className="font-mono text-xs" data-value>
              {choices && words ? (words[String(value)] ?? shown(value)) : shown(value)}
              {unit && value !== null ? ` ${unit}` : ''}
            </span>
          ) : (
            <SettingControl s={s} path={path} value={value} choices={choices} words={words} unit={unit} order={order} label={label} />
          )}
          <button
            type="button"
            className="inline-flex min-h-8 min-w-8 shrink-0 items-center justify-center rounded-md border border-transparent px-1.5 text-xs text-text-dim transition-colors hover:border-border hover:bg-bg-sunk hover:text-accent focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-accent"
            title={open ? 'Hide change history' : `Change history for ${labelText}`}
            aria-label={open ? 'Hide the history' : 'History'}
            aria-expanded={open}
            onClick={() => setOpen(!open)}
          >
            <span aria-hidden="true">{open ? '×' : '↺'}</span>
          </button>
        </div>
      </div>
      {readOnly && <p className="text-xs text-text-dim">{readOnly}</p>}
      {fresh && <FreshNote session={s.session} />}
      {note && <p className="text-xs text-text-dim">{note}</p>}
      {problem && (
        <p className="mt-1 text-xs text-bad" data-problem>
          {problem}
        </p>
      )}
      {open && <HistoryLines s={s} path={path} readOnly={readOnly} />}
    </div>
  )
}

export function SettingControl({
  s,
  path,
  value,
  choices,
  words,
  unit,
  order,
  label,
}: {
  s: Settings
  path: string
  value: unknown
  choices?: string[]
  words?: Record<string, string>
  unit?: string
  order?: boolean
  label: ReactNode
}) {
  const name = typeof label === 'string' ? label : path
  if (choices) {
    if (choices.length > 4) {
      return (
        <select
          aria-label={name}
          className="min-h-8 rounded-md border border-border bg-bg-raised px-2.5 py-1 font-mono text-xs text-text focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-accent"
          value={String(value ?? '')}
          onChange={(e) => e.target.value !== String(value) && s.save(path, e.target.value)}
        >
          {choices.map((c) => (
            <option key={c} value={c}>
              {words?.[c] ?? c}
            </option>
          ))}
        </select>
      )
    }
    return (
      <div role="group" aria-label={name} className="inline-flex flex-wrap gap-0.5 rounded-md border border-border bg-bg-sunk p-0.5">
        {choices.map((c) => (
          <button
            key={c}
            type="button"
            className={
              'min-h-7 rounded px-2.5 py-1 text-xs transition-colors focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-accent ' +
              (value === c ? 'bg-text font-medium text-bg shadow-2xs' : 'text-text hover:bg-bg-raised/70')
            }
            aria-pressed={value === c}
            onClick={() => value !== c && s.save(path, c)}
          >
            {words?.[c] ?? c}
          </button>
        ))}
      </div>
    )
  }
  if (typeof value === 'boolean') {
    return (
      <button type="button" className={button + ' min-h-8 py-1 text-xs'} aria-pressed={value} aria-label={name} onClick={() => s.save(path, !value)}>
        {value ? 'On' : 'Off'}
      </button>
    )
  }
  const float = s.schema?.floats.includes(path) ?? false
  if (typeof value === 'number' && Number.isInteger(value) && !float && !s.schema?.nullable[path]) {
    return <Stepper value={value} name={name} unit={unit} onSave={(n) => s.save(path, n)} />
  }
  if (Array.isArray(value) && order) return <Order items={value.map(String)} name={name} onSave={(l) => s.save(path, l)} />
  if (Array.isArray(value)) return <Lines items={value.map(String)} name={name} onSave={(l) => s.save(path, l)} />
  const kind = s.schema?.nullable[path] ?? (float || typeof value === 'number' ? 'float' : 'string')
  return <Text value={value} kind={kind} name={name} unit={unit} onSave={(v) => s.save(path, v)} />
}

export function Stepper({ value, name, unit, onSave }: { value: number; name: string; unit?: string; onSave: (n: number) => void }) {
  const [text, setText] = useState(String(value))
  useEffect(() => setText(String(value)), [value])
  const commit = () => {
    const n = Number(text)
    if (text.trim() !== '' && Number.isInteger(n) && n !== value) onSave(n)
    else setText(String(value))
  }
  return (
    <span className="inline-flex items-center gap-1">
      <button className={button + ' px-2 py-0.5'} aria-label={`Less: ${name}`} onClick={() => onSave(value - 1)}>
        −
      </button>
      <input
        aria-label={name}
        className="w-12 rounded-md border border-border bg-bg px-1 py-0.5 text-center text-sm"
        value={text}
        inputMode="numeric"
        onChange={(e) => setText(e.target.value)}
        onBlur={commit}
        onKeyDown={(e) => e.key === 'Enter' && commit()}
      />
      <button className={button + ' px-2 py-0.5'} aria-label={`More: ${name}`} onClick={() => onSave(value + 1)}>
        +
      </button>
      {unit && <span className="text-xs text-text-dim">{unit}</span>}
    </span>
  )
}

function Text({ value, kind, name, unit, onSave }: { value: unknown; kind: string; name: string; unit?: string; onSave: (v: unknown) => void }) {
  const initial = value === null || value === undefined ? '' : String(value)
  const [text, setText] = useState(initial)
  useEffect(() => setText(initial), [initial])
  const commit = () => {
    if (text === initial) return
    const t = text.trim()
    if (t === '') onSave(null)
    else if (kind === 'int' || kind === 'float') {
      const n = Number(t)
      if (Number.isNaN(n)) setText(initial)
      else onSave(n)
    } else onSave(t)
  }
  return (
    <span className="inline-flex items-center gap-1">
      <input
        aria-label={name}
        className="w-36 max-w-full rounded-md border border-border bg-bg px-2 py-0.5 font-mono text-xs"
        value={text}
        placeholder="none"
        onChange={(e) => setText(e.target.value)}
        onBlur={commit}
        onKeyDown={(e) => e.key === 'Enter' && commit()}
      />
      {unit && <span className="text-xs text-text-dim">{unit}</span>}
    </span>
  )
}

function Lines({ items, name, onSave }: { items: string[]; name: string; onSave: (l: string[]) => void }) {
  const initial = items.join('\n')
  const [text, setText] = useState(initial)
  useEffect(() => setText(initial), [initial])
  return (
    <span className="flex flex-col items-end gap-1">
      <textarea aria-label={name} className="h-24 w-52 max-w-full rounded-md border border-border bg-bg px-2 py-1 font-mono text-xs" value={text} onChange={(e) => setText(e.target.value)} />
      <button
        className={button + ' py-0.5 text-xs'}
        disabled={text === initial}
        onClick={() => onSave(text.split('\n').map((l) => l.trim()).filter(Boolean))}
      >
        Save
      </button>
    </span>
  )
}

function Order({ items, name, onSave }: { items: string[]; name: string; onSave: (l: string[]) => void }) {
  const move = (i: number, by: number) => {
    const l = [...items]
    const a = l[i]
    const b = l[i + by]
    if (a === undefined || b === undefined) return
    l[i] = b
    l[i + by] = a
    onSave(l)
  }
  return (
    <ol className="space-y-0.5 text-xs" aria-label={name}>
      {items.map((it, i) => (
        <li key={it} className="flex items-center gap-1 font-mono">
          <span className="w-40">{it}</span>
          <button className="px-1 disabled:opacity-30" aria-label={`Up: ${it}`} disabled={i === 0} onClick={() => move(i, -1)}>
            ↑
          </button>
          <button className="px-1 disabled:opacity-30" aria-label={`Down: ${it}`} disabled={i === items.length - 1} onClick={() => move(i, 1)}>
            ↓
          </button>
        </li>
      ))}
    </ol>
  )
}

function HistoryLines({ s, path, readOnly }: { s: Settings; path: string; readOnly?: string }) {
  const [h, setH] = useState<History | null>(null)
  const [failed, setFailed] = useState('')
  useEffect(() => {
    getJSON<History>(`${s.api.history}?key=${encodeURIComponent(path)}`).then(
      (d) => (setH(d), setFailed('')),
      () => setFailed('history cannot be read'),
    )
  }, [path, s.api.history, s.saved])
  return (
    <div className="mt-1 rounded border border-border/60 p-2 text-xs" data-history={path}>
      {failed && <p className="text-bad">{failed}</p>}
      {h && <p className="text-text-dim">Default: {shown(h.default)}</p>}
      {h && h.changes.length === 0 && <p className="text-text-dim">Not changed yet.</p>}
      {readOnly && h && h.changes.length > 0 && <p className="text-text-dim">Read only here, so there is no Put back.</p>}
      <ul className="space-y-1">
        {h?.changes.map((l) => (
          <li key={l.seq} className="flex flex-wrap items-center gap-x-2" data-line={l.seq}>
            <span className="text-text-dim">
              {when(l.at)} · {l.who}
            </span>
            <span className="font-mono">{lineText(l)}</span>
            {!readOnly && (
              <button className="ml-auto text-accent underline" onClick={() => s.putBack(l.seq, path)}>
                Put back
              </button>
            )}
          </li>
        ))}
      </ul>
    </div>
  )
}

export function FreshNote({ session }: { session: SessionView | null }) {
  const left = freshFor(session)
  return (
    <p className="text-xs text-text-dim" data-fresh-note>
      {left > 0
        ? `Asks for a fresh sign-in: yours is fresh for ${left} more ${left === 1 ? 'minute' : 'minutes'}.`
        : `Asks for a fresh sign-in, at most ${session?.freshMinutes ?? 15} minutes old.`}
    </p>
  )
}

export function Problems({ s }: { s: Settings }) {
  const all = Object.entries(s.problems).map(([k, v]) => (k ? `${k}: ${v}` : v))
  if (!all.length && !s.error) return null
  return (
    <p className="my-3 rounded-md border border-bad p-3 text-sm whitespace-pre-line" data-failed>
      {s.error || 'The change was refused, and nothing was stored:\n' + all.join('\n')}
    </p>
  )
}

export function Brackets({ s }: { s: Settings }) {
  const brackets = (valueAt(s.doc, 'agents.brackets') ?? {}) as Record<string, string[]>
  const [name, setName] = useState('')
  const [entry, setEntry] = useState('')
  const keys = Object.keys(brackets).sort((a, b) => (a === 'pro' ? -1 : b === 'pro' ? 1 : a === 'flash' ? -1 : b === 'flash' ? 1 : a.localeCompare(b)))
  return (
    <div data-brackets>
      <p className="mt-3 text-xs tracking-wide text-text-dim uppercase">Brackets across limit groups</p>
      {keys.map((b) => (
        <Field key={b} s={s} path={`agents.brackets.${b}`} label={`Bracket ${b}`} />
      ))}
      <div className="mt-2 flex flex-wrap items-center gap-2 text-xs">
        <input aria-label="New bracket name" className="w-28 rounded-md border border-border bg-bg px-2 py-1 font-mono" value={name} placeholder="bracket" onChange={(e) => setName(e.target.value)} />
        <input aria-label="First CLI/model in the new bracket" className="w-44 rounded-md border border-border bg-bg px-2 py-1 font-mono" value={entry} placeholder="cli/model" onChange={(e) => setEntry(e.target.value)} />
        <button className={button + ' py-0.5 text-xs'} disabled={!name.trim() || !entry.trim()} onClick={() => { s.save(`agents.brackets.${name.trim()}`, [entry.trim()]); setName(''); setEntry('') }}>
          Add bracket
        </button>
      </div>
    </div>
  )
}
