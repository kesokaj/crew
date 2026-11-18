import { createFileRoute, Link } from '@tanstack/react-router'
import { useState, type ReactNode } from 'react'
import { send } from '../lib/api'
import { usePoll } from '../lib/poll'
import { seenIn } from '../lib/evidence'
import { button, Card, Failed, PageHead, Tag, when } from '../components/ui'
import { tokenCount, tokens } from '../lib/tokens'
import { HBars, pct, shareOf, Stat, type Bar } from '../components/bars'
import { iNum, status, type Improvement } from '../components/improvement'
import { GlobalHarnessEditorSection, type BaselineFileItem, type GlobalHarnessEntry } from '../components/harness-editor'
import { agentName } from '../lib/models'
import { cut, roleName, stageName } from '../lib/words'

type Tab = 'signals' | 'improvements' | 'baseline'

export const Route = createFileRoute('/crew')({
  validateSearch: (s: Record<string, unknown>): { tab?: Tab } =>
    ['signals', 'improvements', 'baseline'].includes(s.tab as string) ? { tab: s.tab as Tab } : {},
  component: CrewLevel,
})

type Share = { n: number; of: number; share: number }
type Kind = { kind: string; label: string; row: string; counted: boolean; all: number; byProject: Record<string, number>; noProject: number }
type Version = { version: string; running: boolean; tickets: number; firstRequest: Share; fixRoundsPerTicket: number; failedMerge: Share; tokensPerTicket: number }
type ProjectResult = {
  key: string
  name: string
  done: number
  firstRequest: Share
  fixRoundsPerTicket: number
  tokensPerTicket: number
  interventions: number
  escapedDefects: number
  createdDiscarded: number
}
type PromptChoice = { choice: string; plans: number; questions: number; sentBack: number; passedFirst: Share; enough: boolean }
export type CrewAnalytics = {
  computedAt: string
  version: string
  versionNote: string
  done: number
  projects: number
  interventions: number
  interventionsPer100: number
  interventionsByWhat: Bar[]
  firstRequest: Share
  projectKeys: { key: string; name: string }[]
  kinds: Kind[]
  latest: { at: string; project: string; kind: string; label: string; text: string; ticket?: string | null; version: string }[]
  versions: Version[] | null
  mixed: number
  comparison: ProjectResult[]
  // Use is tokens in and out, and time (design 0.53: no cost).
  use: { perTicket: number; minutesPerTicket: number; byRole: Bar[]; byStage: Bar[]; byModel: Bar[]; byVersion: Bar[] }
  prompts: { plans: PromptChoice[]; improve: { used: number; edited: number; refused: number } }
}
// The Crew level (10.7, crew.png, improvements.png, crew--baseline.png): what Crew sees across every
// project. Agents in projects cannot see it.
function CrewLevel() {
  const { tab = 'signals' } = Route.useSearch()
  const { data: c, error } = usePoll<CrewAnalytics>('/api/crew/analytics', 30000)
  const tabs: [Tab, string][] = [
    ['signals', 'Signals'],
    ['improvements', 'Improvements'],
    ['baseline', 'Baseline'],
  ]
  return (
    <div className="mx-auto max-w-6xl p-6">
      <PageHead title="Crew level">
        Crew sees every project. Signals go up from the projects, the baseline comes down to all of them, and nothing goes from one project to
        another.
      </PageHead>
      <Failed error={error} />
      {c && (
        <div className="mb-5 grid gap-3 md:grid-cols-3">
          <Stat
            title="Times you stepped in"
            value={c.interventionsPer100.toFixed(1)}
            note={`per 100 finished tickets · ${c.interventions} in total`}
            data="interventions"
          />
          <Stat
            title="Checks passed on the first request"
            value={shareOf(c.firstRequest)}
            note={
              c.firstRequest.of
                ? `on Crew ${c.version}, ${c.firstRequest.of} ${c.firstRequest.of === 1 ? 'ticket' : 'tickets'}`
                : `no ticket has run on Crew ${c.version} yet`
            }
            data="first-request"
          />
          <Stat
            title="Crew version"
            value={c.version}
            note={c.versionNote ? <span data-free-text>{c.versionNote}</span> : 'the version that runs now'}
            data="version"
          />
          <Stat title="Finished tickets" value={c.done} note={`across ${c.projects} ${c.projects === 1 ? 'project' : 'projects'}`} data="finished" />
        </div>
      )}
      <div className="mb-4 flex gap-1 border-b border-border" role="tablist">
        {tabs.map(([id, label]) => (
          <Link
            key={id}
            to="/crew"
            search={{ tab: id }}
            role="tab"
            aria-selected={id === tab}
            className={'px-3 py-2 ' + (id === tab ? 'border-b-2 border-text font-semibold' : 'text-text-dim hover:text-text')}
          >
            {label}
          </Link>
        ))}
      </div>
      {tab === 'signals' && c && <SignalsTab c={c} />}
      {tab === 'improvements' && <ImprovementsTab />}
      {tab === 'baseline' && c && <BaselineTab c={c} />}
    </div>
  )
}

