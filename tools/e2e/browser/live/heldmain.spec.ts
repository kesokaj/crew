import { expect, test, type Page } from '@playwright/test'
import { admin, adminRole, cookie, token } from '../tests/session'

// The live check L3 of M9 (13.8, 13.11), approved by the operator: in edit mode the project terminal
// pushes a main that adds a GitHub workflow, a file that is code. Crew holds it for the operator, the
// inbox names the workflow first, and Refuse leaves Crew's main as it was. One microVM, no agent run.
const project = process.env.CREW_PROJECT || 'wordcount'
const shots = process.env.CREW_SHOTS ?? ''
const shot = async (page: Page, name: string) => {
  if (shots) await page.screenshot({ path: `${shots}/${name}.png`, fullPage: true })
}

async function run(page: Page, line: string, until: string | RegExp, timeout = 60_000) {
  await page.getByLabel('Type a command').fill(line)
  await page.getByRole('button', { name: 'Run' }).click()
  await expect(page.locator('[data-terminal] .xterm-rows')).toContainText(until, { timeout })
}

test('a main from the terminal that adds a workflow is held with it named, and refused', async ({ page, context }) => {
  await context.addCookies([cookie(token({ email: admin, roles: [adminRole] }))])
  const origin = { Origin: `https://${process.env.CREW_HOSTNAME}` }
  const before = (await (await page.request.get(`/api/projects/${project}/git`)).json()).main
  await page.request.delete(`/api/projects/${project}/terminal`, { headers: origin })
  await page.goto(`/p/${project}/terminal`)
  await page.getByRole('button', { name: 'Open the project terminal' }).click()
  await expect(page.locator('[data-where]')).toContainText('diagnostics', { timeout: 3 * 60_000 })
  await run(page, 'export PAGER=cat GIT_PAGER=cat; cd ~/work && git fetch -q origin && git reset -q --hard origin/main && echo READY-$((40+2))', 'READY-42')
  await page.getByRole('button', { name: 'Switch to edit mode' }).click()
  await expect(page.locator('[data-where]')).toContainText('edit')
  await run(
    page,
    'mkdir -p .github/workflows && printf "on: push\\njobs: {}\\n" > .github/workflows/m9-live.yml && git add .github && ' +
      'git commit -q -m "M9 live check: a workflow from the terminal" && git push -q origin HEAD:main && echo PUSHED-$((40+2))',
    'PUSHED-42',
  )
  // Held, not taken: the inbox names the workflow first, and main stays.
  type Item = { id: string; kind: string; body: string; alternatives: { label: string; action?: string; body?: object }[] }
  let item: Item | undefined
  await expect
    .poll(
      async () => {
        const { items } = (await (await page.request.get('/api/inbox')).json()) as { items: Item[] }
        item = items.find((i) => i.kind === 'terminal_main' && i.id.startsWith(`main/${project}/`))
        return item?.body ?? ''
      },
      { timeout: 60_000 },
    )
    .toMatch(/^It changes files that are code for the agent CLI, or GitHub workflows: \.github\/workflows\/m9-live\.yml\./)
  expect((await (await page.request.get(`/api/projects/${project}/git`)).json()).main).toBe(before)
  await page.goto('/inbox')
  await expect(page.locator(`[data-item="${item!.id}"]`)).toContainText('.github/workflows/m9-live.yml')
  await shot(page, 'live-held-main')
  await page.locator(`[data-item="${item!.id}"]`).getByRole('button', { name: 'Refuse' }).click()
  await expect(page.locator(`[data-item="${item!.id}"]`)).toHaveCount(0)
  expect((await (await page.request.get(`/api/projects/${project}/git`)).json()).main).toBe(before)
  // Leaving the terminal's page ended edit mode already (13.11): the terminal is closed.
  expect((await page.request.delete(`/api/projects/${project}/terminal`, { headers: origin })).ok()).toBe(true)
})
