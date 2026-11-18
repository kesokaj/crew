import { describe, expect, it } from 'vitest'
import {
  type Detail,
  gateState,
  gateSteps,
  liveLines,
  logText,
  notRun,
  openPress,
  pressable,
  pressPath,
  pressText,
  sendPress,
  shownPath,
} from './gate'
import { saidText } from './said'

const commit = 'abcdef1234567890abcdef1234567890abcdef12'
const build = (state: string, error?: string): Detail['build'] => ({ state, commit, log: '', error })
const row = (state: string, more: Partial<NonNullable<Detail['gate']>> = {}): Detail['gate'] => ({ state, steps: [], log: '', ...more })
const makeCheck = { name: 'make check at abcdef123456, in a sandbox of its own', ok: true }
const passed = { passed: true, steps: [makeCheck], installTest: 'skipped', live: 'passed' }
const stateOf = (d: Detail) => saidText(gateState(d))

describe('gateState', () => {
  // What a build, a step, or the updater said is text from outside the app, marked on the screen; the
  // app's own words around it are read by the gate's no-dollar check (0.53).
  it('gives an error and a failed step\'s detail as free parts', () => {
    expect(gateState({ version: '0.3.0', build: build('failed', 'it printed $3') })).toEqual(['The build failed: ', { free: 'it printed $3' }])
    expect(gateState({ version: '0.3.0', gate: row('failed', { error: 'the updater: $3' }) })).toEqual(['The gate failed: ', { free: 'the updater: $3' }])
    const failedStep = { passed: false, steps: [{ name: 'make check', ok: false, detail: 'it ended with 2: $3' }], installTest: 'skipped' }
    expect(gateState({ version: '0.3.0', gateFile: failedStep })).toEqual(['The gate failed at ', { free: 'make check' }, ': ', { free: 'it ended with 2: $3' }])
    expect(gateState({ version: '0.3.0', gateFile: { ...passed, hold: 'worse in 1 of 2' } })).toEqual([
      'The gate passed, and holds the release for Install: ',
      { free: 'worse in 1 of 2' },
    ])
    expect(gateState({ version: '0.3.0', gateFile: passed })).toEqual(['The gate passed'])
  })

  it('says where a build is before its gate', () => {
    expect(stateOf({ version: '0.3.0', build: build('running') })).toBe('Building 0.3.0 from abcdef123456')
    expect(stateOf({ version: '0.3.0', build: build('failed', 'the build ended with 2') })).toBe('The build failed: the build ended with 2')
    expect(stateOf({ version: '0.3.0', build: build('done') })).toBe("Built from abcdef123456: the gate's make check starts")
  })
  it('says where the gate is while it runs', () => {
    expect(stateOf({ version: '0.3.0', gate: row('checking') })).toBe('make check runs on the exported commit, in a sandbox of its own')
    for (const s of ['asking', 'asked']) {
      expect(stateOf({ version: '0.3.0', gate: row(s) })).toBe(
        "The updater runs the gate's steps: the check server and the smoke run in microVMs, the selftest, and the browser tests",
      )
    }
    expect(stateOf({ version: '0.3.0', gate: row('live') })).toBe(
      'The live steps run on this server: the real ticket on crew-gate, then the eval suites when the release changes what agents see',
    )
    expect(stateOf({ version: '0.3.0', gate: row('done'), gateFile: { ...passed, live: 'pending' } })).toBe("The live steps' result goes to the updater")
    // A gate crew-ctl gate asked for, which the server has not taken up yet.
    expect(stateOf({ version: '0.3.0', gateFile: { ...passed, live: 'pending' } })).toBe(
      "The updater's steps passed: the live steps wait for this server to take them up",
    )
  })
  it('says a waiting gate waits for the press, or why it cannot be pressed', () => {
    expect(stateOf({ version: '0.3.0', gate: row('waiting', { diff: ['image/Dockerfile'] }) })).toBe(
      "Waits for Run the live gate: its runtime image is other code than the running release's",
    )
    expect(stateOf({ version: '0.3.0', gate: row('waiting') })).toBe(
      'Waits for Run the live gate, which is not offered: the running release cannot be read, so the diff cannot be shown',
    )
    // The server says why the diff is empty (review of M16, F1).
    expect(stateOf({ version: '0.3.0', gate: row('waiting', { noDiff: 'unread' }) })).toBe(
      'Waits for Run the live gate, which is not offered: the running release cannot be read, so the diff cannot be shown',
    )
    expect(stateOf({ version: '0.3.0', gate: row('waiting', { noDiff: 'noInputs' }) })).toBe(
      'Waits for Run the live gate, which is not offered: a release records no runtime inputs, so the diff cannot be shown',
    )
    expect(stateOf({ version: '0.3.0', gate: row('waiting', { noDiff: 'badHash' }) })).toBe(
      "Waits for Run the live gate, which is not offered: a release's hash of its runtime inputs is not the hash of the inputs it records, so the diff cannot be trusted",
    )
  })
  it('says how a gate ended, from its gate.json', () => {
    expect(stateOf({ version: '0.3.0', gate: row('done'), gateFile: passed })).toBe('The gate passed')
    expect(stateOf({ version: '0.2.6', gateFile: { passed: true, steps: [makeCheck], installTest: 'passed' } })).toBe('The gate passed')
    expect(stateOf({ version: '0.3.0', gateFile: { ...passed, live: 'skipped', skipped: ['the real ticket: no agent CLI is signed in'] } })).toBe(
      'The gate passed, and a step did not run: the release waits for Install',
    )
    const hold = 'the eval suites measured worse in 1 of 2 compared projects; the numbers are on Versions'
    expect(stateOf({ version: '0.3.0', gateFile: { ...passed, hold } })).toBe(`The gate passed, and holds the release for Install: ${hold}`)
    expect(stateOf({ version: '0.3.0', gate: row('done'), gateFile: { ...passed, passed: false, live: 'failed' } })).toBe('The gate failed in its live steps')
    // A failed live part is a failed gate, whatever its passed says, as release.Read takes it.
    expect(stateOf({ version: '0.3.0', gate: row('done'), gateFile: { ...passed, live: 'failed' } })).toBe('The gate failed in its live steps')
    // The updater's own step failed: gate.json is failed, with no live part, and Read would have left it out.
    expect(
      stateOf({
        version: '0.3.0',
        gate: row('failed', { error: "the updater's gate failed" }),
        gateFile: { passed: false, steps: [makeCheck, { name: 'Check', ok: false, detail: 'the selftest: exit 1' }], installTest: 'skipped' },
      }),
    ).toBe('The gate failed at Check: the selftest: exit 1')
    expect(stateOf({ version: '0.3.0', gate: row('failed', { error: 'make check: exit 2' }) })).toBe('The gate failed: make check: exit 2')
  })
})

