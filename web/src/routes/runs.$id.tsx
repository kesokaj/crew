import { createFileRoute, Link, useNavigate } from '@tanstack/react-router'
import { useCallback, useEffect, useState } from 'react'
import { getJSON, send, type Commit, type FileDiff, type RunDetail, type RunOptions, type Subtask } from '../lib/api'
import { usePoll } from '../lib/poll'
import { button, Card, Rows, when } from '../components/ui'
import { networkText, roleName } from '../lib/words'
import { PromptView } from '../components/prompt'
import { Diff, listed, Omitted } from '../components/diff'
import { CLIModel, CommitLine, Conversation, duration, reasons, StateDot, stateText, waitText } from '../components/runs'
import { Trajectory } from '../components/trajectory'

export const Route = createFileRoute('/runs/$id')({ component: Run })

const wayText: Record<string, string> = {
  new: 'New.',
  same: 'Continued in the same microVM.',
  files: "Continued in a new microVM, with the CLI's own files put back from the database.",
  rebuilt: "Rebuilt from Crew's own copy of the turns: the last turns in full go in front of the prompt.",
}

// A run (design 8.1, 8.3, 12.4, 12.10, run.png): how the agent was started, what its sandbox holds
// and may touch, the conversation it belongs to, the prompt, and its trajectory, live.
function Run() {
  const { id } = Route.useParams()
  const [run, setRun] = useState<RunDetail | null>(null)
  const [error, setError] = useState('')
  const load = useCallback(() => {
    getJSON<RunDetail>(`/api/runs/${id}`).then(
      (r) => {
        setRun(r)
        setError('')
      },
      (e: Error) => setError(e.message),
    )
  }, [id])
  useEffect(() => {
    load()
    const t = setInterval(load, 3000)
    return () => clearInterval(t)
  }, [load])

  if (!run) return <div className="p-6 text-text-dim">{error || 'Loading…'}</div>
  const live = run.state !== 'ended'
  const last = run.runs[run.runs.length - 1]
  return (
    <div className="mx-auto max-w-6xl p-6">
      <p className="font-mono text-xs text-text-dim">
        {run.project && run.ticket ? (
          <>
            <Link to="/p/$project/board" params={{ project: run.project }} className="text-accent">
              {run.project}
            </Link>{' '}
            /{' '}
            <Link to="/p/$project/tickets/$key" params={{ project: run.project, key: run.ticket }} className="text-accent">
              {run.ticket}
            </Link>{' '}
            / {run.ticket}.{run.subtaskNumber}
          </>
        ) : (
          <>
            <Link to="/runs" className="text-accent">
              {run.project || 'runs'}
            </Link>{' '}
            / subtask {run.subtask} / run {run.subtask}.{run.number}
          </>
        )}
      </p>
      <div className="flex flex-wrap items-start gap-3">
        <div>
          <h1 className="text-3xl font-bold tracking-tight">
            <span data-free-text>{run.title}</span>
          </h1>
          <p className="text-text-dim">
            {roleName(run.role)} on <CLIModel r={run} />
            {run.ticket ? ` · ticket ${run.ticket}` : ' · a subtask with no ticket'}
          </p>
        </div>
        {run.project && run.ticket && (
          <Link to="/p/$project/tickets/$key" params={{ project: run.project, key: run.ticket }} className={button + ' ml-auto'}>
            Open the ticket
          </Link>
        )}
        <Actions run={run} onDone={load} />
      </div>
      {error && <p className="mt-4 rounded-md border border-red-400 p-3 text-sm">{error}</p>}
      {(run.stopReason || run.stopping) && (
        <p className="mt-4 rounded-md border border-amber-500 bg-bg-raised p-3 text-sm">
          <b>{stateText(run)}{run.stopDetail ? ': ' : '.'}</b>
          {run.stopDetail ? `${run.stopDetail}.` : ''}{' '}
          {run.stopReason && run.stopReason !== 'ok' && <span className="text-text-dim">{reasons[run.stopReason]?.follows}</span>}
          {last && last.id === run.id && run.waitFor && <span className="text-text-dim"> The subtask is {waitText(run.waitFor)}.</span>}
          {last && last.id !== run.id && (
            <>
              {' '}
              It continued in{' '}
              <Link to="/runs/$id" params={{ id: last.id }} className="text-accent">
                run {last.subtask}.{last.number}
              </Link>
              .
            </>
          )}
        </p>
      )}
      {run.warnings.length > 0 && (
        <ul className="mt-3 space-y-1 text-sm">
          {run.warnings.map((w, i) => (
            <li key={i} className="rounded-md border border-amber-500 px-3 py-1">
              Warning at {when(w.at)}: {w.text}
            </li>
          ))}
        </ul>
      )}

      <div className="mt-6 grid gap-4 md:grid-cols-3">
        <Card title="Subtask">
          <Rows
            narrow
            rows={[
              ['State', <span key="s" className="inline-flex items-center gap-2"><StateDot r={run} />{stateText(run)}</span>],
              ['Agent', roleName(run.role)],
              ['CLI', <span key="c" className="font-mono">{run.cli} {run.cliVersion}</span>],
              ['Model', <span key="m"><span className="font-mono">{run.model || 'the CLI\'s default'}{run.effort ? ` · ${run.effort}` : ''}</span>{run.modelFrom && <span className="block text-xs text-text-dim">set by {run.modelFrom}</span>}</span>],
              ['Crew version', <span key="v" className="font-mono">{run.crewVersion}</span>],
              ['Run', <span key="r" className="font-mono text-xs">{run.id}</span>],
              ['Started', when(run.startedAt)],
              ['Time', duration(run.startedAt, run.endedAt)],
              ['Turns', `${run.turn} turn${run.turn === 1 ? '' : 's'}, ${run.toolSteps} of ${run.maxTurns} tool steps`],
              ['Tokens', `${run.tokensIn.toLocaleString()} in, ${run.tokensOut.toLocaleString()} out${run.tokensCached ? `, ${run.tokensCached.toLocaleString()} read from the cache` : ''}`],
            ]}
          />
          <div className="mt-3 h-1.5 rounded bg-border">
            <div className="h-1.5 rounded bg-accent" style={{ width: `${Math.min(100, (100 * run.toolSteps) / Math.max(1, run.maxTurns))}%` }} />
          </div>
        </Card>
        <Card title="Sandbox">
          <Rows
            narrow
            rows={[
              ['Id', <span key="i" className="font-mono text-xs">{run.sandbox ?? 'not started'}</span>],
              ['Image', <span key="im" className="font-mono text-xs">{run.image}</span>],
              ['Isolation', 'Kata microVM on Cloud Hypervisor, with its own kernel'],
              ['Size', sizeText(run)],
              ['Network', networkText(run.mode)],
              ['Shared with other runs', 'Nothing'],
              ['Removed', run.endedAt ? when(run.endedAt) : live ? 'when the run ends' : ''],
            ]}
          />
        </Card>
        <Card title="What this run holds and may touch">
          <Rows
            narrow
            rows={[
              ['Branch', <span key="b" className="font-mono text-xs">{run.branch}</span>],
              ['Git', run.push ? 'May push to this branch and to its own work in progress only, through Crew\'s git endpoint' : 'Reads this branch and main, and pushes nothing (6.1)'],
              ['Work in progress', <span key="w" className="font-mono text-xs">{run.wip || 'none kept yet'}</span>],
              ['Starts from', <span key="f" className="font-mono text-xs">{run.base}</span>],
              ['CLI login', `${run.cli} only`],
              ['Other credentials', 'The git identity, and a GitHub token that can only read, for one hour. The SSH key and gh stay with the server.'],
              ['Crew MCP', run.tools ? `${run.project || 'its own'} only, with a token for this run` : 'none: a call with no tools'],
            ]}
          />
        </Card>
      </div>

      <div className="mt-4">
        <Card title="How the agent was started">
          <pre className="overflow-x-auto rounded bg-bg p-3 font-mono text-xs whitespace-pre-wrap">
            crew-run turn → {run.command.map((a) => (a === '' || /\s/.test(a) ? JSON.stringify(a) : a)).join(' ')} {'< prompt.txt'}
          </pre>
          <p className="mt-2 text-sm text-text-dim">
            The prompt goes in on standard input, which is then closed. Every line the CLI prints is written to a spool, events.jsonl,
            in the run's home. Crew reads it from there and stores each event in the database, so a restart or an update of Crew does
            not lose the run.
          </p>
        </Card>
      </div>

      <div className="mt-4 grid gap-4 md:grid-cols-3">
        <Card title="The conversation of this run">
          <Rows
            narrow
            rows={[
              ['Id', <span key="i" className="font-mono text-xs">{run.conversation}</span>],
              ['At the CLI', <span key="c" className="font-mono text-xs">{run.cliConversation || 'not named yet'}</span>],
              ['Thread', wayText[run.way] ?? run.way],
              ...(run.continues
                ? ([['Continues', <Link key="k" to="/runs/$id" params={{ id: run.continues }} className="font-mono text-xs text-accent">{run.continues}</Link>]] as [string, React.ReactNode][])
                : []),
              ['If that fails', "Rebuilt from Crew's own copy: the last turns in full go in front of the prompt."],
            ]}
          />
        </Card>
        <Card title="Where it is kept">
          <Rows
            narrow
            rows={[
              ['All in the database', 'nothing of a run is kept in files'],
              ['The turns', `${run.turns.length}: who spoke, the text, and the tool calls in short. For good.`],
              ['The trajectory', 'every event the CLI printed, complete, with secrets removed. For good.'],
              [
                "The CLI's own files",
                run.cliFiles.length === 0 ? (
                  'none kept yet'
                ) : (
                  <ul key="f" className="space-y-1">
                    {run.cliFiles.map((f) => (
                      <li key={f.path}>
                        <span className="font-mono text-xs break-all">~/{f.path}</span>{' '}
                        <span className="text-text-dim">kept until {when(f.keepUntil)}</span>
                      </li>
                    ))}
                  </ul>
                ),
              ],
            ]}
          />
        </Card>
        <Card title="Trajectory of this run">
          <Rows
            narrow
            rows={[
              ['Events', `${run.events}${live ? ' so far' : ''}`],
              ['Turns', String(run.turn)],
              ['Tokens', (run.tokensIn + run.tokensOut).toLocaleString()],
              ['Last event', run.lastEventAt ? when(run.lastEventAt) : 'none yet'],
            ]}
          />
        </Card>
      </div>

      <h2 className="mt-8 mb-2 text-lg font-semibold">
        The prompt{' '}
        <span className="text-sm font-normal text-text-dim">
          saved with the run{run.parts.length ? `, in ${run.parts.length} parts` : ''}
          {run.promptSha256 ? `, sha256 ${run.promptSha256.slice(0, 12)}` : ''}
        </span>
      </h2>
      {run.project && run.ticket && <RunSteps project={run.project} ticket={run.ticket} subtask={`${run.ticket}.${run.subtaskNumber}`} />}
      <PromptView prompt={run.prompt} parts={run.parts} version={run.crewVersion} />
      <RunPushes id={run.id} />

      <h2 className="mt-8 text-lg font-semibold">The conversation</h2>
      <Conversation turns={run.turns} />

      {run.runs.length > 1 && (
        <>
          <h2 className="mt-8 text-lg font-semibold">The runs of this subtask</h2>
          <ul className="mt-2 space-y-1 text-sm">
            {run.runs.map((r) => (
              <li key={r.id} className="flex items-center gap-2">
                <StateDot r={r} />
                <Link to="/runs/$id" params={{ id: r.id }} className={r.id === run.id ? 'font-medium' : 'text-accent'}>
                  run {r.subtask}.{r.number}
                </Link>
                <CLIModel r={r} />
                <span className="text-text-dim">{wayText[r.way]}</span>
                <span className="text-text-dim">{stateText(r)}</span>
              </li>
            ))}
          </ul>
        </>
      )}

      <Trajectory id={run.id} live={live} />
    </div>
  )
}

