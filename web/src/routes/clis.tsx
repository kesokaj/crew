import { createFileRoute, Link } from '@tanstack/react-router'
import { useCallback, useEffect, useState } from 'react'
import { getJSON, send, type CLIState, type Frame } from '../lib/api'
import { usePoll } from '../lib/poll'
import { button, Card, Rows, when } from '../components/ui'
import { agentName, type AgentView, type CLIModels, type ModelsPage, type QuotaWindow } from '../lib/models'

export const Route = createFileRoute('/clis')({ component: CLIs })

// What the screen says of each CLI that is not a state: how its login reaches a sandbox (design 12.6).
const about: Record<string, { title: string; login: string; reaches: string }> = {
  agy: {
    title: 'Antigravity CLI',
    login: 'The token file from the sign-in. If a run or a check renews it, Crew keeps the newer one, after Google says it is the same account.',
    reaches: 'the Antigravity model service',
  },
}

function CLIs() {
  const [clis, setClis] = useState<CLIState[] | null>(null)
  const { data: models } = usePoll<ModelsPage>('/api/crew/models', 30000)
  const { data: frame } = usePoll<Frame>('/api/frame', 30000)
  const [error, setError] = useState('')
  const load = useCallback(() => {
    getJSON<{ clis: CLIState[] }>('/api/clis').then(
      (r) => {
        setClis(r.clis)
        setError('')
      },
      (e: Error) => setError(e.message),
    )
  }, [])
  useEffect(load, [load])
  // While a check runs, look again every few seconds.
  useEffect(() => {
    if (!clis?.some((c) => c.checking)) return
    const t = setTimeout(load, 3000)
    return () => clearTimeout(t)
  }, [clis, load])
  const agyVersion = clis?.find((c) => c.cli === 'agy')?.version

  return (
    <div className="mx-auto max-w-6xl p-6">
      <div className="flex flex-wrap items-start justify-between gap-3">
        <div>
          <h1 className="text-2xl font-semibold">Agent CLIs</h1>
          <p className="mt-1 max-w-3xl text-text-dim">
            Crew runs one agent CLI: <code>agy</code>, the Antigravity CLI. Agents do not call a model API. Each run starts it inside its
            own microVM, signed in with your subscription. It offers Gemini and Claude models, each group with limits of its own, so when
            one group cannot run, the other takes the work it can.
          </p>
        </div>
        <Link to="/models" className={button}>
          Models
        </Link>
      </div>
      {error && <p className="mt-4 rounded-md border border-red-400 p-3 text-sm">{error}</p>}
      <div className="mt-6 grid gap-4 md:grid-cols-2 xl:grid-cols-3">
        {clis?.map((c) => (
          <CLICard key={c.cli} c={c} crewVersion={frame?.version} onCheck={() => void send('POST', `/api/clis/${c.cli}/check`).then(load)} />
        ))}
        {models?.clis.flatMap((c) => groupsOf(c).map((g) => <LimitCard key={c.cli + g.group} c={c} g={g} agents={models.agents} />))}
        <Card title="When a CLI cannot run">
          <ol className="list-decimal space-y-1 pl-5 text-sm">
            <li>
              <b>Signed out, or one group at its limit:</b> a run moves to a model of the same bracket in another group with room.
            </li>
            <li>A run with no next choice waits, and keeps its conversation.</li>
            <li>When no model of another family can run, a check falls back to the builder&apos;s family and the inbox says why.</li>
            <li>
              <b>Nothing can run:</b> every project waits for the first reset or sign-in.
            </li>
          </ol>
        </Card>
        <Card title="The check rule">
          <p className="text-sm">A reviewer and a verifier run on a model of another family than the builder whose work they check.</p>
          <div className="mt-2">
            <Rows
              rows={[
                ['By default the builder runs on', <code key="b">{models?.agents.find((a) => a.role === 'builder')?.runsOn?.Name ?? '—'}</code>],
                ['and the reviewer on', <span key="r">{models?.agents.find((a) => a.role === 'reviewer')?.checkRule ?? '—'}</span>],
                ['When both land on one family', 'the check moves to the first model of another family on its list, and the run says so'],
                ['When no other family can run', 'the check runs on the builder’s family, and the run and the inbox say the rule could not be met'],
                ['Switch', "per project, in its Settings. On by default."],
              ]}
            />
          </div>
        </Card>
        <OneRuntimeImageCard crewVersion={frame?.version} agyVersion={agyVersion} />
        <RoomForMoreCard />
      </div>
      <div className="mt-4">
        <WhatASandboxIsGivenCard />
      </div>
    </div>
  )
}

