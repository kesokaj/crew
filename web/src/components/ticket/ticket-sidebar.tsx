import { useState } from 'react'
import { getJSON, send, type ConvView, type QuoteSource, type Subtask } from '../../lib/api'
import { choiceName, fromWords, type Choice, type OfferedModel } from '../../lib/models'
import { usePoll } from '../../lib/poll'
import { modeName } from '../../lib/words'
import { Entries, markedUnder } from '../chat'
import { CLIModel } from '../runs'
import { button, Failed } from '../ui'
import { WriteBox } from '../writebox'
import { Box } from './ticket-header'

type TicketAgents = { pins: Record<string, { model?: string; effort?: string }>; roles: Record<string, Choice | { waits: string }>; offered: OfferedModel[] }
const pinRoles = ['builder', 'reviewer', 'tester'] as const

export function AgentBox({ base, ended, subs, network, onChange }: { base: string; ended: boolean; subs: Subtask[]; network: string; onChange: () => void }) {
  const { data, reload } = usePoll<TicketAgents>(base + '/agents', 15000)
  const [role, setRole] = useState<(typeof pinRoles)[number]>('builder')
  const [failed, setFailed] = useState('')
  const pin = data?.pins[role] ?? {}
  const save = (key: 'model' | 'effort', value: string) => {
    getJSON<TicketAgents>(base + '/agents')
      .then((now) => {
        const pins = { ...now.pins }
        const entry = { ...(pins[role] ?? {}) }
        if (value) entry[key] = value
        else delete entry[key]
        if (Object.keys(entry).length) pins[role] = entry
        else delete pins[role]
        return send('PATCH', base, { agents: pins })
      })
      .then(
        () => {
          setFailed('')
          reload()
          onChange()
        },
        (e: Error) => setFailed(e.message),
      )
  }
  const model = data?.offered.find((o) => o.entry === pin.model)
  const shown = (r: string) => {
    const c = data?.roles[r]
    if (!c) return '…'
    if ('waits' in c) return 'waits: ' + c.waits
    return `${choiceName(c)}, ${fromWords(c.ModelFrom)}`
  }
  const used = uniqueModels(subs)
  return (
    <Box title="Agent for this ticket">
      <Failed error={failed} />
      <div className="grid grid-cols-[5rem_1fr] items-center gap-2 text-sm">
        <label htmlFor="pin-role" className="text-text-dim">
          For
        </label>
        <select id="pin-role" className={button} value={role} onChange={(e) => setRole(e.target.value as (typeof pinRoles)[number])}>
          {pinRoles.map((r) => (
            <option key={r} value={r}>
              the {r}
              {data?.pins[r] ? ' (pinned)' : ''}
            </option>
          ))}
        </select>
        <label htmlFor="pin-model" className="text-text-dim">
          Model
        </label>
        <select id="pin-model" className={button} disabled={ended} value={pin.model ?? ''} onChange={(e) => save('model', e.target.value)}>
          <option value="">The project's model for each role</option>
          {data?.offered.map((o) => (
            <option key={o.entry} value={o.entry}>
              {o.cli} · {o.model}
            </option>
          ))}
        </select>
        <label htmlFor="pin-effort" className="text-text-dim">
          Effort
        </label>
        <select id="pin-effort" className={button} disabled={ended} value={pin.effort ?? ''} onChange={(e) => save('effort', e.target.value)}>
          <option value="">The project's effort for each role</option>
          {(model?.efforts.length ? model.efforts : ['low', 'medium', 'high', 'xhigh', 'max']).map((e) => (
            <option key={e}>{e}</option>
          ))}
        </select>
        <label htmlFor="ticket-network" className="text-text-dim">
          Network
        </label>
        <select
          id="ticket-network"
          className={button}
          disabled={ended}
          value={network}
          onChange={(e) =>
            send('PATCH', base, { network: e.target.value }).then(
              () => {
                setFailed('')
                onChange()
              },
              (err: Error) => setFailed(err.message),
            )
          }
        >
          <option value="inherit">The project's network</option>
          {(['internet', 'model_only', 'lan'] as const).map((m) => (
            <option key={m} value={m}>
              {modeName[m]}
            </option>
          ))}
        </select>
      </div>
      <dl className="mt-3 grid grid-cols-[8rem_1fr] gap-x-2 gap-y-1 text-sm">
        <dt className="text-text-dim">A builder would run on</dt>
        <dd className="font-mono text-xs">{shown('builder')}</dd>
        <dt className="text-text-dim">A check would run on</dt>
        <dd className="font-mono text-xs">{shown('reviewer')}</dd>
        <dt className="text-text-dim">A tester would run on</dt>
        <dd className="font-mono text-xs">{shown('tester')}</dd>
        <dt className="text-text-dim">Used so far</dt>
        <dd className="flex flex-col items-start gap-1">
          {used.map((m) => (
            <span key={m.key} className="text-sm">
              <CLIModel r={m} /> <span className="text-text-dim">{m.from}</span>
            </span>
          ))}
          {used.length === 0 && <span className="text-text-dim">No run yet.</span>}
        </dd>
      </dl>
      <p className="mt-2 text-xs text-text-dim">
        {ended
          ? 'The ticket is done: a change is a new ticket.'
          : 'Applies to the builder, reviewer, and tester runs of this ticket that start from now on. A running subtask keeps what it started with.'}
      </p>
    </Box>
  )
}

function uniqueModels(subs: Subtask[]) {
  const seen = new Map<string, { key: string; cli: string; model?: string; effort?: string; from: string }>()
  for (const s of subs)
    for (const r of s.runs) {
      const key = `${r.cli}/${r.model ?? ''}/${r.effort ?? ''}`
      if (!seen.has(key)) seen.set(key, { key, cli: r.cli, model: r.model, effort: r.effort, from: r.modelFrom ? fromWords(r.modelFrom) : '' })
    }
  return [...seen.values()]
}

