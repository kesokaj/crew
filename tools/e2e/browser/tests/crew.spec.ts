import { expect, test } from '@playwright/test'
import { admin, adminRole, cookie, token } from './session'

const host = process.env.CREW_HOSTNAME ?? ''

test.describe('without a session', () => {
  test('the UI sends the browser to the sign-in', async ({ page }) => {
    await page.route('https://accounts.google.com/**', (r) => r.abort())
    const resp = await page.request.get('/', { maxRedirects: 0 })
    expect(resp.status()).toBe(302)
    expect(resp.headers()['location']).toContain(`https://auth.${host}/oauth2/google/`)
  })

  test('the API is not reached', async ({ request }) => {
    const resp = await request.get('/api/whoami', { maxRedirects: 0 })
    expect([302, 401, 403]).toContain(resp.status())
    expect(await resp.text()).not.toContain(admin)
  })

  test('the health check is open and says only that it answers', async ({ request }) => {
    const resp = await request.get('/healthz')
    expect(resp.status()).toBe(200)
    const body = await resp.json()
    expect(body).toEqual({ status: 'ok', database: 'ok', version: expect.any(String) })
    expect(resp.headers()['strict-transport-security']).toBe('max-age=31536000')
  })

  test('identity headers sent to the open path vouch for nobody', async ({ request }) => {
    const resp = await request.get('/healthz', {
      headers: { 'X-Caddy-Email': admin, 'X-Crew-Ingress': 'guess', 'X-Token-User-Email': admin },
    })
    expect(resp.status()).toBe(200)
  })

  test('plain HTTP goes to HTTPS', async ({ request }) => {
    const resp = await request.get(`http://${host}/`, { maxRedirects: 0 })
    expect([301, 308]).toContain(resp.status())
    expect(resp.headers()['location']).toBe(`https://${host}/`)
  })
})

test.describe('sessions the ingress refuses', () => {
  for (const [name, t] of [
    ['signed with another key', () => token({ email: admin, roles: [adminRole], key: 'not-the-key' })],
    ['expired', () => token({ email: admin, roles: [adminRole], lifetime: -120 })],
    ['for another account', () => token({ email: 'someone@example.com', roles: ['authp/user'] })],
  ] as const) {
    test(`a session ${name} does not reach the server`, async ({ context }) => {
      await context.addCookies([cookie(t())])
      const resp = await context.request.get('/api/whoami', { maxRedirects: 0 })
      expect(resp.status()).not.toBe(200)
      expect(await resp.text()).not.toContain('"email"')
    })
  }

  test('a session of another account with the admin role is refused by the server', async ({ context }) => {
    // The ingress lets the role through; the server still wants the admin email (20.3).
    await context.addCookies([cookie(token({ email: 'someone@example.com', roles: [adminRole] }))])
    const resp = await context.request.get('/api/whoami', { maxRedirects: 0 })
    expect(resp.status()).toBe(403)
  })
})

// A test that needs a machine nothing was set up on, or that writes what Setup holds, runs only on the
// install test's fresh machine, which sets CREW_FRESH_MACHINE: on a machine in use it would sign the
// operator's git identity in as the test's (it did, on 2026-10-14 and 2026-10-16).
const fresh = process.env.CREW_FRESH_MACHINE === '1'

