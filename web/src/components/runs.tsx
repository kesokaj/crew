import type { RunView, Turn } from '../lib/api'
import { when } from './ui'

// What each stop reason of design 12.4 means, and what follows it.
export const reasons: Record<string, { text: string; follows: string }> = {
  ok: { text: 'Finished', follows: 'Done.' },
  operator: { text: 'Stopped by you', follows: 'Its work is kept. It waits until you continue it.' },
  restart: { text: 'Crew restarted', follows: 'It continues in a new microVM by itself.' },
  login: { text: 'Not signed in', follows: 'It continues by itself once its CLI is signed in again.' },
  quota: { text: 'At a limit', follows: 'It continues after the limit resets.' },
  timeout: { text: 'Past its time limit', follows: 'Retried once in a new microVM.' },
  silent: { text: 'Silent too long', follows: 'Retried once in a new microVM.' },
  turns: { text: 'At its limit of tool steps', follows: 'Retried once in a new microVM.' },
  error: { text: 'Error', follows: 'Retried once in a new microVM.' },
  refused: { text: 'A tool was refused', follows: 'Retried once in a new microVM.' },
  sandbox: { text: 'Its microVM ended', follows: 'Retried once in a new microVM.' },
  // No run stops for a budget since 0.53; a run that did before keeps its reason (12.4).
  budget: { text: 'At its budget (before 0.53)', follows: 'Since 0.53 no run stops for it.' },
}

export function waitText(waitFor?: string): string {
  if (!waitFor) return ''
  const [kind, cli] = waitFor.split(':')
  switch (kind) {
    case 'login':
      return `waiting for ${cli} to be signed in`
    case 'quota':
      return `waiting for the limit of ${cli} to reset`
    case 'retry':
      return 'about to be retried'
    case 'restart':
      return 'about to continue after the restart'
    case 'operator':
      return 'waiting for you'
  }
  return waitFor
}

export function stateText(r: RunView): string {
  if (r.stopping) return `stopping: ${reasons[r.stopping]?.text ?? r.stopping}`
  switch (r.state) {
    case 'starting':
      return 'starting its microVM'
    case 'working':
      return 'working'
    case 'idle':
      return 'waiting for its next turn'
  }
  return reasons[r.stopReason ?? '']?.text ?? r.stopReason ?? 'ended'
}

export function StateDot({ r }: { r: RunView }) {
  const live = r.state !== 'ended'
  const color = r.stopping
    ? 'bg-amber-500'
    : live
      ? 'bg-accent'
      : r.stopReason === 'ok'
        ? 'bg-green-600'
        : r.stopReason === 'operator' || r.stopReason === 'login' || r.stopReason === 'quota' || r.stopReason === 'restart'
          ? 'bg-amber-500'
          : 'bg-red-600'
  return <span className={`inline-block h-2 w-2 shrink-0 rounded-full ${color} ${live && !r.stopping ? 'animate-pulse' : ''}`} />
}

export function duration(from: string, to?: string): string {
  const ms = (to ? new Date(to) : new Date()).getTime() - new Date(from).getTime()
  const s = Math.max(0, Math.round(ms / 1000))
  if (s < 60) return `${s} s`
  const m = Math.round(s / 60)
  if (m < 60) return `${m} min`
  return `${Math.floor(m / 60)} h ${m % 60} min`
}

// The CLI and the model, in the shape of the simulation's chips.
export function CLIModel({ r }: { r: Pick<RunView, 'cli' | 'model' | 'effort'> }) {
  return (
    <span className="inline-flex items-center gap-1 rounded bg-bg px-1.5 py-0.5 font-mono text-xs whitespace-nowrap">
      <b>{r.cli}</b>
      {r.model && <span className="text-text-dim">· {r.model}</span>}
      {r.effort && <span className="text-text-dim">· {r.effort}</span>}
    </span>
  )
}

// CommitLine is a commit as a run's or a ticket's list shows it: its short id, and its subject, which
// the agent wrote and is marked data-free-text (0.53).
export function CommitLine({ c }: { c: { id: string; subject: string } }) {
  return (
    <>
      <span className="font-mono text-xs text-text-dim">{c.id.slice(0, 7)}</span> <span data-free-text>{c.subject}</span>
    </>
  )
}

// OmittedCommits says how many older commits a ticket's Git part leaves out: it keeps its newest
// 1,000 (6.1, since 1.09).
export function OmittedCommits({ n }: { n?: number }) {
  return n ? <p className="mt-1 text-xs text-text-dim">and {n} older commits, not listed here</p> : null
}

// Conversation is the turns of a run's conversation (12.10), as the Run screen shows them. A turn's
// text is the agent's, the operator's, or what Crew sent then, and is marked data-free-text (0.53).
export function Conversation({ turns }: { turns: Turn[] }) {
  return (
    <ol className="mt-2 space-y-2">
      {turns.map((t) => (
        <li key={t.number} className="rounded-md border border-border bg-bg-raised p-3 text-sm">
          <div className="mb-1 text-xs text-text-dim">
            {t.number}. {t.speaker === 'agent' ? 'The agent' : t.speaker === 'crew' ? 'Crew' : 'You'} · {when(t.at)}
            {t.rebuilt ? ' · rebuilt' : ''}
            {t.toolCalls.length > 0 ? ` · tools: ${t.toolCalls.join(', ')}` : ''}
            {t.tokens > 0 ? ` · ${t.tokens.toLocaleString()} tokens` : ''}
          </div>
          <p className="max-h-48 overflow-auto whitespace-pre-wrap" data-free-text>
            {t.text}
          </p>
        </li>
      ))}
    </ol>
  )
}
