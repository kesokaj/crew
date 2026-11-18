import { useState } from 'react'
import { send } from '../../lib/api'
import { usePoll } from '../../lib/poll'
import { Failed, button } from '../ui'

type DreamstoreEntry = {
  id: number
  number: number
  kind: 'episodic' | 'semantic' | 'procedural'
  title: string
  body: string
  paths: string[]
  validityScore: number
  verifiedCommit?: string
  hitPassCount: number
  contradictionCount: number
  status: 'active' | 'drift' | 'quarantined' | 'retired'
}

type DreamProposal = {
  id: number
  key: string
  kind: 'context_micro_adjustment' | 'skill_synthesis' | 'vendored_download'
  targetPath: string
  skillName?: string
  title: string
  rationale: string
  scanVerdict: 'PASS' | 'BLOCKED'
  scanFindings: string[]
  promoteShared: boolean
  state: 'pending_approval' | 'approved' | 'discarded' | 'blocked'
}

type CrossKnowledgeEntry = {
  id: number
  key: string
  kind: 'shared_skill' | 'advisory' | 'spec_pattern'
  sourceProjectName?: string
  skillName?: string
  title: string
  summary: string
  fencedBody: string
  validityScore: number
  scrubbed: boolean
}

export function DreamstorePanel({ project }: { project: string }) {
  const [failed, setFailed] = useState('')
  const [notice, setNotice] = useState('')
  const [showQuarantined, setShowQuarantined] = useState(true)
  const base = `/api/projects/${encodeURIComponent(project)}`
  const dreamstore = usePoll<{ entries: DreamstoreEntry[] }>(
    `${base}/dreamstore?includeQuarantined=${showQuarantined ? 'true' : 'false'}`,
    10000,
  )
  const proposals = usePoll<{ proposals: DreamProposal[] }>(`${base}/dream/proposals`, 10000)
  const cross = usePoll<{ entries: CrossKnowledgeEntry[] }>('/api/knowledge', 12000)

  const decideProposal = (key: string, decision: 'approve' | 'discard') => {
    setNotice('')
    send('POST', `${base}/dream/proposals/${encodeURIComponent(key)}/decision`, { decision }).then(
      () => {
        setFailed('')
        setNotice(`${key} ${decision === 'approve' ? 'approved onto main' : 'discarded'}.`)
        proposals.reload()
      },
      (e: Error) => setFailed(e.message),
    )
  }

  const applyKnowledge = (entry: CrossKnowledgeEntry) => {
    setNotice('')
    send<{ task?: { key?: string } }>('POST', `${base}/knowledge/${encodeURIComponent(entry.key)}/act`, {
      createTicket: false,
    }).then(
      (res) => {
        setFailed('')
        const tKey = res?.task?.key ?? entry.key
        setNotice(`Minted Tasks API item ${tKey} for ${entry.key}.`)
      },
      (e: Error) => setFailed(e.message),
    )
  }

  const dsEntries = dreamstore.data?.entries ?? []
  const visibleEntries = showQuarantined
    ? dsEntries
    : dsEntries.filter((e) => e.status !== 'quarantined' && e.validityScore >= 0.4)
  const propList = proposals.data?.proposals ?? []
  const crossList = cross.data?.entries ?? []

  return (
    <section className="mb-6 grid gap-4 lg:grid-cols-3" aria-label="Dreamstore, Scoped Dreaming, and Cross-Project Knowledge">
      <div className="rounded-md border border-border bg-bg-raised p-4">
        <div className="mb-2 flex flex-wrap items-center justify-between gap-2">
          <h2 className="text-sm font-semibold text-text">Dreamstore</h2>
          <div className="flex items-center gap-2">
            <label className="flex items-center gap-1.5 text-xs text-text-dim">
              <input
                type="checkbox"
                data-testid="dreamstore-quarantine-filter"
                checked={showQuarantined}
                onChange={(e) => setShowQuarantined(e.target.checked)}
              />
              Quarantined (&lt; 0.40)
            </label>
            <span className="rounded-full bg-bg-sunk px-2 py-0.5 text-xs text-text-dim">{visibleEntries.length}</span>
          </div>
        </div>
        <Failed error={dreamstore.error || failed} />
        {notice && <p className="mb-2 text-xs text-accent">{notice}</p>}
        {visibleEntries.length === 0 ? (
          <p className="text-xs text-text-dim">No typed episodic, semantic, or procedural Dreamstore entries yet.</p>
        ) : (
          <ul className="space-y-2 text-xs">
            {visibleEntries.slice(0, 6).map((e) => {
              const quarantined = e.status === 'quarantined' || e.validityScore < 0.4
              const pct = Math.max(4, Math.min(100, Math.round(e.validityScore * 100)))
              return (
                <li key={e.id} data-testid={`dreamstore-entry-${e.number}`} className="rounded border border-border/70 bg-bg p-2">
                  <div className="flex flex-wrap items-center justify-between gap-1">
                    <span className="font-mono text-text-dim">D{String(e.number).padStart(4, '0')} · {e.kind}</span>
                    <span className={quarantined ? 'rounded bg-danger/15 px-1.5 py-0.5 font-medium text-danger' : 'font-mono text-text-dim'}>
                      {e.validityScore.toFixed(2)}{quarantined ? ' quarantined' : ''}
                    </span>
                  </div>
                  <p className="mt-1 font-medium text-text" data-free-text="">{e.title}</p>
                  <div className="mt-1.5 flex items-center gap-2">
                    <svg viewBox="0 0 100 6" className="h-1.5 w-20 rounded bg-bg-sunk" aria-hidden="true">
                      <rect
                        x="0"
                        y="0"
                        width={pct}
                        height="6"
                        rx="3"
                        data-testid={`dreamstore-bar-${e.number}`}
                        data-validity={e.validityScore.toFixed(2)}
                        className={quarantined ? 'fill-danger' : 'fill-accent'}
                      />
                    </svg>
                    <span className="text-text-dim">{e.hitPassCount} pass · {e.contradictionCount} conflict</span>
                  </div>
                  {e.paths.length > 0 && (
                    <p className="mt-1 truncate font-mono text-[11px] text-text-dim" data-free-text="">{e.paths.join(', ')}</p>
                  )}
                </li>
              )
            })}
          </ul>
        )}
      </div>

      <div className="rounded-md border border-border bg-bg-raised p-4">
        <div className="mb-2 flex items-center justify-between gap-2">
          <h2 className="text-sm font-semibold text-text">Scoped Dreaming</h2>
          <span className="rounded-full bg-bg-sunk px-2 py-0.5 text-xs text-text-dim">{propList.length}</span>
        </div>
        {proposals.error && <Failed error={proposals.error} />}
        {propList.length === 0 ? (
          <p className="text-xs text-text-dim">No pending context micro-adjustments or synthesized skill proposals.</p>
        ) : (
          <ul className="space-y-2 text-xs">
            {propList.slice(0, 5).map((p) => (
              <li key={p.key} data-testid={`dream-proposal-${p.key}`} className="rounded border border-border/70 bg-bg p-2">
                <div className="flex flex-wrap items-center justify-between gap-1">
                  <span className="font-mono text-text-dim">{p.key} · {p.state}</span>
                  <span className={p.scanVerdict === 'BLOCKED' ? 'rounded bg-danger/15 px-1.5 py-0.5 font-medium text-danger' : 'rounded bg-bg-sunk px-1.5 py-0.5 text-text-dim'}>
                    vet {p.scanVerdict}
                  </span>
                </div>
                <p className="mt-1 font-medium text-text" data-free-text="">{p.title}</p>
                <p className="mt-0.5 truncate font-mono text-[11px] text-text-dim" data-free-text="">{p.targetPath}</p>
                {p.state === 'pending_approval' && p.scanVerdict === 'PASS' && (
                  <div className="mt-2 flex gap-2">
                    <button
                      type="button"
                      data-testid={`dream-approve-${p.key}`}
                      className={button}
                      onClick={() => decideProposal(p.key, 'approve')}
                    >
                      Approve
                    </button>
                    <button
                      type="button"
                      data-testid={`dream-discard-${p.key}`}
                      className={button}
                      onClick={() => decideProposal(p.key, 'discard')}
                    >
                      Discard
                    </button>
                  </div>
                )}
              </li>
            ))}
          </ul>
        )}
      </div>

      <div className="rounded-md border border-border bg-bg-raised p-4">
        <div className="mb-2 flex items-center justify-between gap-2">
          <h2 className="text-sm font-semibold text-text">Knowledge API</h2>
          <span className="rounded-full bg-bg-sunk px-2 py-0.5 text-xs text-text-dim">{crossList.length}</span>
        </div>
        {cross.error && <Failed error={cross.error} />}
        {crossList.length === 0 ? (
          <p className="text-xs text-text-dim">No installation-wide scrubbed and fenced advisories yet.</p>
        ) : (
          <ul className="space-y-2 text-xs">
            {crossList.slice(0, 5).map((k) => (
              <li key={k.key} className="rounded border border-border/70 bg-bg p-2">
                <div className="flex flex-wrap items-center justify-between gap-1">
                  <span className="font-mono text-text-dim">{k.key} · {k.kind}</span>
                  <span className="rounded bg-bg-sunk px-1.5 py-0.5 text-[11px] text-text-dim">
                    {k.scrubbed ? 'scrubbed · fenced' : 'fenced'}
                  </span>
                </div>
                <p className="mt-1 font-medium text-text" data-free-text="">{k.title}</p>
                {k.summary && (
                  <p className="mt-0.5 text-[11px] text-text-dim" data-free-text="">{k.summary}</p>
                )}
                {k.sourceProjectName && (
                  <p className="mt-0.5 text-[11px] text-text-dim">From {k.sourceProjectName}</p>
                )}
                {k.fencedBody && (
                  <pre
                    data-testid={`knowledge-fenced-${k.key}`}
                    data-free-text=""
                    className="mt-1.5 max-h-28 overflow-auto rounded border border-border/60 bg-bg-sunk p-1.5 font-mono text-[11px] whitespace-pre-wrap text-text-dim"
                  >
                    {k.fencedBody}
                  </pre>
                )}
                <div className="mt-2">
                  <button
                    type="button"
                    data-testid={`knowledge-apply-${k.key}`}
                    className={button}
                    onClick={() => applyKnowledge(k)}
                  >
                    Apply via Tasks API
                  </button>
                </div>
              </li>
            ))}
          </ul>
        )}
      </div>
    </section>
  )
}