// Stop, Finish, and Continue (11.2, 12.10).
function Actions({ run, onDone }: { run: RunDetail; onDone: () => void }) {
  const [text, setText] = useState('')
  const [way, setWay] = useState('')
  const [cli, setCli] = useState('')
  const [model, setModel] = useState('')
  const [opts, setOpts] = useState<RunOptions | null>(null)
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState('')
  const navigate = useNavigate()
  useEffect(() => {
    getJSON<RunOptions>('/api/runs/options').then(setOpts, () => setOpts(null))
  }, [])
  const last = run.runs[run.runs.length - 1]
  const isLast = !last || last.id === run.id
  const act = (p: Promise<unknown>) => {
    setBusy(true)
    setError('')
    p.then(
      (r) => {
        setBusy(false)
        setText('')
        onDone()
        const next = (r as { run?: string } | null)?.run
        if (next && next !== run.id) void navigate({ to: '/runs/$id', params: { id: next } })
      },
      (e: Error) => {
        setBusy(false)
        setError(e.message)
      },
    )
  }
  const working = run.state === 'starting' || run.state === 'working'
  return (
    <div className="ml-auto flex max-w-xl flex-col items-end gap-2">
      <div className="flex gap-2">
        {working && (
          <button disabled={busy || !!run.stopping} onClick={() => act(send('POST', `/api/runs/${run.id}/stop`))} className="rounded-md border border-red-400 px-3 py-1 text-sm disabled:opacity-50">
            {run.stopping ? 'Stopping…' : 'Stop'}
          </button>
        )}
        {run.state === 'idle' && (
          <button disabled={busy} onClick={() => act(send('POST', `/api/runs/${run.id}/finish`))} className="rounded-md border border-border px-3 py-1 text-sm">
            Finish
          </button>
        )}
      </div>
      {isLast && !working && run.subtaskState !== 'done' && (
        <div className="w-full rounded-md border border-border bg-bg-raised p-3">
          <textarea
            aria-label="Next turn"
            value={text}
            onChange={(e) => setText(e.target.value)}
            rows={2}
            placeholder={run.state === 'idle' ? 'The next turn, in the same microVM' : "The next turn; empty sends Crew's message about the stop"}
            className="w-full rounded-md border border-border bg-bg px-2 py-1 text-sm"
          />
          <div className="mt-2 flex flex-wrap items-center gap-2 text-sm">
            <select aria-label="Way" value={way} onChange={(e) => setWay(e.target.value)} className="rounded-md border border-border bg-bg px-2 py-1">
              <option value="">the first way that is possible</option>
              <option value="same">the same microVM</option>
              <option value="files">a new microVM, with the CLI's files</option>
              <option value="rebuilt">rebuilt from the turns</option>
            </select>
            <select aria-label="CLI" value={cli} onChange={(e) => setCli(e.target.value)} className="rounded-md border border-border bg-bg px-2 py-1">
              <option value="">{run.cli}</option>
              {opts?.clis
                .filter((c) => c.cli !== run.cli)
                .map((c) => (
                  <option key={c.cli} value={c.cli}>
                    {c.cli} (rebuilds)
                  </option>
                ))}
            </select>
            <input aria-label="Model" value={model} onChange={(e) => setModel(e.target.value)} placeholder={run.model || 'model'} className="w-40 rounded-md border border-border bg-bg px-2 py-1 font-mono" />
            <button
              disabled={busy || (run.state === 'idle' && !text.trim() && way === '')}
              onClick={() => act(send('POST', `/api/subtasks/${run.subtask}/continue`, { text, way, cli, model }))}
              className="ml-auto rounded-md border border-accent px-3 py-1 font-medium text-accent disabled:opacity-50"
            >
              Continue
            </button>
          </div>
        </div>
      )}
      {error && <p className="rounded-md border border-red-400 p-2 text-sm">{error}</p>}
    </div>
  )
}

