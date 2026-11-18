import type { ReactNode } from 'react'
import { renderToStaticMarkup } from 'react-dom/server'
import { describe, expect, it, vi } from 'vitest'
import type { Summary } from '../lib/api'

vi.mock('@tanstack/react-router', async (importOriginal) => {
  const actual = await importOriginal<typeof import('@tanstack/react-router')>()
  return {
    ...actual,
    createFileRoute: () => (opts: unknown) => opts,
    Link: ({ to, params, className, children }: { to: string; params?: Record<string, string>; className?: string; children: ReactNode }) => {
      const href = Object.entries(params ?? {}).reduce((acc, [k, v]) => acc.replace(`$${k}`, encodeURIComponent(v)), to)
      return (
        <a href={href} className={className}>
          {children}
        </a>
      )
    },
  }
})

import { FeedTable } from './activity'
import { OneRuntimeImageCard, RoomForMoreCard, WhatASandboxIsGivenCard } from '../routes/clis'
import { StagesBar, visibleColumnTickets } from '../routes/p.$project.board'
import { filterTickets } from '../routes/p.$project.tickets.index'
import { SidebarMenu } from '../routes/__root'
import { formatAgentMinutes, sortSummaries, SummariesTable, summaryFixRounds, summaryMinutes, summaryRuns, summaryText, summaryTokens } from '../routes/p.$project.summaries'

const marked = (html: string, needle: string) => {
  const all = html.split(needle).length - 1
  const free = [...html.matchAll(/<\w+[^>]*data-free-text="true"[^>]*>([^<]*)</g)].reduce((n, m) => n + (m[1] ?? '').split(needle).length - 1, 0)
  return all > 0 && free === all
}

describe('the Summaries screen (20.1, summaries.png)', () => {
  const sample: Summary[] = [
    {
      ticket: 'SF-7',
      title: 'Lock account after failed attempts ($3 test)',
      outcome: 'done',
      whatWasDone: 'Added lockout counter.',
      howItWent: 'The first request for a move failed its check. One more round fixed it.',
      missing: false,
      writtenAt: '2026-10-31T12:42:00Z',
      facts: {
        ended: '2026-10-31T12:42:00Z',
        fix_rounds: 0,
        tokens: 142000,
        subtasks: [
          { run: 'r1', minutes: 11, tokens: 30000 },
          { run: 'r2', minutes: 20, tokens: 50000 },
          { run: 'r3', minutes: 30, tokens: 62000 },
        ],
      },
    },
    {
      ticket: 'SF-3',
      title: 'Sign-up endpoint',
      outcome: 'done',
      howItWent: 'Needed 1 fix round: The new function repeats a helper in lib/. Use the helper.',
      missing: false,
      writtenAt: '2026-10-31T11:13:00Z',
      facts: {
        ended: '2026-10-31T11:13:00Z',
        fix_rounds: 2,
        minutes: 67,
        tokens: 296000,
        subtasks: [{ run: 'r1' }, { run: 'r2' }, { run: 'r3' }, { run: 'r4' }, { run: 'r5' }, { run: 'r6' }],
      },
    },
  ]

  it('renders the columns of summaries.png with tokens in place of dollar cost, and marks free text', () => {
    const html = renderToStaticMarkup(<SummariesTable project="storefront-api" summaries={sample} />)
    for (const col of ['Ticket', 'What happened', 'Done', 'Agent runs', 'Agent time', 'Fix rounds', 'Tokens']) {
      expect(html).toContain(`>${col}</th>`)
    }
    expect(html).toContain('href="/p/storefront-api/tickets/SF-7"')
    expect(marked(html, 'Lock account after failed attempts ($3 test)')).toBe(true)
    expect(marked(html, 'Added lockout counter. — The first request for a move failed its check. One more round fixed it.')).toBe(true)
    expect(html).toContain('>1 h 1 min</td>')
    expect(html).toContain('>1 h 7 min</td>')
    expect(html).toContain('>142k</td>')
    expect(html).toContain('>296k</td>')
    expect(summaryRuns(sample[0]!)).toBe(3)
    expect(summaryRuns(sample[1]!)).toBe(6)
    expect(summaryMinutes(sample[0]!)).toBe(61)
    expect(summaryMinutes(sample[1]!)).toBe(67)
    expect(summaryFixRounds(sample[1]!)).toBe(2)
    expect(summaryTokens(sample[1]!)).toBe(296000)
    expect(formatAgentMinutes(47)).toBe('47 min')
    expect(formatAgentMinutes(60)).toBe('1 h')
    expect(summaryText({ ticket: 'SF-1', title: 't', outcome: 'closed', facts: {}, missing: true })).toBe('Withdrawn in Planning: the facts only.')
    expect(
      summaryText({
        ticket: 'WC-13',
        title: 't',
        outcome: 'done',
        whatWasDone: 'Updated [README.md](file:///home/agent/work/README.md).',
        howItWent: 'Clean.',
        facts: {},
        missing: false,
      }),
    ).toBe('Updated README.md. — Clean.')
  })

  it('sorts summaries by tokens, time, and rework', () => {
    expect(sortSummaries(sample, 'tokens').map((s) => s.ticket)).toEqual(['SF-3', 'SF-7'])
    expect(sortSummaries(sample, 'time').map((s) => s.ticket)).toEqual(['SF-3', 'SF-7'])
    expect(sortSummaries(sample, 'rework').map((s) => s.ticket)).toEqual(['SF-3', 'SF-7'])
    expect(sortSummaries(sample, 'newest').map((s) => s.ticket)).toEqual(['SF-7', 'SF-3'])
  })
})

