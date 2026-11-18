import { createFileRoute, Link } from '@tanstack/react-router'
import { useState } from 'react'
import { getJSON, send } from '../lib/api'
import { usePoll } from '../lib/poll'
import { modeName, networkText, stageName } from '../lib/words'
import { button, Card, Failed, PageHead, Tag } from '../components/ui'
import { agentName, choiceName, fromWords, type Choice, type OfferedModel } from '../lib/models'
import { OverridesSection } from '../components/overrides'
import { ProjectSandboxCard } from '../components/project-sandbox-controls'
import { ProjectSkillsSection } from '../components/project-skills-editor'
import { ProjectEnd } from '../components/projectend'
import { Field, Problems, projectAPI, Stepper, useSettings, type Settings as Set } from '../components/settingfield'

export const Route = createFileRoute('/p/$project/settings')({ component: Settings })

type Stage = { id: string; optional?: boolean; on: boolean; role?: string; wip?: number; title?: string; agent?: string; prompt?: string; exit?: string[] }

// own says a stage is one the project added (design 7.4): its agent is defined on main and runs on the reviewer's model.
const isOwnStage = (s: Stage) => !!s.agent
type Waits = { waits: string }
type Agents = {
  roles: Record<string, Choice | Waits>
  own: Record<string, { model?: string; effort?: string } | boolean | string>
  offered: OfferedModel[]
  runs: { model: string; runs: number; roles: Record<string, number> }[]
  checkRule: boolean
}

const efforts = ['low', 'medium', 'high', 'xhigh', 'max']

const stageText: Record<string, string> = {
  planning: 'The breakdown waits for your OK here.',
  backlog: 'Accepted work waits to be analysed.',
  analysis: 'An agent sets why, what, goal, and checks, and decides.',
  in_progress: "A builder works on the ticket's branch.",
  review: 'Another agent reads the change.',
  test: 'Another agent runs the tests in a fresh sandbox.',
  e2e: 'An agent starts the whole application and drives its user flows.',
  verification: 'An agent checks the result against the goal and the plan.',
  done: 'Merged, summary written, changelog updated.',
}

const isChoice = (c: Choice | Waits | undefined): c is Choice => !!c && 'CLI' in c

