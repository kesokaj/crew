import { createFileRoute, Link } from '@tanstack/react-router'
import { useState } from 'react'
import { send, type Frame } from '../lib/api'
import { usePoll } from '../lib/poll'
import { roleName } from '../lib/words'
import { button, Card, Failed, mainButton, PageHead, Rows, Tag, when } from '../components/ui'

export const Route = createFileRoute('/crew-mcp')({ component: CrewMCP })

type Tool = { name: string; group: string; roles: string[]; description: string; calls: number }
type Call = {
  at: string
  project?: string
  run?: string
  subtask?: string
  role: string
  token?: string
  tool: string
  args: Record<string, unknown>
  ok: boolean
  result: string
}
type Created = { key: string; project: string; title: string; what: string; by: string; in?: string; at: string; state: string; parallel?: boolean; on?: string }
type Stats = {
  tickets: number
  waitingForAnalysis: number
  newFeatures: number
  approved: number
  skipped: number
  discarded: number
  subtasks: number
  queued: number
  running: number
  done: number
  decisions: number
  calls: number
}
type Screen = { tools: Tool[]; calls: Call[]; created: Created[]; stats: Stats }
type Token = {
  id: number
  name: string
  projects: string[]
  mayAnswer: boolean
  createdAt: string
  expiresAt: string
  lastUsedAt?: string
  lastAddr?: string
  revokedAt?: string
  calls: number
}

// The Crew MCP screen (design 7.5, 11.5, 11.7, 20.1; mcp.png): the server every run is connected to,
// what agents created through it, the operator's own tools with their personal tokens, every call as
// it happens, and the tools with who may call each.
function CrewMCP() {
  const { data, error } = usePoll<Screen>('/api/crew/mcp', 5000)
  return (
    <div className="mx-auto max-w-6xl p-6">
      <PageHead title="Crew MCP">
        Crew's own MCP server. Every agent run is connected to it. It is how agents ask about their project, write their records, and create
        more tickets and subtasks.
      </PageHead>
      <Failed error={error} />
      {data && (
        <>
          <div className="grid gap-4 md:grid-cols-3">
            <Card title="The server">
              <Rows
                narrow
                rows={[
                  ['Address', <span key="a">/mcp on Crew's server, reached over each sandbox's own bridge</span>],
                  ['Token', 'one per run. It names one project, and for writes one ticket.'],
                  ['agy', <code key="g">~/.gemini/config/mcp_config.json</code>],
                  ['Calls', `${data.stats.calls} so far`],
                  ['No tool can', "give a project its OK, install a release, deploy, or change settings"],
                ]}
              />
            </Card>
            <Card title="Ticket or subtask?">
              <ul className="space-y-2 text-sm">
                <li>
                  Work outside its own ticket: <code>create_ticket</code>. A new ticket in Backlog. Another agent analyses it before it is built.
                </li>
                <li>
                  More work inside its own ticket: <code>create_subtask</code>. Its own run in its own sandbox. Subtasks that do not depend on
                  each other run in parallel, each on its own branch, and Crew joins them.
                </li>
                <li>
                  A small side job for itself: <code>add_helper</code>. A helper in the same sandbox.
                </li>
              </ul>
            </Card>
            <Card title="What agents created">
              <p className="text-2xl font-semibold" data-created-count>
                {data.stats.tickets} {data.stats.tickets === 1 ? 'ticket' : 'tickets'} · {data.stats.subtasks}{' '}
                {data.stats.subtasks === 1 ? 'subtask' : 'subtasks'}
              </p>
              <div className="mt-3">
                <Rows
                  narrow
                  rows={[
                    [
                      'Tickets',
                      `${data.stats.approved} approved · ${data.stats.waitingForAnalysis} waiting for analysis · ${data.stats.newFeatures} new features · ${data.stats.skipped} skipped · ${data.stats.discarded} discarded`,
                    ],
                    ['Subtasks', `${data.stats.queued} queued · ${data.stats.running} running · ${data.stats.done} done`],
                    ['Decisions recorded', String(data.stats.decisions)],
                    ['Limits', 'at most 6 subtasks added to one ticket, 3 running at once'],
                  ]}
                />
              </div>
            </Card>
          </div>
          <YourTools />
          <CreatedList created={data.created} />
          <Calls calls={data.calls} />
          <Tools tools={data.tools} />
        </>
      )}
    </div>
  )
}

