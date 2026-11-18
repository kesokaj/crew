import { createFileRoute, Link } from '@tanstack/react-router'
import { usePoll } from '../lib/poll'
import { stageName } from '../lib/words'
import { tokens } from '../lib/tokens'
import { button, Failed, PageHead, when } from '../components/ui'
import { WriteBox, WriteScope } from '../components/writebox'
import { send } from '../lib/api'
import { useState } from 'react'

export const Route = createFileRoute('/p/$project/plan')({ component: Plan })

type Entry = { number: number; text: string; createdBy: string; createdAt: string }
type PlanView = { original: string; breakdown?: string; amendments: Entry[]; sentBack: Entry[] }
type BoardTickets = { columns: { id: string; tickets: { key: string; title: string }[] }[] }

// The plan (design 7.2, 20.1, plan.png): the original plan, never edited after the OK; the parts with
// the tickets that came from each; the breakdown; what you sent back; and the amendments. The plan is
// shown as text, as the planning agent wrote it.
function Plan() {
  const { project } = Route.useParams()
  const { data: p, error } = usePoll<PlanView>(`/api/projects/${encodeURIComponent(project)}/plan`, 0)
  const { data: board } = usePoll<BoardTickets>(`/api/projects/${encodeURIComponent(project)}/board`, 0)
  const { data: overview } = usePoll<{ project: { state: string } }>(`/api/projects/${encodeURIComponent(project)}/overview`, 0)
  const keys = (board?.columns ?? []).flatMap((c) => c.tickets.map((t) => ({ ...t, stage: c.id })))
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
        title="Plan"
      >
        The original plan and its breakdown. The plan is never edited after the OK: a change is an amendment.
      </PageHead>
      <Failed error={error} />
      {p && (
        <div className="grid gap-4 lg:grid-cols-[minmax(0,1fr)_320px]">
          <div className="space-y-4">
            <FinalReport project={project} />
            <section className="rounded-lg border border-border bg-bg-raised p-4">
              <h2 className="mb-2 text-xs tracking-wide text-text-dim uppercase">The original plan · plan/original.md</h2>
              <pre className="text-sm whitespace-pre-wrap">{p.original}</pre>
            </section>
            {p.breakdown && (
              <section className="rounded-lg border border-border bg-bg-raised p-4">
                <h2 className="mb-2 text-xs tracking-wide text-text-dim uppercase">The breakdown, by the lead</h2>
                <pre className="text-sm whitespace-pre-wrap">{p.breakdown}</pre>
              </section>
            )}
            {[...p.sentBack.map((e) => ({ ...e, kind: 'You sent the breakdown back' })), ...p.amendments.map((e) => ({ ...e, kind: 'Amendment' }))].map((e) => (
              <section key={e.kind + e.number} className="rounded-lg border border-border bg-bg-raised p-4">
                <h2 className="mb-2 text-xs tracking-wide text-text-dim uppercase">
                  {e.kind} {e.number} · {e.createdBy} · {when(e.createdAt)}
                </h2>
                <pre className="text-sm whitespace-pre-wrap">{e.text}</pre>
              </section>
            ))}
          </div>
          <aside className="space-y-4">
            <WriteScope.Provider value={{ project }}>
              <PlanComment project={project} />
            </WriteScope.Provider>
            {(overview?.project.state === 'running' || overview?.project.state === 'done') && <Amend project={project} />}
            <section className="rounded-lg border border-border bg-bg-raised p-4 text-sm">
              <h2 className="mb-2 text-xs tracking-wide text-text-dim uppercase">The tickets</h2>
              {overview?.project.state === 'planning' && <p className="mb-2 text-amber-text">They wait in Planning for your OK, on the board.</p>}
              <ul className="space-y-1">
                {keys.map((t) => (
                  <li key={t.key}>
                    <Link to="/p/$project/tickets/$key" params={{ project, key: t.key }} className="font-mono text-xs text-accent">
                      {t.key}
                    </Link>{' '}
                    {t.title} <span className="text-text-dim">· {stageName[t.stage] ?? t.stage}</span>
                  </li>
                ))}
              </ul>
            </section>
          </aside>
        </div>
      )}
    </div>
  )
}


type Report = {
  number: number
  sections: Record<string, string>
  facts: {
    done: string[]
    cancelled: { key: string; title: string; reason: string; replacedBy?: string[] }[]
    skipped: { key: string; title: string; reason: string }[]
    discarded: { key: string; title: string; reason: string }[]
    missing: string[]
    // The tokens of every run, in and out; a report written before 0.53 has none, and its costUsd is
    // not shown (design 0.53: no cost).
    tokens?: number
    runs: number
    fixRounds: number
  }
  missing?: string
  at: string
}