export function OneRuntimeImageCard({ crewVersion, agyVersion }: { crewVersion?: string; agyVersion?: string }) {
  return (
    <Card title="One runtime image">
      <Rows
        narrow
        rows={[
          ['Image', <span key="i" className="font-mono text-xs">{`crew-runtime:${crewVersion ?? '…'}`}</span>],
          [
            'Holds',
            <span key="h">
              <span className="font-mono text-xs">{`agy${agyVersion ? ' ' + agyVersion : ''}`}</span>, the harness, the run driver, Docker, and the tools
            </span>,
          ],
          [
            'The harness',
            "AGENTS.md (the global context), skills, subagents, and rules. One folder in Crew's source repository, built into the image.",
          ],
          [
            'Kept current',
            'Crew looks for new versions every day. A new one becomes a ticket, then a tested release. It changes the image, so it waits for Install.',
          ],
        ]}
      />
      <div className="mt-3">
        <Link to="/versions" className={button}>
          Versions
        </Link>
      </div>
    </Card>
  )
}

export function RoomForMoreCard() {
  return (
    <Card title="Room for more">
      <p className="mb-2 text-sm text-text-dim">
        Everything that differs between two CLIs is in one adapter: how it is installed, signed in, started, read, and continued, which
        models it offers, and what its limits are. Nothing else in Crew names a CLI.
      </p>
      <Rows
        narrow
        rows={[
          ['Built', <span key="b" className="font-mono text-xs">agy</span>],
          [
            'Not built',
            <span key="n">
              <span className="font-mono text-xs">codex, opencode</span>{' '}
              <span className="text-text-dim">each needs an adapter, a sign-in step, and a spike of its own</span>
            </span>,
          ],
        ]}
      />
    </Card>
  )
}

export function WhatASandboxIsGivenCard() {
  return (
    <Card title="What a sandbox is given">
      <Rows
        rows={[
          [
            'Gets',
            "The login of agy, the git identity, and a GitHub token that can only read this project's repository and lasts one hour. No gcloud. They are your own logins: Crew acts as you. Crew keeps them in its database, encrypted, and copies them in each time a sandbox starts.",
          ],
          ['Does not get', 'The SSH key or gh. The improver gets only the login of agy.'],
          [
            'What is at risk',
            "An agent with internet access can use or send out what its sandbox holds. It can read its project's repository for an hour. It cannot push to GitHub.",
          ],
        ]}
      />
    </Card>
  )
}

type Group = { group: string; name: string; models: string[]; windows: QuotaWindow[] }

// groupsOf lists a CLI's groups of models with the windows the quota guard keeps for each (12.7).
function groupsOf(c: CLIModels): Group[] {
  const out: Group[] = []
  for (const m of c.models.filter((m) => m.offered)) {
    let g = out.find((x) => x.group === m.group)
    if (!g) {
      g = { group: m.group, name: m.groupName, models: [], windows: [] }
      out.push(g)
    }
    if (!g.models.includes(m.model)) g.models.push(m.model)
  }
  for (const w of c.windows) {
    const g = out.find((x) => x.group === w.group) ?? out.find(() => w.group.startsWith('model:'))
    g?.windows.push(w)
  }
  return out
}

const windowName: Record<string, string> = { '5h': 'Five-hour limit', weekly: 'This week', limit: 'A limit' }

