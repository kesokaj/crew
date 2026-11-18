import { createFileRoute, Link, useNavigate } from '@tanstack/react-router'
import { useState } from 'react'
import { send, type ConvView, type Frame } from '../lib/api'
import type { CLIModels } from '../lib/models'
import { usePoll } from '../lib/poll'
import { valueAt, type Doc } from '../lib/settings'
import { tokenCount } from '../lib/tokens'
import { modeName } from '../lib/words'
import { Entries, TicketForm } from '../components/chat'
import { CrewMapCard } from '../components/crew-map-card'
import { Trajectory } from '../components/trajectory'
import { button, Card, Failed, mainButton, PageHead, Rows, when } from '../components/ui'
import { WriteBox, WriteScope } from '../components/writebox'

export const Route = createFileRoute('/conversations/$id')({ component: ConversationPage })

// One conversation (design 11.8, 12.10, 20.1, conv.png): the messages with what the agent did in each
// turn, your next message, the sandbox, how the conversation is kept and continued, what came of it,
// and its trajectory. Where the picture shows claude, --resume or 2 GB, the design wins: agy is the
// only CLI, it continues with --conversation, and the sandbox is Large (plan M20 D16).
function ConversationPage() {
  const { id } = Route.useParams()
  const { data: v, error, reload } = usePoll<ConvView>(`/api/conversations/${id}`, 3000)
  const { data: doc } = usePoll<Doc>('/api/settings/machine', 0)
  const { data: frame } = usePoll<Frame>('/api/frame', 0)
  const navigate = useNavigate()
  const [failed, setFailed] = useState('')
  const [making, setMaking] = useState(false)
  if (!v) return <div className="p-6 text-text-dim">{error || 'Loading…'}</div>
  const lastAnswer = v.entries.findLast((e) => e.kind === 'agent')
  const projects = frame?.projects ?? []
  const act = (path: string, then: (r: unknown) => void, method: 'POST' | 'DELETE' = 'POST', body: unknown = {}) =>
    send(method, path, method === 'POST' ? body : undefined).then(
      (r) => {
        setFailed('')
        then(r)
      },
      (e: Error) => setFailed(e.message),
    )
  const about = v.projectName
    ? `About ${v.projectName}. The agent sees that project and nothing else.`
    : 'About no project. Normal conversation with read-only access to Crew and the map.'
  return (
    <WriteScope.Provider value={{ project: v.projectName }}>
      <div className="mx-auto max-w-7xl p-6">
        <PageHead
          crumbs={
            <>
              <Link to="/conversations" className="text-accent">
                Conversations
              </Link>{' '}
              / {id.slice(0, 8)}
            </>
          }
          title={<span data-free-text>{v.title || 'A conversation'}</span>}
          actions={
            <>
              {v.projectName && lastAnswer && (
                <button className={mainButton} onClick={() => setMaking(!making)}>
                  Make a ticket of this
                </button>
              )}
              {!v.projectName && lastAnswer && projects.length > 0 && (
                <button className={button} onClick={() => setMaking(!making)}>
                  Move to project backlog
                </button>
              )}
              {!v.projectName && lastAnswer && !v.cameOf.draft && (
                <button
                  className={mainButton}
                  onClick={() => void act(`/api/conversations/${id}/plan`, (r) => void navigate({ to: '/plans/$draft', params: { draft: (r as { draft: string }).draft } }))}
                >
                  Start a plan from this
                </button>
              )}
              <button className={button} onClick={() => void act(`/api/conversations/${id}/save`, reload, 'POST', { saved: !v.savedAt })}>
                {v.savedAt ? 'Saved' : 'Save'}
              </button>
              {!v.closedAt && (
                <button className={button} onClick={() => void act(`/api/conversations/${id}/close`, reload)}>
                  Close
                </button>
              )}
              <button
                className={button}
                onClick={() => {
                  if (window.confirm('Delete this conversation, its turns, its trajectory and its work folder?'))
                    void act(`/api/conversations/${id}`, () => void navigate({ to: '/conversations' }), 'DELETE')
                }}
              >
                Delete
              </button>
            </>
          }
        >
          {about} {v.turns} {v.turns === 1 ? 'turn' : 'turns'}, saved in Crew's database.
        </PageHead>
        <Failed error={failed} />
        {making && lastAnswer && (
          <div className="mb-4 rounded-lg border border-border bg-bg-raised p-3 text-sm">
            <TicketForm
              v={v}
              e={lastAnswer}
              prev={v.entries.findLast((e) => e.kind === 'you')}
              projects={projects}
              onDone={() => {
                setMaking(false)
                reload()
              }}
              onCancel={() => setMaking(false)}
            />
          </div>
        )}
        <div className="grid gap-4 lg:grid-cols-[minmax(0,1fr)_380px]">
          <div className="min-w-0 space-y-4">
            <section className="rounded-lg border border-border bg-bg-raised p-4">
              <Entries v={v} asked={{ project: v.projectName, projects }} onDone={reload} />
            </section>
            <Reply v={v} onSent={reload} />
            <Trajectories v={v} />
          </div>
          <aside className="space-y-4">
            {!v.projectName && <CrewMapCard />}
            <SandboxCard v={v} doc={doc} />
            {v.workFolder && v.workFolder.files > 0 && <WorkFolderCard v={v} projects={projects} />}
            <ThisConversation v={v} onPicked={reload} />
            <CameOf v={v} />
          </aside>
        </div>
      </div>
    </WriteScope.Provider>
  )
}

