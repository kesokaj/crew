import { createFileRoute, Link, useNavigate } from '@tanstack/react-router'
import { useState } from 'react'
import type { Commit, FileDiff, GitBranch, ProjectGit, PullRequest } from '../lib/api'
import { usePoll } from '../lib/poll'
import { stageName } from '../lib/words'
import { button, Failed, PageHead, when } from '../components/ui'
import { Diff, listed, Omitted } from '../components/diff'
import { FileExplorerModal } from '../components/explorer'
import { trailers } from '../lib/git'

type Search = { commit?: string; from?: string; to?: string; ref?: string }

export const Route = createFileRoute('/p/$project/git')({
  validateSearch: (s: Record<string, unknown>): Search => ({
    commit: typeof s.commit === 'string' ? s.commit : undefined,
    from: typeof s.from === 'string' ? s.from : undefined,
    to: typeof s.to === 'string' ? s.to : undefined,
    ref: typeof s.ref === 'string' ? s.ref : undefined,
  }),
  component: Git,
})

// The Git screen (design 6.7, 20.1, repo.png): where the repository lives and how to clone it, the
// branches with how far each is ahead and its ticket, the tags, the history of any branch, the diff
// of any commit, and a comparison of any two points. Tickets and records are not here: they are in
// Crew's database.
function Git() {
  const { project } = Route.useParams()
  const search = Route.useSearch()
  const navigate = useNavigate({ from: Route.fullPath })
  const base = `/api/projects/${encodeURIComponent(project)}/git`
  const { data: g, error } = usePoll<ProjectGit>(base, 15000)
  const { data: bt } = usePoll<{ branches: GitBranch[]; tags: { name: string; commit: string }[] }>(base + '/branches', 15000)
  const ref = search.ref ?? 'all'
  const { data: log, error: logError } = usePoll<{ commits: Commit[] }>(`${base}/log?ref=${encodeURIComponent(ref)}&limit=200`, 15000)
  const ticketOf = new Map((bt?.branches ?? []).filter((b) => b.ticket).map((b) => [b.name, b.ticket!]))
  const show = (s: Search) => void navigate({ search: s })
  const github = g?.remote?.includes('github.com')
  const [browseRef, setBrowseRef] = useState<string | null>(null)
  return (
    <div className="mx-auto max-w-7xl p-6">
      <PageHead
        crumbs={
          <>
            <Link to="/" className="text-accent">
              All projects
            </Link>{' '}
            / {project}
          </>
        }
        title="Git"
        actions={
          <div className="flex items-center gap-2">
            <button type="button" className={button} onClick={() => setBrowseRef(ref === 'all' ? 'main' : ref)}>
              Browse files
            </button>
            <Link to="/p/$project/terminal" params={{ project }} className={button}>
              Terminal
            </Link>
          </div>
        }
      >
        The repository holds the code. Code and documentation are on main, and every ticket has its own branch. The plan, the tickets,
        their records, and the settings are not here: they are in Crew's database.
      </PageHead>
      <Failed error={error || logError} />
      {browseRef && <FileExplorerModal project={project} refName={browseRef} initialMode="file" onClose={() => setBrowseRef(null)} />}
      {search.commit && <CommitView project={project} base={base} id={search.commit} onClose={() => show({ ref: search.ref })} />}
      {search.from && search.to && <CompareView project={project} base={base} from={search.from} to={search.to} onClose={() => show({ ref: search.ref })} />}
      <div className="grid gap-3 md:grid-cols-3">
        <section className="rounded-lg border border-border bg-bg-raised p-4 text-sm">
          <h2 className="mb-2 text-xs tracking-wide text-text-dim uppercase">Where it lives</h2>
          <dl className="grid grid-cols-[5.5rem_1fr] gap-y-1">
            <dt className="text-text-dim">Repository</dt>
            <dd>{github ? 'on GitHub' : 'on this machine'}</dd>
            {g?.remote && (
              <>
                <dt className="text-text-dim">Remote</dt>
                <dd className="font-mono text-xs break-all">{g.remote}</dd>
                <dt className="text-text-dim">Sync</dt>
                <dd>
                  {g.pushes[0] ? (
                    g.pushes[0].state === 'done' ? (
                      <span className="text-ok">✓ pushed {when(g.pushes[0].doneAt)}</span>
                    ) : (
                      <span className="text-amber-text">
                        waiting to push, {g.pushes[0].tries} tries{g.pushes[0].detail ? `: ${g.pushes[0].detail}` : ''}
                      </span>
                    )
                  ) : (
                    'nothing pushed yet'
                  )}
                  <span className="block text-xs text-text-dim">
                    {g.fetchDetail
                      ? `fetching failed: ${g.fetchDetail}`
                      : g.fetchedAt
                        ? `main fetched ${when(g.fetchedAt)}; what you push there becomes the base`
                        : 'not fetched yet'}
                  </span>
                  {(g.outside ?? []).slice(0, 3).map((m) => (
                    <span key={m.remote + m.at} className="block text-xs" data-outside={m.state}>
                      {m.kind === 'ahead' ? 'From outside' : 'Went another way'}: <span className="font-mono">{m.remote.slice(0, 7)}</span> ·{' '}
                      {remoteState[m.state] ?? m.state}
                      {m.ticket ? ` · ${m.ticket}` : ''}
                    </span>
                  ))}
                </dd>
              </>
            )}
            <dt className="text-text-dim">Git</dt>
            <dd>{g?.remote ? 'over SSH, with the key from Setup' : "Crew's own repository on this machine"}</dd>
            <dt className="text-text-dim">Merging</dt>
            <dd>{g?.usesPulls ? 'a pull request per ticket, with gh; merged by Crew on this machine' : 'a merge commit after the checks on top of main'}</dd>
          </dl>
          <h3 className="mt-3 mb-1 text-xs tracking-wide text-text-dim uppercase">Clone it yourself</h3>
          {g?.remote ? (
            <>
              <pre className="rounded bg-bg p-2 font-mono text-xs whitespace-pre-wrap">git clone {g.remote}</pre>
              <p className="mt-1 text-xs text-text-dim">What you push to main is picked up. The ticket branches can only be changed through Crew.</p>
            </>
          ) : (
            <p className="text-xs text-text-dim">
              A local project is served over HTTPS with a personal token, which comes with the tokens for your own tools (M9). Until
              then, use the project terminal.
            </p>
          )}
        </section>
        <section className="rounded-lg border border-border bg-bg-raised p-4 text-sm">
          <h2 className="mb-2 text-xs tracking-wide text-text-dim uppercase">Branches</h2>
          <ul className="space-y-2">
            {bt?.branches.map((b) => (
              <li key={b.name} data-branch={b.name}>
                <button className="text-left font-mono text-xs text-accent break-all" onClick={() => show({ ref: b.name })}>
                  {b.name}
                </button>
                {b.name === 'main' ? (
                  <span className="text-text-dim"> code and documentation · at {b.head.slice(0, 7)}</span>
                ) : (
                  <div className="text-xs text-text-dim">
                    {b.ahead} ahead of main{b.behind ? `, ${b.behind} behind` : ''}
                    {b.ticket && (
                      <>
                        {' '}
                        · {stageName[b.stage ?? ''] ?? b.stage} ·{' '}
                        <Link to="/p/$project/tickets/$key" params={{ project, key: b.ticket }} className="text-accent underline">
                          {b.ticket}
                        </Link>
                      </>
                    )}
                    {b.name !== 'main' && b.ahead > 0 && (
                      <button className="ml-2 text-accent underline" onClick={() => show({ from: 'main', to: b.name })}>
                        compare with main
                      </button>
                    )}
                  </div>
                )}
              </li>
            ))}
          </ul>
        </section>
        <section className="rounded-lg border border-border bg-bg-raised p-4 text-sm">
          <h2 className="mb-2 text-xs tracking-wide text-text-dim uppercase">Tags</h2>
          {bt && bt.tags.length === 0 && <p className="text-text-dim">None yet.</p>}
          <ul>
            {bt?.tags.map((t) => (
              <li key={t.name}>
                <button className="text-left font-mono text-xs text-accent break-all" onClick={() => show({ ref: t.name })}>
                  {t.name}
                </button>{' '}
                <span className="font-mono text-xs text-text-dim">{t.commit.slice(0, 7)}</span>
              </li>
            ))}
          </ul>
          <ComparePicker branches={bt?.branches ?? []} onCompare={(from, to) => show({ from, to })} />
        </section>
      </div>
      <h2 className="mt-6 text-xl font-semibold">
        Pull requests <span className="text-sm font-normal text-text-dim">opened, updated, and merged by Crew</span>
      </h2>
      <PullRequests pulls={g?.pulls ?? []} uses={!!g?.usesPulls} github={!!github} project={project} />
      <h2 className="mt-6 text-xl font-semibold">History</h2>
      <p className="text-sm text-text-dim">Every push and merge is a commit. Changes to tickets and records are in the database.</p>
      <div className="my-2 flex flex-wrap items-center gap-2 text-sm">
        <div className="inline-flex overflow-hidden rounded-md border border-border">
          <button className={'px-3 py-1 ' + (ref === 'all' ? 'bg-text text-bg' : 'bg-bg-raised')} onClick={() => show({ ref: 'all' })}>
            All branches
          </button>
          <button className={'px-3 py-1 ' + (ref === 'main' ? 'bg-text text-bg' : 'bg-bg-raised')} onClick={() => show({ ref: 'main' })}>
            main
          </button>
        </div>
        {ref !== 'all' && ref !== 'main' && <span className="font-mono text-xs">{ref}</span>}
        <span className="text-text-dim">{log?.commits.length ?? 0} shown</span>
      </div>
      <div className="overflow-x-auto">
        <table className="w-full text-sm">
          <thead className="text-left text-xs tracking-wide text-text-dim uppercase">
            <tr className="border-b border-border">
              <th className="py-1">Commit</th>
              <th>Message</th>
              <th>Author</th>
              <th>Branch</th>
              <th>Trailers</th>
              <th>When</th>
            </tr>
          </thead>
          <tbody>
            {log?.commits.map((c) => {
              const br = c.ref ?? (ref === 'all' ? '' : ref)
              return (
                <tr key={c.id + br} className="border-b border-border/60 align-top" data-commit={c.id}>
                  <td className="py-1.5 pr-2">
                    <button className="font-mono text-xs text-accent" onClick={() => show({ commit: c.id, ref: search.ref })}>
                      {c.id.slice(0, 7)}
                    </button>
                  </td>
                  <td className="py-1.5 pr-2">{c.subject}</td>
                  <td className="py-1.5 pr-2">{c.author}</td>
                  <td className="py-1.5 pr-2 font-mono text-xs">
                    {br}
                    {ticketOf.get(br) && <span className="block text-text-dim">{ticketOf.get(br)}</span>}
                  </td>
                  <td className="py-1.5 pr-2 font-mono text-xs text-text-dim whitespace-pre">{trailers(c.body)}</td>
                  <td className="py-1.5 whitespace-nowrap">{when(c.when)}</td>
                </tr>
              )
            })}
          </tbody>
        </table>
      </div>
    </div>
  )
}

