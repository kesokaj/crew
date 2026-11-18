import { expect, test } from '@playwright/test'
import { admin, adminRole, cookie, token } from './session'
import { api, shot } from './helpers'

// The screens of M10 (design 13.6, 7.4, 7.12, 20.1). The network test switches the fixture project's
// mode and puts back what it had, so it leaves the machine as it found it.
test.describe('the screens of M10', () => {
  test.use({ viewport: { width: 1280, height: 900 } })
  test.beforeEach(async ({ context }) => {
    await context.addCookies([cookie(token({ email: admin, roles: [adminRole] }))])
  })

  test("a project's Settings switches its network between Internet, Model only, and LAN", async ({ page }) => {
    const base = '/api/projects/fixture/network'
    const before = (await api(page, base)) as { own: string; machine: string; mode: string }
    try {
      await page.goto('/p/fixture/settings')
      const card = page.locator('section').filter({ has: page.getByRole('heading', { name: "Network for this project's sandboxes" }) })
      const group = page.getByRole('group', { name: 'Network mode' })
      await expect(group).toBeVisible()
      for (const name of ['Internet', 'Model only', 'LAN']) await expect(group.getByRole('button', { name, exact: true })).toBeVisible()

      await group.getByRole('button', { name: 'Model only', exact: true }).click()
      await expect(group.getByRole('button', { name: 'Model only', exact: true })).toHaveAttribute('aria-pressed', 'true')
      await expect(card.getByText(/Model only\. Reaches the agent's model service and Crew, and nothing else/)).toBeVisible()
      await expect(card.getByText(/Set for this project/)).toBeVisible()
      expect(((await api(page, base)) as { own: string }).own).toBe('model_only')
      await shot(page, 'settings-network')

      await card.getByRole('button', { name: "Use the machine's" }).click()
      await expect(card.getByText(/The machine's mode, as this project sets none/)).toBeVisible()
      await expect(group.getByRole('button', { name: 'Internet', exact: true })).toHaveAttribute('aria-pressed', String(before.machine === 'internet'))
    } finally {
      // From the page, as the UI sends it: a write with no origin of Crew's is refused.
      if (!page.url().includes('/p/')) await page.goto('/p/fixture/settings')
      const status = await page.evaluate(
        async ([path, mode]) =>
          (await fetch(path, { method: 'PUT', body: JSON.stringify({ mode }), headers: { 'Content-Type': 'application/json' } })).status,
        [base, before.own || 'inherit'],
      )
      expect(status).toBe(200)
    }
    expect(((await api(page, base)) as { own: string }).own).toBe(before.own)
  })

  // send writes as the page does: a write without Crew's origin is refused.
  const send = (page: import('@playwright/test').Page, method: string, path: string, body?: unknown) =>
    page.evaluate(
      async ([m, p, b]) => {
        const r = await fetch(p as string, { method: m as string, body: b === undefined ? undefined : JSON.stringify(b), headers: { 'Content-Type': 'application/json' } })
        return r.status
      },
      [method, path, body],
    )

  test("a project's Settings switches E2E and Verification on, and the board gets their columns", async ({ page }) => {
    const before = (await api(page, '/api/projects/fixture/stages')) as { stages: { id: string; on: boolean }[] }
    const was = Object.fromEntries(before.stages.map((s) => [s.id, s.on]))
    try {
      await page.goto('/p/fixture/settings')
      for (const name of ['E2E', 'Verification']) {
        const sw = page.getByRole('button', { name: new RegExp(`^${name}: (on|off)$`) })
        if ((await sw.getAttribute('aria-pressed')) !== 'true') await sw.click()
        await expect(page.getByRole('button', { name: `${name}: on` })).toHaveAttribute('aria-pressed', 'true')
      }
      await shot(page, 'settings-stages-on')
      await page.goto('/p/fixture/board')
      await expect(page.locator('[data-column="e2e"]')).toBeVisible()
      await expect(page.locator('[data-column="verification"]')).toBeVisible()
      await shot(page, 'board-e2e-verification')
    } finally {
      if (!page.url().includes('/p/')) await page.goto('/p/fixture/settings')
      expect(await send(page, 'PUT', '/api/projects/fixture/stages', { e2e: was.e2e, verification: was.verification })).toBe(200)
    }
  })

  test("a stage of the project's own shows in Settings and on the board by its title", async ({ page }) => {
    await page.goto('/p/fixture/settings')
    const stage = { title: 'Security', role: 'security', agent: '.agents/agents/security.md', prompt: 'Find every place a secret could be logged.',
      wip: 1, exit: ['stage_verdict_pass', 'cmd: make audit', 'judge: No secret is printed to a log.'], on: false, after: 'test' }
    try {
      expect(await send(page, 'PUT', '/api/projects/fixture/stages/security', stage)).toBe(200)
      // An exit nobody answers is refused.
      expect(await send(page, 'PUT', '/api/projects/fixture/stages/audit', { ...stage, exit: ['lint_passes'] })).toBe(400)
      await page.reload()
      const row = page.locator('tr', { has: page.getByText('Security', { exact: true }) })
      await expect(row.getByText(".agents/agents/security.md", { exact: true })).toBeVisible()
      await expect(row.getByText("The reviewer's model, on another family than the builder.")).toBeVisible()
      await shot(page, 'settings-own-stage')
      await page.goto('/p/fixture/board')
      await expect(page.getByRole('list', { name: 'Stages' }).getByText('Security', { exact: true })).toBeVisible()
    } finally {
      if (!page.url().includes('/p/')) await page.goto('/p/fixture/settings')
      expect(await send(page, 'DELETE', '/api/projects/fixture/stages/security')).toBe(200)
    }
    const after = (await api(page, '/api/projects/fixture/stages')) as { stages: { id: string }[] }
    expect(after.stages.map((s) => s.id)).not.toContain('security')
  })

  test('the Git screen has the pull requests and says when main was fetched', async ({ page }) => {
    const r = await page.request.get('/api/projects/wordcount/git')
    test.skip(r.status() !== 200, 'no wordcount project on this machine: seeded by the optional live check')
    const g = (await r.json()) as { usesPulls: boolean; pulls: unknown[]; fetchedAt?: string; fetchDetail?: string }
    await page.goto('/p/wordcount/git')
    await expect(page.getByRole('heading', { name: /Pull requests/ })).toBeVisible()
    if (g.pulls.length === 0) await expect(page.getByText(/None yet: Crew opens one for each ticket|None: Crew opens pull requests when gh is signed in/)).toBeVisible()
    else await expect(page.locator('[data-pull]').first()).toBeVisible()
    // The last fetch: when it was, or why it failed (the remote could not be reached, say).
    if (g.fetchDetail) await expect(page.getByText(`fetching failed: ${g.fetchDetail}`)).toBeVisible()
    else if (g.fetchedAt) await expect(page.getByText(/main fetched .*; what you push there becomes the base/)).toBeVisible()
    await shot(page, 'repo')
  })

  test("a ticket's view shows its judge's answer, its verification, and its pull request", async ({ page }) => {
    const r = await page.request.get('/api/projects/wordcount/tasks/WC-14')
    test.skip(r.status() !== 200, 'no WC-14 on this machine: the live check L2 of M10 made it')
    await page.goto('/p/wordcount/tickets/WC-14')
    await expect(page.getByText('A judge read the branch, for the judge: checks').first()).toBeVisible()
    await expect(page.getByText('Verification', { exact: true }).first()).toBeVisible()
    await expect(page.locator('[data-pull]').getByRole('link', { name: /^#\d+$/ })).toBeVisible()
    await shot(page, 'task-judge-pull')
  })
})

