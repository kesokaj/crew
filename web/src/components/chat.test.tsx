import { renderToStaticMarkup } from 'react-dom/server'
import { describe, expect, it, vi } from 'vitest'
import type { ConvEntry, ConvView } from '../lib/api'
import { Entries, TicketForm } from './chat'
import { ChatRoomsPanel, ChoicePromptCard, type ChoicePromptSpec, type RoomView } from './chat-rooms'

vi.mock('@tanstack/react-router', async (importOriginal) => {
  const actual = await importOriginal<typeof import('@tanstack/react-router')>()
  return {
    ...actual,
    Link: ({ to, children, className }: { to: string; children?: React.ReactNode; className?: string }) => (
      <a href={to} className={className}>
        {children}
      </a>
    ),
  }
})

const entry: ConvEntry = {
  kind: 'agent',
  turn: 1,
  text: 'Add a circuit breaker around the webhook dispatcher.',
  cli: 'agy',
  model: 'gemini-4-argon',
  effort: 'high',
  way: 'new',
  at: '2026-11-01T12:00:00Z',
}

const normalConv: ConvView = {
  id: 'conv-normal-1',
  purpose: 'operator',
  title: 'Webhook reliability across projects',
  agent: { cli: 'agy', model: 'gemini-4-argon', effort: 'high', pickedByYou: false },
  turns: 1,
  tokens: 1200,
  sandbox: { state: 'idle', network: 'internet' },
  entries: [
    { kind: 'you', id: 1, text: 'Check the webhook failures in the map.', at: '2026-11-01T11:59:00Z' },
    entry,
  ],
  cameOf: { tickets: [] },
}

describe('Normal conversation and project conversation chat actions (11.8, 20.1)', () => {
  it('offers Move to project backlog in a normal conversation outside a project without single-project buttons', () => {
    const html = renderToStaticMarkup(
      <Entries
        v={normalConv}
        asked={{ projects: [{ name: 'shop', key: 'SF' }, { name: 'crew', key: 'CREW' }] }}
        onDone={() => {}}
      />,
    )
    expect(html).toContain('Move to project backlog')
    expect(html).not.toContain('Tell the agent')
    expect(html).not.toContain('Make a ticket of this')
    expect(html).not.toContain('Remember this')
  })

  it('renders a Project selector in TicketForm when moving a normal conversation turn into a project backlog', () => {
    const html = renderToStaticMarkup(
      <TicketForm
        v={normalConv}
        e={entry}
        prev={normalConv.entries[0]}
        projects={[
          { name: 'shop', key: 'SF' },
          { name: 'crew', key: 'CREW' },
        ]}
        onDone={() => {}}
        onCancel={() => {}}
      />,
    )
    expect(html).toContain('aria-label="Move to project backlog"')
    expect(html).toContain('aria-label="Project"')
    expect(html).toContain('shop (SF)')
    expect(html).toContain('crew (CREW)')
    expect(html).toContain('>Move to backlog</button>')
  })

  it('renders Make a ticket of this without a Project selector when the conversation is already scoped to one project', () => {
    const scoped: ConvView = { ...normalConv, project: 'SF', projectName: 'shop' }
    const html = renderToStaticMarkup(<TicketForm v={scoped} e={entry} prev={normalConv.entries[0]} onDone={() => {}} onCancel={() => {}} />)
    expect(html).toContain('aria-label="Make a ticket of this"')
    expect(html).not.toContain('aria-label="Project"')
    expect(html).toContain('>Make the ticket</button>')
  })
})

const choiceV1: ChoicePromptSpec = {
  id: 'cache-mode',
  version: 1,
  replaced: true,
  question: 'Choose cache eviction mode',
  default: 'lru',
  choices: [
    { id: 'lru', label: 'LRU eviction' },
    { id: 'ttl', label: 'Fixed TTL' },
  ],
}

const choiceV2: ChoicePromptSpec = {
  ...choiceV1,
  version: 2,
  replaced: false,
  specRef: 'SPEC-001',
}

describe('Project Chat Rooms hardened cards, delivery state, and viewport (20.2)', () => {
  it('renders Superseded locked state on v1 choice card and active 1-click options on v2 with agent_interpreted override prompt', () => {
    const v1Html = renderToStaticMarkup(
      <ChoicePromptCard spec={choiceV1} busy={false} onSelect={() => {}} />,
    )
    expect(v1Html).toContain('v1')
    expect(v1Html).toContain('Superseded')
    expect(v1Html).toContain('disabled=""')

    const v2Html = renderToStaticMarkup(
      <ChoicePromptCard
        spec={choiceV2}
        response={{
          promptKey: 'cache-mode',
          promptVersion: 2,
          choice: 'ttl',
          provenance: 'agent_interpreted',
          sourceMessageId: 12,
        }}
        busy={false}
        onSelect={() => {}}
      />,
    )
    expect(v2Html).toContain('v2')
    expect(v2Html).toContain('agent_interpreted')
    expect(v2Html).toContain('src #12')
    expect(v2Html).toContain('(default)')
    expect(v2Html).toContain('[selected]')
    expect(v2Html).toContain('override directly')
  })

  it('renders addressee delivery state, @security sender, no-wake notice, offload pill, and @user interrupt toggle', () => {
    const roomData: RoomView = {
      channel: '#shop',
      maxAgentHops: 3,
      threads: [
        {
          id: 1,
          threadKey: 'feat/001-cache',
          title: 'Cache invalidation',
          ticketKeys: ['SF-1'],
          state: 'open',
          messageCount: 2,
          lastHopCount: 1,
        },
      ],
      messages: [
        {
          id: 41,
          senderRole: 'planner',
          senderHandle: '@planner',
          kind: 'choice_prompt',
          intent: 'question',
          choicePrompt: choiceV2,
          bodyRunes: 4501,
          offloaded: false,
          mentions: ['builder'],
          addressees: [
            {
              id: 7,
              recipientRole: 'builder',
              recipientHandle: '@builder',
              via: 'explicit',
              deliveryMode: 'turn',
              deliveryState: 'queued',
            },
          ],
          hopCount: 1,
          fenced: true,
          body: 'Please confirm cache eviction strategy.',
          actionable: true,
          createdAt: '2026-11-16T12:00:00Z',
        },
        {
          id: 42,
          senderRole: 'user',
          senderHandle: '@user',
          kind: 'choice_response',
          intent: 'inform',
          choiceResponse: {
            promptKey: 'cache-mode',
            promptVersion: 2,
            choice: 'lru',
            provenance: 'direct',
          },
          mentions: [],
          hopCount: 0,
          fenced: false,
          body: 'Decision DEC-002 (cache-mode v2): selected lru',
          actionable: false,
          createdAt: '2026-11-16T12:01:00Z',
        },
      ],
    }

    const html = renderToStaticMarkup(
      <ChatRoomsPanel
        project="shop"
        roomData={roomData}
        selectedThread=""
        onSelectThread={() => {}}
        onCreateThread={() => {}}
        onPostMessage={() => {}}
        onRespondChoice={() => {}}
        busy={false}
        deliveryNotice="Posted to room; nobody woken."
      />,
    )
    expect(html).toContain('crew-chat-viewport')
    expect(html).toContain('data-kb-open="false"')
    expect(html).toContain('Posted to room; nobody woken.')
    expect(html).toContain('choice_prompt')
    expect(html).toContain('@builder · turn/queued')
    expect(html).toContain('4501 runes')
    expect(html).toContain('direct · @user')
    expect(html).toContain('value="security"')
    expect(html).toContain('data-testid="chat-interrupt-toggle"')
    expect(html).not.toMatch(/style=/)
  })
})
