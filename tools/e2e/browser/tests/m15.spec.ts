import { expect, test } from '@playwright/test'
import { admin, adminRole, cookie, token } from './session'
import { api, project, shot } from './helpers'

type Kind = { kind: string; label: string; all: number }
type Crew = {
  version: string
  done: number
  projects: number
  interventions: number
  interventionsPer100: number
  projectKeys: { key: string; name: string }[]
  kinds: Kind[]
  latest: unknown[]
  versions: { version: string; running: boolean }[] | null
  firstRequest: { n: number; of: number; share: number }
  comparison: { key: string; name: string }[]
}
type Improvements = { improvements: { number: number; title: string; status: string }[] }
type Suite = { id: number; state: string; unmeasured?: string | null; result?: { baseline?: { cases: number } } | null }
type Evals = { cases: { number: number; status: string }[]; suites: Suite[] }
// nothing is a suite that ended having measured no case, read from its result.
const nothing = (s: Suite) => ['done', 'stopped'].includes(s.state) && (s.result?.baseline?.cases ?? 0) === 0

// The screens of M15 (design 9.6, 9.7, 10.3 to 10.7; crew.png, improvements.png, crew--baseline.png,
// analytics.png, knowledge--evals.png): the Crew level with its tabs, an improvement, a project's
// Analytics, its eval cases, and its own baseline in Settings. They read: the only button pressed is a
// tab.
test.describe('the screens of M15', () => {
  test.use({ viewport: { width: 1280, height: 900 } })
  test.beforeEach(async ({ context }) => {
    await context.addCookies([cookie(token({ email: admin, roles: [adminRole] }))])
  })

  test('the Crew level shows how often you stepped in, the signals by kind and project, and the latest', async ({ page }) => {
    const c = (await api(page, '/api/crew/analytics')) as Crew
    await page.goto('/crew')
    await expect(page.getByRole('heading', { name: 'Crew level', exact: true })).toBeVisible()
    await expect(page.getByText('Signals go up from the projects')).toBeVisible()
    await expect(page.locator('[data-stat="interventions"]')).toContainText(c.interventionsPer100.toFixed(1))
    await expect(page.locator('[data-stat="version"]')).toContainText(c.version)
    // A share over no ticket is "—", not 0% (0.2.3 on dave: "0%, on Crew 0.2.3, 0 tickets").
    const first = page.locator('[data-stat="first-request"]')
    if (c.firstRequest.of === 0) {
      await expect(first).not.toContainText('%')
      await expect(first).toContainText('—')
      await expect(first).toContainText(`no ticket has run on Crew ${c.version} yet`)
    } else {
      await expect(first).toContainText(`${Math.round(c.firstRequest.share * 100)}%`)
      await expect(first).toContainText(`on Crew ${c.version}, ${c.firstRequest.of} ${c.firstRequest.of === 1 ? 'ticket' : 'tickets'}`)
    }
    await expect(page.locator('[data-stat="finished"]')).toContainText(String(c.done))
    await expect(page.getByRole('tab', { name: 'Signals' })).toHaveAttribute('aria-selected', 'true')
    for (const k of c.kinds) {
      await expect(page.locator(`[data-kind="${k.kind}"]`)).toBeVisible()
    }
    for (const p of c.projectKeys) {
      await expect(page.locator('[data-matrix] th', { hasText: p.key }).first()).toBeVisible()
    }
    // Every row adds up to All: a plan's choice of a rewrite has no project, and 0.2.4 on dave counted 3
    // under All and 0 in every column.
    for (const row of await page.locator('[data-matrix] tbody tr').all()) {
      const cells = (await row.locator('td').allInnerTexts()).slice(1).map(Number)
      const all = cells.pop()
      expect.soft(cells.reduce((a, b) => a + b, 0), await row.getAttribute('data-kind')).toBe(all)
    }
    await expect(page.getByRole('heading', { name: 'Latest signals' })).toBeVisible()
    // A sandbox's start-up is a measurement, shown in Analytics: 0.2.4 on dave listed 21 among 30.
    await expect(page.locator('[data-latest] tbody tr', { hasText: 'Sandbox start-up' })).toHaveCount(0)
    // A ticket's tokens and time is a measurement too, which its card shows, and every other line says
    // what its signal holds: 0.2.5 on dave repeated the signal's name in 23 of its 30 lines.
    for (const row of await page.locator('[data-latest] tbody tr').all()) {
      const [, , signal, what] = await row.locator('td').allInnerTexts()
      expect.soft(what, signal).not.toBe(signal)
    }
    await expect(page.locator('[data-latest] tbody tr', { hasText: 'Tokens and time of a ticket' })).toHaveCount(0)
    // Use in place of cost (design 0.53): tokens and time per ticket, by stage, and by model.
    for (const t of ['Use per ticket', 'Tokens by stage', 'Tokens by model and effort']) {
      await expect(page.getByText(t, { exact: true })).toBeVisible()
    }
    await expect(page.locator('[data-comparison] th', { hasText: 'Tokens per ticket' })).toBeVisible()
    // A fix round says what failed (0.2.6 on dave: "merge"), and a long line is cut to 200 characters with
    // the whole text on hover (0.2.6: the improver's note filled ten lines of the table).
    for (const row of await page.locator('[data-latest] tbody tr').all()) {
      const [, , signal, what] = await row.locator('td').allInnerTexts()
      expect.soft([...what].length, `${signal}: ${what.slice(0, 60)}`).toBeLessThanOrEqual(201)
      if (signal === 'Fix round') expect.soft(what, signal).toMatch(/\s/)
      const title = await row.locator('td').nth(3).getAttribute('title')
      if (title !== null) {
        const one = (x: string) => x.replace(/\s+/g, ' ').trim()
        const shown = what.endsWith('…') && one(title).startsWith(one(what.slice(0, -1))) && [...title].length > [...what].length
        expect.soft(shown, `${signal}: the whole text on hover`).toBe(true)
      }
    }
    const long = (c.latest as { text: string }[]).map((l) => l.text).filter((t) => [...t].length > 200)
    const titles = await page.locator('[data-latest] tbody td[title]').evaluateAll((tds) => tds.map((td) => td.getAttribute('title')))
    for (const t of long) expect.soft(titles, `on hover: ${t.slice(0, 60)}`).toContain(t)
    await expect(page.getByRole('heading', { name: 'The projects side by side' })).toBeVisible()
    await expect(page.locator('[data-comparison] tbody tr')).toHaveCount(c.comparison.length)
    await expect(page.getByRole('heading', { name: 'Prompts' })).toBeVisible()
    // The menu marks the Crew level, and not All projects (0.2.0 and 0.2.1 marked All projects on every page
    // opened by its address: the one shell is rendered at "/").
    const nav = page.getByRole('navigation')
    await expect(nav.getByRole('link', { name: 'Crew level' })).toHaveAttribute('data-status', 'active')
    await expect(nav.getByRole('link', { name: 'All projects' })).not.toHaveAttribute('data-status', 'active')
    await shot(page, 'crew')
  })

  test('the Improvements tab lists what the improver proposed, with Run the improver now', async ({ page }) => {
    const { improvements } = (await api(page, '/api/crew/improvements')) as Improvements
    await page.goto('/crew?tab=improvements')
    await expect(page.getByRole('tab', { name: 'Improvements' })).toHaveAttribute('aria-selected', 'true')
    await expect(page.getByText('The improver reads the signals of every project')).toBeVisible()
    await expect(page.getByRole('button', { name: 'Run the improver now' })).toBeVisible()
    await expect(page.locator('[data-improvement]')).toHaveCount(improvements.length)
    if (improvements.length === 0) await expect(page.getByText('No improvement yet.')).toBeVisible()
    await shot(page, 'improvements')
    const first = improvements[0]
    if (first) {
      await page.getByRole('link', { name: first.title }).click()
      await expect(page.getByRole('heading', { name: first.title })).toBeVisible()
      await expect(page.getByText('Steps from proposal to a running version')).toBeVisible()
      await expect(page.locator('[data-diff]').first()).toBeVisible()
      await shot(page, 'improvement')
    }
  })

  test('the Baseline tab shows the results by version, the versions, and what the baseline holds', async ({ page }) => {
    const c = (await api(page, '/api/crew/analytics')) as Crew
    await page.goto('/crew?tab=baseline')
    await expect(page.getByText('Results by Crew version, all projects')).toBeVisible()
    for (const v of c.versions ?? []) {
      await expect(page.locator(`[data-version="${v.version}"]`)).toBeVisible()
    }
    await expect(page.getByText('What the baseline holds')).toBeVisible()
    await expect(page.locator('[data-tree]')).toContainText('stages/')
    await expect(page.locator('[data-tree]')).toContainText('improver.md')
    // The versions card and the table's last column stay within the page's width (0.2.0 pushed them
    // off its side). How far down they are depends on how many versions there are.
    const width = page.viewportSize()!.width
    for (const l of [page.getByRole('link', { name: 'Versions, update, and roll back' }), page.locator('[data-versions] th', { hasText: 'Tokens per ticket' })]) {
      const b = (await l.boundingBox())!
      expect(b.x >= 0 && b.x + b.width <= width, `${await l.textContent()} at x ${b.x}, width ${b.width}`).toBe(true)
    }
    await shot(page, 'crew--baseline')
  })

  test("a project's Analytics shows flow, quality, and use", async ({ page }) => {
    const p = await project(page)
    const a = (await api(page, `/api/projects/${p}/analytics`)) as { done: number; tickets: number; evalsUnmeasured: number }
    await page.goto(`/p/${p}/analytics`)
    await expect(page.getByRole('heading', { name: 'Analytics' })).toBeVisible()
    await expect(page.locator('[data-stat="done"]')).toContainText(String(a.done))
    await expect(page.locator('[data-stat="done"]')).toContainText(`of ${a.tickets}`)
    for (const t of [
      'Tickets done per day',
      'Minutes in each stage, average',
      'Tokens by agent',
      'Tokens by model and effort',
      'Tokens by part',
      'Tickets agents created',
      'Documentation that is current',
      'Bugs',
    ]) {
      await expect(page.getByText(t, { exact: true })).toBeVisible()
    }
    // Use in place of cost (design 0.53): tokens, and no amount, estimate, or budget.
    await expect(page.locator('[data-stat="use"]')).toContainText('tokens')
    await expect(page.getByText('Added to the plan', { exact: true })).toBeVisible()
    await expect(page.getByText('Over budget', { exact: true })).toHaveCount(0)
    await expect(page.getByRole('navigation').getByRole('link', { name: 'Analytics' })).toBeVisible()
    if (a.evalsUnmeasured > 0) await expect(page.locator('[data-unmeasured]')).toContainText('measured nothing')
    await shot(page, 'analytics')
  })

  test("the Evals tab lists the project's cases and its suites", async ({ page }) => {
    const p = await project(page)
    const ev = (await api(page, `/api/projects/${p}/evals`)) as Evals
    await page.goto(`/p/${p}/knowledge?tab=evals`)
    await expect(page.getByRole('tab', { name: /Evals/ })).toHaveAttribute('aria-selected', 'true')
    await expect(page.getByText("Cases come from this project's own finished tickets.")).toBeVisible()
    await expect(page.getByRole('button', { name: 'Run the eval suite' })).toBeVisible()
    await expect(page.locator('[data-case]')).toHaveCount(ev.cases.length)
    if (ev.suites.length === 0) await expect(page.getByText('No eval run yet.')).toBeVisible()
    // A suite that measured no case says so with its reason, not 0% (M15 on dave).
    for (const s of ev.suites.filter(nothing)) {
      await expect(page.locator(`[data-suite-id="${s.id}"] [data-goals]`)).toHaveText('nothing measured')
      expect(s.unmeasured, `suite ${s.id}: why it measured nothing`).toBeTruthy()
      await expect(page.locator(`[data-suite-id="${s.id}"]`)).toContainText(s.unmeasured!)
    }
    await shot(page, 'knowledge--evals')
  })

  test("Settings shows the project's own baseline", async ({ page }) => {
    const p = await project(page)
    await page.goto(`/p/${p}/settings`)
    await expect(page.getByRole('heading', { name: "This project's own baseline" })).toBeVisible()
    await expect(page.getByRole('button', { name: "Write this project's own version of a file" })).toBeVisible()
  })
})