function Reply({ v, onSent }: { v: ConvView; onSent: () => void }) {
  const [text, setText] = useState('')
  const [error, setError] = useState('')
  const sendIt = () =>
    send('POST', `/api/conversations/${v.id}/messages`, { text }).then(
      () => {
        setText('')
        setError('')
        onSent()
      },
      (e: Error) => setError(e.message),
    )
  const next =
    v.sandbox.state === 'running' || v.sandbox.state === 'idle'
      ? 'The sandbox is open: your message is a new call of the CLI in it.'
      : v.closedAt
        ? 'It is closed: your message opens it again in a new sandbox, where it stopped.'
        : 'A new sandbox starts, and the conversation continues where it stopped.'
  return (
    <section className="rounded-lg border border-border bg-bg-raised p-4" aria-label="Your message">
      <h2 className="mb-2 text-xs tracking-wide text-text-dim uppercase">Your message</h2>
      <WriteBox aria-label="Your message" rows={3} value={text} onChange={setText} placeholder="Ask, or ask it to try something" />
      <Failed error={error} />
      <div className="mt-2 flex flex-wrap items-center gap-3">
        <button className="rounded-md bg-text px-4 py-2 font-semibold text-bg disabled:opacity-50" disabled={!text.trim()} onClick={() => void sendIt()}>
          Send
        </button>
        <span className="text-sm text-text-dim">{next}</span>
      </div>
    </section>
  )
}

const stateWords: Record<string, string> = {
  none: 'not started: it starts with the first turn',
  running: 'running: a turn works',
  idle: 'open, waiting for your next message',
  saved: 'saved, sandbox removed',
  closed: 'closed, sandbox removed',
}

function sizeText(doc: Doc | null, size?: string): string {
  if (!size) return ''
  const s = valueAt(doc, `sandbox_sizes.${size}`) as { cpus?: number; memory_gb?: number } | undefined
  return s ? `${size}: ${1 + (s.cpus ?? 0)} CPU, ${2 + (s.memory_gb ?? 0)} GB` : size
}

function bytes(n: number): string {
  if (n < 1024) return `${n} bytes`
  if (n < 1024 * 1024) return `${(n / 1024).toFixed(1)} KB`
  return `${(n / 1024 / 1024).toFixed(1)} MB`
}

function SandboxCard({ v, doc }: { v: ConvView; doc: Doc | null }) {
  const idle = valueAt(doc, 'conversations.idle_minutes') as number | undefined
  const open = v.sandbox.state === 'running' || v.sandbox.state === 'idle'
  const net = v.sandbox.network || v.network
  const wf = v.workFolder
  return (
    <Card title="Sandbox">
      <Rows
        narrow
        rows={[
          ['State', <span data-sandbox-state={v.sandbox.state}>{stateWords[v.sandbox.state] ?? v.sandbox.state}</span>],
          ['Id', <span className="font-mono text-xs">{open && v.sandbox.id ? v.sandbox.id : 'none now'}</span>],
          ['Size', open ? sizeText(doc, v.sandbox.size) : sizeText(doc, 'large')],
          [
            'Holds',
            v.projectName
              ? `a read-only copy of main of ${v.projectName}, and the conversation's own work folder, /home/agent/conversation`
              : "only the conversation's own work folder, /home/agent/conversation",
          ],
          ...(net ? ([['Network', modeName[net] ?? net]] as [string, string][]) : []),
          ['Lives', `from your message until ${idle ?? 30} minutes without one, or until you close it. Its work folder is saved in the database.`],
          [
            'Work folder',
            wf ? (
              <span data-work-folder>
                {wf.files} {wf.files === 1 ? 'file' : 'files'}, {bytes(wf.bytes)}, saved {when(wf.savedAt)}
                {wf.note && <span className="block text-amber-text">{wf.note}</span>}
              </span>
            ) : (
              'nothing saved yet'
            ),
          ],
        ]}
      />
    </Card>
  )
}

interface ConvFolderFile {
  path: string
  size: number
  previewable: boolean
}

