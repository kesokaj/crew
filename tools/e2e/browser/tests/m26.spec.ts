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

test.describe('M26 Memory Dreamstore, Scoped Dreaming, Knowledge API, Skills Registry & Hardened Chat Rooms', () => {
  test.skip(!process.env.CREW_M25_E2E && !process.env.CREW_M26_E2E, 'runs in standalone Software Factory Playwright browser verification')

  test('verifies Dreamstore validity & quarantine filter, Scoped Dreaming, Knowledge API fencing, and Skills Registry', async ({ page }) => {
    const errors = watchErrors(page)

    await page.goto('/p/shop/knowledge')
    await expect(page.getByRole('heading', { level: 1 }).first()).toHaveText(/Knowledge/)
    await expect(page.locator('main [style]')).toHaveCount(0)

    const d1 = page.getByTestId('dreamstore-entry-1')
    const d2 = page.getByTestId('dreamstore-entry-2')
    await expect(d1).toContainText('D0001')
    await expect(d1).toContainText('1.00')
    await expect(page.getByTestId('dreamstore-bar-1')).toHaveAttribute('width', '100')
    await expect(page.getByTestId('dreamstore-bar-1')).toHaveAttribute('data-validity', '1.00')

    await expect(d2).toContainText('D0002')
    await expect(d2).toContainText('quarantined')
    await expect(page.getByTestId('dreamstore-bar-2')).toBeVisible()

    const qFilter = page.getByTestId('dreamstore-quarantine-filter')
    await expect(qFilter).toBeChecked()
    await qFilter.uncheck()
    await expect(d2).toHaveCount(0)
    await expect(d1).toBeVisible()
    await qFilter.check()
    await expect(d2).toBeVisible()

    const prop1 = page.getByTestId('dream-proposal-DRM-001')
    await expect(prop1).toContainText('vet BLOCKED')
    await expect(page.getByTestId('dream-approve-DRM-001')).toHaveCount(0)

    const prop2 = page.getByTestId('dream-proposal-DRM-002')
    await expect(prop2).toContainText('vet PASS')
    await expect(prop2).toContainText('pending_approval')
    await page.getByTestId('dream-approve-DRM-002').click()
    await expect(page.locator('main')).toContainText('DRM-002 approved onto main.')
    await expect(prop2).toContainText('DRM-002 · approved')

    await expect(page.locator('main')).toContainText('Idempotent webhook retry guard')
    await expect(page.locator('main')).toContainText('Transactional outbox relay pattern')
    await expect(page.locator('main')).toContainText('From billing')
    await expect(page.locator('main')).toContainText('scrubbed · fenced')
    const fenced = page.getByTestId('knowledge-fenced-KNOW-001')
    await expect(fenced).toContainText('<crew_data_fence')
    await expect(fenced).not.toContainText('redacted-dsn-val')

    await page.getByTestId('knowledge-apply-KNOW-001').click()
    await expect(page.locator('main')).toContainText('Minted Tasks API item')

    await page.goto('/p/shop/skills')
    await expect(page.getByRole('heading', { level: 1 }).first()).toHaveText(/Skills Registry/)
    await expect(page.locator('main [style]')).toHaveCount(0)

    await expect(page.getByTestId('skill-verdict-unpinned-helper')).toHaveText('BLOCKED')
    await expect(page.getByTestId('skill-findings-unpinned-helper')).toBeVisible()
    await expect(page.getByTestId('skill-verdict-vendored-sql-lint')).toHaveText('PASS')
    await expect(page.getByTestId('skill-verdict-vendored-lint')).toHaveText('PASS')

    const manifest = page.getByTestId('vendored-manifest')
    await expect(manifest).toContainText('vendored-lint')
    await expect(manifest).toContainText('abc12345')
    await expect(manifest).toContainText('MIT')

    await page.getByTestId('skill-card-cache-hints').getByRole('button', { name: 'Inspect' }).click()
    await expect(page.locator('main')).toContainText('SKILL.md · cache-hints')
    await expect(page.locator('main')).toContainText('Verify TTL.')

    expect(errors, errors.join('\n')).toEqual([])
  })

  test('verifies keyed ChoicePrompt lifecycle, posted_nobody_woken, @security, @user-only interrupt, and noDollars', async ({ page }) => {
    const errors = watchErrors(page)

    await page.goto('/p/shop/chat')
    await expect(page.getByRole('heading', { level: 1 }).first()).toHaveText(/Project Chat/)
    await expect(page.locator('main [style]')).toHaveCount(0)

    await page.locator('[data-thread="feat/001-cache-invalidation"]').click()
    await expect(page.locator('main')).toContainText('crew_data_fence')

    const v1Card = page.getByTestId('choice-card-cache-eviction-policy-v1')
    await expect(v1Card).toContainText('v1')
    await expect(v1Card).toContainText('Superseded')
    await expect(page.getByTestId('choice-opt-cache-eviction-policy-lru')).toBeDisabled()

    const v2Card = page.getByTestId('choice-card-cache-eviction-policy-v2')
    await expect(v2Card).toContainText('v2')
    await page.getByTestId('choice-opt-cache-eviction-policy-tinylfu').click()
    await expect(page.getByTestId('chat-delivery-notice')).toContainText('Recorded direct choice decision DEC-002 on cache-eviction-policy (v2).')
    await expect(v2Card).toContainText('direct · @user')
    await expect(v2Card).toContainText('[selected]')

    await page.goto('/p/shop/specs')
    await expect(page.locator('main')).toContainText('DEC-002')

    await page.goto('/p/shop/chat')
    const senderSelect = page.getByLabel('Sender role')
    await expect(senderSelect.locator('option[value="security"]')).toHaveText('@security')

    const interruptToggle = page.getByTestId('chat-interrupt-toggle')
    await expect(interruptToggle).toBeEnabled()
    await interruptToggle.check()
    await expect(interruptToggle).toBeChecked()

    await senderSelect.selectOption('security')
    await expect(interruptToggle).toBeDisabled()
    await expect(interruptToggle).not.toBeChecked()

    await page.getByLabel('Message intent').selectOption('inform')
    await page.getByTestId('chat-composer-input').fill('Security review note for builder without mention')
    await page.getByTestId('chat-send-btn').click()
    await expect(page.getByTestId('chat-delivery-notice')).toHaveText('Posted to room; nobody woken.')

    const surfaces: Screen[] = [
      { path: '/p/shop/knowledge', heading: /Knowledge/ },
      { path: '/p/shop/skills', heading: /Skills Registry/ },
      { path: '/p/shop/chat', heading: /Project Chat/ },
      { path: '/p/shop/specs', heading: /Specs & WORKFLOW/ },
      { path: '/p/shop/factory', heading: /Factory Line/ },
      { path: '/storage', heading: /Runtimes & Storage/ },
    ]
    await noDollars(page, surfaces)
    expect(errors, errors.join('\n')).toEqual([])
  })
})