// The project's Settings (design 14.1 to 14.3, 20.1, settings.png): the stages with their agents and
// limits, the project's own model and effort for each stage's agent (M8), and every other key of its
// settings in cards with its history and Put back (M17). The picture's budgets are gone (0.53).
function Settings() {
  const { project } = Route.useParams()
  const base = `/api/projects/${encodeURIComponent(project)}`
  const { data: stages, reload: reloadStages } = usePoll<{ stages: Stage[] }>(base + '/stages', 0)
  const { data, error, reload } = usePoll<Agents>(base + '/agents', 15000)
  const [failed, setFailed] = useState('')
  const ps = useSettings(projectAPI(project))
  const { data: overview, reload: reloadOverview } = usePoll<{ projects: { name: string; byStage: Record<string, number>; protected?: boolean; locked?: boolean }[] }>('/api/overview', 15000)
  const { data: projOverview, reload: reloadProj } = usePoll<{ project: { protected?: boolean }; locked?: boolean }>(base + '/overview', 15000)
  const ovProj = overview?.projects.find((p) => p.name === project)
  const byStage = ovProj?.byStage ?? {}
  const isLocked = !!(projOverview?.locked ?? ovProj?.locked)
  const isProtected = isLocked || !!(projOverview?.project?.protected ?? ovProj?.protected)
  // setLimit changes a stage's limit, its wip, in the project's settings (7.4).
  const setLimit = async (id: string, wip: number) => {
    await ps.change((d) => ({ ...d, stages: ((d.stages as Stage[] | undefined) ?? []).map((st) => (st.id === id ? { ...st, wip } : st)) }))
    reloadStages()
  }

  // set changes the project's own entry for a role: model, effort, or both; "" takes it away.
  const set = async (role: string, key: 'model' | 'effort', value: string) => {
    try {
      const doc = await getJSON<Record<string, unknown>>(base + '/settings')
      const agents = { ...((doc.agents as Record<string, unknown>) ?? {}) }
      const entry = { ...((agents[role] as Record<string, string>) ?? {}) }
      if (value) entry[key] = value
      else delete entry[key]
      if (Object.keys(entry).length) agents[role] = entry
      else delete agents[role]
      await send('PUT', base + '/settings', { ...doc, agents })
      setFailed('')
      reload()
    } catch (e) {
      setFailed((e as Error).message)
    }
  }
  // switchStage switches an optional stage on or off (design 7.4): it applies to the tickets that have not reached it yet.
  const switchStage = async (id: string, on: boolean) => {
    try {
      await send('PUT', base + '/stages', { [id]: on })
      setFailed('')
      reloadStages()
    } catch (e) {
      setFailed((e as Error).message)
    }
  }
  const setRule = async (on: boolean) => {
    try {
      const doc = await getJSON<Record<string, unknown>>(base + '/settings')
      await send('PUT', base + '/settings', { ...doc, agents: { ...((doc.agents as Record<string, unknown>) ?? {}), checks_use_another_family: on } })
      reload()
    } catch (e) {
      setFailed((e as Error).message)
    }
  }
  const own = (role: string) => (data?.own[role] && typeof data.own[role] === 'object' ? (data.own[role] as { model?: string; effort?: string }) : {})

  return (
    <div className="mx-auto max-w-6xl p-6">
      <PageHead
        crumbs={
          <>
            <Link to="/" className="text-accent">
              All projects
            </Link>{' '}
            / {project}
            {isProtected && (
              <span className="ml-2" data-protected={isLocked ? 'locked' : 'on'}>
                <Tag tone="blue">Protected</Tag>
              </span>
            )}
          </>
        }
        title="Settings"
      >
        These are the project's settings. They are rows in Crew's database. Every change is kept with the value before it, and applies to
        what starts after it.
      </PageHead>
      <Failed error={error || failed} />
      <Problems s={ps} />
      <h2 className="mb-2 text-xl font-semibold">
        Stages <span className="text-sm font-normal text-text-dim">a ticket only moves to the right; the optional stages sit between In progress and Done</span>
      </h2>
      <div className="overflow-x-auto">
        <table className="w-full min-w-[52rem] text-sm">
          <thead className="text-left text-xs text-text-dim uppercase">
            <tr>
              <th className="py-1 font-normal">Stage</th>
              <th className="font-normal" />
              <th className="font-normal">Agent</th>
              <th className="font-normal">Model</th>
              <th className="font-normal">Effort</th>
              <th className="font-normal">Limit</th>
              <th className="font-normal">Tickets now</th>
            </tr>
          </thead>
          <tbody>
            {stages?.stages.map((s) => {
              const c = s.role ? data?.roles[s.role] : undefined
              const mine = s.role ? own(s.role) : {}
              const off = s.optional && !s.on
              return (
                <tr key={s.id} className={'border-t border-border align-top ' + (off ? 'text-text-dim' : '')}>
                  <td className="py-2 pr-3">
                    <b>{s.title ?? stageName[s.id] ?? s.id}</b>
                    {isOwnStage(s) ? (
                      <p className="max-w-[16rem] text-xs text-text-dim">
                        The project's own stage. Its agent is <span className="font-mono">{s.agent}</span> on main; it exits on{' '}
                        {(s.exit ?? []).join(', ')}.
                      </p>
                    ) : (
                      <p className="max-w-[16rem] text-xs text-text-dim">{stageText[s.id]}</p>
                    )}
                  </td>
                  <td className="py-2 pr-3">
                    {s.optional ? (
                      <button
                        className={'rounded-full border px-2.5 py-0.5 text-xs ' + (s.on ? 'border-accent bg-accent-soft text-accent' : 'border-border text-text-dim')}
                        aria-pressed={s.on}
                        aria-label={`${s.title ?? stageName[s.id] ?? s.id}: ${s.on ? 'on' : 'off'}`}
                        onClick={() => switchStage(s.id, !s.on)}
                      >
                        {s.on ? 'On' : 'Off'}
                      </button>
                    ) : (
                      <span className="text-xs text-text-dim">always on</span>
                    )}
                  </td>
                  <td className="py-2 pr-3">{s.role ? agentName(s.role) : '—'}</td>
                  <td className="py-2 pr-3">
                    {isOwnStage(s) && <p className="max-w-[14rem] text-xs text-text-dim">The reviewer's model, on another family than the builder.</p>}
                    {s.role && data && !isOwnStage(s) && (
                      <>
                        <select aria-label={`The model of the ${s.role}`} className={button} value={mine.model ?? ''} onChange={(e) => set(s.role!, 'model', e.target.value)}>
                          <option value="">Default{isChoice(c) && !mine.model ? `: ${c.CLI} · ${c.Model}` : ''}</option>
                          {data.offered.map((o) => (
                            <option key={o.entry} value={o.entry}>
                              {o.cli} · {o.model}
                            </option>
                          ))}
                        </select>
                        <p className="mt-1 font-mono text-xs text-text-dim">{isChoice(c) ? `runs on ${choiceName(c)}, ${fromWords(c.ModelFrom)}` : (c as Waits | undefined)?.waits}</p>
                      </>
                    )}
                    {!s.role && '—'}
                  </td>
                  <td className="py-2 pr-3">
                    {s.role && !isOwnStage(s) && (
                      <select aria-label={`The effort of the ${s.role}`} className={button} value={mine.effort ?? ''} onChange={(e) => set(s.role!, 'effort', e.target.value)}>
                        <option value="">Default{isChoice(c) && !mine.effort ? `: ${c.Effort || 'none'}` : ''}</option>
                        {efforts.map((e) => (
                          <option key={e}>{e}</option>
                        ))}
                      </select>
                    )}
                  </td>
                  <td className="py-2 pr-3">
                    {s.wip !== undefined && (
                      <span data-stage-limit={s.id}>
                        <Stepper value={s.wip} name={`The limit of ${s.title ?? stageName[s.id] ?? s.id}`} onSave={(n) => setLimit(s.id, n)} />
                      </span>
                    )}
                  </td>
                  <td className="py-2" data-stage-now={s.id}>
                    {byStage[s.id] ?? 0}
                  </td>
                </tr>
              )
            })}
          </tbody>
        </table>
      </div>
      <p className="mt-2 text-xs text-text-dim">
        Every run is agy, on a Gemini or a Claude model. The model and the effort are settings per role: the defaults are the
        machine's, which the Models screen sets, and you can set others here or pin them on a ticket. The lists hold what the CLIs offer
        now. A stage's limit is how many of its tickets are worked at once. Switching a stage on applies to tickets that have not
        passed it yet; switching it off lets tickets skip it from now on.
      </p>
      <div className="mt-6 grid gap-4 md:grid-cols-2 lg:grid-cols-3">
        <WhoDecides s={ps} />
        <Limits s={ps} />
        <Card title="Curator and summary">
          {(['curator', 'summary'] as const).map((r) => {
            const c = data?.roles[r]
            return (
              <p key={r} className="mb-1 text-sm">
                {agentName(r)}: <span className="font-mono text-xs">{isChoice(c) ? `${choiceName(c)}, ${fromWords(c.ModelFrom)}` : (c as Waits | undefined)?.waits}</span>
              </p>
            )
          })}
        </Card>
        <Network base={base} />
        <SandboxesAndGit s={ps} />
        <ProjectSandboxCard project={project} />
        <Card title="The model for a run">
          <ol className="list-decimal space-y-1 pl-5 text-sm">
            <li>A model or an effort you pinned on the ticket wins.</li>
            <li>Otherwise what you set for the role in this project.</li>
            <li>Otherwise the machine's entry for the role: Crew's choice, or yours in the Models screen.</li>
            <li>A reviewer and a verifier then move to another family than the builder, if they landed on the same one.</li>
            <li>At the limit of its group of models, or when its CLI is signed out, a run takes its agent's next choice, or waits.</li>
          </ol>
          <p className="mt-3 text-sm">A check uses another model family than the build</p>
          <button className={button + ' mt-1'} onClick={() => data && setRule(!data.checkRule)} aria-pressed={data?.checkRule}>
            {data?.checkRule ? 'On' : 'Off'}
          </button>
        </Card>
        <Card title="Runs in this project so far">
          {data?.runs.length === 0 && <p className="text-sm text-text-dim">No run yet.</p>}
          <dl className="grid grid-cols-[1fr_auto] gap-x-3 gap-y-1 text-sm">
            {data?.runs.map((r) => (
              <div key={r.model} className="contents">
                <dt className="font-mono text-xs">{r.model}</dt>
                <dd>
                  {r.runs} {r.runs === 1 ? 'run' : 'runs'}
                  <span className="block text-xs text-text-dim">
                    {Object.entries(r.roles)
                      .map(([role, n]) => `${n} ${agentName(role).toLowerCase()}`)
                      .join(', ')}
                  </span>
                </dd>
              </div>
            ))}
          </dl>
          <p className="mt-2 text-xs text-text-dim">Each run records its model, its effort, and where they came from.</p>
        </Card>
        <DocsAndKnowledge s={ps} />
        <Hosting s={ps} />
        <ProjectEnd
          project={project}
          base={base}
          isProtected={isProtected}
          isLocked={isLocked}
          onChanged={() => {
            reloadProj()
            reloadOverview()
          }}
        />
      </div>
      <ProjectSkillsSection project={project} base={base} />
      <OverridesSection project={project} />
    </div>
  )
}

// Who decides (7.6, settings.png): the project OK is always yours; the rest is the project's choice.
function WhoDecides({ s }: { s: Set }) {
  return (
    <Card title="Who decides">
      <Field s={s} path="approvals.project" label="The project OK" words={{ operator: 'you' }} readOnly="Always yours." />
      <Field s={s} path="approvals.created_task" label="A ticket an agent creates" words={{ agent: 'another agent decides', operator: 'you' }} />
      <Field
        s={s}
        path="approvals.new_feature"
        label="A new feature from an agent"
        words={{ notice: 'a notice, then by itself', operator: 'you', agent: 'another agent decides' }}
      />
      <Field s={s} path="approvals.new_feature_wait_hours" label="A notice goes ahead after" unit="hours" />
      <Field s={s} path="approvals.agent_change" label="An override the lead proposes" words={{ auto: 'by itself when its evals are not worse', operator: 'you' }} note="One for a role that checks always waits for you." />
      <Field s={s} path="approvals.feature_specs" label="Feature specs (.crew/specs/NNN)" words={{ autonomous: 'autonomous within foundation and 13.8', manual: 'you' }} />
      <dl className="mt-2 grid grid-cols-[9rem_1fr] gap-x-3 gap-y-1 text-sm">
        <dt className="text-text-dim">An open choice</dt>
        <dd>the agent decides and records it</dd>
        <dt className="text-text-dim">A ticket you add</dt>
        <dd>approved by adding it</dd>
      </dl>
    </Card>
  )
}

// Limits (7.7, settings.png): how far a ticket and a run may go, in rounds, turns and minutes. No limit is in dollars (0.53).
function Limits({ s }: { s: Set }) {
  return (
    <Card title="Limits">
      <Field s={s} path="limits.max_active_tickets" label="Active tickets of this project at once" />
      <Field s={s} path="limits.max_fix_rounds" label="Fix rounds at most" note="Then the lead looks again: one new approach, then cancelled and replaced by other tickets." />
      <Field s={s} path="limits.max_added_share" label="Work agents may add, as a share of the plan's tickets" />
      <Field s={s} path="limits.run_timeout_minutes" label="A run's minutes at most" unit="min" />
      <Field s={s} path="limits.run_max_turns" label="A run's turns at most" />
      <Field s={s} path="limits.max_goal_rounds" label="Check-and-continue rounds in one run" />
      <Field s={s} path="limits.check_timeout_minutes" label="A check ends as an error after" unit="min" />
      <Field s={s} path="limits.max_added_subtasks" label="Subtasks an agent may add to a ticket" />
      <Field s={s} path="limits.max_parallel_subtasks" label="Subtasks of a ticket at once" />
      <p className="mt-3 text-xs tracking-wide text-text-dim uppercase">The watchdog</p>
      <Field s={s} path="watchdog.silent_warn_minutes" label="A silent run is warned after" unit="min" />
      <Field s={s} path="watchdog.silent_stop_minutes" label="A silent run is stopped after" unit="min" />
      <Field s={s} path="watchdog.repeat_fail_warn" label="The same failure warned after" unit="times" />
      <Field s={s} path="watchdog.repeat_fail_stop" label="The same failure stopped after" unit="times" />
      <Field s={s} path="watchdog.slow_stage_factor" label="A stage is slow at this many times its usual time" />
    </Card>
  )
}

function SandboxesAndGit({ s }: { s: Set }) {
  return (
    <div id="sandbox">
      <Card title="Sandboxes and git">
        <p className="mb-1 text-xs tracking-wide text-text-dim uppercase">Warm project microVM (crew-proj-&lt;id&gt;)</p>
        <Field s={s} path="sandbox.cpus" label="Virtual CPUs for the project microVM" note="Applies on the next start of the microVM (2 to 32)." />
        <Field s={s} path="sandbox.memory_gb" label="Memory for the project microVM" unit="GB" note="Applies on the next start of the microVM (2 to 64)." />
        <Field s={s} path="sandbox.disk_gb" label="Disk limit for persistent /home/agent" unit="GB" />
        <Field s={s} path="sandbox.suspend_after_minutes" label="Idle minutes before suspending (0% CPU)" unit="min" note="None uses the machine default." />
        <Field s={s} path="sandbox.stop_after_minutes" label="Idle minutes before stopping (0 MB RAM)" unit="min" note="None uses the machine default." />
        <Field s={s} path="sandbox.max_snapshots" label="Snapshots of /home/agent kept" note="None uses the machine default." />
        <p className="mt-3 mb-1 text-xs tracking-wide text-text-dim uppercase">Checks and git</p>
        <Field s={s} path="sandbox.setup" label="The project's setup, in a new sandbox" />
        <Field s={s} path="sandbox.test" label="The project's tests, before a merge" />
        <Field
          s={s}
          path="sandbox.check_network"
          label="The network of its check sandboxes"
          words={{ inherit: "the project's own", internet: 'Internet', model_only: 'Model only', lan: 'LAN', sources: 'Sources' }}
          fresh
        />
        <Field s={s} path="sandbox.github_read" label="Other repositories of yours its sandboxes may read" />
        <Field s={s} path="git.remote" label="The remote" readOnly="Set when the project was made." />
        <Field s={s} path="git.push" label="What is pushed to the remote" />
        <Field s={s} path="git.fetch_minutes" label="The remote is fetched every" unit="min" />
        <Field s={s} path="git.merge" label="A ticket is merged by" words={{ auto: 'a pull request when it can', merge_commit: 'a merge commit', pull_request: 'a pull request' }} />
      </Card>
    </div>
  )
}

function DocsAndKnowledge({ s }: { s: Set }) {
  return (
    <Card title="Documents and knowledge">
      <Field s={s} path="docs.root" label="The pages are on main in" />
      <Field s={s} path="docs.changelog" label="The changelog" />
      <Field s={s} path="docs.required" label="Every ticket names the pages it updates, or why not" />
      <Field s={s} path="knowledge.memory.index_max_chars" label="The memory's index at most" unit="characters" />
      <Field s={s} path="knowledge.feedback.max_per_run" label="Feedback lines from one run" />
      <Field s={s} path="knowledge.evals.repeats" label="Each eval case runs" unit="times" />
      <Field s={s} path="knowledge.evals.min_cases" label="Cases before a suite can tell" />
      <Field s={s} path="planning.rewrite" label="A better wording of a first message" words={{ inherit: "the machine's", suggest: 'proposed', off: 'off' }} note="Kept for later use: since 0.32 a proposal is made only for a plan draft." />
    </Card>
  )
}

// Hosting (section 19): how the project is hosted from this machine, which its Hosting screen starts.
// A change of the internet, the size, the port, the health address or the data folder starts a live
// project's image again at once (plan M19 D21).
function Hosting({ s }: { s: Set }) {
  return (
    <Card title="Hosting">
      <p className="mb-1 text-xs text-text-dim">
        The Hosting screen hosts the project. On a live project a change of the reach, the size, the port, the health check or the data folder starts it again at once.
      </p>
      <Field s={s} path="hosting.dockerfile" label="Built from" />
      <Field s={s} path="hosting.port" label="Port" />
      <Field s={s} path="hosting.health" label="Health check" />
      <Field s={s} path="hosting.data" label="The data folder is mounted at" />
      <Field s={s} path="hosting.size" label="Size" />
      <Field
        s={s}
        path="hosting.access"
        label="Reached by"
        words={{ private: 'the admin email', listed: 'listed addresses', google: 'any Google account', public: 'everyone' }}
        fresh
      />
      <Field s={s} path="hosting.internet" label="May reach the internet from inside" fresh />
      <Field s={s} path="hosting.allowed" label="The addresses of listed" />
      <Field s={s} path="hosting.hostname" label="An own domain" note="Not used yet: an own domain comes later, with hosting several containers." />
      <Field s={s} path="hosting.public_rps" label="Requests a second from one address, when public" />
      <Field s={s} path="hosting.log_days" label="Logs kept" unit="days" />
      <Field s={s} path="hosting.data_copies" label="Copies of the data folder kept" />
      <Field s={s} path="hosting.follow_main" label="Every ticket that reaches Done is deployed" />
    </Card>
  )
}

type NetworkState = { own: string; machine: string; mode: string }

const modes = ['internet', 'model_only', 'lan'] as const

const modeMore: Record<string, string> = {
  internet: 'An agent can read documentation and install packages. A ticket can be set to Model only.',
  model_only: 'An agent cannot install packages from the internet: a package mirror must be on the list of hosts.',
  lan: 'Set on purpose: the sandboxes reach what this machine reaches.',
}

// Network is the project's network mode (design 13.6, settings.png): its own, or the machine's. It
// applies to the sandboxes that start after it. Sandboxes never reach each other, in any mode.
function Network({ base }: { base: string }) {
  const { data, reload } = usePoll<NetworkState>(base + '/network', 0)
  const [failed, setFailed] = useState('')
  const set = async (mode: string) => {
    try {
      await send('PUT', base + '/network', { mode })
      setFailed('')
      reload()
    } catch (e) {
      setFailed(String(e))
    }
  }
  const own = data?.own && data.own !== 'inherit' ? data.own : ''
  return (
    <Card title="Network for this project's sandboxes">
      <div role="group" aria-label="Network mode" className="inline-flex overflow-hidden rounded border border-border">
        {modes.map((m) => (
          <button
            key={m}
            className={`px-3 py-1 text-sm ${data?.mode === m ? 'bg-text text-bg' : 'hover:bg-border/40'}`}
            aria-pressed={data?.mode === m}
            onClick={() => set(m)}
          >
            {modeName[m]}
          </button>
        ))}
      </div>
      {data && <p className="mt-3 text-sm">{networkText(data.mode)}</p>}
      {data && <p className="mt-2 text-xs text-text-dim">{modeMore[data.mode]} Sandboxes never reach each other, in any mode.</p>}
      {data && (
        <p className="mt-2 text-xs text-text-dim">
          {own ? (
            <>
              Set for this project. The machine's mode is {modeName[data.machine]}.{' '}
              <button className="underline" onClick={() => set('inherit')}>
                Use the machine's
              </button>
            </>
          ) : (
            <>The machine's mode, as this project sets none.</>
          )}{' '}
          A change applies to the sandboxes that start after it. LAN, set or taken away, asks for a fresh sign-in.
        </p>
      )}
      <Failed error={failed} />
    </Card>
  )
}
