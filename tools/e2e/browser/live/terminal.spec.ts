import { expect, test, type Page } from '@playwright/test'
import { admin, adminRole, cookie, token } from '../tests/session'

// The project terminal, live (13.11): it opens with the repository and every branch, takes no push in
// diagnostics mode, and in edit mode pushes one commit to main, which Crew takes as the new base and
// sends to the remote. One microVM, no agent run. CREW_PROJECT names the project.
const project = process.env.CREW_PROJECT ?? 'wordcount'
const shots = process.env.CREW_SHOTS ?? ''
const shot = async (page: Page, name: string) => {
  if (shots) await page.screenshot({ path: `${shots}/${name}.png`, fullPage: true })
}

// run types a line and waits for until in what the terminal shows. until must be something only the
// output holds, such as a marker the shell computes, never a word of the line itself.
async function run(page: Page, line: string, until: string | RegExp, timeout = 60_000) {
  await page.getByLabel('Type a command').fill(line)
  await page.getByRole('button', { name: 'Run' }).click()
  await expect(page.locator('[data-terminal] .xterm-rows')).toContainText(until, { timeout })
}

test('the project terminal, diagnostics then edit mode with one push to main', async ({ page, context }) => {
  await context.addCookies([cookie(token({ email: admin, roles: [adminRole] }))])
  const before = await (await page.request.get(`/api/projects/${project}/git`)).json()
  // A terminal left open by an earlier try is closed first.
  await page.request.delete(`/api/projects/${project}/terminal`, { headers: { Origin: `https://${process.env.CREW_HOSTNAME}` } })
  await page.goto(`/p/${project}/terminal`)
  await page.getByRole('button', { name: 'Open the project terminal' }).click()
  await expect(page.locator('[data-where]')).toContainText('diagnostics', { timeout: 3 * 60_000 })
  await expect(page.locator('[data-terminal] .xterm-rows')).toBeVisible()
  await run(page, 'export PAGER=cat GIT_PAGER=cat; cd ~/work && git branch -a && git log --oneline -3 && git status -sb', 'remotes/origin/main')
  await shot(page, 'live-term')
  // Diagnostics: the endpoint takes no push.
  await run(page, 'git commit -q --allow-empty -m nothing && git push -q origin HEAD:main; echo PUSH-EXIT=$?', /PUSH-EXIT=[1-9]/)
  await run(page, 'git reset -q --hard origin/main && echo RESET-$((40+2))', 'RESET-42')
  // Edit mode: one commit to main.
  await page.getByRole('button', { name: 'Switch to edit mode' }).click()
  await expect(page.locator('[data-where]')).toContainText('edit')
  await run(
    page,
    'printf "Edited by hand from the project terminal, in edit mode (M7 live check).\\n" >> TERMINAL.md && git add TERMINAL.md && ' +
      'git commit -q -m "M7 live check: a commit from the project terminal in edit mode" && git push -q origin HEAD:main && echo PUSHED-$((40+2))',
    'PUSHED-42',
  )
  await shot(page, 'live-term-edit')
  // Crew took it as the new base: main moved, and a push to the remote followed it.
  await expect
    .poll(async () => (await (await page.request.get(`/api/projects/${project}/git`)).json()).main, { timeout: 30_000 })
    .not.toBe(before.main)
  const log = await (await page.request.get(`/api/projects/${project}/git/log?ref=main&limit=1`)).json()
  expect(log.commits[0].subject).toBe('M7 live check: a commit from the project terminal in edit mode')
  await page.getByRole('button', { name: 'Back to diagnostics' }).click()
  await page.getByRole('button', { name: 'Close the terminal' }).click()
  await expect(page.getByRole('button', { name: 'Open the project terminal' })).toBeVisible()
})
