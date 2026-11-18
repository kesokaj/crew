import type { ReactNode } from 'react'
import { renderToStaticMarkup } from 'react-dom/server'
import { describe, expect, it, vi } from 'vitest'

vi.mock('@tanstack/react-router', async (importOriginal) => {
  const actual = await importOriginal<typeof import('@tanstack/react-router')>()
  return {
    ...actual,
    createFileRoute: () => (opts: unknown) => opts,
    Link: ({
      to,
      params,
      hash,
      className,
      children,
    }: {
      to: string
      params?: Record<string, string>
      hash?: string
      className?: string
      children: ReactNode
    }) => {
      const href =
        Object.entries(params ?? {}).reduce((acc, [k, v]) => acc.replace(`$${k}`, encodeURIComponent(v)), to) +
        (hash ? `#${hash}` : '')
      return (
        <a href={href} className={className}>
          {children}
        </a>
      )
    },
  }
})

import { ProjectSandboxBarView } from './project-sandbox-controls'
import { SandboxesScreenView } from '../routes/sandboxes'

const noop = () => {}

describe('ProjectSandboxBarView', () => {
  it('renders DOWN · 0 MB and Start VM when stopped', () => {
    const html = renderToStaticMarkup(
      <ProjectSandboxBarView
        data={{
          projectId: 'p1',
          sandboxId: '',
          state: 'stopped',
          imageTag: '',
          snapshotId: '',
          activeSessions: 0,
          snapshots: [],
        }}
        busy={false}
        error=""
        onStart={noop}
        onSuspend={noop}
        onStop={noop}
        onSnapshot={noop}
      />,
    )
    expect(html).toContain('data-project-sandbox="stopped"')
    expect(html).toContain('DOWN · 0 MB')
    expect(html).toContain('Start VM')
    expect(html).toContain('Snapshot')
  })

  it('renders UP with active ACP sessions and disables Suspend while sessions run', () => {
    const html = renderToStaticMarkup(
      <ProjectSandboxBarView
        project="shop"
        data={{
          projectId: 'p1',
          sandboxId: 'proj-p1-0001',
          state: 'running',
          imageTag: 'crew-project-runtime:shp-123456',
          snapshotId: '',
          activeSessions: 2,
          cpus: 8,
          memoryGB: 12,
          diskGB: 40,
          snapshots: [],
        }}
        busy={false}
        error=""
        onStart={noop}
        onSuspend={noop}
        onStop={noop}
        onSnapshot={noop}
      />,
    )
    expect(html).toContain('data-project-sandbox="running"')
    expect(html).toContain('UP · 2 ACP sessions')
    expect(html).toContain('8 vCPUs · 12 GB RAM · 40 GB disk')
    expect(html).toContain('proj-p1-0001')
    expect(html).toContain('image: crew-project-runtime:shp-123456')
    expect(html).toContain('href="/p/shop/settings#sandbox"')
    expect(html).toMatch(/<button[^>]*disabled=""[^>]*>Suspend<\/button>/)
  })

  it('renders SUSPENDED · 0% CPU and Resume VM when paused', () => {
    const html = renderToStaticMarkup(
      <ProjectSandboxBarView
        data={{
          projectId: 'p1',
          sandboxId: 'proj-p1-0001',
          state: 'paused',
          imageTag: '',
          snapshotId: 'snap-1',
          activeSessions: 0,
          snapshots: [],
        }}
        busy={false}
        error=""
        onStart={noop}
        onSuspend={noop}
        onStop={noop}
        onSnapshot={noop}
      />,
    )
    expect(html).toContain('data-project-sandbox="paused"')
    expect(html).toContain('SUSPENDED · 0% CPU')
    expect(html).toContain('Resume VM')
    expect(html).toContain('Stop · 0 MB')
  })

  it('renders SandboxesScreenView with machine capacity, active-ticket limits, project microVMs, and ephemeral sandboxes', () => {
    const html = renderToStaticMarkup(
      <SandboxesScreenView
        data={{
          machine: {
            cores: 8,
            totalMemMB: 32768,
            roomCPUs: 12,
            roomMemMB: 28672,
            usedCPUs: 4,
            usedMemMB: 10240,
            activeTickets: 2,
            maxActiveTickets: 6,
            setMaxActiveTickets: 6,
          },
          projectSandboxes: [
            {
              project: 'shop',
              projectId: 'p1',
              projectKey: 'SF',
              projectState: 'running',
              sandboxId: 'proj-p1',
              container: 'crew-proj-sf',
              state: 'running',
              activeSessions: 1,
              activeTickets: 2,
              maxActiveTickets: 2,
              cpus: 4,
              usedCPUs: 4,
              cpuPercent: 37.5,
              memoryGB: 6,
              memUsedMB: 2048,
              memLimitMB: 6144,
              diskGB: 20,
              diskUsedMB: 512,
              snapshotsCount: 2,
              snapshotsBytes: 4 * 1024 * 1024,
              maxSnapshots: 5,
              suspendAfterMinutes: 5,
              stopAfterMinutes: 30,
              imageTag: 'crew-project-runtime:sf-1234',
              runs: [
                {
                  run: 'r1',
                  role: 'builder',
                  cli: 'agy',
                  model: 'gemini-4-argon',
                  subtask: 'SF-1.1',
                  ticket: 'SF-1',
                  title: 'Build checkout',
                  state: 'working',
                  tokens: 1200,
                },
              ],
            },
          ],
          ephemeralSandboxes: [
            {
              id: 'chk-1',
              container: 'crew-sb-chk-1',
              kind: 'check',
              project: 'shop',
              detail: 'SF-1 exit checks',
              cpus: 4,
              cpuPercent: 12.0,
              memUsedMB: 1024,
              memLimitMB: 6144,
              startedAt: '2026-11-06T20:00:00Z',
            },
          ],
          waiting: [],
        }}
        busyKey=""
        onProjectAction={noop}
        onSaveMaxTickets={noop}
      />,
    )
    expect(html).toContain('data-sandboxes-screen')
    expect(html).toContain('2 of 6')
    expect(html).toContain('crew-proj-sf')
    expect(html).toContain('37.5% · 4 / 4 vCPUs')
    expect(html).toContain('2048 / 6144 MB')
    expect(html).toContain('512 MB / 20 GB')
    expect(html).toContain('2 / 5 snapshots (4.0 MB)')
    expect(html).toContain('2 / 2 active tickets')
    expect(html).toContain('href="/p/shop/settings#sandbox"')
    expect(html).toContain('crew-sb-chk-1')
    expect(html.toLowerCase()).not.toMatch(/budget|cost|estimate|\$\s*\d/)
  })
})
