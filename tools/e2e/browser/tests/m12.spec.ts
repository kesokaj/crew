import { expect, test } from '@playwright/test'
import { admin, adminRole, cookie, token } from './session'
import { api, project, shot } from './helpers'

// The screens of M12 (design 9, 12.9, 20.1): a project's knowledge, its documentation, and its skills
// and context. They read and start nothing: no feedback is given, no page is edited, no bug reported.
test.describe('the screens of M12', () => {
  test.use({ viewport: { width: 1280, height: 900 } })
  test.beforeEach(async ({ context }) => {
    await context.addCookies([cookie(token({ email: admin, roles: [adminRole] }))])
  })

  test('Knowledge has its memory, bugs, feedback, evals, and decisions', async ({ page }) => {
    const p = await project(page)
    const { entries } = (await api(page, `/api/projects/${p}/memory`)) as { entries: { status: string }[] }
    await page.goto(`/p/${p}/knowledge`)
    await expect(page.getByRole('heading', { name: 'Knowledge' })).toBeVisible()
    await expect(page.getByRole('tab', { name: /Memory/ })).toHaveAttribute('aria-selected', 'true')
    await expect(page.getByText('Short hints agents write for later runs')).toBeVisible()
    await expect(page.locator('[data-memory]')).toHaveCount(entries.filter((e) => e.status === 'active').length)
    await shot(page, 'knowledge')
    const { bugs } = (await api(page, `/api/projects/${p}/bugs`)) as { bugs: unknown[] }
    await page.getByRole('tab', { name: /Bugs/ }).click()
    await expect(page.getByText('Any agent can report a bug.')).toBeVisible()
    await expect(page.locator('[data-bug]')).toHaveCount(bugs.length)
    await expect(page.getByRole('button', { name: 'Report a bug' })).toBeVisible()
    await shot(page, 'knowledge--bugs')
    await page.getByRole('tab', { name: /Feedback/ }).click()
    await expect(page.getByLabel('Your feedback')).toBeVisible()
    await expect(page.getByLabel('About the')).toHaveValue('builder')
    await expect(page.getByText('Make it a rule: store it in memory now')).toBeVisible()
    await shot(page, 'knowledge--feedback')
    await page.getByRole('tab', { name: /Evals/ }).click()
    await expect(page.getByText("Cases come from this project's own finished tickets.")).toBeVisible()
    await page.getByRole('tab', { name: /Decisions/ }).click()
    await expect(page.getByText('Choices the plan did not settle.')).toBeVisible()
    await shot(page, 'knowledge--decisions')
  })

  test('Documentation shows the pages of main, each with what it covers and whether it is current', async ({ page }) => {
    const p = await project(page)
    const docs = (await api(page, `/api/projects/${p}/docs`)) as { pages: { path: string; kind: string }[]; current: number }
    expect(docs.pages.length, 'a project has at least its README').toBeGreaterThan(0)
    await page.goto(`/p/${p}/docs`)
    await expect(page.getByRole('heading', { name: 'Documentation' })).toBeVisible()
    await expect(page.getByText(`${docs.current} of ${docs.pages.length} pages are current.`)).toBeVisible()
    await expect(page.locator('[data-page]')).toHaveCount(docs.pages.length)
    await expect(page.getByRole('article', { name: 'The page' })).toContainText('on main')
    await expect(page.getByRole('region', { name: 'About this page' })).toContainText('Covers')
    await shot(page, 'docs')
    // A search finds the overview by its first word.
    const readme = (await api(page, `/api/projects/${p}/docs/page?path=README.md`)) as { page: { title: string } }
    await page.getByLabel('Search the pages').fill(readme.page.title.split(/\W+/).filter(Boolean)[0] ?? p)
    await page.getByLabel('Search the pages').press('Enter')
    await expect(page.getByRole('list', { name: 'Found' }).getByRole('link').first()).toBeVisible()
  })

  test("Settings lists the project's skills and context beside the base", async ({ page }) => {
    const p = await project(page)
    const skills = (await api(page, `/api/projects/${p}/skills`)) as { baseCounts: Record<string, number>; project: unknown[] }
    await page.goto(`/p/${p}/settings`)
    const section = page.getByRole('region', { name: 'Skills and context' })
    await expect(section).toBeVisible()
    await expect(section).toContainText(`${skills.baseCounts.skill} skills, ${skills.baseCounts.agent} subagents`)
    await expect(section.locator('[data-layer]')).toHaveCount(skills.project.length)
    await expect(section).toContainText('It calls request_skill and carries on.')
    await shot(page, 'settings')
  })
})
