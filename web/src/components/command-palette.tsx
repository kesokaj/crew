import { useNavigate } from '@tanstack/react-router'
import { useEffect, useRef, useState } from 'react'
import { getJSON, type AllSandboxesView, type Frame } from '../lib/api'
import { stageName } from '../lib/words'

export type CommandItem = {
  id: string
  category: 'Project' | 'Ticket' | 'Sandbox' | 'Project screen' | 'Navigation'
  title: string
  subtitle?: string
  freeText?: boolean
  href: string
}

type TicketSummaryRow = {
  key: string
  title: string
  stage: string
}

const GLOBAL_ROUTES: { title: string; subtitle: string; href: string }[] = [
  { title: 'All projects', subtitle: 'Overview & capacity', href: '/' },
  { title: 'Plans', subtitle: 'Plan drafts', href: '/plans' },
  { title: 'New project', subtitle: 'Create a project', href: '/projects/new' },
  { title: 'Sandboxes', subtitle: 'All project microVMs & ephemeral sandboxes', href: '/sandboxes' },
  { title: 'Inbox', subtitle: 'Items waiting for you', href: '/inbox' },
  { title: 'Conversations', subtitle: 'Normal & project conversations', href: '/conversations' },
  { title: 'Crew', subtitle: 'Signals, baseline & improvements', href: '/crew' },
  { title: 'Versions', subtitle: 'Releases, gate & rollbacks', href: '/versions' },
  { title: 'Agent CLIs', subtitle: 'Sign-in & subscription limits', href: '/clis' },
  { title: 'Models', subtitle: 'Model choices & comparisons', href: '/models' },
  { title: 'Database', subtitle: 'Tables, size & backups', href: '/database' },
  { title: 'Crew MCP', subtitle: 'Tools, calls & personal tokens', href: '/crew-mcp' },
  { title: 'Notifications', subtitle: 'Mail & daily digest', href: '/notifications' },
  { title: 'Machine settings', subtitle: 'Global Crew configuration', href: '/settings' },
  { title: 'Runtimes & Storage', subtitle: 'ComputeRuntime Kata, StateDB WAL & BlobStore', href: '/storage' },
  { title: 'Setup', subtitle: 'Machine check & credentials', href: '/setup' },
]

const PROJECT_SCREENS: [string, string][] = [
  ['factory', 'Factory Line'],
  ['board', 'Board'],
  ['tickets', 'Tickets'],
  ['specs', 'Specs & WORKFLOW'],
  ['chat', 'Project Chat'],
  ['skills', 'Skills Registry'],
  ['terminal', 'Terminal'],
  ['plan', 'Plan'],
  ['activity', 'Activity'],
  ['agents', 'Agents'],
  ['docs', 'Documentation'],
  ['summaries', 'Summaries'],
  ['trajectories', 'Trajectories'],
  ['knowledge', 'Knowledge'],
  ['analytics', 'Analytics'],
  ['hosting', 'Hosting'],
  ['git', 'Git'],
  ['settings', 'Settings'],
]

export function buildCommandItems(
  f: Frame | null,
  activeProject?: string,
  ticketsByProject: Record<string, TicketSummaryRow[]> = {},
  sandboxes?: Pick<AllSandboxesView, 'projectSandboxes' | 'ephemeralSandboxes'> | null,
): CommandItem[] {
  const items: CommandItem[] = []

  if (activeProject) {
    for (const [sub, label] of PROJECT_SCREENS) {
      items.push({
        id: `proj-screen:${activeProject}:${sub}`,
        category: 'Project screen',
        title: `${activeProject} › ${label}`,
        subtitle: `/p/${activeProject}/${sub}`,
        href: `/p/${encodeURIComponent(activeProject)}/${sub}`,
      })
    }
  }

  for (const p of f?.projects ?? []) {
    items.push({
      id: `project:${p.name}`,
      category: 'Project',
      title: p.name,
      subtitle: `${p.key} · ${p.state}${p.working > 0 ? ` · ${p.working} running` : ''}`,
      freeText: true,
      href: `/p/${encodeURIComponent(p.name)}/board`,
    })
  }

  for (const [proj, rows] of Object.entries(ticketsByProject)) {
    for (const t of rows) {
      items.push({
        id: `ticket:${proj}:${t.key}`,
        category: 'Ticket',
        title: `${t.key} — ${t.title}`,
        subtitle: `${proj} · ${stageName[t.stage] ?? t.stage}`,
        freeText: true,
        href: `/p/${encodeURIComponent(proj)}/tickets/${encodeURIComponent(t.key)}`,
      })
    }
  }

  for (const sb of sandboxes?.projectSandboxes ?? []) {
    items.push({
      id: `sandbox:proj:${sb.projectKey}`,
      category: 'Sandbox',
      title: `${sb.container} — ${sb.project}`,
      subtitle: `${sb.state} · ${sb.activeSessions} sessions · ${sb.cpus} vCPU`,
      freeText: true,
      href: '/sandboxes',
    })
  }

  for (const ep of sandboxes?.ephemeralSandboxes ?? []) {
    items.push({
      id: `sandbox:eph:${ep.id}`,
      category: 'Sandbox',
      title: ep.detail ? `${ep.container} — ${ep.detail}` : ep.container,
      subtitle: `${ep.kind}${ep.project ? ` · ${ep.project}` : ''}`,
      freeText: true,
      href: '/sandboxes',
    })
  }

  for (const r of GLOBAL_ROUTES) {
    items.push({
      id: `nav:${r.href}`,
      category: 'Navigation',
      title: r.title,
      subtitle: r.subtitle,
      href: r.href,
    })
  }

  return items
}