function ComparePicker({ branches, onCompare }: { branches: GitBranch[]; onCompare: (from: string, to: string) => void }) {
  const [from, setFrom] = useState('main')
  const [to, setTo] = useState('')
  return (
    <form
      className="mt-4"
      onSubmit={(e) => {
        e.preventDefault()
        if (from && to) onCompare(from, to)
      }}
    >
      <h3 className="mb-1 text-xs tracking-wide text-text-dim uppercase">Compare two points</h3>
      <p className="mb-1 text-xs text-text-dim">A branch, a tag, or a commit.</p>
      <div className="flex flex-wrap gap-1">
        <input aria-label="From" list="git-points" value={from} onChange={(e) => setFrom(e.target.value)} className="w-28 rounded border border-border bg-bg px-1 font-mono text-xs" />
        <span>…</span>
        <input aria-label="To" list="git-points" value={to} onChange={(e) => setTo(e.target.value)} className="w-28 rounded border border-border bg-bg px-1 font-mono text-xs" />
        <datalist id="git-points">
          {branches.map((b) => (
            <option key={b.name} value={b.name} />
          ))}
        </datalist>
        <button className={button}>Compare</button>
      </div>
    </form>
  )
}

function Files({ files, project, refName }: { files: FileDiff[]; project: string; refName: string }) {
  const [open] = useState<string | null>(files.length === 1 ? files[0]!.path : null)
  const [explorerPath, setExplorerPath] = useState<string | null>(null)
  return (
    <>
      <ul className="mt-2 space-y-1">
        {listed(files).map((f) => (
          <li key={f.path}>
            <button
              type="button"
              className="flex w-full justify-between rounded border border-border px-2 py-1 font-mono text-xs hover:border-accent hover:bg-accent-soft/40 transition-colors"
              onClick={() => setExplorerPath(f.path)}
            >
              <span>{f.path}</span>
              <span>
                <span className="text-ok">+{f.added}</span> <span className="text-bad">−{f.removed}</span>
              </span>
            </button>
            {open === f.path && <Diff file={f} />}
          </li>
        ))}
        {files.length === 0 && <li className="text-sm text-text-dim">No file changed.</li>}
        <li>
          <Omitted files={files} />
        </li>
      </ul>
      {explorerPath && (
        <FileExplorerModal
          project={project}
          refName={refName}
          files={files}
          initialPath={explorerPath}
          initialMode="diff"
          onClose={() => setExplorerPath(null)}
        />
      )}
    </>
  )
}

