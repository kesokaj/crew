import { expect, test } from '@playwright/test'

// Since design 0.79 no run and no conversation gets gcloud (AGENTS.md safety rule 8, 12.6): neither
// settings screen offers it, and Setup says a run gets none. The test reads only. Each screen's field
// beside the one that went shows that the screen's settings arrived.
test('no settings screen offers gcloud for a sandbox, and Setup says a run gets none', async ({ page, request }) => {
  await page.goto('/settings')
  await expect(page.locator('[data-setting="conversations.idle_minutes"]')).toBeVisible()
  await expect(page.locator('[data-setting="conversations.credentials"]')).toHaveCount(0)

  const r = await request.get('/api/overview')
  expect(r.status()).toBe(200)
  const { projects } = (await r.json()) as { projects: { name: string }[] }
  // The gate's copy holds the machine's projects; with none, the loop would show nothing.
  expect(projects.length).toBeGreaterThan(0)
  for (const p of projects) {
    await page.goto(`/p/${encodeURIComponent(p.name)}/settings`)
    await expect(page.locator('[data-setting="sandbox.test"]')).toBeVisible()
    await expect(page.locator('[data-setting="sandbox.credentials"]')).toHaveCount(0)
    await expect(page.getByText('gcloud in its sandboxes')).toHaveCount(0)
  }

  await page.goto('/setup')
  const row = page.getByText(/^the login of the one CLI it runs, the git identity, and a GitHub token/)
  await expect(row).toBeVisible()
  await expect(row).toContainText('No gcloud.')

  await page.goto('/clis')
  const given = page.getByText(/^The login of agy, the git identity, and a GitHub token/)
  await expect(given).toBeVisible()
  await expect(given).toContainText('No gcloud.')
  await expect(page.getByText('act with gcloud')).toHaveCount(0)
})
