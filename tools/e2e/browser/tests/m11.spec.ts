import { expect, test } from '@playwright/test'
import { admin, adminRole, cookie, token } from './session'
import { api, project, shot } from './helpers'

// The screens of M11 (design 7.7, 7.9, 7.13, 12.7, 16.10, 20.1). The one that changes something,
// Pause all, puts it back; nothing here starts a run: no amendment is sent, nothing is reverted or
// deleted.
test.describe('the screens of M11', () => {
  test.use({ viewport: { width: 1280, height: 900 } })
  test.beforeEach(async ({ context }) => {
    await context.addCookies([cookie(token({ email: admin, roles: [adminRole] }))])
  })

  // post writes as the page does: a write without Crew's origin is refused.
  const post = (page: import('@playwright/test').Page, path: string) =>
    page.evaluate(async (p) => (await fetch(p, { method: 'POST', headers: { 'Content-Type': 'application/json' } })).status, path)

  test('Pause all holds Crew, the page and the top bar say so, and Resume lets it go on', async ({ page }) => {
    const before = (await api(page, '/api/crew/hold')) as { state: { pausedAt?: string } }
    expect(before.state.pausedAt, 'Crew is not paused before the test').toBeFalsy()
    await page.goto('/')
    try {
      await page.getByRole('button', { name: 'Pause all' }).click()
      const banner = page.locator('[data-hold]')
      await expect(banner).toContainText('Crew is paused', { timeout: 15000 })
      await expect(banner).toContainText('No new run starts, and running runs finish.')
      await expect(page.locator('[data-held]')).toHaveText('Paused', { timeout: 15000 })
      await expect(page.getByRole('button', { name: 'Resume' })).toBeVisible()
      await shot(page, 'overview-paused')
      // The top of the page, as overview.png shows it: the switches and the banner.
      if (process.env.CREW_SHOTS) await page.screenshot({ path: `${process.env.CREW_SHOTS}/overview-paused-top.png` })
      await page.getByRole('button', { name: 'Resume' }).click()
      await expect(banner).toHaveCount(0, { timeout: 15000 })
      await expect(page.getByRole('button', { name: 'Pause all' })).toBeVisible()
    } finally {
      if (((await api(page, '/api/crew/hold')) as { state: { pausedAt?: string } }).state.pausedAt) {
        expect(await post(page, '/api/crew/resume')).toBe(200)
      }
    }
    expect(((await api(page, '/api/crew/hold')) as { held: string }).held).toBe('')
  })

  test("a project's Tickets screen lists every ticket, and filters them", async ({ page }) => {
    const name = await project(page)
    const { tickets } = (await api(page, `/api/projects/${name}/tickets`)) as { tickets: { key: string; stage: string }[] }
    await page.goto(`/p/${name}/tickets`)
    const table = page.locator('[data-tickets]')
    await expect(table.locator('tbody tr')).toHaveCount(tickets.length, { timeout: 15000 })
    await expect(page.getByText(`${tickets.length} shown`)).toBeVisible()
    await shot(page, 'tickets')
    await page.getByRole('combobox', { name: 'Stage' }).selectOption('done')
    const done = tickets.filter((t) => t.stage === 'done').length
    await expect(table.locator('tbody tr')).toHaveCount(done)
    await expect(table.getByText('✓ Merged').first()).toBeVisible()
  })

  // Design 0.53: nothing is priced. A model has no price, and a run says its tokens and no cost.
  test('the Models screen prices no model, and a run says its tokens and no cost', async ({ page }) => {
    await page.goto('/models')
    await expect(page.getByRole('heading', { name: 'Results by CLI, model, and effort' })).toBeVisible({ timeout: 15000 })
    await expect(page.getByText(/a million tokens/)).toHaveCount(0)
    await shot(page, 'models-results')
    const { finished } = (await api(page, '/api/runs')) as {
      finished: { id: string; tokensIn: number; tokensOut: number; costUSD?: number; costBasis?: string }[]
    }
    for (const r of finished) expect.soft([r.costUSD, r.costBasis], `the run ${r.id} names no cost`).toEqual([undefined, undefined])
    const used = finished.find((r) => r.tokensIn + r.tokensOut > 0)
    expect(used, 'a run that used tokens').toBeTruthy()
    await page.goto(`/runs/${used!.id}`)
    // The first Tokens row says in and out; the trajectory's card, lower down, says their sum.
    const row = page.locator('dt', { hasText: /^Tokens$/ }).first().locator('xpath=following-sibling::dd[1]')
    await expect(row).toHaveText(/^[\d.,\s]+ in, [\d.,\s]+ out/, { timeout: 15000 })
    await expect(page.locator('dt', { hasText: /^Cost$/ })).toHaveCount(0)
    await expect(page.getByText(/Nothing is billed per run/)).toHaveCount(0)
  })

  test('a Done ticket offers Revert, and the Database screen says the disk', async ({ page }) => {
    const name = await project(page)
    const { tickets } = (await api(page, `/api/projects/${name}/tickets`)) as { tickets: { key: string; stage: string }[] }
    const done = tickets.find((t) => t.stage === 'done')!
    await page.goto(`/p/${name}/tickets/${done.key}`)
    await expect(page.getByRole('button', { name: 'Revert' })).toBeVisible({ timeout: 15000 })
    await shot(page, 'task-done-revert')
    await page.goto('/database')
    await expect(page.locator('[data-disk]')).toContainText('Crew cleans at 85% and starts no new run at 95%', { timeout: 15000 })
    await shot(page, 'database-disk')
  })

  test("a running project's Plan can be amended, and its Settings archive or delete it with the name typed", async ({ page }) => {
    const name = await project(page)
    await page.goto(`/p/${name}/plan`)
    await expect(page.getByRole('heading', { name: 'Amend the plan' })).toBeVisible({ timeout: 15000 })
    await expect(page.getByRole('button', { name: 'Add the amendment' })).toBeDisabled()
    await shot(page, 'plan-amend')
    await page.goto(`/p/${name}/settings`)
    const del = page.getByRole('button', { name: `Delete ${name}` })
    await expect(del).toBeDisabled({ timeout: 15000 })
    await page.getByLabel("The project's name, to delete it").fill(name + 'x')
    await expect(del).toBeDisabled()
    await page.getByLabel("The project's name, to delete it").fill(name)
    await expect(del).toBeEnabled()
    await page.getByRole('button', { name: 'Protected: off' }).click()
    await expect(page.getByRole('button', { name: 'Protected: on' })).toBeVisible({ timeout: 15000 })
    await expect(del).toBeDisabled()
    await expect(page.getByRole('button', { name: 'Archive', exact: true })).toBeDisabled()
    await page.getByRole('button', { name: 'Protected: on' }).click()
    await expect(page.getByRole('button', { name: 'Protected: off' })).toBeVisible({ timeout: 15000 })
    await expect(del).toBeEnabled()
    await page.getByLabel("The project's name, to delete it").fill('')
    await shot(page, 'settings-delete')
  })

  test("a project's Board and Settings show its Project Sandbox (agy acp) lifecycle, snapshots, and custom image controls", async ({ page }) => {
    const name = await project(page)
    await page.goto(`/p/${name}/board`)
    const bar = page.locator('[data-project-sandbox]')
    await expect(bar).toContainText('Project Sandbox (agy acp):', { timeout: 15000 })
    await expect(bar.getByRole('button', { name: 'Snapshot' })).toBeVisible()

    await page.goto(`/p/${name}/settings`)
    const card = page.locator('[data-project-sandbox-card]')
    await expect(card).toContainText('Home Snapshots', { timeout: 15000 })
    await expect(card.getByRole('button', { name: 'Snapshot', exact: true })).toBeVisible()
    await expect(card.getByRole('button', { name: 'Build Project Image' })).toBeVisible()
  })
})
