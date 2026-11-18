import { expect, test } from '@playwright/test'
import { admin, adminRole, cookie, token } from './session'
import { api, shot } from './helpers'
import { copyLines, type Copies, waiting } from '../gate/copies'
import { gateLines, type Request, shownRequests } from '../gate/lastchanges'

type Release = {
  version: string
  state: string
  kind: string
  rollBack: boolean
  restores: boolean
  images: Record<string, string>
  pins: Record<string, string>
  schema: number
}
type Screen = {
  running?: Release
  next?: Release
  updater: { answers: boolean; running: string; version: string }
  releases: Release[]
  requests: { id: number; version: string; state: string }[]
  candidate?: string
}

// The screens of M14 (design 16.1 to 16.5, 16.9, versions.png, db.png): the Versions screen with the
// running version, the next release, the updater and its last changes, every release with Roll back;
// and the Database screen's backups. They read: no button that installs, holds, or rolls back is
// pressed here.
test.describe('the screens of M14', () => {
  test.use({ viewport: { width: 1280, height: 900 } })
  test.beforeEach(async ({ context }) => {
    await context.addCookies([cookie(token({ email: admin, roles: [adminRole] }))])
  })

  test('Versions shows the running version, the next release, the updater, and every release', async ({ page }) => {
    const v = (await api(page, '/api/crew/versions')) as Screen
    expect(v.updater.answers).toBe(true)
    await page.goto('/versions')
    await expect(page.getByRole('heading', { name: 'Versions', exact: true })).toBeVisible()
    await expect(page.getByText('Running now', { exact: true })).toBeVisible()
    await expect(page.locator('[data-running]')).toHaveText(`Crew ${v.running?.version ?? v.updater.running}`)
    if (v.running) {
      await expect(page.getByText(v.running.images['crew-server'], { exact: true })).toBeVisible()
      // When it was installed, never a dash.
      await expect(page.locator('dt', { hasText: 'Since' }).locator('xpath=following-sibling::dd[1]')).not.toHaveText('—')
      await expect(page.getByText(`${v.running.schema}`, { exact: true }).first()).toBeVisible()
    }
    await expect(page.getByText('Next release', { exact: true })).toBeVisible()
    if (v.next) {
      await expect(page.locator(`[data-next="${v.next.version}"]`).getByText(`Crew ${v.next.version}`)).toBeVisible()
      await expect(page.getByRole('button', { name: 'Install now' })).toBeVisible()
      if (v.next.kind === 'code') await expect(page.getByText('Waits for Install')).toBeVisible()
    } else {
      // Only that: a release is built by Crew too since 0.52, not only by crew-ctl build.
      await expect(page.getByText('No release waits.', { exact: true })).toBeVisible()
    }
    // The gate of the candidate, a build or a gate newer than the running release, is a panel of its
    // own; with none, there is no panel (16.2 step 6, since 0.52).
    if (v.candidate) {
      const gate = page.locator(`[data-gate="${v.candidate}"]`)
      await expect(gate.getByText(`The gate of ${v.candidate}`, { exact: true })).toBeVisible()
      await expect(gate.locator('[data-gate-state]')).not.toBeEmpty()
    } else {
      await expect(page.locator('[data-gate]')).toHaveCount(0)
    }
    await expect(page.getByText('Updater', { exact: true })).toBeVisible()
    await expect(page.getByText('Last changes', { exact: true })).toBeVisible()
    await expect(page.getByRole('heading', { name: /Releases/ })).toBeVisible()
    for (const r of v.releases) {
      const row = page.locator(`[data-release="${r.version}"]`)
      await expect(row).toBeVisible()
      if (r.version === v.running?.version) await expect(row.getByText('✓ running')).toBeVisible()
      if (r.rollBack) await expect(row.getByRole('button', { name: 'Roll back to this' })).toBeVisible()
    }
    for (const card of ['What an update does', 'What a roll back does', 'What protects the way back']) {
      await expect(page.getByText(card, { exact: true })).toBeVisible()
    }
    // The menu names the release that waits.
    const menu = page.getByRole('navigation').getByRole('link', { name: /Versions/ })
    await expect(menu).toBeVisible()
    if (v.next) await expect(menu).toContainText(v.next.version)
    await shot(page, 'versions')
  })

  test("the Updater card words a gate's request as a gate, never as an install", async ({ page }) => {
    const { requests } = (await api(page, '/api/crew/versions')) as { requests: Request[] }
    test.skip(!shownRequests(requests).some((q) => q.action === 'gate' || q.action === 'check'), 'no gate or check among the last changes')
    await page.goto('/versions')
    await gateLines(page, requests)
  })

  test('Versions never says a release is being copied to GitHub while copying is off', async ({ page }) => {
    const v = (await api(page, '/api/crew/versions')) as Copies
    test.skip(v.publish === true, 'releases are copied to GitHub here')
    test.skip(waiting(v).length === 0, 'no release that passed a gate waits for a copy')
    await page.goto('/versions')
    await copyLines(page, v)
  })

  test('a roll back that reads a snapshot back asks for the version name first, and nothing is sent before', async ({ page }) => {
    const v = (await api(page, '/api/crew/versions')) as Screen
    const r = v.releases.find((x) => x.rollBack && x.restores)
    test.skip(!r, 'no kept release whose roll back reads a snapshot back')
    const sent: string[] = []
    page.on('request', (q) => {
      if (q.method() !== 'GET' && q.url().includes('/api/crew/')) sent.push(q.url())
    })
    await page.goto('/versions')
    const row = page.locator(`[data-release="${r!.version}"]`)
    await row.getByRole('button', { name: 'Roll back to this' }).click()
    await expect(row.getByText('everything written since is lost')).toBeVisible()
    await expect(row.getByRole('button', { name: 'Roll back', exact: true })).toBeDisabled()
    expect(sent).toEqual([])
  })

  // The daily look (12.8, since 0.56): its newest look, and Look now, which asks first and sends
  // nothing before; a server that does not look says so (the gate's check server is the gate's own
  // test, gate/screens.spec.ts). Look now itself makes tickets on Crew's project, so the test never
  // presses it through.
  test('Versions shows the daily look, and Look now asks first and sends nothing before', async ({ page }) => {
    const pins = (await api(page, '/api/crew/pins')) as { look: { at: string } | null; looks: boolean }
    const sent: string[] = []
    page.on('request', (q) => {
      if (q.method() !== 'GET' && q.url().includes('/api/crew/pins')) sent.push(q.url())
    })
    await page.goto('/versions')
    const card = page.locator('[data-daily-look]')
    await expect(card.getByRole('heading', { name: 'The daily look' })).toBeVisible()
    await expect(card.getByText(pins.look ? /^The last look was/ : 'No look yet.')).toBeVisible()
    if (!pins.looks) {
      await expect(card.getByText('The daily look does not run on this server.')).toBeVisible()
      return
    }
    await card.getByRole('button', { name: 'Look now…' }).click()
    await expect(card.getByText(/makes a ticket on Crew's project for each new version/)).toBeVisible()
    await card.getByRole('button', { name: 'Cancel' }).click()
    await expect(card.getByRole('button', { name: 'Look now…' })).toBeVisible()
    expect(sent).toEqual([])
  })

  // Every release measured (10.7, since 0.57): each release installed has its line under its results,
  // the measured ones in Crew's words with the six numbers under them, the others the tickets they
  // wait for. A worse one would be news too, which this test does not make.
  test('Versions shows each installed release measured against the one it replaced', async ({ page }) => {
    const v = (await api(page, '/api/crew/versions')) as {
      measureAfter: number
      releases: { version: string; installedAt?: string; measure?: { verdict: string; before?: string; why: string; results?: unknown[] } | null }[]
    }
    const installed = v.releases.filter((r) => r.installedAt)
    expect(installed.length).toBeGreaterThan(0)
    await page.goto('/versions')
    for (const r of installed) {
      const line = page.locator(`[data-release="${r.version}"] [data-measure]`)
      const m = r.measure
      if (!m) await expect(line).toHaveText(`measured once ${v.measureAfter} tickets ran on it`)
      else if (m.verdict === 'not_compared') await expect(line).toHaveText(`not compared: ${m.why}`)
      else if (m.verdict === 'worse') await expect(line).toContainText(`worse than ${m.before}: `)
      else await expect(line).toContainText(m.why)
      if (m && m.verdict !== 'not_compared') {
        await line.getByText('the six numbers').click()
        await expect(line.locator('li')).toHaveCount(6)
      }
    }
  })

  test('the Database screen names the day’s tar.gz, the month files, and the snapshot', async ({ page }) => {
    const db = (await api(page, '/api/database')) as { backups: string[] }
    const days = db.backups.filter((f) => f.startsWith('crew-'))
    await page.goto('/database')
    await expect(page.getByRole('heading', { name: 'Database', exact: true })).toBeVisible()
    await expect(page.locator('[data-backups]')).toContainText("inside the day's tar.gz")
    // The newest: the API lists them newest first.
    if (days.length > 0) await expect(page.locator('[data-backups]')).toContainText(`The last is ${days[0]}, ${days.length} kept.`)
    await expect(page.locator('[data-months]')).toContainText('kept for good')
    await expect(page.getByText('one dump before every update, kept with each kept version (Machine settings, Updates: 5 unless set)')).toBeVisible()
    await expect(page.getByRole('link', { name: 'Versions' }).last()).toBeVisible()
    await shot(page, 'db')
  })
})
