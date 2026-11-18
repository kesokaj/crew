import { renderToStaticMarkup } from 'react-dom/server'
import { describe, expect, it } from 'vitest'
import { BuildReleaseView, DailyLookView, type DailyLookProps, DeployFilesRow, LastChanges, NextCommits, nextWhy, ReleaseMeasureLine, ReleaseWhat, sendBuild, UpdaterSteps } from './versions'

// Since 0.53 the gate's browser check reads Versions too (the operator's choice of report 9). What
// the screen shows from outside the app is marked data-free-text: a commit's subject, which an agent
// or the operator wrote, a release's title, what the updater or a command said. The app's own words
// around them are read.

// marked says the markup shows needle, and only inside elements marked data-free-text.
const marked = (html: string, needle: string) => {
  const all = html.split(needle).length - 1
  const free = [...html.matchAll(/<\w+[^>]*data-free-text="true"[^>]*>([^<]*)</g)].reduce((n, m) => n + (m[1] ?? '').split(needle).length - 1, 0)
  return all > 0 && free === all
}

describe("Versions' own text and what it shows from outside", () => {
  it("says whether crew-ctl and its files are the running release's, with the command for root", () => {
    const same = renderToStaticMarkup(<DeployFilesRow f={{ state: 'same' }} />)
    expect(same).toContain('the running release&#x27;s, as install recorded them')
    expect(same).not.toContain('As root')
    const command = 'd=$(mktemp -d) &amp;&amp; tar -xzf &#x27;/var/lib/crew/releases/0.2.23/deploy.tar.gz&#x27; -C &quot;$d&quot; deploy/crew-ctl &amp;&amp; &quot;$d/deploy/crew-ctl&quot; install --deploy-files'
    const differ = renderToStaticMarkup(
      <DeployFilesRow
        f={{
          state: 'differ',
          differ: 2,
          paths: ['deploy/compose.yaml', 'versions.lock'],
          command: 'd=$(mktemp -d) && tar -xzf \'/var/lib/crew/releases/0.2.23/deploy.tar.gz\' -C "$d" deploy/crew-ctl && "$d/deploy/crew-ctl" install --deploy-files',
        }}
      />,
    )
    expect(differ).toContain('2 files differ')
    expect(marked(differ, 'deploy/compose.yaml')).toBe(true)
    expect(marked(differ, 'versions.lock')).toBe(true)
    expect(differ).toContain(command)
    const unchecked = renderToStaticMarkup(<DeployFilesRow f={{ state: 'differ', differ: 1, paths: ['deploy/crew-ctl'], error: 'the deploy archive is not the one release.json names' }} />)
    expect(unchecked).toContain('not the one release.json names')
    expect(unchecked).not.toContain('As root')
    const unrecorded = renderToStaticMarkup(<DeployFilesRow f={{ state: 'unrecorded', command: 'x install --deploy-files' }} />)
    expect(unrecorded).toContain('not recorded')
    expect(unrecorded).toContain('x install --deploy-files')
    expect(renderToStaticMarkup(<DeployFilesRow f={{}} />)).toContain('no deploy archive')
  })

  it("marks the commit subjects of a release's notes, and not the kind", () => {
    const notes = '# Crew 0.2.18\n\nThe commits since 7361384ca931:\n\n- Mark a run\'s own words: the gate read a run\'s "$3.54" (d3e77d0)\n'
    const html = renderToStaticMarkup(<NextCommits notes={notes} kind="code" />)
    expect(marked(html, 'the gate read a run')).toBe(true)
    expect(html).toContain('>code<')
    expect(marked(html, 'code')).toBe(false)
  })

  it("marks a release's title and the updater's failure, and says the rest itself", () => {
    const html = renderToStaticMarkup(
      <ReleaseWhat
        r={{ version: '0.2.18', commit: 'b227d3831d1c00', title: 'Fix the $3 estimate', failure: 'the browser tests: $4', state: 'failed', byHand: false, published: {}, gate: { passed: true } }}
        publish={false}
      />,
    )
    expect(marked(html, 'Fix the $3 estimate')).toBe(true)
    expect(marked(html, 'the browser tests: $4')).toBe(true)
    expect(marked(html, "Stopped by the updater's check: ")).toBe(false)
    expect(html).toContain('not copied to GitHub: copying is off')
  })

  it('marks what the updater said of a request that ended, and of each step of the one that runs', () => {
    const error = 'Check: the browser tests: no screen shows a dollar, an estimate, or a budget'
    const html = renderToStaticMarkup(
      <LastChanges
        requests={[
          { id: 2, version: '0.2.17', from: '0.2.16', action: 'gate', state: 'failed', error, askedAt: '2026-10-21T23:46:51Z', endedAt: '2026-10-21T23:55:48Z' },
          { id: 1, version: '0.2.16', from: '0.2.15', action: 'install', state: 'done', askedAt: '2026-10-21T21:17:31Z' },
        ]}
      />,
    )
    expect(marked(html, error)).toBe(true)
    expect(marked(html, 'The gate did not pass: ')).toBe(false)
    expect(html).toContain('Checked beside 0.2.15, switched, and healthy for ten minutes.')
    const steps = renderToStaticMarkup(<UpdaterSteps steps={[{ step: 3, name: 'Try the state', ok: false, detail: 'restore: $5' }]} />)
    expect(marked(steps, 'restore: $5')).toBe(true)
    expect(marked(steps, 'Try the state')).toBe(false)
  })
})

