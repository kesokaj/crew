import { expect, test, type BrowserContext, type Page } from '@playwright/test'
import { shot } from './helpers'

// Conversations and the chat of a ticket (design 11.8, 11.9, 20.1, 20.2; plan M20 phase 5). They read
// the rows m17-stack.sh seed-talk sets and write messages, tickets, comments and memory, so they run
// only where CREW_E2E_WRITES is set: the throwaway stack of M17's D28, which has no Docker, so no turn
// starts there and a message waits.
test.skip(!process.env.CREW_E2E_WRITES, 'reads the seeded m20shop and writes: only on a throwaway installation')

const c1 = '20000000-0000-4000-8000-000000000001'
const c2 = '20000000-0000-4000-8000-000000000002'

// The two headers the ingress takes from the sign-in come from two cookies on the stack (M17 phase 4).
async function signedIn(context: BrowserContext) {
  await context.addCookies([
    { name: 'm17_auth', value: String(Math.floor(Date.now() / 1000)), domain: '.crew.test', path: '/', secure: true },
    { name: 'm17_sid', value: `m20-${Date.now()}`, domain: '.crew.test', path: '/', secure: true },
  ])
}

test.beforeEach(async ({ context }) => signedIn(context))

// Where a picture shows claude, --resume, 2 GB or dollars, the design wins (plan M20 D16, 0.53).
async function noPictureLeftovers(page: Page) {
  const text = (await page.locator('main').innerText()).toLowerCase()
  for (const word of ['claude', '--resume', '2 gb', '$']) expect(text, word).not.toContain(word)
}

test('Conversations is in the menu after Inbox, and lists each conversation with what it is about, searchable', async ({ page }) => {
  await page.goto('/conversations')
  const nav = page.locator('nav')
  const items = await nav.locator('a').allInnerTexts()
  const inbox = items.findIndex((t) => t.startsWith('Inbox'))
  expect(items[inbox + 1]?.trim()).toBe('Conversations')
  await expect(page.getByRole('heading', { name: 'Conversations', exact: true })).toBeVisible()
  const table = page.getByRole('table', { name: 'Conversations' })
  const first = table.locator(`[data-conversation="${c1}"]`)
  await expect(first).toContainText('Why is the first checkout request after a deploy slow?')
  await expect(first).toContainText('Agent: Building the table at start')
  await expect(first).toContainText('m20shop')
  await expect(first).toContainText('agy · gemini-3.1-pro')
  await expect(first.locator('td').nth(3)).toHaveText('2')
  await expect(first).toContainText('saved')
  await expect(table.locator(`[data-conversation="${c2}"]`)).toContainText('no project')
  await expect(page.getByText('What a conversation can do')).toBeVisible()
  await expect(page.getByText('How a conversation is kept')).toBeVisible()
  await expect(page.getByText('agy -p --conversation')).toBeVisible()
  await noPictureLeftovers(page)
  await shot(page, 'm20-convs')
  await page.getByLabel('Search the conversations').fill('pears')
  await expect(table.locator('tbody tr')).toHaveCount(1)
  await expect(table.locator(`[data-conversation="${c2}"]`)).toBeVisible()
})

