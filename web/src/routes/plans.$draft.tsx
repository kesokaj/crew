import { createFileRoute, Link, useNavigate } from '@tanstack/react-router'
import { useCallback, useRef, useState } from 'react'
import { send, type Draft, type DraftMessage, type Rewrite } from '../lib/api'
import { usePoll } from '../lib/poll'
import { button, Failed, mainButton, PageHead, when } from '../components/ui'
import { WriteBox, WriteScope } from '../components/writebox'
import { FirstProposal, Marked } from '../components/proposal'

export const Route = createFileRoute('/plans/$draft')({ component: DraftPage })

// A plan draft (design 7.2, 20.1, plans.png, rewrite.png): the conversation with the planning agent on
// one side and the plan on the other. Before the first message is sent, Crew's rewrite of it is shown
// beside it. Selecting text in your message, or in the plan, shows Improve text and Review text. A
// project is created from the draft.
function DraftPage() {
  const { draft } = Route.useParams()
  const { data: d, error, reload } = usePoll<Draft>(`/api/drafts/${draft}`, 3000)
  const [proposal, setProposal] = useState<number | null>(null)
  const chosen = useCallback(() => {
    setProposal(null)
    reload()
  }, [reload])
  if (!d) return <div className="p-6 text-text-dim">{error || 'Loading…'}</div>
  const first = proposal ?? d.firstRewrite
  return (
    <WriteScope.Provider value={{ draft: d.id }}>
      <div className="mx-auto max-w-7xl p-6">
        <PageHead
          crumbs={
            <>
              <Link to="/plans" className="text-accent">
                Plans
              </Link>{' '}
              / {d.title || 'a draft'}
            </>
          }
          title="Plans"
        >
          A plan is developed with a planning agent before any project or repository exists. A project is created from it.
        </PageHead>
        <Failed error={error} />
        <div className="grid gap-4 lg:grid-cols-[minmax(0,1fr)_380px]">
          <div className="min-w-0 space-y-3">
            <h2 className="text-xl font-semibold">Conversation</h2>
            {d.messages.length === 0 && first ? (
              <FirstProposal id={first} onChosen={chosen} onAgain={setProposal} />
            ) : (
              <Conversation d={d} />
            )}
            {d.state === 'open' && d.messages.length > 0 && <Reply draft={d.id} onSent={reload} />}
            {d.state === 'project' && d.project && (
              <p className="text-sm text-text-dim">This draft is a project now: its plan is the project's original plan.</p>
            )}
          </div>
          <aside className="space-y-4">
            <PlanPanel d={d} onAsked={reload} />
            {d.state === 'open' && d.version > 0 && <CreateProject d={d} />}
          </aside>
        </div>
      </div>
    </WriteScope.Provider>
  )
}

// The conversation: your messages, with the rewrite each was chosen from, and the planning agent's
// answers with what ran them.
function Conversation({ d }: { d: Draft }) {
  type Item = { at: string; mine?: DraftMessage; agent?: Draft['turns'][number] }
  const items: Item[] = [
    ...d.messages.map((m) => ({ at: m.at, mine: m })),
    ...d.turns.filter((t) => t.speaker === 'agent').map((t) => ({ at: t.at, agent: t })),
  ].sort((a, b) => a.at.localeCompare(b.at))
  return (
    <ol className="space-y-3">
      {items.map((it, i) =>
        it.mine ? (
          <li key={'m' + it.mine.id} className="ml-8 rounded-lg border border-amber bg-amber-soft p-3 text-sm" data-message={it.mine.id}>
            <p className="mb-1 text-xs tracking-wide text-text-dim uppercase">
              You · {when(it.mine.at)}
              {!it.mine.taken && ' · waiting for the planning agent'}
            </p>
            {it.mine.quote && (
              <>
                <p className="font-semibold">{it.mine.ask === 'review' ? 'Review this part of the plan' : 'Improve this part of the plan'}</p>
                <blockquote className="my-1 border-l-2 border-amber pl-2 whitespace-pre-wrap">{it.mine.quote}</blockquote>
              </>
            )}
            {it.mine.text && <p className="whitespace-pre-wrap">{it.mine.text}</p>}
            {it.mine.rewrite && <SentFrom id={it.mine.rewrite} />}
          </li>
        ) : (
          <li key={'t' + i} className="mr-8 rounded-lg border border-border bg-bg-raised p-3 text-sm">
            <p className="mb-1 text-xs tracking-wide text-text-dim uppercase">
              Planning agent · {it.agent!.cli} · {it.agent!.model} {it.agent!.effort ? `· ${it.agent!.effort}` : ''} · {when(it.agent!.at)}
            </p>
            <p className="whitespace-pre-wrap">{it.agent!.text}</p>
          </li>
        ),
      )}
      {d.agent && (d.agent.state === 'working' || d.agent.state === 'starting') && (
        <li className="mr-8 text-sm text-accent">● The planning agent is working…</li>
      )}
    </ol>
  )
}