describe('gateSteps', () => {
  it("reads gate.json's steps and live steps once it is there", () => {
    const d: Detail = {
      version: '0.3.0',
      gate: row('done', { steps: [makeCheck] }),
      gateFile: { ...passed, steps: [makeCheck, { name: 'Check', ok: true, detail: 'the selftest' }], liveSteps: [{ name: 'the real ticket', ok: true, detail: 'GATE-1 reached Done' }] },
    }
    expect(gateSteps(d, null)).toEqual([
      { mark: '✓', text: makeCheck.name },
      { mark: '✓', text: 'Check', detail: 'the selftest' },
      { mark: '✓', text: 'the real ticket', detail: 'GATE-1 reached Done' },
    ])
  })
  it("reads the gate's row and the updater's request before gate.json is there, each step once", () => {
    const current = {
      version: '0.3.0',
      action: 'gate',
      steps: [
        { step: 1, name: 'Ask', ok: true, detail: 'asked by the server' },
        { step: 2, name: 'Load', ok: true, detail: 'four images' },
        { step: 4, name: 'Check', ok: false, detail: 'the selftest: exit 1' },
      ],
    }
    const d: Detail = { version: '0.3.0', gate: row('asked', { steps: [makeCheck] }) }
    expect(gateSteps(d, current)).toEqual([
      { mark: '✓', text: makeCheck.name },
      { mark: '✓', text: 'Load', detail: 'four images' },
      { mark: '×', text: 'Check', detail: 'the selftest: exit 1' },
    ])
    // The updater's request of another version, or an install, is not this gate's.
    expect(gateSteps(d, { ...current, version: '0.3.1' })).toEqual([{ mark: '✓', text: makeCheck.name }])
    expect(gateSteps(d, { ...current, action: 'install' })).toEqual([{ mark: '✓', text: makeCheck.name }])
    expect(gateSteps({ version: '0.3.0', build: build('running') }, null)).toEqual([])
  })
})

