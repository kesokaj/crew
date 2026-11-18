import { createFileRoute, Link, useNavigate } from '@tanstack/react-router'
import { useEffect, useState } from 'react'
import { send } from '../lib/api'
import { usePoll } from '../lib/poll'
import { agentDoing, plainWait, roleName, stageName } from '../lib/words'
import { button, Failed, mainButton, PageHead, Tag } from '../components/ui'
import { AddTicket } from '../components/addticket'
import { stripBackticks } from '../components/activity'
import { FactoryTasksLedger, TicketYamlModal } from '../components/factory-ledger'
import { ProjectSandboxBar } from '../components/project-sandbox-controls'
import { WriteBox, WriteScope } from '../components/writebox'

export const Route = createFileRoute('/p/$project/board')({ component: Board })

export function visibleColumnTickets<T>(tickets: T[], expanded?: boolean, limit = 12): T[] {
  return expanded ? tickets : tickets.slice(0, limit)
}

type CardAgent = { role: string; cli: string; model?: string; effort?: string; state: string; stepsDone: number; steps: number }
type Card = {
  key: string
  title: string
  goal: string
  kind: string
  priority: number
  origin: string
  createdBy: string
  hold: boolean
  analysis: string
  waiting?: string
  stuck?: string
  replaces: boolean
  fixRounds: number
  agents: CardAgent[]
}
type Column = { id: string; title?: string; optional: boolean; role?: string; wip?: number; tickets: Card[] }
type BoardView = {
  project: { name: string; key: string; goal: string; state: string; pauseReason?: string; protected?: boolean }
  columns: Column[]
  stages: { id: string; title?: string; optional: boolean; on: boolean }[]
  closed: number
  inStagesOff: Card[]
  notice?: string
}

