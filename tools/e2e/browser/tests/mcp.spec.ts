import { expect, request, test } from '@playwright/test'
import { admin, adminRole, cookie, token } from './session'
import { api, project, shot } from './helpers'

// The Crew MCP screen of M9 (design 7.5, 11.5, 11.7; mcp.png), and a personal token used through the
// real ingress, as the operator's own tool on a laptop would: no sign-in, a bearer token, and /mcp.
// The token this test makes is revoked at its end. It starts no run.
test.describe('the Crew MCP of M9', () => {
  test.use({ viewport: { width: 1280, height: 900 } })
  test.beforeEach(async ({ context }) => {
    await context.addCookies([cookie(token({ email: admin, roles: [adminRole] }))])
  })

  test('the Crew MCP screen shows the server, what agents created, your own tools, the calls, and the tools', async ({ page }) => {
    await page.goto('/crew-mcp')
    await expect(page.getByRole('heading', { name: 'Crew MCP', exact: true })).toBeVisible()
    for (const card of ['The server', 'Ticket or subtask?', 'What agents created', 'Personal tokens', 'What a personal token can do']) {
      await expect(page.getByRole('heading', { name: card, exact: true })).toBeVisible()
    }
    await expect(page.locator('[data-created-count]')).toContainText(/\d+ tickets? · \d+ subtasks?/)
    await expect(page.locator('[data-tools]')).toContainText('create_subtask')
    await expect(page.locator('[data-tools]')).toContainText('list_projects')
    await expect(page.getByRole('link', { name: 'Crew MCP' })).toBeVisible()
    await shot(page, 'mcp')
  })

  test('a personal token made in the app works at /mcp through the ingress, with no sign-in, until it is revoked', async ({ page, baseURL }) => {
    const p = await project(page)
    await page.goto('/crew-mcp')
    await page.getByRole('button', { name: 'Create a token' }).click()
    const name = `e2e ${Date.now()}`
    await page.getByLabel('Token name').fill(name)
    await page.locator('[data-new-token]').getByLabel(p, { exact: true }).check()
    await page.getByRole('button', { name: 'Make the token' }).click()
    const shown = page.locator('[data-made-token] code')
    await expect(shown).toContainText('crew_pat_')
    const secret = (await shown.textContent())!.trim()
    await shot(page, 'mcp-token')
    // The token is shown once: the list never holds it.
    const listed = JSON.stringify(await api(page, '/api/crew/mcp/tokens'))
    expect(listed).not.toContain(secret)
    // A tool with no cookie, as one on a laptop is.
    const tool = await request.newContext({ baseURL, ignoreHTTPSErrors: true })
    const rpc = (tok: string, method: string, params: object) =>
      tool.post('/mcp', { headers: { Authorization: `Bearer ${tok}` }, data: { jsonrpc: '2.0', id: 1, method, params } })
    const init = await rpc(secret, 'initialize', { protocolVersion: '2025-06-18' })
    expect(init.status()).toBe(200)
    expect(JSON.stringify(await init.json())).toContain('"crew"')
    const projects = await rpc(secret, 'tools/call', { name: 'list_projects', arguments: {} })
    expect(JSON.stringify(await projects.json())).toContain(p)
    const ticket = await rpc(secret, 'tools/call', { name: 'list_tickets', arguments: { project: p } })
    expect((await ticket.json()).result.isError).toBe(false)
    // The sign-in still guards everything else: the API answers this tool nothing.
    expect((await tool.get('/api/frame', { headers: { Authorization: `Bearer ${secret}` }, maxRedirects: 0 })).status()).not.toBe(200)
    // Refused: 401, or 429 once five wrong tokens from this address in an hour blocked it (11.7); each
    // run of this test sends two, so a third run within the hour meets the block.
    const refused = [401, 429]
    expect(refused).toContain((await rpc('crew_pat_not-a-token', 'tools/list', {})).status())
    // The call is on the screen, with the token's name.
    await page.goto('/crew-mcp')
    await expect(page.locator('[data-calls]')).toContainText(`token ${name}`)
    // Revoked, it no longer works.
    await page.locator(`[data-token="${name}"]`).getByRole('button', { name: 'Revoke' }).click()
    await expect(page.locator(`[data-token="${name}"]`)).toHaveCount(0)
    expect(refused).toContain((await rpc(secret, 'tools/list', {})).status())
    await tool.dispose()
  })
})