function LimitCard({ c, g, agents }: { c: CLIModels; g: Group; agents: AgentView[] }) {
  const reached = g.windows.find((w) => w.reachedUntil)
  const five = g.windows.find((w) => w.window === '5h' && w.group === g.group)
  const main = reached && (five?.used ?? 0) < 1 ? reached : five
  const close = g.windows.some((w) => w.close)
  const byDefault = agents.filter((a) => a.runsOn?.CLI === c.cli && g.models.includes(a.runsOn.Model)).map((a) => agentName(a.role).toLowerCase())
  const running = Math.max(0, ...g.windows.filter((w) => w.group === g.group).map((w) => w.running))
  return (
    <Card
      title={`Limit · ${c.cli === 'agy' ? "agy's " : ''}${g.name}`}
      right={
        reached ? (
          <span className="text-red-700 dark:text-red-400">At its limit until {when(reached.reachedUntil)}</span>
        ) : close ? (
          <span className="text-amber-text">Close to its limit</span>
        ) : (
          <span className="text-green-700 dark:text-green-400">✓ Available</span>
        )
      }
    >
      {main ? (
        <>
          <p className="text-3xl font-semibold">{Math.round(main.used * 100)}%</p>
          <div className="mt-1 mb-3 h-1.5 rounded bg-bg-sunk">
            <div className="h-1.5 rounded bg-accent" style={{ width: `${Math.min(100, Math.round(main.used * 100))}%` }} />
          </div>
        </>
      ) : (
        <p className="mb-3 text-sm text-text-dim">Not read yet: the next check reads it.</p>
      )}
      <Rows
        narrow
        rows={[
          ...g.windows.map(
            (w) =>
              [
                w.group === g.group ? windowName[w.window] ?? w.window : `${w.groupName}, ${windowName[w.window] ?? w.window}`,
                `${Math.round(w.used * 100)}% used${w.resetsAt ? ` · resets ${when(w.resetsAt)}` : ''}`,
              ] as [string, React.ReactNode],
          ),
          ['Runs', `${running} running`],
          ['Models', <span key="m" className="font-mono text-xs">{g.models.join(', ')}</span>],
          ['By default', byDefault.join(', ') || 'no role'],
        ]}
      />
    </Card>
  )
}

function CLICard({ c, crewVersion, onCheck }: { c: CLIState; crewVersion?: string; onCheck: () => void }) {
  const a = about[c.cli]
  const signedIn = c.setUp && c.state === 'signed_in'
  const stateText = !c.setUp ? 'not signed in' : c.state === 'signed_in' ? 'signed in' : c.state === 'signed_out' ? 'signed out' : 'not checked yet'
  return (
    <Card
      title={`${a?.title ?? c.cli} · ${c.cli}${c.version ? ' ' + c.version : ''}`}
      right={<span className={signedIn ? 'text-green-700 dark:text-green-400' : 'text-red-700 dark:text-red-400'}>{signedIn ? '✓ Signed in' : stateText}</span>}
    >
      <Rows
        narrow
        rows={[
          [
            'Signed in',
            <span key="s">
              {stateText}
              {c.account ? `, as ${c.account}` : ''}
              {c.signedAt ? <span className="text-text-dim">, since {when(c.signedAt)}</span> : null}
            </span>,
          ],
          [
            'Last check',
            <span key="l">
              {c.checking ? 'checking now…' : c.checkedAt ? `${when(c.checkedAt)}: ${c.result}` : 'never'}
              {c.failures > 0 && <span className="text-red-700 dark:text-red-400"> ({c.failures} refused in a row)</span>}
              <span className="block text-xs text-text-dim">checked every 30 minutes, and before every run</span>
            </span>,
          ],
          ['Login in a sandbox', a?.login ?? ''],
          ...(c.version
            ? [
                [
                  'Version',
                  <span key="v">
                    <span className="font-mono text-xs">{c.version}</span>
                    {crewVersion ? <span className="text-text-dim">{` set by Crew ${crewVersion}. The CLI does not update itself.`}</span> : null}
                  </span>,
                ] as [string, React.ReactNode],
              ]
            : []),
          ['Reaches', a?.reaches ?? ''],
        ]}
      />
      <div className="mt-3 flex gap-2">
        <button disabled={c.checking || !c.setUp} className="rounded-md border border-border px-3 py-1 text-sm disabled:opacity-50" onClick={onCheck}>
          Check now
        </button>
        <Link to="/setup" search={{ step: c.cli }} className="rounded-md border border-border px-3 py-1 text-sm">
          Sign in again
        </Link>
      </div>
    </Card>
  )
}