type RunGit = { ref: string; from: string; to: string; commits: Commit[]; files: FileDiff[] }

// What this run pushed, and the diff of only this run (6.7).
function RunPushes({ id }: { id: string }) {
  const { data } = usePoll<RunGit>(`/api/runs/${id}/git`, 15000)
  const [open, setOpen] = useState<string | null>(null)
  if (!data || data.commits.length === 0) return null
  return (
    <>
      <h2 className="mt-8 mb-2 text-lg font-semibold">
        The commits this run pushed <span className="text-sm font-normal text-text-dim">to {data.ref.replace('refs/heads/', '')}</span>
      </h2>
      <ul className="space-y-1 text-sm">
        {data.commits.map((c) => (
          <li key={c.id}>
            <CommitLine c={c} />
          </li>
        ))}
      </ul>
      <h3 className="mt-4 mb-1 text-sm font-semibold">The diff of only this run</h3>
      <ul className="space-y-1">
        {listed(data.files).map((f) => (
          <li key={f.path}>
            <button className="flex w-full justify-between rounded border border-border bg-bg-raised px-2 py-1 font-mono text-xs" onClick={() => setOpen(open === f.path ? null : f.path)}>
              <span data-free-text>{f.path}</span>
              <span>
                <span className="text-ok">+{f.added}</span> <span className="text-bad">−{f.removed}</span>
              </span>
            </button>
            {open === f.path && <Diff file={f} />}
          </li>
        ))}
      </ul>
      <Omitted files={data.files} />
    </>
  )
}

