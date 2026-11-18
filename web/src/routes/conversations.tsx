import { createFileRoute, Link, Outlet, useNavigate, useRouterState } from '@tanstack/react-router'
import { useState } from 'react'
import { send, type ConvSummary, type ConvView, type Frame } from '../lib/api'
import { usePoll } from '../lib/poll'
import { Card, Failed, mainButton, PageHead, when } from '../components/ui'
import { WriteBox, WriteScope } from '../components/writebox'

export const Route = createFileRoute('/conversations')({ component: Conversations })

// Conversations (design 11.8, 20.1, convs.png): talk to an agent without a ticket, about one project
// or none. The picture's "the lead's CLI and model" is the assistant's model here (12.3), and its
// "Create a ticket in its project" is a proposal the operator makes theirs (plan M20 D3).
function Conversations() {
  const path = useRouterState({ select: (s) => s.location.pathname })
  if (path !== '/conversations' && path !== '/conversations/') return <Outlet />
  return <ConversationList />
}

const sandboxWords: Record<string, string> = {
  running: 'running',
  idle: 'open, waiting for you',
  saved: 'saved',
  closed: 'closed',
}

type Filter = 'all' | 'saved' | 'backlog' | 'plan'

function ConversationList() {
  const [q, setQ] = useState('')
  const [filter, setFilter] = useState<Filter>('all')
  const { data, error } = usePoll<{ conversations: ConvSummary[] }>(`/api/conversations${q.trim() ? `?q=${encodeURIComponent(q.trim())}` : ''}`, 10000)
  const list = (data?.conversations ?? []).filter((c) => {
    if (filter === 'saved') return !!c.savedAt
    if (filter === 'backlog') return (c.tickets ?? 0) > 0
    if (filter === 'plan') return !!c.draft
    return true
  })
  return (
    <div className="mx-auto max-w-7xl p-6">
      <PageHead title="Conversations">
        Talk to an agent without a ticket. A normal conversation outside a project reads everything running in Crew and the map, and can be moved into a project backlog, converted to a plan for a new project, saved, or discarded.
      </PageHead>
      <Failed error={error} />
      <div className="grid gap-4 lg:grid-cols-[minmax(0,1fr)_380px]">
        <div className="min-w-0 space-y-4">
          <StartOne />
          <div className="flex flex-wrap items-center gap-2">
            {(
              [
                ['all', 'All'],
                ['saved', 'Saved'],
                ['backlog', 'Moved to backlog'],
                ['plan', 'Converted to plan'],
              ] as const
            ).map(([k, label]) => (
              <button
                key={k}
                type="button"
                onClick={() => setFilter(k)}
                className={`rounded-md border px-2.5 py-1 text-xs ${filter === k ? 'border-amber bg-amber-soft font-semibold' : 'border-border bg-bg-raised text-text-dim'}`}
              >
                {label}
              </button>
            ))}
          </div>
          <input
            aria-label="Search the conversations"
            className="w-full rounded-md border border-border bg-bg-raised px-3 py-1.5 text-sm"
            placeholder="Search the conversations"
            value={q}
            onChange={(e) => setQ(e.target.value)}
          />
          <div className="overflow-x-auto">
            <table className="w-full text-sm" aria-label="Conversations">
              <thead className="text-left text-xs tracking-wide text-text-dim uppercase">
                <tr>
                  <th className="p-2">Conversation</th>
                  <th className="p-2">About</th>
                  <th className="p-2">Agent</th>
                  <th className="p-2">Turns</th>
                  <th className="p-2">Last</th>
                  <th className="p-2">State</th>
                </tr>
              </thead>
              <tbody>
                {list.map((c) => (
                  <tr key={c.id} className="border-t border-border align-top" data-conversation={c.id}>
                    <td className="max-w-[22rem] p-2">
                      <Link to="/conversations/$id" params={{ id: c.id }} className="font-semibold hover:underline" data-free-text>
                        {c.title || 'A conversation'}
                      </Link>
                      {c.savedAt && <span className="ml-2 rounded bg-amber-soft px-1.5 py-0.5 text-xs">kept</span>}
                      {c.last && (
                        <p className="mt-0.5 line-clamp-3 text-text-dim" data-free-text>
                          Agent: {c.last}
                        </p>
                      )}
                    </td>
                    <td className="p-2 font-mono text-xs">{c.project ?? 'no project'}</td>
                    <td className="p-2">
                      {c.cli ? (
                        <span className="rounded bg-bg-sunk px-1.5 font-mono text-xs">
                          <b>{c.cli}</b> · {c.model}
                        </span>
                      ) : (
                        <span className="text-text-dim">not yet</span>
                      )}
                    </td>
                    <td className="p-2">{c.turns}</td>
                    <td className="p-2 whitespace-nowrap">{when(c.lastAt)}</td>
                    <td className="p-2 text-text-dim">{sandboxWords[c.sandbox] ?? c.sandbox}</td>
                  </tr>
                ))}
              </tbody>
            </table>
            {data && list.length === 0 && <p className="p-2 text-sm text-text-dim">{q.trim() || filter !== 'all' ? 'None matches.' : 'No conversation yet.'}</p>}
          </div>
        </div>
        <aside className="space-y-4">
          <CanDo />
          <Kept />
        </aside>
      </div>
    </div>
  )
}

