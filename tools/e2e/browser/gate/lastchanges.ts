import { expect, type Page } from '@playwright/test'

// A request to the updater as GET /api/crew/versions lists it, newest first.
export type Request = { action: string; state: string; version: string; from?: string }

// The Updater card's Last changes show the five newest requests that ended.
export function shownRequests(requests: Request[]): Request[] {
  return requests.filter((q) => q.state !== 'running').slice(0, 5)
}

// gateLines reads, on the Versions screen that is open, each line of Last changes whose request is a
// gate's or a check's: it is worded as a check beside the running version, never as an install. The
// gates of 0.2.14 and 0.2.15 on dave read "Not installed" and "switched, and healthy for ten
// minutes", and nothing had switched. It is shared by the gate's tests and the browser tests.
export async function gateLines(page: Page, requests: Request[]): Promise<void> {
  const shown = shownRequests(requests)
  const lines = page.locator('[data-last-changes] > li')
  await expect(lines).toHaveCount(Math.max(shown.length, 1))
  for (const [i, q] of shown.entries()) {
    if (q.action !== 'gate' && q.action !== 'check') continue
    const line = lines.nth(i)
    // The same request as the API's: a line out of order would read another's words.
    await expect(line).toContainText(`${q.from ?? '—'} → ${q.version}`)
    await expect(line).not.toContainText(/switched|Not installed|kept running/)
  }
}
