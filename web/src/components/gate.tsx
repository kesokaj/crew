import { useState } from 'react'
import { send } from '../lib/api'
import {
  type Changes,
  clean,
  type Current,
  type Detail,
  gateState,
  gateSteps,
  liveLines,
  type Line,
  logText,
  notRun,
  openPress,
  type Press,
  pressable,
  pressText,
  sendPress,
  shownPath,
} from '../lib/gate'
import { usePoll } from '../lib/poll'
import { SaidView } from './said'
import { button, mainButton } from './ui'

// The gate of the candidate on Versions (16.2 step 6, since 0.52). GateView lays out what lib/gate.ts
// says of a version's detail; GatePanel reads the detail and sends the press.

const markColour = (m: Line['mark']) => (m === '✓' ? 'text-green-700 dark:text-green-400' : m === '×' ? 'text-bad' : 'text-text-dim')

// GateLines are steps or the live part's lines. A step's detail is what its command or the server
// said, and a live line may name a project, an operator's word: each is marked data-free-text (0.54).
function GateLines({ lines, kind }: { lines: Line[]; kind: string }) {
  return (
    <ol className="space-y-0.5 text-sm" data-gate-lines={kind}>
      {lines.map((l, i) => (
        <li key={i}>
          <span className={markColour(l.mark)}>{l.mark}</span> {l.free ? <span data-free-text>{l.text}</span> : l.text}
          {l.detail && (
            <div className="ml-4 whitespace-pre-wrap text-text-dim" data-free-text>
              {l.detail}
            </div>
          )}
        </li>
      ))}
    </ol>
  )
}

function Log({ title, text }: { title: string; text: string }) {
  return (
    <details>
      <summary className="cursor-pointer text-sm text-text-dim">{title}</summary>
      <pre className="mt-1 max-h-96 overflow-auto rounded bg-bg-sunk p-2 text-xs whitespace-pre-wrap" data-free-text>
        {logText(text)}
      </pre>
    </details>
  )
}

// Holds is what the version holds since the release before: its tickets, by key, and the commits from
// outside, each title and subject marked as text from outside the app (16.2 step 6, since 0.54).
function Holds({ changes: c }: { changes: Changes }) {
  if (c.note) return <p className="text-sm text-text-dim">What is in it cannot be read: {clean(c.note)}</p>
  return (
    <div data-gate-holds>
      <p className="mb-1 text-xs tracking-wide text-text-dim uppercase">
        What is in it{c.since ? `, since ${clean(c.since.version)}` : ''}
      </p>
      {c.tickets.length === 0 && c.outside.length === 0 && <p className="text-sm text-text-dim">Nothing since.</p>}
      <ul className="space-y-0.5 text-sm">
        {c.tickets.map((t) => (
          <li key={t.commit}>
            <span className="font-mono text-xs">{clean(t.key)}</span> <span data-free-text>{clean(t.title)}</span>
          </li>
        ))}
      </ul>
      {c.outside.length > 0 && (
        <>
          <p className="mt-2 mb-1 text-xs text-text-dim">From outside</p>
          <ul className="space-y-0.5 text-sm">
            {c.outside.map((o) => (
              <li key={o.commit}>
                <span className="font-mono text-xs">{clean(o.commit).slice(0, 7)}</span> <span data-free-text>{clean(o.subject)}</span>
              </li>
            ))}
          </ul>
        </>
      )}
      {(c.cut.tickets || c.cut.outside) && <p className="mt-1 text-xs text-text-dim">The list is long: only the newest are listed.</p>}
    </div>
  )
}

export type GateViewProps = {
  version: string
  det: Detail | null
  error: string
  current?: Current
  // confirm is the confirmation that is open, with the version, the diff, and the hash it was opened
  // on; busy while a press is on its way; failed the server's words when it refused one.
  confirm: Press | null
  busy: boolean
  failed: string
  onOpen: (p: Press) => void
  onPress: (p: Press) => void
  onCancel: () => void
  onToggleInstall?: (install: boolean) => void
}

