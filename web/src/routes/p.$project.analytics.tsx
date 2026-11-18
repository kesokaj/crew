import { createFileRoute, Link } from '@tanstack/react-router'
import { usePoll } from '../lib/poll'
import { Card, Failed, PageHead, Rows } from '../components/ui'
import { Columns, HBars, pct, shareOf, Stat, type Bar } from '../components/bars'
import { tokenCount, tokens } from '../lib/tokens'

export const Route = createFileRoute('/p/$project/analytics')({ component: Analytics })

type Share = { n: number; of: number; share: number }
export type ProjectAnalytics = {
  computedAt: string
  done: number
  tickets: number
  passedFirst: Share
  firstRequest: Share
  fixRounds: number
  fixRoundsPerTicket: number
  // Use is tokens in and out, and time (design 0.53: no cost). Per ticket is per ticket done.
  tokensIn: number
  tokensOut: number
  tokensPerTicket: number
  minutesPerTicket: number
  donePerDay: Bar[]
  stageMinutes: Bar[]
  tokensByRole: Bar[]
  tokensByModel: Bar[]
  tokensByPart: Bar[]
  // added: the tickets agents added, as the scope guard counts them, of the approved plan's tickets.
  created: { total: number; approved: number; skipped: number; discarded: number; notice: number; added: Share; byRole: Bar[] }
  docs: Share
  bugs: { open: number; fixed: number; closed: number }
  quality: { lookedAgain: number; cancelled: number; watchdogWarnings: number; watchdogStops: number; escapedDefects: number; pagesBehind: number }
  feedbackByRole: Bar[]
  waiting: { operator: number; leadSlot: number; room: number; analysis: number }
  sandboxStartSeconds: number
  evals: Bar[]
  evalsUnmeasured: number
}

const stageWords: Record<string, string> = {
  planning: 'Planning', backlog: 'Backlog', analysis: 'Analysis', in_progress: 'In progress', review: 'Review', test: 'Test', e2e: 'E2E',
  verification: 'Verification',
}
const roleWords: Record<string, string> = {
  builder: 'Builder', lead: 'Lead', reviewer: 'Reviewer', tester: 'Tester', e2e: 'E2E tester', verifier: 'Verifier', summary: 'Summary writer',
  judge: 'Judge', planner: 'Planner', prompt: 'Prompt writer', stage: 'Own stage',
}