test('A conversation shows its turns, how each was continued, its sandbox, what came of it, and its trajectory', async ({ page }) => {
  await page.goto(`/conversations/${c1}`)
  await expect(page.getByRole('heading', { name: 'Why is the first checkout request after a deploy slow?' })).toBeVisible()
  await expect(page.locator('main')).toContainText('About m20shop. The agent sees that project and nothing else. 2 turns, saved in Crew\'s database.')
  const answers = page.locator('[data-entry="agent"]')
  await expect(answers).toHaveCount(2)
  await expect(answers.first()).toContainText('agy · gemini-3.1-pro · high')
  await expect(answers.first()).toContainText('a new conversation')
  await expect(answers.nth(1)).toContainText("a new sandbox, the CLI's files put back")
  await expect(answers.nth(1).locator('[data-tools]')).toContainText('go test ./conversation/...')
  await expect(answers.nth(1)).toContainText('It proposed MV-3')
  await expect(page.locator('[data-entry="you"]')).toHaveCount(2)
  const sandbox = page.getByRole('region', { name: 'Sandbox' })
  await expect(sandbox.locator('[data-sandbox-state]')).toHaveText('saved, sandbox removed')
  await expect(sandbox).toContainText('none now')
  await expect(sandbox).toContainText('large: 4 CPU, 6 GB')
  await expect(sandbox).toContainText('a read-only copy of main of m20shop')
  await expect(sandbox.locator('[data-work-folder]')).toContainText('2 files, 2.0 KB')
  const conv = page.getByRole('region', { name: 'This conversation' })
  await expect(conv).toContainText('aade77c4-2f16-896e-d282-123cded355cc')
  await expect(conv).toContainText('agy -p --conversation aade77c4-2f16-896e-d282-123cded355cc')
  await expect(conv).toContainText('13.8k')
  await expect(page.locator('[data-came-of="MV-3"]')).toContainText('proposed by the agent, not yours yet')
  // The trajectory's events come from the runs' route, which a stack without Docker does not serve.
  await expect(page.getByRole('heading', { name: /Trajectory/ })).toBeVisible()
  await expect(page.getByRole('button', { name: 'Close' })).toBeVisible()
  await expect(page.getByRole('button', { name: 'Delete' })).toBeVisible()
  await expect(page.getByRole('button', { name: 'Start a plan from this' })).toHaveCount(0)
  await noPictureLeftovers(page)
  await shot(page, 'm20-conv')
})

test("Make a ticket of this adopts the agent's proposal, and Remember this and Tell the agent keep their source", async ({ page }) => {
  await page.goto(`/conversations/${c1}`)
  const second = page.locator('[data-entry="agent"]').nth(1)
  await second.getByRole('button', { name: 'Make a ticket of this' }).click()
  const form = second.getByRole('form', { name: 'Make a ticket of this' })
  await expect(form.getByLabel('Which ticket')).toHaveValue('MV-3')
  await expect(form.getByLabel('What')).toHaveValue(/Building the table at start/)
  await form.getByLabel('Goal').fill('The first checkout request after a deploy takes under 0.1 s.')
  await form.getByRole('button', { name: 'Adopt MV-3' }).click()
  await expect(page.locator('[data-came-of="MV-3"]')).toContainText('your ticket')

  const first = page.locator('[data-entry="agent"]').first()
  await first.getByRole('button', { name: 'Remember this' }).click()
  const remember = first.getByRole('form', { name: 'Remember this' })
  await remember.getByLabel('Title').fill('The price table is built on the first request')
  await remember.getByRole('button', { name: 'Remember this' }).click()
  await expect(remember).toHaveCount(0)
  await page.goto('/p/m20shop/knowledge')
  const row = page.locator('tr', { hasText: 'The price table is built on the first request' })
  await expect(row).toContainText('the assistant wrote it, you confirmed it')

  await page.goto(`/conversations/${c1}`)
  await page.locator('[data-entry="agent"]').first().getByRole('button', { name: 'Tell the agent' }).click()
  const tell = page.getByRole('form', { name: 'Tell the agent' })
  await tell.getByLabel('The ticket').fill('MV-1')
  await tell.getByLabel('The text the agent gets').fill('The first checkout request builds the price table: keep that in mind.')
  await tell.getByRole('button', { name: 'Tell the agent' }).click()
  await expect(tell).toHaveCount(0)
  await page.goto('/p/m20shop/tickets/MV-1')
  const records = page.locator('section[data-part="record"]')
  await expect(records).toContainText('by the assistant of the chat, sent on by you')
  await expect(records).toContainText('keep that in mind')
})

