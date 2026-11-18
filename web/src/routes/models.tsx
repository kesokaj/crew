import { createFileRoute, Link } from '@tanstack/react-router'
import { useState } from 'react'
import { send } from '../lib/api'
import { usePoll } from '../lib/poll'
import { tokenCount } from '../lib/tokens'
import { button, Card, Failed, mainButton, PageHead, Rows, Tag, when } from '../components/ui'
import {
  agentDoes,
  agentName,
  choiceName,
  familyName,
  pct,
  reasonFor,
  type AgentView,
  type CLIModels,
  type ModelChoice,
  type ModelRow,
  type ModelsPage,
  type Numbers,
} from '../lib/models'

export const Route = createFileRoute('/models')({ component: Models })

const efforts = ['low', 'medium', 'high', 'xhigh', 'max']

// perTicket is the time a unit took on average: seconds, then minutes. Since 0.53 time and results,
// and no cost, are what the numbers show (12.3).
const perTicket = (seconds: number, units: number) =>
  !units ? '—' : seconds < 60 ? `${Math.round(seconds)} s` : `${Math.round(seconds / 60)} min`

// The Models screen (design 12.3, 20.1): every model the CLIs offer, by CLI and family; one model
// opened; the agents with what each runs on and its next choices, which can be changed here; what
// Crew chose, with Go back and Set it myself, and what it would choose for an agent you set; the
// comparisons; which model each agent uses in each project; and the results.
function Models() {
  const { data, error, reload } = usePoll<ModelsPage>('/api/crew/models', 15000)
  const [open, setOpen] = useState<{ cli: string; model: string } | null>(null)
  const [failed, setFailed] = useState('')
  const act = (p: Promise<unknown>) =>
    p.then(
      () => {
        setFailed('')
        reload()
      },
      (e: Error) => setFailed(e.message),
    )
  return (
    <div className="mx-auto max-w-6xl p-6">
      <PageHead
        title="Models"
        actions={
          <>
            <button className={button} onClick={() => act(send('POST', '/api/crew/models/refresh'))}>
              Refresh
            </button>
            <Link to="/clis" className={button}>
              Agent CLIs
            </Link>
          </>
        }
      >
        Every model <code>agy</code> lists on your subscription, Gemini and Claude models alike. Crew is made of several agents, and
        each runs on a model and an effort of its own.
      </PageHead>
      <Failed error={error || failed} />
      {data && (
        <>
          <WhatCrewChose data={data} act={act} />
          <h2 className="mt-8 mb-1 text-xl font-semibold">Models the CLIs offer</h2>
          <p className="mb-3 text-sm text-text-dim">
            <code>agy</code> prints its list with <code>agy models</code>, which makes no model request. Crew reads it after every sign-in,
            at every check, and when you press Refresh. A model agy no longer offers stays here, marked, so old runs still show what they
            used.
          </p>
          <div className="grid gap-4 md:grid-cols-2">
            {data.clis.map((c) => (
              <CLIList key={c.cli} c={c} agents={data.agents} onOpen={(model) => setOpen({ cli: c.cli, model })} act={act} />
            ))}
          </div>
          {open && <OneModel cli={open.cli} model={open.model} onClose={() => setOpen(null)} />}
          <Agents data={data} act={act} />
          <ByProject data={data} />
          <Results results={data.results} />
        </>
      )}
    </div>
  )
}

type Act = (p: Promise<unknown>) => void

