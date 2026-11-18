import { Link } from '@tanstack/react-router'
import type { RunDetail, Subtask } from '../../lib/api'
import { tokenCount } from '../../lib/tokens'
import { roleName } from '../../lib/words'
import { PromptView } from '../prompt'
import { CLIModel, duration } from '../runs'
import { when } from '../ui'
import { Box } from './ticket-header'

export function SubtaskTable({ subs }: { subs: Subtask[] }) {
  const running = subs.filter((s) => s.runs.some((r) => r.state !== 'ended')).length
  return (
    <Box title="Subtasks this ticket is using or has used" part="subtask" right={<span className="text-text-dim">{running} running · {subs.filter((s) => s.state === 'done').length} finished</span>}>
      {subs.length === 0 && <p className="text-sm text-text-dim">None yet.</p>}
      {subs.length > 0 && (
        <div className="overflow-x-auto">
          <table className="w-full text-sm">
            <thead className="text-left text-xs tracking-wide text-text-dim uppercase">
              <tr>
                <th className="py-1 pr-2">Subtask</th>
                <th className="pr-2">State</th>
                <th className="pr-2">Agent, CLI, and model</th>
                <th className="pr-2">Started</th>
                <th className="pr-2">Tokens</th>
              </tr>
            </thead>
            <tbody>
              {subs.map((s) => {
                const last = s.runs.at(-1)
                const used = s.runs.reduce((n, r) => n + (r.tokens ?? 0), 0)
                const live = last && last.state !== 'ended'
                return (
                  <tr key={s.id} className={'border-t border-border align-top ' + (live ? 'bg-accent-soft' : '')} data-subtask={s.id}>
                    <td className="py-1.5 pr-2">
                      <span className="font-mono text-xs">{s.id}</span> <span data-free-text>{s.title}</span>
                      <Origin s={s} />
                      {s.steps.length > 0 && (
                        <span className="block text-xs text-text-dim">
                          {s.steps.filter((x) => x.done).length}/{s.steps.length} steps
                        </span>
                      )}
                      {last && (
                        <Link to="/runs/$id" params={{ id: last.id }} className="block text-xs text-accent underline">
                          open log and sandbox
                        </Link>
                      )}
                    </td>
                    <td className="pr-2 whitespace-nowrap">{live ? <span className="text-accent">● running</span> : s.state === 'done' ? <span className="text-ok">✓ done</span> : s.state}</td>
                    <td className="pr-2">
                      {roleName(s.role)} {last ? <CLIModel r={last} /> : <span className="text-text-dim">no agent</span>}
                      {s.runs.length > 1 && <span className="block text-xs text-text-dim">{s.runs.length} runs</span>}
                    </td>
                    <td className="pr-2 whitespace-nowrap">
                      {last ? when(last.startedAt) : when(s.createdAt)}
                      {last && <span className="block text-xs text-text-dim">{duration(last.startedAt, last.endedAt)}</span>}
                    </td>
                    <td className="pr-2 tabular-nums">{tokenCount(used)}</td>
                  </tr>
                )
              })}
            </tbody>
          </table>
        </div>
      )}
      <p className="mt-2 text-xs text-text-dim">
        A subtask is one piece of work done by one agent run. An agent can add subtasks to its own ticket through the Crew MCP, and those that do not depend on each other run in
        parallel, each on its own branch; Crew joins them. Each opens with its own prompt, steps, log, and trajectory.
      </p>
    </Box>
  )
}

function Origin({ s }: { s: Subtask }) {
  const role = s.createdBy.split(' · ')[0] ?? s.createdBy
  if (s.kind === 'helper') return <span className="block text-xs text-text-dim">a helper{s.parent ? ` of ${s.parent}` : ''}, in the same sandbox</span>
  if (s.kind === 'join' && s.role === 'crew') return <span className="block text-xs text-text-dim">by the backend, into the ticket's branch</span>
  if (s.kind === 'join') return <span className="block text-xs text-text-dim">created by the backend: the parallel work did not join by itself</span>
  if (s.kind !== 'part') return null
  return (
    <span className="block text-xs text-text-dim" data-origin>
      created by the {role}
      {s.createdIn ? ` in ${s.createdIn}` : ''}, with <code>create_subtask</code>
      {s.branch ? (
        <>
          {' '}
          · in parallel, on <code>{s.branch}</code>
        </>
      ) : (
        ' · queued'
      )}
      {s.touches && s.touches.length > 0 ? ` · touches ${s.touches.join(', ')}` : ''}
      {s.joinedAt ? ' · joined' : ''}
    </span>
  )
}

export function TicketPromptBox({
  run,
  subs,
  isLive,
}: {
  run: RunDetail | null
  subs: Subtask[]
  isLive: boolean
}) {
  if (!run) return null
  return (
    <Box title={isLive ? 'The prompt that is running' : 'The prompt that was used'} part="prompt">
      <p className="mb-2 text-sm">
        <span data-free-text>{run.title}</span> · {roleName(subs.find((s) => s.runs.some((r) => r.id === run.id))?.role ?? '')} on <CLIModel r={run} /> · Crew {run.crewVersion}
      </p>
      <PromptView key={run.id} prompt={run.prompt} parts={run.parts} version={run.crewVersion} />
      <Messages run={run} />
    </Box>
  )
}

function Messages({ run }: { run: RunDetail }) {
  const later = run.turns.filter((t) => t.run === run.id && t.speaker !== 'agent').slice(1)
  if (later.length === 0) return null
  return (
    <>
      <h4 className="mt-3 mb-1 text-xs tracking-wide text-text-dim uppercase">Sent during the run</h4>
      <ol className="space-y-2">
        {later.map((t) => (
          <li key={t.number} className="rounded-md border border-border p-2 text-sm">
            <p className="text-xs text-text-dim">
              {t.speaker === 'crew' ? 'Crew' : 'You'} · {when(t.at)}
            </p>
            <pre className="mt-1 max-h-48 overflow-auto text-xs whitespace-pre-wrap" data-free-text>
              {t.text}
            </pre>
          </li>
        ))}
      </ol>
    </>
  )
}
