import { Link } from '@tanstack/react-router'
import { useState } from 'react'
import type { CheckRun, Decision, Summary, TaskView } from '../../lib/api'
import { isJudgePurpose, plainText, purposeText } from '../../lib/words'
import { when } from '../ui'
import { Box } from './ticket-header'

export function WhyWhatGoalBox({ v }: { v: TaskView }) {
  const t = v.ticket
  return (
    <Box title="Why, what, goal" part="why">
      <dl className="grid max-w-[65ch] grid-cols-[5rem_minmax(0,1fr)] gap-x-3 gap-y-2.5 text-sm break-words [overflow-wrap:anywhere]">
        <dt className="text-text-dim">Why</dt>
        <dd className="min-w-0 leading-relaxed whitespace-pre-wrap break-words [overflow-wrap:anywhere]" data-free-text data-part="why">
          {t.why}
        </dd>
        <dt className="text-text-dim">What</dt>
        <dd className="min-w-0 leading-relaxed whitespace-pre-wrap break-words [overflow-wrap:anywhere]" data-free-text data-part="what">
          {t.what}
        </dd>
        <dt className="text-text-dim">Goal</dt>
        <dd className="min-w-0 font-semibold leading-relaxed whitespace-pre-wrap break-words [overflow-wrap:anywhere]" data-free-text data-part="goal">
          {t.goal}
        </dd>
        {t.approach && (
          <>
            <dt className="text-text-dim">Approach</dt>
            <dd className="min-w-0 leading-relaxed whitespace-pre-wrap break-words [overflow-wrap:anywhere]" data-free-text data-part="approach">
              {t.approach}
            </dd>
          </>
        )}
      </dl>
      <h4 className="mt-4 mb-1.5 text-xs tracking-wide text-text-dim uppercase">Checks</h4>
      <ul className="space-y-1 text-sm" data-part="checks">
        {t.checks.map((c, i) => (
          <li key={i} className="flex min-w-0 gap-2">
            <CheckMark state={latestOf(v.checkRuns, c.spec)} />
            <span className={'min-w-0 break-words [overflow-wrap:anywhere] ' + (c.kind === 'cmd' ? 'font-mono text-xs' : '')}>
              {c.kind === 'judge' ? 'judge: ' : ''}
              <span data-free-text>{c.spec}</span>
              {c.keeps && <span className="text-text-dim"> · must keep passing</span>}
            </span>
          </li>
        ))}
        {t.checks.length === 0 && <li className="text-text-dim">None set yet: Analysis sets them.</li>}
      </ul>
      <CheckRuns runs={v.checkRuns} />
    </Box>
  )
}

function latestOf(runs: CheckRun[], spec: string): string | undefined {
  for (const r of runs) {
    const res = r.results.find((x) => x.name === spec)
    if (res) return res.passed ? 'passed' : 'failed'
  }
  return undefined
}

function CheckMark({ state }: { state?: string }) {
  if (state === 'passed') return <span className="text-ok">✓</span>
  if (state === 'failed') return <span className="text-bad">✗</span>
  return <span className="text-text-dim">·</span>
}

function CheckRuns({ runs }: { runs: CheckRun[] }) {
  const [open, setOpen] = useState<number | null>(null)
  if (runs.length === 0) return null
  return (
    <>
      <h4 className="mt-3 mb-1 text-xs tracking-wide text-text-dim uppercase">Check runs, in a check sandbox, and the judge's answers</h4>
      <ul className="space-y-1 text-sm">
        {runs.map((r) => (
          <li key={r.id}>
            <button className="flex w-full min-w-0 gap-2 text-left" onClick={() => setOpen(open === r.id ? null : r.id)}>
              <CheckMark state={r.state} />
              <span className="min-w-0 break-words [overflow-wrap:anywhere]">
                {purposeText[r.purpose] ?? r.purpose} <span className="font-mono text-xs text-text-dim">{r.commit.slice(0, 7)}</span>
                <span className="text-text-dim">
                  {' '}
                  · {r.state}
                  {r.flaky ? ', flaky' : ''} · {when(r.startedAt)}
                </span>
              </span>
            </button>
            {open === r.id && (
              <div className="mt-1 ml-5 space-y-1">
                {r.detail && <p className="text-xs break-words [overflow-wrap:anywhere]">{r.detail}</p>}
                {r.results.map((x, i) => (
                  <div key={i}>
                    <p className="font-mono text-xs break-words [overflow-wrap:anywhere]">
                      {x.passed ? '✓' : '✗'} {x.name} · {isJudgePurpose(r.purpose) ? (x.passed ? 'met' : 'not met') : `exit ${x.code}`}
                    </p>
                    {x.output && (
                      <pre className="max-h-40 overflow-auto rounded bg-term p-2 text-xs whitespace-pre-wrap text-neutral-200 break-words [overflow-wrap:anywhere]">
                        {x.output}
                      </pre>
                    )}
                  </div>
                ))}
              </div>
            )}
          </li>
        ))}
      </ul>
    </>
  )
}