// YourTools are the operator's personal tokens (11.7): made here and shown once, listed with their
// last use, switched to answer, renewed, and revoked.
function YourTools() {
  const { data, reload } = usePoll<{ tokens: Token[] }>('/api/crew/mcp/tokens', 10000)
  const { data: frame } = usePoll<Frame>('/api/frame', 30000)
  const [open, setOpen] = useState(false)
  const [name, setName] = useState('')
  const [projects, setProjects] = useState<string[]>([])
  const [answer, setAnswer] = useState(false)
  const [made, setMade] = useState<string | null>(null)
  const [failed, setFailed] = useState('')
  const act = (p: Promise<unknown>) =>
    p.then(
      () => {
        setFailed('')
        reload()
      },
      (e: Error) => setFailed(e.message),
    )
  const create = () =>
    send<{ token: string }>('POST', '/api/crew/mcp/tokens', { name, projects, mayAnswer: answer }).then(
      (r) => {
        setMade(r?.token ?? null)
        setOpen(false)
        setName('')
        setProjects([])
        setAnswer(false)
        setFailed('')
        reload()
      },
      (e: Error) => setFailed(e.message),
    )
  const live = (data?.tokens ?? []).filter((t) => !t.revokedAt)
  return (
    <>
      <h2 className="mt-8 mb-1 text-xl font-semibold">
        Your own tools <span className="text-sm font-normal text-text-dim">the same server, from outside Crew</span>
      </h2>
      <div className="grid gap-4 lg:grid-cols-[minmax(0,1fr)_340px]">
        <Card
          title="Personal tokens"
          right={
            <button className={button} onClick={() => setOpen(!open)}>
              Create a token
            </button>
          }
        >
          <Failed error={failed} />
          {made && (
            <div className="mb-3 rounded-md border border-amber p-3 text-sm" data-made-token>
              <p className="font-semibold">Your token. It is shown once: copy it now.</p>
              <code className="mt-1 block break-all">{made}</code>
              <button className={'mt-2 ' + button} onClick={() => setMade(null)}>
                I copied it
              </button>
            </div>
          )}
          {open && (
            <div className="mb-3 space-y-2 rounded-md border border-border p-3 text-sm" data-new-token>
              <label className="block">
                Name{' '}
                <input
                  aria-label="Token name"
                  className="ml-1 rounded-md border border-border bg-bg px-2 py-1"
                  value={name}
                  placeholder="laptop · agy"
                  onChange={(e) => setName(e.target.value)}
                />
              </label>
              <fieldset>
                <legend className="text-text-dim">The projects it may use</legend>
                {(frame?.projects ?? []).map((p) => (
                  <label key={p.name} className="mr-3 inline-flex items-center gap-1">
                    <input
                      type="checkbox"
                      checked={projects.includes(p.name)}
                      onChange={(e) => setProjects(e.target.checked ? [...projects, p.name] : projects.filter((x) => x !== p.name))}
                    />
                    {p.name}
                  </label>
                ))}
              </fieldset>
              <label className="flex items-center gap-1">
                <input type="checkbox" checked={answer} onChange={(e) => setAnswer(e.target.checked)} /> May answer: approve or discard a new
                feature, and change a decision an agent recorded
              </label>
              <button className={mainButton} disabled={!name.trim() || projects.length === 0} onClick={() => void create()}>
                Make the token
              </button>
            </div>
          )}
          {live.length === 0 && <p className="text-sm text-text-dim">No token yet.</p>}
          {live.length > 0 && (
            <div className="overflow-x-auto">
              <table className="w-full text-sm">
                <thead className="text-left text-xs tracking-wide text-text-dim uppercase">
                  <tr>
                    <th className="py-1 pr-2">Name</th>
                    <th className="pr-2">May use</th>
                    <th className="pr-2">May answer</th>
                    <th className="pr-2">Created</th>
                    <th className="pr-2">Runs out</th>
                    <th className="pr-2">Last used</th>
                    <th className="pr-2">Calls</th>
                    <th />
                  </tr>
                </thead>
                <tbody>
                  {live.map((t) => (
                    <tr key={t.id} className="border-t border-border align-top" data-token={t.name}>
                      <td className="py-1.5 pr-2 font-semibold">{t.name}</td>
                      <td className="pr-2">{t.projects.join(', ')}; read, create tickets, comment</td>
                      <td className="pr-2">
                        <button
                          className={button + (t.mayAnswer ? ' border-accent text-accent' : '')}
                          aria-pressed={t.mayAnswer}
                          onClick={() => void act(send('PATCH', `/api/crew/mcp/tokens/${t.id}`, { mayAnswer: !t.mayAnswer }))}
                        >
                          {t.mayAnswer ? 'On' : 'Off'}
                        </button>
                      </td>
                      <td className="pr-2">{when(t.createdAt)}</td>
                      <td className="pr-2">{when(t.expiresAt)}</td>
                      <td className="pr-2">
                        {t.lastUsedAt ? when(t.lastUsedAt) : 'never'}
                        {t.lastAddr && <span className="block text-xs text-text-dim">from {t.lastAddr}</span>}
                      </td>
                      <td className="pr-2">{t.calls}</td>
                      <td className="space-x-1 whitespace-nowrap">
                        <button className={button} onClick={() => void act(send('POST', `/api/crew/mcp/tokens/${t.id}/renew`))}>
                          Renew
                        </button>
                        <button className={button} onClick={() => void act(send('POST', `/api/crew/mcp/tokens/${t.id}/revoke`))}>
                          Revoke
                        </button>
                      </td>
                    </tr>
                  ))}
                </tbody>
              </table>
            </div>
          )}
          <pre className="mt-3 rounded-md bg-bg-sunk p-2 text-xs">
            server: https://{location.host}/mcp{'\n'}header: Authorization: Bearer {'<token>'}
          </pre>
          <p className="mt-1 text-xs text-text-dim">
            For agy on a laptop that is an entry in <code>~/.gemini/config/mcp_config.json</code>, with <code>serverUrl</code> and{' '}
            <code>headers</code>. Any tool that speaks MCP over HTTP works the same way.
          </p>
        </Card>
        <Card title="What a personal token can do">
          <Rows
            narrow
            rows={[
              ['Can', 'list projects, read the plan, tickets, records, and summaries, create a ticket, comment'],
              ['With may answer', 'also approve or discard a new feature, and change a decision an agent recorded. Off for a new token.'],
              ['A ticket it creates', 'is analysed like an agent’s; with may answer it is yours, approved by adding it'],
              ['Never', 'give a project its OK, install or hold a release, deploy, change settings, sign in, or read secrets. Those stay in the app.'],
              ['Reached', 'through the ingress, with the token: five wrong tokens block an address for an hour, and a token makes at most 60 calls a minute. A run’s token never works here.'],
            ]}
          />
        </Card>
      </div>
    </>
  )
}

