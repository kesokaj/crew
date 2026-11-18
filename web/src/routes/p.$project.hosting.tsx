import { createFileRoute, Link } from '@tanstack/react-router'
import { useState } from 'react'
import { send, type HostingDeploy, type HostingView } from '../lib/api'
import { usePoll } from '../lib/poll'
import { button, Card, Failed, PageHead, Rows, Tag, when } from '../components/ui'

export const Route = createFileRoute('/p/$project/hosting')({ component: Hosting })

// The four choices of who may open a hosted project (19.4), in the order host.png shows them.
const choices = [
  ['private', 'Only the admin email', 'Google sign-in, and only your own address, the admin email.'],
  ['listed', 'Listed addresses', 'Google sign-in, and only the Google mail addresses on the project\'s list.'],
  ['google', 'Any Google account', 'Google sign-in, with any Google account.'],
  ['public', 'Public', 'Anyone, with no sign-in, at most hosting.public_rps requests a second from one address.'],
] as const

const why: Record<string, string> = {
  asked: 'you asked',
  follow_main: 'Follow main',
  rollback: 'a roll back',
  hostable: 'main got its Dockerfile',
  settings: 'the settings changed',
}

const short = (c: string) => c.slice(0, 7)

// The Hosting screen (design 19, host.png): the address and who may open it, what runs, how it is kept
// current, the deploys with Roll back, and what the project prints. Deploy again and Stop are plain;
// a roll back and the values ask for a fresh sign-in (20.3).
function Hosting() {
  const { project } = Route.useParams()
  const base = `/api/projects/${encodeURIComponent(project)}/hosting`
  const { data: h, error, reload } = usePoll<HostingView>(base, 5000)
  const { data: logs } = usePoll<{ lines: string[] }>(base + '/logs?lines=200', 15000)
  const [failed, setFailed] = useState('')
  const [busy, setBusy] = useState(false)
  const act = (method: 'POST' | 'DELETE', path: string, body?: unknown) => {
    setBusy(true)
    setFailed('')
    send(method, path, body).then(
      () => {
        setBusy(false)
        reload()
      },
      (e: Error) => {
        setBusy(false)
        setFailed(e.message)
      },
    )
  }
  const serving = h?.state === 'live' || h?.state === 'failing'
  const canDeploy = !!h && !h.refused && !h.compose && !h.running
  return (
    <div className="mx-auto max-w-7xl p-6">
      <PageHead
        crumbs={
          <>
            <Link to="/" className="text-accent">
              All projects
            </Link>{' '}
            / {project}
          </>
        }
        title="Hosting"
        actions={
          h && (
            <>
              <button className={button} disabled={!canDeploy || busy} onClick={() => act('POST', base + '/deploys')}>
                {serving ? 'Deploy again' : 'Host locally'}
              </button>
              {(serving || h.running > 0) && (
                <button className={button} disabled={busy} onClick={() => act('DELETE', base)}>
                  Stop hosting
                </button>
              )}
            </>
          )
        }
      >
        Host what was built, from this machine. Crew builds the project into a container image, runs it in a sandbox of its own, and gives
        it an address.
      </PageHead>
      <Failed error={error || failed} />
      {h?.refused && (
        <p className="my-3 rounded-md border border-amber bg-amber-soft p-3 text-sm" data-refused>
          {h.refused}
        </p>
      )}
      {h?.compose && (
        <p className="my-3 rounded-md border border-border p-3 text-sm">
          {h.dockerfile} is a compose file. Hosting several containers comes later; one Dockerfile is hosted now.
        </p>
      )}
      {h?.ticket && (
        <p className="my-3 rounded-md border border-border p-3 text-sm" data-waiting>
          Hosting waits for{' '}
          <Link to="/p/$project/tickets/$key" params={{ project, key: h.ticket.key }} className="text-accent">
            {h.ticket.key}
          </Link>
          {h.ticket.stage === 'cancelled'
            ? ', which was cancelled: its replacements make the project hostable, and hosting goes on by itself when main has ' + h.dockerfile + '.'
            : ', which adds ' + h.dockerfile + '. It goes on by itself when main has it.'}
        </p>
      )}
      {h?.running ? <p className="my-3 text-sm text-text-dim">Deploy {h.running} is running.</p> : null}
      {h?.valuesLost && (
        <p className="my-3 rounded-md border border-bad p-3 text-sm">
          The values must be entered again: they were sealed with a key file that is gone. A deploy fails until they are.
        </p>
      )}
      {h && <Cards h={h} />}
      {h && <Deploys h={h} onRollBack={(n, data) => act('POST', `${base}/deploys/${n}/rollback`, { restoreData: data })} busy={busy} />}
      {h && <Values base={base} names={h.values} onSaved={reload} />}
      <h2 className="mt-8 mb-2 text-xl font-semibold">
        Logs <span className="ml-2 text-sm font-normal text-text-dim">what the hosted project prints, kept for {h?.logDays ?? 14} days</span>
      </h2>
      <pre className="max-h-96 overflow-auto rounded-lg bg-[#0f1115] p-4 font-mono text-xs leading-5 text-gray-300" data-logs>
        {(logs?.lines ?? []).length ? logs!.lines.map((l) => logLine(l)).join('\n') : 'Nothing printed yet.'}
      </pre>
    </div>
  )
}

