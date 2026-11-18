import { createFileRoute, Link } from '@tanstack/react-router'
import { useState } from 'react'
import { send } from '../lib/api'
import { usePoll } from '../lib/poll'
import { seenIn } from '../lib/evidence'
import { lineDiff } from '../lib/linediff'
import { button, Card, Check, Failed, mainButton, PageHead, Rows, when } from '../components/ui'
import { pct } from '../components/bars'
import { iNum, status, type Improvement } from '../components/improvement'

export const Route = createFileRoute('/crew_/improvements/$n')({ component: ImprovementPage })

type Detail = {
  improvement: Improvement
  files: { path: string; before: string; after: string }[]
  projectEvals: { project: string; cases: number; result: Record<string, { runs?: Record<string, boolean[]> }> }[]
}

// One improvement (10.5): its change, the steps from the proposal to a running version, its evidence,
// its evals in each project, and what it measured. You approve one that waits for you, and take out one
// that is not installed.
function ImprovementPage() {
  const { n } = Route.useParams()
  const { data, error, reload } = usePoll<Detail>(`/api/crew/improvements/${encodeURIComponent(n)}`, 10000)
  const [failed, setFailed] = useState('')
  const decide = (choice: string) =>
    send('POST', `/api/crew/improvements/${encodeURIComponent(n)}`, { choice }).then(
      () => (setFailed(''), reload()),
      (e: Error) => setFailed(e.message),
    )
  const x = data?.improvement
  const waitsForYou = x?.waitsFor === 'operator'
  const canTakeOut = x && ['proposed', 'testing', 'passed'].includes(x.status)
  const steps: [string, boolean | null][] = x
    ? [
        ['Proposed by the improver, from signals', true],
        ['What it may touch, and the generic check: no project name, path, or copied text', x.status === 'refused' && !x.evals ? false : true],
        [
          'Evals in each project that has cases',
          x.evals ? x.evals.verdict === 'not_worse' : x.waitsFor === 'operator' && x.status === 'proposed' ? null : x.status === 'refused' ? null : null,
        ],
        ['A ticket on Crew\'s own project', x.ticket ? true : null],
        ['Shipped in a release', x.releaseTo ? true : null],
        ['Measured after it was applied', x.result ? x.result.verdict !== 'worse' : null],
      ]
    : []
  return (
    <div className="mx-auto max-w-6xl p-6">
      <PageHead
        crumbs={
          <>
            <Link to="/crew" search={{ tab: 'improvements' }} className="text-accent">
              Crew level
            </Link>{' '}
            / {x ? iNum(x.number) : n}
          </>
        }
        title={x?.title ?? 'Improvement'}
        actions={
          x && (
            <>
              {waitsForYou && (
                <button className={mainButton} onClick={() => decide('approve')}>
                  Approve
                </button>
              )}
              {canTakeOut && (
                <button className={button} onClick={() => decide('take_out')}>
                  Take it out
                </button>
              )}
            </>
          )
        }
      >
        A change to the baseline that every project runs on. {x && <>Proposed by the improver {when(x.createdAt)}.</>}
      </PageHead>
      <Failed error={error || failed} />
      {x && data && (
        <div className="grid gap-3 md:grid-cols-[2fr_1fr]">
          <div className="grid content-start gap-3">
            {data.files.map((f) => (
              <Card key={f.path} title="The change" right={<span className="font-mono">{f.path}</span>}>
                <pre className="max-h-[32rem] overflow-auto rounded-md bg-bg-sunk p-2 text-xs leading-5" data-diff={f.path}>
                  {lineDiff(f.before, f.after).map((l, i) => (
                    <div
                      key={i}
                      className={l.kind === '+' ? 'bg-green-100 dark:bg-green-950' : l.kind === '-' ? 'bg-red-100 dark:bg-red-950' : 'text-text-dim'}
                    >
                      {l.kind} {l.text}
                    </div>
                  ))}
                </pre>
              </Card>
            ))}
            {x.body && (
              <Card title="The change and its risk">
                <p className="text-sm whitespace-pre-wrap">{x.body}</p>
              </Card>
            )}
            <Card title="Steps from proposal to a running version">
              <ul className="grid gap-1.5 text-sm" data-steps>
                {steps.map(([label, ok]) => (
                  <li key={label} className="flex gap-2">
                    {ok === false ? <span className="w-4 text-bad">✗</span> : <Check on={ok === true} />}
                    <span className={ok === null ? 'text-text-dim' : ''}>{label}</span>
                  </li>
                ))}
              </ul>
              {x.refusedWhy && <p className="mt-3 text-sm text-bad">{x.refusedWhy}</p>}
            </Card>
            {x.examples && x.examples.length > 0 && (
              <Card title="Three of your prompts, by the old rules and the new">
                <div className="grid gap-4 text-sm">
                  {x.examples.map((e, i) => (
                    <div key={i} className="grid gap-2 md:grid-cols-3">
                      <div>
                        <p className="text-xs text-text-dim uppercase">Yours</p>
                        <p className="whitespace-pre-wrap">{e.original}</p>
                      </div>
                      <div>
                        <p className="text-xs text-text-dim uppercase">The old rules</p>
                        <p className="whitespace-pre-wrap">{e.old}</p>
                      </div>
                      <div>
                        <p className="text-xs text-text-dim uppercase">The new rules</p>
                        <p className="whitespace-pre-wrap">{e.new}</p>
                      </div>
                    </div>
                  ))}
                </div>
              </Card>
            )}
          </div>
          <div className="grid content-start gap-3">
            <Card title="Status">
              <p className="mb-3" data-status={x.status}>
                {status(x)}
              </p>
              <Rows
                narrow
                rows={[
                  ['Crew version', x.releaseFrom ? `${x.releaseFrom}${x.releaseTo ? ` → ${x.releaseTo}` : ''}` : '—'],
                  ['Expected', x.expected || '—'],
                  ['Measured by', x.measure],
                  ['Ticket', x.ticket ?? '—'],
                ]}
              />
            </Card>
            <Card title="Evidence">
              <Rows
                narrow
                rows={[
                  ['Signal', x.evidence.signal || '—'],
                  ['Seen in', seenIn(x.evidence)],
                  ['Window', x.evidence.window || '—'],
                ]}
              />
              {x.evidence.text && <p className="mt-2 text-sm whitespace-pre-wrap">{x.evidence.text}</p>}
              <p className="mt-2 text-xs text-text-dim">The evidence stays at the Crew level. Only the change itself reaches the projects.</p>
            </Card>
            {x.evals && (
              <Card title="Evals, each run inside its own project">
                <Rows
                  narrow
                  rows={[
                    ['Cases', `${x.evals.cases ?? 0} in ${x.evals.projects ?? 0} projects`],
                    ['Goals met before', x.evals.passedBefore !== undefined ? pct(x.evals.passedBefore) : '—'],
                    ['Goals met after', x.evals.passedAfter !== undefined ? pct(x.evals.passedAfter) : '—'],
                    ['Margin', x.evals.margin !== undefined ? `${Math.round(x.evals.margin * 100)} points` : '—'],
                  ]}
                />
                {x.evals.why && <p className="mt-2 text-sm">{x.evals.why}</p>}
                {data.projectEvals.length > 0 && (
                  <ul className="mt-2 text-xs text-text-dim">
                    {data.projectEvals.map((p) => (
                      <li key={p.project}>
                        {p.project}: {p.cases} cases
                      </li>
                    ))}
                  </ul>
                )}
              </Card>
            )}
            {x.result && (
              <Card title="Measured after it was applied">
                <p className="text-2xl font-bold">
                  {pct(x.result.before)} → {pct(x.result.after)}
                </p>
                <p className={x.result.verdict === 'worse' ? 'text-bad' : 'text-accent'}>{x.result.verdict}</p>
                <p className="mt-2 text-sm text-text-dim">{x.result.why}</p>
              </Card>
            )}
          </div>
        </div>
      )}
    </div>
  )
}