function CreatedList({ created }: { created: Created[] }) {
  return (
    <>
      <h2 className="mt-8 mb-1 text-xl font-semibold">
        Created by agents <span className="text-sm font-normal text-text-dim">tickets and subtasks, newest first</span>
      </h2>
      <Card title="">
        {created.length === 0 && <p className="text-sm text-text-dim">Nothing yet.</p>}
        {created.length > 0 && (
          <table className="w-full text-sm" data-created>
            <thead className="text-left text-xs tracking-wide text-text-dim uppercase">
              <tr>
                <th className="py-1 pr-2">#</th>
                <th className="pr-2">What</th>
                <th className="pr-2">Created by</th>
                <th className="pr-2">When</th>
                <th className="pr-2">State</th>
              </tr>
            </thead>
            <tbody>
              {created.map((c) => {
                const role = c.by.split(' · ')[0] ?? c.by
                const ticket = c.key.split('.')[0]!
                return (
                  <tr key={c.key} className="border-t border-border align-top">
                    <td className="py-1.5 pr-2 font-mono text-xs whitespace-nowrap">{c.key}</td>
                    <td className="pr-2">
                      <Link to="/p/$project/tickets/$key" params={{ project: c.project, key: ticket }} className="font-semibold text-accent">
                        {c.title}
                      </Link>
                      <span className="block text-xs text-text-dim">
                        {c.what === 'ticket' ? 'ticket · create_ticket' : `subtask on ${ticket} ${c.on} · create_subtask · in parallel`}
                      </span>
                    </td>
                    <td className="pr-2 whitespace-nowrap">
                      {role.startsWith('token') ? role : `the ${role}`}
                      {c.in ? ` on ${c.in}` : ''}
                    </td>
                    <td className="pr-2 whitespace-nowrap">{when(c.at)}</td>
                    <td className="pr-2 whitespace-nowrap">{stateWord(c.state)}</td>
                  </tr>
                )
              })}
            </tbody>
          </table>
        )}
      </Card>
    </>
  )
}

