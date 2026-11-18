import { expect, test, type BrowserContext } from '@playwright/test'
import { shot } from './helpers'

// Notifications by mail (design 18; M18). They store a mail account and send, so they run only where
// CREW_E2E_WRITES is set: the throwaway stack of M17's D28, whose mail goes to 127.0.0.1:9, where
// nothing answers (plan D30), never dave's own installation.
test.skip(!process.env.CREW_E2E_WRITES, 'stores a mail account: only on a throwaway installation')

// The two headers the ingress takes from the sign-in come from two cookies on the stack (M17 phase 4).
async function signedIn(context: BrowserContext, ageSeconds: number, sid: string) {
  const now = Math.floor(Date.now() / 1000)
  await context.addCookies([
    { name: 'm17_auth', value: String(now - ageSeconds), domain: '.crew.test', path: '/', secure: true },
    { name: 'm17_sid', value: sid, domain: '.crew.test', path: '/', secure: true },
  ])
}

const stale = 20 * 60
const run = Date.now()
const password = 'made up app password'

// Setup's Mail (18.4, 20.3): the account and its app password are stored only behind a fresh sign-in,
// the password is never shown again, and a test message says whether it went.
test("Setup's mail account: a fresh sign-in to store it, never shown again, and a test message", async ({ page, context }) => {
  await signedIn(context, stale, `m18-device-${run}`)
  await page.goto('/setup')
  const form = page.locator('[data-mail-form]')
  await form.getByLabel('Account').fill('crew@example.test')
  await form.getByLabel('App password').fill(password)
  await form.getByRole('button', { name: 'Store' }).click()
  await expect(page.locator('[data-signin="FRESH_SIGN_IN"]')).toBeVisible()
  await page.locator('[data-signin]').getByRole('button', { name: 'Not now' }).click()
  expect((await (await page.request.get('/api/setup/mail')).json()).state).toBe('none')

  await signedIn(context, 0, `m18-device-${run}`)
  await page.goto('/setup')
  await form.getByLabel('Account').fill('crew@example.test')
  await form.getByLabel('App password').fill(password)
  await form.getByRole('button', { name: 'Store' }).click()
  await expect(form).toContainText('Stored.')
  await expect(form.getByLabel('App password')).toHaveValue('')
  await expect(page.locator('[data-mail-state]')).toContainText('crew@example.test, stored; nothing sent with it yet')
  const account = await page.request.get('/api/setup/mail')
  expect(await account.text()).not.toContain(password)

  await form.getByRole('button', { name: 'Send a test message' }).click()
  await expect(form).toContainText('The test message did not go')
  await shot(page, 'm18-setup-mail')
  const sent = await (await page.request.get('/api/notifications')).json()
  expect(sent.sent[0].kind).toBe('test')
  expect(sent.sent[0].state).toBe('failed')
  expect(sent.sent[0].error).toContain('127.0.0.1:9')
  expect(JSON.stringify(sent)).not.toContain(password)
})

type Sent = { sent: { item: string | null; kind: string; links: { label: string; url: string }[] }[] }