test.describe('the operator, signed in', () => {
  test.beforeEach(async ({ context }) => {
    await context.addCookies([cookie(token({ email: admin, roles: [adminRole] }))])
  })

  test('the first screen is Setup while no CLI is signed in, with the version and the account', async ({ page }) => {
    test.skip(!fresh, 'only on a fresh machine (CREW_FRESH_MACHINE=1)')
    const errors: string[] = []
    page.on('console', (m) => m.type() === 'error' && errors.push(m.text()))
    const health = await (await page.request.get('/healthz')).json()
    await page.goto('/')
    await expect(page).toHaveURL(/\/setup$/)
    await expect(page.getByRole('heading', { name: 'Setup', exact: true })).toBeVisible()
    await expect(page.locator('header')).toContainText('Crew')
    await expect(page.locator('header')).toContainText(health.version)
    await expect(page.locator('header')).toContainText(admin)
    // The machine check: Docker answers, and Kata is its runtime for sandboxes.
    await expect(page.getByText('Machine check')).toBeVisible()
    await expect(page.getByText(/Kata as the runtime mvm/)).toBeVisible()
    // Nothing the page needed was blocked, by its policy or otherwise.
    expect(errors).toEqual([])
  })

  test('Agent CLIs shows agy, the one CLI, signed out until Setup signs it in', async ({ page }) => {
    await page.goto('/clis')
    await expect(page.getByRole('heading', { name: 'Agent CLIs' })).toBeVisible()
    await expect(page.getByText(/Antigravity CLI · agy/)).toBeVisible()
    await expect(page.getByText(/Claude Code/)).toHaveCount(0)
    await expect(page.getByRole('button', { name: 'Check now' })).toHaveCount(1)
  })

  // The whole way of Setup, with the step that needs no account: the terminal through the ingress,
  // the setup microVM, crew-setup in it, the collection into the database, and the microVM removed.
  test('Setup signs in the git identity in a terminal of the setup microVM', async ({ page }) => {
    test.skip(!fresh, 'only on a fresh machine (CREW_FRESH_MACHINE=1): it writes the git identity')
    test.setTimeout(180_000)
    const errors: string[] = []
    page.on('console', (m) => m.type() === 'error' && errors.push(m.text()))
    await page.goto('/setup')
    const row = page.locator('li', { hasText: 'Git identity' })
    await row.getByRole('button', { name: /Set up/ }).click()
    const term = page.locator('.xterm')
    await expect(term).toContainText('your name:', { timeout: 120_000 })
    await page.keyboard.type('Ada Test\r')
    await expect(term).toContainText('your mail address:')
    await page.keyboard.type('ada@example.com\r')
    await expect(page.getByText('Git identity: collected for ada@example.com')).toBeVisible({ timeout: 60_000 })
    await expect(row).toContainText('ada@example.com')
    // The ended step keeps its terminal on the page, with what it printed last, until it is closed.
    await expect(term).toContainText('Crew collects this now')
    await page.getByRole('button', { name: 'Close' }).click()
    await expect(page.getByText(/^Not running/)).toBeVisible()
    // The terminal's own style elements were let in by the page's nonce; nothing was blocked.
    expect(errors).toEqual([])
    const st = await (await page.request.get('/api/setup')).json()
    expect(st.running).toBe('')
    expect(st.credentials.find((c: { id: string }) => c.id === 'git-identity')).toMatchObject({ set: true, account: 'ada@example.com' })
  })

  // A sign-in prints a link and asks for a code: the link opens by a click in the terminal, and a
  // code reaches the step by Ctrl+V and by the line field. The step is stopped, so nothing is stored.
  test('the terminal of Setup shows its links and takes a pasted code', async ({ page, context }) => {
    test.setTimeout(180_000)
    await context.grantPermissions(['clipboard-read', 'clipboard-write'])
    const before = await (await page.request.get('/api/setup')).json()
    await page.goto('/setup')
    await page.locator('li', { hasText: 'gcloud' }).getByRole('button', { name: /Set up/ }).click()
    const term = page.locator('.xterm')
    await expect(term).toContainText('verification code', { timeout: 120_000 })
    // The link is opened by a click where it is printed, in a tab of its own.
    const row = term.locator('.xterm-rows > div', { hasText: 'https://accounts.google.com/' }).first()
    const box = await row.boundingBox()
    if (!box) throw new Error('the link is not on the screen')
    await page.mouse.move(box.x + 40, box.y + box.height / 2)
    const [tab] = await Promise.all([page.waitForEvent('popup'), page.mouse.click(box.x + 40, box.y + box.height / 2)])
    expect(tab.url()).toMatch(/^https:\/\/accounts\.google\.com\//)
    await tab.close()
    await page.evaluate(() => navigator.clipboard.writeText('pasted-by-ctrl-v'))
    await term.click()
    await page.keyboard.press('Control+V')
    await expect(term).toContainText('pasted-by-ctrl-v')
    await page.getByLabel('A line for the terminal').fill('sent-by-the-field')
    await page.getByRole('button', { name: 'Send', exact: true }).click()
    await expect(term).toContainText('sent-by-the-field')
    await page.getByRole('button', { name: 'Stop and remove it' }).click()
    // Kata takes about half a minute to remove a microVM.
    await expect(page.getByText(/^Not running/)).toBeVisible({ timeout: 90_000 })
    const after = await (await page.request.get('/api/setup')).json()
    expect(after.running).toBe('')
    const g = (st: { credentials: { id: string; updatedAt?: string }[] }) => st.credentials.find((c) => c.id === 'gcloud')
    expect(g(after)).toEqual(g(before))
  })

  test('the terminal of Setup is refused to a page of another address', async ({ context }) => {
    const resp = await context.request.get('/api/setup/terminal?step=git', {
      headers: {
        Origin: 'https://evil.example.com',
        Connection: 'Upgrade',
        Upgrade: 'websocket',
        'Sec-WebSocket-Version': '13',
        'Sec-WebSocket-Key': 'dGhlIHNhbXBsZSBub25jZQ==',
      },
    })
    expect(resp.status()).toBe(403)
  })

  test('a route the UI does not have loads the UI, which says so', async ({ page }) => {
    const resp = await page.goto('/projects/some/board')
    expect(resp?.status()).toBe(200)
    await expect(page.getByRole('heading', { name: 'Not here yet' })).toBeVisible()
    await expect(page.locator('header')).toContainText(admin)
  })

  test('the UI is served with its security headers, and a fresh nonce for its scripts', async ({ context }) => {
    const first = (await context.request.get('/')).headers()
    const second = (await context.request.get('/')).headers()
    const csp = first['content-security-policy']
    expect(csp).toContain("default-src 'self'")
    expect(csp).toContain("frame-ancestors 'none'")
    expect(csp).toMatch(/script-src 'nonce-[\w-]+' 'strict-dynamic'/)
    expect(csp).not.toContain('unsafe-inline')
    expect(second['content-security-policy']).not.toBe(csp)
    expect(first['x-content-type-options']).toBe('nosniff')
  })

  test('the server sees the account the session names, whatever the browser sends', async ({ context }) => {
    const resp = await context.request.get('/api/whoami', {
      headers: { 'X-Caddy-Email': 'intruder@example.com', 'X-Crew-Ingress': 'guess', 'X-Token-User-Email': 'intruder@example.com' },
    })
    expect(resp.status()).toBe(200)
    expect((await resp.json()).email).toBe(admin)
  })

  test('an unknown endpoint answers in the error shape', async ({ context }) => {
    const resp = await context.request.get('/api/nothing-here')
    expect(resp.status()).toBe(404)
    expect((await resp.json()).error).toMatchObject({ code: 404, status: 'NOT_FOUND' })
  })

  test('Runs lists the runs, and offers a run on the fixture with agy', async ({ page }) => {
    const errors: string[] = []
    page.on('console', (m) => m.type() === 'error' && errors.push(m.text()))
    await page.goto('/runs')
    await expect(page.getByRole('heading', { name: 'Runs', exact: true })).toBeVisible()
    await expect(page.getByRole('heading', { name: /Running now/ })).toBeVisible()
    await expect(page.getByText('Start a run on the fixture project')).toBeVisible()
    await expect(page.getByRole('button', { name: /^agy/ })).toBeVisible()
    await expect(page.getByRole('button', { name: /^claude/ })).toHaveCount(0)
    // Nothing starts without a prompt.
    await expect(page.getByRole('button', { name: 'Start the run' })).toBeDisabled()
    const list = await (await page.request.get('/api/runs')).json()
    expect(list).toMatchObject({ running: expect.any(Array), waiting: expect.any(Array), finished: expect.any(Array) })
    expect(errors).toEqual([])
  })

  test('a run that does not exist says so', async ({ page }) => {
    await page.goto('/runs/f1c50000-0000-4000-8000-00000000dead')
    await expect(page.getByText('no such run')).toBeVisible()
    const resp = await page.request.get('/api/runs/not-a-run')
    expect(resp.status()).toBe(404)
  })

  test('a run is started, stopped, and continued only from a page of Crew', async ({ page }) => {
    await page.goto('/runs')
    for (const [path, body] of [
      ['/api/runs', { cli: 'agy', prompt: 'x' }],
      ['/api/runs/f1c50000-0000-4000-8000-00000000dead/stop', {}],
      ['/api/subtasks/1/continue', {}],
    ] as const) {
      const foreign = await page.request.post(path, { data: body, headers: { Origin: 'https://evil.example' } })
      expect(foreign.status()).toBe(403)
      const bare = await page.request.post(path, { data: body })
      expect(bare.status()).toBe(403)
    }
  })

  test('the settings are read, and changed only from a page of Crew', async ({ page }) => {
    await page.goto('/')
    const before = await (await page.request.get('/api/settings/machine')).json()
    expect(before.disk.clean_at).toBeGreaterThan(0)

    const changed = { ...before, disk: { ...before.disk, clean_at: 70 } }
    // From another site, and with no origin at all: refused.
    const foreign = await page.request.put('/api/settings/machine', {
      data: changed,
      headers: { Origin: 'https://evil.example' },
    })
    expect(foreign.status()).toBe(403)
    const bare = await page.request.put('/api/settings/machine', { data: changed })
    expect(bare.status()).toBe(403)

    // From the page itself, as the UI will send it.
    const result = await page.evaluate(async (body) => {
      const r = await fetch('/api/settings/machine', { method: 'PUT', body: JSON.stringify(body), headers: { 'Content-Type': 'application/json' } })
      return { status: r.status, body: await r.json() }
    }, changed)
    expect(result.status).toBe(200)
    expect(result.body.disk.clean_at).toBe(70)

    // A value that does not validate is refused, and changes nothing.
    const invalid = await page.evaluate(async (body) => {
      const r = await fetch('/api/settings/machine', { method: 'PUT', body: JSON.stringify(body) })
      return { status: r.status, body: await r.json() }
    }, { ...before, disk: { clean_at: 99, stop_at: 95 } })
    expect(invalid.status).toBe(400)
    const after = await (await page.request.get('/api/settings/machine')).json()
    expect(after.disk.clean_at).toBe(70)

    // Put it back.
    const back = await page.evaluate(async (body) => (await fetch('/api/settings/machine', { method: 'PUT', body: JSON.stringify(body) })).status, before)
    expect(back).toBe(200)
  })
})

test('the ingress limits requests from one address', async ({ request }) => {
  const codes = await Promise.all(Array.from({ length: 250 }, () => request.get('/healthz').then((r) => r.status())))
  // The ingress counts an address's requests over one second (ingress/generator.go, window 1s), and it
  // counted each request it let through before that request's answer came. Wait the second out, even
  // when an expect below fails: the next test's page, its script and its writes, would get 429 within it.
  await new Promise((r) => setTimeout(r, 1500))
  expect(codes).toContain(429)
  expect(codes).toContain(200)
})
