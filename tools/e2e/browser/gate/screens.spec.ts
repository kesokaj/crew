import { expect, test } from '@playwright/test'
import { copyLines, type Copies, waiting } from './copies'
import { gateLines, type Request, shownRequests } from './lastchanges'
import { noDollars, screensToCheck } from './nodollars'

// Each screen of the release opens on the copy of the stored state, says no error, and every call
// it makes to the API answers. The gate reads only: a test that writes is not here.
const screens: [string, RegExp][] = [
  // On a machine where Setup is not done, the first screen is Setup (12.6).
  ['/', /All projects|Projects|Setup/],
  ['/inbox', /Inbox/],
  ['/versions', /Versions/],
  ['/database', /Database/],
  ['/models', /Models/],
  ['/clis', /Agent CLIs/],
  ['/crew-mcp', /Crew MCP/],
  ['/runs', /Runs/],
  ['/plans', /Plans/],
  ['/crew', /Crew level/],
]

for (const [path, heading] of screens) {
  test(`the screen ${path} opens on the release`, async ({ page }) => {
    const failed: string[] = []
    page.on('response', (r) => {
      if (r.url().includes('/api/') && r.status() >= 500) failed.push(`${r.status()} ${r.url()}`)
    })
    // React's #418, a mismatch of the first render with the page the server sent, is on every screen
    // since M12 and is carried to M17; every other page error fails the gate.
    page.on('pageerror', (e) => {
      if (!e.message.includes('Minified React error #418')) failed.push(`page error: ${e.message}`)
    })
    await page.goto(path)
    await expect(page.getByRole('heading', { level: 1 }).first()).toHaveText(heading)
    await page.waitForLoadState('networkidle')
    expect(failed, failed.join('\n')).toEqual([])
  })
}

// Design 0.53: no screen shows a cost, an estimate, or a budget; where a cost was, tokens are.
test('no screen shows a dollar, an estimate, or a budget', async ({ page }) => {
  const screens = await screensToCheck(page)
  test.setTimeout(30_000 + 20_000 * screens.length)
  await noDollars(page, screens)
})

test("the Updater card words a gate's request as a gate, never as an install", async ({ page, request }) => {
  const r = await request.get('/api/crew/versions')
  expect(r.status()).toBe(200)
  const { requests } = (await r.json()) as { requests: Request[] }
  test.skip(!shownRequests(requests).some((q) => q.action === 'gate' || q.action === 'check'), 'no gate or check among the last changes')
  await page.goto('/versions')
  await gateLines(page, requests)
})

test('Versions never says a release is being copied to GitHub while copying is off', async ({ page, request }) => {
  const r = await request.get('/api/crew/versions')
  expect(r.status()).toBe(200)
  const v = (await r.json()) as Copies
  test.skip(v.publish === true, 'releases are copied to GitHub here')
  test.skip(waiting(v).length === 0, 'no release that passed a gate waits for a copy')
  await page.goto('/versions')
  await copyLines(page, v)
})

// The daily look (12.8, since 0.56): Versions shows the newest look of the copied state, and a check
// server, which never looks, says so and offers no Look now. It reads only.
test('Versions shows the daily look, which a check server does not run', async ({ page, request }) => {
  const r = await request.get('/api/crew/pins')
  expect(r.status()).toBe(200)
  const pins = (await r.json()) as { look: { at: string } | null; looks: boolean }
  expect(pins.looks).toBe(false)
  await page.goto('/versions')
  const card = page.locator('[data-daily-look]')
  await expect(card.getByRole('heading', { name: 'The daily look' })).toBeVisible()
  await expect(card.getByText(pins.look ? /^The last look was/ : 'No look yet.')).toBeVisible()
  await expect(card.getByText('The daily look does not run on this server.')).toBeVisible()
  await expect(card.getByRole('button', { name: /Look now/ })).toHaveCount(0)
})

// Every release is measured against the one it replaced (10.7, since 0.57): each release that was
// installed has its measure line on Versions, in Crew's words. A check server measures nothing, so the
// line is what the copied state holds, or the tickets the release waits for. It reads only.
test("Versions shows an installed release's measure", async ({ page, request }) => {
  const r = await request.get('/api/crew/versions')
  expect(r.status()).toBe(200)
  const v = (await r.json()) as { releases: { version: string; installedAt?: string; state: string }[] }
  const installed = v.releases.find((x) => x.installedAt)
  test.skip(!installed, 'no release was installed here')
  await page.goto('/versions')
  const line = page.locator(`[data-release="${installed!.version}"] [data-measure]`)
  await expect(line).toHaveText(/^(measured once \d+ tickets ran on it|worse than |better than |the same as |too few tickets against |not compared: )/)
})

test('the release says its own version', async ({ request }) => {
  const r = await request.get('/api/whoami')
  expect(r.status()).toBe(200)
  expect((await r.json()).version).toBe(process.env.CREW_VERSION)
})
