import { expect, test, type BrowserContext, type Page } from '@playwright/test'
import { shot } from './helpers'

// Both settings screens (design 14.2, 14.3; M17 phase 1): the cards of gsettings.png and settings.png in
// today's words, with no CLI but agy and no budget (0.42, 0.53), a change saved with its history line,
// and Put back. They change settings, so they run only where CREW_E2E_WRITES is set: the throwaway
// stack of the plan's D28, or an install-test VM, never dave's own installation.
test.skip(!process.env.CREW_E2E_WRITES, 'changes settings: only on a throwaway installation')

async function settingsValue(page: Page, path: string, url = '/api/settings/machine'): Promise<unknown> {
  const r = await page.request.get(url)
  expect(r.status()).toBe(200)
  let cur: unknown = await r.json()
  for (const p of path.split('.')) cur = (cur as Record<string, unknown>)[p]
  return cur
}

// noBudgetNorOtherCLI: since 0.42 agy is the one CLI and since 0.53 nothing is in dollars.
async function noBudgetNorOtherCLI(page: Page) {
  const text = (await page.locator('main').innerText()).toLowerCase()
  expect(text).not.toMatch(/\$\s?\d|budget|lead runs at once on claude/)
}

// Sessions and the fresh sign-in (design 20.3; M17 phase 4). On the stack the two headers the ingress
// takes from the token come from two cookies, and auth.crew.test stands in for the ingress's sign-in:
// it sets the time to now and gives a new device id, as a sign-in after the browser restarted does.
async function signedIn(context: BrowserContext, ageSeconds: number, sid: string) {
  const now = Math.floor(Date.now() / 1000)
  await context.addCookies([
    { name: 'm17_auth', value: String(now - ageSeconds), domain: '.crew.test', path: '/', secure: true },
    { name: 'm17_sid', value: sid, domain: '.crew.test', path: '/', secure: true },
  ])
}

const stale = 20 * 60
// The device ids of this run, so a run again on the same stack starts with devices of its own.
const run = Date.now()

test('Machine settings: every card, a change with its history line, and Put back', async ({ page }) => {
  await page.goto('/settings')
  await expect(page.getByRole('heading', { name: 'Machine settings', level: 1 })).toBeVisible()
  // The menu's first group ends with it, as gsettings.png has it.
  await expect(page.getByRole('link', { name: 'Machine settings' })).toBeVisible()
  for (const card of [
    'Lead slots, for each CLI',
    'Machine',
    'GitHub',
    'Network modes',
    'The model for each role',
    'Updates',
    'Improvements',
    'Kept in the database',
    'Looking after the machine',
    'Install',
    'Open-source, reproducible, and clean',
  ]) {
    await expect(page.getByRole('heading', { name: card, exact: true })).toBeVisible()
  }
  await expect(page.locator('[data-setting="max_concurrent_runs"]')).toHaveCount(1)
  await noBudgetNorOtherCLI(page)
  // A key 20.3 guards says that it asks for a fresh sign-in, and can be changed here (M17 phase 4).
  const auto = page.locator('[data-setting="updates.auto_install"]')
  await expect(auto).toContainText('Asks for a fresh sign-in')
  await expect(auto.getByRole('button', { name: /never|text/ })).toHaveCount(2)
  await shot(page, 'm17-machine-settings')

  // Raise max_concurrent_runs from 8 to 9: saved, with its line in the history.
  const slots = page.locator('[data-setting="max_concurrent_runs"]')
  await expect(slots.getByRole('textbox')).toHaveValue('8')
  await slots.getByRole('button', { name: /^More: / }).click()
  await expect(slots.getByRole('textbox')).toHaveValue('9')
  expect(await settingsValue(page, 'max_concurrent_runs')).toBe(9)
  await slots.getByRole('button', { name: 'History' }).click()
  const history = slots.locator('[data-history="max_concurrent_runs"]')
  await expect(history.locator('[data-line]').first()).toContainText('the default → 9')

  // Put it back: 8 again, and the history says so too.
  await history.locator('[data-line]').first().getByRole('button', { name: 'Put back' }).click()
  await expect(slots.getByRole('textbox')).toHaveValue('8')
  await expect(history.locator('[data-line]')).toHaveCount(2)
  await expect(history.locator('[data-line]').first()).toContainText('9 → the default')
  expect(await settingsValue(page, 'max_concurrent_runs')).toBe(8)

  // A setting that takes a fraction takes one while it holds a whole number.
  const over = page.locator('[data-setting="cpu_overcommit"]')
  await over.getByRole('textbox').fill('1.5')
  await over.getByRole('textbox').press('Enter')
  await expect.poll(() => settingsValue(page, 'cpu_overcommit')).toBe(1.5)

  // A value out of bounds is refused, with the reason under the field, and nothing is stored.
  const clean = page.locator('[data-setting="disk.clean_at"]')
  await clean.getByRole('textbox').fill('99')
  await clean.getByRole('textbox').press('Enter')
  await expect(clean.locator('[data-problem]')).toContainText('below stop_at')
  expect(await settingsValue(page, 'disk.clean_at')).toBe(85)
})