// Analytics (9.7, analytics.png): flow, quality, and use (tokens and time) of one project, from its
// summaries, run records, moves, and signals. Agents read the same numbers with get_metrics.
function Analytics() {
  const { project } = Route.useParams()
  const base = `/api/projects/${encodeURIComponent(project)}`
  const { data: a, error } = usePoll<ProjectAnalytics>(`${base}/analytics`, 30000)
  const settings = usePoll<{ limits?: { max_added_share?: number } }>(`${base}/settings`, 0)
  const allowed = settings.data?.limits?.max_added_share
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
        title="Analytics"
      >
        Computed from this project's summaries, run records, and commits. Agents can query the same numbers.
      </PageHead>
      <Failed error={error} />
      {a && (
        <div className="grid gap-3">
          <div className="grid gap-3 md:grid-cols-3">
            <Stat title="Tickets done" value={a.done} note={`of ${a.tickets}`} data="done" />
            <Stat title="Passed first time" value={shareOf(a.passedFirst)} note="no failed check and no fix round" data="passed-first" />
            <Stat
              title="Fix rounds per ticket"
              value={a.fixRoundsPerTicket.toFixed(2)}
              note={`${a.fixRounds} ${a.fixRounds === 1 ? 'fix' : 'fixes'}, each in the stage that asked for it`}
              data="fix-rounds"
            />
            <Stat
              title="Use"
              value={tokens(a.tokensIn + a.tokensOut)}
              note={
                a.done
                  ? `${tokenCount(a.tokensPerTicket)} tokens and ${Math.round(a.minutesPerTicket)} min per ticket done`
                  : 'in and out, of every run; no ticket done yet'
              }
              data="use"
            />
          </div>
          <div className="grid gap-3 md:grid-cols-3">
            <Card title="Tickets done per day">
              <Columns bars={a.donePerDay} />
            </Card>
            <Card title="Minutes in each stage, average">
              <HBars bars={a.stageMinutes.map((b) => ({ label: stageWords[b.label] ?? b.label, value: Math.round(b.value) }))} show={(v) => `${v} min`} />
            </Card>
            <Card title="Tokens by agent">
              <HBars bars={a.tokensByRole.map((b) => ({ label: roleWords[b.label] ?? b.label, value: b.value }))} show={tokenCount} />
            </Card>
          </div>
          <div className="grid gap-3 md:grid-cols-3">
            <Card title="Tickets agents created">
              <p className="mb-3 text-3xl font-bold">{a.created.total}</p>
              <Rows
                rows={[
                  ['Approved', String(a.created.approved)],
                  ['Skipped', String(a.created.skipped)],
                  ['Discarded', String(a.created.discarded)],
                  ['New features, with a notice', String(a.created.notice)],
                  [
                    'Added to the plan',
                    a.created.added.of
                      ? `${a.created.added.n} of the plan's ${a.created.added.of}, ${pct(a.created.added.share)}${allowed !== undefined ? ` · ${pct(allowed)} allowed` : ''}`
                      : `${a.created.added.n}, and no plan's tickets to count them against`,
                  ],
                ]}
              />
            </Card>
            <Card title="Documentation that is current">
              <p className="mb-3 text-3xl font-bold" data-docs>
                {a.docs.n} of {a.docs.of}
              </p>
              <span className="block h-1.5 rounded-full bg-bg-sunk">
                <span className="block h-full rounded-full bg-text" style={{ width: `${a.docs.of ? (a.docs.n / a.docs.of) * 100 : 0}%` }} />
              </span>
              <p className="mt-3 text-sm text-text-dim">A page falls behind when a ticket changes the code it covers and not the page.</p>
            </Card>
            <Card title="Bugs">
              <p className="mb-3 text-3xl font-bold">{a.bugs.open + a.bugs.fixed + a.bugs.closed}</p>
              <Rows
                rows={[
                  ['Open', String(a.bugs.open)],
                  ['Fixed', String(a.bugs.fixed)],
                  ['Closed in analysis', String(a.bugs.closed)],
                ]}
              />
            </Card>
          </div>
          <div className="grid gap-3 md:grid-cols-3">
            <Card title="What went another way">
              <Rows
                rows={[
                  ['Checks passed on the first request', `${shareOf(a.firstRequest)} of ${a.firstRequest.of}`],
                  ['Looked at again', String(a.quality.lookedAgain)],
                  ['Cancelled', String(a.quality.cancelled)],
                  ['Bugs found after Done', String(a.quality.escapedDefects)],
                  ['Watchdog warnings and stops', `${a.quality.watchdogWarnings} and ${a.quality.watchdogStops}`],
                  ['Code changed without its page', String(a.quality.pagesBehind)],
                ]}
              />
            </Card>
            <Card title="Waiting now">
              <Rows
                rows={[
                  ['For you', String(a.waiting.operator)],
                  ['For a lead slot', String(a.waiting.leadSlot)],
                  ['For room', String(a.waiting.room)],
                  ['For analysis', String(a.waiting.analysis)],
                  ['Sandbox start-up', a.sandboxStartSeconds ? `${Math.round(a.sandboxStartSeconds)} s on average` : '—'],
                ]}
              />
            </Card>
            <Card title="Tokens by model and effort">
              {a.tokensByModel.length > 0 ? <HBars bars={a.tokensByModel} show={tokenCount} wide /> : <p className="text-sm text-text-dim">No run yet.</p>}
            </Card>
            <Card title="Tokens by part">
              {a.tokensByPart.length > 0 ? (
                <HBars bars={a.tokensByPart} show={tokenCount} wide free />
              ) : (
                <p className="text-sm text-text-dim">No ticket of a part has run yet.</p>
              )}
            </Card>
          </div>
          <div className="grid gap-3 md:grid-cols-3">
            <Card title="Feedback by role">
              {a.feedbackByRole.length > 0 ? (
                <HBars bars={a.feedbackByRole.map((b) => ({ label: roleWords[b.label] ?? b.label, value: b.value }))} />
              ) : (
                <p className="text-sm text-text-dim">No feedback yet.</p>
              )}
            </Card>
            <Card title="Tickets created by role">
              {a.created.byRole.length > 0 ? (
                <HBars bars={a.created.byRole.map((b) => ({ label: roleWords[b.label] ?? b.label, value: b.value }))} />
              ) : (
                <p className="text-sm text-text-dim">No ticket was created by an agent.</p>
              )}
            </Card>
            <Card title="Evals, share of runs that met the goal">
              {a.evals.length > 0 && <HBars bars={a.evals.map((b) => ({ label: b.label, value: Math.round(b.value * 100) }))} show={(v) => `${v}%`} wide />}
              {/* A suite that measured no case is no share of zero (M15 on dave): it is counted, not drawn. */}
              {a.evalsUnmeasured > 0 && (
                <p className="text-sm text-text-dim" data-unmeasured>
                  {a.evalsUnmeasured === 1 ? '1 suite' : `${a.evalsUnmeasured} suites`} measured nothing{a.evals.length > 0 ? ', not counted here' : ''}.
                </p>
              )}
              {a.evals.length === 0 && (
                <p className="text-sm text-text-dim">
                  {a.evalsUnmeasured > 0 ? 'No eval measured a result yet.' : 'No eval run yet.'}{' '}
                  <Link to="/p/$project/knowledge" params={{ project }} search={{ tab: 'evals' }} className="text-accent underline">
                    The eval cases
                  </Link>
                </p>
              )}
            </Card>
          </div>
          <p className="text-xs text-text-dim">Computed {new Date(a.computedAt).toLocaleTimeString()}, and again after five minutes.</p>
        </div>
      )}
    </div>
  )
}
