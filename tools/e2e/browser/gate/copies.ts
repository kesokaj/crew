import { expect, type Page } from '@playwright/test'

// A release as GET /api/crew/versions lists it, with what the copy on GitHub needs.
export type Copied = { version: string; gate?: unknown; published?: { done?: string } | null }

// What GET /api/crew/versions says of copying: publish is true only when releases are copied to
// GitHub (16.9, since 0.53). A server before 0.2.17 does not say it.
export type Copies = { publish?: boolean; releases: Copied[] }

// waiting are the releases that passed a gate and are not on GitHub: their line says whether they are
// being copied.
export function waiting(v: Copies): Copied[] {
  return v.releases.filter((r) => r.gate && !r.published?.done)
}

// copyLines reads, on the Versions screen that is open, the line of each release that passed a gate
// and is not on GitHub: while copying is off, it says so, and no line says a release is being copied.
// On dave, with updates.publish off, every such line said "being copied to GitHub" and nothing was
// copied. It is shared by the gate's tests and the browser tests.
export async function copyLines(page: Page, v: Copies): Promise<void> {
  for (const r of waiting(v)) {
    await expect(page.locator(`[data-release="${r.version}"]`)).toContainText('not copied to GitHub: copying is off')
  }
  await expect(page.getByText('being copied to GitHub')).toHaveCount(0)
}