test('A conversation about no project has no buttons of a project, saves, moves a ticket to a backlog, and starts a plan', async ({ page }) => {
  await page.goto(`/conversations/${c2}`)
  await expect(page.locator('main')).toContainText('About no project.')
  await expect(page.locator('[data-entry="agent"]')).toHaveCount(1)
  for (const name of ['Tell the agent', 'Make a ticket of this', 'Remember this']) await expect(page.getByRole('button', { name })).toHaveCount(0)
  await expect(page.getByRole('button', { name: 'Move to project backlog' }).first()).toBeVisible()
  await expect(page.getByRole('button', { name: 'Save', exact: true })).toBeVisible()
  await page.getByRole('button', { name: 'Save', exact: true }).click()
  await expect(page.getByRole('button', { name: 'Saved', exact: true })).toBeVisible()
  await expect(page.getByRole('region', { name: 'Sandbox' })).toContainText("only the conversation's own work folder")
  const answer = page.locator('[data-entry="agent"]').first()
  await answer.getByRole('button', { name: 'Move to project backlog' }).click()
  const form = answer.getByRole('form', { name: 'Move to project backlog' })
  await form.getByLabel('Project').selectOption('m20shop')
  await form.getByLabel('Title').fill('Server-side sessions for pears')
  await form.getByLabel('Goal').fill('Sessions end immediately when a password changes.')
  await form.getByRole('button', { name: 'Move to backlog' }).click()
  await expect(form).toHaveCount(0)
  await expect(page.locator('[data-came-of="MV-4"]')).toContainText('moved to m20shop backlog')
  await page.getByRole('button', { name: 'Start a plan from this' }).click()
  await expect(page).toHaveURL(/\/plans\/[0-9a-f-]{36}$/)
})

test('Start a conversation: the message is saved and waits for its turn; Delete removes it', async ({ page }) => {
  await page.goto('/conversations')
  const start = page.getByRole('region', { name: 'Start a conversation' })
  await start.getByLabel('Your first message').fill('What is a good name for a shop of pears?')
  await start.getByRole('button', { name: 'Start' }).click()
  await expect(page).toHaveURL(/\/conversations\/[0-9a-f-]{36}$/)
  await expect(page.getByRole('heading', { name: 'What is a good name for a shop of pears?' })).toBeVisible()
  await expect(page.locator('[data-entry="you"]')).toHaveText(/What is a good name/)
  await expect(page.locator('[data-waiting]')).toBeVisible()
  await expect(page.locator('[data-sandbox-state]')).toHaveText('not started: it starts with the first turn')
  page.once('dialog', (d) => void d.accept())
  await page.getByRole('button', { name: 'Delete' }).click()
  await expect(page).toHaveURL(/\/conversations$/)
  await expect(page.getByText('What is a good name for a shop of pears?')).toHaveCount(0)
})

