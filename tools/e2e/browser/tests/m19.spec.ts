import { expect, test } from '@playwright/test'
import { shot } from './helpers'

// The Hosting screen (design 19, host.png; plan M19 E2E 7). It reads a project the throwaway stack of
// M17's D28 seeds as hosted (m17-stack.sh seed-hosting), so it runs only where CREW_E2E_WRITES is set.
test.skip(!process.env.CREW_E2E_WRITES, 'reads the seeded m19shop: only on a throwaway installation')

test('Hosting shows the address, who may open it, what runs, Follow main, the deploys and the logs', async ({ page }) => {
  await page.goto('/p/m19shop/hosting')
  await expect(page.getByRole('heading', { name: 'Hosting', exact: true })).toBeVisible()
  await expect(page.locator('[data-address]')).toHaveText('https://m19shop.apps.test')
  const access = page.locator('[data-access]')
  for (const choice of ['Only the admin email', 'Listed addresses', 'Any Google account', 'Public']) {
    await expect(access).toContainText(choice)
  }
  await expect(access.locator('[data-selected="true"]')).toHaveText('Only the admin email')
  const running = page.locator('section', { hasText: 'Running' })
  await expect(running).toContainText('✓ Healthy')
  for (const row of ['f975247 · deploy 2 · 3 tickets in it', 'crew-host/0f8fad5b-d9cb-469f-a165-70867728950e:2', 'crew-host-0f8fad5bd9cb-2',
    '/healthz answers. Restarted 0 times.', 'Live since', '/data, kept across deploys and in the backups', 'No CLI login, and no agent.',
    'the internet, from inside']) {
    await expect(running).toContainText(row)
  }
  const current = page.locator('section', { hasText: 'Keeping it current' })
  await expect(current).toContainText('Follow main')
  await expect(current).toContainText('The hosted version changes only when you press Deploy again.')
  await expect(current).toContainText('the last 3 images, for rolling back')
  const deploys = page.locator('[data-deploys]')
  await expect(deploys).toContainText('✓ live')
  await expect(deploys).toContainText('replaced')
  await expect(deploys).toContainText('failed')
  await expect(deploys).toContainText('/healthz did not answer in 2m0s')
  await expect(deploys).toContainText('MU-4, MU-5')
  await expect(page.getByRole('button', { name: 'Deploy again' })).toBeEnabled()
  await expect(page.getByRole('button', { name: 'Stop hosting' })).toBeVisible()
  await expect(page.locator('[data-logs]')).toContainText('GET /healthz 200')
  await expect(page.getByRole('link', { name: 'Hosting', exact: true })).toBeVisible()
  const text = (await page.locator('main, body').first().innerText()).toLowerCase()
  expect(text).not.toContain('claude')
  expect(text).not.toContain('$')
  await shot(page, 'm19-hosting')
})
