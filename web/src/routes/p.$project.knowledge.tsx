import { createFileRoute, Link } from '@tanstack/react-router'
import type { Bug, Decision, FeedbackPiece, MemoryEntry } from '../lib/api'
import { usePoll } from '../lib/poll'
import { PageHead } from '../components/ui'
import { BugsTab, MemoryTab } from '../components/knowledge/memory-bugs-tabs'
import { DecisionsTab, FeedbackTab } from '../components/knowledge/feedback-decisions-tabs'
import { EvalsTab, type Evals } from '../components/knowledge/evals-tab'
import { DreamstorePanel } from '../components/knowledge/dreamstore-panel'

export type KnowledgeTab = 'memory' | 'bugs' | 'feedback' | 'evals' | 'decisions'

export function validateKnowledgeSearch(s: Record<string, unknown>): { tab?: KnowledgeTab } {
  return ['memory', 'bugs', 'feedback', 'evals', 'decisions'].includes(s.tab as string) ? { tab: s.tab as KnowledgeTab } : {}
}

export const Route = createFileRoute('/p/$project/knowledge')({
  validateSearch: validateKnowledgeSearch,
  component: Knowledge,
})

// Knowledge (section 9, knowledge.png): what the project learned, in tabs. Memory, bugs, and feedback
// came with M12; evals with M15.
function Knowledge() {
  const { project } = Route.useParams()
  const { tab = 'memory' } = Route.useSearch()
  const base = `/api/projects/${encodeURIComponent(project)}`
  const memory = usePoll<{ entries: MemoryEntry[] }>(`${base}/memory`, 10000)
  const bugs = usePoll<{ bugs: Bug[] }>(`${base}/bugs`, 10000)
  const feedback = usePoll<{ feedback: FeedbackPiece[] }>(`${base}/feedback`, 10000)
  const decisions = usePoll<{ decisions: Decision[] }>(`${base}/decisions`, 10000)
  const evals = usePoll<Evals>(`${base}/evals`, 10000)
  const active = memory.data?.entries.filter((e) => e.status === 'active') ?? []
  const tabs: [KnowledgeTab, string, number | string][] = [
    ['memory', 'Memory', active.length],
    ['bugs', 'Bugs', bugs.data?.bugs.length ?? 0],
    ['feedback', 'Feedback', feedback.data?.feedback.length ?? 0],
    ['evals', 'Evals', evals.data?.cases.filter((c) => c.status !== 'retired').length ?? 0],
    ['decisions', 'Decisions', decisions.data?.decisions.length ?? 0],
  ]
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
        title="Knowledge"
      >
        What {project} has learned. Dreamstore scores entries (0.00–1.00 validity; &lt; 0.40 quarantined) inside this project boundary.
        Cross-factory advisories pass through secret scrubbing and <code>&lt;crew_data_fence&gt;</code> isolation.
      </PageHead>
      <div className="mb-3 flex flex-wrap items-center gap-2 rounded-md border border-border bg-bg-raised px-3 py-2 text-xs text-text-dim">
        <span className="font-semibold text-text">Dreamstore Gate:</span>
        <span>validity &gt;= 0.40 active</span>
        <span>·</span>
        <span>Cross-project advisories scrubbed &amp; fenced (Rule 14)</span>
      </div>
      <DreamstorePanel project={project} />
      <div className="mb-4 flex gap-1 border-b border-border" role="tablist">
        {tabs.map(([id, label, n]) => (
          <Link
            key={id}
            to="/p/$project/knowledge"
            params={{ project }}
            search={{ tab: id }}
            role="tab"
            aria-selected={id === tab}
            className={'px-3 py-2 ' + (id === tab ? 'border-b-2 border-text font-semibold' : 'text-text-dim hover:text-text')}
          >
            {label} <span className="rounded-full bg-bg-sunk px-1.5 text-xs">{n}</span>
          </Link>
        ))}
      </div>
      {tab === 'memory' && <MemoryTab project={project} entries={memory.data?.entries} error={memory.error} reload={memory.reload} />}
      {tab === 'bugs' && <BugsTab project={project} bugs={bugs.data?.bugs} error={bugs.error} reload={bugs.reload} />}
      {tab === 'feedback' && <FeedbackTab project={project} list={feedback.data?.feedback} error={feedback.error} reload={() => (feedback.reload(), memory.reload())} />}
      {tab === 'evals' && <EvalsTab project={project} evals={evals.data} error={evals.error} reload={evals.reload} />}
      {tab === 'decisions' && <DecisionsTab project={project} list={decisions.data?.decisions} error={decisions.error} reload={decisions.reload} />}
    </div>
  )
}