function CommitView({ project, base, id, onClose }: { project: string; base: string; id: string; onClose: () => void }) {
  const { data, error } = usePoll<{ commit: Commit; files: FileDiff[] }>(`${base}/commits/${encodeURIComponent(id)}`, 0)
  return (
    <section className="mb-4 rounded-lg border border-accent bg-bg-raised p-4" aria-label="Commit">
      <div className="flex items-start gap-2">
        <h2 className="font-semibold">
          <span className="font-mono text-sm text-text-dim">{id.slice(0, 7)}</span> {data?.commit.subject}
        </h2>
        <button className={button + ' ml-auto'} onClick={onClose}>
          Close
        </button>
      </div>
      <Failed error={error} />
      {data && (
        <>
          <p className="text-sm text-text-dim">
            {data.commit.author} · {when(data.commit.when)}
            {data.commit.parents.length > 1 ? ' · a merge, shown against its first parent' : ''}
          </p>
          {data.commit.body && <pre className="mt-2 text-sm whitespace-pre-wrap">{data.commit.body}</pre>}
          <Files files={data.files} project={project} refName={id} />
        </>
      )}
    </section>
  )
}

function CompareView({ project, base, from, to, onClose }: { project: string; base: string; from: string; to: string; onClose: () => void }) {
  const { data, error } = usePoll<{ files: FileDiff[] }>(`${base}/compare?from=${encodeURIComponent(from)}&to=${encodeURIComponent(to)}`, 0)
  return (
    <section className="mb-4 rounded-lg border border-accent bg-bg-raised p-4" aria-label="Comparison">
      <div className="flex items-start gap-2">
        <h2 className="font-semibold">
          What <span className="font-mono text-sm">{to.length === 40 ? to.slice(0, 7) : to}</span> changed since{' '}
          <span className="font-mono text-sm">{from.length === 40 ? from.slice(0, 7) : from}</span>
        </h2>
        <button className={button + ' ml-auto'} onClick={onClose}>
          Close
        </button>
      </div>
      <Failed error={error} />
      {data && <Files files={data.files} project={project} refName={to} />}
    </section>
  )
}