const sectionTitle: Record<string, string> = {
  built: 'What was built',
  skipped: 'What was skipped',
  cancelled: 'What was cancelled',
  missing: 'What is missing',
  learned: 'What we learned',
}

// FinalReport is the newest final report of a Done project (design 7.2): the lead's sections, and the
// facts Crew counted itself.
function FinalReport({ project }: { project: string }) {
  const { data } = usePoll<{ reports: Report[] }>(`/api/projects/${encodeURIComponent(project)}/report`, 0)
  const r = data?.reports[0]
  if (!r) return null
  const f = r.facts
  return (
    <section data-report className="rounded-lg border border-accent bg-bg-raised p-4 text-sm">
      <h2 className="mb-1 text-xs tracking-wide text-text-dim uppercase">
        The final report {r.number > 1 ? r.number : ''} · {when(r.at)}
      </h2>
      <p className="mb-3 text-text-dim">
        {f.done.length} done, {f.cancelled.length} cancelled, {f.skipped.length} skipped, {f.discarded.length} discarded · {f.runs} runs,{' '}
        {f.fixRounds} fix rounds{f.tokens !== undefined ? ` · ${tokens(f.tokens)}` : ''}
      </p>
      {r.missing && <p className="mb-3 text-bad">The lead could not write it: {r.missing}. These are Crew's facts only.</p>}
      {Object.keys(sectionTitle).map((k) =>
        r.sections[k] ? (
          <div key={k} className="mb-3">
            <h3 className="font-semibold">{sectionTitle[k]}</h3>
            <p className="whitespace-pre-wrap">{r.sections[k]}</p>
          </div>
        ) : null,
      )}
      {f.cancelled.length > 0 && (
        <div className="mb-2">
          <h3 className="font-semibold">Cancelled, as Crew recorded it</h3>
          <ul className="list-disc pl-5">
            {f.cancelled.map((c) => (
              <li key={c.key}>
                <span className="font-mono">{c.key}</span> {c.title}: {c.reason}
                {c.replacedBy && c.replacedBy.length > 0 ? ` Replaced by ${c.replacedBy.join(', ')}.` : ''}
              </li>
            ))}
          </ul>
        </div>
      )}
      {f.missing.length > 0 && (
        <div>
          <h3 className="font-semibold">Missing, as the lead recorded it</h3>
          <ul className="list-disc pl-5">
            {f.missing.map((m) => (
              <li key={m}>{m}</li>
            ))}
          </ul>
        </div>
      )}
    </section>
  )
}

// Amend adds an amendment to the plan of a project that has its OK (design 7.13): the lead breaks it
// down like the plan, its tickets wait in Planning for the OK in the inbox, and the project runs on.
function Amend({ project }: { project: string }) {
  const [text, setText] = useState('')
  const [said, setSaid] = useState('')
  return (
    <section className="rounded-lg border border-border bg-bg-raised p-4 text-sm">
      <h2 className="mb-2 text-xs tracking-wide text-text-dim uppercase">Amend the plan</h2>
      <p className="mb-2 text-text-dim">The lead breaks it down like the plan. Its tickets wait for your OK in the inbox.</p>
      <textarea
        aria-label="The amendment"
        className="h-24 w-full rounded-md border border-border bg-bg p-2"
        value={text}
        onChange={(e) => setText(e.target.value)}
      />
      <button
        className={button + ' mt-2'}
        disabled={!text.trim()}
        onClick={() =>
          send('POST', `/api/projects/${encodeURIComponent(project)}/amendments`, { text }).then(
            () => {
              setText('')
              setSaid('The lead breaks the amendment down now.')
            },
            (e: Error) => setSaid(e.message),
          )
        }
      >
        Add the amendment
      </button>
      {said && <p className="mt-2">{said}</p>}
    </section>
  )
}

// PlanComment is a comment on the project's plan: it starts the lead, who answers it (11.6).
function PlanComment({ project }: { project: string }) {
  const [text, setText] = useState('')
  const [said, setSaid] = useState('')
  return (
    <section className="rounded-lg border border-border bg-bg-raised p-4 text-sm">
      <h2 className="mb-2 text-xs tracking-wide text-text-dim uppercase">A comment on the plan</h2>
      <WriteBox aria-label="Your comment on the plan" rows={3} value={text} onChange={setText} placeholder="The lead answers it" />
      <button
        className={button + ' mt-2'}
        disabled={!text.trim()}
        onClick={() =>
          send('POST', `/api/projects/${encodeURIComponent(project)}/comments`, { text }).then(
            () => {
              setText('')
              setSaid('Sent: the lead answers it.')
            },
            (e: Error) => setSaid(e.message),
          )
        }
      >
        Send the comment
      </button>
      {said && <p className="mt-1 text-text-dim">{said}</p>}
    </section>
  )
}
