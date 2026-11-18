import { expect, test } from '@playwright/test'

type Share = { n: number; of: number; share: number }

// A share of no ticket reads "—", not 0%, which shows none of them passed (0.2.3 on dave: "Checks
// passed on the first request 0%, on Crew 0.2.3, 0 tickets"). A release in the gate is a version no
// ticket has run on yet, so the Crew level's share is one of none here. It reads only.
test('a share of no ticket says so, on the Crew level and every project', async ({ page }) => {
  const c = (await (await page.request.get('/api/crew/analytics')).json()) as { version: string; firstRequest: Share }
  await page.goto('/crew')
  const first = page.locator('[data-stat="first-request"]')
  if (c.firstRequest.of === 0) {
    await expect(first).not.toContainText('%')
    await expect(first).toContainText(`no ticket has run on Crew ${c.version} yet`)
  } else {
    await expect(first).toContainText(`${Math.round(c.firstRequest.share * 100)}%`)
  }
  const overview = (await (await page.request.get('/api/overview')).json()) as { projects: { name: string }[] }
  for (const { name } of overview.projects) {
    const a = (await (await page.request.get(`/api/projects/${name}/analytics`)).json()) as { done: number }
    if (a.done > 0) continue
    await page.goto(`/p/${name}/analytics`)
    await expect(page.locator('[data-stat="passed-first"]'), name).not.toContainText('%')
  }
})