function WorkFolderCard({ v, projects }: { v: ConvView; projects: { name: string }[] }) {
  const { data } = usePoll<{ files: ConvFolderFile[] }>(`/api/conversations/${v.id}/files`, 5000)
  const [previewPath, setPreviewPath] = useState('')
  const [targetProject, setTargetProject] = useState(v.projectName || projects[0]?.name || '')
  const [pushed, setPushed] = useState<{ project: string; branch: string; commit: string } | null>(null)
  const [error, setError] = useState('')
  const files = data?.files ?? []
  const pushBranch = () =>
    send<{ project: string; branch: string; commit: string }>('POST', `/api/conversations/${v.id}/branch`, { project: v.projectName || targetProject }).then(
      (r) => {
        setError('')
        setPushed(r)
      },
      (e: Error) => setError(e.message),
    )
  return (
    <Card title="Work folder & preview">
      {files.length === 0 ? (
        <p className="text-xs text-text-dim">Loading files…</p>
      ) : (
        <ul className="max-h-56 space-y-1 overflow-y-auto text-xs">
          {files.map((f) => (
            <li key={f.path} className="flex items-center justify-between gap-2 rounded px-1.5 py-1 hover:bg-bg-sunk/60">
              <span className="truncate font-mono" title={f.path}>
                {f.path} <span className="text-text-dim">({bytes(f.size)})</span>
              </span>
              {f.previewable && (
                <button type="button" className={button} onClick={() => setPreviewPath(f.path)}>
                  Preview
                </button>
              )}
            </li>
          ))}
        </ul>
      )}
      {(v.projectName || projects.length > 0) && (
        <div className="mt-3 space-y-2 border-t border-border pt-2 text-xs">
          <div className="flex flex-wrap items-center gap-2">
            {!v.projectName && projects.length > 1 && (
              <select className={button} value={targetProject} onChange={(e) => setTargetProject(e.target.value)} aria-label="Target project for branch">
                {projects.map((p) => (
                  <option key={p.name} value={p.name}>
                    {p.name}
                  </option>
                ))}
              </select>
            )}
            <button type="button" className={button} onClick={() => void pushBranch()}>
              Push to branch conv/{(v.id ?? '').slice(0, 8)}
            </button>
          </div>
          {pushed && (
            <p className="text-green-text">
              Pushed to{' '}
              <Link to="/p/$project/git" params={{ project: pushed.project }} className="font-mono underline">
                {pushed.project}:{pushed.branch}
              </Link>{' '}
              (<span className="font-mono">{pushed.commit.slice(0, 7)}</span>)
            </p>
          )}
          <Failed error={error} />
        </div>
      )}
      {previewPath && (
        <div
          className="fixed inset-0 z-50 flex items-center justify-center bg-black/60 p-4"
          role="dialog"
          aria-modal="true"
          aria-label={`Preview ${previewPath}`}
          onClick={(e) => {
            if (e.target === e.currentTarget) setPreviewPath('')
          }}
        >
          <div className="flex h-[85vh] w-[92vw] max-w-6xl flex-col overflow-hidden rounded-xl border border-border bg-bg shadow-2xl">
            <div className="flex items-center justify-between border-b border-border bg-bg-raised px-4 py-2.5 text-sm">
              <span className="font-mono font-semibold">{previewPath}</span>
              <button type="button" className={button} onClick={() => setPreviewPath('')}>
                Close
              </button>
            </div>
            <iframe
              sandbox=""
              title={`Preview ${previewPath}`}
              src={`/api/conversations/${v.id}/preview/${previewPath}`}
              className="min-h-0 flex-1 w-full bg-white"
            />
          </div>
        </div>
      )}
    </Card>
  )
}

// ThisConversation is who answers and how the conversation is kept and continued (12.10), with the
// picker of the agent for the next turns (12.3).
function ThisConversation({ v, onPicked }: { v: ConvView; onPicked: () => void }) {
  const agent = [v.agent.cli, v.agent.model, v.agent.effort].filter(Boolean).join(' · ')
  return (
    <Card title="This conversation">
      <Rows
        narrow
        rows={[
          [
            'Agent',
            <span>
              {agent ? <span className="rounded bg-bg-sunk px-1.5 font-mono text-xs">{agent}</span> : 'not yet'}{' '}
              <span className="text-text-dim">{v.agent.pickedByYou ? 'picked by you' : "the assistant's model"}</span>
            </span>,
          ],
          ['Id at the CLI', <span className="font-mono text-xs break-all">{v.cliId || 'none yet'}</span>],
          ['Turns', String(v.turns)],
          ['Tokens', tokenCount(v.tokens)],
          ['Saved', 'every turn in Crew\'s database, for good. Every event in the trajectory, for good.'],
          ['Continued with', <span className="font-mono text-xs break-all">{v.cliId ? `agy -p --conversation ${v.cliId}` : 'its first turn starts it'}</span>],
        ]}
      />
      <Picker v={v} onPicked={onPicked} />
    </Card>
  )
}