function Head({ children }: { children: ReactNode }) {
  return <thead className="text-left text-xs tracking-wide text-text-dim uppercase">{children}</thead>
}

// whatMax is how much of a signal's What a line of Latest signals shows.
const whatMax = 200

// The Signals tab: the signals by kind, in every project and in each, the newest of them, and the
// numbers of 10.7 beside them: use (tokens and time), the projects side by side, and the prompts.
function SignalsTab({ c }: { c: CrewAnalytics }) {
  const byKind = [...c.kinds].sort((a, b) => b.all - a.all)
  // Plans and the improver record signals of no project; their column is there only when one is, so
  // every row adds up to All.
  const noProject = c.kinds.some((k) => k.noProject > 0)
  return (
    <div className="grid grid-cols-[minmax(0,1fr)] gap-5">
      <div className="grid gap-3 md:grid-cols-[minmax(0,1fr)_minmax(0,2fr)]">
        <Card title="Signals by kind, all projects">
          <HBars bars={byKind.map((k) => ({ label: k.label, value: k.all }))} wide />
        </Card>
        <section className="min-w-0 rounded-lg border border-border bg-bg-raised p-4">
          <h2 className="mb-3 text-xs tracking-wide text-text-dim uppercase">Signals by kind and project</h2>
          <div className="overflow-x-auto">
            <table className="w-full text-sm" data-matrix>
              <Head>
                <tr className="border-b border-border">
                  <th className="py-1">Signal</th>
                  {c.projectKeys.map((p) => (
                    <th key={p.key} className="text-right" title={p.name}>
                      {p.key}
                    </th>
                  ))}
                  {noProject && (
                    <th className="text-right" title="Plans and the improver">
                      No project
                    </th>
                  )}
                  <th className="text-right">All</th>
                </tr>
              </Head>
              <tbody>
                {c.kinds.map((k) => (
                  <tr key={k.kind} className="border-b border-border/60" data-kind={k.kind}>
                    <td className="py-1.5">{k.label}</td>
                    {c.projectKeys.map((p) => (
                      <td key={p.key} className={'text-right tabular-nums ' + (k.byProject[p.key] ? '' : 'text-text-dim')}>
                        {k.byProject[p.key] ?? 0}
                      </td>
                    ))}
                    {noProject && (
                      <td className={'text-right tabular-nums ' + (k.noProject ? '' : 'text-text-dim')}>{k.noProject}</td>
                    )}
                    <td className="text-right font-bold tabular-nums">{k.all}</td>
                  </tr>
                ))}
              </tbody>
            </table>
          </div>
          <p className="mt-2 text-xs text-text-dim">Crew records these from events and commits. Agents do not send them.</p>
        </section>
      </div>
      <section>
        <h2 className="mb-2 text-xl font-bold">Latest signals</h2>
        <table className="w-full text-sm" data-latest>
          <Head>
            <tr className="border-b border-border">
              <th className="py-1 pr-3">When</th>
              <th className="pr-3">Project</th>
              <th className="pr-3">Signal</th>
              <th className="pr-3">What</th>
              <th className="pr-3">Ticket</th>
              <th>Version</th>
            </tr>
          </Head>
          <tbody>
            {c.latest.map((s, i) => (
              <tr key={i} className="border-b border-border/60 align-top">
                <td className="py-1.5 pr-3 font-mono text-xs whitespace-nowrap">{when(s.at)}</td>
                <td className="py-1.5 pr-3 font-mono text-xs whitespace-nowrap">{s.project}</td>
                <td className="py-1.5 pr-3 whitespace-nowrap">{s.label}</td>
                {/* A long line is cut, with the whole text on hover (0.2.6 on dave: the improver's note filled ten lines). */}
                <td className="py-1.5 pr-3" title={Array.from(s.text).length > whatMax ? s.text : undefined} data-free-text>
                  {cut(s.text, whatMax)}
                </td>
                <td className="py-1.5 pr-3 whitespace-nowrap">
                  {s.ticket && (
                    <TicketLink c={c} ticket={s.ticket} />
                  )}
                </td>
                <td className="py-1.5 font-mono text-xs whitespace-nowrap">{s.version}</td>
              </tr>
            ))}
          </tbody>
        </table>
        {c.latest.length === 0 && <p className="text-text-dim">No signal yet.</p>}
      </section>
      <div className="grid gap-3 md:grid-cols-3">
        <Card title="Use per ticket">
          <p className="mb-2 text-2xl font-bold">{tokens(c.use.perTicket)}</p>
          <p className="mb-3 text-sm text-text-dim">{c.use.minutesPerTicket ? `${Math.round(c.use.minutesPerTicket)} minutes on average` : 'no ticket timed yet'}</p>
          <HBars bars={c.use.byRole.map((b) => ({ label: agentName(b.label), value: b.value }))} show={tokenCount} />
        </Card>
        <Card title="Tokens by stage">
          <HBars bars={c.use.byStage.map((b) => ({ label: stageName[b.label] ?? roleName(b.label), value: b.value }))} show={tokenCount} />
        </Card>
        <Card title="Tokens by model and effort">
          <HBars bars={c.use.byModel} show={tokenCount} wide />
          <p className="mt-3 text-xs text-text-dim">
            Each agent's results on each model, and every change of a model with who made it, are on{' '}
            <Link to="/models" className="text-accent underline">
              Models
            </Link>
            .
          </p>
        </Card>
      </div>
      <section>
        <h2 className="mb-2 text-xl font-bold">The projects side by side</h2>
        <table className="w-full text-sm" data-comparison>
          <Head>
            <tr className="border-b border-border">
              <th className="py-1">Project</th>
              <th className="text-right">Done</th>
              <th className="text-right">Passed on the first request</th>
              <th className="text-right">Fix rounds per ticket</th>
              <th className="text-right">Tokens per ticket</th>
              <th className="text-right">You stepped in</th>
              <th className="text-right">Bugs after Done</th>
              <th className="text-right">Created and discarded</th>
            </tr>
          </Head>
          <tbody>
            {c.comparison.map((p) => (
              <tr key={p.key} className="border-b border-border/60">
                <td className="py-1.5">
                  <Link to="/p/$project/analytics" params={{ project: p.name }} className="text-accent underline">
                    {p.name}
                  </Link>
                </td>
                <td className="text-right tabular-nums">{p.done}</td>
                <td className="text-right tabular-nums">{shareOf(p.firstRequest)}</td>
                <td className="text-right tabular-nums">{p.done ? p.fixRoundsPerTicket.toFixed(2) : '—'}</td>
                <td className="text-right tabular-nums">{p.done ? tokenCount(p.tokensPerTicket) : '—'}</td>
                <td className="text-right tabular-nums">{p.interventions}</td>
                <td className="text-right tabular-nums">{p.escapedDefects}</td>
                <td className="text-right tabular-nums">{p.createdDiscarded}</td>
              </tr>
            ))}
          </tbody>
        </table>
      </section>
      <section>
        <h2 className="mb-2 text-xl font-bold">Prompts</h2>
        <p className="mb-2 max-w-3xl text-sm text-text-dim">
          What followed plans that started from the prompt writer's rewrite, from your edit of it, and from your own text. With one developer
          these are few: Crew draws no conclusion from fewer than ten plans of a kind.
        </p>
        <table className="w-full text-sm" data-prompts>
          <Head>
            <tr className="border-b border-border">
              <th className="py-1">The first message was</th>
              <th className="text-right">Plans</th>
              <th className="text-right">Questions the planning agent asked</th>
              <th className="text-right">Breakdowns sent back</th>
              <th className="text-right">Tickets that passed the first time</th>
            </tr>
          </Head>
          <tbody>
            {c.prompts.plans.map((p) => (
              <tr key={p.choice} className="border-b border-border/60">
                <td className="py-1.5">{{ rewrite: 'the rewrite', edited: 'an edited rewrite', mine: 'your own text' }[p.choice] ?? p.choice}</td>
                <td className="text-right tabular-nums">{p.plans}</td>
                <td className="text-right tabular-nums">{p.questions}</td>
                <td className="text-right tabular-nums">{p.sentBack}</td>
                <td className="text-right tabular-nums">
                  {p.passedFirst.n} of {p.passedFirst.of}
                  {!p.enough && p.plans > 0 && <span className="ml-1 text-xs text-text-dim">(too few to tell)</span>}
                </td>
              </tr>
            ))}
          </tbody>
        </table>
        <p className="mt-2 text-sm text-text-dim">
          Improve text: used {c.prompts.improve.used}, edited {c.prompts.improve.edited}, kept your own {c.prompts.improve.refused}.
        </p>
      </section>
    </div>
  )
}

