import { expect, test } from '@playwright/test'

// The Crew level's signals (0.2.4 and 0.2.5 on dave): every row of the matrix adds up to All, though a
// plan's choice of a rewrite has no project, and Latest signals leaves out the measurements, a
// sandbox's start-up and a ticket's tokens and time, and says what each signal holds. It reads only.
test("every row of the Crew level's signals adds up, and the latest say what they hold", async ({ page }) => {
  await page.goto('/crew')
  await expect(page.getByRole('heading', { name: 'Latest signals' })).toBeVisible()
  const rows = await page.locator('[data-matrix] tbody tr').all()
  expect(rows.length).toBeGreaterThan(0)
  for (const row of rows) {
    const cells = (await row.locator('td').allInnerTexts()).slice(1).map(Number)
    const all = cells.pop()
    expect.soft(cells.reduce((a, b) => a + b, 0), await row.getAttribute('data-kind')).toBe(all)
  }
  await expect(page.locator('[data-latest] tbody tr', { hasText: 'Sandbox start-up' })).toHaveCount(0)
  // A ticket's tokens and time is a measurement too, and every other line says what its signal holds
  // (0.2.5 on dave: 23 of 30 lines repeated the signal's name).
  for (const row of await page.locator('[data-latest] tbody tr').all()) {
    const [, , signal, what] = await row.locator('td').allInnerTexts()
    expect.soft(what, signal).not.toBe(signal)
  }
  // The kind keeps its stored name, cost_and_time, and since 0.53 its label says what it holds.
  await expect(page.locator('[data-latest] tbody tr', { hasText: /(Cost|Tokens) and time of a ticket/ })).toHaveCount(0)
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
})
