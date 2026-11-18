import { createFileRoute, Link } from '@tanstack/react-router'
import { send, type ActiveTicketsSummary } from '../lib/api'
import { usePoll } from '../lib/poll'
import { roleName } from '../lib/words'
import { ago, button, Failed, PageHead, Tag, when } from '../components/ui'
import { ProjectSandboxBar } from '../components/project-sandbox-controls'
import { tokenCount } from '../lib/tokens'

export const Route = createFileRoute('/p/$project/agents')({ component: Agents })

type AgentRun = {
  run: string
  role: string
  cli: string
  model?: string
  effort?: string
  subtask: string
  ticket?: string
  title: string
  sandbox?: string
  size?: string
  state: string
  startedAt?: string
  endedAt?: string
  // Its tokens in and out (design 0.53: no cost).
  tokens: number
  steps: number
  stepsDone: number
  step?: string
  result?: string
}

type Sandboxes = {
  activeTickets?: ActiveTicketsSummary
  running: AgentRun[]
  waiting: { what: string; for: string; since: string }[]
  finished: AgentRun[]
}

const head = 'px-3 py-2 text-left text-xs font-normal tracking-wide text-text-dim uppercase'
const cell = 'px-3 py-2 align-top'

function Model({ r }: { r: AgentRun }) {
  return (
    <span className="inline-block rounded bg-bg-sunk px-1.5 font-mono text-xs whitespace-nowrap">
      <b>{r.cli}</b> · {r.model ?? 'its default'}
      {r.effort && !r.model?.endsWith(r.effort) ? ` · ${r.effort}` : ''}
    </span>
  )
}