// SentFrom says which text of a first message was sent, with the original one click away (7.2).
function SentFrom({ id }: { id: number }) {
  const { data: r } = usePoll<Rewrite>(`/api/rewrites/${id}`, 0)
  const [open, setOpen] = useState(false)
  if (!r) return null
  const said =
    r.choice === 'rewrite'
      ? "Crew's rewrite. You chose it."
      : r.choice === 'edited'
        ? "Crew's rewrite, as you edited it."
        : r.state === 'late'
          ? `Your own text, sent as it is: ${r.detail ?? 'the prompt writer was late'}.`
          : r.state === 'failed'
            ? `Your own text, sent as it is: ${r.detail ?? 'the prompt writer could not answer'}.`
            : r.state === 'good'
              ? 'Your own text: the prompt writer found it good as it is.'
              : r.state === 'off'
                ? 'Your own text: the prompt writer is off.'
                : 'Your own text. You chose it.'
  return (
    <div className="mt-2 border-t border-dashed border-amber pt-2 text-xs" data-sent-from={r.choice}>
      <p>✓ {said}</p>
      {r.rewrite && (
        <button className="mt-1 text-accent underline" onClick={() => setOpen(!open)}>
          {r.choice === 'mine' ? "Crew's rewrite, and what it changed" : 'Your original, and what changed'}
        </button>
      )}
      {open && r.rewrite && (
        <div className="mt-2 grid gap-2 md:grid-cols-2">
          <p className="whitespace-pre-wrap">{r.original}</p>
          <p className="whitespace-pre-wrap">
            <Marked original={r.original} rewrite={r.rewrite} assumptions={r.assumptions} />
          </p>
        </div>
      )}
    </div>
  )
}

function Reply({ draft, onSent }: { draft: string; onSent: () => void }) {
  const [text, setText] = useState('')
  const [error, setError] = useState('')
  const sendIt = () =>
    send('POST', `/api/drafts/${draft}/messages`, { text }).then(
      () => {
        setText('')
        setError('')
        onSent()
      },
      (e: Error) => setError(e.message),
    )
  return (
    <div className="pb-10">
      <div className="flex gap-2">
        <div className="flex-1">
          <WriteBox aria-label="Your message" rows={3} value={text} onChange={setText} placeholder="Tell the planning agent what to change" />
        </div>
        <button className="self-start rounded-md bg-text px-4 py-2 font-semibold text-bg disabled:opacity-50" disabled={!text.trim()} onClick={() => void sendIt()}>
          Send
        </button>
      </div>
      <Failed error={error} />
      <p className="mt-6 text-sm text-text-dim">
        Select text with the mouse, in your message or in the plan on the right, and choose Improve text or Review text. On your own writing
        the prompt writer answers, and nothing is sent until you send it. On the plan, the request goes to the planning agent with the part you
        marked.
      </p>
    </div>
  )
}

