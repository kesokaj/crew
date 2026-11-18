import { createFileRoute, Link } from '@tanstack/react-router'
import { useState, type FormEvent } from 'react'
import { send } from '../lib/api'
import { usePoll } from '../lib/poll'
import { Card, Failed, PageHead, Tag, ago, button, mainButton } from '../components/ui'

export const Route = createFileRoute('/p/$project/specs')({ component: ProjectSpecs })

type Spec = {
  id: number
  specKey: string
  path: string
  tier: string
  title: string
  bodyMarkdown: string
  state: string
  approvalMode: string
  decidedBy?: string
  escalationReason?: string
  touchedPaths: string[]
  altersFoundation: boolean
  threadRef?: string
  emittedKeys: string[]
  updatedAt: string
}

type SpecDecision = {
  key: string
  specKey?: string
  askingRole?: string
  decidedBy: string
  question: string
  choice: string
  alternatives: string[]
  rationale: string
  state: string
  artifactPath: string
  createdAt: string
}

type WorkflowView = {
  mainSha: string
  rawMarkdown: string
  harness: string
  maxTries: number
  maxAgentHops: number
  featureSpecs: string
  syncedAt: string
  heldBranches: { ticketKey: string; branch: string; approved: boolean }[]
}

function ProjectSpecs() {
  const { project } = Route.useParams()
  const enc = encodeURIComponent(project)
  const specsPoll = usePoll<{ specs: Spec[] }>(`/api/projects/${enc}/specs`, 6000)
  const decPoll = usePoll<{ decisions: SpecDecision[] }>(`/api/projects/${enc}/spec-decisions`, 8000)
  const wfPoll = usePoll<WorkflowView>(`/api/projects/${enc}/workflow`, 10000)

  const [selectedKey, setSelectedKey] = useState('')
  const [askTitle, setAskTitle] = useState('')
  const [askQuestion, setAskQuestion] = useState('')
  const [askPaths, setAskPaths] = useState('')
  const [actionErr, setActionErr] = useState('')
  const [busy, setBusy] = useState(false)

  const specs = specsPoll.data?.specs ?? []
  const selected = specs.find((s) => s.specKey === selectedKey) ?? specs[0]
  const decisions = decPoll.data?.decisions ?? []
  const wf = wfPoll.data

  const runAction = async (fn: () => Promise<unknown>) => {
    setBusy(true)
    setActionErr('')
    try {
      await fn()
      specsPoll.reload()
      decPoll.reload()
      wfPoll.reload()
    } catch (e) {
      setActionErr((e as Error).message)
    } finally {
      setBusy(false)
    }
  }

  const submitAsk = (e: FormEvent) => {
    e.preventDefault()
    if (!askQuestion.trim()) return
    const touchedPaths = askPaths
      .split(',')
      .map((s) => s.trim())
      .filter(Boolean)
    void runAction(async () => {
      await send('POST', `/api/projects/${enc}/specs/ask`, {
        spec: selected && selected.tier !== 'foundation' ? selected.specKey : '',
        title: askTitle.trim() || 'Feature clarification',
        askingRole: '@builder',
        question: askQuestion.trim(),
        touchedPaths,
      })
      setAskTitle('')
      setAskQuestion('')
      setAskPaths('')
    })
  }

  return (
    <div className="mx-auto max-w-7xl space-y-5 p-6">
      <PageHead
        crumbs={
          <>
            <Link to="/" className="text-accent">
              All projects
            </Link>{' '}
            / <span data-free-text>{project}</span>
          </>
        }
        title="Specs & WORKFLOW"
        actions={
          <div className="flex flex-wrap gap-2">
            <Link to="/p/$project/factory" params={{ project }} className={button}>
              Factory Line
            </Link>
            <Link to="/p/$project/chat" params={{ project }} className={button}>
              Project Chat
            </Link>
            <button
              type="button"
              className={button}
              disabled={busy}
              onClick={() => void runAction(() => send('POST', `/api/projects/${enc}/workflow/sync`, {}))}
            >
              Sync WORKFLOW.md from main
            </button>
          </div>
        }
      >
        Tier-1 <code>000-foundation.md</code> stays gated on @user Project OK (Rule 12). Tier-2 feature specs support autonomous{' '}
        <code>@agent -&gt; @planner</code> decisions within <code>13.8</code> bounds.
      </PageHead>

      <Failed error={specsPoll.error || decPoll.error || wfPoll.error || actionErr} />

      <div className="grid gap-4 lg:grid-cols-[320px_minmax(0,1fr)]">
        <div className="space-y-4">
          <Card title="Specification Tiers">
            {specs.length === 0 ? (
              <p className="text-sm text-text-dim">No specs initialized yet.</p>
            ) : (
              <ul className="space-y-2">
                {specs.map((sp) => (
                  <li key={sp.specKey}>
                    <button
                      type="button"
                      onClick={() => setSelectedKey(sp.specKey)}
                      className={
                        'w-full rounded-md border p-2.5 text-left text-sm transition-colors ' +
                        (selected?.specKey === sp.specKey
                          ? 'border-accent bg-accent-soft text-accent'
                          : 'border-border bg-bg hover:bg-bg-sunk')
                      }
                    >
                      <div className="flex items-center justify-between gap-2">
                        <span className="font-mono text-xs font-semibold">{sp.specKey}</span>
                        <Tag tone={sp.state === 'approved' ? 'ok' : sp.state === 'escalated' ? 'amber' : 'blue'}>{sp.state}</Tag>
                      </div>
                      <p className="mt-1 truncate font-medium" data-free-text>
                        {sp.title}
                      </p>
                      <p className="mt-0.5 truncate font-mono text-[11px] text-text-dim" data-free-text>
                        {sp.path}
                      </p>
                    </button>
                  </li>
                ))}
              </ul>
            )}
          </Card>

          <Card title="Ask @planner on Feature Spec">
            <form onSubmit={submitAsk} className="space-y-2.5 text-sm">
              <input
                aria-label="Feature spec title"
                placeholder="Spec title (e.g. Session eviction)"
                value={askTitle}
                onChange={(e) => setAskTitle(e.target.value)}
                className="w-full rounded-md border border-border bg-bg px-2.5 py-1.5"
              />
              <textarea
                aria-label="Design question"
                rows={3}
                placeholder="Design question for @planner…"
                value={askQuestion}
                onChange={(e) => setAskQuestion(e.target.value)}
                className="w-full rounded-md border border-border bg-bg px-2.5 py-1.5"
              />
              <input
                aria-label="Touched paths"
                placeholder="Touched paths (comma separated)"
                value={askPaths}
                onChange={(e) => setAskPaths(e.target.value)}
                className="w-full rounded-md border border-border bg-bg px-2.5 py-1.5 font-mono text-xs"
              />
              <button type="submit" disabled={busy || !askQuestion.trim()} className={mainButton}>
                Ask @planner
              </button>
            </form>
          </Card>
        </div>

        <div className="space-y-4">
          {selected && (
            <Card
              title={`${selected.specKey} · ${selected.tier === 'foundation' ? 'Tier 1 Foundation' : 'Tier 2 Feature Spec'}`}
              right={
                <div className="flex flex-wrap items-center gap-2">
                  <Tag tone={selected.state === 'approved' ? 'ok' : 'amber'}>{selected.state}</Tag>
                  {selected.tier !== 'foundation' && selected.state === 'escalated' && (
                    <>
                      <button
                        type="button"
                        disabled={busy}
                        className={mainButton}
                        onClick={() =>
                          void runAction(() => send('POST', `/api/projects/${enc}/specs/${selected.specKey}/decision`, { choice: 'approve' }))
                        }
                      >
                        Approve
                      </button>
                      <button
                        type="button"
                        disabled={busy}
                        className={button}
                        onClick={() =>
                          void runAction(() => send('POST', `/api/projects/${enc}/specs/${selected.specKey}/decision`, { choice: 'discard' }))
                        }
                      >
                        Discard
                      </button>
                    </>
                  )}
                </div>
              }
            >
              <div className="mb-3 flex flex-wrap items-center gap-2 text-xs text-text-dim">
                <span>Approval: {selected.approvalMode}</span>
                {selected.decidedBy && <span data-free-text>· Decided by: {selected.decidedBy}</span>}
                {selected.emittedKeys.length > 0 && <span>· Backlog: {selected.emittedKeys.join(', ')}</span>}
              </div>
              {selected.escalationReason && (
                <p className="mb-3 rounded border border-amber bg-amber-soft p-2 text-xs text-amber-text" data-free-text>
                  Escalation reason: {selected.escalationReason}
                </p>
              )}
              <pre className="max-h-72 overflow-auto rounded border border-border bg-bg p-3 font-mono text-xs leading-relaxed" data-free-text>
                {selected.bodyMarkdown}
              </pre>
            </Card>
          )}

          <Card title="Recorded Decisions (DEC-*)">
            {decisions.length === 0 ? (
              <p className="text-sm text-text-dim">No feature decisions recorded yet.</p>
            ) : (
              <div className="space-y-2">
                {decisions.map((d) => (
                  <div key={d.key} className="rounded border border-border bg-bg p-3 text-sm">
                    <div className="flex flex-wrap items-center justify-between gap-2">
                      <span className="font-mono text-xs font-semibold text-accent">
                        {d.key} · {d.specKey || 'spec'}
                      </span>
                      <span className="text-xs text-text-dim">
                        {d.decidedBy} · {ago(d.createdAt)}
                      </span>
                    </div>
                    <p className="mt-1 font-medium" data-free-text>
                      {d.choice}
                    </p>
                    <p className="text-xs text-text-dim" data-free-text>
                      {d.rationale}
                    </p>
                  </div>
                ))}
              </div>
            )}
          </Card>

          <Card title="WORKFLOW.md Runtime Contract (Pinned to main · 13.8)" right={wf && <Tag mono>{wf.mainSha.slice(0, 8) || 'head'}</Tag>}>
            {!wf ? (
              <p className="text-sm text-text-dim">Loading WORKFLOW.md…</p>
            ) : (
              <div className="space-y-2">
                <div className="flex flex-wrap gap-3 text-xs text-text-dim">
                  <span>Harness: {wf.harness}</span>
                  <span>max_tries: {wf.maxTries}</span>
                  <span>max_agent_hops: {wf.maxAgentHops}</span>
                  <span>feature_specs: {wf.featureSpecs}</span>
                  {wf.heldBranches.length > 0 && <Tag tone="amber">{wf.heldBranches.length} held 13.8 branch</Tag>}
                </div>
                <pre className="max-h-60 overflow-auto rounded border border-border bg-bg p-3 font-mono text-xs" data-free-text>
                  {wf.rawMarkdown || 'No WORKFLOW.md committed on main yet.'}
                </pre>
              </div>
            )}
          </Card>
        </div>
      </div>
    </div>
  )
}