test("A project's Settings: Who decides and Limits without budgets, a change and Put back", async ({ page }) => {
  // The throwaway stack has the project as a row; an installation with a git identity makes it.
  if ((await page.request.get('/api/projects/m17shop/stages')).status() === 404) {
    const made = await page.request.post('/api/projects', { data: { name: 'm17shop', key: 'MS', plan: 'x', repository: { kind: 'local' } } })
    expect(made.status()).toBe(201)
  }
  await page.goto('/p/m17shop/settings')
  await expect(page.getByRole('heading', { name: 'Settings', level: 1 })).toBeVisible()
  for (const card of ['Who decides', 'Limits', 'Sandboxes and git', 'Documents and knowledge', 'Hosting']) {
    await expect(page.getByRole('heading', { name: card, exact: true })).toBeVisible()
  }
  const who = page.locator('section', { has: page.getByRole('heading', { name: 'Who decides', exact: true }) })
  await expect(who.locator('[data-setting="approvals.project"]')).toContainText('you')
  await expect(who.locator('[data-setting="approvals.created_task"] [aria-pressed="true"]')).toHaveText('another agent decides')
  await expect(who.locator('[data-setting="approvals.new_feature"] [aria-pressed="true"]')).toHaveText('a notice, then by itself')
  await expect(who.locator('[data-setting="approvals.new_feature_wait_hours"] input')).toHaveValue('24')
  const limits = page.locator('section', { has: page.getByRole('heading', { name: 'Limits', exact: true }) })
  await expect(limits.locator('[data-setting="limits.max_fix_rounds"] input')).toHaveValue('3')
  await expect(limits.locator('[data-setting="limits.run_timeout_minutes"] input')).toHaveValue('60')
  await expect(limits.locator('[data-setting="limits.run_max_turns"] input')).toHaveValue('200')
  await expect(limits.locator('[data-setting="watchdog.silent_stop_minutes"] input')).toHaveValue('15')
  // The stage table's Limit is a stepper, and it has the tickets in each stage now.
  await expect(page.getByRole('columnheader', { name: 'Tickets now' })).toBeVisible()
  await expect(page.locator('[data-stage-limit="in_progress"] input')).toHaveValue('6')
  await noBudgetNorOtherCLI(page)
  await shot(page, 'm17-project-settings')

  const rounds = limits.locator('[data-setting="limits.max_fix_rounds"]')
  await rounds.getByRole('button', { name: /^More: / }).click()
  await expect(rounds.locator('input')).toHaveValue('4')
  expect(await settingsValue(page, 'limits.max_fix_rounds', '/api/projects/m17shop/settings')).toBe(4)
  await rounds.getByRole('button', { name: 'History' }).click()
  const line = rounds.locator('[data-line]').first()
  await expect(line).toContainText('the default → 4')
  await line.getByRole('button', { name: 'Put back' }).click()
  await expect(rounds.locator('input')).toHaveValue('3')
  expect(await settingsValue(page, 'limits.max_fix_rounds', '/api/projects/m17shop/settings')).toBe(3)
})

