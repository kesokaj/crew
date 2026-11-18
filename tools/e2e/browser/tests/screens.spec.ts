import { expect, test } from '@playwright/test'
import { admin, adminRole, cookie, token } from './session'
import { api, project, shot } from './helpers'
import { noDollars, screensToCheck } from '../gate/nodollars'

// The screens of M7 (design 20.1), each opened on the installation as it is, with a screenshot at the
// width of the simulation's pictures (docs/plan/screens/) when CREW_SHOTS names a folder. They read
// and start nothing: no run, no microVM, no rewrite. A project to show is the first one the API lists
// with a ticket that is done.
test.describe('the screens of M7', () => {
  test.use({ viewport: { width: 1280, height: 900 } })
  test.beforeEach(async ({ context }) => {
    await context.addCookies([cookie(token({ email: admin, roles: [adminRole] }))])
  })

  test('the frame shows the lead slots, the sandboxes, and the inbox; the front page the projects and the feed', async ({ page }) => {
    await page.goto('/')
    await expect(page.getByText('Leads')).toBeVisible()
    await expect(page.getByRole('banner').getByRole('link', { name: /^Inbox \d+$/ })).toBeVisible()
    await expect(page.getByText(/^Sandboxes \d+$/)).toBeVisible()
    await expect(page.getByRole('heading', { name: 'All projects', exact: true })).toBeVisible()
    await expect(page.getByRole('heading', { name: /What agents are doing/ })).toBeVisible()
    await shot(page, 'overview')
  })

  // Design 0.53: no screen shows a cost, an estimate, or a budget; where a cost was, tokens are. The
  // gate's tests run the same check on a release in check mode.
  test('no screen shows a dollar, an estimate, or a budget', async ({ page }) => {
    const screens = await screensToCheck(page)
    test.setTimeout(30_000 + 20_000 * screens.length)
    await noDollars(page, screens)
  })

  test('the inbox lists what needs you, each with its alternatives', async ({ page }) => {
    await page.goto('/inbox')
    await expect(page.getByRole('heading', { name: 'Inbox' })).toBeVisible()
    // The list loads after the heading: wait for it to hold what the frame counts.
    const frame = (await api(page, '/api/frame')) as { inbox: number }
    await expect(page.locator('[data-item]')).toHaveCount(frame.inbox)
    await shot(page, 'inbox')
  })

  test('a project has its board, plan, activity, trajectories, knowledge, and git', async ({ page }) => {
    const p = await project(page)
    await page.goto(`/p/${p}/board`)
    await expect(page.getByRole('heading', { name: 'Board' })).toBeVisible()
    await expect(page.getByRole('list', { name: 'Stages' })).toBeVisible()
    await expect(page.locator('[data-column="done"]')).toBeAttached()
    await shot(page, 'board')
    await page.goto(`/p/${p}/plan`)
    await expect(page.getByRole('heading', { name: /The original plan/ })).toBeVisible()
    await shot(page, 'plan')
    await page.goto(`/p/${p}/activity`)
    await expect(page.locator('tbody tr').first()).toBeVisible()
    await shot(page, 'activity')
    await page.goto(`/p/${p}/summaries`)
    await expect(page.getByRole('heading', { name: 'Summaries' })).toBeVisible()
    await shot(page, 'summaries')
    await page.goto(`/p/${p}/trajectories`)
    await expect(page.locator('[data-run]').first()).toBeVisible()
    await page.getByRole('button', { name: 'Reviewer' }).click()
    await expect(page.locator('[data-run]').first()).toBeVisible()
    await page.getByRole('button', { name: 'All' }).click()
    await shot(page, 'traj')
    await page.goto(`/p/${p}/knowledge?tab=decisions`)
    await expect(page.getByRole('tab', { name: /Decisions/ })).toHaveAttribute('aria-selected', 'true')
    await shot(page, 'decisions')
    await page.goto(`/p/${p}/git`)
    await expect(page.locator('[data-branch="main"]')).toBeVisible()
    await expect(page.locator('[data-commit]').first()).toBeVisible()
    await shot(page, 'repo')
    // A commit opens with its diff.
    await page.locator('[data-commit] button').first().click()
    await expect(page.getByRole('region', { name: 'Commit' })).toBeVisible()
    await shot(page, 'repo-commit')
  })

  test('a done ticket opens its task view, a subtask its run, and the terminal waits to be opened', async ({ page }) => {
    const p = await project(page)
    await page.goto(`/p/${p}/board`)
    await page.locator('[data-column="done"] [data-card]').first().click()
    await expect(page.getByRole('heading', { name: 'Outcome' })).toBeVisible()
    await expect(page.getByText('Subtasks this ticket is using or has used')).toBeVisible()
    await expect(page.locator('[data-part="ticket"]')).toBeVisible()
    await expect(page.getByRole('heading', { name: 'Summary', exact: true })).toBeVisible()
    await expect(page.getByRole('heading', { name: 'Git', exact: true })).toBeVisible()
    await shot(page, 'task-done')
    await page.locator('[data-subtask] a').first().click()
    await expect(page.getByText('How the agent was started')).toBeVisible()
    await shot(page, 'run')
    await page.goto(`/p/${p}/terminal`)
    await expect(page.getByRole('heading', { name: 'Terminal', exact: true })).toBeVisible()
    await shot(page, 'term-closed')
  })

  test('Plans lists the drafts, and the new plan box takes a first message', async ({ page }) => {
    await page.goto('/plans')
    await expect(page.getByRole('heading', { name: 'Plans' })).toBeVisible()
    await expect(page.getByRole('region', { name: 'A new plan' })).toBeVisible()
    await shot(page, 'plans')
    const first = page.locator('a[href^="/plans/"]').first()
    if ((await first.count()) > 0) {
      await first.click()
      await expect(page.getByRole('heading', { name: 'Conversation' })).toBeVisible()
      await shot(page, 'plans-draft')
    }
  })

  test('selecting text in a box where you write shows Improve text and Review text', async ({ page }) => {
    const p = await project(page)
    await page.goto(`/p/${p}/board`)
    await page.getByRole('button', { name: 'Add a ticket' }).click()
    const why = page.locator('#t-why')
    await why.fill('Customers ask where their order is.')
    await why.evaluate((el: HTMLTextAreaElement) => {
      el.focus()
      el.setSelectionRange(0, 9)
      el.dispatchEvent(new MouseEvent('mouseup', { bubbles: true }))
    })
    await expect(page.getByRole('menu', { name: 'On the selection' })).toBeVisible()
    await expect(page.getByRole('menuitem', { name: 'Improve text' })).toBeVisible()
    await expect(page.getByRole('menuitem', { name: 'Review text' })).toBeVisible()
    await shot(page, 'select-menu')
  })

  test('the Database screen reads, and changes nothing', async ({ page }) => {
    await page.goto('/database')
    await expect(page.getByRole('heading', { name: 'Database' })).toBeVisible()
    await expect(page.locator('[data-group="The board"]')).toBeVisible()
    await expect(page.getByText(/PostgreSQL 18/)).toBeVisible()
    await shot(page, 'db')
  })

  test('a terminal is refused to a page of another address', async ({ page }) => {
    const p = await project(page)
    const resp = await page.request.get(`/api/projects/${p}/terminal/ws`, {
      headers: { Origin: 'https://elsewhere.example', Connection: 'Upgrade', Upgrade: 'websocket', 'Sec-WebSocket-Version': '13', 'Sec-WebSocket-Key': 'dGhlIHNhbXBsZSBub25jZQ==' },
    })
    expect(resp.status()).not.toBe(101)
  })
})
