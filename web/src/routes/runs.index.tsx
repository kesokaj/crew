import { createFileRoute, Link, useNavigate } from '@tanstack/react-router'
import { useCallback, useEffect, useState } from 'react'
import { getJSON, send, type RunList, type RunOptions, type RunView } from '../lib/api'
import { Card } from '../components/ui'
import { CLIModel, duration, reasons, StateDot, stateText, waitText } from '../components/runs'
import { when } from '../components/ui'
import { tokenCount } from '../lib/tokens'

export const Route = createFileRoute('/runs/')({ component: Runs })

// Runs (design 8.1, 20.1, agents.png): every run in its own microVM, what waits and why, and what
// finished. A run started here is on the fixture project, with no ticket; a ticket's runs come with M6.
function Runs() {
  const [list, setList] = useState<RunList | null>(null)
  const [error, setError] = useState('')
  const load = useCallback(() => {
    getJSON<RunList>('/api/runs').then(
      (l) => {
        setList(l)
        setError('')
      },
      (e: Error) => setError(e.message),
    )
  }, [])
  useEffect(() => {
    load()
    const t = setInterval(load, 3000)
    return () => clearInterval(t)
  }, [load])

  return (
    <div className="mx-auto max-w-6xl p-6">
      <p className="font-mono text-xs text-text-dim">Crew / fixture</p>
      <h1 className="text-2xl font-semibold">Runs</h1>
      <p className="mt-1 max-w-3xl text-text-dim">
        Every run has its own microVM: its own kernel, files, clone, and network. It holds the login of one agent CLI and nothing
        else, and it is removed when the run ends. Every line the CLI prints is kept in Crew's database as it happens.
      </p>
      {error && <p className="mt-4 rounded-md border border-red-400 p-3 text-sm">{error}</p>}

      <h2 className="mt-8 text-lg font-semibold">
        Running now <span className="ml-1 text-sm font-normal text-text-dim">{list?.running.length ?? ''}</span>
      </h2>
      <RunTable runs={list?.running ?? []} live empty="Nothing runs." />

      <h2 className="mt-8 text-lg font-semibold">Waiting</h2>
      {list && list.waiting.length === 0 && <p className="mt-2 text-sm text-text-dim">Nothing waits.</p>}
      <ul className="mt-2 space-y-2">
        {list?.waiting.map((r) => (
          <li key={r.id} className="flex flex-wrap items-center gap-3 rounded-md border border-border bg-bg-raised px-3 py-2 text-sm">
            <StateDot r={r} />
            <Link to="/runs/$id" params={{ id: r.id }} className="font-medium text-accent">
              {r.subtask}.{r.number} <span data-free-text>{r.title}</span>
            </Link>
            <span className="text-text-dim">
              {reasons[r.stopReason ?? '']?.text ?? r.stopReason}: {waitText(r.waitFor)}
            </span>
            <span className="ml-auto text-xs text-text-dim">since {when(r.endedAt)}</span>
          </li>
        ))}
      </ul>

      <StartRun onStarted={load} />

      <h2 className="mt-8 text-lg font-semibold">Recently finished</h2>
      <RunTable runs={list?.finished ?? []} empty="No run has ended yet." />
    </div>
  )
}

function RunTable({ runs, live, empty }: { runs: RunView[]; live?: boolean; empty: string }) {
  if (runs.length === 0) return <p className="mt-2 text-sm text-text-dim">{empty}</p>
  return (
    <div className="mt-2 overflow-x-auto">
      <table className="w-full text-left text-sm">
        <thead className="text-xs tracking-wide text-text-dim uppercase">
          <tr className="border-b border-border">
            <th className="py-2 pr-3 font-normal">Agent</th>
            <th className="py-2 pr-3 font-normal">CLI and model</th>
            <th className="py-2 pr-3 font-normal">Working on</th>
            <th className="py-2 pr-3 font-normal">Sandbox</th>
            <th className="py-2 pr-3 font-normal">{live ? 'Time' : 'Ended'}</th>
            <th className="py-2 pr-3 font-normal">{live ? 'Step' : 'Result'}</th>
            <th className="py-2 font-normal">Tokens</th>
          </tr>
        </thead>
        <tbody>
          {runs.map((r) => (
            <tr key={r.id} className="border-b border-border align-top">
              <td className="py-2 pr-3">Builder</td>
              <td className="py-2 pr-3">
                <CLIModel r={r} />
              </td>
              <td className="py-2 pr-3">
                <Link to="/runs/$id" params={{ id: r.id }} className="text-accent">
                  <span className="font-mono text-xs">{r.subtask}.{r.number}</span> <span data-free-text>{r.title}</span>
                </Link>
              </td>
              <td className="py-2 pr-3 font-mono text-xs">{r.sandbox ?? ''}</td>
              <td className="py-2 pr-3">{live ? duration(r.startedAt) : when(r.endedAt)}</td>
              <td className="py-2 pr-3">
                <div className="flex items-center gap-2">
                  <StateDot r={r} />
                  <span>{live ? stateText(r) : (reasons[r.stopReason ?? '']?.text ?? r.stopReason)}</span>
                </div>
                {live && r.maxTurns > 0 && (
                  <div className="mt-1 h-1 w-40 rounded bg-border">
                    <div className="h-1 rounded bg-accent" style={{ width: `${Math.min(100, (100 * r.toolSteps) / r.maxTurns)}%` }} />
                  </div>
                )}
                {live && r.lastAction && (
                  <div className="mt-1 max-w-sm truncate text-xs text-text-dim" data-free-text>
                    {r.lastAction}
                  </div>
                )}
              </td>
              <td className="py-2 tabular-nums" title={`${r.tokensIn.toLocaleString()} in, ${r.tokensOut.toLocaleString()} out`}>
                {tokenCount(r.tokensIn + r.tokensOut)}
              </td>
            </tr>
          ))}
        </tbody>
      </table>
    </div>
  )
}

