import { expect, test, type Page } from '@playwright/test'
import { admin, adminRole, cookie, token } from '../tests/session'

// The live check L1 of M9 (spike item 24, 7.5, 7.11), approved by the operator: one ticket on
// wordcount whose work falls in three parts in three files. The lead plans it in parts with
// create_subtask, the parts run in parallel on their own branches, the backend joins them, and the
// ticket goes on through Review and Test to Done. It runs the lead, three builders, maybe a builder
// that joins by hand, the reviewer, the tester, and the summary writer on agy.
const project = process.env.CREW_PROJECT || 'wordcount'
const shots = process.env.CREW_SHOTS ?? ''
const shot = async (page: Page, name: string) => {
  if (!shots) return
  const h = await page.evaluate(() => document.querySelector('main')?.scrollHeight ?? 900)
  await page.setViewportSize({ width: 1280, height: Math.min(Math.max(h + 52, 900), 8000) })
  await page.screenshot({ path: `${shots}/${name}.png`, fullPage: true })
  await page.setViewportSize({ width: 1280, height: 900 })
}

type Sub = { id: string; kind: string; role: string; state: string; createdBy: string; branch?: string; joinedAt?: string; runs: { state: string }[] }

test('a ticket in three parts is built in parallel and joined (spike item 24)', async ({ page, context }) => {
  test.setTimeout(60 * 60_000)
  await context.addCookies([cookie(token({ email: admin, roles: [adminRole] }))])
  const made = await page.request.post(`/api/projects/${project}/tasks`, {
    headers: { Origin: `https://${process.env.CREW_HOSTNAME}` },
    data: {
      title: 'Write counts as a table, as JSON, and as CSV',
      kind: 'feature',
      why: 'People read the counts in a table, and scripts read them as JSON or CSV; the command (WC-10) will choose one of the three.',
      what:
        'A package internal/format with three writers, each in a file of its own with its own test, and no type they share: ' +
        'Table(w io.Writer, names []string, counts []count.Counts) error in internal/format/table.go, which writes aligned columns of ' +
        'lines, words, bytes, and the name, as wc does; JSON(w, names, counts) in internal/format/json.go, which writes one array of ' +
        'objects with name, lines, words, and bytes; and CSV(w, names, counts) in internal/format/csv.go, with a header row. ' +
        'The three are independent: build them as three subtasks, one for each file and its test, which run in parallel. ' +
        'Not in it: choosing a format in the command, which is WC-10.',
      goal: 'go test ./internal/format/... passes, with a test of each of the three writers.',
      checks: [
        { kind: 'cmd', spec: 'test -f internal/format/table.go && test -f internal/format/json.go && test -f internal/format/csv.go' },
        { kind: 'cmd', spec: 'go test ./internal/format/...' },
      ],
      docsReason: 'A package with no user-facing change; WC-10 documents the formats it offers.',
    },
  })
  expect(made.status()).toBe(201)
  const key = ((await made.json()) as { key: string }).key
  console.log('the ticket:', key)
  let parallelShot = false
  let maxParallel = 0
  const deadline = Date.now() + 58 * 60_000
  for (;;) {
    const view = (await (await page.request.get(`/api/projects/${project}/tasks/${key}`)).json()) as {
      ticket: { stage: string; stuck?: string; waiting?: string }
    }
    const subs = ((await (await page.request.get(`/api/projects/${project}/tasks/${key}/subtasks`)).json()) as { subtasks: Sub[] }).subtasks
    const parts = subs.filter((s) => s.kind === 'part')
    const running = parts.filter((s) => s.runs.some((r) => r.state !== 'ended'))
    maxParallel = Math.max(maxParallel, running.length)
    if (running.length >= 2 && !parallelShot) {
      parallelShot = true
      await page.goto(`/p/${project}/tickets/${key}`)
      await expect(page.locator('[data-parallel]')).toBeVisible({ timeout: 30_000 })
      await shot(page, 'live-task-parallel')
      await page.goto(`/p/${project}/board`)
      await shot(page, 'live-board-parallel')
    }
    if (view.ticket.stage === 'done' || view.ticket.stuck || Date.now() > deadline) {
      console.log('stage', view.ticket.stage, 'stuck', view.ticket.stuck ?? '', 'waiting', view.ticket.waiting ?? '')
      for (const s of subs) console.log(s.id, s.kind, s.role, s.state, s.createdBy.split(' · ')[0], s.branch ?? '', s.joinedAt ? 'joined' : '')
      console.log('parts', parts.length, 'at most running at once', maxParallel)
      await page.goto(`/p/${project}/tickets/${key}`)
      await shot(page, 'live-task-parts-end')
      await page.goto('/crew-mcp')
      await shot(page, 'live-mcp')
      expect(view.ticket.stage).toBe('done')
      return
    }
    await page.waitForTimeout(15_000)
  }
})
