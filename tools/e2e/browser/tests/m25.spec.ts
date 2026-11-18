import { expect, test, type Page } from '@playwright/test'
import { noDollars, type Screen } from '../gate/nodollars'

function watchErrors(page: Page) {
  const errors: string[] = []
  page.on('pageerror', (e) => {
    if (!e.message.includes('Minified React error #418')) errors.push(`pageerror: ${e.message}`)
  })
  page.on('console', (m) => {
    if (m.type() === 'error' && !m.text().includes('Minified React error #418')) errors.push(`console.error: ${m.text()}`)
  })
  page.on('response', (r) => {
    if (r.url().includes('/api/') && r.status() >= 500) errors.push(`${r.status()} ${r.url()}`)
  })
  return errors
}

test.describe('M25 Visual Control Plane (10 views + 40 themes + storage + mobile)', () => {
  test.skip(!process.env.CREW_M25_E2E, 'runs in standalone M25 Playwright browser verification')

  test('renders all 10 screens with zero console, CSP, 5xx, or nodollars errors', async ({ page }) => {
    const errors = watchErrors(page)
    const screens: Screen[] = [
      { path: '/p/shop/factory', heading: /Factory Line/ },
      { path: '/p/shop/board', heading: /Board/ },
      { path: '/p/shop/specs', heading: /Specs & WORKFLOW/ },
      { path: '/p/shop/chat', heading: /Project Chat/ },
      { path: '/p/shop/skills', heading: /Skills Registry/ },
      { path: '/p/shop/trajectories', heading: /Trajectories/ },
      { path: '/p/shop/agents', heading: /Agents/, words: false },
      { path: '/p/shop/knowledge', heading: /Knowledge/ },
      { path: '/p/shop/tickets/SF-1', heading: /Seed factory cache/, words: false },
      { path: '/storage', heading: /Runtimes & Storage/ },
    ]

    for (const s of screens) {
      await page.goto(s.path)
      await expect(page.getByRole('heading', { level: 1 }).first()).toHaveText(s.heading)
      await page.waitForLoadState('networkidle')
    }

    await page.goto('/p/shop/agents')
    const agentsMain = (await page.locator('main').innerText()).toLowerCase()
    expect(agentsMain).not.toContain('claude')

    await page.goto('/p/shop/specs')
    await expect(page.locator('main')).toContainText('SPEC-000')
    await expect(page.locator('main')).toContainText('SPEC-001')
    await expect(page.locator('main')).toContainText('DEC-001')

    await page.goto('/p/shop/chat')
    await expect(page.locator('main')).toContainText('feat/001-cache-invalidation')

    await page.goto('/p/shop/skills')
    await expect(page.locator('main')).toContainText('cache-hints')
    await expect(page.locator('main')).toContainText('invoice-guard')
    await expect(page.locator('main')).toContainText('vendored-lint')

    await noDollars(page, screens)
    expect(errors, errors.join('\n')).toEqual([])
  })

  test('switches across all 40 themes, toggles light/dark via d, and persists /api/prefs', async ({ page }) => {
    const errors = watchErrors(page)
    await page.goto('/p/shop/factory')
    await expect(page.locator('[data-open-themes]')).toBeVisible()
    await page.locator('[data-open-themes]').click()

    const choices = page.locator('[data-theme-choice]')
    await expect(choices).toHaveCount(40)
    const ids = await choices.evaluateAll((els) => els.map((el) => el.getAttribute('data-theme-choice') || ''))
    expect(ids).toHaveLength(40)

    for (const id of ids) {
      const btn = page.locator(`[data-theme-choice="${id}"]`)
      await btn.scrollIntoViewIfNeeded()
      await btn.click()
      const applied = await page.evaluate(() => document.documentElement.dataset.theme)
      expect(applied).toBe(id)
    }

    await page.locator('[data-theme-choice="linear"]').scrollIntoViewIfNeeded()
    await page.locator('[data-theme-choice="linear"]').click()
    await page.getByRole('button', { name: 'Close' }).first().click()

    const beforeDark = await page.evaluate(() => document.documentElement.classList.contains('dark'))
    await page.keyboard.press('d')
    const afterDark = await page.evaluate(() => document.documentElement.classList.contains('dark'))
    expect(afterDark).toBe(!beforeDark)

    const waitPut = page.waitForResponse((r) => r.url().includes('/api/prefs') && r.request().method() === 'PUT')
    await page.locator('[data-toggle-dark]').click()
    await waitPut

    await page.reload()
    await expect(page.locator('[data-open-themes]')).toBeVisible()
    await expect(async () => {
      const reloadedTheme = await page.evaluate(() => document.documentElement.dataset.theme)
      expect(reloadedTheme).toBe('linear')
    }).toPass()
    const prefRes = await page.request.get('/api/prefs')
    expect(prefRes.status()).toBe(200)
    const prefJson = (await prefRes.json()) as { theme: string; dark: boolean }
    expect(prefJson.theme).toBe('linear')
    expect(prefJson.dark).toBe(beforeDark)
    expect(errors, errors.join('\n')).toEqual([])
  })

  test('keyboard Kanban j/y opens YAML modal, offloads trajectory to BlobStore, and toggles SQLite WAL', async ({ page }) => {
    const errors = watchErrors(page)

    await page.goto('/p/shop/board')
    await expect(page.locator('[data-card="SF-1"]')).toBeVisible()
    await page.keyboard.press('j')
    await page.keyboard.press('y')
    await expect(page.locator('[data-ticket-yaml-modal]')).toContainText('SF-1')
    await page.keyboard.press('Escape')
    await expect(page.locator('[data-ticket-yaml-modal]')).toHaveCount(0)

    await page.goto('/p/shop/trajectories')
    const offloadBtn = page.locator('[data-offload-run]').first()
    await expect(offloadBtn).toBeVisible()
    await offloadBtn.click()
    await expect(page.locator('[data-blob-key]').first()).toBeVisible()

    await page.goto('/storage')
    await expect(page.locator('main')).toContainText('.jsonl.zst')
    await page.locator('[data-set-statedb="sqlite"]').click()
    await expect(page.locator('main')).toContainText('SQLITE')
    await page.locator('[data-checkpoint-wal]').click()
    await page.locator('[data-set-statedb="postgres"]').click()
    await expect(page.locator('main')).toContainText('POSTGRES')
    expect(errors, errors.join('\n')).toEqual([])
  })

  test('390x844 mobile viewports render cleanly with zero horizontal overflow', async ({ page }) => {
    const errors = watchErrors(page)
    await page.setViewportSize({ width: 390, height: 844 })

    for (const path of ['/p/shop/factory', '/p/shop/chat', '/p/shop/skills', '/storage']) {
      await page.goto(path)
      await page.waitForLoadState('networkidle')
      const overflow = await page.evaluate(() => document.documentElement.scrollWidth - window.innerWidth)
      expect(overflow, `horizontal overflow on ${path}`).toBeLessThanOrEqual(0)
    }

    await page.locator('button[aria-label="Open navigation menu"]').click()
    await expect(page.locator('[data-mobile-drawer]')).toBeVisible()
    await page.locator('[data-mobile-drawer] button[aria-label="Close navigation menu"]').click()
    expect(errors, errors.join('\n')).toEqual([])
  })
})