describe('the board stages bar and the Agent CLIs cards (board.png, clis.png)', () => {
  it('renders Change the stages on the stages bar linking to the project settings', () => {
    const html = renderToStaticMarkup(
      <StagesBar
        project="storefront-api"
        stages={[
          { id: 'planning', optional: false, on: true },
          { id: 'review', optional: true, on: true },
          { id: 'verification', optional: true, on: false },
        ]}
      />,
    )
    expect(html).toContain('Change the stages')
    expect(html).toContain('href="/p/storefront-api/settings"')
  })

  it('renders the One runtime image card, Room for more, and What a sandbox is given for agy', () => {
    const runtime = renderToStaticMarkup(<OneRuntimeImageCard crewVersion="0.3.16" agyVersion="1.4.7" />)
    expect(runtime).toContain('One runtime image')
    expect(runtime).toContain('crew-runtime:0.3.16')
    expect(runtime).toContain('agy 1.4.7')
    expect(runtime).toContain('href="/versions"')
    expect(runtime).not.toContain('claude')

    const room = renderToStaticMarkup(<RoomForMoreCard />)
    expect(room).toContain('Room for more')
    expect(room).toContain('codex, opencode')
    expect(room).not.toContain('claude')

    const sandbox = renderToStaticMarkup(<WhatASandboxIsGivenCard />)
    expect(sandbox).toContain('What a sandbox is given')
    expect(sandbox).toContain('The login of agy')
    expect(sandbox).toContain('No gcloud.')
    expect(sandbox).not.toContain('and gcloud')
    expect(sandbox).not.toContain('act with gcloud')
    expect(sandbox).not.toContain('claude')
  })

  it('renders the activity feed timestamp cell without wrapping or colliding with the event kind', () => {
    const html = renderToStaticMarkup(
      <FeedTable
        allProjects
        lines={[
          {
            at: '2026-10-23T12:00:00Z',
            project: 'wordcount',
            kind: 'agent',
            text: 'Builder started WC-14.2 Build on gemini-4-argon · high',
            ticket: 'WC-14',
          },
        ]}
      />,
    )
    expect(html).toContain('py-1.5 pr-3 font-mono text-xs whitespace-nowrap text-text-dim')
    expect(html).toContain('>AGENT</td>')
    expect(html).not.toContain('w-14')
  })

  it('truncates board columns to 12 unless expanded and filters tickets by text search', () => {
    const items = Array.from({ length: 15 }, (_, i) => ({
      key: `CREW-${i + 1}`,
      title: i === 2 ? 'Refactor polling hook' : `Ticket ${i + 1}`,
      kind: 'feature',
      stage: 'done',
      goal: i === 6 ? 'Zero layout flash' : '',
      origin: 'plan',
    }))
    expect(visibleColumnTickets(items, false)).toHaveLength(12)
    expect(visibleColumnTickets(items, true)).toHaveLength(15)
    expect(filterTickets(items, 'CREW-14').map((t) => t.key)).toEqual(['CREW-14'])
    expect(filterTickets(items, 'polling').map((t) => t.key)).toEqual(['CREW-3'])
    expect(filterTickets(items, { query: 'layout flash', stage: 'done' }).map((t) => t.key)).toEqual(['CREW-7'])
  })

  it('renders SidebarMenu links for desktop and mobile navigation drawer', () => {
    const html = renderToStaticMarkup(
      <SidebarMenu
        f={{
          email: 'op@example.com',
          version: '0.3.20',
          leadSlots: [{ cli: 'agy', used: 0, slots: 2 }],
          sandboxes: 3,
          inbox: 2,
          drafts: 0,
          projects: [{ name: 'crew', key: 'CREW', state: 'active', needsYou: 0, working: 1 }],
        }}
        open="crew"
      />,
    )
    expect(html).toContain('href="/plans"')
    expect(html).toContain('href="/sandboxes"')
    expect(html).toContain('href="/p/crew/board"')
    expect(html).toContain('href="/p/crew/tickets"')
    expect(html).toContain('href="/p/crew/knowledge"')
  })
})