// Agents (design 20.1, agents.png): every sandbox that works in the project with its agent, model,
// ticket, and step; what waits for a lead slot, for room, for a limit, or for the setup's fix; and the
// runs that ended last.
function Agents() {
  const { project } = Route.useParams()
  const enc = encodeURIComponent(project)
  const { data, error } = usePoll<Sandboxes>(`/api/projects/${enc}/sandboxes`, 5000)
  const sessions = usePoll<{
    sessions: {
      id: string
      role: string
      state: string
      taskKey?: string
      taskId?: string
      ticket?: string
      readOnly: boolean
      branch?: string
      model?: string
    }[]
  }>(`/api/agents/sessions?project=${enc}`, 5000)
  const runtime = usePoll<{ harness?: { activeCLI: string; protocol: string; command: string[]; subagentsDisabled: boolean }; computeDriver?: string }>(
    '/api/agents/runtime',
    15000,
  )
  const abortPeer = (id: string) => {
    void send('POST', `/api/agents/sessions/${encodeURIComponent(id)}/abort`, {}).then(() => sessions.reload())
  }
  return (
    <div className="mx-auto max-w-6xl p-6">
      <PageHead
        crumbs={
          <>
            <Link to="/" className="text-accent">
              All projects
            </Link>{' '}
            / {project}
          </>
        }
        title="Agents"
      >
        This project runs <code>agy acp --stdio --process-per-session</code> inside its warm Kata microVM with isolated per-run git worktrees
        and a persistent <code>/home/agent</code>. Command checks and judges run in fresh throwaway microVMs.
      </PageHead>
      <Failed error={error} />
      <ProjectSandboxBar project={project} />
      {data?.activeTickets && (
        <p className="mb-4 text-xs text-text-dim" data-project-active-tickets>
          Active tickets in {project}: <b>{data.activeTickets.projectActive ?? 0} of {data.activeTickets.projectMax ?? 2}</b> · Machine:{' '}
          <b>{data.activeTickets.active} of {data.activeTickets.max}</b> ·{' '}
          <Link to="/sandboxes" className="text-accent underline">
            All sandboxes
          </Link>
        </p>
      )}
      {data && (
        <>
          <h2 className="mb-2 text-lg font-semibold">
            Running now <span className="text-sm font-normal text-text-dim">{data.running.length}</span>
          </h2>
          {data.running.length === 0 ? (
            <p className="mb-6 text-text-dim">No agent works in {project} now.</p>
          ) : (
            <table className="mb-6 w-full text-sm" aria-label="Running now">
              <thead>
                <tr className="border-b border-border">
                  <th className={head}>Agent</th>
                  <th className={head}>CLI and model</th>
                  <th className={head}>Working on</th>
                  <th className={head}>Sandbox</th>
                  <th className={head}>Time</th>
                  <th className={head}>Step</th>
                  <th className={head}>Tokens</th>
                </tr>
              </thead>
              <tbody>
                {data.running.map((r) => (
                  <tr key={r.run} className="border-b border-border" data-run={r.run}>
                    <td className={cell}>{roleName(r.role)}</td>
                    <td className={cell}>
                      <Model r={r} />
                    </td>
                    <td className={cell}>
                      {r.subtask.includes('-') && <span className="font-mono text-xs">{r.subtask} </span>}
                      {r.title}
                    </td>
                    <td className={cell + ' font-mono text-xs whitespace-nowrap'}>
                      {r.sandbox ?? 'starting'}
                      {r.size && <span className="block text-text-dim">{r.size}</span>}
                    </td>
                    <td className={cell}>{ago(r.startedAt)}</td>
                    <td className={cell + ' w-48'}>
                      {r.steps > 0 ? (
                        <>
                          <svg
                            className="h-1 w-full rounded bg-bg-sunk"
                            viewBox="0 0 100 4"
                            preserveAspectRatio="none"
                            aria-hidden="true"
                          >
                            <rect
                              x={0}
                              y={0}
                              width={Math.min(100, Math.max(0, Math.round((100 * r.stepsDone) / r.steps)))}
                              height={4}
                              fill="var(--color-accent)"
                            />
                          </svg>
                          <span className="text-xs text-text-dim">{r.step ?? `${r.stepsDone}/${r.steps} steps`}</span>
                        </>
                      ) : (
                        <span className="text-xs text-text-dim">{r.state === 'idle' ? 'waits for its next turn' : 'no steps written yet'}</span>
                      )}
                    </td>
                    <td className={cell + ' tabular-nums'}>{tokenCount(r.tokens)}</td>
                  </tr>
                ))}
              </tbody>
            </table>
          )}
          <h2 className="mb-2 text-lg font-semibold">Waiting</h2>
          {data.waiting.length === 0 ? (
            <p className="mb-6 text-text-dim">Nothing waits.</p>
          ) : (
            <ul className="mb-6 space-y-1" aria-label="Waiting">
              {data.waiting.map((w, i) => (
                <li key={i} className="rounded-md border border-border bg-bg-raised px-3 py-2 text-sm">
                  {w.what} <span className="text-text-dim">· waiting for {w.for}, {ago(w.since)}</span>
                </li>
              ))}
            </ul>
          )}
          <h2 className="mb-2 text-lg font-semibold">Recently finished</h2>
          {data.finished.length === 0 ? (
            <p className="text-text-dim">No run has ended yet.</p>
          ) : (
            <table className="w-full text-sm" aria-label="Recently finished">
              <thead>
                <tr className="border-b border-border">
                  <th className={head}>Agent</th>
                  <th className={head}>CLI and model</th>
                  <th className={head}>Worked on</th>
                  <th className={head}>Sandbox</th>
                  <th className={head}>Ended</th>
                  <th className={head}>Result</th>
                  <th className={head}>Tokens</th>
                </tr>
              </thead>
              <tbody>
                {data.finished.map((r) => (
                  <tr key={r.run} className="border-b border-border">
                    <td className={cell}>{roleName(r.role)}</td>
                    <td className={cell}>
                      <Model r={r} />
                    </td>
                    <td className={cell}>
                      {r.subtask.includes('-') && <span className="font-mono text-xs">{r.subtask} </span>}
                      {r.title}
                    </td>
                    <td className={cell + ' font-mono text-xs whitespace-nowrap'}>{r.sandbox ?? '—'}</td>
                    <td className={cell}>{when(r.endedAt)}</td>
                    <td className={cell}>{r.result}</td>
                    <td className={cell + ' tabular-nums'}>{tokenCount(r.tokens)}</td>
                  </tr>
                ))}
              </tbody>
            </table>
          )}
        </>
      )}

      <div className="mt-8 space-y-3 rounded-lg border border-border bg-bg-raised p-4">
        <div className="flex flex-wrap items-center justify-between gap-2">
          <h2 className="text-base font-semibold">ACP Peer Sessions &amp; Harness Runtime</h2>
          {runtime.data?.harness && (
            <div className="flex flex-wrap items-center gap-2 text-xs text-text-dim">
              <Tag tone="blue">{runtime.data.harness.activeCLI}</Tag>
              <span>{runtime.data.harness.protocol}</span>
              <span>·</span>
              <code data-free-text>{runtime.data.harness.command.join(' ')}</code>
              <span>·</span>
              <span>Adapter seam: internal/cli/ACPAdapter</span>
            </div>
          )}
        </div>
        {!sessions.data || sessions.data.sessions.length === 0 ? (
          <p className="text-sm text-text-dim">No ACP peer sessions active or recorded for {project}.</p>
        ) : (
          <ul className="space-y-2 text-sm">
            {sessions.data.sessions.slice(0, 12).map((s) => (
              <li key={s.id} className="flex flex-wrap items-center justify-between gap-2 rounded border border-border bg-bg p-2.5">
                <div className="min-w-0">
                  <span className="font-medium">{roleName(s.role)}</span>
                  {s.taskKey && <span className="ml-2 font-mono text-xs text-text-dim">{s.taskKey}</span>}
                  {s.ticket && <span className="ml-2 font-mono text-xs text-accent">{s.ticket}</span>}
                  {s.branch && (
                    <span className="ml-2 font-mono text-xs text-text-dim" data-free-text>
                      {s.branch}
                    </span>
                  )}
                </div>
                <div className="flex items-center gap-2">
                  {s.readOnly && <Tag tone="amber">ReadOnly</Tag>}
                  <Tag tone={s.state === 'ended' ? undefined : 'blue'}>{s.state}</Tag>
                  {s.state !== 'ended' && (
                    <button type="button" className={button} onClick={() => abortPeer(s.id)}>
                      Abort
                    </button>
                  )}
                </div>
              </li>
            ))}
          </ul>
        )}
      </div>
    </div>
  )
}