// A ticket key links to its ticket, in the project its key names.
function TicketLink({ c, ticket }: { c: CrewAnalytics; ticket: string }) {
  const key = ticket.split('-')[0]
  const project = c.projectKeys.find((p) => p.key === key)?.name
  if (!project) return <span className="font-mono text-xs">{ticket}</span>
  return (
    <Link to="/p/$project/tickets/$key" params={{ project, key: ticket }} className="font-mono text-xs text-accent underline">
      {ticket}
    </Link>
  )
}

// The Improvements tab (improvements.png): what the improver proposed, where each one is, and what it
// measured. Each opens to its evidence, its diff, and its evals.
function ImprovementsTab() {
  const { data, error, reload } = usePoll<{ improvements: Improvement[]; improverLastRun?: string | null }>('/api/crew/improvements', 10000)
  const [asked, setAsked] = useState('')
  const [failed, setFailed] = useState('')
  const run = () =>
    send('POST', '/api/crew/improver/run', {}).then(
      () => (setAsked('Asked: the improver runs when a lead slot is free.'), setFailed(''), reload()),
      (e: Error) => setFailed(e.message),
    )
  return (
    <div>
      <p className="mb-3 max-w-4xl text-text-dim">
        The improver reads the signals of every project and proposes the smallest change to the baseline. Each proposal passes a generic check and
        the evals. If the evals are not worse, it ships in the next Crew release without waiting for you. You get the notice of that release, and
        can hold it or take the change out. A change that measures worse afterwards is rolled back by Crew.
      </p>
      <div className="mb-4 flex items-center gap-3">
        <button className={button} onClick={run}>
          Run the improver now
        </button>
        {data?.improverLastRun && <span className="text-sm text-text-dim">Last run {when(data.improverLastRun)}</span>}
        {asked && <span className="text-sm text-text-dim">{asked}</span>}
      </div>
      <Failed error={error || failed} />
      <CrewProject reload={reload} />
      {data && data.improvements.length === 0 && <p className="text-text-dim">No improvement yet.</p>}
      {data && data.improvements.length > 0 && (
        <table className="w-full text-sm" data-improvements>
          <Head>
            <tr className="border-b border-border">
              <th className="py-1"></th>
              <th>Improvement</th>
              <th>Changes</th>
              <th>From signals in</th>
              <th>Status</th>
              <th>Measured</th>
            </tr>
          </Head>
          <tbody>
            {data.improvements.map((x) => (
              <tr key={x.number} className="border-b border-border/60 align-top" data-improvement={x.number}>
                <td className="py-2 pr-2 font-mono text-xs text-text-dim">{iNum(x.number)}</td>
                <td className="py-2 pr-2">
                  <Link to="/crew/improvements/$n" params={{ n: String(x.number) }} className="hover:underline">
                    {x.title}
                  </Link>
                </td>
                <td className="py-2 pr-2 font-mono text-xs">{x.target.join(' ')}</td>
                <td className="py-2 pr-2">
                  {seenIn(x.evidence)}
                </td>
                <td className="py-2 pr-2">{status(x)}</td>
                <td className="py-2">
                  {x.result ? (
                    <>
                      {pct(x.result.before)} → {pct(x.result.after)}
                      <p className={x.result.verdict === 'worse' ? 'text-bad' : 'text-accent'}>{x.result.verdict}</p>
                    </>
                  ) : (
                    ''
                  )}
                </td>
              </tr>
            ))}
          </tbody>
        </table>
      )}
    </div>
  )
}

