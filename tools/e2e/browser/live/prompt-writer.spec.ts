import { expect, test, type Page } from '@playwright/test'
import { admin, adminRole, cookie, token } from '../tests/session'

// The prompt writer, live (7.2): a new plan's first message gets a proposal from a real call of the
// prompt writer; the operator answers a question and asks for Rewrite again; uses the rewrite, which
// starts the planning agent once; then selects text in the draft's message box for Improve text and
// Review text. Four calls of the prompt writer and one run of the planning agent.
const shots = process.env.CREW_SHOTS ?? ''
const shot = async (page: Page, name: string) => {
  if (!shots) return
  const h = await page.evaluate(() => document.querySelector('main')?.scrollHeight ?? 900)
  await page.setViewportSize({ width: 1280, height: Math.min(Math.max(h + 52, 900), 8000) })
  await page.screenshot({ path: `${shots}/${name}.png`, fullPage: true })
  await page.setViewportSize({ width: 1280, height: 900 })
}

test('a first message, its proposal, Rewrite again, Use the rewrite, then Improve and Review text', async ({ page, context }) => {
  await context.addCookies([cookie(token({ email: admin, roles: [adminRole] }))])
  await page.goto('/plans')
  await page.getByLabel('Your first message').fill(
    'I want a small tool that tells me when a website I run is down. It should check a few sites and email me. Keep it simple.',
  )
  await page.getByLabel('Title').fill('Site watcher (M7 live check)')
  const started = Date.now()
  await page.getByRole('button', { name: 'Send', exact: true }).click()
  await page.waitForURL(/\/plans\/[0-9a-f-]+$/)
  const panel = page.getByRole('region', { name: "Crew's rewrite of your message" })
  await expect(panel).toBeVisible()
  await expect(panel.getByRole('button', { name: 'Send mine' })).toBeVisible()
  await shot(page, 'live-proposal-writing')
  await expect(panel.locator('[data-rewrite]')).toBeVisible({ timeout: 60_000 })
  console.log(`the proposal took ${Math.round((Date.now() - started) / 1000)} s`)
  await shot(page, 'live-proposal')
  // Rewrite again, with an answer to its first question, when it asked one.
  const answers = panel.locator('label input')
  if ((await answers.count()) > 0) {
    await answers.first().fill('Three sites, checked every five minutes; email to me only.')
    await panel.getByRole('button', { name: 'Rewrite again' }).click()
    await expect(page.getByText(/round 2/i)).toBeVisible({ timeout: 10_000 })
    await expect(panel.locator('[data-rewrite]')).toBeVisible({ timeout: 60_000 })
    await shot(page, 'live-proposal-round2')
  }
  await panel.getByRole('button', { name: 'Use the rewrite' }).click()
  await expect(page.locator('[data-sent-from="rewrite"]')).toBeVisible({ timeout: 15_000 })
  await shot(page, 'live-sent')
  // The planning agent answers the chosen text.
  await expect(page.getByText(/Planning agent ·/)).toBeVisible({ timeout: 5 * 60_000 })
  await shot(page, 'live-planning-agent')
  // Improve text and Review text on a selection of the operator's own next message.
  const box = page.getByLabel('Your message')
  await box.fill('Also add a page that shows the history of every check, kept for a month.')
  for (const [ask, done] of [
    ['Improve text', '[data-rewrite]'],
    ['Review text', '[data-notes]'],
  ] as const) {
    await box.evaluate((el: HTMLTextAreaElement) => {
      el.focus()
      el.setSelectionRange(0, el.value.length)
      el.dispatchEvent(new MouseEvent('mouseup', { bubbles: true }))
    })
    await page.getByRole('menuitem', { name: ask }).click()
    const answer = page.getByRole('region', { name: ask })
    await expect(answer.locator(done)).toBeVisible({ timeout: 60_000 })
    await shot(page, ask === 'Improve text' ? 'live-improve' : 'live-review')
    await answer.getByRole('button', { name: ask === 'Improve text' ? 'Keep mine' : 'Close' }).click()
  }
})
