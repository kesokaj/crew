import { expect, test } from '@playwright/test'
import { admin, adminRole, cookie, token } from './session'
import { api, project, shot } from './helpers'

// The screens of M8 (design 12.3, 12.7, 7.9, 20.1): the Models screen, the limits on Agent CLIs, the
// lead slots and their queue on the front page, a project's Settings, and the task view's agent. They
// read and change nothing.
test.describe('the screens of M8', () => {
  test.use({ viewport: { width: 1280, height: 900 } })
  test.beforeEach(async ({ context }) => {
    await context.addCookies([cookie(token({ email: admin, roles: [adminRole] }))])
  })

  test('Models shows every model each CLI lists, the agents, and what Crew chose', async ({ page }) => {
    const m = (await api(page, '/api/crew/models')) as {
      clis: { cli: string; models: { model: string; offered: boolean }[] }[]
      agents: { role: string; entries: { entry: string; offered: boolean }[] }[]
    }
    await page.goto('/models')
    await expect(page.getByRole('heading', { name: 'Models', exact: true })).toBeVisible()
    await expect(page.getByRole('heading', { name: 'What Crew chose' })).toBeVisible()
    await expect(page.getByRole('heading', { name: 'Models the CLIs offer' })).toBeVisible()
    await expect(page.getByRole('heading', { name: 'The agents' })).toBeVisible()
    // Every model a CLI lists is on the screen, by its name.
    for (const c of m.clis)
      for (const name of new Set(c.models.map((x) => x.model)))
        await expect(page.getByRole('button', { name, exact: true }).first()).toBeVisible()
    // An entry no CLI offers now is shown as skipped, as gemini-3.1-pro is on these accounts.
    const gone = m.agents.flatMap((a) => a.entries.filter((e) => !e.offered).map((e) => e.entry))
    if (gone.length) await expect(page.getByText(`${gone[0]}: not offered, skipped`).first()).toBeVisible()
    // Every agent has its model and its effort to choose.
    await expect(page.getByLabel(/^The effort of the /)).toHaveCount(m.agents.length)
    await expect(page.getByLabel(/^The model of the /)).toHaveCount(m.agents.length)
    await shot(page, 'models')
    // One model opens with what it has done.
    await page.getByRole('button', { name: m.clis[0].models[0].model, exact: true }).first().click()
    await expect(page.getByRole('heading', { name: 'What it has done' })).toBeVisible()
    await shot(page, 'models-one')
  })

  test('Agent CLIs shows the limits of each group of models, and the rule for checks', async ({ page }) => {
    await page.goto('/clis')
    await expect(page.getByRole('heading', { name: 'Agent CLIs' })).toBeVisible()
    await expect(page.getByText(/Limit · agy's Gemini models/i)).toBeVisible()
    await expect(page.getByText(/Limit · agy's Claude and GPT models/i)).toBeVisible()
    await expect(page.getByText('The check rule', { exact: true })).toBeVisible()
    await shot(page, 'clis')
  })

  test('the front page shows the lead slots of each CLI and the lead runs that wait for one', async ({ page }) => {
    await page.goto('/')
    await expect(page.getByRole('region', { name: 'Lead slots' })).toBeVisible()
    await expect(page.getByRole('region', { name: 'Waiting for a lead slot' })).toBeVisible()
    await expect(page.getByRole('region', { name: 'Lead slots' }).getByText('agy')).toBeVisible()
    await shot(page, 'overview-m8')
  })

  test("a project's Settings gives each stage's agent a model and an effort, and a ticket its pin", async ({ page }) => {
    const p = await project(page)
    await page.goto(`/p/${p}/settings`)
    await expect(page.getByRole('heading', { name: 'Settings', exact: true })).toBeVisible()
    await expect(page.getByLabel('The model of the builder')).toBeVisible()
    await expect(page.getByLabel('The effort of the builder')).toBeVisible()
    await expect(page.getByText('Runs in this project so far')).toBeVisible()
    await shot(page, 'settings')
    await page.goto(`/p/${p}/board`)
    await page.locator('[data-column="done"] [data-card]').first().click()
    await expect(page.getByText('Agent for this ticket')).toBeVisible()
    await expect(page.getByText('A builder would run on')).toBeVisible()
    // A done ticket takes no pin: a change is a new ticket.
    await expect(page.locator('#pin-model')).toBeDisabled()
    await shot(page, 'task-agent')
  })
})