const isoStamp = /^\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}/

// logLine shows a line as the clock time of its stamp and its text. A line without an ISO-8601 stamp,
// such as a continuation line of a failed Docker BuildKit build (#0, #1, #2), is left as it is.
export function logLine(l: string): string {
  const at = l.indexOf(' ')
  const stamp = at > 0 ? l.slice(0, at) : ''
  return !isoStamp.test(stamp) || Number.isNaN(Date.parse(stamp)) ? l : `${when(stamp)}  ${l.slice(at + 1)}`
}

// signInCallback is the apps portal's Google OAuth callback address when sign-in is enabled (19.4).
function signInCallback(h: Pick<HostingView, 'access' | 'certificate'>): string | null {
  if (h.access === 'public' || !h.certificate) return null
  const domain = h.certificate.replace(/^\*\./, '')
  return domain ? `https://auth.${domain}/oauth2/google/authorization-code-callback` : null
}

export function Cards({ h }: { h: HostingView }) {
  const live = h.live
  const access = choices.find(([k]) => k === h.access)
  const callback = signInCallback(h)
  return (
    <div className="grid gap-3 md:grid-cols-3">
      <Card title="Address">
        <p className="mb-3 font-mono text-sm break-all" data-address>
          {h.address || '—'}
        </p>
        <h3 className="mb-2 text-xs tracking-wide text-text-dim uppercase">Who may open it</h3>
        <div className="mb-2 flex flex-wrap rounded-md border border-border p-1 text-sm" data-access>
          {choices.map(([k, label]) => (
            <span key={k} className={'rounded px-2 py-1 ' + (k === h.access ? 'bg-text font-mono text-bg' : 'font-mono')} data-selected={k === h.access}>
              {label}
            </span>
          ))}
        </div>
        <p className="mb-3 text-sm text-text-dim">{access?.[2]} Change it in the project's Settings.</p>
        <Rows
          narrow
          rows={[
            ['Reached from', 'the internet, through the ingress'],
            [
              'Sign-in',
              <span>
                the ingress&apos;s own. The project gets the signed-in account in a header, and needs no sign-in of its own.
                {callback && (
                  <>
                    {' '}
                    The Google sign-in client lists{' '}
                    <span className="font-mono text-xs break-all" data-signin-callback>
                      {callback}
                    </span>
                    .
                  </>
                )}
              </span>,
            ],
            ['Certificate', <span className="font-mono text-xs">{h.certificate ? `the certificate of ${h.certificate}` : '—'}</span>],
            ['Own domain', 'later: an own hostname comes with hosting several containers'],
          ]}
        />
      </Card>
      <Card
        title="Running"
        right={
          live ? (
            live.healthy ? (
              <span className="text-ok">✓ Healthy</span>
            ) : (
              <span className="text-bad">Not answering{live.downSince ? ` since ${when(live.downSince)}` : ''}</span>
            )
          ) : null
        }
      >
        {live ? (
          <Rows
            narrow
            rows={[
              ['Version', <span><span className="font-mono">{short(live.commit)}</span> · deploy {live.deploy} · {live.tickets} tickets in it</span>],
              ['Image', <span className="font-mono text-xs break-all">{live.tag}</span>],
              ['Sandbox', <span><span className="font-mono text-xs">crew-{live.sandbox}</span> a microVM of its own · {h.sizeText || h.size}</span>],
              ['Health', `${h.health} ${live.healthy ? 'answers' : 'does not answer'}. Restarted ${live.restarts} times.`],
              ['Live since', when(live.liveSince)],
              ['Data', <span><span className="font-mono">{h.data}</span>, kept across deploys and in the backups</span>],
              ['Holds', 'its own settings and secrets. No CLI login, and no agent.'],
              ['Reaches', <span>{h.internet ? 'the internet, from inside' : 'nothing beyond its bridge'} <Tag tone={h.internet ? 'blue' : undefined}>{h.internet ? 'On' : 'Off'}</Tag></span>],
            ]}
          />
        ) : (
          <p className="text-sm text-text-dim">Nothing is hosted now.</p>
        )}
      </Card>
      <Card title="Keeping it current">
        <div className="mb-2 flex items-center">
          <span>Follow main</span>
          <span className="ml-auto">
            <Tag tone={h.followMain ? 'blue' : undefined}>{h.followMain ? 'On' : 'Off'}</Tag>
          </span>
        </div>
        <p className="mb-4 text-sm text-text-dim">
          {h.followMain
            ? 'When a ticket reaches Done, main is deployed, checked before it serves.'
            : 'The hosted version changes only when you press Deploy again.'}{' '}
          The switch is in the project's Settings.
        </p>
        <Rows
          narrow
          rows={[
            ['Who deploys', 'you, or this switch. Agents cannot.'],
            ['Kept', `the last ${h.keepImages} images, for rolling back`],
            ['Settings and secrets', `${h.values.length} values, kept in Crew's database, encrypted, and never in git`],
          ]}
        />
      </Card>
    </div>
  )
}

