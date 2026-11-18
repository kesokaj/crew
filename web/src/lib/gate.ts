// What the Versions screen says of a release's gate (16.2 steps 5 and 6, since 0.52), from the
// version's detail, GET /api/crew/versions/{v}. Once gate.json is there, its steps say how the gate
// ended; before, the gate's row and the updater's request say where it is. The live part's lines
// come from the server's own view of it while it runs, as text: crew-gate has no screen.

import { free, type Said } from './said'
import { stageName } from './words'

export type GateStep = { name: string; ok: boolean; detail?: string }

export type GateFile = {
  passed: boolean
  steps: GateStep[]
  installTest: string
  installTestNote?: string
  live?: string
  liveSteps?: GateStep[]
  skipped?: string[]
  hold?: string
}

export type LiveStep = GateStep & { skipped?: string; hold?: string }

export type LiveSuite = { project: string; suite?: number; state?: string; waitFor?: string; verdict?: string; note?: string }

export type LivePart = { ticket?: string; stage?: string; real?: LiveStep; suites?: LiveSuite[]; suitesStep?: LiveStep }

// Changes is what a version holds since the release before (16.2 step 6, since 0.54), or why it
// cannot be read.
export type Changes = {
  since?: { version: string; commit: string } | null
  tickets: { key: string; title: string; commit: string }[]
  outside: { commit: string; subject: string }[]
  cut: { tickets: boolean; outside: boolean }
  note?: string
}

export type Detail = {
  version: string
  build?: { state: string; commit: string; log: string; error?: string } | null
  gate?: {
    state: string
    steps: GateStep[] | null
    log: string
    error?: string
    installWhenDone?: boolean
    diff?: string[]
    noDiff?: 'unread' | 'noInputs' | 'badHash'
    livePart?: LivePart
  } | null
  manifest?: { runtimeSha256?: string; commit?: string; schema?: number; pins?: Record<string, string> } | null
  gateFile?: GateFile | null
  changes?: Changes | null
  kind?: { kind: string; changed: number; protected: string[] } | null
}

// Current is the updater's request that runs, as the screen has it.
export type Current = { version: string; action: string; steps: { step: number; name: string; ok: boolean; detail: string }[] | null }

// free says the line's text is from outside the app, a project's name, which the panel marks (0.54).
export type Line = { mark: '✓' | '×' | '·'; text: string; detail?: string; free?: true }

const short = (commit: string) => commit.slice(0, 12)

// ended is gate.json once the gate ended: passed, failed, or skipped, not pending.
const ended = (d: Detail) => (d.gateFile && d.gateFile.live !== 'pending' ? d.gateFile : null)

// gateState is one line of where the gate is, or how it ended. A step's name and detail, an error,
// and a hold are free parts: they come in part from what code agents wrote printed (said.ts).
export function gateState(d: Detail): Said {
  const f = ended(d)
  if (f) {
    if (f.passed && f.live !== 'failed') {
      if (f.hold) return ['The gate passed, and holds the release for Install: ', free(clean(f.hold))]
      if ((f.skipped ?? []).length > 0) return ['The gate passed, and a step did not run: the release waits for Install']
      return ['The gate passed']
    }
    if (f.live === 'failed') return ['The gate failed in its live steps']
    const bad = f.steps.find((s) => !s.ok)
    return bad ? ['The gate failed at ', free(clean(bad.name)), ...(bad.detail ? [': ', free(clean(bad.detail))] : [])] : ['The gate failed']
  }
  const g = d.gate
  switch (g?.state) {
    case 'failed':
      return ['The gate failed: ', free(clean(g.error ?? 'no reason was kept'))]
    case 'checking':
      return ['make check runs on the exported commit, in a sandbox of its own']
    case 'asking':
    case 'asked':
      return ["The updater runs the gate's steps: the check server and the smoke run in microVMs, the selftest, and the browser tests"]
    case 'waiting':
      return [
        (g.diff ?? []).length > 0
          ? "Waits for Run the live gate: its runtime image is other code than the running release's"
          : `Waits for Run the live gate, which is not offered: ${Object.hasOwn(noDiffWhy, g.noDiff ?? '') ? noDiffWhy[g.noDiff ?? ''] : noDiffWhy.unread}`,
      ]
    case 'live':
      return ['The live steps run on this server: the real ticket on crew-gate, then the eval suites when the release changes what agents see']
    case 'done':
      return ["The live steps' result goes to the updater"]
  }
  if (d.gateFile) return ["The updater's steps passed: the live steps wait for this server to take them up"]
  switch (d.build?.state) {
    case 'running':
      return [`Building ${d.version} from ${short(d.build.commit)}`]
    case 'failed':
      return ['The build failed: ', free(clean(d.build.error ?? 'no reason was kept'))]
    case 'done':
      return [`Built from ${short(d.build.commit)}: the gate's make check starts`]
  }
  return []
}