// The machine's two items of the inbox (design 21; M17 phase 3), shaped as inbox.png's cards: two
// microVMs in a row that did not boot hold new runs, and the top bar says so; the database written
// again says from when to when. Resume lifts the hold, behind a fresh sign-in (M17 phase 4), and Read it
// takes the time away. The state is set in
// the throwaway stack's database first (m17-stack.sh seed-machine), so the test runs only where
// CREW_E2E_MACHINE says it was.
test('The inbox: microVMs that do not start, and the database written again', async ({ page, context }) => {
  test.skip(!process.env.CREW_E2E_MACHINE, "needs the machine's two items set in the stack's database")
  await page.goto('/inbox')
  const held = page.locator('[data-held]')
  await expect(held).toHaveText('MicroVMs do not start')
  const boot = page.locator('[data-item="boot"]')
  await expect(boot.getByRole('heading', { name: 'MicroVMs do not start: new runs are held' })).toBeVisible()
  await expect(boot).toContainText('kata: /dev/kvm is missing')
  await expect(boot).toContainText('crew level · needs you')
  await expect(boot.getByRole('button', { name: 'Run the check' })).toBeVisible()
  await expect(boot.getByRole('button', { name: 'Resume' })).toBeVisible()
  const db = page.locator('[data-item="db-writable"]')
  await expect(db.getByRole('heading', { name: 'The database can be written again' })).toBeVisible()
  await expect(db).toContainText('Crew could not write its database for 14 min, until 26 min ago.')
  await shot(page, 'inbox-machine')
  // The Projects screen's banner says the same, not that the disk is full (round 1 of the review).
  await page.goto('/')
  await expect(page.locator('[data-hold]')).toContainText('MicroVMs do not start: starting the sandbox: kata: /dev/kvm is missing')
  // Resume lifts a hold, so it asks for a fresh sign-in (20.3): a stale one gets the dialog, and the
  // hold stays.
  await signedIn(context, stale, `m17-device-r-${run}`)
  await page.goto('/inbox')
  await boot.getByRole('button', { name: 'Resume' }).click()
  await expect(page.locator('[data-signin="FRESH_SIGN_IN"]')).toBeVisible()
  await page.getByRole('button', { name: 'Not now' }).click()
  await expect(page.locator('[data-signin]')).toHaveCount(0)
  await expect(boot).toBeVisible()
  await signedIn(context, 60, `m17-device-r-${run}`)
  await boot.getByRole('button', { name: 'Resume' }).click()
  await expect(boot).toHaveCount(0)
  // The top bar reads the frame every 10 seconds.
  await expect(held).toHaveCount(0, { timeout: 15000 })
  await db.getByRole('button', { name: 'Read it' }).click()
  await expect(db).toHaveCount(0)
})