// Build a release on Versions (16.2 step 3, since 0.54): offered where the server builds and no build
// runs, with a choice of patch, minor, or major; a press asks once, naming the bump; a refusal shows
// the server's words, screened. No browser test presses it: a press starts a real build.
describe('Build a release', () => {
  const none = () => {}
  const view = (p: Partial<Parameters<typeof BuildReleaseView>[0]>) =>
    renderToStaticMarkup(<BuildReleaseView canBuild building="" bump="patch" confirming={false} busy={false} failed="" onBump={none} onOpen={none} onPress={none} onCancel={none} {...p} />)
  it('is offered with a choice of number, where the server builds and no build runs', () => {
    const html = view({})
    expect(html).toContain('>Build a release</button>')
    for (const b of ['patch', 'minor', 'major']) expect(html).toContain(`value="${b}"`)
    expect(view({ canBuild: false })).toBe('')
    const running = view({ building: '0.2.20' })
    expect(running).not.toContain('>Build a release</button>')
    expect(running).toContain('A build of 0.2.20 runs')
  })
  it('asks once, naming the bump, and shows a refusal screened', () => {
    const html = view({ confirming: true, bump: 'minor' })
    expect(html).toContain('Build the next minor release from the newest commit of Crew&#x27;s project&#x27;s main?')
    expect(html).toContain('>Build it</button>')
    expect(html).toContain('>Cancel</button>')
    expect(view({ failed: 'main has not moved\u202e' })).toContain('main has not moved<')
  })
  it('posts the bump, and gives the server\'s words when it refuses', async () => {
    const sent: unknown[] = []
    expect(await sendBuild('minor', (...a) => (sent.push(a), Promise.resolve({})))).toBe('')
    expect(sent).toEqual([['POST', '/api/crew/versions/build', { bump: 'minor' }]])
    expect(await sendBuild('patch', () => Promise.reject(new Error('a build runs')))).toBe('a build runs')
  })
})

describe('the daily look', () => {
  const at = '2026-10-23T03:00:00Z'
  const look = {
    at,
    made: ['go@1.27.4'],
    lines: [
      { source: 'go.dev', text: 'Go 1.27.4, newer than the pin 1.27.3', ok: true },
      { source: 'GO_IMAGE', text: 'not read: it answered 404', ok: false },
    ],
  }
  const view = (p: Partial<DailyLookProps>) =>
    renderToStaticMarkup(
      <DailyLookView look={look} looks now={Date.parse(at) + 5 * 60000} confirming={false} busy={false} failed="" onOpen={() => {}} onPress={() => {}} onCancel={() => {}} {...p} />,
    )

  it("says when the last look was, marks what the publishers gave, and offers Look now", () => {
    const html = view({})
    expect(html).toContain('The last look was 5 min ago')
    expect(html).not.toContain('more than two days ago')
    expect(marked(html, 'Go 1.27.4, newer than the pin 1.27.3')).toBe(true)
    expect(marked(html, 'go@1.27.4')).toBe(true)
    expect(html).toContain('Look now…')
    expect(html).not.toContain('data-look-confirm')
  })

  it('says a look older than two days, and none yet', () => {
    expect(view({ now: Date.parse(at) + 3 * 24 * 3600 * 1000 })).toContain('more than two days ago')
    expect(view({ look: null })).toContain('No look yet.')
  })

  it("asks before it looks, and a server without the look offers none", () => {
    const confirm = view({ confirming: true })
    expect(confirm).toContain('data-look-confirm')
    expect(confirm).toContain('makes a ticket on Crew&#x27;s project for each new version')
    const check = view({ looks: false })
    expect(check).toContain('The daily look does not run on this server.')
    expect(check).not.toContain('Look now')
  })
})