// CrewProject is Crew's own project (16.2): the one the improvements become tickets on, made once
// from updates.source_repo by its button.
function CrewProject({ reload }: { reload: () => void }) {
  const { data, error, reload: again } = usePoll<{ project?: string | null; sourceRepo?: string | null }>('/api/crew/project', 0)
  const [failed, setFailed] = useState('')
  const [making, setMaking] = useState(false)
  const make = () => {
    setMaking(true)
    send('POST', '/api/crew/project', {}).then(
      () => (setMaking(false), setFailed(''), again(), reload()),
      (e: Error) => (setMaking(false), setFailed(e.message)),
    )
  }
  if (!data) return <Failed error={error} />
  return (
    <div className="mb-4 text-sm" data-crew-project={data.project ?? ''}>
      {data.project ? (
        <p>
          Crew&apos;s project is{' '}
          <Link to="/p/$project/board" params={{ project: data.project }} className="text-accent underline">
            {data.project}
          </Link>
          , from {data.sourceRepo}. A passed improvement becomes a ticket there.
        </p>
      ) : data.sourceRepo ? (
        <div className="flex items-center gap-3">
          <button className={button} onClick={make} disabled={making}>
            Make Crew&apos;s project
          </button>
          <span className="text-text-dim">
            Imports {data.sourceRepo} as a project. Passed improvements become its tickets; your own pushes to main are taken as work from outside.
          </span>
        </div>
      ) : (
        <p className="text-text-dim">This machine builds no Crew: updates.source_repo is empty, so there is no Crew&apos;s project.</p>
      )}
      <Failed error={failed} />
    </div>
  )
}