test('a key 20.3 guards asks for a fresh sign-in, and the round trip to the sign-in lets it', async ({ page, context }) => {
  await signedIn(context, stale, `m17-device-a-${run}`)
  await page.goto('/settings')
  const fresh = page.locator('[data-setting="sessions.fresh_minutes"]')
  await expect(fresh).toContainText('Asks for a fresh sign-in, at most 15 minutes old')
  const mine = page.locator('[data-device]', { hasText: 'This device' })
  await expect(mine).toHaveCount(1)
  const devices = await page.locator('[data-device]').count()
  const id = await mine.getAttribute('data-device')
  await fresh.getByRole('button', { name: /^Less: / }).click()
  const dialog = page.locator('[data-signin="FRESH_SIGN_IN"]')
  await expect(dialog).toBeVisible()
  await expect(dialog).toContainText('A change of sessions.fresh_minutes needs a fresh sign-in')
  expect(await settingsValue(page, 'sessions.fresh_minutes')).toBe(15)
  await shot(page, 'm17-fresh-dialog')

  // There and back: the same page, the nonce taken out of the address, and the same device, though the
  // sign-in brought a new id.
  await dialog.getByRole('button', { name: 'Sign in again' }).click()
  await page.waitForURL((u) => u.hostname === 'crew.test' && u.pathname === '/settings' && !u.searchParams.has('handover'))
  await expect(page.locator('[data-signin]')).toHaveCount(0)
  await expect(page.locator('[data-fresh-state]')).toContainText('Your sign-in is fresh for')
  await expect(page.locator('[data-device]')).toHaveCount(devices)
  await expect(mine).toHaveAttribute('data-device', id!)
  await expect(fresh).toContainText('Asks for a fresh sign-in: yours is fresh for')

  await fresh.getByRole('button', { name: /^Less: / }).click()
  await expect.poll(() => settingsValue(page, 'sessions.fresh_minutes')).toBe(14)
  await fresh.getByRole('button', { name: 'History' }).click()
  await fresh.locator('[data-line]').first().getByRole('button', { name: 'Put back' }).click()
  await expect.poll(() => settingsValue(page, 'sessions.fresh_minutes')).toBe(15)
  await shot(page, 'm17-devices')
})

test('a device signed out on the Devices card is refused on its next request', async ({ page, context, browser }) => {
  const other = await browser.newContext({ baseURL: process.env.CREW_CHECK_URL, ignoreHTTPSErrors: true })
  await signedIn(other, 60, `m17-device-b-${run}`)
  const phone = await other.newPage()
  await phone.goto('/settings')
  const { id } = (await (await phone.request.get('/api/sessions/this')).json()) as { id: number }
  expect(id).toBeGreaterThan(0)

  await signedIn(context, 60, `m17-device-c-${run}`)
  await page.goto('/settings')
  const row = page.locator(`[data-device="${id}"]`)
  await expect(row).toBeVisible()
  await expect(row).not.toContainText('This device')
  await row.getByRole('button', { name: 'Sign out' }).click()
  await expect(row).toHaveCount(0)

  await phone.reload()
  const dialog = phone.locator('[data-signin="SIGNED_OUT"]')
  await expect(dialog).toBeVisible()
  await expect(dialog).toContainText('Signing in again makes it a new device')
  await expect(dialog.getByRole('button', { name: 'Not now' })).toHaveCount(0)
  await shot(phone, 'm17-signed-out')
  await other.close()
})

// Looking after the machine (design 16.10; M17 phase 5): the hours a restart for security updates may
// come in are a setting of the card of gsettings.png, in CREW_TIMEZONE; a range that is none is refused
// under the field, and Put back gives the default again.
test('Looking after the machine: the restart hours, a change, a refusal, and Put back', async ({ page }) => {
  await page.goto('/settings')
  const hours = page.locator('[data-setting="machine_care.restart_hours"]')
  await expect(hours).toContainText('Restarts for security updates come in the hours')
  await expect(hours).toContainText('CREW_TIMEZONE')
  await expect(hours.getByRole('textbox')).toHaveValue('02:00-05:00')
  await shot(page, 'm17-restart-hours')
  await hours.getByRole('textbox').fill('01:00-04:00')
  await hours.getByRole('textbox').press('Enter')
  await expect.poll(() => settingsValue(page, 'machine_care.restart_hours')).toBe('01:00-04:00')
  await hours.getByRole('textbox').fill('25:00-03:00')
  await hours.getByRole('textbox').press('Enter')
  await expect(hours.locator('[data-problem]')).toContainText('is not a range of hours such as 02:00-05:00')
  expect(await settingsValue(page, 'machine_care.restart_hours')).toBe('01:00-04:00')
  await hours.getByRole('button', { name: 'History' }).click()
  const line = hours.locator('[data-line]').first()
  await expect(line).toContainText('the default → 01:00-04:00')
  await line.getByRole('button', { name: 'Put back' }).click()
  await expect(hours.getByRole('textbox')).toHaveValue('02:00-05:00')
  expect(await settingsValue(page, 'machine_care.restart_hours')).toBe('02:00-05:00')
})

