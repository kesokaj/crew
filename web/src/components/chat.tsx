import { useState, type ReactNode } from 'react'
import { send, type ConvEntry, type ConvView } from '../lib/api'
import { markedUnder } from '../lib/marked'
import { reasons } from './runs'
import { button, Failed, mainButton, when } from './ui'

// The parts of the screens of design 11.8 and 11.9 that a conversation and a ticket's chat share:
// the messages, with what the agent did in each turn, and the three buttons under each of its
// answers. Only the operator's buttons send something on; each opens a form with the text to check
// and change first (11.9).

// How a turn was continued (12.10): conv.png says it in the head of the answer.
export const wayWords: Record<string, string> = {
  new: 'a new conversation',
  same: 'the same sandbox, a new call of the CLI',
  files: "a new sandbox, the CLI's files put back",
  rebuilt: 'a new sandbox, rebuilt from the turns',
}

export { markedUnder } from '../lib/marked'

export function agentLine(e: ConvEntry): string {
  return [e.cli, e.model, e.effort].filter(Boolean).join(' · ')
}

// You is a message of the operator's, with the text they marked quoted above it.
function You({ e }: { e: ConvEntry }) {
  return (
    <li className="ml-auto max-w-[85%] rounded-lg border border-amber bg-amber-soft p-3 text-sm" data-entry="you">
      <p className="mb-1 text-xs tracking-wide text-text-dim uppercase">You · {when(e.at)}</p>
      {e.quote && (
        <div className="mb-2" data-quote>
          <blockquote className="border-l-2 border-amber pl-2 whitespace-pre-wrap" data-free-text>
            {e.quote}
          </blockquote>
          <p className="mt-0.5 text-xs text-text-dim">{markedUnder(e.source)}</p>
        </div>
      )}
      {e.text && (
        <p className="whitespace-pre-wrap" data-free-text>
          {e.text}
        </p>
      )}
      {e.ended && <Ended e={e} />}
    </li>
  )
}

function Ended({ e }: { e: ConvEntry }) {
  if (!e.ended) return null
  return (
    <p className="mt-2 text-xs text-bad" data-ended={e.ended.reason}>
      {reasons[e.ended.reason]?.text ?? e.ended.reason}
      {e.ended.detail ? `: ${e.ended.detail}` : ''}. Nothing goes on by itself: your next message continues in a new sandbox.
    </p>
  )
}

export type Asked = { project?: string; ticket?: string; ended?: boolean; projects?: { name: string; key: string }[] }

// Entries is the conversation: the operator's messages and the agent's answers, in order. who is the
// agent's name in the head of an answer, "Assistant" in a chat; asked says which buttons an answer
// has: Tell the agent is not on an ended ticket, and a normal conversation outside a project offers
// Move to project backlog when projects exist.
export function Entries({ v, who, asked, onDone }: { v: ConvView; who?: string; asked: Asked; onDone: () => void }) {
  return (
    <ol className="flex flex-col gap-3" aria-label="Messages">
      {v.entries.map((e, i) =>
        e.kind === 'you' ? (
          <You key={'m' + e.id} e={e} />
        ) : (
          <Answer key={'t' + e.turn} v={v} e={e} prev={v.entries.slice(0, i).findLast((x) => x.kind === 'you')} who={who} asked={asked} onDone={onDone} />
        ),
      )}
      {v.sandbox.state === 'running' && <li className="text-sm text-accent">● The agent is working…</li>}
      {v.waiting && v.sandbox.state !== 'running' && (
        <li className="text-sm text-text-dim" data-waiting>
          Your message waits: {v.waiting}
        </li>
      )}
    </ol>
  )
}

type Form = 'tell' | 'ticket' | 'remember' | null

