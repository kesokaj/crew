import { createFileRoute, Link } from '@tanstack/react-router'
import { useEffect, useRef, useState } from 'react'
import { ApiError, getJSON, send, type RunDetail } from '../lib/api'
import { roleName } from '../lib/words'
import { button, Failed, mainButton, PageHead, when } from '../components/ui'
import { LiveTerminal } from '../components/LiveTerminal'
import { FreshGate } from '../components/freshgate'

type Search = { run?: string; ticket?: string }

export const Route = createFileRoute('/p/$project/terminal')({
  validateSearch: (s: Record<string, unknown>): Search => ({
    run: typeof s.run === 'string' ? s.run : undefined,
    ticket: typeof s.ticket === 'string' ? s.ticket : undefined,
  }),
  component: Terminal,
})

type ProjectTerminal = {
  project: string
  sandbox: string
  mode: 'diagnostics' | 'edit'
  branch: string
  openedAt: string
  inputAt: string
  logins: string[]
  network: string
}

// The terminal (design 13.11, 20.1, term.png): the project terminal, a microVM of its own with the
// repository and every branch, read-only until you switch on edit mode; or, from a ticket where an
// agent works, the inside of that run's microVM.
function Terminal() {
  const { project } = Route.useParams()
  const { run, ticket } = Route.useSearch()
  return run ? <InsideRun project={project} run={run} /> : <ProjectTerm project={project} ticket={ticket} />
}

function Crumbs({ project }: { project: string }) {
  return (
    <>
      <Link to="/p/$project/board" params={{ project }} className="text-accent">
        {project}
      </Link>{' '}
      / Terminal
    </>
  )
}