// A restart for security updates that waited 7 days (16.10): it holds new runs, the top bar and the
// Projects screen say so, not that the disk is full, and its news is on the Crew level of the feed, as
// inbox.png names that level. The state is set in the throwaway stack's database first (m17-stack.sh
// seed-restart), so the test runs only where CREW_E2E_RESTART says it was.
test('a restart that waited 7 days holds new runs, and its news is on the Crew level', async ({ page }) => {
  test.skip(!process.env.CREW_E2E_RESTART, "needs the restart's wait set in the stack's database")
  await page.goto('/')
  await expect(page.locator('[data-held]')).toHaveText('Restart for updates', { timeout: 15000 })
  const banner = page.locator('[data-hold]')
  await expect(banner).toContainText('A restart for security updates waits since')
  await expect(banner).toContainText('the machine restarts once the running ones finished')
  await expect(banner).not.toContainText('disk')
  const news = page.locator('tr', { hasText: 'has waited 7 days for quiet hours' })
  await expect(news).toContainText('NEWS')
  await expect(news).toContainText('crew level')
  const cert = page.locator('tr', { hasText: 'The certificate of crew.test runs out in 9 days' })
  await expect(cert).toContainText('crew level')
  await shot(page, 'm17-restart-wait')
})

// Kata 4 (design 13.2, 13.10; M17 phase 6). The Machine card's sizes are beside the 1 CPU and 2 GB every
// microVM has, which Kata 4 gives as the overhead of Crew's drop-in, no longer as Kata's base; its
// Isolation is gsettings.png's own words, with no version, which the server cannot read (BUG-002).
test('the Machine card: the sizes beside every microVM, and Isolation as gsettings.png has it', async ({ page }) => {
  await page.goto('/settings')
  const machine = page.locator('section').filter({ has: page.getByRole('heading', { name: 'Machine', exact: true }) })
  await expect(machine).toContainText('Sandbox sizes, beside the 1 CPU and 2 GB every microVM has', { ignoreCase: true })
  await expect(machine).not.toContainText("Kata's base")
  await expect(machine).toContainText('Isolationthe Kata runtime with Cloud Hypervisor')
  await shot(page, 'm17-kata-machine')
})

