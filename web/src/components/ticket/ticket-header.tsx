import { Link } from '@tanstack/react-router'
import type { ReactNode } from 'react'
import type { Subtask, TaskView } from '../../lib/api'
import { CLIModel } from '../runs'
import { button, Failed, mainButton, Tag, when } from '../ui'
import { roleName, stageName } from '../../lib/words'

export type WorkspaceTab = 'all' | 'spec' | 'run' | 'prompt' | 'history'

export const WORKSPACE_TABS: { id: WorkspaceTab; label: string }[] = [
  { id: 'all', label: 'All sections' },
  { id: 'spec', label: 'Spec & Checks' },
  { id: 'run', label: 'Live Run & Diff' },
  { id: 'prompt', label: 'Prompt' },
  { id: 'history', label: 'Reviews & History' },
]

// A box of the task view is a part a text can be marked in (20.2): its title is where, and part is one of
// the parts the server knows (board.QuoteParts); an element inside may name a narrower part.
export function Box({ title, children, right, tone, part }: { title: string; children: ReactNode; right?: ReactNode; tone?: 'blue'; part?: string }) {
  return (
    <section
      className={'min-w-0 rounded-lg border bg-bg-raised p-4 break-words [overflow-wrap:anywhere] ' + (tone === 'blue' ? 'border-accent border-2' : 'border-border')}
      data-part={part ?? 'other'}
      data-label={title}
      aria-label={title}
    >
      <div className="mb-3 flex items-center gap-2">
        <h2 className="text-xs tracking-wide text-text-dim uppercase">{title}</h2>
        {right && <div className="ml-auto text-xs">{right}</div>}
      </div>
      {children}
    </section>
  )
}

export function TicketHeader({
  project,
  v,
  stages,
  live,
  ended,
  error,
  failed,
  activeTab,
  onTabChange,
  onPatch,
  onDecide,
}: {
  project: string
  v: TaskView
  stages: { id: string; optional: boolean; on: boolean }[]
  live: { s: Subtask; r: Subtask['runs'][number] }[]
  ended: boolean
  error: string
  failed: string
  activeTab: WorkspaceTab
  onTabChange: (tab: WorkspaceTab) => void
  onPatch: (body: unknown) => void
  onDecide: (choice: string) => void
}) {
  const t = v.ticket
  const activeRun = live[0]
  return (
    <>
      <p className="mb-1 font-mono text-xs text-text-dim">
        <Link to="/p/$project/board" params={{ project }} className="text-accent">
          {project}
        </Link>{' '}
        / Tickets / {t.key}
      </p>
      <div className="flex flex-wrap items-start gap-3">
        <h1 className="text-3xl font-bold tracking-tight">
          <span className="font-mono text-xl font-normal text-text-dim">{t.key}</span> <span data-free-text>{t.title}</span>
        </h1>
        <div className="ml-auto flex flex-wrap gap-2">
          {live.length > 0 ? (
            <Link to="/p/$project/terminal" params={{ project }} search={{ run: live[0]!.r.id }} className={button}>
              Jump into the running sandbox
            </Link>
          ) : (
            <Link to="/p/$project/terminal" params={{ project }} search={{ ticket: t.key }} className={button}>
              Terminal
            </Link>
          )}
          {!ended && (
            <button className={button} onClick={() => onPatch({ hold: !t.hold })}>
              {t.hold ? 'Let it go' : 'Put on hold'}
            </button>
          )}
          {!ended && (
            <select
              aria-label="Priority"
              className={button}
              value={t.priority}
              onChange={(e) => onPatch({ priority: Number(e.target.value) })}
            >
              {['urgent', 'high', 'normal', 'low'].map((n, i) => (
                <option key={n} value={i}>
                  priority {i}, {n}
                </option>
              ))}
            </select>
          )}
        </div>
      </div>
      <p className="mt-1 flex flex-wrap items-center gap-2 text-sm text-text-dim">
        <Tag>{t.kind}</Tag> priority {t.priority} · created {when(t.createdAt)} by {t.origin === 'plan' ? 'the breakdown' : t.createdBy} ·{' '}
        {t.fixRounds} fix {t.fixRounds === 1 ? 'round' : 'rounds'}
        {t.hold && <Tag tone="amber">on hold</Tag>}
        {t.approaches > 0 && <Tag tone="amber">the lead gave it a new approach</Tag>}
      </p>
      <ol className="mt-3 flex flex-wrap gap-1 text-xs" aria-label="Stages">
        {stages.map((s) => {
          const skipped = !s.on || t.skips.includes(s.id)
          const here = s.id === t.stage
          return (
            <li
              key={s.id}
              className={
                'rounded-full border px-2.5 py-0.5 ' +
                (here ? 'border-accent bg-accent-soft font-semibold text-accent' : skipped ? 'border-dashed text-text-dim line-through' : 'border-border bg-bg-raised')
              }
            >
              {stageName[s.id] ?? s.id}
            </li>
          )
        })}
        {t.stage === 'closed' && <li className="rounded-full border border-bad px-2.5 py-0.5 text-bad">Closed: {t.closedReason}</li>}
      </ol>
      <Failed error={error || failed} />
      {t.stage === 'analysis' && t.analysis.status === 'new_feature_notice' && (
        <section data-notice className="mt-4 rounded-lg border border-amber bg-bg-raised p-4">
          <h2 className="font-semibold">A new feature</h2>
          <p className="mt-1 text-sm">
            Analysis found that it adds something the approved plan does not cover. <span data-free-text>{t.analysis.reason}</span>{' '}
            {t.hold
              ? 'It is on hold: it does not go ahead by itself.'
              : t.noticeUntil
                ? `It goes ahead by itself ${when(t.noticeUntil)}, unless you discard it or hold it.`
                : 'It waits for you: new features of this project do not go ahead by themselves.'}
          </p>
          <div className="mt-3 flex flex-wrap gap-2">
            <button className={mainButton} onClick={() => onDecide('approve_now')}>
              Approve now
            </button>
            <button className={button} onClick={() => onDecide('discard')}>
              Discard
            </button>
            <button className={button} onClick={() => onDecide(t.hold ? 'release' : 'hold')}>
              {t.hold ? 'Lift the hold' : 'Hold'}
            </button>
          </div>
        </section>
      )}
      <div
        data-sticky-decision-header
        className="sticky top-0 z-10 mt-4 flex flex-wrap items-center justify-between gap-2 rounded-lg border border-border bg-bg-raised/95 px-3 py-2 backdrop-blur"
      >
        <div className="flex flex-wrap items-center gap-2 text-xs">
          <span className="font-mono font-semibold">{t.key}</span>
          <Tag tone={ended ? (t.stage === 'done' ? 'ok' : 'bad') : 'blue'}>{stageName[t.stage] ?? t.stage}</Tag>
          {activeRun && (
            <span className="text-text-dim">
              {roleName(activeRun.s.role)} · <CLIModel r={activeRun.r} />
            </span>
          )}
        </div>
        <div className="flex flex-wrap gap-1" role="tablist" aria-label="Task workspace views">
          {WORKSPACE_TABS.map((tab) => (
            <button
              key={tab.id}
              type="button"
              role="tab"
              aria-selected={activeTab === tab.id}
              onClick={() => onTabChange(tab.id)}
              className={
                'min-h-8 rounded-md px-2.5 py-1 text-xs transition-colors focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-accent ' +
                (activeTab === tab.id ? 'bg-text font-semibold text-bg' : 'text-text-dim hover:bg-bg-sunk hover:text-text')
              }
            >
              {tab.label}
            </button>
          ))}
        </div>
      </div>
    </>
  )
}
