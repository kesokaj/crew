import { createFileRoute, Link } from '@tanstack/react-router'
import { useEffect, useState } from 'react'
import Markdown from 'react-markdown'
import remarkGfm from 'remark-gfm'
import { getJSON, send, type DocHit, type DocPage } from '../lib/api'
import { usePoll } from '../lib/poll'
import { button, Failed, PageHead, Tag, when } from '../components/ui'

export const Route = createFileRoute('/p/$project/docs')({
  validateSearch: (s: Record<string, unknown>): { page?: string } => (typeof s.page === 'string' ? { page: s.page } : {}),
  component: Docs,
})

// The groups of the tree, by a page's kind (9.9).
const groups: [string, string[]][] = [
  ['Overview', ['overview']],
  ['How it is built', ['architecture', 'setup']],
  ['Guides', ['guide']],
  ['Reference', ['reference', 'page']],
  ['Decisions', ['decision']],
  ['History', ['changelog']],
]

// frontless is a page without its front matter, which Crew reads and the reader does not need.
const frontless = (body: string) => body.replace(/^---\n[\s\S]*?\n---\n/, '')

// Documentation (9.9, docs.png): the pages every project keeps about itself, on main beside the code.
// Crew indexes them when main changes; agents write them with the change they describe. You read,
// search, and edit them here; your edit is a commit on main.
function Docs() {
  const { project } = Route.useParams()
  const { page } = Route.useSearch()
  const base = `/api/projects/${encodeURIComponent(project)}/docs`
  const { data, error, reload } = usePoll<{ main: string; pages: DocPage[]; current: number }>(base, 15000)
  const [query, setQuery] = useState('')
  const [hits, setHits] = useState<DocHit[] | null>(null)
  const pages = data?.pages ?? []
  const path = page ?? pages.find((p) => p.kind === 'overview')?.path ?? pages[0]?.path
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
        title="Documentation"
      >
        The pages are files in the project's repository, beside the code. Crew indexes them and shows them here.
        {data && (
          <>
            {' '}
            {data.current} of {pages.length} pages are current.
          </>
        )}
      </PageHead>
      <Failed error={error} />
      <div className="grid grid-cols-[15rem_minmax(0,1fr)] gap-4">
        <nav aria-label="Pages" className="self-start rounded-lg border border-border bg-bg-raised p-2">
          <form
            className="mb-2"
            onSubmit={(e) => {
              e.preventDefault()
              if (!query.trim()) return setHits(null)
              getJSON<{ pages: DocHit[] }>(`${base}/search?q=${encodeURIComponent(query)}`).then(
                (r) => setHits(r.pages),
                () => setHits([]),
              )
            }}
          >
            <input
              aria-label="Search the pages"
              value={query}
              onChange={(e) => setQuery(e.target.value)}
              placeholder="Search"
              className="w-full rounded border border-border bg-bg px-2 py-1 text-sm"
            />
          </form>
          {hits ? (
            <ul aria-label="Found">
              {hits.length === 0 && <li className="px-2 py-1 text-sm text-text-dim">No page holds that.</li>}
              {hits.map((h) => (
                <li key={h.path}>
                  <Link to="/p/$project/docs" params={{ project }} search={{ page: h.path }} className="block rounded px-2 py-1 text-sm hover:bg-bg-sunk">
                    <span className="font-medium">{h.title}</span>
                    <span className="block text-xs text-text-dim">{h.snippet.replaceAll('**', '')}</span>
                  </Link>
                </li>
              ))}
            </ul>
          ) : (
            groups.map(([name, kinds]) => {
              const in_ = pages.filter((p) => kinds.includes(p.kind))
              if (in_.length === 0) return null
              return (
                <div key={name}>
                  <p className="px-2 pt-2 pb-1 text-xs tracking-wide text-text-dim uppercase">{name}</p>
                  {in_.map((p) => (
                    <Link
                      key={p.path}
                      to="/p/$project/docs"
                      params={{ project }}
                      search={{ page: p.path }}
                      data-page={p.path}
                      className={'flex items-center justify-between rounded px-2 py-1 text-sm ' + (p.path === path ? 'border border-border bg-bg font-semibold' : 'hover:bg-bg-sunk')}
                    >
                      <span className="truncate">{p.title}</span>
                      {!p.outdatedSince ? null : <span title="Its code changed since: possibly out of date" className="text-amber">●</span>}
                    </Link>
                  ))}
                </div>
              )
            })
          )}
          {data && pages.length === 0 && <p className="px-2 py-1 text-sm text-text-dim">main has no page yet.</p>}
        </nav>
        {path && data && <Page key={path + data.main} project={project} path={path} onSaved={reload} />}
      </div>
    </div>
  )
}