// StartOne starts a conversation with its first message: by default a normal conversation outside a
// project (with read-only Crew and map access), or scoped to one project.
function StartOne() {
  const { data: f } = usePoll<Frame>('/api/frame', 0)
  const navigate = useNavigate()
  const [about, setAbout] = useState('')
  const [network, setNetwork] = useState<'internet' | 'model_only'>('internet')
  const [text, setText] = useState('')
  const [busy, setBusy] = useState(false)
  const [failed, setFailed] = useState('')
  const start = () => {
    setBusy(true)
    setFailed('')
    send<ConvView>('POST', '/api/conversations', { project: about, network: about ? undefined : network, text }).then(
      (v) => {
        setBusy(false)
        if (v?.id) void navigate({ to: '/conversations/$id', params: { id: v.id } })
      },
      (e: Error) => {
        setBusy(false)
        setFailed(e.message)
      },
    )
  }
  return (
    <section className="rounded-lg border border-amber bg-bg-raised p-4" aria-label="Start a conversation">
      <h2 className="mb-3 font-semibold">Start a conversation</h2>
      <div className="mb-3 flex flex-wrap items-center gap-4 text-sm">
        <label className="flex items-center gap-2">
          About
          <select className="rounded-md border border-border bg-bg px-2 py-1" value={about} onChange={(e) => setAbout(e.target.value)}>
            <option value="">No project (Normal conversation · Crew &amp; map)</option>
            {f?.projects.map((p) => (
              <option key={p.name} value={p.name}>
                {p.name}
              </option>
            ))}
          </select>
        </label>
        {!about && (
          <label className="flex items-center gap-2">
            Network
            <select aria-label="Network" className="rounded-md border border-border bg-bg px-2 py-1" value={network} onChange={(e) => setNetwork(e.target.value as 'internet' | 'model_only')}>
              <option value="internet">Internet</option>
              <option value="model_only">Model only</option>
            </select>
          </label>
        )}
      </div>
      <WriteScope.Provider value={{ project: about || undefined }}>
        <WriteBox aria-label="Your first message" rows={3} value={text} onChange={setText} placeholder="Ask something, inspect Crew and the map, or ask it to try something" />
      </WriteScope.Provider>
      <Failed error={failed} />
      <div className="mt-2 flex flex-wrap items-center gap-3">
        <button className={mainButton} disabled={busy || !text.trim()} onClick={start}>
          Start
        </button>
        <span className="text-sm text-text-dim">The agent runs on the assistant's model. The sandbox starts with your message.</span>
      </div>
    </section>
  )
}

function CanDo() {
  const yes = (t: string) => (
    <li className="flex gap-2">
      <span className="text-ok">✓</span>
      {t}
    </li>
  )
  const no = (t: string) => (
    <li className="flex gap-2">
      <span className="text-bad">×</span>
      {t}
    </li>
  )
  return (
    <Card title="What a conversation can do">
      <ul className="space-y-1.5 text-sm">
        {yes('Read the live Crew map, every project, its plan, tickets, runs, checks, and memory in a normal conversation outside a project')}
        {yes("Answer from one project's code, plan, tickets, and memory, when it is scoped to one project")}
        {yes('Try things in its own sandbox: clone any project read-only, run, test, write files')}
        {yes('Move a turn or the conversation into a project backlog, or start a plan for a new project')}
        {no('Change main or the board, move a ticket, deploy, or change settings')}
      </ul>
    </Card>
  )
}

function Kept() {
  return (
    <Card title="How a conversation is kept">
      <dl className="grid grid-cols-[8rem_1fr] gap-x-3 gap-y-1.5 text-sm">
        <dt className="text-text-dim">All in the database</dt>
        <dd>nothing is kept in a sandbox</dd>
        <dt className="text-text-dim">The turns</dt>
        <dd>for good, until you delete the conversation</dd>
        <dt className="text-text-dim">The trajectory</dt>
        <dd>every event, for good</dd>
        <dt className="text-text-dim">The CLI's own files</dt>
        <dd>saved after every turn, so it can be continued</dd>
        <dt className="text-text-dim">The work folder</dt>
        <dd>saved with the conversation, up to 200 MB of files</dd>
      </dl>
      <h3 className="mt-4 mb-2 text-xs tracking-wide text-text-dim uppercase">How it is continued</h3>
      <ol className="list-decimal space-y-1.5 pl-5 text-sm">
        <li>Sandbox still running: your message is a new call of the CLI in the same sandbox.</li>
        <li>
          Sandbox removed: a new one. Crew puts the CLI's files and the work folder back from the database, then <code>agy -p --conversation</code>.
        </li>
        <li>No file any more, or a newer CLI cannot read it: rebuilt from the turns.</li>
      </ol>
    </Card>
  )
}