const stateWords: Record<string, string> = {
  proposed: 'waits for analysis',
  waiting: 'waits for analysis',
  analysing: 'being analysed',
  ready: '✓ approved',
  new_feature_notice: 'new feature: notice',
  waits_for_operator: 'waits for your decision',
  skipped: 'skipped',
  closed: 'discarded',
  queued: 'queued',
  running: '● running',
  idle: '● running',
  done: '✓ done',
  joined: '✓ joined',
}
const stateWord = (s: string) => stateWords[s] ?? s

function Calls({ calls }: { calls: Call[] }) {
  return (
    <>
      <h2 className="mt-8 mb-1 text-xl font-semibold">
        Calls <span className="text-sm font-normal text-text-dim">live, newest first</span>
      </h2>
      <Card title="">
        {calls.length === 0 && <p className="text-sm text-text-dim">No call yet.</p>}
        {calls.length > 0 && (
          <div className="overflow-x-auto">
            <table className="w-full text-sm" data-calls>
              <thead className="text-left text-xs tracking-wide text-text-dim uppercase">
                <tr>
                  <th className="py-1 pr-2">When</th>
                  <th className="pr-2">Project</th>
                  <th className="pr-2">Run</th>
                  <th className="pr-2">Role</th>
                  <th className="pr-2">Call</th>
                </tr>
              </thead>
              <tbody>
                {calls.map((c, i) => (
                  <tr key={i} className="border-t border-border align-top">
                    <td className="py-1 pr-2 whitespace-nowrap">{new Date(c.at).toLocaleTimeString([], { hour: '2-digit', minute: '2-digit' })}</td>
                    <td className="pr-2 whitespace-nowrap">{c.project ?? (c.token ? '' : 'a plan draft')}</td>
                    <td className="pr-2 whitespace-nowrap">
                      {c.run ? (
                        <Link to="/runs/$id" params={{ id: c.run }} className="font-mono text-xs text-accent underline">
                          {c.subtask ?? c.run.slice(0, 8)}
                        </Link>
                      ) : c.token ? (
                        <Tag>token {c.token}</Tag>
                      ) : null}
                    </td>
                    <td className="pr-2 whitespace-nowrap">{c.token ? 'your tool' : roleName(c.role)}</td>
                    <td className="pr-2">
                      <code className={c.ok ? '' : 'text-bad'}>{c.tool}</code> <span className="text-text-dim">{argWords(c.args)}</span>
                      {!c.ok && <span className="block text-xs text-bad">{c.result.slice(0, 160)}</span>}
                    </td>
                  </tr>
                ))}
              </tbody>
            </table>
          </div>
        )}
      </Card>
    </>
  )
}

// argWords is a call's arguments in a few words: the first texts it was given.
function argWords(args: Record<string, unknown>): string {
  const words = Object.values(args ?? {})
    .filter((v) => typeof v === 'string' || typeof v === 'number')
    .map((v) => String(v))
  const s = words.slice(0, 2).join(' · ')
  return s.length > 80 ? s.slice(0, 80) + '…' : s
}

function Tools({ tools }: { tools: Tool[] }) {
  const words: Record<string, string> = {
    all: 'All',
    token: 'your own tools',
    planner: 'Planning agent',
    assistant: 'the assistant in a conversation',
    chat: "the assistant in a ticket's chat",
  }
  const who = (roles: string[]) => roles.map((r) => words[r] ?? roleName(r)).join(', ')
  return (
    <>
      <h2 className="mt-8 mb-1 text-xl font-semibold">
        Tools{' '}
        <span className="text-sm font-normal text-text-dim">
          {tools.length} tools. The run's token decides what a call may touch.
        </span>
      </h2>
      <Card title="">
        <table className="w-full text-sm" data-tools>
          <thead className="text-left text-xs tracking-wide text-text-dim uppercase">
            <tr>
              <th className="py-1 pr-2">Group</th>
              <th className="pr-2">Tool</th>
              <th className="pr-2">What it does</th>
              <th className="pr-2">Who may call it</th>
              <th className="pr-2">Calls</th>
            </tr>
          </thead>
          <tbody>
            {tools.map((t) => (
              <tr
                key={t.name}
                className={'border-t border-border align-top ' + (t.name === 'create_ticket' || t.name === 'create_subtask' ? 'bg-accent-soft' : '')}
              >
                <td className="py-1 pr-2 text-text-dim">{t.group}</td>
                <td className="pr-2">
                  <code>{t.name}</code>
                </td>
                <td className="pr-2">{t.description}</td>
                <td className="pr-2">{who(t.roles)}</td>
                <td className="pr-2">{t.calls}</td>
              </tr>
            ))}
          </tbody>
        </table>
      </Card>
    </>
  )
}