describe('notRun', () => {
  it("says the install test's note and each step that did not run", () => {
    const note = 'the install test runs by crew-ctl gate, with the operator: a gate Crew drives does not start a virtual machine'
    expect(notRun({ version: '0.3.0', gateFile: { ...passed, installTestNote: note, skipped: ['the real ticket: no agent CLI is signed in'] } })).toEqual([
      `The install test: ${note}`,
      'the real ticket: no agent CLI is signed in',
    ])
    expect(notRun({ version: '0.3.0', gateFile: { ...passed, installTest: 'passed' } })).toEqual([])
    expect(notRun({ version: '0.3.0', gateFile: passed })).toEqual(['The install test: skipped'])
    expect(notRun({ version: '0.3.0', gate: row('live') })).toEqual([])
  })
})

describe('liveLines', () => {
  it('follows the real ticket on crew-gate while the live part runs', () => {
    expect(liveLines({ version: '0.3.0', gate: row('live') })).toEqual([{ mark: '·', text: 'The real ticket', detail: 'starts on crew-gate' }])
    expect(liveLines({ version: '0.3.0', gate: row('live', { livePart: { ticket: 'GATE-1' } }) })).toEqual([
      { mark: '·', text: 'The real ticket GATE-1 on crew-gate', detail: 'being added' },
    ])
    expect(liveLines({ version: '0.3.0', gate: row('live', { livePart: { ticket: 'GATE-1', stage: 'in_progress' } }) })).toEqual([
      { mark: '·', text: 'The real ticket GATE-1 on crew-gate', detail: 'In progress' },
    ])
  })
  it("names each project of the eval suites with its suite's state, verdict, or note", () => {
    const livePart = {
      ticket: 'GATE-1',
      real: { name: 'the real ticket', ok: true, detail: 'GATE-1 reached Done on its checks' },
      suites: [
        { project: 'shop', suite: 7, state: 'running', waitFor: 'model' },
        { project: 'tea', suite: 8, state: 'done', verdict: 'not_worse' },
        { project: 'web', suite: 9, state: 'done', verdict: 'worse' },
        { project: 'aaa', note: 'the project is paused' },
        { project: 'zzz' },
        { project: 'pub', suite: 10, state: 'queued' },
      ],
    }
    expect(liveLines({ version: '0.3.0', gate: row('live', { livePart }) })).toEqual([
      { mark: '✓', text: 'The real ticket', detail: 'GATE-1 reached Done on its checks' },
      { mark: '·', text: 'shop', detail: 'its suite waits for a model at a limit', free: true },
      { mark: '✓', text: 'tea', detail: 'not worse', free: true },
      { mark: '×', text: 'web', detail: 'worse', free: true },
      { mark: '·', text: 'aaa', detail: 'not compared: the project is paused', free: true },
      { mark: '·', text: 'zzz', detail: 'waits its turn', free: true },
      { mark: '·', text: 'pub', detail: 'its suite waits in the queue', free: true },
    ])
  })
  it('gives a step that ended, or was skipped, once', () => {
    const livePart = {
      real: { name: 'the real ticket', ok: false, detail: 'not run', skipped: 'no agent CLI is signed in' },
      suitesStep: { name: 'the eval suites', ok: true, detail: 'tea: not worse' },
      suites: [{ project: 'tea', suite: 8, state: 'done', verdict: 'not_worse' }],
    }
    expect(liveLines({ version: '0.3.0', gate: row('live', { livePart }) })).toEqual([
      { mark: '·', text: 'The real ticket', detail: 'not run: no agent CLI is signed in' },
      { mark: '✓', text: 'The eval suites', detail: 'tea: not worse' },
    ])
  })
  it('says nothing when the live part does not run: gate.json has its steps then', () => {
    const livePart = { ticket: 'GATE-1', real: { name: 'the real ticket', ok: true } }
    for (const s of ['done', 'failed', 'waiting', 'asked']) {
      expect(liveLines({ version: '0.3.0', gate: row(s, { livePart }) })).toEqual([])
    }
    expect(liveLines({ version: '0.3.0' })).toEqual([])
  })
})