// The board (design 20.1, board.png): one column per stage that is on, from Planning to Done. Agents
// move the cards; you add tickets, set priority and hold on a ticket, and pause the project. Amber
// means it needs you, blue that an agent is working.
function Board() {
  const { project } = Route.useParams()
  const navigate = useNavigate()
  const { data: b, error, reload } = usePoll<BoardView>(`/api/projects/${encodeURIComponent(project)}/board`, 4000)
  const [failed, setFailed] = useState('')
  const [addOpen, setAddOpen] = useState(false)
  const [expandedCols, setExpandedCols] = useState<Record<string, boolean>>({})
  const [selectedKey, setSelectedKey] = useState('')
  const [yamlKey, setYamlKey] = useState('')
  const cardKeys = b ? b.columns.flatMap((c) => c.tickets.map((t) => t.key)) : []
  const act = (path: string, body: unknown = {}) =>
    send('POST', path, body).then(reload, (e: Error) => setFailed(e.message))

  useEffect(() => {
    const onKey = (e: KeyboardEvent) => {
      if (e.metaKey || e.ctrlKey || e.altKey) return
      const el = e.target as HTMLElement | null
      const tag = el?.tagName?.toLowerCase() ?? ''
      if (tag === 'input' || tag === 'textarea' || tag === 'select' || el?.isContentEditable) return
      if (e.key === 'Escape' && yamlKey) {
        setYamlKey('')
        return
      }
      if (cardKeys.length === 0) return
      const curIdx = selectedKey ? cardKeys.indexOf(selectedKey) : -1
      const activeKey = curIdx >= 0 ? cardKeys[curIdx]! : cardKeys[0]!
      if (e.key === 'j') {
        e.preventDefault()
        setSelectedKey(cardKeys[(curIdx + 1) % cardKeys.length]!)
      } else if (e.key === 'k') {
        e.preventDefault()
        const prev = curIdx <= 0 ? cardKeys.length - 1 : curIdx - 1
        setSelectedKey(cardKeys[prev]!)
      } else if (e.key === 'Enter' && selectedKey) {
        e.preventDefault()
        void navigate({ to: '/p/$project/tickets/$key', params: { project, key: activeKey } })
      } else if (e.key === 'y' || e.key === 'Y') {
        e.preventDefault()
        setYamlKey((cur) => (cur === activeKey ? '' : activeKey))
      }
    }
    window.addEventListener('keydown', onKey)
    return () => window.removeEventListener('keydown', onKey)
  }, [cardKeys, navigate, project, selectedKey, yamlKey])

  if (!b) return <div className="p-6 text-text-dim">{error || 'Loading…'}</div>
  const p = b.project
  return (
    <WriteScope.Provider value={{ project: p.name }}>
    <div className="p-6">
      <PageHead
        crumbs={
          <>
            <Link to="/" className="text-accent">
              All projects
            </Link>{' '}
            / {p.name}
            {p.protected && (
              <span className="ml-2" data-protected>
                <Tag tone="blue">Protected</Tag>
              </span>
            )}
          </>
        }
        title="Board"
        actions={
          <>
            <button type="button" className={button} onClick={() => setAddOpen((o) => !o)}>
              + New ticket
            </button>
            {p.state === 'running' && (
              <button className={button} onClick={() => act(`/api/projects/${p.name}/pause`)}>
                Pause the project
              </button>
            )}
            {p.state === 'paused' && (
              <button className={mainButton} onClick={() => act(`/api/projects/${p.name}/resume`)}>
                Resume
              </button>
            )}
            <Link to="/p/$project/terminal" params={{ project: p.name }} className={button}>
              Terminal
            </Link>
          </>
        }
      >
        <span data-free-text>{stripBackticks(p.goal)}</span>
      </PageHead>
      <Failed error={error || failed} />
      {addOpen && (
        <div className="mb-4">
          <AddTicket project={p.name} onAdded={reload} open={addOpen} onOpenChange={setAddOpen} hideTrigger />
        </div>
      )}
      {p.state === 'paused' && (
        <p className="mb-3 rounded-md border border-amber bg-amber-soft p-2 text-sm text-amber-text">
          Paused: {p.pauseReason}. Nothing starts, and no ticket moves, until it is resumed.
        </p>
      )}
      <div className="mb-4 flex flex-wrap items-center gap-2">
        <StagesBar stages={b.stages} project={p.name} />
      </div>
      <div className="mb-4 flex flex-wrap items-center gap-2 rounded-md border border-border bg-bg-raised px-3 py-2 text-xs">
        <Link to="/p/$project/factory" params={{ project: p.name }} className="font-semibold text-accent hover:underline">
          Factory Line
        </Link>
        <span className="text-text-dim">·</span>
        <Link to="/p/$project/specs" params={{ project: p.name }} className="text-text hover:text-accent hover:underline">
          Specs &amp; WORKFLOW
        </Link>
        <span className="text-text-dim">·</span>
        <Link to="/p/$project/chat" params={{ project: p.name }} className="text-text hover:text-accent hover:underline">
          Project Chat
        </Link>
        <span className="text-text-dim">·</span>
        <Link to="/p/$project/skills" params={{ project: p.name }} className="text-text hover:text-accent hover:underline">
          Skills Registry
        </Link>
        <span className="ml-auto text-text-dim">Keys: j / k select · Enter open · y YAML</span>
        {cardKeys.length > 0 && (
          <button
            type="button"
            className="rounded border border-border px-2 py-0.5 font-mono hover:border-accent"
            onClick={() => setYamlKey(selectedKey || cardKeys[0]!)}
          >
            YAML
          </button>
        )}
      </div>
      <ProjectSandboxBar project={p.name} />
      {b.notice && <p className="mb-3 text-sm text-text-dim">{b.notice}</p>}
      <div className="flex gap-3 overflow-x-auto pb-3">
        {b.columns.map((c) => (
          <section key={c.id} className="w-[220px] shrink-0" data-column={c.id}>
            <h2 className="mb-2 flex border-b border-dashed border-border pb-1 text-xs tracking-wide text-text-dim uppercase">
              {c.title ?? stageName[c.id] ?? c.id}
              {c.optional && <span className="ml-1 normal-case">· {c.title || !stageName[c.id] ? "the project's own" : 'optional'}</span>}
              <span className="ml-auto">
                {c.wip ? `${c.tickets.filter((t) => t.agents.length > 0).length} / ${c.wip}` : c.tickets.length}
              </span>
            </h2>
            {c.id === 'planning' && p.state === 'planning' && c.tickets.length > 0 && (
              <div className="mb-2 rounded-md border border-amber bg-amber-soft p-2 text-sm">
                <p>The breakdown waits for your OK. Nothing is built before it.</p>
                <button className={mainButton + ' mt-2'} onClick={() => act(`/api/projects/${p.name}/ok`)}>
                  Give the OK
                </button>
                <SendBack project={p.name} onSent={reload} />
              </div>
            )}
            {c.tickets.length === 0 && (
              <p className="text-sm text-text-dim">{c.id === 'planning' ? 'Nothing waits for you.' : '—'}</p>
            )}
            <div className="space-y-2">
              {visibleColumnTickets(c.tickets, expandedCols[c.id], 12).map((t) => (
                <TicketCard key={t.key} project={p.name} t={t} selected={t.key === selectedKey} />
              ))}
              {c.tickets.length > 12 && (
                <button
                  type="button"
                  className="w-full rounded-md bg-bg-sunk px-2 py-1 text-left text-sm text-accent hover:underline focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-accent"
                  onClick={() => setExpandedCols((prev) => ({ ...prev, [c.id]: !prev[c.id] }))}
                >
                  {expandedCols[c.id] ? 'Show first 12' : `Show all ${c.tickets.length} tickets (and ${c.tickets.length - 12} more)`}
                </button>
              )}
            </div>
          </section>
        ))}
      </div>
      {b.inStagesOff.length > 0 && (
        <p className="mt-2 text-sm text-text-dim">
          In a stage that was switched off, finishing it: {b.inStagesOff.map((t) => t.key).join(', ')}.
        </p>
      )}
      <p className="mt-4 text-sm text-text-dim">
        Cards are moved by agents when their checks pass. A dashed card was created by an agent or by you after the plan. {b.closed}{' '}
        {b.closed === 1 ? 'ticket is' : 'tickets are'} closed.
      </p>
      <FactoryTasksLedger project={p.name} />
      {!addOpen && <AddTicket project={p.name} onAdded={reload} open={addOpen} onOpenChange={setAddOpen} />}
      {yamlKey && <TicketYamlModal project={p.name} ticketKey={yamlKey} onClose={() => setYamlKey('')} />}
    </div>
    </WriteScope.Provider>
  )
}