function Page({ project, path, onSaved }: { project: string; path: string; onSaved: () => void }) {
  const url = `/api/projects/${encodeURIComponent(project)}/docs/page?path=${encodeURIComponent(path)}`
  const [doc, setDoc] = useState<{ main: string; page: DocPage } | null>(null)
  const [error, setError] = useState('')
  const [edit, setEdit] = useState<string | null>(null)
  const [busy, setBusy] = useState(false)
  useEffect(() => {
    getJSON<{ main: string; page: DocPage }>(url).then(setDoc, (e: Error) => setError(e.message))
  }, [url])
  if (!doc) return <Failed error={error} />
  const p = doc.page
  const save = () => {
    setBusy(true)
    send('PUT', `/api/projects/${encodeURIComponent(project)}/docs/page`, { path, base: doc.main, content: edit }).then(
      (r) => {
        setBusy(false)
        setEdit(null)
        setError('')
        if (r) setDoc(r as { main: string; page: DocPage })
        onSaved()
      },
      (e: Error) => {
        setBusy(false)
        setError(e.message)
      },
    )
  }
  const tickets = p.tickets ?? []
  return (
    <div className="flex min-w-0 flex-col gap-3">
      <article className="rounded-lg border border-border bg-bg-raised p-4" aria-label="The page">
        <div className="mb-2 flex items-center justify-between text-xs text-text-dim">
          <span className="font-mono">
            {p.path} · on main
          </span>
          <span className="flex items-center gap-2">
            {p.outdatedSince ? <Tag tone="amber">Possibly out of date</Tag> : <span>✓ Current</span>}
            {p.kind !== 'changelog' && edit === null && (
              <button className="text-accent" onClick={() => setEdit(p.body ?? '')}>
                Edit
              </button>
            )}
          </span>
        </div>
        {edit === null ? (
          <div className="markdown">
            <Markdown remarkPlugins={[remarkGfm]}>{frontless(p.body ?? '')}</Markdown>
          </div>
        ) : (
          <div>
            <textarea aria-label="The page's text" value={edit} onChange={(e) => setEdit(e.target.value)} rows={18} className="w-full rounded border border-border bg-bg p-2 font-mono text-sm" />
            <p className="my-1 text-xs text-text-dim">Saving makes a commit on main with you as its author. A page that changed on main since you opened it is not overwritten.</p>
            <div className="flex gap-2">
              <button className={button} disabled={busy} onClick={save}>
                Save to main
              </button>
              <button className="text-sm text-text-dim" onClick={() => setEdit(null)}>
                Cancel
              </button>
            </div>
          </div>
        )}
        <Failed error={error} />
      </article>
      <section className="rounded-lg border border-border bg-bg-raised p-4 text-sm" aria-label="About this page">
        <p className="mb-2 text-xs tracking-wide text-text-dim uppercase">About this page</p>
        <dl className="grid grid-cols-[8rem_minmax(0,1fr)] gap-y-1">
          <dt className="text-text-dim">Kind</dt>
          <dd>{p.kind}</dd>
          <dt className="text-text-dim">Covers</dt>
          <dd className="font-mono">{p.covers.length > 0 ? p.covers.join(', ') : p.kind === 'overview' ? 'the whole project' : 'nothing named'}</dd>
          <dt className="text-text-dim">Last changed</dt>
          <dd>
            {p.lastTicket ? <TicketLink project={project} ticket={p.lastTicket} /> : <span className="font-mono text-xs">{p.lastCommit.slice(0, 12)}</span>} {when(p.lastChangedAt)}
          </dd>
          <dt className="text-text-dim">Changed by</dt>
          <dd className="flex flex-wrap gap-1">
            {tickets.length === 0 ? <span className="text-text-dim">no ticket</span> : tickets.map((t) => <TicketLink key={t} project={project} ticket={t} />)}
          </dd>
          {p.outdatedSince && (
            <>
              <dt className="text-text-dim">Marked</dt>
              <dd>
                {when(p.outdatedSince)}: its code changed in {p.outdatedBy}, and the page did not. The lead reads it in housekeeping.
              </dd>
            </>
          )}
        </dl>
      </section>
    </div>
  )
}

function TicketLink({ project, ticket }: { project: string; ticket: string }) {
  return (
    <Link to="/p/$project/tickets/$key" params={{ project, key: ticket }} className="font-mono text-xs text-accent underline">
      {ticket}
    </Link>
  )
}