function ProjectTerm({ project, ticket }: { project: string; ticket?: string }) {
  const base = `/api/projects/${encodeURIComponent(project)}/terminal`
  const [t, setT] = useState<ProjectTerminal | null>(null)
  const [state, setState] = useState<'loading' | 'closed' | 'starting' | 'open'>('loading')
  const [error, setError] = useState('')
  useEffect(() => {
    getJSON<ProjectTerminal>(base).then(
      (x) => {
        setT(x)
        setState('open')
      },
      (e: ApiError) => (e.code === 404 ? setState('closed') : setError(e.message)),
    )
  }, [base])
  // A ticket where nothing runs opens the project terminal on that ticket's branch, once.
  const switched = useRef('')
  useEffect(() => {
    if (!ticket || switched.current === ticket || (state !== 'closed' && state !== 'open')) return
    switched.current = ticket
    open()
    // open reads the state it is called in, which is the one this effect saw.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [ticket, state])
  const open = () => {
    setState('starting')
    setError('')
    send<ProjectTerminal>('POST', base, ticket ? { ticket } : {}).then(
      (x) => {
        setT(x)
        setState('open')
      },
      (e: Error) => {
        setError(e.message)
        setState(t ? 'open' : 'closed')
      },
    )
  }
  const mode = (m: 'edit' | 'diagnostics') => send<ProjectTerminal>('PUT', base + '/mode', { mode: m }).then((x) => x && setT(x), (e: Error) => setError(e.message))
  const close = () =>
    send('DELETE', base).then(
      () => {
        setT(null)
        setState('closed')
      },
      (e: Error) => setError(e.message),
    )
  const edit = t?.mode === 'edit'
  return (
    <div className="mx-auto max-w-7xl p-6">
      <PageHead
        crumbs={<Crumbs project={project} />}
        title="Terminal"
        actions={
          <>
            {state === 'open' && (
              <button className={edit ? button : mainButton} onClick={() => void mode(edit ? 'diagnostics' : 'edit')}>
                {edit ? 'Back to diagnostics' : 'Switch to edit mode'}
              </button>
            )}
            {state === 'open' && (
              <button className={button} onClick={() => void close()}>
                Close the terminal
              </button>
            )}
            <Link to="/p/$project/board" params={{ project }} className={button}>
              Back to the board
            </Link>
          </>
        }
      >
        The project terminal: a microVM of its own, with the git repository of {project} cloned as the workspace. Every branch is there.
      </PageHead>
      <Failed error={error} />
      <div className="grid gap-4 lg:grid-cols-[minmax(0,1fr)_360px]">
        <section className="rounded-lg border border-border bg-bg-raised p-4">
          {state === 'open' && t ? (
            <FreshGate what="A terminal">
              <LiveTerminal path={`${base}/ws`} />
            </FreshGate>
          ) : (
            <div className="py-10 text-center">
              <p className="mb-3 text-text-dim">
                {state === 'starting' ? 'Starting the microVM and cloning the repository…' : state === 'loading' ? 'Loading…' : 'No terminal is open for this project.'}
              </p>
              {state === 'closed' && (
                <button className={mainButton} onClick={open}>
                  Open the project terminal
                </button>
              )}
            </div>
          )}
        </section>
        <aside className="space-y-4">
          <section className="rounded-lg border border-border bg-bg-raised p-4 text-sm">
            <h2 className="mb-2 text-xs tracking-wide text-text-dim uppercase">Where you are</h2>
            {t ? (
              <dl className="grid grid-cols-[6rem_1fr] gap-y-1.5" data-where>
                <dt className="text-text-dim">Sandbox</dt>
                <dd className="font-mono text-xs">{t.sandbox}</dd>
                <dt className="text-text-dim">Kind</dt>
                <dd>the project terminal, removed when closed or after 30 minutes without input</dd>
                <dt className="text-text-dim">Mode</dt>
                <dd className={edit ? 'font-semibold text-amber-text' : ''}>{t.mode}</dd>
                <dt className="text-text-dim">Workspace</dt>
                <dd>the project's repository, cloned from Crew's git endpoint, every branch fetched</dd>
                <dt className="text-text-dim">Branch</dt>
                <dd className="font-mono text-xs">{t.branch || '—'}</dd>
                <dt className="text-text-dim">Logins</dt>
                <dd>{t.logins.length ? t.logins.join(', ') : 'none signed in'}, as a run of this project gets them</dd>
                <dt className="text-text-dim">Push</dt>
                <dd>
                  {edit
                    ? 'main (only forward: it becomes the new base), your own branches under you/, and a ticket’s branch while no agent runs on it'
                    : 'Not in diagnostics mode. Switch to edit mode to push.'}
                </dd>
                <dt className="text-text-dim">Open since</dt>
                <dd>{when(t.openedAt)}</dd>
              </dl>
            ) : (
              <p className="text-text-dim">Nothing is open.</p>
            )}
          </section>
          <section className="rounded-lg border border-border bg-bg-raised p-4 text-sm">
            <h2 className="mb-2 text-xs tracking-wide text-text-dim uppercase">What a terminal is for</h2>
            <p>Diagnostics and troubleshooting, when needed. Edit mode is there for when you have to go in and change something yourself. Agents are not driven from here.</p>
            <ul className="mt-2 list-disc space-y-1 pl-5 text-text-dim">
              <li>The project terminal has the whole repository, with every branch.</li>
              <li>On a ticket where an agent is working, the button jumps straight into that sandbox.</li>
              <li>Opening a terminal is recorded, and what is typed and printed is kept for 180 days.</li>
            </ul>
          </section>
        </aside>
      </div>
    </div>
  )
}

// Inside a running run's microVM: the agent's own sandbox, its files and processes (13.11).
function InsideRun({ project, run }: { project: string; run: string }) {
  const [r, setR] = useState<RunDetail | null>(null)
  const [error, setError] = useState('')
  useEffect(() => {
    getJSON<RunDetail>(`/api/runs/${run}`).then(setR, (e: Error) => setError(e.message))
  }, [run])
  const live = r && r.state !== 'ended'
  return (
    <div className="mx-auto max-w-7xl p-6">
      <PageHead
        crumbs={<Crumbs project={project} />}
        title="Inside a running sandbox"
        actions={
          r?.ticket && (
            <Link to="/p/$project/tickets/$key" params={{ project, key: r.ticket }} className={button}>
              Back to {r.ticket}
            </Link>
          )
        }
      >
        You are in the agent's own microVM: the same files and the same processes. You can do what the agent can. Agents stay headless:
        this does not drive the agent.
      </PageHead>
      <Failed error={error} />
      <div className="grid gap-4 lg:grid-cols-[minmax(0,1fr)_360px]">
        <section className="rounded-lg border border-border bg-bg-raised p-4">
          {r && !live && <p className="text-text-dim">The run has ended, and its microVM is gone. Open the project terminal instead.</p>}
          {live && (
            <FreshGate what="A terminal">
              <LiveTerminal path={`/api/projects/${encodeURIComponent(project)}/terminal/ws?run=${run}`} />
            </FreshGate>
          )}
        </section>
        <aside>
          <section className="rounded-lg border border-border bg-bg-raised p-4 text-sm">
            <h2 className="mb-2 text-xs tracking-wide text-text-dim uppercase">Where you are</h2>
            {r && (
              <dl className="grid grid-cols-[6rem_1fr] gap-y-1.5">
                <dt className="text-text-dim">Sandbox</dt>
                <dd className="font-mono text-xs">{r.sandbox}</dd>
                <dt className="text-text-dim">Agent</dt>
                <dd>
                  {roleName(r.role)} on {r.cli} {r.model}
                </dd>
                <dt className="text-text-dim">Subtask</dt>
                <dd>
                  {r.ticket}.{r.subtaskNumber} {r.title}
                </dd>
                <dt className="text-text-dim">Branch</dt>
                <dd className="font-mono text-xs">{r.branch}</dd>
                <dt className="text-text-dim">Kept</dt>
                <dd>Opening it is in the ticket's history, and what is typed and printed is kept for 180 days.</dd>
              </dl>
            )}
          </section>
        </aside>
      </div>
    </div>
  )
}