function Answer({ v, e, prev, who, asked, onDone }: { v: ConvView; e: ConvEntry; prev?: ConvEntry; who?: string; asked: Asked; onDone: () => void }) {
  const [form, setForm] = useState<Form>(null)
  const done = () => {
    setForm(null)
    onDone()
  }
  const head = [who, agentLine(e), when(e.at), e.way ? wayWords[e.way] : ''].filter(Boolean).join(' · ')
  return (
    <li className="mr-auto max-w-[85%] rounded-lg border border-border bg-bg-raised p-3 text-sm" data-entry="agent" data-turn={e.turn}>
      <p className="mb-1 text-xs tracking-wide text-text-dim uppercase">{head}</p>
      <p className="whitespace-pre-wrap" data-free-text>
        {e.text}
      </p>
      {(e.tools ?? []).length > 0 && (
        <pre className="mt-2 font-mono text-xs whitespace-pre-wrap text-text-dim" data-tools>
          {(e.tools ?? []).join('\n')}
        </pre>
      )}
      {(e.tickets ?? []).length > 0 && (
        <p className="mt-2 text-xs text-text-dim">
          It proposed {(e.tickets ?? []).join(', ')}: Make a ticket of this makes it yours.
        </p>
      )}
      {e.ended && <Ended e={e} />}
      {asked.project ? (
        <div className="mt-2 flex flex-wrap gap-2">
          {!asked.ended && (
            <button className={button} onClick={() => setForm(form === 'tell' ? null : 'tell')}>
              Tell the agent
            </button>
          )}
          <button className={button} onClick={() => setForm(form === 'ticket' ? null : 'ticket')}>
            Make a ticket of this
          </button>
          <button className={button} onClick={() => setForm(form === 'remember' ? null : 'remember')}>
            Remember this
          </button>
        </div>
      ) : (
        v.purpose === 'operator' &&
        (asked.projects ?? []).length > 0 && (
          <div className="mt-2 flex flex-wrap gap-2">
            <button className={button} onClick={() => setForm(form === 'ticket' ? null : 'ticket')}>
              Move to project backlog
            </button>
          </div>
        )
      )}
      {form === 'tell' && <TellForm v={v} e={e} asked={asked} onDone={done} onCancel={() => setForm(null)} />}
      {form === 'ticket' && <TicketForm v={v} e={e} prev={prev} projects={asked.projects} onDone={done} onCancel={() => setForm(null)} />}
      {form === 'remember' && <RememberForm v={v} e={e} onDone={done} onCancel={() => setForm(null)} />}
    </li>
  )
}

const input = 'w-full rounded-md border border-border bg-bg px-2 py-1 text-sm'

function FormBox({ title, children, note }: { title: string; children: ReactNode; note: string }) {
  return (
    <div className="mt-3 rounded-md border border-amber bg-bg p-3" role="form" aria-label={title}>
      <p className="mb-2 font-semibold">{title}</p>
      {children}
      <p className="mt-2 text-xs text-text-dim">{note}</p>
    </div>
  )
}

function Buttons({ ok, label, busy, onCancel }: { ok: () => void; label: string; busy: boolean; onCancel: () => void }) {
  return (
    <div className="mt-2 flex gap-2">
      <button className={mainButton} disabled={busy} onClick={ok}>
        {label}
      </button>
      <button className={button} onClick={onCancel}>
        Cancel
      </button>
    </div>
  )
}

// useSend sends a form and says what was refused.
function useSend(onDone: () => void) {
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState('')
  const go = (path: string, body: unknown) => {
    setBusy(true)
    setError('')
    send('POST', path, body).then(
      () => {
        setBusy(false)
        onDone()
      },
      (err: Error) => {
        setBusy(false)
        setError(err.message)
      },
    )
  }
  return { busy, error, go }
}

// TellForm sends the answer on to the agent that works on the ticket, as the chat's assistant's text
// the operator sent on (11.9, D11). A conversation asks which ticket of its project (D26).
function TellForm({ v, e, asked, onDone, onCancel }: { v: ConvView; e: ConvEntry; asked: Asked; onDone: () => void; onCancel: () => void }) {
  const [text, setText] = useState(e.text)
  const [ticket, setTicket] = useState(asked.ticket ?? '')
  const s = useSend(onDone)
  return (
    <FormBox
      title="Tell the agent"
      note="It becomes a comment on the ticket, from the assistant, sent on by you. The working agent gets it now, or the next run on the ticket does."
    >
      {!asked.ticket && <input aria-label="The ticket" className={input + ' mb-2 font-mono'} placeholder={`${v.project}-1`} value={ticket} onChange={(x) => setTicket(x.target.value.toUpperCase())} />}
      <textarea aria-label="The text the agent gets" rows={5} className={input} value={text} onChange={(x) => setText(x.target.value)} />
      <Failed error={s.error} />
      <Buttons label="Tell the agent" busy={s.busy || !text.trim() || !ticket} onCancel={onCancel} ok={() => s.go(`/api/conversations/${v.id}/tell`, { turn: e.turn, ticket: asked.ticket ? undefined : ticket, text })} />
    </FormBox>
  )
}

const kinds = ['feature', 'fix', 'chore', 'docs'] as const

function firstLine(t: string, n: number): string {
  const l = t.trim().split('\n')[0] ?? ''
  return l.length > n ? l.slice(0, n - 1) + '…' : l
}

