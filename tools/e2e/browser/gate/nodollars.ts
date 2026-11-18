import { expect, type Page } from '@playwright/test'

// Since design 0.53 Crew shows no cost and no budget anywhere: where a cost was, tokens and time are.
// This check opens the screens that showed one (the front page, Runs, Models, the Crew level, Versions,
// each project's Analytics and Tickets, and the first project's Plan, Agents, Trajectories, a ticket of
// it, and a run) and reads what the app itself says on them, its text and its titles and labels. It only
// reads: it opens screens and clicks nothing. It is shared by the gate's tests and the browser tests.
// On the gate's check server, which holds no key file, Setup is not done and the front page sends it
// to Setup: there the front page's projects are not read, and the browser tests of an installed
// machine, where Setup is done, read them.
//
// What is read, and why so: every text of the page, less the elements the app marks data-free-text,
// which render the operator's or agents' own words (a title, a goal, a run's last action, a signal's
// text, the activity feed, a part's name). Those may say "$5" or "budget" and are no label of the app.
// A narrower scope, to the cells that hold numbers, would have missed 0.2.16's own "$1.20 of $10.00
// est." in a plain span of a project card, and would miss the next amount put in an unmarked element.
// So the check is wide by default and narrowed only where the app says the text is not its own: a
// free text that is not marked fails the check, loudly, and never lets an amount through. On a
// release before the marks (0.2.16), everything is read, and its amounts fail it.
//
// The one place the words budget and cost may stand is a label the app dates "(before 0.53)", for a
// record from then that keeps its name: a run that stopped at its budget, or an old signal of a ticket
// over its budget. Only the word, with the date after it on its line and no parenthesis between, is
// left out; an amount in such a label still fails (review of 0.53).

// words says the screen's text is the app's own, apart from what it marks data-free-text, so the words
// budget, cost and estimate are read on it too; a screen whose page holds a plan's, a ticket's, or a
// run's own words unmarked is read for amounts alone (round 2 of the review of 0.53).
export type Screen = { path: string; heading: RegExp; ready?: string; words?: false }

// The screens of the machine, and of each project the overview lists. ready is what the screen shows
// once its data arrived, in 0.2.16 and since; a screen with none is read after the network is idle.
export const machineScreens: Screen[] = [
  // On a machine where Setup is not done, the front page is Setup (12.6).
  { path: '/', heading: /All projects|Setup/, ready: 'section[aria-label="Lead slots"]' },
  { path: '/runs', heading: /Runs/ },
  { path: '/models', heading: /Models/, ready: 'h2:has-text("Results by CLI, model, and effort")' },
  { path: '/crew', heading: /Crew level/, ready: 'h2:has-text("Latest signals")' },
  // Versions shows the gates and the notes of releases written before 0.53, with amounts a server then
  // wrote; since the operator's choice of report 9 the server reads them without (release.Unpriced),
  // and the notes' commit subjects, a release's title, and what the updater or a command said are
  // marked as text from outside the app.
  { path: '/versions', heading: /Versions/, ready: '[data-running]' },
  { path: '/storage', heading: /Runtimes & Storage/ },
]

export const projectScreens = (project: string): Screen[] => [
  { path: `/p/${encodeURIComponent(project)}/factory`, heading: /Factory Line/ },
  { path: `/p/${encodeURIComponent(project)}/specs`, heading: /Specs & WORKFLOW/ },
  { path: `/p/${encodeURIComponent(project)}/chat`, heading: /Project Chat/ },
  { path: `/p/${encodeURIComponent(project)}/skills`, heading: /Skills Registry/ },
  { path: `/p/${encodeURIComponent(project)}/analytics`, heading: /Analytics/, ready: '[data-stat="done"]' },
  { path: `/p/${encodeURIComponent(project)}/summaries`, heading: /Summaries|Not here yet/ },
  { path: `/p/${encodeURIComponent(project)}/tickets`, heading: /Tickets/ },
]

// firstProjectScreens are the screens of one project that showed a cost on its runs and its tickets:
// its plan, Agents, Trajectories, and its newest ticket. Their headings are their own words, and they
// still show words that are not the app's unmarked: the plan's on its screen, and on a run and a ticket
// the stop detail and the warnings, which Crew writes around what the CLI said. So they are read for
// amounts alone. The run's and the ticket's own words (a turn, the trajectory, the prompt, a commit's
// subject, a diff, a summary, a record) are marked since the gate of 0.2.17 failed on a crew-gate run
// whose agent wrote "the project cost $3.54". What Crew itself writes into them (a run's title, its
// prompt parts, a status event) is marked with the agents' words, and is kept free of amounts by the
// baseline package's tests of Crew's prompt files and of every string in its Go code.
const firstProjectScreens = (project: string, ticket?: string): Screen[] => [
  { path: `/p/${encodeURIComponent(project)}/plan`, heading: /./, words: false },
  { path: `/p/${encodeURIComponent(project)}/agents`, heading: /./, words: false },
  { path: `/p/${encodeURIComponent(project)}/trajectories`, heading: /./, words: false },
  ...(ticket ? [{ path: `/p/${encodeURIComponent(project)}/tickets/${encodeURIComponent(ticket)}`, heading: /./, words: false as const }] : []),
]