export type Mark = { quote: string; source: QuoteSource }
const maxQuote = 20000
const maxLabel = 80

export function readMark(): (Mark & { x: number; y: number }) | null {
  const sel = window.getSelection()
  if (!sel || sel.isCollapsed || sel.rangeCount === 0) return null
  const active = document.activeElement
  if (active && active.matches('textarea, input, select')) return null
  const quote = sel.toString().trim()
  if (!quote) return null
  const range = sel.getRangeAt(0)
  const node = range.commonAncestorContainer
  const el = node instanceof Element ? node : node.parentElement
  if (!el || el.closest('textarea, input, select, [data-marked], [role="menu"], [role="form"]')) return null
  const part = el.closest('[data-part]')?.getAttribute('data-part') ?? 'other'
  const label = (el.closest('[data-label]')?.getAttribute('data-label') ?? 'the task view').replace(/\s+/g, ' ').slice(0, maxLabel)
  const r = range.getBoundingClientRect()
  return { quote: quote.slice(0, maxQuote), source: { part, label }, x: r.right, y: r.bottom }
}

export function MarkMenu({ at, onComment }: { at: { x: number; y: number }; onComment: () => void }) {
  return (
    <div
      role="menu"
      aria-label="On the marked text"
      className="fixed z-20 rounded-md border border-border bg-bg-raised p-1 shadow"
      style={{ left: Math.max(8, Math.min(at.x - 40, window.innerWidth - 120)), top: at.y + 6 }}
      onMouseDown={(e) => e.preventDefault()}
    >
      <button role="menuitem" className={button} onClick={onComment}>
        Comment
      </button>
    </div>
  )
}

export function TicketChat({
  base,
  project,
  ticket,
  ended,
  marked,
  setMarked,
  onTold,
}: {
  base: string
  project: string
  ticket: string
  ended: boolean
  marked: Mark | null
  setMarked: (m: Mark | null) => void
  onTold: () => void
}) {
  const { data: v, error, reload } = usePoll<ConvView>(base + '/chat', 3000)
  const [text, setText] = useState('')
  const [remember, setRemember] = useState(false)
  const [failed, setFailed] = useState('')
  const sent = () => {
    setText('')
    setRemember(false)
    setMarked(null)
    setFailed('')
  }
  const quoted = marked ? { quote: marked.quote, source: marked.source } : {}
  const ask = () =>
    send('POST', base + '/chat', { text, ...quoted }).then(
      () => {
        sent()
        reload()
      },
      (e: Error) => setFailed(e.message),
    )
  const tell = () =>
    send('POST', `/api/projects/${encodeURIComponent(project)}/comments`, { ticket, text, remember, ...quoted }).then(
      () => {
        sent()
        onTold()
      },
      (e: Error) => setFailed(e.message),
    )
  const agent = v ? [v.agent.cli, v.agent.model, v.agent.effort].filter(Boolean).join(' · ') : ''
  const right = v && (v.sandbox.state === 'running' || v.sandbox.state === 'idle') && v.sandbox.id ? `sandbox ${v.sandbox.id} · Assistant · ${agent}` : agent ? `Assistant · ${agent}` : ''
  return (
    <Box title="Chat about this ticket" part="message" right={<span className="font-mono text-text-dim">{right}</span>}>
      <div id="chat" />
      <Failed error={error} />
      {v && v.entries.length === 0 && (
        <p className="mb-3 text-sm text-text-dim">Ask the assistant what happened, or why. It reads this ticket, its runs and its code, and changes nothing.</p>
      )}
      {v && <Entries v={v} who="Assistant" asked={{ project, ticket, ended }} onDone={onTold} />}
      {marked && (
        <div className="mt-3 rounded-md border border-amber bg-amber-soft p-2 text-sm" data-marked>
          <div className="flex items-start gap-2">
            <blockquote className="max-h-32 flex-1 overflow-auto border-l-2 border-amber pl-2 whitespace-pre-wrap">{marked.quote}</blockquote>
            <button className="text-xs text-text-dim" aria-label="Leave out the marked text" onClick={() => setMarked(null)}>
              ×
            </button>
          </div>
          <p className="mt-0.5 text-xs text-text-dim">{markedUnder(marked.source)}</p>
        </div>
      )}
      <div className="mt-3 flex flex-wrap items-start gap-2">
        <div className="min-w-48 flex-1">
          <WriteBox id="chat-box" aria-label="Ask about this ticket" rows={2} value={text} onChange={setText} placeholder="Ask about this ticket" />
        </div>
        <button className="rounded-md bg-text px-4 py-2 font-semibold text-bg disabled:opacity-50" disabled={!text.trim() && !marked} onClick={() => void ask()}>
          Ask
        </button>
        {!ended && (
          <button className={button + ' py-2'} disabled={!text.trim()} onClick={() => void tell()}>
            Tell the agent
          </button>
        )}
      </div>
      {!ended && (
        <label className="mt-1 flex items-center gap-1.5 text-sm">
          <input type="checkbox" checked={remember} onChange={(e) => setRemember(e.target.checked)} />
          With Tell the agent, remember it for the project too
        </label>
      )}
      <Failed error={failed} />
      <p className="mt-2 text-xs text-text-dim">
        Select text anywhere on this page and choose Comment: it goes to this chat with the marked text quoted. Ask goes to the assistant and changes nothing.
        {!ended && ' Tell the agent is a comment to the agent that works on the ticket.'}
      </p>
    </Box>
  )
}