const remoteState: Record<string, string> = {
  held: 'waits for you: it changes files that are code',
  taken: 'taken by you',
  checking: 'Crew runs the tests on it',
  passed: 'the tests passed',
  failed: 'the tests failed: a fix ticket',
  conflict: 'it conflicts with main: a ticket joins them',
  error: 'Crew could not check it',
  refused: 'left by you',
}

const pullState: Record<string, string> = { opening: 'opening', open: 'open', merged: 'merged', closed: 'closed' }

// PullRequests is the table of a project's pull requests (design 7.12, repo.png): one per ticket,
// opened when it leaves In progress, and marked merged by the push of main.
function PullRequests({ pulls, uses, github, project }: { pulls: PullRequest[]; uses: boolean; github: boolean; project: string }) {
  if (pulls.length === 0)
    return (
      <p className="mt-1 text-sm text-text-dim">
        {uses
          ? 'None yet: Crew opens one for each ticket when it leaves In progress.'
          : github
            ? 'None: Crew opens pull requests when gh is signed in in Setup, or with git.merge set to pull_request. A ticket is merged with a merge commit.'
            : 'None: a pull request needs a GitHub remote. A ticket is merged with a merge commit.'}
      </p>
    )
  return (
    <table className="mt-2 w-full text-sm">
      <thead className="text-left text-xs tracking-wide text-text-dim uppercase">
        <tr>
          <th className="py-1 font-normal">#</th>
          <th className="font-normal">Ticket</th>
          <th className="font-normal">Title</th>
          <th className="font-normal">State</th>
          <th className="font-normal">Branch</th>
        </tr>
      </thead>
      <tbody>
        {pulls.map((p) => (
          <tr key={p.ticket} className="border-t border-border" data-pull={p.ticket}>
            <td className="py-1.5 pr-3">{p.number && p.url ? <a href={p.url} className="text-accent" target="_blank" rel="noreferrer">#{p.number}</a> : '—'}</td>
            <td className="pr-3">
              <Link to="/p/$project/tickets/$key" params={{ project, key: p.ticket }} className="font-mono text-xs text-accent">
                {p.ticket}
              </Link>
            </td>
            <td className="pr-3">{p.title}</td>
            <td className="pr-3">
              <span className={p.state === 'merged' ? 'text-ok' : p.state === 'closed' ? 'text-text-dim' : ''}>
                {p.state === 'merged' ? '✓ ' : ''}
                {pullState[p.state] ?? p.state}
              </span>
              {p.held && <span className="block text-xs text-amber-text">a head waits for your yes: it changes files that are code</span>}
            </td>
            <td className="font-mono text-xs">{p.branch}</td>
          </tr>
        ))}
      </tbody>
    </table>
  )
}