describe('the press', () => {
  it('is offered on a waiting gate whose diff and hash are there', () => {
    const manifest = { runtimeSha256: 'f'.repeat(64) }
    expect(pressable({ version: '0.3.0', manifest, gate: row('waiting', { diff: ['image/Dockerfile'] }) })).toBe(true)
    expect(pressable({ version: '0.3.0', manifest, gate: row('waiting') })).toBe(false)
    expect(pressable({ version: '0.3.0', manifest, gate: row('waiting', { diff: [] }) })).toBe(false)
    expect(pressable({ version: '0.3.0', gate: row('waiting', { diff: ['image/Dockerfile'] }) })).toBe(false)
    expect(pressable({ version: '0.3.0', manifest, gate: row('live', { diff: ['image/Dockerfile'] }) })).toBe(false)
  })
  it('says what the press lets the candidate image do', () => {
    const t = pressText()
    for (const w of [
      "agent CLI's login",
      'the real ticket on crew-gate',
      'eval suites',
      'each project with active cases',
      'network mode',
      'setup',
      'read-only GitHub token',
      'waits for Install',
    ]) {
      expect(t).toContain(w)
    }
  })
})

describe('logText', () => {
  it('keeps a log as text: lines and tabs, no control characters, no colours, no reordering', () => {
    expect(logText('step 1\tok\r\nstep 2\u0007\u001b[31m red\u001b[0m\u0000\n\u202eevil\u2066')).toBe('step 1\tok\nstep 2 red\nevil')
  })
  it('keeps the end of a log over 64 KB, as the server does', () => {
    const long = 'a'.repeat(70 << 10) + 'END'
    const t = logText(long)
    expect(t.length).toBe(64 << 10)
    expect(t.endsWith('END')).toBe(true)
  })
})


// Every free text the panel shows is screened as the logs are: step details, errors, notes, and
// project names come from the gate's row, the updater's request, and the projects, and some from the
// output of code agents wrote (reviews of P5).
describe('the screened text', () => {
  const bad = 'it \u202epassed\u2066 \u061c\u001b]0;title\u0007\u001b[32mok\u001b[0m\u0000'
  const good = 'it passed ok'
  it("screens the updater's request and the gate's row", () => {
    const current = { version: '0.3.0', action: 'gate', steps: [{ step: 4, name: 'Check\u202e', ok: false, detail: bad }] }
    expect(gateSteps({ version: '0.3.0', gate: row('asked', { steps: [{ name: 'make check', ok: false, detail: bad }] }) }, current)).toEqual([
      { mark: '×', text: 'make check', detail: good },
      { mark: '×', text: 'Check', detail: good },
    ])
    expect(stateOf({ version: '0.3.0', gate: row('failed', { error: bad }) })).toBe(`The gate failed: ${good}`)
    expect(stateOf({ version: '0.3.0', build: build('failed', bad) })).toBe(`The build failed: ${good}`)
    expect(stateOf({ version: '0.3.0', gateFile: { ...passed, hold: bad } })).toBe(`The gate passed, and holds the release for Install: ${good}`)
    expect(stateOf({ version: '0.3.0', gateFile: { passed: false, installTest: 'skipped', steps: [{ name: 'Check', ok: false, detail: bad }] } })).toBe(
      `The gate failed at Check: ${good}`,
    )
    expect(notRun({ version: '0.3.0', gateFile: { ...passed, installTestNote: bad, skipped: [bad] } })).toEqual([`The install test: ${good}`, good])
  })
  it('screens the live part', () => {
    const livePart = {
      ticket: 'GATE-1',
      real: { name: 'the real ticket', ok: true, detail: bad },
      suites: [
        { project: 'shop\u202e', note: bad },
        { project: 'tea', suite: 8, state: 'done', verdict: 'odd\u202e' },
      ],
    }
    expect(liveLines({ version: '0.3.0', gate: row('live', { livePart }) })).toEqual([
      { mark: '✓', text: 'The real ticket', detail: good },
      { mark: '·', text: 'shop', detail: `not compared: ${good}`, free: true },
      { mark: '·', text: 'tea', detail: 'odd', free: true },
    ])
  })
  it('removes a whole OSC sequence and the Arabic letter mark from a log', () => {
    expect(logText('a\u001b]8;;http://x\u001b\\link\u001b]8;;\u001b\\ b\u061c')).toBe('alink b')
  })
})