// TicketForm is Make a ticket of this (or Move to project backlog in a normal conversation): a
// title, a why, a what and a goal, drafted from the answer and the question before it, which the
// operator changes and confirms. When the answer's run proposed a ticket, it can be adopted instead
// of adding another (D19).
export function TicketForm({
  v,
  e,
  prev,
  projects,
  onDone,
  onCancel,
}: {
  v: ConvView
  e: ConvEntry
  prev?: ConvEntry
  projects?: { name: string; key: string }[]
  onDone: () => void
  onCancel: () => void
}) {
  const proposed = e.tickets ?? []
  const [adopt, setAdopt] = useState(proposed[0] ?? '')
  const [project, setProject] = useState(v.projectName ?? projects?.[0]?.name ?? '')
  const [title, setTitle] = useState(firstLine(e.text, 80))
  const [kind, setKind] = useState<string>('feature')
  const where = v.purpose === 'chat' ? `the chat of ${v.ticket}` : `the conversation "${v.title}"`
  const [why, setWhy] = useState(prev?.text ? `Asked in ${where}: ${prev.text}` : `From ${where}.`)
  const [what, setWhat] = useState(e.text)
  const [goal, setGoal] = useState('')
  const s = useSend(onDone)
  const isNormal = !v.projectName && v.purpose === 'operator'
  return (
    <FormBox
      title={isNormal ? 'Move to project backlog' : 'Make a ticket of this'}
      note={
        v.purpose === 'chat'
          ? `It is your ticket, approved by adding it, linked to ${v.ticket} as created from it. The assistant drafted it, so its Review cannot be skipped.`
          : 'It is your ticket, approved by adding it. The assistant drafted it, so its Review cannot be skipped.'
      }
    >
      {proposed.length > 0 && (
        <select aria-label="Which ticket" className={input + ' mb-2'} value={adopt} onChange={(x) => setAdopt(x.target.value)}>
          {proposed.map((k) => (
            <option key={k} value={k}>
              Adopt {k}, which the agent proposed
            </option>
          ))}
          <option value="">A new ticket</option>
        </select>
      )}
      <div className="grid grid-cols-[4rem_1fr] items-start gap-2">
        {isNormal && (
          <>
            <label className="text-text-dim">Project</label>
            <select aria-label="Project" className={input} value={project} onChange={(x) => setProject(x.target.value)}>
              {(projects ?? []).map((p) => (
                <option key={p.name} value={p.name}>
                  {p.name} ({p.key})
                </option>
              ))}
            </select>
          </>
        )}
        <label className="text-text-dim">Title</label>
        <input aria-label="Title" className={input} value={title} onChange={(x) => setTitle(x.target.value)} />
        <label className="text-text-dim">Kind</label>
        <select aria-label="Kind" className={input} value={kind} onChange={(x) => setKind(x.target.value)}>
          {kinds.map((k) => (
            <option key={k}>{k}</option>
          ))}
        </select>
        <label className="text-text-dim">Why</label>
        <textarea aria-label="Why" rows={2} className={input} value={why} onChange={(x) => setWhy(x.target.value)} />
        <label className="text-text-dim">What</label>
        <textarea aria-label="What" rows={4} className={input} value={what} onChange={(x) => setWhat(x.target.value)} />
        <label className="text-text-dim">Goal</label>
        <textarea aria-label="Goal" rows={2} className={input} placeholder="What is true when it is done" value={goal} onChange={(x) => setGoal(x.target.value)} />
      </div>
      <Failed error={s.error} />
      <Buttons
        label={adopt ? `Adopt ${adopt}` : isNormal ? 'Move to backlog' : 'Make the ticket'}
        busy={s.busy || (isNormal && !project.trim()) || !title.trim() || !why.trim() || !what.trim() || !goal.trim()}
        onCancel={onCancel}
        ok={() =>
          s.go(`/api/conversations/${v.id}/ticket`, {
            project: isNormal ? project : undefined,
            turn: e.turn,
            adopt: adopt || undefined,
            title,
            kind,
            why,
            what,
            goal,
          })
        }
      />
    </FormBox>
  )
}

const memoryKinds = ['convention', 'fact', 'decision', 'lesson'] as const

// RememberForm keeps the answer as a memory entry of the project, which says the assistant wrote it
// and the operator confirmed it (9.3, 11.9).
function RememberForm({ v, e, onDone, onCancel }: { v: ConvView; e: ConvEntry; onDone: () => void; onCancel: () => void }) {
  const [kind, setKind] = useState<string>('fact')
  const [title, setTitle] = useState(firstLine(e.text, 80))
  const [body, setBody] = useState(e.text)
  const s = useSend(onDone)
  return (
    <FormBox title="Remember this" note="It becomes a memory entry of the project: the assistant wrote it, you confirmed it. Agents read it from their next run.">
      <div className="grid grid-cols-[4rem_1fr] items-start gap-2">
        <label className="text-text-dim">Kind</label>
        <select aria-label="Kind" className={input} value={kind} onChange={(x) => setKind(x.target.value)}>
          {memoryKinds.map((k) => (
            <option key={k}>{k}</option>
          ))}
        </select>
        <label className="text-text-dim">Title</label>
        <input aria-label="Title" className={input} value={title} onChange={(x) => setTitle(x.target.value)} />
        <label className="text-text-dim">Entry</label>
        <textarea aria-label="Entry" rows={4} className={input} value={body} onChange={(x) => setBody(x.target.value)} />
      </div>
      <Failed error={s.error} />
      <Buttons
        label="Remember this"
        busy={s.busy || !title.trim() || !body.trim()}
        onCancel={onCancel}
        ok={() => s.go(`/api/conversations/${v.id}/remember`, { turn: e.turn, kind, title, body, appliesTo: [], paths: [], keywords: [] })}
      />
    </FormBox>
  )
}
