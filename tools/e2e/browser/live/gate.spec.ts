import { expect, test, type Page } from '@playwright/test'
import { admin, adminRole, cookie, token } from '../tests/session'

// The panel "The gate of V" on Versions while the gate's live part runs (16.2 step 6), live: it needs
// a gate whose real ticket runs on crew-gate, which costs the operator's subscription, so it runs only
// when the operator approved it. CREW_GATE names the version. It reads, and presses nothing.
const version = process.env.CREW_GATE ?? ''
const shots = process.env.CREW_SHOTS ?? ''
const shot = async (page: Page, name: string) => {
  if (!shots) return
  const h = await page.evaluate(() => document.querySelector('main')?.scrollHeight ?? 900)
  await page.setViewportSize({ width: 1280, height: Math.min(Math.max(h + 52, 900), 8000) })
  await page.screenshot({ path: `${shots}/${name}.png`, fullPage: true })
  await page.setViewportSize({ width: 1280, height: 900 })
}

test('the gate of a release while its live part runs', async ({ page, context }) => {
  expect(version, 'CREW_GATE names the gate').toMatch(/^\d+\.\d+\.\d+$/)
  await context.addCookies([cookie(token({ email: admin, roles: [adminRole] }))])
  await page.goto('/versions')
  const panel = page.locator(`[data-gate="${version}"]`)
  await expect(panel).toBeVisible()
  await expect(panel.locator('[data-gate-state]')).toHaveText(
    'The live steps run on this server: the real ticket on crew-gate, then the eval suites when the release changes what agents see',
  )
  // The updater's steps passed, each with its mark, and none failed.
  const steps = panel.locator('[data-gate-lines="steps"]')
  await expect(steps).toContainText('Try the state')
  await expect(steps).toContainText('Check')
  await expect(steps).not.toContainText('×')
  // The live part, as text: the real ticket's key on crew-gate and its stage.
  await expect(panel.locator('[data-gate-lines="live"]')).toContainText(/The real ticket GATE-\d+ on crew-gate/)
  await shot(page, 'live-gate')
})