function WhatCrewChose({ data, act }: { data: ModelsPage; act: Act }) {
  const auto = data.agents.filter((a) => data.autoRoles.includes(a.role))
  const cards = data.choices.filter((c) => (c.kind === 'change' && (c.state === 'watching' || c.state === 'kept')) || (c.kind === 'recommend' && c.state === 'open'))
  return (
    <>
      <h2 className="mb-1 text-xl font-semibold">What Crew chose</h2>
      <p className="mb-3 text-sm text-text-dim">
        For the builder Crew chooses the CLI, the model, and the effort from its own numbers on this machine, tries others with one
        ticket in {Math.round(1 / (data.auto.exploreShare || 0.1))}, and tells you what it changed. Your own choice always wins. The agents
        that check are chosen the same way, one run at a time: by fewer defects that escaped them, with a share of their runs that
        asked for a fix close to the one they run on. Results alone decide.
      </p>
      <Card title="Who chooses" right={<Tag tone={data.auto.on ? 'blue' : undefined}>{data.auto.on ? 'Automatic choice: on' : 'Automatic choice: off'}</Tag>}>
        <table className="w-full text-sm">
          <tbody>
            {auto.map((a) => (
              <tr key={a.role} className="border-t border-border first:border-0">
                <td className="py-1.5 pr-3">{agentName(a.role)}</td>
                <td className="py-1.5 pr-3 font-mono text-xs">{a.runsOn ? choiceName(a.runsOn) : <span className="text-amber-text">{a.waits}</span>}</td>
                <td className="py-1.5 text-right">
                  {a.state === 'automatic' ? (
                    <button className={button} onClick={() => act(send('PUT', `/api/crew/agents/${a.role}/choice`, { state: 'set_by_you' }))}>
                      Automatic
                    </button>
                  ) : (
                    <button className={button} onClick={() => act(send('PUT', `/api/crew/agents/${a.role}/choice`, { state: 'automatic' }))}>
                      Set by you · back to Automatic
                    </button>
                  )}
                  {!data.chosen.includes(a.role) && <span className="ml-2 text-xs text-text-dim">follows its list</span>}
                </td>
              </tr>
            ))}
          </tbody>
        </table>
      </Card>
      <div className="mt-3 grid gap-3">
        {cards.length === 0 && <p className="text-sm text-text-dim">Crew has changed nothing yet. It needs {data.auto.minRuns} tickets on two entries first.</p>}
        {cards.map((c) => (
          <ChoiceCard key={c.id} c={c} act={act} />
        ))}
      </div>
      <Compare data={data} act={act} />
    </>
  )
}

function ChoiceCard({ c, act }: { c: ModelChoice; act: Act }) {
  const n = c.numbers
  const rec = c.kind === 'recommend'
  return (
    <section className={'rounded-lg border p-4 ' + (rec ? 'border-amber' : 'border-accent')}>
      <div className="mb-2 flex items-center gap-2 text-xs tracking-wide text-text-dim uppercase">
        {agentName(c.role)} · {rec ? 'set by you' : 'automatic'}
        <span className="ml-auto normal-case">
          <Tag tone={rec ? 'amber' : 'blue'}>
            {rec ? `Crew would choose: ${reasonFor(c.role, c.reason)}` : `Crew changed it ${when(c.at)}, ${c.state === 'watching' ? 'watching' : 'and kept it'}`}
          </Tag>
        </span>
      </div>
      <table className="w-full text-sm">
        <thead className="text-left text-xs text-text-dim">
          <tr>
            <th className="font-normal" />
            <th className="font-normal">CLI, model, effort</th>
            <th className="font-normal">Tickets</th>
            <th className="font-normal">Passed</th>
            <th className="font-normal">Rounds</th>
            <th className="font-normal">Time</th>
            <th className="font-normal">Checked by</th>
          </tr>
        </thead>
        <tbody>
          {[
            [rec ? 'Now' : 'Before', n.from, n.fromStats],
            [rec ? "Crew's pick" : 'Now', n.to, n.toStats],
          ].map(([label, p, s]) => {
            const pick = p as ModelChoice['from']
            const st = s as ModelChoice['numbers']['fromStats']
            return (
              <tr key={label as string} className="border-t border-border">
                <td className="py-1 pr-2">{label as string}</td>
                <td className="py-1 pr-2 font-mono text-xs">
                  {pick.entry.replace('/', ' · ')} · {pick.effort}
                </td>
                <td>{st.units}</td>
                <td>{pct(st.passed, st.units)}</td>
                <td>{st.fixRounds.toFixed(1)}</td>
                <td>{perTicket(st.seconds, st.units)}</td>
                <td>{(st.checkers ?? []).join(', ') || '—'}</td>
              </tr>
            )
          })}
        </tbody>
      </table>
      <p className="mt-2 text-xs text-text-dim">
        Passed: the ticket's command checks passed Crew's own check sandbox at the first request for a move. Rounds: fix rounds for each
        ticket. Time: how long a ticket took, on average.
      </p>
      <div className="mt-3 flex flex-wrap gap-2">
        {rec ? (
          <>
            <button className={mainButton} onClick={() => act(send('POST', `/api/crew/model-choices/${c.id}/use`))}>
              Use it
            </button>
            <button className={button} onClick={() => act(send('PUT', `/api/crew/agents/${c.role}/choice`, { state: 'automatic' }))}>
              Back to Automatic
            </button>
            <button className={button} onClick={() => act(send('POST', `/api/crew/model-choices/${c.id}/dismiss`))}>
              Not now
            </button>
          </>
        ) : (
          <>
            <button className={button} onClick={() => act(send('POST', `/api/crew/model-choices/${c.id}/undo`))}>
              Go back
            </button>
            <button className={button} onClick={() => act(send('PUT', `/api/crew/agents/${c.role}/choice`, { state: 'set_by_you' }))}>
              Set it myself
            </button>
          </>
        )}
      </div>
    </section>
  )
}

