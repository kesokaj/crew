import { expect, test, type Page } from '@playwright/test'
import { admin, adminRole, cookie, token } from '../tests/session'

// The live checks of M8 (12.3, 12.7), as they are since 0.42, when agy became the only CLI. Approved
// by the operator for M8: reading agy's list of models and its limits, which makes no model request but starts the login-check microVMs; and one ticket
// let go with a pin on its builder, which runs the lead, the builder, the reviewer, the tester, and
// the summary writer on the subscriptions. CREW_PROJECT and CREW_TICKET name the ticket.
const project = process.env.CREW_PROJECT || 'wordcount'
const ticket = process.env.CREW_TICKET || 'WC-9'
const shots = process.env.CREW_SHOTS ?? ''
const shot = async (page: Page, name: string) => {
  if (!shots) return
  const h = await page.evaluate(() => document.querySelector('main')?.scrollHeight ?? 900)
  await page.setViewportSize({ width: 1280, height: Math.min(Math.max(h + 52, 900), 8000) })
  await page.screenshot({ path: `${shots}/${name}.png`, fullPage: true })
  await page.setViewportSize({ width: 1280, height: 900 })
}

type CLIModels = { cli: string; readAt?: string; short: boolean; models: { model: string; offered: boolean }[]; windows: { group: string; window: string }[] }

async function models(page: Page): Promise<CLIModels[]> {
  const r = await page.request.get('/api/crew/models')
  expect(r.status()).toBe(200)
  return ((await r.json()) as { clis: CLIModels[] }).clis
}

test("Refresh reads agy's list of models and its limits, with no model request", async ({ page, context }) => {
  await context.addCookies([cookie(token({ email: admin, roles: [adminRole] }))])
  const before = Date.now()
  await page.goto('/models')
  await page.getByRole('button', { name: 'Refresh' }).click()
  // The list is read after the press, from agy models.
  await expect
    .poll(async () => (await models(page)).every((c) => !c.short && c.readAt && Date.parse(c.readAt) >= before - 1000), { timeout: 4 * 60_000, intervals: [5000] })
    .toBe(true)
  const clis = await models(page)
  expect(clis.map((c) => c.cli)).toEqual(['agy'])
  const agy = clis.find((c) => c.cli === 'agy')!
  expect(agy.models.some((m) => m.model === 'gemini-3.8-flash' && m.offered)).toBe(true)
  // agy's /usage gives both groups, each with its five-hour and weekly windows.
  expect(agy.windows.map((w) => w.group + '/' + w.window).sort()).toEqual(expect.arrayContaining(['3p/5h', '3p/weekly', 'gemini/5h', 'gemini/weekly']))
  await page.reload()
  await shot(page, 'live-models')
  await page.goto('/clis')
  await expect(page.getByText(/Limit · agy's Gemini models/i)).toBeVisible()
  await shot(page, 'live-clis')
})

test("a pin on a ticket's builder runs it on the pinned model, and the rule for checks moves the reviewer", async ({ page, context }) => {
  test.setTimeout(60 * 60_000)
  await context.addCookies([cookie(token({ email: admin, roles: [adminRole] }))])
  await page.goto(`/p/${project}/board`)
  await page.locator(`[data-card="${ticket}"]`).click()
  await page.locator('#pin-role').selectOption('builder')
  await page.locator('#pin-model').selectOption('agy/claude-sonnet-5-5')
  await expect(page.locator('#pin-model')).toHaveValue('agy/claude-sonnet-5-5')
  await page.locator('#pin-effort').selectOption('medium')
  await expect(page.getByText(/A builder would run on/).locator('xpath=following-sibling::dd[1]')).toContainText('agy · claude-sonnet-5-5-medium, pinned on the ticket')
  await shot(page, 'live-pin')
  const hold = page.getByRole('button', { name: 'Let it go' })
  if (await hold.count()) await hold.click()
  // The builder starts on the pinned model and effort, and the run says where they came from.
  type Sub = { role: string; runs: { cli: string; model?: string; effort?: string; modelFrom?: string }[] }
  const subs = async () => {
    const r = await page.request.get(`/api/projects/${project}/tasks/${ticket}/subtasks`)
    return ((await r.json()) as { subtasks: Sub[] }).subtasks
  }
  await expect
    .poll(async () => (await subs()).some((s) => s.role === 'builder' && s.runs.some((r) => r.model === 'claude-sonnet-5-5' && r.effort === 'medium' && r.modelFrom === 'ticket')), {
      timeout: 15 * 60_000,
      intervals: [10_000],
    })
    .toBe(true)
  await page.reload()
  await shot(page, 'live-pinned-build')
  // The review of a Claude build runs on another family, a Gemini model, by the rule.
  await expect
    .poll(async () => (await subs()).some((s) => s.role === 'reviewer' && s.runs.some((r) => r.cli === 'agy' && r.modelFrom === 'check_rule')), {
      timeout: 30 * 60_000,
      intervals: [15_000],
    })
    .toBe(true)
  await page.reload()
  await shot(page, 'live-check-rule')
})
