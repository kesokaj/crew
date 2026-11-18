import { expect, test, type Page } from '@playwright/test'
import { admin, adminRole, cookie, token } from '../tests/session'

// The jump into a running run's microVM, live (13.11): from the task view of a ticket an agent works
// on, the terminal opens inside that run's sandbox, with the agent's files and processes. CREW_PROJECT
// and CREW_TICKET name the ticket; an agent must be running on it.
const project = process.env.CREW_PROJECT ?? 'wordcount'
const ticket = process.env.CREW_TICKET ?? 'WC-8'
const shots = process.env.CREW_SHOTS ?? ''
const shot = async (page: Page, name: string) => {
  if (!shots) return
  const h = await page.evaluate(() => document.querySelector('main')?.scrollHeight ?? 900)
  await page.setViewportSize({ width: 1280, height: Math.min(Math.max(h + 52, 900), 8000) })
  await page.screenshot({ path: `${shots}/${name}.png`, fullPage: true })
  await page.setViewportSize({ width: 1280, height: 900 })
}

test('the board and the task view while an agent works, and the jump into its sandbox', async ({ page, context }) => {
  await context.addCookies([cookie(token({ email: admin, roles: [adminRole] }))])
  await page.goto(`/p/${project}/board`)
  await expect(page.locator(`[data-card="${ticket}"]`)).toContainText(/is (building|reviewing|testing|analysing|starting)/, { timeout: 5 * 60_000 })
  await shot(page, 'live-board')
  await page.locator(`[data-card="${ticket}"]`).click()
  await expect(page.getByText('Steps the agent set for itself').first()).toBeVisible()
  await expect(page.locator('[data-log]')).toBeVisible()
  await shot(page, 'live-task')
  await page.getByRole('link', { name: 'Jump into the running sandbox' }).click()
  await expect(page.getByRole('heading', { name: 'Inside a running sandbox' })).toBeVisible()
  await page.getByLabel('Type a command').fill('ls ~/work; echo AGENTS-$(ps -eo comm | grep -cE "^agy$"); echo JUMP-$((40+2))')
  await page.getByRole('button', { name: 'Run' }).click()
  await expect(page.locator('[data-terminal] .xterm-rows')).toContainText('JUMP-42', { timeout: 60_000 })
  // The marker is the shell's sum, so only its output matches, never the line that was typed.
  await expect(page.locator('[data-terminal] .xterm-rows')).toContainText(/AGENTS-[1-9]/)
  await shot(page, 'live-jump')
})