function Compare({ data, act }: { data: ModelsPage; act: Act }) {
  const [role, setRole] = useState('builder')
  const offered = data.clis.flatMap((c) => (c.signedIn && c.inUse ? uniqueModels(c.models).map((m) => `${c.cli}/${m.model}`) : []))
  const [model, setModel] = useState('')
  const [effort, setEffort] = useState('high')
  const running = data.comparisons.filter((c) => c.state === 'running')
  return (
    <Card title="Get numbers faster: a comparison">
      <p className="mb-2 text-sm text-text-dim">
        Every second ticket of the agent goes to the other entry, until each has {data.auto.compareRuns} or you stop it. The rule for
        checks and the pins still hold.
      </p>
      <div className="flex flex-wrap items-center gap-2 text-sm">
        <select aria-label="Agent" className={button} value={role} onChange={(e) => setRole(e.target.value)}>
          {data.autoRoles.map((r) => (
            <option key={r} value={r}>
              {agentName(r)}
            </option>
          ))}
        </select>
        <span>on its own entry, or</span>
        <select aria-label="The other model" className={button} value={model} onChange={(e) => setModel(e.target.value)}>
          <option value="">a model…</option>
          {offered.map((m) => (
            <option key={m} value={m}>
              {m.replace('/', ' · ')}
            </option>
          ))}
        </select>
        <select aria-label="Its effort" className={button} value={effort} onChange={(e) => setEffort(e.target.value)}>
          {efforts.map((e) => (
            <option key={e}>{e}</option>
          ))}
        </select>
        <button className={button} disabled={!model} onClick={() => act(send('POST', '/api/crew/comparisons', { role, model, effort }))}>
          Start
        </button>
      </div>
      {running.map((c) => (
        <p key={c.id} className="mt-2 text-sm">
          The {agentName(c.role).toLowerCase()}: {c.own.entry} · {c.own.effort} against {c.other.entry} · {c.other.effort}, since {when(c.startedAt)}{' '}
          <button className="ml-2 text-accent underline" onClick={() => act(send('DELETE', `/api/crew/comparisons/${c.id}`))}>
            Stop
          </button>
        </p>
      ))}
    </Card>
  )
}

// uniqueModels groups a CLI's names by model: agy lists one name for each effort.
function uniqueModels(rows: ModelRow[]): { model: string; rows: ModelRow[] }[] {
  const out: { model: string; rows: ModelRow[] }[] = []
  for (const r of rows) {
    const g = out.find((x) => x.model === r.model)
    if (g) g.rows.push(r)
    else out.push({ model: r.model, rows: [r] })
  }
  return out
}