export function filterCommandItems(items: CommandItem[], query: string, limit = 30): CommandItem[] {
  const q = query.trim().toLowerCase()
  if (!q) return items.slice(0, limit)
  const terms = q.split(/\s+/).filter(Boolean)
  return items
    .filter((item) => {
      const hay = `${item.category} ${item.title} ${item.subtitle ?? ''} ${item.href}`.toLowerCase()
      return terms.every((t) => hay.includes(t))
    })
    .slice(0, limit)
}

export function CommandPalette({
  open,
  onClose,
  frame,
  activeProject,
}: {
  open: boolean
  onClose: () => void
  frame: Frame | null
  activeProject?: string
}) {
  const navigate = useNavigate()
  const [query, setQuery] = useState('')
  const [selectedIndex, setSelectedIndex] = useState(0)
  const [ticketsByProject, setTicketsByProject] = useState<Record<string, TicketSummaryRow[]>>({})
  const [sandboxes, setSandboxes] = useState<AllSandboxesView | null>(null)
  const inputRef = useRef<HTMLInputElement | null>(null)

  useEffect(() => {
    if (!open) return
    setQuery('')
    setSelectedIndex(0)
    setTimeout(() => inputRef.current?.focus(), 0)
    const projectsToLoad = activeProject ? [activeProject] : (frame?.projects ?? []).slice(0, 5).map((p) => p.name)
    let cancelled = false
    for (const proj of projectsToLoad) {
      getJSON<{ tickets: TicketSummaryRow[] }>(`/api/projects/${encodeURIComponent(proj)}/tickets`).then(
        (res) => { if (!cancelled && res?.tickets) setTicketsByProject((prev) => ({ ...prev, [proj]: res.tickets.slice(0, 50) })) },
        () => {},
      )
    }
    getJSON<AllSandboxesView>('/api/sandboxes').then(
      (res) => { if (!cancelled && res) setSandboxes(res) },
      () => {},
    )
    return () => { cancelled = true }
  }, [open, activeProject, frame])

  if (!open) return null
  const results = filterCommandItems(buildCommandItems(frame, activeProject, ticketsByProject, sandboxes), query)
  const clampedIndex = results.length === 0 ? 0 : Math.min(selectedIndex, results.length - 1)
  const choose = (item: CommandItem) => {
    onClose()
    void navigate({ to: item.href })
  }

  return (
    <div
      role="dialog"
      aria-modal="true"
      aria-label="Command palette"
      className="fixed inset-0 z-50 flex items-start justify-center bg-black/50 p-4 pt-16"
      onClick={(e) => { if (e.target === e.currentTarget) onClose() }}
      onKeyDown={(e) => {
        if (e.key === 'Escape') { e.preventDefault(); onClose() }
        else if (e.key === 'ArrowDown') { e.preventDefault(); setSelectedIndex((i) => (results.length ? (i + 1) % results.length : 0)) }
        else if (e.key === 'ArrowUp') { e.preventDefault(); setSelectedIndex((i) => (results.length ? (i - 1 + results.length) % results.length : 0)) }
        else if (e.key === 'Enter' && results[clampedIndex]) { e.preventDefault(); choose(results[clampedIndex]!) }
      }}
    >
      <div className="w-full max-w-xl overflow-hidden rounded-xl border border-border bg-bg-raised shadow-2xl">
        <div className="flex items-center gap-2 border-b border-border px-3 py-2.5">
          <span className="font-mono text-xs text-text-dim" aria-hidden="true">⌘K</span>
          <input
            ref={inputRef}
            type="search"
            aria-label="Command palette search"
            placeholder="Jump to project, ticket, sandbox, or screen…"
            value={query}
            onChange={(e) => { setQuery(e.target.value); setSelectedIndex(0) }}
            className="min-w-0 flex-1 bg-transparent text-sm text-text outline-none placeholder:text-text-dim"
          />
          <button type="button" onClick={onClose} className="min-h-8 rounded-md px-2 py-1 font-mono text-xs text-text-dim hover:bg-bg-sunk hover:text-text">
            Esc
          </button>
        </div>
        <CommandResultsList results={results} clampedIndex={clampedIndex} onHover={setSelectedIndex} onChoose={choose} />
      </div>
    </div>
  )
}

function CommandResultsList({
  results,
  clampedIndex,
  onHover,
  onChoose,
}: {
  results: CommandItem[]
  clampedIndex: number
  onHover: (idx: number) => void
  onChoose: (item: CommandItem) => void
}) {
  return (
    <ul role="listbox" aria-label="Command results" className="max-h-96 overflow-y-auto p-1.5">
      {results.length === 0 && <li className="px-3 py-6 text-center text-sm text-text-dim">No matching project, ticket, sandbox, or screen.</li>}
      {results.map((item, idx) => {
        const active = idx === clampedIndex
        return (
          <li key={item.id} role="option" aria-selected={active}>
            <button
              type="button"
              onMouseEnter={() => onHover(idx)}
              onClick={() => onChoose(item)}
              className={'flex w-full items-center justify-between gap-3 rounded-lg px-3 py-2 text-left text-sm transition-colors ' + (active ? 'bg-accent-soft text-accent' : 'text-text hover:bg-bg-sunk')}
            >
              <div className="min-w-0 flex-1">
                <div className="truncate font-medium" data-free-text={item.freeText ? true : undefined}>{item.title}</div>
                {item.subtitle && <div className="truncate font-mono text-xs text-text-dim" data-free-text={item.freeText ? true : undefined}>{item.subtitle}</div>}
              </div>
              <span className="shrink-0 rounded bg-bg-sunk px-1.5 py-0.5 font-mono text-[11px] text-text-dim">{item.category}</span>
            </button>
          </li>
        )
      })}
    </ul>
  )
}
