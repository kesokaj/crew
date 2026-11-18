import { renderToStaticMarkup } from 'react-dom/server'
import { describe, expect, it } from 'vitest'
import type { RunEvent, Turn } from '../lib/api'
import { Diff } from './diff'
import { PromptView } from './prompt'
import { CommitLine, Conversation } from './runs'
import { summary } from './trajectory'

// Since design 0.53 no screen shows a cost, and the gate's browser check reads every text of a screen
// less what the app marks data-free-text: the operator's and agents' own words. A run of crew-gate
// whose agent wrote "the project cost $3.54" failed the gate of 0.2.17 on its run page, where the
// conversation and the trajectory showed that text unmarked. These pin the marks of the run's text.

// marked says the markup shows needle, and only inside elements marked data-free-text.
const marked = (html: string, needle: string) => {
  const all = html.split(needle).length - 1
  const free = [...html.matchAll(/<\w+[^>]*data-free-text="true"[^>]*>([^<]*)</g)].reduce((n, m) => n + (m[1] ?? '').split(needle).length - 1, 0)
  return all > 0 && free === all
}
const agentSaid = 'Neither ticket needed a fix round, and the project cost $3.54 over 19 runs.'
const event = (type: string, payload: Record<string, unknown>): RunEvent => ({ seq: 1, at: '2026-10-21T19:30:00Z', type, payload })

describe("the run's own text is marked as not the app's", () => {
  it("marks each turn's text in the conversation", () => {
    const turns: Turn[] = [
      { number: 1, speaker: 'crew', text: 'Build WC-12.', toolCalls: [], tokens: 0, rebuilt: false, at: '2026-10-21T19:20:00Z' },
      { number: 2, speaker: 'agent', text: agentSaid, toolCalls: ['report'], tokens: 1200, rebuilt: false, at: '2026-10-21T19:30:00Z' },
    ]
    const html = renderToStaticMarkup(<Conversation turns={turns} />)
    expect(marked(html, agentSaid)).toBe(true)
    expect(marked(html, 'Build WC-12.')).toBe(true)
    // What the app says of a turn stays its own text, which the check reads.
    expect(marked(html, '1,200 tokens')).toBe(false)
    expect(html).toContain('1,200 tokens')
  })

  it("marks what an agent, a tool or the CLI wrote in the trajectory, and not the app's tokens", () => {
    const html = (e: RunEvent) => renderToStaticMarkup(<>{summary(e)}</>)
    expect(marked(html(event('message', { text: agentSaid })), agentSaid)).toBe(true)
    expect(marked(html(event('tool_call', { tool: 'shell', input: 'echo $3' })), 'echo $3')).toBe(true)
    expect(marked(html(event('tool_result', { tool: 'shell', output: '$3' })), '$3')).toBe(true)
    expect(marked(html(event('status', { status: 'done', detail: 'spent $3' })), 'spent $3')).toBe(true)
    expect(marked(html(event('quota', { left: '$3 of credit' })), '$3 of credit')).toBe(true)
    const usage = html(event('usage', { response: agentSaid, tokensIn: 1000, tokensOut: 2 }))
    expect(marked(usage, agentSaid)).toBe(true)
    expect(usage).toContain(' · 1k in, 2 out')
    expect(marked(usage, '1k in, 2 out')).toBe(false)
  })

  it("marks a commit's subject and the lines of a diff, which the agent wrote", () => {
    const line = renderToStaticMarkup(<CommitLine c={{ id: 'abcdef1234567', subject: 'Fix the $3 estimate' }} />)
    expect(marked(line, 'Fix the $3 estimate')).toBe(true)
    expect(marked(line, 'abcdef1')).toBe(false)
    const diff = renderToStaticMarkup(
      <Diff file={{ path: 'README.md', added: 1, removed: 0, binary: false, cut: false, patch: '@@ -1 +1 @@\n+costs $3 a month' }} />,
    )
    expect(marked(diff, '+costs $3 a month')).toBe(true)
    expect(marked(diff, '@@ -1 +1 @@')).toBe(true)
  })

  it('marks every part of a saved prompt, and the prompt as one text', () => {
    const parts = [
      { part: 'stage', source: 'baseline/stages/build.md', text: 'Build the ticket.' },
      { part: 'ticket', source: 'ticket WC-12', text: 'Keep the $3 budget.' },
    ]
    const inParts = renderToStaticMarkup(<PromptView prompt={'Build the ticket.\n\nKeep the $3 budget.'} parts={parts} />)
    expect(marked(inParts, 'Keep the $3 budget.')).toBe(true)
    expect(marked(inParts, 'Build the ticket.')).toBe(true)
    // A prompt whose parts were not saved is shown as one text.
    const whole = renderToStaticMarkup(<PromptView prompt="Keep the $3 budget." parts={[]} />)
    expect(marked(whole, 'Keep the $3 budget.')).toBe(true)
  })
})