const step = (s: GateStep): Line => ({ mark: s.ok ? '✓' : '×', text: clean(s.name), ...(s.detail ? { detail: clean(s.detail) } : {}) })

// gateSteps is each step of the gate with its mark: gate.json's steps and live steps once it is there;
// before, the gate's row, which has the server's make check, and the updater's steps of this gate's
// request while it runs (its first step, Ask, is the caller's, which the row has).
export function gateSteps(d: Detail, current: Current | null | undefined): Line[] {
  if (d.gateFile) return [...d.gateFile.steps, ...(d.gateFile.liveSteps ?? [])].map(step)
  const out = (d.gate?.steps ?? []).map(step)
  if (current && current.action === 'gate' && current.version === d.version) {
    for (const s of current.steps ?? []) if (s.step >= 2) out.push(step(s))
  }
  return out
}

// notRun is what the gate did not run: the install test with its note, and each step that was skipped,
// with why. A release with one never installs by itself (16.3).
export function notRun(d: Detail): string[] {
  const f = d.gateFile
  if (!f) return []
  const out: string[] = []
  if (f.installTest === 'skipped') out.push(`The install test: ${clean(f.installTestNote || 'skipped')}`)
  return out.concat((f.skipped ?? []).map(clean))
}

const waitWords: Record<string, string> = {
  model: 'its suite waits for a model at a limit',
  login: 'its suite waits for a sign-in',
  held: 'its suite waits: new runs are held',
  room: 'its suite waits for room on the machine',
}

const verdictWords: Record<string, string> = { not_worse: 'not worse', worse: 'worse' }

function suiteLine(l: LiveSuite): Line {
  return { ...suiteWords(l), free: true }
}

function suiteWords(l: LiveSuite): Line {
  const s = { ...l, project: clean(l.project) }
  if (s.note) return { mark: '·', text: s.project, detail: `not compared: ${clean(s.note)}` }
  if (!s.suite) return { mark: '·', text: s.project, detail: 'waits its turn' }
  if (s.verdict) return { mark: s.verdict === 'worse' ? '×' : s.verdict === 'not_worse' ? '✓' : '·', text: s.project, detail: verdictWords[s.verdict] ?? clean(s.verdict) }
  switch (s.state) {
    case 'queued':
      return { mark: '·', text: s.project, detail: 'its suite waits in the queue' }
    case 'running':
      return { mark: '·', text: s.project, detail: (s.waitFor && waitWords[s.waitFor]) || 'its suite runs' }
  }
  return { mark: '·', text: s.project, detail: `its suite is ${clean(s.state ?? 'gone')}` }
}

function ended1(name: string, s: LiveStep): Line {
  if (s.skipped) return { mark: '·', text: name, detail: `not run: ${clean(s.skipped)}` }
  return { mark: s.ok ? '✓' : '×', text: name, ...(s.detail ? { detail: clean(s.detail) } : {}) }
}

// liveLines is where the live part is while it runs: the real ticket on crew-gate, then each project
// of the eval suites, or each step once it ended. Once the gate's row is no longer live, gate.json's
// live steps say it, and these are none.
export function liveLines(d: Detail): Line[] {
  if (d.gate?.state !== 'live') return []
  const lp = d.gate.livePart ?? {}
  const out: Line[] = []
  if (lp.real) out.push(ended1('The real ticket', lp.real))
  else if (lp.ticket) out.push({ mark: '·', text: `The real ticket ${clean(lp.ticket)} on crew-gate`, detail: lp.stage ? (stageName[lp.stage] ?? clean(lp.stage)) : 'being added' })
  else out.push({ mark: '·', text: 'The real ticket', detail: 'starts on crew-gate' })
  if (lp.suitesStep) out.push(ended1('The eval suites', lp.suitesStep))
  else for (const s of lp.suites ?? []) out.push(suiteLine(s))
  return out
}

