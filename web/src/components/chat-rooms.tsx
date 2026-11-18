import { Link } from '@tanstack/react-router'
import { useEffect, useState, type FormEvent } from 'react'
import { send } from '../lib/api'
import { usePoll } from '../lib/poll'
import { Card, Failed, PageHead, Tag, ago, button, mainButton } from './ui'

export type RoomThread = {
  id: number
  threadKey: string
  title: string
  specKey?: string
  ticketKeys: string[]
  state: string
  messageCount: number
  lastHopCount: number
}

export type ChoiceOption = {
  id: string
  label: string
  description?: string
}

export type ChoicePromptSpec = {
  id: string
  version: number
  update?: boolean
  replaced?: boolean
  question: string
  choices: ChoiceOption[]
  default?: string
  specRef?: string
}

export type ChoiceResponseSpec = {
  promptKey: string
  promptVersion: number
  choice: string
  provenance: 'direct' | 'agent_interpreted' | string
  sourceMessageId?: number
  rationale?: string
}

export type RoomAddressee = {
  id: number
  recipientRole: string
  recipientHandle: string
  via: string
  deliveryMode: 'none' | 'turn' | 'steer' | 'spawn' | string
  deliveryState: 'pending' | 'queued' | 'delivered' | 'suppressed' | 'failed' | string
  taskId?: string
  sessionId?: string
  failureReason?: string
}

export type RoomMessage = {
  id: number
  threadKey?: string
  taskKey?: string
  ticketKey?: string
  senderRole: string
  senderHandle: string
  kind?: 'text' | 'event' | 'choice_prompt' | 'choice_response' | string
  intent?: 'inform' | 'request' | 'question' | string
  eventType?: string
  replyToId?: number
  interrupt?: boolean
  choicePrompt?: ChoicePromptSpec
  choiceResponse?: ChoiceResponseSpec
  bodySha256?: string
  bodyRunes?: number
  offloaded?: boolean
  mentions: string[]
  addressees?: RoomAddressee[]
  hopCount: number
  fenced: boolean
  body: string
  fencedBody?: string
  actionable: boolean
  createdAt: string
}

export type RoomView = {
  channel: string
  topic?: string
  maxAgentHops: number
  threads: RoomThread[]
  messages: RoomMessage[]
}

export type ThreadView = {
  thread: RoomThread
  maxAgentHops: number
  currentHops: number
  messages: RoomMessage[]
}

// Tracks mobile virtual-keyboard compression using VisualViewport without inline style hacks (CSP-safe).
export function useVisualViewportKeyboard(): boolean {
  const [kbOpen, setKbOpen] = useState(false)

  useEffect(() => {
    if (typeof window === 'undefined' || !window.visualViewport) {
      return
    }
    const vv = window.visualViewport
    const update = () => {
      setKbOpen(window.innerHeight - vv.height > 120)
    }
    update()
    vv.addEventListener('resize', update)
    vv.addEventListener('scroll', update)
    return () => {
      vv.removeEventListener('resize', update)
      vv.removeEventListener('scroll', update)
    }
  }, [])

  return kbOpen
}

export function deliveryTone(state: string): 'ok' | 'amber' | 'bad' | 'blue' | undefined {
  switch (state) {
    case 'delivered':
      return 'ok'
    case 'queued':
    case 'pending':
      return 'blue'
    case 'failed':
      return 'bad'
    case 'suppressed':
      return 'amber'
    default:
      return undefined
  }
}

export function findLatestChoiceResponse(
  messages: RoomMessage[],
  promptKey: string,
  version: number,
): ChoiceResponseSpec | undefined {
  let found: ChoiceResponseSpec | undefined
  for (const m of messages) {
    const cr = m.choiceResponse
    if (cr && cr.promptKey === promptKey && cr.promptVersion === version) {
      found = cr
    }
  }
  return found
}