describe('the diff the press is asked on', () => {
  it('shows each invisible or reordering character of a path, so a name cannot pass for another', () => {
    expect(shownPath('image/Dockerfile')).toBe('image/Dockerfile')
    expect(shownPath('image/a\u202egp.go')).toBe('image/a<U+202E>gp.go')
    expect(shownPath('cmd/crew-run/x\n1111  y.go')).toBe('cmd/crew-run/x<U+000A>1111  y.go')
    expect(shownPath('a\u200bb\u061c')).toBe('a<U+200B>b<U+061C>')
  })
  it('shows every character outside printable ASCII, and the brackets of its own marks (round 2)', () => {
    for (const p of ['image/Dockerfile', 'cmd/crew-run/main.go', 'versions.lock', 'image/a b+c@d=e,f:g~h-i_j.k']) {
      expect(shownPath(p)).toBe(p)
    }
    // Hangul fillers, the combining grapheme joiner, a variation selector, a letter that looks Latin.
    expect(shownPath('a\u3164b\u115fc\u034fd\ufe0fe\u0430')).toBe('a<U+3164>b<U+115F>c<U+034F>d<U+FE0F>e<U+0430>')
    // A name that holds a mark's own text cannot pass for the mark.
    expect(shownPath('a<U+202E>b>')).toBe('a<U+003C>U+202E<U+003E>b<U+003E>')
    expect(shownPath('x\u{1F600}')).toBe('x<U+1F600>')
  })
  it('keeps the version, the diff, and the hash it was opened on, and presses that version', () => {
    const d: Detail = { version: '0.3.0', manifest: { runtimeSha256: 'f'.repeat(64) }, gate: row('waiting', { diff: ['image/Dockerfile'] }) }
    const c = openPress(d)
    expect(c).toEqual({ version: '0.3.0', diff: ['image/Dockerfile'], runtime: 'f'.repeat(64) })
    // The detail moves on, the confirmation does not.
    d.version = '0.3.1'
    d.gate!.diff = ['image/other']
    expect(c.version).toBe('0.3.0')
    expect(pressPath(c)).toBe('/api/crew/versions/0.3.0/gate')
  })
})

describe('sendPress', () => {
  const c = { version: '0.3.0', diff: ['image/Dockerfile'], runtime: 'f'.repeat(64) }
  it("posts the confirmation's hash to its version, and gives nothing when the server took it", async () => {
    const sent: unknown[][] = []
    const why = await sendPress(c, (...a) => {
      sent.push(a)
      return Promise.resolve(null)
    })
    expect(sent).toEqual([['POST', '/api/crew/versions/0.3.0/gate', { runtimeSha256: 'f'.repeat(64) }]])
    expect(why).toBe('')
  })
  it("gives the server's words when it refused the press", async () => {
    const why = await sendPress(c, () => Promise.reject(new Error("the release's runtime image is not the one whose diff was shown")))
    expect(why).toBe("the release's runtime image is not the one whose diff was shown")
  })
})