function CLIList({ c, agents, onOpen, act }: { c: CLIModels; agents: AgentView[]; onOpen: (model: string) => void; act: Act }) {
  const [name, setName] = useState('')
  const families = [...new Set(c.models.map((m) => m.family))]
  return (
    <Card
      title={`${c.cli}${c.signedIn ? '' : ' · not signed in'}${c.inUse ? '' : ' · not in use'}`}
      right={<span className="text-text-dim">{c.short ? 'the short list: never read yet' : c.readAt ? `read ${when(c.readAt)}` : ''}</span>}
    >
      {c.failures > 0 && (
        <p className="mb-2 text-sm text-amber-text">
          The last {c.failures} reads failed: {c.error}. The last list is kept.
        </p>
      )}
      {families.map((f) => (
        <div key={f} className="mb-3">
          <p className="mb-1 text-xs text-text-dim">
            {familyName[f] ?? f} · limit: {c.models.find((m) => m.family === f)?.groupName}
          </p>
          <ul className="space-y-1.5">
            {uniqueModels(c.models.filter((m) => m.family === f)).map((g) => {
              const all = g.rows.flatMap((r) => r.efforts)
              const gone = g.rows.every((r) => !r.offered)
              const first = g.rows[0]
              const users = agents.filter((a) => a.runsOn?.CLI === c.cli && a.runsOn.Model === g.model).map((a) => agentName(a.role).toLowerCase())
              return (
                <li key={g.model} className={'rounded-md border border-border p-2 ' + (gone ? 'opacity-60' : '')}>
                  <button className="font-mono text-sm font-semibold underline-offset-2 hover:underline" onClick={() => onOpen(g.model)}>
                    {g.model}
                  </button>{' '}
                  {all.map((e) => (
                    <Tag key={e}>{e}</Tag>
                  ))}
                  {gone && <Tag tone="bad">no longer offered</Tag>}
                  {!gone && g.rows.some((r) => r.refused) && <Tag tone="amber">refused in a run: passed over, then tried again</Tag>}
                  {first?.addedBy === 'operator' && <Tag tone="amber">added by you</Tag>}
                  <p className="text-xs text-text-dim">
                    {first?.resolved && first.resolved !== g.model ? `now ${first.resolved} · ` : ''}
                    {users.length ? `the ${users.join(', ')} by default` : 'no agent uses it by default'}
                  </p>
                </li>
              )
            })}
          </ul>
        </div>
      ))}
      <form
        className="mt-2 flex gap-2"
        onSubmit={(e) => {
          e.preventDefault()
          act(send('POST', '/api/crew/models', { cli: c.cli, name }).then(() => setName('')))
        }}
      >
        <input
          aria-label={`A name for ${c.cli}`}
          className="min-w-0 flex-1 rounded-md border border-border bg-bg px-2 py-1 font-mono text-sm"
          placeholder="a name agy takes"
          value={name}
          onChange={(e) => setName(e.target.value)}
        />
        <button className={button} disabled={!name.trim()}>
          Add a name
        </button>
      </form>
    </Card>
  )
}

type One = {
  cli: string
  model: string
  names: { Name: string; Efforts: string[]; Family: string; Group: string; FirstSeen: string; Offered: boolean; Resolved: string }[]
  byDefault: string[]
  listedBy: string[]
  projects: { project: string; role: string; own: boolean }[]
  uses: { role: string; effort: string; runs: number; failed: number; tokens: number }[]
  latest: { id: string; role: string; effort: string; project?: string; ticket?: string; title: string; state: string; startedAt: string }[]
}

function OneModel({ cli, model, onClose }: { cli: string; model: string; onClose: () => void }) {
  const { data, error } = usePoll<One>(`/api/crew/models/${cli}/${encodeURIComponent(model)}`, 0)
  return (
    <section className="mt-4 rounded-lg border border-accent bg-bg-raised p-4">
      <div className="mb-2 flex items-center">
        <h2 className="text-xs tracking-wide text-text-dim uppercase">One model</h2>
        <button className="ml-auto text-sm text-accent underline" onClick={onClose}>
          Close
        </button>
      </div>
      <Failed error={error} />
      <p className="text-2xl font-semibold">{model}</p>
      {data && (
        <>
          <div className="mt-2">
            <Rows
              rows={[
                ['CLI', cli],
                ['Family', data.names[0]?.Family ?? ''],
                ['Effort', [...new Set(data.names.flatMap((n) => n.Efforts ?? []))].join(', ') || 'one fixed effort'],
                ['How it is passed', data.names.map((n) => n.Name).join(', ')],
                ['Agents, by default', data.byDefault.join(', ') || 'none'],
                ['On their lists', data.listedBy.join(', ') || 'none'],
                ['Set in projects', data.projects.filter((p) => p.own).map((p) => `${p.project} (${p.role})`).join(', ') || 'none'],
                ['First seen', when(data.names[0]?.FirstSeen)],
              ]}
            />
          </div>
          <h3 className="mt-4 mb-1 text-xs tracking-wide text-text-dim uppercase">What it has done</h3>
          <table className="w-full text-sm">
            <thead className="text-left text-xs text-text-dim">
              <tr>
                <th className="font-normal">Agent</th>
                <th className="font-normal">Effort</th>
                <th className="font-normal">Runs</th>
                <th className="font-normal">Failed</th>
                <th className="font-normal">Tokens</th>
              </tr>
            </thead>
            <tbody>
              {data.uses.map((u) => (
                <tr key={u.role + u.effort} className="border-t border-border">
                  <td className="py-1">{agentName(u.role)}</td>
                  <td>{u.effort ? <Tag>{u.effort}</Tag> : '—'}</td>
                  <td>{u.runs}</td>
                  <td>{u.failed}</td>
                  <td className="tabular-nums">{tokenCount(u.tokens)}</td>
                </tr>
              ))}
              {data.uses.length === 0 && (
                <tr>
                  <td className="py-1 text-text-dim" colSpan={5}>
                    No run on it yet.
                  </td>
                </tr>
              )}
            </tbody>
          </table>
          <h3 className="mt-4 mb-1 text-xs tracking-wide text-text-dim uppercase">Latest runs</h3>
          <ul className="space-y-1 text-sm">
            {data.latest.map((r) => (
              <li key={r.id}>
                <Link to="/runs/$id" params={{ id: r.id }} className="underline-offset-2 hover:underline">
                  {r.ticket ? `${r.ticket} ` : ''}
                  {r.title}
                </Link>{' '}
                <span className="text-text-dim">
                  {agentName(r.role)} · {r.effort} · {r.state}
                </span>
              </li>
            ))}
          </ul>
        </>
      )}
    </section>
  )
}