export function ChoicePromptCard({
  spec,
  response,
  busy,
  onSelect,
}: {
  spec: ChoicePromptSpec
  response?: ChoiceResponseSpec
  busy: boolean
  onSelect: (spec: ChoicePromptSpec, optionId: string) => void
}) {
  const superseded = Boolean(spec.replaced)
  const isInterpreted = response?.provenance === 'agent_interpreted'
  const isDirect = response?.provenance === 'direct'
  const locked = busy || superseded

  return (
    <div
      data-testid={`choice-card-${spec.id}-v${spec.version}`}
      className={
        'mt-2 rounded-md border p-3 text-sm ' +
        (superseded ? 'border-border bg-bg-sunk opacity-75' : 'border-accent/40 bg-bg-raised')
      }
    >
      <div className="mb-1.5 flex flex-wrap items-center gap-2">
        <span className="font-mono text-xs font-semibold text-accent">{spec.id}</span>
        <Tag>v{spec.version}</Tag>
        {superseded && <Tag>Superseded</Tag>}
        {isDirect && <Tag tone="ok">direct · @user</Tag>}
        {isInterpreted && (
          <Tag tone="amber">
            agent_interpreted{response?.sourceMessageId ? ` · src #${response.sourceMessageId}` : ''}
          </Tag>
        )}
        {spec.specRef && <Tag tone="blue">{spec.specRef}</Tag>}
      </div>
      <p className="font-medium text-text" data-free-text>
        {spec.question}
      </p>
      <div className="mt-2.5 flex flex-wrap gap-2">
        {spec.choices.map((opt) => {
          const isChosen = response?.choice === opt.id
          const isDefault = spec.default === opt.id
          return (
            <button
              key={opt.id}
              type="button"
              disabled={locked}
              data-testid={`choice-opt-${spec.id}-${opt.id}`}
              onClick={() => onSelect(spec, opt.id)}
              className={
                'rounded-md border px-2.5 py-1.5 text-left text-xs transition ' +
                (isChosen
                  ? 'border-accent bg-accent-soft font-semibold text-accent'
                  : 'border-border bg-bg hover:bg-bg-sunk')
              }
            >
              <span>{opt.label}</span>
              {isDefault && <span className="ml-1.5 font-mono text-[10px] text-text-dim">(default)</span>}
              {isChosen && <span className="ml-1.5 font-mono text-[10px]">[selected]</span>}
            </button>
          )
        })}
      </div>
      {isInterpreted && !superseded && (
        <p className="mt-2 text-xs text-text-dim">
          Interpreted from prose reply; click any option above as @user to override directly.
        </p>
      )}
    </div>
  )
}

export type ChatComposerSubmit = {
  body: string
  senderRole: string
  intent: string
  actionable?: boolean
  interrupt: boolean
}

