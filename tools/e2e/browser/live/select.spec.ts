import { expect, test, type Page } from '@playwright/test'
import { admin, adminRole, cookie, token } from '../tests/session'

// Improve text and Review text on a selection, live (7.2): two calls of the prompt writer on the
// operator's own next message in the draft CREW_DRAFT, which must be open. Nothing is sent.
const draft = process.env.CREW_DRAFT ?? ''
const shots = process.env.CREW_SHOTS ?? ''
const shot = async (page: Page, name: string) => {
  if (!shots) return
  const h = await page.evaluate(() => document.querySelector('main')?.scrollHeight ?? 900)
  await page.setViewportSize({ width: 1280, height: Math.min(Math.max(h + 52, 900), 8000) })
  await page.screenshot({ path: `${shots}/${name}.png`, fullPage: true })
  await page.setViewportSize({ width: 1280, height: 900 })
}

test('Improve text and Review text on a selection send nothing', async ({ page, context }) => {
  expect(draft, 'CREW_DRAFT names an open draft').not.toBe('')
  await context.addCookies([cookie(token({ email: admin, roles: [adminRole] }))])
  await page.goto(`/plans/${draft}`)
  const box = page.getByLabel('Your message')
  await box.fill('Also add a page that shows the history of every check, kept for a month.')
  for (const [ask, done, close] of [
    ['Improve text', '[data-rewrite]', 'Keep mine'],
    ['Review text', '[data-notes]', 'Close'],
  ] as const) {
    await box.evaluate((el: HTMLTextAreaElement) => {
      el.focus()
      el.setSelectionRange(0, el.value.length)
      el.dispatchEvent(new MouseEvent('mouseup', { bubbles: true }))
    })
    const t = Date.now()
    await page.getByRole('menuitem', { name: ask }).click()
    const answer = page.getByRole('region', { name: ask })
    await expect(answer.locator(done)).toBeVisible({ timeout: 60_000 })
    console.log(`${ask} answered in ${Math.round((Date.now() - t) / 1000)} s`)
    await shot(page, ask === 'Improve text' ? 'live-improve' : 'live-review')
    await answer.getByRole('button', { name: close }).click()
  }
  await expect(box).toHaveValue('Also add a page that shows the history of every check, kept for a month.')
})
