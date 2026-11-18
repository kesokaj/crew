import { renderToStaticMarkup } from 'react-dom/server'
import { describe, expect, it } from 'vitest'
import { type Detail, pressText } from '../lib/gate'
import { GatePanel, GateView, type GateViewProps } from './gate'

// The gate's panel as markup (16.2 step 6, since 0.52): what it shows in each state of the gate, and
// that the press is offered only as lib/gate.ts allows. The clicks themselves are the props' callers.
const none = () => {}
const text = (html: string) =>
  html.replaceAll('&#x27;', "'").replaceAll('&quot;', '"').replaceAll('&lt;', '<').replaceAll('&gt;', '>').replaceAll('&amp;', '&')
const view = (p: Partial<GateViewProps>) =>
  renderToStaticMarkup(
    <GateView version="0.3.0" det={null} error="" confirm={null} busy={false} failed="" onOpen={none} onPress={none} onCancel={none} {...p} />,
  )
const runtime = 'f'.repeat(64)
const waiting: Detail = {
  version: '0.3.0',
  manifest: { runtimeSha256: runtime },
  build: { state: 'done', commit: 'abcdef1234567890', log: 'step 1\u001b[31m red\u001b[0m\n' },
  gate: { state: 'waiting', steps: [{ name: 'make check at abcdef123456', ok: true }], log: 'ok\u0007\n', diff: ['image/Dockerfile', 'cmd/crew-run/x\u202e.go'] },
}
const pressButton = '>Run the live gate</button>'
// marked says the markup shows needle, and only inside elements marked data-free-text.
const marked = (html: string, needle: string) => {
  const all = html.split(needle).length - 1
  const free = [...html.matchAll(/<\w+[^>]*data-free-text="true"[^>]*>([^<]*)</g)].reduce((n, m) => n + (m[1] ?? '').split(needle).length - 1, 0)
  return all > 0 && free === all
}

