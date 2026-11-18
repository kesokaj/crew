import { createFileRoute, Link, Outlet, useNavigate, useRouterState } from '@tanstack/react-router'
import { useState } from 'react'
import { send, type DraftSummary, type Rewrite } from '../lib/api'
import { usePoll } from '../lib/poll'
import { Failed, mainButton, PageHead, when } from '../components/ui'
import { WriteBox } from '../components/writebox'

export const Route = createFileRoute('/plans')({ component: Plans })

// Plans (design 7.2, 20.1, plans.png): a plan is developed with a planning agent before any project
// or repository exists, and a project is created from it. A new plan starts with your first message,
// which the prompt writer proposes a better wording of before the planning agent gets it.
function Plans() {
  const path = useRouterState({ select: (s) => s.location.pathname })
  if (path !== '/plans' && path !== '/plans/') return <Outlet />
  return <PlanList />
}

function PlanList() {
  const { data, error } = usePoll<{ drafts: DraftSummary[] }>('/api/drafts', 10000)
  const navigate = useNavigate()
  const [text, setText] = useState('')
  const [title, setTitle] = useState('')
  const [repo, setRepo] = useState('')
  const [busy, setBusy] = useState(false)
  const [failed, setFailed] = useState('')
  const start = () => {
    setBusy(true)
    setFailed('')
    send<Rewrite>('POST', '/api/rewrites', { kind: 'first', text, title, repo }).then(
      (r) => {
        setBusy(false)
        if (r?.draft) void navigate({ to: '/plans/$draft', params: { draft: r.draft } })
      },
      (e: Error) => {
        setBusy(false)
        setFailed(e.message)
      },
    )
  }
  return (
    <div className="mx-auto max-w-6xl p-6">
      <PageHead title="Plans">A plan is developed with a planning agent before any project or repository exists. A project is created from it.</PageHead>
      <Failed error={error || failed} />
      <section className="mb-6 rounded-lg border border-border bg-bg-raised p-4" aria-label="A new plan">
        <h2 className="mb-2 text-lg font-semibold">A new plan</h2>
        <p className="mb-2 text-sm text-text-dim">
          Say what you want built: the goal, why, what is in and out, the limits, and how to see it is done. Crew first proposes a better
          wording, and you choose which one the planning agent gets.
        </p>
        <WriteBox aria-label="Your first message" rows={6} value={text} onChange={setText} placeholder="What do you want built?" />
        <div className="mt-2 grid gap-2 text-sm md:grid-cols-2">
          <input aria-label="Title" className="rounded-md border border-border bg-bg px-2 py-1" placeholder="A title, optional" value={title} onChange={(e) => setTitle(e.target.value)} />
          <input
            aria-label="A repository on GitHub to read"
            className="rounded-md border border-border bg-bg px-2 py-1 font-mono"
            placeholder="owner/name of a repository to read, optional"
            value={repo}
            onChange={(e) => setRepo(e.target.value)}
          />
        </div>
        <button className={mainButton + ' mt-3'} disabled={busy || !text.trim()} onClick={start}>
          Send
        </button>
      </section>
      <h2 className="mb-2 text-lg font-semibold">Drafts</h2>
      {data && data.drafts.length === 0 && <p className="text-text-dim">No draft yet.</p>}
      <ul className="space-y-2">
        {data?.drafts.map((d) => (
          <li key={d.id}>
            <Link to="/plans/$draft" params={{ draft: d.id }} className="block rounded-lg border border-border bg-bg-raised p-3 text-sm">
              <span className="font-semibold">{d.title || 'Untitled draft'}</span>{' '}
              <span className="text-text-dim">
                · {d.state === 'project' ? 'a project now' : d.state} · revision {d.version} · {when(d.updatedAt)}
                {d.waiting > 0 ? ` · ${d.waiting} message${d.waiting === 1 ? '' : 's'} waiting` : ''}
              </span>
            </Link>
          </li>
        ))}
      </ul>
    </div>
  )
}