type BaselineView = {
  version: string
  files: BaselineFileItem[]
  harness: Record<string, number>
  harnessFiles?: GlobalHarnessEntry[]
  versions: { version: string; installed?: string | null; running: boolean; note: string }[]
}

// The Baseline tab (crew--baseline.png): the results by Crew version, the versions with what each
// changed, and what the baseline holds.
function BaselineTab({ c }: { c: CrewAnalytics }) {
  const { data: b, error, reload } = usePoll<BaselineView>('/api/crew/baseline', 0)
  const groups = new Map<string, string[]>()
  for (const f of b?.files ?? []) {
    const parts = f.path.split('/')
    const name = parts[1] ?? ''
    if (parts.length > 2) groups.set(name + '/', [...(groups.get(name + '/') ?? []), parts.slice(2).join('/')])
    else groups.set(name, [])
  }
  return (
    <div className="grid grid-cols-[minmax(0,1fr)] gap-3">
      <Failed error={error} />
      <div className="grid gap-3 md:grid-cols-[minmax(0,2fr)_minmax(0,1fr)]">
        <section className="min-w-0 rounded-lg border border-border bg-bg-raised p-4">
          <h2 className="mb-3 text-xs tracking-wide text-text-dim uppercase">Results by Crew version, all projects</h2>
          <div className="overflow-x-auto">
            <table className="w-full text-sm" data-versions>
              <Head>
                <tr className="border-b border-border">
                  <th className="py-1">Version</th>
                  <th className="text-right">Tickets</th>
                  <th className="text-right">Checks passed on the first request</th>
                  <th className="text-right">Fix rounds per ticket</th>
                  <th className="text-right">Failed the merge check</th>
                  <th className="text-right">Tokens per ticket</th>
                </tr>
              </Head>
              <tbody>
                {(c.versions ?? []).map((v) => (
                  <tr key={v.version} className="border-b border-border/60" data-version={v.version}>
                    <td className="py-1.5 font-mono">
                      {v.version} {v.running && <Tag>running</Tag>}
                    </td>
                    <td className="text-right tabular-nums">{v.tickets}</td>
                    <td className="text-right tabular-nums">{shareOf(v.firstRequest)}</td>
                    <td className="text-right tabular-nums">{v.fixRoundsPerTicket.toFixed(2)}</td>
                    <td className="text-right tabular-nums">{shareOf(v.failedMerge)}</td>
                    <td className="text-right tabular-nums">{tokenCount(v.tokensPerTicket)}</td>
                  </tr>
                ))}
              </tbody>
            </table>
          </div>
          <p className="mt-2 text-sm text-text-dim">
            Every run records the version it used. With few tickets, a difference between two versions is often noise.
            {c.mixed > 0 && ` ${c.mixed} ${c.mixed === 1 ? 'ticket ran' : 'tickets ran'} on more than one version and ${c.mixed === 1 ? 'counts' : 'count'} on none.`}
          </p>
        </section>
        <Card title="Versions">
          <div className="grid gap-2">
            {(b?.versions ?? []).map((v) => (
              <div key={v.version}>
                <span className="font-mono text-sm">{v.version}</span>{' '}
                <span className="text-xs text-text-dim">{v.installed ? when(v.installed) : 'not installed'}</span>
                {v.running && (
                  <>
                    {' '}
                    <Tag>running</Tag>
                  </>
                )}
                {v.note && <p className="text-sm">{v.note}</p>}
              </div>
            ))}
            {b && b.versions.length === 0 && <p className="text-sm text-text-dim">This version was built from a checkout and has no release.</p>}
          </div>
          <Link to="/versions" className={button + ' mt-3 inline-block'}>
            Versions, update, and roll back
          </Link>
        </Card>
      </div>
      <Card title="What the baseline holds">
        <pre className="overflow-x-auto rounded-md bg-bg-sunk p-3 text-xs leading-5 whitespace-pre-wrap break-words" data-tree>
          {`harness/          AGENTS.md (the global context), ${b?.harness.skill ?? 0} skills, ${b?.harness.agent ?? 0} subagents, and ${b?.harness.rule ?? 0} ${
            b?.harness.rule === 1 ? 'rule' : 'rules'
          }\n                  built into the runtime image\nbaseline/\n`}
          {[...groups.entries()].map(([dir, files]) => `  ${dir.padEnd(14)}${files.join('  ')}\n`).join('')}
          {`improver.md       the improver's own definition: not part of the baseline, and protected\n`}
        </pre>
        <p className="mt-2 text-sm text-text-dim">
          Both are folders in Crew's own repository and part of the Crew release, so a baseline version is a Crew version. Every project runs the
          baseline of the running version and can override parts for itself. The files of the roles that check, and the prompt writer's rules, are
          changed only with you.
        </p>
      </Card>
      {b && <GlobalHarnessEditorSection files={b.files} harnessFiles={b.harnessFiles ?? []} onSaved={reload} />}
    </div>
  )
}