export function DecisionsBox({ project, mine }: { project: string; mine: Decision[] }) {
  if (mine.length === 0) return null
  return (
    <Box title="Decisions agents recorded on this ticket">
      <ul className="space-y-2 text-sm">
        {mine.map((d) => (
          <li key={d.id}>
            <b data-free-text>{d.question}</b> → <span data-free-text>{d.choice}</span>
            <span className="text-text-dim">
              {' '}
              · the {d.role} · <span data-free-text>{d.reason}</span>
            </span>
          </li>
        ))}
      </ul>
      <Link to="/p/$project/knowledge" params={{ project }} className="mt-2 inline-block text-sm text-accent">
        Change one in Decisions
      </Link>
    </Box>
  )
}

export function SummaryCard({ s }: { s: Summary }) {
  const factsOnly = s.missing && s.outcome !== 'done' && !s.writtenAt
  return (
    <Box
      title="Summary"
      part="summary"
      right={<span className="text-text-dim">{s.writtenAt ? `written ${when(s.writtenAt)}, stored in Crew's database` : factsOnly ? 'the facts only' : 'being written'}</span>}
    >
      <dl className="grid max-w-[65ch] grid-cols-[8rem_minmax(0,1fr)] gap-x-3 gap-y-2 text-sm break-words">
        {s.whatWasDone && (
          <>
            <dt className="text-text-dim">What was done</dt>
            <dd className="whitespace-pre-wrap" data-free-text>
              {plainText(s.whatWasDone)}
            </dd>
          </>
        )}
        {s.howItWent && (
          <>
            <dt className="text-text-dim">How it went</dt>
            <dd className="whitespace-pre-wrap" data-free-text>
              {plainText(s.howItWent)}
            </dd>
          </>
        )}
        {s.whatToKnowNextTime && (
          <>
            <dt className="text-text-dim">Next time</dt>
            <dd className="whitespace-pre-wrap" data-free-text>
              {plainText(s.whatToKnowNextTime)}
            </dd>
          </>
        )}
        <dt className="text-text-dim">Facts</dt>
        <dd>
          <Facts facts={s.facts} />
        </dd>
      </dl>
      {factsOnly && <p className="mt-2 text-xs text-text-dim">A ticket withdrawn in Planning gets the facts of a summary and no summary writer (8.4).</p>}
    </Box>
  )
}

const noCost = (key: string, v: unknown) => (key === 'cost_usd' ? undefined : v)
function Facts({ facts }: { facts: Record<string, unknown> }) {
  const json = (v: unknown) => JSON.stringify(v, noCost)
  const plain = (v: unknown): string =>
    Array.isArray(v) ? `${v.length} ${v.length === 1 ? 'item' : 'items'}` : v !== null && typeof v === 'object' ? json(v) : String(v)
  return (
    <dl className="grid grid-cols-[9rem_minmax(0,1fr)] gap-x-2 text-xs" data-facts>
      {Object.entries(facts)
        .filter(([k]) => k !== 'cost_usd')
        .map(([k, v]) => (
          <div key={k} className="contents">
            <dt className="text-text-dim">{k.replaceAll('_', ' ')}</dt>
            <dd className="truncate font-mono" title={typeof v === 'object' ? json(v) : String(v)}>
              {plain(v)}
            </dd>
          </div>
        ))}
    </dl>
  )
}