// SendBack sends the breakdown back to the lead with a comment, before the OK (7.2, step 5).
function SendBack({ project, onSent }: { project: string; onSent: () => void }) {
  const [open, setOpen] = useState(false)
  const [text, setText] = useState('')
  const [error, setError] = useState('')
  if (!open)
    return (
      <button className="mt-2 block text-sm text-accent" onClick={() => setOpen(true)}>
        Send it back with a comment
      </button>
    )
  return (
    <div className="mt-2">
      <WriteBox aria-label="Your comment on the breakdown" rows={3} value={text} onChange={setText} placeholder="What the lead should change" />
      <button
        className={button + ' mt-1'}
        disabled={!text.trim()}
        onClick={() =>
          send('POST', `/api/projects/${project}/ok`, { sendBack: text }).then(
            () => {
              setOpen(false)
              setText('')
              onSent()
            },
            (e: Error) => setError(e.message),
          )
        }
      >
        Send back
      </button>
      <Failed error={error} />
    </div>
  )
}

export function StagesBar({ stages, project }: { stages: BoardView['stages']; project: string }) {
  return (
    <>
      <ol className="flex flex-wrap items-center gap-1 text-sm" aria-label="Stages">
        {stages.map((s, i) => (
          <li key={s.id} className="flex items-center gap-1">
            {i > 0 && <span className="text-text-dim">›</span>}
            <span
              className={
                'rounded-full border px-2.5 py-0.5 ' +
                (!s.on ? 'border-dashed text-text-dim line-through' : s.optional ? 'border-accent bg-accent-soft text-accent' : 'border-border bg-bg-raised')
              }
              title={!s.on ? 'Switched off for this project' : s.optional ? 'An optional stage, on' : 'A fixed stage'}
            >
              {s.title ?? stageName[s.id] ?? s.id}
            </span>
          </li>
        ))}
      </ol>
      <Link to="/p/$project/settings" params={{ project }} className={button + ' ml-auto'}>
        Change the stages
      </Link>
    </>
  )
}