describe("a release's measure", () => {
  // Every release is compared with the one it replaced, once 20 tickets ran on it (10.7, since 0.57).
  const results = [
    { metric: 'first_request_pass_rate', before: 1, after: 0, ticketsBefore: 20, ticketsAfter: 20, margin: 0.1, verdict: 'worse', why: '' },
    { metric: 'passed_first_time_rate', before: 1, after: 0, ticketsBefore: 20, ticketsAfter: 20, margin: 0.1, verdict: 'worse', why: '' },
    { metric: 'fix_rounds_per_ticket', before: 0.4, after: 0.4, ticketsBefore: 20, ticketsAfter: 20, margin: 0.2, verdict: 'same', why: '' },
    { metric: 'tokens_per_ticket', before: 120000, after: 118000, ticketsBefore: 20, ticketsAfter: 20, margin: 0.2, verdict: 'same', why: '' },
    { metric: 'interventions_per_ticket', before: 0, after: 0, ticketsBefore: 20, ticketsAfter: 20, margin: 0.2, verdict: 'same', why: '' },
    { metric: 'escaped_defects_per_ticket', before: 0, after: 0, ticketsBefore: 20, ticketsAfter: 20, margin: 0.2, verdict: 'same', why: '' },
  ]
  const line = (m: Parameters<typeof ReleaseMeasureLine>[0]['m'], installed = true) =>
    renderToStaticMarkup(<ReleaseMeasureLine m={m} installed={installed} window={20} />)

  it('says when a release is measured, and nothing of one never installed', () => {
    const html = line(undefined)
    expect(html).toContain('data-measure')
    expect(html).toContain('measured once 20 tickets ran on it')
    expect(line(undefined, false)).toBe('')
  })

  it('names the release before and each worse metric, with the six numbers under it', () => {
    const html = line({ before: '0.2.0', verdict: 'worse', why: 'x', ticketsBefore: 20, ticketsAfter: 20, window: 20, results })
    // The line names the worse metrics alone; the details follow it.
    expect(html).toContain('worse than 0.2.0: checks passed on the first request, passed the first time<details')
    expect(html).toContain('data-measure-numbers')
    expect(html).toContain('Checks passed on the first request: 100% → 0%, worse')
    expect(html).toContain('Fix rounds per ticket: 0.40 → 0.40, the same')
    expect(html).toContain('Tokens per ticket: 120000 → 118000, the same')
    expect(html).not.toContain('data-free-text')
  })

  it('says the same, too few tickets, and not compared in its own words', () => {
    expect(line({ before: '0.2.1', verdict: 'same', why: 'the same as 0.2.1 over 20 tickets each', ticketsBefore: 20, ticketsAfter: 20, window: 20, results })).toContain(
      'the same as 0.2.1 over 20 tickets each',
    )
    const few = line({
      before: '0.2.1', verdict: 'inconclusive', why: 'too few tickets against 0.2.1: 3 on it and 7 on 0.2.1, of 20 each', ticketsBefore: 7, ticketsAfter: 3, window: 20, results,
    })
    expect(few).toContain('too few tickets against 0.2.1: 3 on it and 7 on 0.2.1, of 20 each')
    const none = line({ verdict: 'not_compared', why: 'Crew does not know what ran before it', ticketsBefore: 0, ticketsAfter: 0, window: 20 })
    expect(none).toContain('not compared: Crew does not know what ran before it')
    expect(none).not.toContain('data-measure-numbers')
  })
})

describe('whether the next release installs by itself', () => {
  // Its clock says so: auto_install never, which another installation takes (16.12), gives none, and a
  // release of prompts then waits for Install like one of code.
  it('says a release with no clock waits for Install, whatever it changes', () => {
    const at = '2026-10-25T10:00:00Z'
    expect(nextWhy({ kind: 'text', held: false, installAfter: at })).toBe('It changes only prompts and harness text, so it installs by itself unless you hold it.')
    expect(nextWhy({ kind: 'text', held: false })).toBe('It changes only prompts and harness text, and does not install by itself here. It waits until you press Install now.')
    expect(nextWhy({ kind: 'code', held: false })).toBe('It changes code, so it does not install by itself. It waits until you press Install now.')
    expect(nextWhy({ kind: 'code', held: false, installAfter: at })).toBe('It changes code and touches no protected part, so it installs by itself unless you hold it.')
    expect(nextWhy({ kind: 'text', held: true, heldWhy: 'a suite measured worse', installAfter: at })).toBe('It changes only prompts and harness text, and is held: a suite measured worse')
    expect(nextWhy({ kind: 'setup', held: false })).toContain('crew-ctl install --upgrade-runtime')
  })
})