function Agents({ data, act }: { data: ModelsPage; act: Act }) {
  const offered = data.clis.flatMap((c) =>
    c.signedIn && c.inUse ? uniqueModels(c.models.filter((m) => m.offered)).map((g) => ({ entry: `${c.cli}/${g.model}`, efforts: g.rows.flatMap((r) => r.efforts) })) : [],
  )
  return (
    <>
      <h2 className="mt-8 mb-1 text-xl font-semibold">The agents</h2>
      <p className="mb-3 text-sm text-text-dim">
        What each one does, and what it runs on. Choosing a model or an effort here sets that agent's entry for the machine, and an agent
        Crew chooses for becomes Set by you. A project's own setting and a pin on a ticket still come first.
      </p>
      <div className="overflow-x-auto">
        <table className="w-full min-w-[56rem] text-sm">
          <thead className="text-left text-xs text-text-dim">
            <tr>
              <th className="py-1 font-normal">Agent</th>
              <th className="font-normal">Model</th>
              <th className="font-normal">Effort</th>
              <th className="font-normal">Who chooses</th>
              <th className="font-normal">Next choices, when the first cannot run</th>
            </tr>
          </thead>
          <tbody>
            {data.agents.map((a) => (
              <tr key={a.role} className="border-t border-border align-top">
                <td className="py-2 pr-3">
                  <b>{agentName(a.role)}</b>
                  <p className="max-w-[16rem] text-xs text-text-dim">{agentDoes[a.role]?.does}</p>
                </td>
                <td className="py-2 pr-3">
                  <select
                    aria-label={`The model of the ${agentName(a.role)}`}
                    className={button}
                    value={a.entries.find((e) => e.offered)?.entry ?? ''}
                    onChange={(e) => act(send('PUT', `/api/crew/agents/${a.role}`, { model: e.target.value }))}
                  >
                    {!a.entries.some((e) => e.offered) && <option value="">none is offered</option>}
                    {offered.map((o) => (
                      <option key={o.entry} value={o.entry}>
                        {o.entry.replace('/', ' · ')}
                      </option>
                    ))}
                  </select>
                  {a.runsOn ? (
                    <p className="mt-1 font-mono text-xs text-text-dim">runs on {choiceName(a.runsOn)}</p>
                  ) : (
                    <p className="mt-1 text-xs text-amber-text">waits: {a.waits}</p>
                  )}
                  {a.entries
                    .filter((e) => !e.offered)
                    .map((e) => (
                      <p key={e.entry} className="font-mono text-xs text-text-dim line-through" title={e.why}>
                        {e.entry}: not offered, skipped
                      </p>
                    ))}
                </td>
                <td className="py-2 pr-3">
                  <select
                    aria-label={`The effort of the ${agentName(a.role)}`}
                    className={button}
                    value={a.effort}
                    onChange={(e) => act(send('PUT', `/api/crew/agents/${a.role}`, { effort: e.target.value }))}
                  >
                    {efforts.map((e) => (
                      <option key={e}>{e}</option>
                    ))}
                  </select>
                  {a.runsOn && a.runsOn.Effort !== a.effort && (
                    <p className="mt-1 text-xs text-text-dim">runs at {a.runsOn.Effort || 'its one effort'}: the nearest it has</p>
                  )}
                </td>
                <td className="py-2 pr-3">
                  {a.state === 'list' ? <span className="text-text-dim">its list</span> : <Tag tone={a.state === 'automatic' ? 'blue' : 'amber'}>{a.state === 'automatic' ? 'Automatic' : 'Set by you'}</Tag>}
                  {a.checkRule && <p className="mt-1 max-w-[14rem] text-xs text-text-dim">Rule for checks: {a.checkRule}</p>}
                </td>
                <td className="py-2 font-mono text-xs">
                  {a.entries
                    .filter((e) => e.offered && e.entry !== (a.runsOn ? `${a.runsOn.CLI}/${a.runsOn.Model}` : ''))
                    .map((e) => (
                      <p key={e.entry} className={e.canRun ? '' : 'text-text-dim'} title={e.why}>
                        {e.entry.replace('/', ' · ')}
                        {!e.canRun && e.why ? ` (${e.why})` : ''}
                      </p>
                    ))}
                </td>
              </tr>
            ))}
          </tbody>
        </table>
      </div>
    </>
  )
}

