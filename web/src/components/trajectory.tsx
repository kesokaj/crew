import { useEffect, useRef, useState, type ReactNode } from 'react'
import { getJSON, type RunEvent } from '../lib/api'
import { tokenCount } from '../lib/tokens'

// The trajectory (8.3): every event, in order, live while the run goes on.
export function Trajectory({ id, live }: { id: string; live: boolean }) {
  const [events, setEvents] = useState<RunEvent[]>([])
  const [raw, setRaw] = useState(false)
  const lastSeq = useRef(0)
  useEffect(() => {
    lastSeq.current = 0
    setEvents([])
  }, [id, raw])
  useEffect(() => {
    let stop = false
    // One request at a time, so two answers never add the same events.
    let inFlight = false
    const poll = () => {
      if (inFlight) return
      inFlight = true
      getJSON<{ events: RunEvent[] }>(`/api/runs/${id}/events?after=${lastSeq.current}&raw=${raw}&limit=500`)
        .then((r) => {
          const last = r.events.at(-1)
          if (stop || !last) return
          lastSeq.current = last.seq
          setEvents((old) => [...old, ...r.events])
          // A full page means more are waiting: the next is asked for at once.
          if (r.events.length === 500) setTimeout(poll, 0)
        }, () => {})
        .finally(() => {
          inFlight = false
        })
    }
    poll()
    const t = setInterval(poll, live ? 1500 : 10000)
    return () => {
      stop = true
      clearInterval(t)
    }
  }, [id, live, raw])
  const counts = events.reduce<Record<string, number>>((m, e) => ({ ...m, [e.type]: (m[e.type] ?? 0) + 1 }), {})
  return (
    <>
      <h2 className="mt-8 flex flex-wrap items-baseline gap-3 text-lg font-semibold">
        Trajectory
        {live && <span className="text-sm font-normal text-accent">● live: every event as it happens</span>}
        <span className="text-sm font-normal text-text-dim">
          {Object.entries(counts)
            .map(([k, v]) => `${v} ${k}`)
            .join(', ')}
        </span>
        <label className="ml-auto flex items-center gap-1 text-sm font-normal">
          <input type="checkbox" checked={raw} onChange={(e) => setRaw(e.target.checked)} /> the lines as the CLI printed them
        </label>
      </h2>
      <div className="mt-2 max-h-[40rem] overflow-auto rounded-md bg-neutral-900 p-3 font-mono text-xs text-neutral-200">
        {events.length === 0 && <p className="text-neutral-400">No event yet.</p>}
        {events.map((e) => (
          <div key={e.seq} className="border-b border-neutral-800 py-1">
            <span className="text-neutral-500">{new Date(e.at).toLocaleTimeString()}</span>{' '}
            <span className={typeColor[e.type] ?? 'text-neutral-300'}>{e.type}</span> {summary(e)}
            {raw && e.raw && (
              <div className="mt-1 break-all text-neutral-500" data-free-text>
                {e.raw}
              </div>
            )}
          </div>
        ))}
      </div>
    </>
  )
}

const typeColor: Record<string, string> = {
  message: 'text-sky-300',
  tool_call: 'text-amber-300',
  tool_result: 'text-amber-200',
  usage: 'text-green-300',
  quota: 'text-purple-300',
  error: 'text-red-400',
  status: 'text-neutral-400',
}

function text(v: unknown, n = 300): string {
  if (v == null) return ''
  const s = typeof v === 'string' ? v : JSON.stringify(v)
  return s.length > n ? s.slice(0, n) + '…' : s
}

// free marks what the agent, a tool, or the CLI wrote as not the app's own text (0.53).
const free = (s: string) => (s ? <span data-free-text>{s}</span> : null)

// summary says in one line what an event holds, from the payload its adapter made.
export function summary(e: RunEvent): ReactNode {
  const p = e.payload
  switch (e.type) {
    case 'message':
      return free(text(p.text) || text(p.stepType) || '')
    case 'tool_call':
      return free(`${text(p.tool)} ${text(p.input, 200)}`)
    case 'tool_result':
      return free(`${text(p.tool)} ${p.error ? '(failed) ' : ''}${text(p.output, 200)}`)
    case 'usage':
      // A usage event may still name a cost the CLI reported: since 0.53 Crew shows its tokens, never a cost.
      return (
        <>
          {free(text(p.response ?? p.result, 200))}
          {typeof p.tokensIn === 'number' || typeof p.tokensOut === 'number'
            ? ` · ${tokenCount(Number(p.tokensIn ?? 0))} in, ${tokenCount(Number(p.tokensOut ?? 0))} out`
            : ''}
        </>
      )
    case 'status':
      return free([p.status, p.kind, p.reason, p.model, p.detail].filter(Boolean).map((x) => text(x, 120)).join(' · '))
  }
  return free(text(p, 200))
}

// LogTail is the last lines of a run's trajectory, live, as the task view shows them (20.2).
export function LogTail({ id, live, lines = 6 }: { id: string; live: boolean; lines?: number }) {
  const [events, setEvents] = useState<RunEvent[]>([])
  useEffect(() => {
    let stop = false
    const poll = () =>
      getJSON<{ events: RunEvent[] }>(`/api/runs/${id}/events?tail=${lines}`).then(
        (r) => {
          if (!stop) setEvents(r.events)
        },
        () => {},
      )
    poll()
    if (!live) return
    const t = setInterval(poll, 2000)
    return () => {
      stop = true
      clearInterval(t)
    }
  }, [id, live, lines])
  return (
    <div className="rounded-md bg-term p-3 font-mono text-xs text-neutral-200" data-log={id}>
      {events.length === 0 && <p className="text-neutral-400">No event yet.</p>}
      {events.map((e) => (
        <div key={e.seq} className="py-0.5">
          <span className="text-neutral-500">{new Date(e.at).toLocaleTimeString([], { hour: '2-digit', minute: '2-digit' })}</span>{' '}
          <span className={typeColor[e.type] ?? 'text-neutral-300'}>{e.type}</span> {summary(e)}
        </div>
      ))}
    </div>
  )
}