// A mail's link chooses, Confirm decides (18.3; recovery row 77): the link of Give the OK, as the
// notifier wrote it, opens the inbox with it chosen; opening every link of the mail changes nothing;
// an alternative that is not offered is said so; Confirm gives the OK; the same link again shows what
// was decided, by whom and when. An item that was mailed says so (inbox.png). It needs the stack's
// seed-machine and seed-ok.
test('a link chooses and Confirm decides', async ({ page, context }) => {
  test.skip(!process.env.CREW_E2E_OK, 'needs m17-stack.sh seed-machine and seed-ok')
  await signedIn(context, 0, `m18-device-ok-${run}`)
  let links: { label: string; url: string }[] = []
  await expect
    .poll(
      async () => {
        const r: Sent = await (await page.request.get('/api/notifications')).json()
        links = r.sent.find((m) => m.item === 'ok/m18shop' && m.kind === 'needs_you')?.links ?? []
        return links.length
      },
      { timeout: 45_000 },
    )
    .toBe(3)
  const ok = links.find((l) => l.label === 'Give the OK')
  expect(ok?.url).toBe('https://crew.test/inbox?choose=Give+the+OK&item=ok%2Fm18shop')
  const path = (u: string) => new URL(u).pathname + new URL(u).search
  // A mail scanner opens every link: nothing is decided.
  for (const l of links) await page.request.get(path(l.url))
  const item = async () => (await (await page.request.get('/api/inbox/item?id=ok%2Fm18shop')).json()) as { item: unknown; decisions: unknown[] }
  expect((await item()).item).not.toBeNull()

  await page.goto('/inbox?item=ok%2Fm18shop&choose=Install')
  await expect(page.locator('[data-not-offered]')).toContainText('Install is no longer offered here')

  await page.goto(path(ok!.url))
  const chosen = page.locator('[data-chosen]')
  await expect(chosen).toContainText('Give the OK is chosen. Nothing happens until you confirm.')
  await expect(chosen.getByRole('button', { name: 'Read the plan' })).toHaveCount(0)
  await shot(page, 'm18-inbox-chosen')
  expect((await item()).decisions).toHaveLength(0)
  await chosen.getByRole('button', { name: 'Confirm: Give the OK' }).click()
  await expect(page.locator('[data-chosen-gone]')).toContainText('Decided: Give the OK, by admin@example.test at')

  await page.goto(path(ok!.url))
  await expect(page.locator('[data-chosen-gone]')).toContainText('Decided: Give the OK, by admin@example.test at')
  await shot(page, 'm18-inbox-decided')

  await page.goto('/inbox')
  await expect(page.locator('[data-item="db-writable"] [data-notified]')).toContainText(/Notified by mail at \d\d:\d\d, with these alternatives/)
  await shot(page, 'm18-inbox-notified')
})

// The Notifications screen (18, notify.png): the menu's entry before Machine settings; the cards Mail,
// What is sent and when, and Web push for later; Google Chat off; Send a test message with what came
// of it; the Sent list with each message's state. No dollar, no claude, no hosted project (0.42, 0.53,
// D28). On the stack nothing answers at 127.0.0.1:9, so the test message does not go.
test('the Notifications screen', async ({ page, context }) => {
  await signedIn(context, 0, `m18-device-n-${run}`)
  await page.goto('/setup')
  const stored = await page.evaluate(async () => {
    const r = await fetch('/api/setup/mail', {
      method: 'PUT',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ account: 'crew@example.test', password: 'made up app password' }),
    })
    return r.status
  })
  expect(stored).toBe(200)
  await page.goto('/notifications')
  await expect(page.getByRole('heading', { name: 'Notifications', level: 1 })).toBeVisible()
  const menu = page.locator('nav')
  await expect(menu.getByRole('link', { name: 'Notifications' })).toBeVisible()
  const order = await menu.getByRole('link').allInnerTexts()
  expect(order.indexOf('Notifications')).toBe(order.indexOf('Machine settings') - 1)
  for (const card of ['Mail', 'What is sent, and when', 'Web push, to a phone', 'Answering takes two presses']) {
    await expect(page.getByRole('heading', { name: card, exact: true })).toBeVisible()
  }
  await expect(page.locator('[data-mail-card]')).toContainText('admin@example.test')
  await expect(page.locator('[data-mail-card]')).toContainText('Crew, sent through crew@example.test with an app password')
  await expect(page.getByText('Google Chat')).toBeVisible()
  await page.getByRole('button', { name: 'Send a test message' }).click()
  await expect(page.locator('[data-test-said]')).toContainText('The test message did not go')
  const first = page.locator('[data-sent] tbody tr').first()
  await expect(first).toContainText('test message')
  await expect(first.locator('[data-state]')).toContainText('failed: reaching the mail server')
  const text = (await page.locator('main').innerText()).toLowerCase()
  expect(text).not.toMatch(/\$\s?\d|budget|claude|hosted project/)
  await shot(page, 'm18-notifications')
})