function TicketCard({ project, t, selected }: { project: string; t: Card; selected?: boolean }) {
  const working = t.agents.find((a) => a.state !== 'idle') ?? t.agents[0]
  const needsYou = !!t.stuck && !plainWait(t.stuck)
  const border = needsYou ? 'border-amber' : working ? 'border-accent' : 'border-border'
  return (
    <Link
      to="/p/$project/tickets/$key"
      params={{ project, key: t.key }}
      data-card={t.key}
      className={
        'block rounded-md border bg-bg-raised p-2.5 text-sm break-words [overflow-wrap:anywhere] ' +
        border +
        (t.origin !== 'plan' ? ' border-dashed' : '') +
        (selected ? ' ring-2 ring-accent' : '')
      }
    >
      <p className="flex flex-wrap items-center gap-1.5">
        <span className="font-mono text-xs text-text-dim">{t.key}</span>
        <Tag>{t.kind}</Tag>
        {t.hold && <Tag tone="amber">on hold</Tag>}
        {t.replaces && <Tag>replaces a cancelled ticket</Tag>}
      </p>
      <p className="mt-1 font-semibold">{t.title}</p>
      {working ? (
        <>
          <p className="mt-1 text-accent">
            ●{' '}
            {t.agents.filter((a) => a.state !== 'idle' && a.role === working.role).length > 1
              ? `${t.agents.filter((a) => a.state !== 'idle' && a.role === working.role).length} ${roleName(working.role).toLowerCase()}s work in parallel`
              : agentDoing(working.role, working.state)}
          </p>
          {t.agents.map((a, i) => (
            <p key={i} className="mt-1 truncate rounded bg-bg-sunk px-1.5 font-mono text-xs">
              <b>{a.cli}</b> · {a.model ?? 'its default'}
              {a.effort ? ` · ${a.effort}` : ''}
            </p>
          ))}
          {working.steps > 0 && (
            <>
              <p className="mt-1 text-xs text-text-dim">
                {working.stepsDone}/{working.steps} steps
              </p>
              <svg className="h-1 w-full rounded bg-bg-sunk" viewBox="0 0 100 4" preserveAspectRatio="none" aria-hidden="true">
                <rect
                  x={0}
                  y={0}
                  width={Math.min(100, Math.max(0, Math.round((100 * working.stepsDone) / working.steps)))}
                  height={4}
                  fill="var(--color-accent)"
                />
              </svg>
            </>
          )}
        </>
      ) : (
        (t.stuck && !needsYou ? t.stuck : t.waiting) && <p className="mt-1 text-text-dim">{t.stuck && !needsYou ? t.stuck : t.waiting}</p>
      )}
      {needsYou && <p className="mt-1 text-amber-text">Stuck: {t.stuck}</p>}
      {t.fixRounds > 0 && <p className="mt-1 text-xs text-text-dim">{t.fixRounds} fix {t.fixRounds === 1 ? 'round' : 'rounds'}</p>}
    </Link>
  )
}
