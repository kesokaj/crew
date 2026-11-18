import type { ReactNode } from 'react'
import { renderToStaticMarkup } from 'react-dom/server'
import { describe, expect, it, vi } from 'vitest'

vi.mock('@tanstack/react-router', async (importOriginal) => {
  const actual = await importOriginal<typeof import('@tanstack/react-router')>()
  return {
    ...actual,
    useNavigate: () => vi.fn(),
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

import { AddTicket } from './addticket'
import { buildCommandItems, CommandPalette, filterCommandItems } from './command-palette'
import { Lead } from './overview-panels'
import { isCurrentTicketEnded } from '../routes/p.$project.tickets.$key'
import { validateKnowledgeSearch } from '../routes/p.$project.knowledge'
import { PromptView } from './prompt'
import { TicketHeader } from './ticket/ticket-header'
import { Records } from './ticket/ticket-history-tab'
import { SubtaskTable } from './ticket/ticket-prompt-tab'
import { RunningNow } from './ticket/ticket-run-tab'
import { AgentBox } from './ticket/ticket-sidebar'
import { WhyWhatGoalBox } from './ticket/ticket-spec-tab'
import { EvalsTab } from './knowledge/evals-tab'
import { DecisionsTab, FeedbackTab } from './knowledge/feedback-decisions-tabs'
import { MemoryTab } from './knowledge/memory-bugs-tabs'

const marked = (html: string, needle: string) => {
  const all = html.split(needle).length - 1
  const free = [...html.matchAll(/<\w+[^>]*data-free-text="true"[^>]*>([^<]*)</g)].reduce((n, m) => n + (m[1] ?? '').split(needle).length - 1, 0)
  return all > 0 && free === all
}

describe('CommandPalette and decomposed Task/Knowledge modules', () => {
  const frame = {
    email: 'op@example.com',
    version: '0.3.20',
    leadSlots: [{ cli: 'agy', used: 0, slots: 2 }],
    sandboxes: 2,
    inbox: 1,
    drafts: 0,
    projects: [{ name: 'crew', key: 'CREW', state: 'active', needsYou: 0, working: 1 }],
  }

  it('indexes projects, project screens, tickets, sandboxes, and global routes and filters by query', () => {
    const items = buildCommandItems(
      frame,
      'crew',
      {
        crew: [{ key: 'CREW-42', title: 'Add command palette ($5 note)', stage: 'in_progress' }],
      },
      {
        projectSandboxes: [
          {
            project: 'crew',
            projectId: 'p1',
            projectKey: 'CREW',
            projectState: 'active',
            sandboxId: 'sb-1',
            container: 'crew-proj-crew',
            state: 'running',
            activeSessions: 1,
            activeTickets: 1,
            maxActiveTickets: 2,
            cpus: 4,
            usedCPUs: 4,
            cpuPercent: 12,
            memoryGB: 6,
            memUsedMB: 1024,
            memLimitMB: 6144,
            diskGB: 20,
            diskUsedMB: 512,
            snapshotsCount: 1,
            snapshotsBytes: 1024,
            maxSnapshots: 5,
            suspendAfterMinutes: 5,
            stopAfterMinutes: 30,
            imageTag: 'v0.3.20',
            runs: [],
          },
        ],
        ephemeralSandboxes: [
          {
            id: 'eph-1',
            container: 'crew-check-42',
            kind: 'check',
            project: 'crew',
            detail: 'make check',
            cpus: 2,
            cpuPercent: 5,
            memUsedMB: 256,
            memLimitMB: 2048,
            startedAt: '2026-11-07T00:00:00Z',
          },
        ],
      },
    )
    expect(items.some((x) => x.href === '/p/crew/board' && x.category === 'Project screen')).toBe(true)
    expect(items.some((x) => x.href === '/p/crew/tickets/CREW-42' && x.category === 'Ticket')).toBe(true)
    expect(items.some((x) => x.id === 'sandbox:proj:CREW' && x.category === 'Sandbox')).toBe(true)
    expect(items.some((x) => x.id === 'sandbox:eph:eph-1' && x.category === 'Sandbox')).toBe(true)
    expect(items.some((x) => x.href === '/sandboxes' && x.category === 'Navigation')).toBe(true)

    expect(filterCommandItems(items, 'CREW-42').map((x) => x.id)).toEqual(['ticket:crew:CREW-42'])
    expect(filterCommandItems(items, 'crew-proj-crew').map((x) => x.id)).toEqual(['sandbox:proj:CREW'])
    expect(filterCommandItems(items, 'sandboxes').some((x) => x.href === '/sandboxes')).toBe(true)

    const html = renderToStaticMarkup(<CommandPalette open={true} onClose={() => {}} frame={frame} activeProject="crew" />)
    expect(html).toContain('role="dialog"')
    expect(html).toContain('Command palette')
    expect(html).toContain('crew › Board')
  })

  it('guards isCurrentTicketEnded against stale keepPreviousData ticket views and opens only primary parts in PromptView', () => {
    const staleDoneView = {
      ticket: { key: 'CREW-1', stage: 'done' } as Parameters<typeof isCurrentTicketEnded>[0] extends { ticket: infer T } | null ? T : never,
    }
    expect(isCurrentTicketEnded(staleDoneView, 'CREW-2')).toBe(false)
    expect(isCurrentTicketEnded(staleDoneView, 'CREW-1')).toBe(true)

    const promptHtml = renderToStaticMarkup(
      <PromptView
        prompt="full prompt"
        version="0.3.20"
        parts={[
          { part: 'stage', source: 'baseline/builder.md', text: 'stage instructions' },
          { part: 'ticket', source: 'CREW-2', text: 'ticket instructions' },
        ]}
      />,
    )
    expect(promptHtml).toContain('data-part="stage" data-open="false"')
    expect(promptHtml).toContain('data-part="ticket" data-open="true"')
  })

  it('renders TicketHeader with sticky decision bar and workspace tabs, WhyWhatGoalBox, and progressive Records', () => {
    const v = {
      ticket: {
        id: 't1',
        project: 'crew',
        number: 1,
        key: 'CREW-1',
        title: 'Task decomposition ($3 test)',
        kind: 'feature',
        stage: 'review',
        stageAt: '2026-11-06T12:00:00Z',
        priority: 1,
        origin: 'plan',
        createdBy: 'operator',
        createdAt: '2026-11-06T10:00:00Z',
        fixRounds: 0,
        approaches: 0,
        hold: false,
        skips: [],
        why: 'Task file was 1,309 lines ($3).',
        what: 'Split into cohesive sub-modules.',
        goal: 'All modules under 350 lines.',
        checks: [{ kind: 'cmd', spec: 'npm --prefix web test', keeps: true }],
        analysis: { status: 'approved' },
        links: [],
        docs: [],
      },
      checkRuns: [],
      records: [
        { id: 1, kind: 'note', author: 'builder', body: 'Initial build ($1).', resolved: false, createdAt: '2026-11-06T11:00:00Z' },
        { id: 2, kind: 'review', author: 'reviewer', verdict: 'approve', body: 'Clean split ($2).', resolved: false, createdAt: '2026-11-06T11:30:00Z' },
      ],
      linkedFrom: [],
      history: [],
    }
    const headerHtml = renderToStaticMarkup(
      <TicketHeader
        project="crew"
        v={v}
        stages={[
          { id: 'in_progress', optional: false, on: true },
          { id: 'review', optional: true, on: true },
        ]}
        live={[]}
        ended={false}
        error=""
        failed=""
        activeTab="all"
        onTabChange={() => {}}
        onPatch={() => {}}
        onDecide={() => {}}
      />,
    )
    expect(headerHtml).toContain('data-sticky-decision-header="true"')
    expect(headerHtml).toContain('Spec &amp; Checks')
    expect(headerHtml).toContain('Live Run &amp; Diff')
    expect(marked(headerHtml, 'Task decomposition ($3 test)')).toBe(true)

    const specHtml = renderToStaticMarkup(<WhyWhatGoalBox v={v} />)
    expect(marked(specHtml, 'Task file was 1,309 lines ($3).')).toBe(true)
    expect(specHtml).toContain('data-part="why"')
    expect(specHtml).toContain('data-part="goal"')

    const recordsHtml = renderToStaticMarkup(<Records records={v.records} files={{}} project="crew" ended={false} />)
    expect(recordsHtml).toContain('Expand all (2)')
    expect(marked(recordsHtml, 'Clean split ($2).')).toBe(true)
    expect(marked(recordsHtml, 'Initial build ($1).')).toBe(true)
  })

  it('renders decomposed Knowledge MemoryTab, FeedbackTab, DecisionsTab, EvalsTab, and extracted Overview/Board/Task panels', () => {
    expect(validateKnowledgeSearch({ tab: 'evals' })).toEqual({ tab: 'evals' })
    expect(validateKnowledgeSearch({ tab: 'invalid' })).toEqual({})

    const html = renderToStaticMarkup(
      <MemoryTab
        project="crew"
        entries={[
          {
            number: 1,
            title: 'Keep UI components under 400 lines',
            body: 'Split large route files into sub-components.',
            kind: 'convention',
            appliesTo: ['builder'],
            paths: ['web/src/'],
            keywords: ['modularity'],
            author: 'operator',
            status: 'active',
            createdAt: '2026-11-06T10:00:00Z',
          },
        ]}
        error=""
        reload={() => {}}
      />,
    )
    expect(html).toContain('data-memory="1"')
    expect(html).toContain('Keep UI components under 400 lines')

    const feedbackHtml = renderToStaticMarkup(<FeedbackTab project="crew" list={[]} error="" reload={() => {}} />)
    expect(feedbackHtml).toContain('Give feedback')

    const decisionsHtml = renderToStaticMarkup(<DecisionsTab project="crew" list={[]} error="" reload={() => {}} />)
    expect(decisionsHtml).toContain('No agent has decided anything alone yet.')

    const evalsHtml = renderToStaticMarkup(
      <EvalsTab project="crew" evals={{ cases: [], suites: [] }} error="" reload={() => {}} />,
    )
    expect(evalsHtml).toContain('Make the case')

    const addTicketHtml = renderToStaticMarkup(<AddTicket project="crew" onAdded={() => {}} open={true} hideTrigger={true} />)
    expect(addTicketHtml).toContain('Add a ticket')

    const leadHtml = renderToStaticMarkup(
      <Lead lead={{ slots: [{ cli: 'agy', slots: 2, set: 2, runs: [] }], queue: [] }} room={null} sandboxes={[]} />,
    )
    expect(leadHtml).toContain('Lead slots')

    const subtaskHtml = renderToStaticMarkup(<SubtaskTable subs={[]} />)
    expect(subtaskHtml).toContain('Subtasks this ticket is using or has used')

    const runningHtml = renderToStaticMarkup(
      <RunningNow
        project="crew"
        v={{
          ticket: {
            project: 'crew',
            number: 1,
            key: 'CREW-1',
            title: 'Test',
            kind: 'feature',
            stage: 'in_progress',
            stageAt: '2026-11-06T12:00:00Z',
            priority: 1,
            origin: 'plan',
            createdBy: 'operator',
            createdAt: '2026-11-06T10:00:00Z',
            fixRounds: 0,
            approaches: 0,
            hold: false,
            skips: [],
            why: '',
            what: '',
            goal: '',
            checks: [],
            analysis: { status: 'approved' },
            links: [],
            docs: [],
          },
          checkRuns: [],
          records: [],
          linkedFrom: [],
          history: [],
        }}
        live={[]}
        run={null}
      />,
    )
    expect(runningHtml).toContain('Nothing runs on it now.')

    const agentBoxHtml = renderToStaticMarkup(<AgentBox base="/api/projects/crew/tasks/CREW-1" ended={false} subs={[]} network="inherit" onChange={() => {}} />)
    expect(agentBoxHtml).toContain('Agent for this ticket')
  })
})