function ByProject({ data }: { data: ModelsPage }) {
  const roles = ['curator', 'builder', 'reviewer', 'tester', 'verifier']
  if (data.projects.length === 0) return null
  return (
    <>
      <h2 className="mt-8 mb-1 text-xl font-semibold">
        Which model does what <span className="text-sm font-normal text-text-dim">in each project, as a run would get it now</span>
      </h2>
      <div className="overflow-x-auto">
        <table className="w-full min-w-[48rem] text-sm">
          <thead className="text-left text-xs text-text-dim">
            <tr>
              <th className="py-1 font-normal">Project</th>
              {roles.map((r) => (
                <th key={r} className="font-normal">
                  {agentName(r)}
                </th>
              ))}
            </tr>
          </thead>
          <tbody>
            {data.projects.map((p) => (
              <tr key={p.name} className="border-t border-border">
                <td className="py-1.5 pr-2">
                  <Link to="/p/$project/settings" params={{ project: p.name }} className="underline-offset-2 hover:underline">
                    {p.name}
                  </Link>
                </td>
                {roles.map((r) => (
                  <td key={r} className="pr-2 font-mono text-xs">
                    {p.roles[r] ? choiceName(p.roles[r]) : <span className="text-amber-text">waits</span>}
                    {p.own.includes(r) && <span className="ml-1 text-amber-text">set</span>}
                  </td>
                ))}
              </tr>
            ))}
          </tbody>
        </table>
      </div>
    </>
  )
}

function Results({ results }: { results: Numbers[] }) {
  const all = results.filter((n) => !n.kind)
  return (
    <>
      <h2 className="mt-8 mb-1 text-xl font-semibold">Results by CLI, model, and effort</h2>
      <p className="mb-2 text-sm text-text-dim">
        Measured from the finished runs of the last 90 days, under the model each really ran. For the builder a unit is a ticket, and
        passed means Crew's own checks passed at its first request for a move; for an agent that checks it is a run, and what counts is
        how often it asked for a fix.
      </p>
      <div className="overflow-x-auto">
        <table className="w-full min-w-[40rem] text-sm">
          <thead className="text-left text-xs text-text-dim">
            <tr>
              <th className="py-1 font-normal">Agent</th>
              <th className="font-normal">CLI, model, and effort</th>
              <th className="font-normal">Units</th>
              <th className="font-normal">Passed, or asked for a fix</th>
              <th className="font-normal">Fix rounds</th>
              <th className="font-normal">Time</th>
            </tr>
          </thead>
          <tbody>
            {all.map((n) => (
              <tr key={n.role + n.cli + n.model + n.effort} className="border-t border-border">
                <td className="py-1">{agentName(n.role)}</td>
                <td className="font-mono text-xs">
                  {n.cli} · {n.model} · {n.effort}
                </td>
                <td>{n.units}</td>
                <td>{n.role === 'builder' ? `${pct(n.passed, n.units)} passed` : `${pct(n.askedFix, n.units)} asked for a fix`}</td>
                <td>{n.role === 'builder' ? n.fixRounds.toFixed(1) : '—'}</td>
                <td>{perTicket(n.seconds, n.units)}</td>
              </tr>
            ))}
            {all.length === 0 && (
              <tr>
                <td colSpan={6} className="py-2 text-text-dim">
                  No finished runs to measure yet.
                </td>
              </tr>
            )}
          </tbody>
        </table>
      </div>
    </>
  )
}