// PlanPanel is the plan of the draft, as the planning agent wrote it, shown as text. Selecting a part
// of it offers Improve text and Review text, which go to the planning agent with the part quoted.
function PlanPanel({ d, onAsked }: { d: Draft; onAsked: () => void }) {
  const box = useRef<HTMLPreElement>(null)
  const [quote, setQuote] = useState('')
  const [ask, setAsk] = useState<'improve' | 'review' | null>(null)
  const [line, setLine] = useState('')
  const [error, setError] = useState('')
  const readSelection = () => {
    const s = window.getSelection()
    if (!s || s.isCollapsed || !box.current || !box.current.contains(s.anchorNode) || !box.current.contains(s.focusNode)) return
    const q = s.toString().trim()
    if (q) {
      setQuote(q)
      setAsk(null)
    }
  }
  const sendAsk = () =>
    send('POST', `/api/drafts/${d.id}/messages`, { quote, ask, text: line }).then(
      () => {
        setQuote('')
        setAsk(null)
        setLine('')
        onAsked()
      },
      (e: Error) => setError(e.message),
    )
  return (
    <section className="rounded-lg border border-border bg-bg-raised p-4">
      <p className="mb-2 flex text-xs tracking-wide text-text-dim uppercase">
        Plan draft, revision {d.version}
        <span className="ml-auto font-mono normal-case">drafts/{d.id.slice(0, 8)}.md</span>
      </p>
      {d.version === 0 ? (
        <p className="text-sm text-text-dim">The planning agent has not written the plan yet.</p>
      ) : (
        <pre ref={box} onMouseUp={readSelection} className="max-h-[70vh] overflow-auto text-sm whitespace-pre-wrap" data-plan>
          {d.plan}
        </pre>
      )}
      {quote && d.state === 'open' && (
        <div className="mt-3 rounded-md border border-amber p-2 text-sm" role="menu" aria-label="On the plan">
          <blockquote className="mb-2 max-h-24 overflow-auto border-l-2 border-amber pl-2 text-xs whitespace-pre-wrap">{quote}</blockquote>
          {ask === null ? (
            <div className="flex gap-1">
              <button role="menuitem" className={button} onClick={() => setAsk('improve')}>
                Improve text
              </button>
              <button role="menuitem" className={button} onClick={() => setAsk('review')}>
                Review text
              </button>
              <button className="ml-auto text-xs text-text-dim" onClick={() => setQuote('')}>
                Cancel
              </button>
            </div>
          ) : (
            <>
              <p className="mb-1">{ask === 'improve' ? 'Improve this part' : 'Review this part'}: a message to the planning agent, with the part quoted.</p>
              <input
                aria-label="A line of your own"
                className="w-full rounded-md border border-border bg-bg px-2 py-1"
                placeholder="A line of your own, optional"
                value={line}
                onChange={(e) => setLine(e.target.value)}
              />
              <div className="mt-2 flex gap-2">
                <button className={mainButton} onClick={() => void sendAsk()}>
                  Send to the planning agent
                </button>
                <button className={button} onClick={() => setAsk(null)}>
                  Back
                </button>
              </div>
            </>
          )}
          <Failed error={error} />
        </div>
      )}
    </section>
  )
}

// CreateProject creates the project from the draft (7.2, step 2): a name, a key for its ticket
// numbers, and how its repository is made.
function CreateProject({ d }: { d: Draft }) {
  const navigate = useNavigate()
  const [name, setName] = useState(d.title.toLowerCase().replace(/[^a-z0-9]+/g, '-').replace(/^-|-$/g, '').slice(0, 40))
  const [key, setKey] = useState('')
  const [kind, setKind] = useState<'github' | 'local' | 'existing'>('github')
  const [url, setUrl] = useState('')
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState('')
  const create = () => {
    setBusy(true)
    setError('')
    send<{ name: string }>('POST', '/api/projects', { name, key, draft: d.id, repository: { kind, url } }).then(
      (p) => void navigate({ to: '/p/$project/board', params: { project: p?.name ?? name } }),
      (e: Error) => {
        setBusy(false)
        setError(e.message)
      },
    )
  }
  const input = 'w-full rounded-md border border-border bg-bg px-2 py-1 font-mono text-sm'
  return (
    <section className="rounded-lg border border-amber bg-bg-raised p-4 text-sm" aria-label="Create the project">
      <h2 className="mb-3 font-semibold">Create the project</h2>
      <div className="grid grid-cols-[6rem_1fr] items-center gap-2">
        <label htmlFor="p-name">Name</label>
        <input id="p-name" className={input} value={name} onChange={(e) => setName(e.target.value)} />
        <label htmlFor="p-key">Ticket key</label>
        <input id="p-key" className={input} value={key} placeholder="NS" onChange={(e) => setKey(e.target.value.toUpperCase())} />
        <span>Repository</span>
        <div className="flex flex-col gap-1">
          {(
            [
              ['github', 'Create on GitHub'],
              ['local', 'Create on this machine'],
              ['existing', 'Use an existing one'],
            ] as const
          ).map(([k, label]) => (
            <label key={k} className="flex items-center gap-2">
              <input type="radio" name="repo" checked={kind === k} onChange={() => setKind(k)} /> {label}
            </label>
          ))}
          {kind === 'existing' && <input aria-label="Its address" className={input} placeholder="https://github.com/owner/name.git" value={url} onChange={(e) => setUrl(e.target.value)} />}
        </div>
      </div>
      <Failed error={error} />
      <button className={mainButton + ' mt-3'} disabled={busy || !name || !key} onClick={create}>
        Create project
      </button>
      <p className="mt-2 text-text-dim">The plan is added to the project as its original plan. An agent then splits it into tickets, which wait in Planning for your OK.</p>
    </section>
  )
}