function Picker({ v, onPicked }: { v: ConvView; onPicked: () => void }) {
  const { data } = usePoll<{ clis: CLIModels[] }>('/api/crew/models', 0)
  const [error, setError] = useState('')
  const offered: { entry: string; label: string; efforts: string[] }[] = []
  for (const c of (data?.clis ?? []).filter((c) => c.signedIn && c.inUse)) {
    for (const m of c.models.filter((m) => m.offered)) {
      const entry = `${c.cli}/${m.model}`
      let item = offered.find((x) => x.entry === entry)
      if (!item) {
        item = { entry, label: `${c.cli} · ${m.display ?? m.model}`, efforts: [] }
        offered.push(item)
      }
      for (const ef of m.efforts) {
        if (!item.efforts.includes(ef)) item.efforts.push(ef)
      }
    }
  }
  const current = v.agent.pickedByYou && v.agent.cli && v.agent.model ? `${v.agent.cli}/${v.agent.model}` : ''
  const efforts = offered.find((m) => m.entry === current)?.efforts ?? []
  const pick = (model: string, effort: string) =>
    send('POST', `/api/conversations/${v.id}/pick`, { model, effort }).then(
      () => {
        setError('')
        onPicked()
      },
      (e: Error) => setError(e.message),
    )
  return (
    <div className="mt-3 grid grid-cols-[6.5rem_1fr] items-center gap-2 text-sm">
      <label htmlFor="conv-model" className="text-text-dim">
        Next turns on
      </label>
      <select id="conv-model" className={button} value={current} onChange={(e) => void pick(e.target.value, '')}>
        <option value="">The assistant's model</option>
        {offered.map((m) => (
          <option key={m.entry} value={m.entry}>
            {m.label}
          </option>
        ))}
      </select>
      {current && efforts.length > 0 && (
        <>
          <label htmlFor="conv-effort" className="text-text-dim">
            Effort
          </label>
          <select id="conv-effort" className={button} value={v.agent.effort ?? ''} onChange={(e) => void pick(current, e.target.value)}>
            <option value="">Its own</option>
            {efforts.map((x) => (
              <option key={x}>{x}</option>
            ))}
          </select>
        </>
      )}
      <Failed error={error} />
    </div>
  )
}

function CameOf({ v }: { v: ConvView }) {
  const none = v.cameOf.tickets.length === 0 && !v.cameOf.draft
  return (
    <Card title="What came of it">
      {none && (
        <p className="text-sm">
          {v.projectName ? 'No ticket yet.' : 'No ticket or plan yet.'} Nothing in a conversation changes main. Work that should land becomes a ticket.
        </p>
      )}
      <ul className="space-y-1 text-sm">
        {v.cameOf.tickets.map((t) => {
          const proj = v.projectName ?? t.projectName
          return (
            <li key={t.key} data-came-of={t.key}>
              {proj ? (
                <Link to="/p/$project/tickets/$key" params={{ project: proj, key: t.key }} className="font-mono text-accent">
                  {t.key}
                </Link>
              ) : (
                <span className="font-mono">{t.key}</span>
              )}{' '}
              <span data-free-text>{t.title}</span>{' '}
              <span className="text-text-dim">
                · {t.adopted ? (t.projectName && !v.projectName ? `moved to ${t.projectName} backlog` : 'your ticket') : 'proposed by the agent, not yours yet'}
              </span>
            </li>
          )
        })}
        {v.cameOf.draft && (
          <li>
            <Link to="/plans/$draft" params={{ draft: v.cameOf.draft }} className="text-accent">
              A plan draft
            </Link>{' '}
            <span className="text-text-dim">started from this conversation</span>
          </li>
        )}
      </ul>
    </Card>
  )
}

// Trajectories is the trajectory of a turn's run, the newest by default: every event, kept for good.
function Trajectories({ v }: { v: ConvView }) {
  const runs = v.entries.filter((e) => e.kind === 'agent' && e.run).map((e) => e.run!).filter((r, i, all) => all.indexOf(r) === i)
  const [chosen, setChosen] = useState('')
  const run = chosen || runs.at(-1)
  if (!run) return null
  return (
    <section aria-label="Trajectory">
      {runs.length > 1 && (
        <label className="mt-6 flex items-center gap-2 text-sm">
          The trajectory of
          <select className={button} value={run} onChange={(e) => setChosen(e.target.value)}>
            {runs.map((r, i) => (
              <option key={r} value={r}>
                sandbox {i + 1} of {runs.length}
              </option>
            ))}
          </select>
        </label>
      )}
      <Trajectory id={run} live={v.sandbox.state === 'running' && run === runs.at(-1)} />
    </section>
  )
}