export function ChatRoomsPanel({
  project,
  roomData,
  threadData,
  selectedThread,
  onSelectThread,
  onCreateThread,
  onPostMessage,
  onRespondChoice,
  busy,
  errorText,
  deliveryNotice,
}: {
  project: string
  roomData?: RoomView
  threadData?: ThreadView
  selectedThread: string
  onSelectThread: (threadKey: string) => void
  onCreateThread: (title: string) => void
  onPostMessage: (input: ChatComposerSubmit) => void
  onRespondChoice: (spec: ChoicePromptSpec, optionId: string) => void
  busy: boolean
  errorText?: string
  deliveryNotice?: string
}) {
  const kbOpen = useVisualViewportKeyboard()
  const [threadTitle, setThreadTitle] = useState('')
  const [senderRole, setSenderRole] = useState('user')
  const [intent, setIntent] = useState('')
  const [body, setBody] = useState('')
  const [actionableOverride, setActionableOverride] = useState<'auto' | 'yes' | 'no'>('auto')
  const [interrupt, setInterrupt] = useState(false)

  const threads = roomData?.threads ?? []
  const maxHops = roomData?.maxAgentHops ?? 3
  const messages = selectedThread ? (threadData?.messages ?? []) : (roomData?.messages ?? [])
  const currentHops = selectedThread
    ? (threadData?.currentHops ?? 0)
    : messages.reduce((acc, m) => Math.max(acc, m.hopCount), 0)

  const submitThread = (e: FormEvent) => {
    e.preventDefault()
    if (!threadTitle.trim()) return
    onCreateThread(threadTitle.trim())
    setThreadTitle('')
  }

  const submitMessage = (e: FormEvent) => {
    e.preventDefault()
    if (!body.trim()) return
    const actionable = actionableOverride === 'auto' ? undefined : actionableOverride === 'yes'
    onPostMessage({
      body: body.trim(),
      senderRole,
      intent,
      actionable,
      interrupt: senderRole === 'user' && interrupt,
    })
    setBody('')
    setInterrupt(false)
    setActionableOverride('auto')
  }

  return (
    <div
      className="crew-chat-viewport mx-auto max-w-7xl space-y-5 p-4 sm:p-6"
      data-kb-open={kbOpen ? 'true' : 'false'}
    >
      <PageHead
        crumbs={
          <>
            <Link to="/" className="text-accent">
              All projects
            </Link>{' '}
            / <span data-free-text>{project}</span>
          </>
        }
        title="Project Chat"
        actions={
          <div className="flex flex-wrap gap-2">
            <Link to="/p/$project/factory" params={{ project }} className={button}>
              Factory Line
            </Link>
            <Link to="/p/$project/specs" params={{ project }} className={button}>
              Specs &amp; WORKFLOW
            </Link>
          </div>
        }
      >
        Canonical <code data-free-text>{roomData?.channel ?? `#${project}`}</code> channel and per-feature threads (
        <code>feat/&lt;NNN&gt;-&lt;slug&gt;</code>) with <code>&lt;crew_data_fence&gt;</code> provenance (Rule 14) and{' '}
        <code>max_agent_hops: {maxHops}</code>.
      </PageHead>

      <Failed error={errorText ?? ''} />

      {deliveryNotice && (
        <div
          data-testid="chat-delivery-notice"
          className="rounded-md border border-border bg-bg-sunk px-3 py-2 text-xs text-text-dim"
        >
          {deliveryNotice}
        </div>
      )}

      <div className="grid gap-4 lg:grid-cols-[280px_minmax(0,1fr)]">
        <div className="min-w-0 space-y-4">
          <Card title="Channel & Feature Threads">
            <div className="space-y-1.5">
              <button
                type="button"
                onClick={() => onSelectThread('')}
                className={
                  'flex w-full items-center justify-between rounded-md border px-2.5 py-2 text-left text-sm ' +
                  (selectedThread === '' ? 'border-accent bg-accent-soft font-semibold text-accent' : 'border-border bg-bg hover:bg-bg-sunk')
                }
              >
                <span className="truncate font-mono" data-free-text>
                  {roomData?.channel ?? `#${project}`}
                </span>
                <Tag>channel</Tag>
              </button>
              {threads.map((th) => (
                <button
                  key={th.threadKey}
                  type="button"
                  data-thread={th.threadKey}
                  onClick={() => onSelectThread(th.threadKey)}
                  className={
                    'w-full rounded-md border p-2.5 text-left text-sm ' +
                    (selectedThread === th.threadKey
                      ? 'border-accent bg-accent-soft text-accent'
                      : 'border-border bg-bg hover:bg-bg-sunk')
                  }
                >
                  <div className="flex items-center justify-between gap-2">
                    <span className="truncate font-mono text-xs" data-free-text>
                      {th.threadKey}
                    </span>
                    <span className="text-xs text-text-dim">{th.messageCount}</span>
                  </div>
                  <p className="mt-1 truncate text-xs text-text-dim" data-free-text>
                    {th.title}
                  </p>
                </button>
              ))}
            </div>
          </Card>

          <Card title="New Feature Thread">
            <form onSubmit={submitThread} className="space-y-2 text-sm">
              <input
                aria-label="Thread title"
                data-testid="new-thread-title"
                placeholder="e.g. Cache eviction rules"
                value={threadTitle}
                onChange={(e) => setThreadTitle(e.target.value)}
                className="w-full rounded-md border border-border bg-bg px-2.5 py-1.5"
              />
              <button
                type="submit"
                data-testid="create-thread-btn"
                disabled={busy || !threadTitle.trim()}
                className={button}
              >
                Create thread
              </button>
            </form>
          </Card>
        </div>

        <div className="min-w-0 space-y-4">
          <Card
            title={selectedThread ? `Thread · ${selectedThread}` : `Channel · ${roomData?.channel ?? `#${project}`}`}
            right={
              <Tag tone={currentHops >= maxHops ? 'amber' : 'blue'}>
                hop {currentHops}/{maxHops}
              </Tag>
            }
          >
            {messages.length === 0 ? (
              <p className="text-sm text-text-dim">No messages in this stream yet.</p>
            ) : (
              <ul className="space-y-2.5">
                {messages.map((m) => {
                  const cp = m.choicePrompt
                  const cr = cp ? findLatestChoiceResponse(messages, cp.id, cp.version) : undefined
                  return (
                    <li key={m.id} className="rounded-md border border-border bg-bg p-3 text-sm">
                      <div className="mb-1 flex flex-wrap items-center gap-2">
                        <span className="font-mono text-xs font-semibold text-accent">
                          {m.senderHandle || m.senderRole}
                        </span>
                        {m.kind && m.kind !== 'text' && <Tag tone="blue">{m.kind}</Tag>}
                        {m.intent && <Tag>{m.intent}</Tag>}
                        {m.interrupt && <Tag tone="bad">steer</Tag>}
                        {m.fenced && <Tag tone="amber">crew_data_fence</Tag>}
                        {m.taskKey && <Tag tone="blue">{m.taskKey}</Tag>}
                        {m.ticketKey && <Tag>{m.ticketKey}</Tag>}
                        {(m.bodyRunes ?? 0) > 4000 && (
                          <Tag>{m.bodyRunes} runes · agent offload</Tag>
                        )}
                        <span className="ml-auto text-xs text-text-dim">
                          #{m.id} · hop {m.hopCount} · {ago(m.createdAt)}
                        </span>
                      </div>
                      {m.addressees && m.addressees.length > 0 && (
                        <div className="mb-1.5 flex flex-wrap items-center gap-1.5 text-xs">
                          <span className="text-text-dim">to:</span>
                          {m.addressees.map((a) => (
                            <Tag key={`${m.id}-${a.recipientRole}`} tone={deliveryTone(a.deliveryState)}>
                              {a.recipientHandle} · {a.deliveryMode}/{a.deliveryState}
                            </Tag>
                          ))}
                        </div>
                      )}
                      <p className="whitespace-pre-wrap break-words" data-free-text>
                        {m.body}
                      </p>
                      {cp && (
                        <ChoicePromptCard
                          spec={cp}
                          response={cr}
                          busy={busy}
                          onSelect={onRespondChoice}
                        />
                      )}
                      {m.choiceResponse && !cp && (
                        <div className="mt-1.5 flex flex-wrap items-center gap-1.5 text-xs">
                          <Tag tone={m.choiceResponse.provenance === 'direct' ? 'ok' : 'amber'}>
                            {m.choiceResponse.provenance}
                          </Tag>
                          <span className="font-mono text-text-dim">
                            {m.choiceResponse.promptKey} v{m.choiceResponse.promptVersion} -&gt; {m.choiceResponse.choice}
                          </span>
                        </div>
                      )}
                    </li>
                  )
                })}
              </ul>
            )}

            <form onSubmit={submitMessage} className="mt-4 space-y-2 border-t border-border pt-3 text-sm">
              <div className="flex flex-wrap items-center gap-3">
                <label className="text-xs text-text-dim">
                  Sender
                  <select
                    aria-label="Sender role"
                    value={senderRole}
                    onChange={(e) => {
                      const next = e.target.value
                      setSenderRole(next)
                      if (next !== 'user') setInterrupt(false)
                    }}
                    className="ml-2 rounded border border-border bg-bg px-2 py-1 text-sm text-text"
                  >
                    <option value="user">@user</option>
                    <option value="builder">@builder</option>
                    <option value="planner">@planner</option>
                    <option value="researcher">@researcher</option>
                    <option value="security">@security</option>
                    <option value="coordinator">@coordinator</option>
                  </select>
                </label>
                <label className="text-xs text-text-dim">
                  Intent
                  <select
                    aria-label="Message intent"
                    value={intent}
                    onChange={(e) => setIntent(e.target.value)}
                    className="ml-2 rounded border border-border bg-bg px-2 py-1 text-sm text-text"
                  >
                    <option value="">auto</option>
                    <option value="request">request (wake @peer)</option>
                    <option value="question">question</option>
                    <option value="inform">inform (post only)</option>
                  </select>
                </label>
                <label className="text-xs text-text-dim">
                  Wake peer
                  <select
                    aria-label="Wake peer"
                    value={actionableOverride}
                    onChange={(e) => setActionableOverride(e.target.value as 'auto' | 'yes' | 'no')}
                    className="ml-2 rounded border border-border bg-bg px-2 py-1 text-sm text-text"
                  >
                    <option value="auto">auto</option>
                    <option value="yes">force task</option>
                    <option value="no">no task</option>
                  </select>
                </label>
                <label
                  className={
                    'flex items-center gap-1.5 text-xs ' +
                    (senderRole === 'user' ? 'text-text-dim' : 'cursor-not-allowed text-text-dim opacity-50')
                  }
                >
                  <input
                    type="checkbox"
                    data-testid="chat-interrupt-toggle"
                    disabled={senderRole !== 'user'}
                    checked={senderRole === 'user' && interrupt}
                    onChange={(e) => setInterrupt(e.target.checked)}
                  />
                  Interrupt active turn now
                </label>
              </div>
              <textarea
                aria-label="Room message"
                data-testid="chat-composer-input"
                rows={3}
                placeholder="Post to room or thread (use @planner / @builder / @security to route; name bystanders without @)…"
                value={body}
                onChange={(e) => setBody(e.target.value)}
                className="w-full rounded-md border border-border bg-bg px-3 py-2"
              />
              <button
                type="submit"
                data-testid="chat-send-btn"
                disabled={busy || !body.trim()}
                className={mainButton}
              >
                Send message
              </button>
            </form>
          </Card>
        </div>
      </div>
    </div>
  )
}

export function ChatRoomsView({ project }: { project: string }) {
  const enc = encodeURIComponent(project)
  const [selectedThread, setSelectedThread] = useState('')
  const [actionErr, setActionErr] = useState('')
  const [deliveryNotice, setDeliveryNotice] = useState('')
  const [busy, setBusy] = useState(false)

  const roomPoll = usePoll<RoomView>(`/api/projects/${enc}/room?limit=80`, 4000)
  const threadPath = selectedThread
    ? `/api/projects/${enc}/room/threads/${selectedThread}?limit=80`
    : `/api/projects/${enc}/room?limit=80`
  const threadPoll = usePoll<ThreadView>(threadPath, 4000)

  const reloadAll = () => {
    roomPoll.reload()
    threadPoll.reload()
  }

  const handleCreateThread = async (title: string) => {
    const threads = roomPoll.data?.threads ?? []
    setBusy(true)
    setActionErr('')
    try {
      const slug = title
        .trim()
        .toLowerCase()
        .replace(/[^a-z0-9]+/g, '-')
        .replace(/^-|-$/g, '')
      const nextNum = String(threads.length + 1).padStart(3, '0')
      const threadKey = `feat/${nextNum}-${slug || 'thread'}`
      await send('POST', `/api/projects/${enc}/room/threads`, {
        threadKey,
        title,
      })
      setSelectedThread(threadKey)
      reloadAll()
    } catch (err) {
      setActionErr((err as Error).message)
    } finally {
      setBusy(false)
    }
  }

  const handlePostMessage = async (input: ChatComposerSubmit) => {
    setBusy(true)
    setActionErr('')
    setDeliveryNotice('')
    try {
      const res = await send<{ deliveryOutcome?: string; spawnedTask?: { key?: string } }>(
        'POST',
        `/api/projects/${enc}/room/messages`,
        {
          threadKey: selectedThread || undefined,
          senderRole: input.senderRole,
          intent: input.intent || undefined,
          interrupt: input.interrupt,
          body: input.body,
          actionable: input.actionable,
        },
      )
      if (res?.deliveryOutcome === 'posted_nobody_woken') {
        setDeliveryNotice('Posted to room; nobody woken.')
      } else if (res?.spawnedTask?.key) {
        setDeliveryNotice(`Posted and woke peer task ${res.spawnedTask.key}.`)
      }
      reloadAll()
    } catch (err) {
      setActionErr((err as Error).message)
    } finally {
      setBusy(false)
    }
  }

  const handleRespondChoice = async (spec: ChoicePromptSpec, optionId: string) => {
    setBusy(true)
    setActionErr('')
    setDeliveryNotice('')
    try {
      const res = await send<{ decision?: { key?: string } }>(
        'POST',
        `/api/projects/${enc}/room/choices/${encodeURIComponent(spec.id)}/respond`,
        {
          promptVersion: spec.version,
          choice: optionId,
          senderRole: 'user',
        },
      )
      if (res?.decision?.key) {
        setDeliveryNotice(`Recorded direct choice decision ${res.decision.key} on ${spec.id} (v${spec.version}).`)
      }
      reloadAll()
    } catch (err) {
      setActionErr((err as Error).message)
    } finally {
      setBusy(false)
    }
  }

  return (
    <ChatRoomsPanel
      project={project}
      roomData={roomPoll.data ?? undefined}
      threadData={threadPoll.data ?? undefined}
      selectedThread={selectedThread}
      onSelectThread={setSelectedThread}
      onCreateThread={(t) => void handleCreateThread(t)}
      onPostMessage={(inMsg) => void handlePostMessage(inMsg)}
      onRespondChoice={(spec, opt) => void handleRespondChoice(spec, opt)}
      busy={busy}
      errorText={roomPoll.error || (selectedThread ? threadPoll.error : '') || actionErr}
      deliveryNotice={deliveryNotice}
    />
  )
}