// The form that starts a run on the fixture project. The models are the ones each CLI listed itself
// (12.3); a CLI whose list is not read yet takes a name typed in.
function StartRun({ onStarted }: { onStarted: () => void }) {
  const [opts, setOpts] = useState<RunOptions | null>(null)
  const [cli, setCli] = useState('')
  const [model, setModel] = useState('')
  const [effort, setEffort] = useState('')
  const [prompt, setPrompt] = useState('')
  const [title, setTitle] = useState('')
  const [minutes, setMinutes] = useState(30)
  const [maxTurns, setMaxTurns] = useState(100)
  const [withoutLogin, setWithoutLogin] = useState(false)
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState('')
  const navigate = useNavigate()
  useEffect(() => {
    getJSON<RunOptions>('/api/runs/options').then(
      (o) => {
        setOpts(o)
        setCli((c) => c || (o.clis.find((x) => x.signedIn)?.cli ?? o.clis[0]?.cli ?? ''))
      },
      (e: Error) => setError(e.message),
    )
  }, [])
  const chosen = opts?.clis.find((c) => c.cli === cli)
  const start = () => {
    setBusy(true)
    setError('')
    send<{ id: string }>('POST', '/api/runs', {
      cli,
      model,
      effort,
      title,
      prompt,
      timeoutSeconds: minutes * 60,
      maxTurns,
      withoutLogin,
    }).then(
      (r) => {
        onStarted()
        if (r) void navigate({ to: '/runs/$id', params: { id: r.id } })
      },
      (e: Error) => {
        setError(e.message)
        setBusy(false)
      },
    )
  }
  return (
    <div className="mt-8">
      <Card title="Start a run on the fixture project">
        <div className="grid gap-3 md:grid-cols-[12rem_1fr]">
          <label className="text-sm text-text-dim" htmlFor="run-cli">
            Agent CLI
          </label>
          <div className="flex flex-wrap gap-2">
            {opts?.clis.map((c) => (
              <button
                key={c.cli}
                type="button"
                onClick={() => {
                  setCli(c.cli)
                  setModel('')
                }}
                className={'rounded-md border px-3 py-1 text-sm ' + (cli === c.cli ? 'border-accent font-medium' : 'border-border')}
              >
                {c.cli} {c.signedIn ? '' : '(not signed in)'}
              </button>
            ))}
          </div>
          <label className="text-sm text-text-dim" htmlFor="run-model">
            Model
          </label>
          <div className="flex flex-wrap gap-2">
            <input
              id="run-model"
              list="run-models"
              value={model}
              onChange={(e) => setModel(e.target.value)}
              placeholder={chosen?.models.length ? 'a model the CLI lists' : 'a model'}
              className="w-72 rounded-md border border-border bg-bg px-2 py-1 font-mono text-sm"
            />
            <datalist id="run-models">
              {chosen?.models.map((m) => (
                <option key={m} value={m} />
              ))}
            </datalist>
            <input
              aria-label="Effort"
              value={effort}
              onChange={(e) => setEffort(e.target.value)}
              placeholder="effort, if any"
              className="w-56 rounded-md border border-border bg-bg px-2 py-1 font-mono text-sm"
            />
          </div>
          <label className="text-sm text-text-dim" htmlFor="run-title">
            Title
          </label>
          <input
            id="run-title"
            value={title}
            onChange={(e) => setTitle(e.target.value)}
            placeholder="the first line of the prompt"
            className="rounded-md border border-border bg-bg px-2 py-1 text-sm"
          />
          <label className="text-sm text-text-dim" htmlFor="run-prompt">
            Prompt
          </label>
          <textarea
            id="run-prompt"
            value={prompt}
            onChange={(e) => setPrompt(e.target.value)}
            rows={4}
            placeholder="What the agent does in the fixture's repository. It works on a branch of its own, and commits and pushes as it goes."
            className="rounded-md border border-border bg-bg px-2 py-1 text-sm"
          />
          <span className="text-sm text-text-dim">Limits</span>
          <div className="flex flex-wrap items-center gap-2 text-sm">
            <input type="number" min={1} value={minutes} onChange={(e) => setMinutes(Number(e.target.value))} className="w-20 rounded-md border border-border bg-bg px-2 py-1" aria-label="Minutes" />
            minutes,
            <input type="number" min={1} value={maxTurns} onChange={(e) => setMaxTurns(Number(e.target.value))} className="w-20 rounded-md border border-border bg-bg px-2 py-1" aria-label="Tool steps" />
            tool steps
          </div>
          <span className="text-sm text-text-dim">To show a stop</span>
          <label className="flex items-center gap-2 text-sm">
            <input type="checkbox" checked={withoutLogin} onChange={(e) => setWithoutLogin(e.target.checked)} />
            Start without the CLI's login: the run stops for a sign-in, and continues by itself after the next check
          </label>
        </div>
        {error && <p className="mt-3 rounded-md border border-red-400 p-2 text-sm">{error}</p>}
        <div className="mt-3">
          <button
            disabled={busy || !cli || !prompt.trim()}
            onClick={start}
            className="rounded-md border border-accent px-4 py-1.5 text-sm font-medium text-accent disabled:opacity-50"
          >
            {busy ? 'Starting…' : 'Start the run'}
          </button>
          <span className="ml-3 text-xs text-text-dim">A run uses your subscription of its CLI.</span>
        </div>
      </Card>
    </div>
  )
}