// The steps the agent set for itself (7.11), ticked as it goes.
function RunSteps({ project, ticket, subtask }: { project: string; ticket: string; subtask: string }) {
  const { data } = usePoll<{ subtasks: Subtask[] }>(`/api/projects/${encodeURIComponent(project)}/tasks/${encodeURIComponent(ticket)}/subtasks`, 5000)
  const steps = data?.subtasks.find((s) => s.id === subtask)?.steps ?? []
  if (steps.length === 0) return null
  return (
    <div className="mb-3 rounded-md border border-border bg-bg-raised p-3">
      <h3 className="mb-1 text-xs tracking-wide text-text-dim uppercase">Steps the agent set for itself</h3>
      <ol className="space-y-0.5 text-sm">
        {steps.map((x, i) => (
          <li key={i} className={x.done ? 'text-text-dim' : ''}>
            {x.done ? '✓' : '·'} <span data-free-text>{x.text}</span>
          </li>
        ))}
      </ol>
    </div>
  )
}

// sizeText is a run's sandbox size as run.png shows it: "4 CPU, 6 GB · its own Docker daemon", named.
function sizeText(run: RunDetail): string {
  if (!run.size) return 'not recorded'
  const name = run.size.charAt(0).toUpperCase() + run.size.slice(1)
  if (!run.sizing) return name
  const z = run.sizing
  return `${name}: ${z.cpus} CPU, ${Math.round(z.memMB / 1024)} GB · ${z.docker ? 'its own Docker daemon' : 'no Docker daemon'}`
}