// GateView is the gate of the candidate: where its gate is, each step with its mark, the live part
// while it runs, what did not run, the diff and Run the live gate on a waiting gate, and the logs as
// text. The words are lib/gate.ts's.
export function GateView({ version, det, error, current, confirm, busy, failed, onOpen, onPress, onCancel, onToggleInstall }: GateViewProps) {
  const steps = det ? gateSteps(det, current) : []
  const live = det ? liveLines(det) : []
  const skipped = det ? notRun(det) : []
  const diff = det?.gate?.state === 'waiting' ? (det.gate.diff ?? []) : []
  return (
    <section className="mt-3 rounded-lg border border-border bg-bg-raised p-4" data-gate={version}>
      <h2 className="mb-2 text-xs tracking-wide text-text-dim uppercase">The gate of {version}</h2>
      {!det && error && (
        <p className="text-sm text-bad">
          The gate could not be read: <span data-free-text>{clean(error)}</span>
        </p>
      )}
      {det && (
        <div className="space-y-3">
          <p className="text-sm font-semibold" data-gate-state>
            <SaidView said={gateState(det)} />
          </p>
          {onToggleInstall && det.gate && det.gate.state !== 'failed' && (!det.gateFile || det.gateFile.live === 'pending') && (
            <label className="flex cursor-pointer items-center gap-2 text-xs text-text-dim">
              <input
                type="checkbox"
                checked={!!det.gate.installWhenDone}
                disabled={busy}
                onChange={(e) => onToggleInstall(e.target.checked)}
                className="accent-accent"
              />
              <span>Install automatically as soon as the gate passes</span>
            </label>
          )}
          {steps.length > 0 && <GateLines lines={steps} kind="steps" />}
          {live.length > 0 && (
            <div>
              <p className="mb-1 text-xs tracking-wide text-text-dim uppercase">The live part</p>
              <GateLines lines={live} kind="live" />
            </div>
          )}
          {skipped.length > 0 && (
            <div>
              <p className="mb-1 text-xs tracking-wide text-text-dim uppercase">What did not run</p>
              <ul className="space-y-0.5 text-sm" data-gate-not-run>
                {skipped.map((s, i) => (
                  <li key={i}>
                    · <span data-free-text>{s}</span>
                  </li>
                ))}
              </ul>
            </div>
          )}
          {diff.length > 0 && (
            <div data-gate-diff>
              <p className="mb-1 text-xs tracking-wide text-text-dim uppercase">The runtime image's inputs that differ from the running release's</p>
              <ul className="font-mono text-xs">
                {diff.map((p) => (
                  <li key={p} data-free-text>
                    {shownPath(p)}
                  </li>
                ))}
              </ul>
            </div>
          )}
          {failed && (
            <p className="text-sm text-bad" data-free-text>
              {clean(failed)}
            </p>
          )}
          {confirm ? (
            <div className="rounded border border-amber p-3 text-sm" data-gate-confirm={confirm.version}>
              <p className="mb-1 font-semibold">Run the live gate of {confirm.version}</p>
              <p className="mb-2">{pressText()}</p>
              <ul className="mb-2 font-mono text-xs">
                {confirm.diff.map((p) => (
                  <li key={p} data-free-text>
                    {shownPath(p)}
                  </li>
                ))}
              </ul>
              <div className="flex gap-2">
                <button className={mainButton} disabled={busy} onClick={() => onPress(confirm)}>
                  Run it
                </button>
                <button className={button} disabled={busy} onClick={onCancel}>
                  Cancel
                </button>
              </div>
            </div>
          ) : (
            pressable(det) && (
              <button className={button} disabled={busy} onClick={() => onOpen(openPress(det))}>
                Run the live gate
              </button>
            )
          )}
          {det.changes && <Holds changes={det.changes} />}
          {(det.kind || det.manifest?.commit) && (
            <p className="text-sm text-text-dim" data-gate-kind>
              {det.kind && (
                <>
                  Against the running release: {det.kind.kind}, {det.kind.changed} paths changed
                  {det.kind.protected.length > 0 && (
                    <>
                      ; protected: <span data-free-text>{det.kind.protected.map(clean).join(', ')}</span>
                    </>
                  )}
                  .{' '}
                </>
              )}
              {det.manifest?.commit && (
                <span className="font-mono text-xs">
                  release.json: {clean(det.manifest.commit).slice(0, 12)}, schema {det.manifest.schema ?? '—'}, agy{' '}
                  <span data-free-text>{clean(det.manifest.pins?.agy ?? '—')}</span>
                </span>
              )}
            </p>
          )}
          {det.build?.log && <Log title="The build's log" text={det.build.log} />}
          {det.gate?.log && <Log title="make check's log" text={det.gate.log} />}
        </div>
      )}
    </section>
  )
}

// GatePanel reads the version's detail every ten seconds and sends the press. The confirmation keeps
// the version, the diff, and the hash it was opened with, so what the press sends is what was shown,
// though the detail is read again meanwhile; a refusal shows the server's words, and the screen and
// the detail are read again after a press.
export function GatePanel({ version, current, onPress }: { version: string; current?: Current; onPress: () => void }) {
  const { data: det, error, reload } = usePoll<Detail>(`/api/crew/versions/${version}`, 10000)
  const [confirm, setConfirm] = useState<Press | null>(null)
  const [busy, setBusy] = useState(false)
  const [failed, setFailed] = useState('')
  const press = (c: Press) => {
    setBusy(true)
    setFailed('')
    sendPress(c, send).then((why) => {
      setBusy(false)
      setConfirm(null)
      setFailed(why)
      reload()
      onPress()
    })
  }
  const toggleInstall = (install: boolean) => {
    setBusy(true)
    setFailed('')
    send('POST', `/api/crew/versions/${version}/gate/install`, { install }).then(
      () => {
        setBusy(false)
        reload()
        onPress()
      },
      (e: Error) => {
        setBusy(false)
        setFailed(e.message)
      },
    )
  }
  return (
    <GateView
      version={version}
      det={det}
      error={error}
      current={current}
      confirm={confirm}
      busy={busy}
      failed={failed}
      onOpen={setConfirm}
      onPress={press}
      onCancel={() => setConfirm(null)}
      onToggleInstall={toggleInstall}
    />
  )
}
