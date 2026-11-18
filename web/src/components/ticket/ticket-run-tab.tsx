import { Link, useNavigate } from '@tanstack/react-router'
import { useState } from 'react'
import { send, type RunDetail, type Subtask, type TaskGit, type TaskView } from '../../lib/api'
import { fromWords } from '../../lib/models'
import { tokens } from '../../lib/tokens'
import { agentDoing, roleName } from '../../lib/words'
import { listed, Omitted } from '../diff'
import { FileExplorerModal } from '../explorer'
import { CLIModel, CommitLine, duration, OmittedCommits, reasons } from '../runs'
import { LogTail } from '../trajectory'
import { button, when } from '../ui'
import { Box } from './ticket-header'

export function RunningNow({ project, v, live, run }: { project: string; v: TaskView; live: { s: Subtask; r: Subtask['runs'][number] }[]; run: RunDetail | null }) {
  const t = v.ticket
  if (live.length === 0) {
    const why = t.hold ? 'It is on hold. Let it go to go on.' : t.stuck ? `Nothing more starts for it: ${t.stuck}` : t.waiting || 'Nothing runs on it now.'
    return (
      <Box title="Running now" part="log">
        <p className={t.stuck ? 'text-amber-text' : 'text-text-dim'} data-why>
          {why}
        </p>
        {t.stuck && t.waiting && <p className="mt-1 text-sm text-text-dim">The board says: {t.waiting}</p>}
        {t.stuck && <p className="mt-2 text-sm text-text-dim">Continue its stopped subtask from the subtask's page; a continue you ask for clears this.</p>}
      </Box>
    )
  }
  const parallel = live.filter(({ s }) => s.kind === 'part')
  const shown = parallel.length > 1 ? [live.find(({ s }) => s.kind === 'part')!] : live
  const others = parallel.length > 1 ? parallel.slice(1) : []
  return (
    <>
      {shown.map(({ s, r }) => {
        const detail = run && run.id === r.id ? run : null
        const done = s.steps.filter((x) => x.done).length
        const right =
          parallel.length > 1 ? (
            <span className="text-accent">
              ● {parallel.length} {roleName(s.role).toLowerCase()}s work in parallel
            </span>
          ) : (
            <span className="text-accent">● {agentDoing(s.role, r.state === 'idle' ? 'idle' : 'working')}</span>
          )
        return (
          <Box key={r.id} title="Running now" tone="blue" right={right} part="log">
            {parallel.length > 1 && (
              <p className="mb-3 text-sm" data-parallel>
                <b>{parallel.length} subtasks of this ticket run in parallel</b>, each in its own sandbox and on its own branch. Two that name the same files run one after the
                other. Crew joins them when they are done. Also running:{' '}
                {others.map(({ s: o }, i) => (
                  <span key={o.id}>
                    {i > 0 && ', '}
                    <code>{o.id}</code> <span data-free-text>{o.title}</span>
                  </span>
                ))}
              </p>
            )}
            <div className="grid gap-4 md:grid-cols-2">
              <dl className="grid grid-cols-[6.5rem_1fr] gap-x-2 gap-y-1.5 text-sm">
                <dt className="text-text-dim">Agent</dt>
                <dd className="font-semibold">{roleName(s.role)}</dd>
                <dt className="text-text-dim">Model</dt>
                <dd>
                  <CLIModel r={r} />
                  {r.modelFrom && <div className="text-xs text-text-dim">{fromWords(r.modelFrom)}</div>}
                </dd>
                <dt className="text-text-dim">Subtask</dt>
                <dd>
                  {s.id} <span data-free-text>{s.title}</span>
                </dd>
                <dt className="text-text-dim">Run</dt>
                <dd>
                  <Link to="/runs/$id" params={{ id: r.id }} className="font-mono text-xs text-accent">
                    {r.id.slice(0, 8)}
                  </Link>
                </dd>
                <dt className="text-text-dim">Sandbox</dt>
                <dd className="font-mono text-xs">{detail?.sandbox ?? '…'}</dd>
                <dt className="text-text-dim">Conversation</dt>
                <dd className="font-mono text-xs">{detail ? `${detail.way === 'new' ? 'new' : 'continues'}, ${detail.conversation.slice(0, 8)}` : '…'}</dd>
                <dt className="text-text-dim">Crew version</dt>
                <dd className="font-mono text-xs">
                  {detail?.crewVersion} · {r.cli} {detail?.cliVersion}
                </dd>
                <dt className="text-text-dim">Running for</dt>
                <dd>
                  {duration(r.startedAt)}, since {when(r.startedAt)}
                </dd>
                <dt className="text-text-dim">Tokens</dt>
                <dd>{tokens(r.tokens)} in this run, in and out</dd>
                {detail && (
                  <>
                    <dt className="text-text-dim">Turns</dt>
                    <dd>
                      {detail.toolSteps} of {detail.maxTurns} tool steps
                    </dd>
                  </>
                )}
              </dl>
              <div>
                <h4 className="mb-1 text-xs tracking-wide text-text-dim uppercase">Steps the agent set for itself</h4>
                {s.steps.length === 0 && <p className="text-sm text-text-dim">None set yet.</p>}
                <ol className="space-y-1 text-sm">
                  {s.steps.map((x, i) => {
                    const current = !x.done && s.steps.slice(0, i).every((y) => y.done)
                    return (
                      <li key={i} className={x.done ? 'text-text-dim' : current ? 'font-semibold' : 'text-text-dim'}>
                        {x.done ? '✓' : current ? '›' : ' '} <span data-free-text>{x.text}</span>
                      </li>
                    )
                  })}
                </ol>
                {s.steps.length > 0 && (
                  <div className="mt-2 h-1 rounded bg-bg-sunk">
                    <div className="h-1 rounded bg-accent" style={{ width: `${(100 * done) / s.steps.length}%` }} />
                  </div>
                )}
              </div>
            </div>
            <div className="mt-3">
              <LogTail id={r.id} live />
            </div>
            <div className="mt-2 flex flex-wrap gap-2">
              <Link to="/runs/$id" params={{ id: r.id }} className={button}>
                Open the full log, the command line, and the sandbox
              </Link>
              <Link to="/p/$project/terminal" params={{ project }} search={{ run: r.id }} className={button}>
                Jump into this sandbox
              </Link>
            </div>
          </Box>
        )
      })}
    </>
  )
}

export function Outcome({ project, t, git, lastRun }: { project: string; t: TaskView; git: TaskGit | null; lastRun?: Subtask['runs'][number] }) {
  const tk = t.ticket
  const navigate = useNavigate()
  const [error, setError] = useState('')
  const by = t.linkedFrom.filter((l) => l.kind === 'replaces')
  return (
    <Box title="Outcome" right={tk.stage === 'done' ? <span className="text-ok">✓ {git?.mergedAs ? 'Merged' : 'Done'}</span> : <span className="text-bad">{tk.closedReason}</span>}>
      {tk.stage === 'done' ? (
        <p className="text-sm">
          Done {when(tk.stageAt)}
          {git?.mergedAs && (
            <>
              , merged into main as <span className="font-mono">{git.mergedAs.slice(0, 7)}</span>
            </>
          )}
          .
          {!t.linkedFrom.some((l) => l.kind === 'reverts') && (
            <>
              {' '}
              <button
                className={button + ' ml-2'}
                onClick={() =>
                  window.confirm(`Revert ${tk.key}? A new ticket takes its change out of main, through the project's checks.`) &&
                  send<{ key: string }>('POST', `/api/projects/${encodeURIComponent(project)}/tasks/${tk.key}/revert`).then(
                    (r) => r && navigate({ to: '/p/$project/tickets/$key', params: { project, key: r.key } }),
                    (e: Error) => setError(e.message),
                  )
                }
              >
                Revert
              </button>
              {error && <span className="ml-2 text-bad">{error}</span>}
            </>
          )}
        </p>
      ) : tk.closedReason === 'cancelled' ? (
        <div data-cancelled className="text-sm">
          <p>
            Cancelled {when(tk.stageAt)}, as it could not get through: <span data-free-text>{tk.analysis.reason}</span>
          </p>
          {by.length === 0 ? (
            <p className="mt-1 text-text-dim">Nothing replaces it.</p>
          ) : (
            <p className="mt-1">
              Replaced by{' '}
              {by.map((l, i) => (
                <span key={l.to}>
                  {i > 0 && ', '}
                  <Link to="/p/$project/tickets/$key" params={{ project, key: l.to }} className="font-mono text-accent">
                    {l.to}
                  </Link>
                </span>
              ))}
              .
            </p>
          )}
        </div>
      ) : (
        <p className="text-sm">
          Closed {when(tk.stageAt)}: {tk.closedReason}.
        </p>
      )}
      {lastRun && (
        <p className="mt-1 text-sm text-text-dim">
          Its last run ended {when(lastRun.endedAt)}: {reasons[lastRun.stopReason ?? '']?.text ?? lastRun.stopReason ?? lastRun.state}.
        </p>
      )}
    </Box>
  )
}

export function GitPart({ project, t, git }: { project: string; t: TaskView; git: TaskGit | null }) {
  const [explorer, setExplorer] = useState<{ path: string | null; mode: 'diff' | 'file' } | null>(null)
  const refName = git?.mergedAs ?? (git?.pushed && t.ticket.branch ? t.ticket.branch : 'main')
  return (
    <Box title="Git" part="diff">
      <dl className="grid grid-cols-[5.5rem_minmax(0,1fr)] gap-x-2 gap-y-1 text-sm break-words [overflow-wrap:anywhere]">
        <dt className="text-text-dim">Branch</dt>
        <dd className="min-w-0 font-mono text-xs break-all">
          {t.ticket.branch ?? 'not cut yet: Crew cuts it when the ticket enters In progress'}
          {' · '}
          <button type="button" className="font-sans text-xs text-accent underline" onClick={() => setExplorer({ path: null, mode: 'file' })}>
            Browse files
          </button>
        </dd>
        <dt className="text-text-dim">Based on</dt>
        <dd className="min-w-0 font-mono text-xs">main</dd>
        <dt className="text-text-dim">Merge</dt>
        <dd className="min-w-0 text-sm">
          {git?.mergedAs ? (
            <Link to="/p/$project/git" params={{ project }} search={{ commit: git.mergedAs }} className="font-mono text-xs text-accent">
              {git.mergedAs.slice(0, 7)}
            </Link>
          ) : (
            'a merge commit, after the checks on top of main.'
          )}
        </dd>
        <dt className="text-text-dim">Pull request</dt>
        <dd className="text-sm" data-pull>
          {git?.pull ? (
            <>
              {git.pull.url && git.pull.number ? (
                <a href={git.pull.url} target="_blank" rel="noreferrer" className="text-accent">
                  #{git.pull.number}
                </a>
              ) : (
                'being opened'
              )}{' '}
              · {git.pull.state}
              {git.pull.held && <span className="block text-xs text-amber-text">its newest head waits for your yes before it is pushed</span>}
            </>
          ) : (
            <span className="text-text-dim">none: one is opened when the ticket leaves In progress, with a GitHub remote and gh signed in</span>
          )}
        </dd>
      </dl>
      <h4 className="mt-3 mb-1 text-xs tracking-wide text-text-dim uppercase">Commits, by the subtask that made them</h4>
      {git && git.groups.length === 0 && git.other.length === 0 && <p className="text-sm text-text-dim">No commit yet.</p>}
      <ul className="space-y-2 text-sm">
        {git?.groups.map((g, i) => (
          <li key={i}>
            <p className="font-semibold">
              {g.subtask ? `${g.subtask} ${roleName(g.role)}` : g.who}
              {g.from && (
                <Link to="/p/$project/git" params={{ project }} search={{ from: g.from, to: g.to }} className="ml-2 text-xs font-normal text-accent">
                  its diff
                </Link>
              )}
            </p>
            {g.commits.map((c) => (
              <p key={c.id}>
                <CommitLine c={c} />
              </p>
            ))}
          </li>
        ))}
        {git && git.other.length > 0 && (
          <li>
            <p className="font-semibold">Not recorded by a run</p>
            {git.other.map((c) => (
              <p key={c.id}>
                <CommitLine c={c} />
              </p>
            ))}
          </li>
        )}
      </ul>
      <OmittedCommits n={git?.omitted} />
      <h4 className="mt-3 mb-1 text-xs tracking-wide text-text-dim uppercase">Diff against main</h4>
      {git && git.files.length === 0 && <p className="text-sm text-text-dim">Nothing changed yet.</p>}
      <ul className="space-y-1">
        {listed(git?.files ?? []).map((f) => (
          <li key={f.path}>
            <button
              type="button"
              className="flex w-full justify-between rounded border border-border px-2 py-1 font-mono text-xs transition-colors hover:border-accent hover:bg-accent-soft/40"
              onClick={() => setExplorer({ path: f.path, mode: 'diff' })}
            >
              <span className="truncate" data-free-text>
                {f.path}
              </span>
              <span>
                <span className="text-ok">+{f.added}</span> <span className="text-bad">−{f.removed}</span>
              </span>
            </button>
          </li>
        ))}
      </ul>
      {git && <Omitted files={git.files} />}
      {explorer && (
        <FileExplorerModal project={project} refName={refName} files={git?.files ?? []} initialPath={explorer.path} initialMode={explorer.mode} onClose={() => setExplorer(null)} />
      )}
    </Box>
  )
}
