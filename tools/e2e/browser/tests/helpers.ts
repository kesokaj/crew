import { expect, type Page } from '@playwright/test'

// What the screens' tests share: a screenshot at the width of the simulation's pictures
// (docs/plan/screens/) when CREW_SHOTS names a folder, and reading a route as the page would.
const shots = process.env.CREW_SHOTS ?? ''

// shot takes the whole page: the window is made as tall as the screen's content, which scrolls inside
// the app's frame, so a full-page picture would stop at the window.
export async function shot(page: Page, name: string) {
  if (!shots) return
  const h = await page.evaluate(() => document.querySelector('main')?.scrollHeight ?? 900)
  await page.setViewportSize({ width: 1280, height: Math.min(Math.max(h + 52, 900), 8000) })
  await page.screenshot({ path: `${shots}/${name}.png`, fullPage: true })
  await page.setViewportSize({ width: 1280, height: 900 })
}

// api reads a route as the page would. The ingress answers 429 to a client that asks more than 100
// times a second, which a run of these tests can: it waits, and asks again.
export async function api(page: Page, path: string): Promise<unknown> {
  for (let i = 0; ; i++) {
    const r = await page.request.get(path)
    if (r.status() !== 429 || i === 10) {
      expect(r.status(), path).toBe(200)
      return r.json()
    }
    await page.waitForTimeout(300)
  }
}

// project is the name of the first project with a ticket that is done.
export async function project(page: Page): Promise<string> {
  const { projects } = (await api(page, '/api/overview')) as { projects: { name: string; byStage: Record<string, number> }[] }
  const p = projects.find((x) => (x.byStage.done ?? 0) > 0)
  expect(p, 'a project with a done ticket').toBeTruthy()
  return p!.name
}
