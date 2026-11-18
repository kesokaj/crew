import { createFileRoute, Link, useNavigate } from '@tanstack/react-router'
import { useCallback, useEffect, useState } from 'react'
import { getJSON, send, type InboxItem, type InboxItemAnswer } from '../lib/api'
import { usePoll } from '../lib/poll'
import { ago, button, Failed, mainButton, PageHead, when } from '../components/ui'

type Search = { item?: string; choose?: string }

export const Route = createFileRoute('/inbox')({
  component: Inbox,
  validateSearch: (s: Record<string, unknown>): Search => ({
    ...(typeof s.item === 'string' ? { item: s.item } : {}),
    ...(typeof s.choose === 'string' ? { choose: s.choose } : {}),
  }),
})

// The inbox (design 7.8, inbox.png): everything that needs you. Each item has its alternatives, says
// how long it has waited, and when it was mailed (18.1). A mail's link opens it here with an
// alternative chosen; only Confirm decides (18.3), and every press goes through the one route that
// records it, so an old link shows what was decided and when.
function Inbox() {
  const { item: chosenId, choose } = Route.useSearch()
  const navigate = useNavigate()
  const { data, error, reload } = usePoll<{ items: InboxItem[] }>('/api/inbox', 10000)
  const [answer, setAnswer] = useState<InboxItemAnswer | null>(null)
  const [busy, setBusy] = useState('')
  const [failed, setFailed] = useState('')
  const loadChosen = useCallback(() => {
    if (!chosenId) return setAnswer(null)
    getJSON<InboxItemAnswer>(`/api/inbox/item?id=${encodeURIComponent(chosenId)}`).then(setAnswer, (e: Error) => setFailed(e.message))
  }, [chosenId])
  useEffect(loadChosen, [loadChosen])
  const act = (it: InboxItem, label: string) => {
    setBusy(it.id)
    setFailed('')
    send('POST', '/api/inbox/decision', { item: it.id, choose: label }).then(
      () => {
        setBusy('')
        reload()
        loadChosen()
      },
      (e: Error) => {
        setBusy('')
        setFailed(`${it.title}: ${e.message}`)
        loadChosen()
      },
    )
  }
  const chosen = answer?.item ?? null
  const offered = chosen?.alternatives.some((a) => a.action && a.label === choose) ?? false
  const others = data?.items.filter((it) => it.id !== chosen?.id) ?? []
  const lastPress = answer?.decisions[0]
  return (
    <div className="mx-auto max-w-6xl p-6">
      <PageHead title="Inbox">
        What needs you: a project's OK, a project that Crew paused at a limit, a CLI that must be signed in again, and the machine, when
        microVMs do not start or the database could not be written. New features tell you first and then go ahead by themselves. Agents
        decide open choices themselves and record them; those are in each project's Decisions. Each item is also sent to you by mail.
      </PageHead>
      <Failed error={error || failed} />
      {chosenId && answer && !chosen && (
        <section data-chosen-gone className="mb-3 rounded-lg border border-border bg-bg-raised p-4">
          {lastPress ? (
            <p>
              Decided: <b>{lastPress.label}</b>, by {lastPress.by} at {when(lastPress.at)}.
            </p>
          ) : (
            <p>This no longer waits for you.</p>
          )}
          <button className={`${button} mt-3`} onClick={() => void navigate({ to: '/inbox', search: {} })}>
            See the inbox
          </button>
        </section>
      )}
      {chosen && (
        <div data-chosen className="mb-3">
          {choose && offered && (
            <p className="mb-2 text-sm">
              From a mail: <b>{choose}</b> is chosen. Nothing happens until you confirm.
            </p>
          )}
          {choose && !offered && (
            <p className="mb-2 text-sm" data-not-offered>
              <b>{choose}</b> is no longer offered here. Choose again below.
            </p>
          )}
          <Item it={chosen} chosen={offered ? choose : undefined} busy={busy} act={act} onNotNow={() => void navigate({ to: '/inbox', search: {} })} />
        </div>
      )}
      {data && data.items.length === 0 && !chosenId && <p className="text-text-dim">Nothing needs you.</p>}
      <div className="space-y-3">
        {others.map((it) => (
          <Item key={it.id} it={it} busy={busy} act={act} />
        ))}
      </div>
    </div>
  )
}

// Item is one card. With chosen, the alternative a mail chose is the one button, Confirm, beside Not
// now; without, every alternative is a button or a link.
function Item({
  it,
  chosen,
  busy,
  act,
  onNotNow,
}: {
  it: InboxItem
  chosen?: string
  busy: string
  act: (it: InboxItem, label: string) => void
  onNotNow?: () => void
}) {
  const mailed = it.notified ?? []
  const first = mailed[0]
  const last = mailed[mailed.length - 1]
  return (
    <section data-item={it.id} className="rounded-lg border border-amber bg-bg-raised p-4">
      <div className="flex font-mono text-xs text-text-dim">
        <span>{it.project || 'crew level'} · needs you</span>
        {ago(it.since) && <span className="ml-auto font-sans">waiting {ago(it.since)}</span>}
      </div>
      <h2 className="mt-2 font-semibold">{it.title}</h2>
      <p className="mt-1">{it.body}</p>
      <div className="mt-3 flex flex-wrap gap-2">
        {chosen ? (
          <>
            <button className={mainButton} disabled={busy === it.id} onClick={() => act(it, chosen)}>
              Confirm: {chosen}
            </button>
            <button className={button} onClick={onNotNow}>
              Not now
            </button>
          </>
        ) : (
          it.alternatives.map((a) =>
            a.action ? (
              <button key={a.label} className={a.main ? mainButton : button} disabled={busy === it.id} onClick={() => act(it, a.label)}>
                {a.label}
              </button>
            ) : (
              <Link key={a.label} to={a.link!} className={a.main ? mainButton : button}>
                {a.label}
              </Link>
            ),
          )
        )}
      </div>
      {first && (
        <p className="mt-2 text-xs text-text-dim" data-notified>
          Notified by mail at {when(first.sentAt)}
          {last && last !== first ? `, and again at ${when(last.sentAt)}` : ''}, with these alternatives
        </p>
      )}
    </section>
  )
}