// getJSON reads an answer of the API, asking again while the ingress answers 429.
async function getJSON<T>(page: Page, path: string): Promise<T> {
  for (let i = 0; ; i++) {
    const r = await page.request.get(path)
    // The ingress answers 429 to a client that asks too often; the gate has no ingress.
    if (r.status() === 429 && i < 10) {
      await page.waitForTimeout(300)
      continue
    }
    expect(r.status(), path).toBe(200)
    return (await r.json()) as T
  }
}

// screensToCheck reads the projects from GET /api/overview, as the front page lists them: a machine
// with no project has no project screen to read.
export async function screensToCheck(page: Page): Promise<Screen[]> {
  const { projects } = await getJSON<{ projects: { name: string }[] }>(page, '/api/overview')
  const out = [...machineScreens, ...projects.flatMap((p) => projectScreens(p.name))]
  if (projects.length > 0) {
    const first = projects[0].name
    const tickets = await getJSON<{ tickets?: { key: string }[] }>(page, `/api/projects/${encodeURIComponent(first)}/tickets`)
    out.push(...firstProjectScreens(first, tickets.tickets?.[0]?.key))
  }
  const runs = await getJSON<{ finished?: { id: string }[] }>(page, '/api/runs')
  const run = runs.finished?.[0]?.id
  if (run) out.push({ path: `/runs/${encodeURIComponent(run)}`, heading: /./, words: false })
  return out
}

// amounts are read on every screen; words only on a screen whose text is the app's own.
const amounts: [string, RegExp][] = [
  ['a dollar amount', /\$\s*\d/g],
  ['a euro amount', /€\s*\d|\d\s*€/g],
  ['dollars', /\bUSD\b|\bEUR\b|\bdollars?\b/gi],
]
const words: [string, RegExp][] = [['an estimate', /\best\.|\bestimat(?:e|ed|es|ion)\b/gi]]
// The words the app may say only in a label dated before 0.53, with the date right after them, a few
// characters on and no parenthesis or line between: "Ticket passed its budget (before 0.53)".
const dateable: [string, RegExp][] = [
  ['the word budget', /\bbudgets?\b/gi],
  ['the word cost', /\bcosts?\b/gi],
]
const datedAfter = /^[^()\n]{0,12}\(before 0\.53\)/

// ownText is the page's text, less the elements marked as the operator's or agents' own words.
async function ownText(page: Page): Promise<string> {
  return page.evaluate(() => {
    const out: string[] = []
    const walk = document.createTreeWalker(document.body, NodeFilter.SHOW_TEXT)
    for (let n = walk.nextNode(); n; n = walk.nextNode()) {
      const el = n.parentElement
      if (!el || el.closest('[data-free-text], script, style, noscript, template')) continue
      const t = n.textContent ?? ''
      if (t.trim()) out.push(t)
    }
    // A title or a label says as much as text does, and is read the same way.
    for (const el of Array.from(document.body.querySelectorAll('[title], [aria-label], [placeholder], [alt]'))) {
      if (el.closest('[data-free-text]')) continue
      for (const a of ['title', 'aria-label', 'placeholder', 'alt']) {
        const v = el.getAttribute(a)
        if (v && v.trim()) out.push(v)
      }
    }
    return out.join('\n')
  })
}

function around(text: string, at: number, length: number): string {
  return text.slice(Math.max(0, at - 40), at + length + 40).replace(/\s+/g, ' ')
}

// findings are what the text says that the app may not, each with the words around it: amounts always,
// and the words too when withWords.
export function findings(text: string, withWords = true): string[] {
  const found: string[] = []
  for (const [what, re] of withWords ? [...amounts, ...words] : amounts) {
    for (const m of text.matchAll(re)) found.push(`${what}: …${around(text, m.index ?? 0, m[0].length)}…`)
  }
  if (!withWords) return found
  for (const [what, re] of dateable) {
    for (const m of text.matchAll(re)) {
      const at = m.index ?? 0
      if (datedAfter.test(text.slice(at))) continue
      found.push(`${what}: …${around(text, at, m[0].length)}…`)
    }
  }
  return found
}

// noDollars opens each screen, waits for its heading, its data (or, on the front page only, Setup),
// and an idle network, and reads it. A screen that could not load its data says so, since a screen
// that shows nothing shows no dollar either. Every screen is read before the check fails, so one
// failure names them all.
export async function noDollars(page: Page, screens: Screen[]): Promise<void> {
  const found: string[] = []
  for (const s of screens) {
    await page.goto(s.path)
    await expect.soft(page.getByRole('heading', { level: 1 }).first(), s.path).toHaveText(s.heading)
    // Where Setup is not done, the front page shows "All projects" and then, once /api/clis answered,
    // sends the browser to /setup: the gate's check server, which holds no key file, is such a machine
    // (the gates of 0.2.17 and 0.2.18 failed there, waiting for lead slots). So the front page is ready
    // when its data or Setup shows, whichever comes; every other screen waits for its own data.
    if (s.ready) {
      const data = page.locator(s.ready)
      const ready = s.path === '/' ? data.or(page.getByRole('heading', { level: 1, name: 'Setup', exact: true })) : data
      await expect.soft(ready.first(), `${s.path}: its data`).toBeVisible({ timeout: 15000 })
    }
    await page.waitForLoadState('networkidle')
    await expect.soft(page.locator('[data-failed]'), `${s.path}: an error instead of its data`).toHaveCount(0)
    for (const f of findings(await ownText(page), s.words !== false)) found.push(`${s.path}: ${f}`)
  }
  expect(found, `the screens show a cost, an estimate, or a budget:\n${found.join('\n')}`).toEqual([])
}
