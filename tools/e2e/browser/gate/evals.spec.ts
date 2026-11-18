import { expect, test } from '@playwright/test'

type Suite = { id: number; state: string; unmeasured?: string | null; result?: { baseline?: { cases: number } } | null }

// nothing is a suite that ended having measured no case, read from its result, so that a release
// that does not say why fails here.
const nothing = (s: Suite) => ['done', 'stopped'].includes(s.state) && (s.result?.baseline?.cases ?? 0) === 0

// A suite that measured no case says "nothing measured" with its reason on the Evals tab, and
// Analytics counts it rather than drawing it as 0% (M15 on dave). On the copy of the stored state,
// so a release is checked against the suites the machine has. It reads only.
test('a suite that measured nothing says so, on every project', async ({ page }) => {
  const overview = (await (await page.request.get('/api/overview')).json()) as { projects: { name: string }[] }
  for (const { name } of overview.projects) {
    const r = await page.request.get(`/api/projects/${name}/evals`)
    expect(r.status(), name).toBe(200)
    const unmeasured = ((await r.json()) as { suites: Suite[] }).suites.filter(nothing)
    if (unmeasured.length === 0) continue
    await page.goto(`/p/${name}/knowledge?tab=evals`)
    for (const s of unmeasured) {
      const row = page.locator(`[data-suite-id="${s.id}"]`)
      await expect(row.locator('[data-goals]'), `${name} suite ${s.id}`).toHaveText('nothing measured')
      expect(s.unmeasured, `${name} suite ${s.id}: why it measured nothing`).toBeTruthy()
      await expect(row, `${name} suite ${s.id}`).toContainText(s.unmeasured!)
    }
    if (unmeasured.some((s) => s.state === 'done')) {
      await page.goto(`/p/${name}/analytics`)
      await expect(page.locator('[data-unmeasured]'), name).toContainText('measured nothing')
    }
  }
})
