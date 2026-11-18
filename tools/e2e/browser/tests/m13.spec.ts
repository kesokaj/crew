import { expect, test } from '@playwright/test'
import { admin, adminRole, cookie, token } from './session'
import { api, project, shot } from './helpers'

type Room = {
  cores: number
  room: { cpus: number; memMB: number }
  used: { cpus: number; memMB: number; large: number; small: number }
  waiting: unknown[]
}

type Sandboxes = { running: unknown[]; waiting: unknown[]; finished: { result: string }[] }

// The screens of M13 (design 7.9, 13.10, 20.1): the front page with the sandboxes on the machine and
// the tokens each project's runs used (since 0.53 no spend and no budget), a project's Agents screen,
// its board, and its activity.
// They read and start nothing: no run, no microVM, no decision.
test.describe('the screens of M13', () => {
  test.use({ viewport: { width: 1280, height: 900 } })
  test.beforeEach(async ({ context }) => {
    await context.addCookies([cookie(token({ email: admin, roles: [adminRole] }))])
  })

  test('the front page shows the lead slots, the sandboxes on the machine, and each project with its tokens and repository', async ({ page }) => {
    const { room, projects } = (await api(page, '/api/overview')) as {
      room: Room
      projects: { name: string; tokens: number; github: boolean; network: string; budgetUsd?: number; spentUsd?: number }[]
    }
    expect(room.cores).toBeGreaterThan(0)
    expect(room.room.cpus).toBeGreaterThan(0)
    await page.goto('/')
    await expect(page.getByRole('heading', { name: 'All projects', exact: true })).toBeVisible()
    const machine = page.getByRole('region', { name: 'Sandboxes on the machine' })
    await expect(machine).toBeVisible()
    await expect(machine.getByText(`of ${room.room.cpus} virtual CPUs · ${room.cores} cores`)).toBeVisible()
    await expect(machine.getByText(`of ${Math.round(room.room.memMB / 1024)} GB given out`)).toBeVisible()
    await expect(machine.getByText('Waiting for room')).toBeVisible()
    await expect(page.getByRole('region', { name: 'Lead slots' })).toBeVisible()
    await expect(page.getByRole('region', { name: 'Waiting for a lead slot' })).toBeVisible()
    for (const p of projects) {
      const card = page.locator('a', { hasText: `${p.name} · ` }).first()
      await expect(card.getByText(p.github ? 'GitHub repository' : 'local repository')).toBeVisible()
      // Design 0.53: tokens where the spend and its budget were, and no amount.
      expect([p.budgetUsd, p.spentUsd], `${p.name}: no budget and no spend in the API`).toEqual([undefined, undefined])
      await expect(card.locator('[data-tokens]')).toHaveText(p.tokens === 1 ? '1 token' : /^\d+(\.\d)?[kMB]? tokens$/)
      await expect(card).not.toContainText('$')
      await expect(card).not.toContainText(/ est\./)
    }
    await shot(page, 'overview')
    // The top of the page, at the window's size, where the panels are.
    if (process.env.CREW_SHOTS) await page.screenshot({ path: `${process.env.CREW_SHOTS}/overview--top.png` })
  })

  test("a project's Agents screen lists what runs, what waits, and what ended", async ({ page }) => {
    const p = await project(page)
    const sb = (await api(page, `/api/projects/${p}/sandboxes`)) as Sandboxes
    await page.goto(`/p/${p}/agents`)
    await expect(page.getByRole('heading', { name: 'Agents', exact: true })).toBeVisible()
    await expect(page.getByRole('heading', { name: /Running now/ })).toBeVisible()
    await expect(page.getByRole('heading', { name: 'Waiting' })).toBeVisible()
    await expect(page.getByRole('heading', { name: 'Recently finished' })).toBeVisible()
    if (sb.running.length === 0) await expect(page.getByText(`No agent works in ${p} now.`)).toBeVisible()
    else await expect(page.getByRole('table', { name: 'Running now' }).locator('tbody tr')).toHaveCount(sb.running.length)
    if (sb.waiting.length === 0) await expect(page.getByText('Nothing waits.')).toBeVisible()
    else await expect(page.getByRole('list', { name: 'Waiting' }).locator('li')).toHaveCount(sb.waiting.length)
    const finished = page.getByRole('table', { name: 'Recently finished' })
    await expect(finished.locator('tbody tr')).toHaveCount(sb.finished.length)
    if (sb.finished.length > 0) await expect(finished.getByText(sb.finished[0].result).first()).toBeVisible()
    // It is in the project's menu, after Activity.
    const menu = await page.locator(`a[href^="/p/${p}/"]`).allTextContents()
    expect(menu.indexOf('Agents')).toBe(menu.indexOf('Activity') + 1)
    await shot(page, 'agents')
  })

  test("a project's board and activity", async ({ page }) => {
    const p = await project(page)
    await page.goto(`/p/${p}/board`)
    await expect(page.getByRole('heading', { name: 'Board' })).toBeVisible()
    await expect(page.locator('[data-column="done"]')).toBeAttached()
    await shot(page, 'board')
    await page.goto(`/p/${p}/activity`)
    await expect(page.locator('tbody tr').first()).toBeVisible()
    await shot(page, 'activity')
  })
})