test("A running ticket's chat: the answers with three buttons, marked text with Comment, Ask, and Tell the agent", async ({ page }) => {
  await page.goto('/p/m20shop/tickets/MV-1')
  const chat = page.locator('section[data-part="message"]')
  await expect(chat.getByRole('heading', { name: 'Chat about this ticket' })).toBeVisible()
  const answer = chat.locator('[data-entry="agent"]').first()
  await expect(answer).toContainText('Assistant · agy · claude-opus-4-6 · high')
  await expect(answer).toContainText('I changed nothing.')
  for (const name of ['Tell the agent', 'Make a ticket of this', 'Remember this']) await expect(answer.getByRole('button', { name })).toBeVisible()
  await expect(chat.getByRole('button', { name: 'Ask', exact: true })).toBeVisible()
  await expect(chat.getByRole('button', { name: 'Tell the agent' }).last()).toBeVisible()
  await expect(chat).toContainText('Select text anywhere on this page and choose Comment')
  await expect(page.locator('main')).toContainText('A comment to the agent is sent from the chat of this ticket, with Tell the agent.')
  await expect(page.getByRole('heading', { name: 'A comment to the agent', exact: true })).toHaveCount(0)

  // A selection in the box where the operator writes has the box's own Improve text and Review text
  // (7.2), not Comment. What stops Comment is the browser: a text box's selection is not in the page's
  // selection, which readMark reads; readMark's own stop at a text box is a second guard, and a build
  // without it passes here too (.agents/scratch/s39/mut-mark-tell.log).
  const box = chat.getByLabel('Ask about this ticket')
  await box.fill('a few words')
  await box.selectText()
  await box.dispatchEvent('mouseup')
  await expect(chat.getByRole('menu', { name: 'On the selection' })).toBeVisible()
  await expect(page.getByRole('menu', { name: 'On the marked text' })).toHaveCount(0)
  await box.fill('')

  // Marked text: the goal, selected with the mouse, then Comment.
  await page.locator('dd[data-part="goal"]').click({ clickCount: 3 })
  await page.getByRole('menu', { name: 'On the marked text' }).getByRole('menuitem', { name: 'Comment' }).click()
  const marked = chat.locator('[data-marked]')
  await expect(marked).toContainText('The form signs a user in and shows a clear error on failure.')
  await expect(marked).toContainText('marked under Why, what, goal')
  await box.fill('Is this goal still right?')
  await chat.getByRole('button', { name: 'Ask', exact: true }).click()
  const asked = chat.locator('[data-entry="you"]').last()
  await expect(asked).toContainText('Is this goal still right?')
  await expect(asked.locator('[data-quote]')).toContainText('The form signs a user in')
  await expect(asked.locator('[data-quote]')).toContainText('marked under Why, what, goal')
  await expect(chat.locator('[data-waiting]')).toBeVisible()
  // The ticket's history says that it was asked, not what (D28).
  const history = page.getByRole('region', { name: 'History' })
  await expect(history).toContainText('commented on marked text in the chat · operator')
  await expect(history).not.toContainText('Is this goal still right?')
  await shot(page, 'm20-chat')

  // Tell the agent with marked text: the operator's own comment, with the quote.
  await page.locator('dd[data-part="why"]').click({ clickCount: 3 })
  await page.getByRole('menuitem', { name: 'Comment' }).click()
  await box.fill('Keep the error message short.')
  await chat.getByRole('button', { name: 'Tell the agent' }).last().click()
  const records = page.locator('section[data-part="record"]')
  const comment = records.locator('[data-record="comment"]', { hasText: 'Keep the error message short.' })
  await expect(comment).toContainText('by you')
  await expect(comment.locator('[data-quote]')).toContainText('users sign in with email and password')
  await expect(comment.locator('[data-quote]')).toContainText('marked under Why, what, goal')
})

test("A done ticket's chat has no Tell the agent, and Make a ticket of this makes a ticket created from it", async ({ page }) => {
  await page.goto('/p/m20shop/tickets/MV-2')
  const chat = page.locator('section[data-part="message"]')
  const answer = chat.locator('[data-entry="agent"]')
  await expect(answer).toContainText('a product added after the start is missing')
  await expect(answer.getByRole('button', { name: 'Make a ticket of this' })).toBeVisible()
  await expect(answer.getByRole('button', { name: 'Remember this' })).toBeVisible()
  // Not offered on an ended ticket: TicketChat's and Answer's ended guards.
  await expect(page.getByRole('button', { name: 'Tell the agent' })).toHaveCount(0)
  await expect(page.locator('main')).not.toContainText('A comment to the agent is sent from the chat')
  await shot(page, 'm20-chat-done')
  await answer.getByRole('button', { name: 'Make a ticket of this' }).click()
  const form = answer.getByRole('form', { name: 'Make a ticket of this' })
  await expect(form).toContainText('linked to MV-2 as created from it')
  await form.getByLabel('Title').fill('Add a new product to the price table')
  await form.getByLabel('Kind').selectOption('fix')
  await form.getByLabel('Goal').fill('A product added after the start has its price in the table.')
  await form.getByRole('button', { name: 'Make the ticket' }).click()
  await expect(form).toHaveCount(0)
  await expect(page.getByRole('region', { name: 'Links' })).toContainText('Led to')
  await chat.getByLabel('Ask about this ticket').fill('Why was there no fix round?')
  await chat.getByRole('button', { name: 'Ask', exact: true }).click()
  await expect(chat.locator('[data-entry="you"]').last()).toContainText('Why was there no fix round?')
})