describe('GateView', () => {
  // What a version holds, its kind, and its release.json (16.2 step 6, since 0.54): the tickets' titles,
  // the outside commits' subjects, the protected paths and the agy pin are text from outside the app.
  it('says what the version holds, its kind, and its release.json', () => {
    const det: Detail = {
      ...waiting,
      manifest: { runtimeSha256: runtime, commit: 'abcdef1234567890', schema: 64, pins: { agy: '1.$2' } },
      changes: {
        since: { version: '0.2.19', commit: '79f63a9aaaaaaaaa' },
        tickets: [{ key: 'CREW-1', title: 'Cut the $3 estimate', commit: 'c1' }],
        outside: [{ commit: '0123456789ab', subject: 'Push $4' }],
        cut: { tickets: false, outside: true },
      },
      kind: { kind: 'code', changed: 12, protected: ['internal/$5/'] },
    }
    const html = view({ det })
    const t = text(html)
    expect(t).toContain('What is in it, since 0.2.19')
    expect(t).toContain('CREW-1')
    expect(marked(html, 'Cut the $3 estimate')).toBe(true)
    expect(t).toContain('From outside')
    expect(t).toContain('0123456')
    expect(marked(html, 'Push $4')).toBe(true)
    expect(t).toContain('only the newest are listed')
    expect(t).toContain('Against the running release: code, 12 paths changed')
    expect(marked(html, 'internal/$5/')).toBe(true)
    expect(t).toContain('schema 64')
    expect(marked(html, '1.$2')).toBe(true)
    expect(marked(html, 'What is in it')).toBe(false)
    const unread = view({ det: { ...det, changes: { tickets: [], outside: [], cut: { tickets: false, outside: false }, note: "Crew's project is not made" } } })
    expect(text(unread)).toContain("What is in it cannot be read: Crew's project is not made")
  })
  // Since 0.53 the gate's browser check reads Versions too: what a build, a command, the updater, or a
  // candidate's tree said is marked as not the app's own; the app's words around it are read.
  it("marks the text that comes from outside the app, and not the app's own words", () => {
    const failed: Detail = {
      version: '0.3.0',
      build: { state: 'done', commit: 'abcdef1234567890', log: 'npm said $3\n' },
      gate: { state: 'failed', steps: [], log: 'go said $4\n', error: 'the updater: $5', diff: [] },
      gateFile: {
        passed: false,
        installTest: 'skipped',
        installTestNote: 'the note $6',
        steps: [{ name: 'make check', ok: false, detail: 'it ended with 2: $7' }],
        skipped: ['the eval suites: $8 shop: not compared'],
      },
    }
    const html = view({ det: failed })
    for (const said of ['npm said $3', 'go said $4', 'it ended with 2: $7', 'the eval suites: $8 shop: not compared']) {
      expect(marked(html, said), said).toBe(true)
    }
    expect(marked(html, 'The gate failed at ')).toBe(false)
    expect(html).toContain('The gate failed at ')
    const live: Detail = { ...waiting, gate: { ...waiting.gate!, state: 'live', livePart: { ticket: 'GATE-1', suites: [{ project: '$9 shop', note: 'paused' }] } } }
    const liveHtml = view({ det: live })
    expect(marked(liveHtml, '$9 shop')).toBe(true)
    // The app's own words on a live line stay read (review of a9532aa).
    expect(marked(liveHtml, 'The real ticket GATE-1 on crew-gate')).toBe(false)
    expect(liveHtml).toContain('The real ticket GATE-1 on crew-gate')
    // What the server answered, an error or a refused press, is not the app's own either.
    expect(marked(view({ det: null, error: 'it said $12' }), 'it said $12')).toBe(true)
    expect(marked(view({ det: waiting, failed: 'refused: $13' }), 'refused: $13')).toBe(true)
    const diffed = view({ det: { ...waiting, gate: { ...waiting.gate!, diff: ['image/$10.go'] } }, confirm: { version: '0.3.0', diff: ['image/$11.go'], runtime } })
    expect(marked(diffed, 'image/$10.go')).toBe(true)
    expect(marked(diffed, 'image/$11.go')).toBe(true)
  })
  it('shows a waiting gate with its steps, its diff with each path shown, and Run the live gate', () => {
    const html = view({ det: waiting })
    const t = text(html)
    expect(html).toContain('data-gate="0.3.0"')
    expect(t).toContain('The gate of 0.3.0')
    expect(t).toContain("Waits for Run the live gate: its runtime image is other code than the running release's")
    expect(t).toContain('make check at abcdef123456')
    expect(t).toContain('image/Dockerfile')
    expect(t).toContain('cmd/crew-run/x<U+202E>.go')
    expect(html).not.toContain('\u202e')
    expect(html).toContain(pressButton)
    expect(html).not.toContain('data-gate-confirm')
  })
  it('offers no press on a waiting gate with no diff, and says why', () => {
    const html = view({ det: { ...waiting, gate: { ...waiting.gate!, diff: [] } } })
    expect(text(html)).toContain('which is not offered: the running release cannot be read')
    expect(html).not.toContain(pressButton)
    expect(html).not.toContain('data-gate-diff')
  })
  it('asks once, naming the version, what the press lets the image do, and the diff it was opened on', () => {
    // A newer candidate took the panel after the confirmation was opened: it still names its own version.
    const html = view({ version: '0.3.1', det: waiting, confirm: { version: '0.3.0', diff: ['image/old\u2066'], runtime } })
    const t = text(html)
    expect(html).toContain('data-gate-confirm="0.3.0"')
    expect(t).toContain('Run the live gate of 0.3.0')
    expect(t).toContain(pressText())
    expect(t).toContain('image/old<U+2066>')
    expect(t).toContain('>Run it</button>')
    expect(t).toContain('>Cancel</button>')
    // The button that opened it is gone while it is open.
    expect(html).not.toContain(pressButton)
    expect(view({ det: waiting, busy: true, confirm: { version: '0.3.0', diff: [], runtime } })).toMatch(/disabled=""[^>]*>Run it/)
  })
  it("shows the server's words when it refused a press, screened", () => {
    expect(text(view({ det: waiting, failed: 'the release\u202e is gone' }))).toContain('the release is gone')
  })
  // A gate.json from before 0.53 may name what its live steps cost: the panel shows no cost (7.7).
  it('shows a failed gate with its failed step, what did not run, no cost, and no press', () => {
    const det: Detail = {
      version: '0.3.0',
      gate: { state: 'failed', steps: [], log: '', error: "the updater's gate failed" },
      gateFile: {
        passed: false,
        installTest: 'skipped',
        installTestNote: 'updates.install_test is off',
        steps: [
          { name: 'make check', ok: true },
          { name: 'Check', ok: false, detail: 'the selftest: exit 1' },
        ],
        costUsd: 3.75,
      } as Detail['gateFile'],
    }
    const t = text(view({ det }))
    expect(t.replace(/<span data-free-text="true">([^<]*)<\/span>/g, '$1')).toContain('The gate failed at Check: the selftest: exit 1')
    expect(t).toContain('×</span> Check')
    expect(t).toContain('What did not run')
    expect(t).toContain('The install test: updates.install_test is off')
    expect(t).not.toContain('cost')
    expect(t).not.toContain('$')
    expect(t).not.toContain(pressButton)
  })
  // The eval suites' numbers (16.2 step 5, P5 of M16): each compared project's line, as the step's
  // detail has it, is on the screen, and a worse one holds the release, which Versions says.
  it("shows the eval suites' numbers of each project, and a worse one's hold", () => {
    const det: Detail = {
      version: '0.3.0',
      gate: { state: 'done', steps: [], log: '' },
      gateFile: {
        passed: true,
        installTest: 'skipped',
        steps: [
          { name: 'the real ticket on crew-gate', ok: true, detail: 'GATE-7 reached Done on its checks' },
          {
            name: 'the eval suites',
            ok: false,
            detail: 'shop: worse: 6 of 10 cases met their goal, against 8 of 10; wordcount: not worse: 5 of 5, against 5 of 5',
          },
        ],
        hold: 'the eval suites measured worse in 1 of 2 compared projects; the numbers are on Versions',
      } as Detail['gateFile'],
    }
    const t = text(view({ det })).replace(/<span data-free-text="true">([^<]*)<\/span>/g, '$1')
    expect(t).toContain('shop: worse: 6 of 10 cases met their goal, against 8 of 10')
    expect(t).toContain('wordcount: not worse: 5 of 5, against 5 of 5')
    expect(t).toContain('the eval suites measured worse in 1 of 2 compared projects')
  })
  it('follows the live part while it runs', () => {
    const det: Detail = { version: '0.3.0', gate: { state: 'live', steps: [], log: '', livePart: { ticket: 'GATE-1', stage: 'review' } } }
    const t = text(view({ det }))
    expect(t).toContain('The live part')
    expect(t).toContain('The real ticket GATE-1 on crew-gate')
    expect(t).toContain('Review')
  })
  it('shows the logs collapsed, as text', () => {
    const html = view({ det: waiting })
    const t = text(html)
    expect(html).toMatch(/<details><summary[^>]*>The build(&#x27;|')s log<\/summary><pre[^>]*>step 1 red\n<\/pre><\/details>/)
    expect(t).toContain("make check's log")
    expect(html).not.toContain('\u001b')
    expect(html).not.toContain('\u0007')
  })
  it('says so when the detail cannot be read', () => {
    const t = text(view({ error: 'HTTP\u202e 500' }))
    expect(t).toContain('The gate could not be read: <span data-free-text="true">HTTP 500</span>')
    expect(t).not.toContain('data-gate-state')
  })
})

describe('GatePanel', () => {
  it('is the panel of its version before its detail is read', () => {
    const html = renderToStaticMarkup(<GatePanel version="0.3.1" onPress={none} />)
    expect(html).toContain('data-gate="0.3.1"')
    expect(text(html)).toContain('The gate of 0.3.1')
    expect(html).not.toContain('data-gate-state')
  })
})