function Deploys({ h, onRollBack, busy }: { h: HostingView; onRollBack: (n: number, data: boolean) => void; busy: boolean }) {
  return (
    <>
      <h2 className="mt-8 mb-2 text-xl font-semibold">
        Deploys <span className="ml-2 text-sm font-normal text-text-dim">each one is recorded in the database</span>
      </h2>
      <table className="w-full text-sm" data-deploys>
        <thead className="text-left text-xs tracking-wide text-text-dim uppercase">
          <tr>
            <th className="py-2">#</th>
            <th>Commit</th>
            <th>Why</th>
            <th>When</th>
            <th>State</th>
            <th />
          </tr>
        </thead>
        <tbody>
          {h.deploys.map((d) => (
            <DeployRow key={d.number} d={d} onRollBack={onRollBack} busy={busy} />
          ))}
          {h.deploys.length === 0 && (
            <tr>
              <td colSpan={6} className="py-3 text-text-dim">
                No deploy yet.
              </td>
            </tr>
          )}
        </tbody>
      </table>
    </>
  )
}

function DeployRow({ d, onRollBack, busy }: { d: HostingDeploy; onRollBack: (n: number, data: boolean) => void; busy: boolean }) {
  const [data, setData] = useState(false)
  return (
    <tr className="border-t border-border align-top">
      <td className="py-2">{d.number}</td>
      <td className="font-mono">{short(d.commit)}</td>
      <td>
        {why[d.why] ?? d.why}
        {d.tickets.length > 0 && <span className="text-text-dim"> · {d.tickets.join(', ')}</span>}
      </td>
      <td>{when(d.startedAt)}</td>
      <td>
        {d.state === 'live' ? <span className="text-ok">✓ live</span> : d.state === 'failed' ? <span className="text-bad">failed</span> : d.state}
        {d.state === 'failed' && d.detail && <pre className="mt-1 max-w-xl text-xs whitespace-pre-wrap text-text-dim">{d.detail}</pre>}
      </td>
      <td className="text-right">
        {d.canRollBack && (
          <span className="inline-flex items-center gap-2">
            {d.canRestoreData && (
              <label className="text-xs text-text-dim">
                <input type="checkbox" checked={data} onChange={(e) => setData(e.target.checked)} /> and its data
              </label>
            )}
            <button className={button} disabled={busy} onClick={() => onRollBack(d.number, data)}>
              Roll back to this
            </button>
          </span>
        )}
      </td>
    </tr>
  )
}

// Values are the settings and secrets the project gets as environment values (R176): set as a whole,
// shown by name only, behind a fresh sign-in.
function Values({ base, names, onSaved }: { base: string; names: string[]; onSaved: () => void }) {
  const [open, setOpen] = useState(false)
  const [text, setText] = useState('')
  const [failed, setFailed] = useState('')
  const save = () => {
    const values: Record<string, string> = {}
    for (const line of text.split('\n')) {
      if (!line.trim()) continue
      const at = line.indexOf('=')
      if (at < 1) {
        setFailed(`"${line}" is not NAME=value`)
        return
      }
      values[line.slice(0, at).trim()] = line.slice(at + 1)
    }
    send('PUT', base + '/settings', { values }).then(
      () => {
        setText('')
        setOpen(false)
        setFailed('')
        onSaved()
      },
      (e: Error) => setFailed(e.message),
    )
  }
  return (
    <div className="mt-6 text-sm">
      <span className="text-text-dim">Settings and secrets: </span>
      {names.length ? names.map((n) => <Tag key={n} mono>{n}</Tag>) : <span className="text-text-dim">none</span>}{' '}
      <button className={button} onClick={() => setOpen(!open)}>
        {open ? 'Close' : 'Set the values'}
      </button>
      {open && (
        <div className="mt-2 max-w-2xl">
          <p className="mb-1 text-xs text-text-dim">
            One NAME=value a line, the whole set: what you leave out goes. Values are never shown again. A live project starts again with them.
          </p>
          <textarea className="h-32 w-full rounded-md border border-border bg-bg p-2 font-mono text-xs" value={text} onChange={(e) => setText(e.target.value)} />
          <Failed error={failed} />
          <button className={button} onClick={save}>
            Save the values
          </button>
        </div>
      )}
    </div>
  )
}