// A run's page says the size of its microVM, as run.png has it ("4 CPU, 6 GB · its own Docker
// daemon"), and only a large one claims a Docker daemon of its own. The stack has no Docker, so its runs
// are answered by the test, in the shape GET /api/runs/{id} has (runs.Detail).
test("a run's page says the size of its microVM", async ({ page }) => {
  const detail = (id: string, size: 'small' | 'large', sizing: { cpus: number; memMB: number; docker: boolean } | undefined) => ({
    id, subtask: 1, number: 1, title: `A ${size} sandbox`, subtaskState: 'done', state: 'ended', cli: 'agy', crewVersion: '0.0.0-dev',
    image: 'crew-runtime:0.0.0-dev', conversation: 'c', way: 'new', branch: 'main', turn: 1, events: 0, toolSteps: 0, maxTurns: 200,
    tools: true, timeoutSeconds: 3600, tokensIn: 0, tokensOut: 0, tokensCached: 0, warnings: [], startedAt: new Date().toISOString(),
    endedAt: new Date().toISOString(), role: size === 'large' ? 'builder' : 'reviewer', kind: 'build', subtaskNumber: 1, push: false,
    size, sizing, prompt: '', parts: [], command: [], turns: [], runs: [], cliFiles: [],
  })
  const runs: Record<string, unknown> = {
    'a0000000-0000-4000-8000-000000000017': detail('a0000000-0000-4000-8000-000000000017', 'large', { cpus: 4, memMB: 6144, docker: true }),
    'b0000000-0000-4000-8000-000000000017': detail('b0000000-0000-4000-8000-000000000017', 'small', { cpus: 2, memMB: 2048, docker: false }),
  }
  await page.route(/\/api\/runs\/[0-9a-f-]{36}$/, (route) => {
    const id = new URL(route.request().url()).pathname.split('/').pop() ?? ''
    return runs[id] ? route.fulfill({ json: runs[id] }) : route.fulfill({ status: 404, json: { error: { code: 404, status: 'NOT_FOUND', message: 'no such run' } } })
  })
  const sandbox = page.locator('section').filter({ has: page.getByRole('heading', { name: 'Sandbox', exact: true }) })
  await page.goto('/runs/a0000000-0000-4000-8000-000000000017')
  await expect(sandbox).toContainText('Large: 4 CPU, 6 GB · its own Docker daemon')
  await expect(sandbox).toContainText('Kata microVM on Cloud Hypervisor, with its own kernel')
  await shot(page, 'm17-run-large')
  await page.goto('/runs/b0000000-0000-4000-8000-000000000017')
  await expect(sandbox).toContainText('Small: 2 CPU, 2 GB · no Docker daemon')
  await expect(sandbox).not.toContainText('its own Docker daemon')
  await shot(page, 'm17-run-small')
})

// Another installation (design 16.12; M17 phase 7, finding 7 of the review of d8ea5d9): the stack, as
// another machine, names no repository it is built from, so every release waits for Install there and
// the Updates card says so beside auto_install. Once updates.source_repo names one, it says nothing.
test('on another installation the Updates card says a release never installs by itself', async ({ page, context }) => {
  await signedIn(context, 0, `m17-device-u-${run}`)
  await page.goto('/settings')
  const auto = page.locator('[data-setting="updates.auto_install"]')
  const note = 'Never on this installation: it names no repository it is built from, so every release waits for Install.'
  expect(await settingsValue(page, 'updates.source_repo')).toBeNull()
  await expect(auto).toContainText(note)
  await shot(page, 'm17-updates-another')

  const repo = page.locator('[data-setting="updates.source_repo"]')
  await repo.getByRole('textbox').fill('example/crew')
  await repo.getByRole('textbox').press('Enter')
  await expect.poll(() => settingsValue(page, 'updates.source_repo')).toBe('example/crew')
  await expect(auto).not.toContainText(note)

  await repo.getByRole('button', { name: 'History' }).click()
  await repo.locator('[data-line]').first().getByRole('button', { name: 'Put back' }).click()
  await expect.poll(() => settingsValue(page, 'updates.source_repo')).toBeNull()
  await expect(auto).toContainText(note)
})

// A project's export asks for a fresh sign-in (20.3, since 0.70): a stale one opens the dialog, as every
// other refusal does, and a fresh one passes the guard (review of M17's Close, round 2). The stack's
// project is a row with no repository, so the export itself then fails, which is not the guard's.
test("a project's export asks for a fresh sign-in, and a fresh one passes", async ({ page, context }) => {
  await signedIn(context, stale, `m17-device-x-${run}`)
  await page.goto('/p/m17shop/settings')
  const exportIt = page.getByRole('button', { name: 'Export it as files' })
  await exportIt.click()
  await expect(page.locator('[data-signin="FRESH_SIGN_IN"]')).toBeVisible()
  await page.locator('[data-signin]').getByRole('button', { name: 'Not now' }).click()

  await signedIn(context, 0, `m17-device-x-${run}`)
  await page.goto('/p/m17shop/settings')
  const answer = page.waitForResponse((r) => r.url().endsWith('/api/projects/m17shop/export'))
  await exportIt.click()
  expect((await answer).status()).not.toBe(401)
  await expect(page.locator('[data-signin]')).toHaveCount(0)
})