// noDiffWhy is why a waiting gate's diff is empty, by the server's noDiff (review of M16, F1).
const noDiffWhy: Record<string, string> = {
  unread: 'the running release cannot be read, so the diff cannot be shown',
  noInputs: 'a release records no runtime inputs, so the diff cannot be shown',
  badHash: "a release's hash of its runtime inputs is not the hash of the inputs it records, so the diff cannot be trusted",
}

// pressable is whether Run the live gate is offered: on a waiting gate whose diff and the hash of its
// runtime inputs are there, so the press names what was shown. An empty diff means the running
// release could not be read, or a release's record of its inputs cannot give one (noDiffWhy), and
// nothing is shown to press on (cold read of P5, G6).
export function pressable(d: Detail): boolean {
  return d.gate?.state === 'waiting' && (d.gate.diff ?? []).length > 0 && !!d.manifest?.runtimeSha256
}

// pressText is what Run the live gate asks before it sends the press (16.2 step 5, security review of
// the eval suites). It names no project: no endpoint gives each project's network mode, and the
// suites' projects are fixed only after the real ticket.
export function pressText(): string {
  return (
    "Run the live gate gives this release's runtime image the agent CLI's login. It runs the real ticket on crew-gate and, when " +
    "the release changes what agents see, the eval suites' builders, reviewers, and testers in each project with active cases, " +
    "each with that project's network mode, setup, and read-only GitHub token, as any run of it. Its image is other code than the " +
    'running release\'s, and the live steps rest on what its runs did and said, a reviewer\'s verdict among them, so the release ' +
    'waits for Install even when its live steps pass.'
  )
}

const maxLog = 64 << 10

// clean is free text as the panel shows it: lines and tabs, without colours, control characters, or
// characters that reorder what follows. Step details, errors, notes, and project names come from the
// gate's row, the updater, and the projects, and some of them from the output of code agents wrote,
// which the updater screens in gate.json and nothing screens elsewhere (review of P5).
export function clean(s: string): string {
  return s
    .replace(/\u001b\][^\u0007\u001b]*(?:\u0007|\u001b\\)/g, '')
    .replace(/\u001b\[[0-9;?]*[ -/]*[@-~]/g, '')
    .replace(/[\p{Cc}\p{Cf}\p{Zl}\p{Zp}]/gu, (c) => (c === '\n' || c === '\t' ? c : ''))
}

// logText is a log as the panel shows it, as clean text, and the end of it: at most the 64 KB the
// server keeps. The build's and make check's output is of code agents wrote.
export function logText(s: string): string {
  const t = clean(s)
  return t.length > maxLog ? t.slice(t.length - maxLog) : t
}


// shownPath is a path of the diff the press is asked on, with each character outside printable ASCII
// shown as its code point, not removed, and so the brackets of those marks too: a path is a file name
// agents chose, and no invisible, reordering, or look-alike character may let one pass for another
// (security review of P5, two rounds). The runtime inputs' paths are ASCII in Crew's own tree.
export function shownPath(p: string): string {
  return p.replace(/[^\x20-\x3b\x3d\x3f-\x7e]/gu, (c) => `<U+${(c.codePointAt(0) ?? 0).toString(16).toUpperCase().padStart(4, '0')}>`)
}

// Press is what Run the live gate's confirmation was opened on: the version, the diff it showed, and
// the hash of those runtime inputs, which the press sends. It stays as it was while the screen reads
// the detail again, and while a newer candidate takes the panel (reviews of P5).
export type Press = { version: string; diff: string[]; runtime: string }

export function openPress(d: Detail): Press {
  return { version: d.version, diff: d.gate?.diff ?? [], runtime: d.manifest?.runtimeSha256 ?? '' }
}

export const pressPath = (c: Press): string => `/api/crew/versions/${c.version}/gate`

// sendPress sends Run the live gate for the confirmation c, by post (the API's send): the hash it
// was opened on, to the version it was opened on. It gives the server's words when the server
// refused the press, and '' when it took it.
export function sendPress(c: Press, post: (method: 'POST', path: string, body: Record<string, unknown>) => Promise<unknown>): Promise<string> {
  return post('POST', pressPath(c), { runtimeSha256: c.runtime }).then(
    () => '',
    (e: Error) => e.message,
  )
}
