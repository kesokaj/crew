import { createFileRoute, Link } from '@tanstack/react-router'
import { useState, type FormEvent } from 'react'
import { getJSON, send } from '../lib/api'
import { usePoll } from '../lib/poll'
import { Card, Failed, PageHead, Tag, button, mainButton } from '../components/ui'

export const Route = createFileRoute('/p/$project/skills')({ component: SkillsRegistry })

type RegistryEntry = {
  id: string
  tier: 'base' | 'shared' | 'project' | 'dream' | 'vendored' | string
  name: string
  path: string
  project?: string
  description: string
  ticket?: string
  sourceUrl?: string
  pinnedSha?: string
  licence?: string
  scanVerdict: string
  scanFindings?: string[]
  promptBytes: number
  tokenFootprint: number
}

type SkillsRegistryView = {
  entries: RegistryEntry[]
  tierCounts: Record<string, number>
  vendoredMarkdown: string
  warmSyncPath: string
}

const tierLabels: [string, string][] = [
  ['all', 'All tiers'],
  ['base', 'Base'],
  ['shared', 'Cross-Factory Shared'],
  ['project', 'Project & Dreamed'],
  ['vendored', 'Vendored'],
]

function SkillsRegistry() {
  const { project } = Route.useParams()
  const enc = encodeURIComponent(project)
  const { data, error, reload } = usePoll<SkillsRegistryView>(`/api/projects/${enc}/skills-registry`, 8000)

  const [tierFilter, setTierFilter] = useState('all')
  const [search, setSearch] = useState('')
  const [inspected, setInspected] = useState<{ title: string; path: string; content: string } | null>(null)
  const [reqName, setReqName] = useState('')
  const [reqGoal, setReqGoal] = useState('')
  const [actionMsg, setActionMsg] = useState('')
  const [actionErr, setActionErr] = useState('')

  const entries = data?.entries ?? []
  const counts = data?.tierCounts ?? {}
  const q = search.trim().toLowerCase()
  const visible = entries.filter((e) => {
    const matchTier =
      tierFilter === 'all' ||
      e.tier === tierFilter ||
      (tierFilter === 'project' && (e.tier === 'project' || e.tier === 'dream'))
    const matchText = !q || e.name.toLowerCase().includes(q) || e.description.toLowerCase().includes(q) || e.path.toLowerCase().includes(q)
    return matchTier && matchText
  })

  const inspectSkill = async (e: RegistryEntry) => {
    setActionErr('')
    const targetProj = encodeURIComponent(e.project || project)
    const filePath = e.tier === 'base' ? `harness/${e.path}` : e.path
    try {
      const res = await getJSON<{ path: string; content: string }>(
        `/api/projects/${targetProj}/skills/file?path=${encodeURIComponent(filePath)}`,
      )
      setInspected({ title: e.name, path: res.path, content: res.content })
    } catch {
      setInspected({ title: e.name, path: e.path, content: e.description })
    }
  }

  const requestSkill = async (ev: FormEvent) => {
    ev.preventDefault()
    if (!reqName.trim() || !reqGoal.trim()) return
    setActionErr('')
    setActionMsg('')
    try {
      const res = await send<{ ticket: string; joined?: boolean }>('POST', `/api/projects/${enc}/skills/request`, {
        name: reqName.trim(),
        need: reqGoal.trim(),
      })
      setReqName('')
      setReqGoal('')
      setActionMsg(res ? `Tracked skill request ${res.ticket}${res.joined ? ' (joined existing request)' : ''}` : 'Skill request queued')
      reload()
    } catch (err) {
      setActionErr((err as Error).message)
    }
  }

  return (
    <div className="mx-auto max-w-7xl space-y-5 p-6">
      <PageHead
        crumbs={
          <>
            <Link to="/" className="text-accent">
              All projects
            </Link>{' '}
            / <span data-free-text>{project}</span>
          </>
        }
        title="Skills Registry"
        actions={
          <div className="flex flex-wrap gap-2">
            <Link to="/p/$project/factory" params={{ project }} className={button}>
              Factory Line
            </Link>
            <Link to="/p/$project/settings" params={{ project }} className={button}>
              Project Settings
            </Link>
          </div>
        }
      >
        4-tier vetted catalog (Base, Cross-Factory Shared, Project &amp; Dreamed, SHA-pinned Vendored) synced to warm microVM path{' '}
        <code data-free-text>{data?.warmSyncPath ?? `<CREW_DATA_DIR>/projects/${project}/home/.gemini/config/skills/`}</code>.
      </PageHead>

      <Failed error={error || actionErr} />
      {actionMsg && (
        <p className="rounded-md border border-ok bg-bg-raised px-3 py-2 text-sm text-ok" data-free-text>
          {actionMsg}
        </p>
      )}

      <div className="flex flex-wrap items-center gap-2">
        {tierLabels.map(([id, label]) => {
          const n =
            id === 'all'
              ? entries.length
              : id === 'project'
                ? (counts.project ?? 0) + (counts.dream ?? 0)
                : (counts[id] ?? 0)
          return (
            <button
              key={id}
              type="button"
              data-tier-filter={id}
              onClick={() => setTierFilter(id)}
              className={tierFilter === id ? mainButton : button}
            >
              {label} ({n})
            </button>
          )
        })}
        <input
          aria-label="Filter skills"
          placeholder="Search skill name or description…"
          value={search}
          onChange={(e) => setSearch(e.target.value)}
          className="w-full rounded-md border border-border bg-bg px-3 py-1.5 text-sm sm:ml-auto sm:w-56"
        />
      </div>

      <div className="grid gap-4 lg:grid-cols-[minmax(0,1fr)_340px]">
        <div className="min-w-0 space-y-3">
          {visible.length === 0 ? (
            <p className="text-sm text-text-dim">No skills match the active tier filter.</p>
          ) : (
            <div className="grid gap-3 md:grid-cols-2">
              {visible.map((s) => (
                <div
                  key={s.id}
                  data-skill-tier={s.tier}
                  data-testid={`skill-card-${s.name}`}
                  className="flex flex-col justify-between rounded-lg border border-border bg-bg-raised p-4"
                >
                  <div>
                    <div className="flex flex-wrap items-center gap-2">
                      <span className="font-semibold" data-free-text>
                        {s.name}
                      </span>
                      <Tag tone={s.tier === 'vendored' ? 'amber' : s.tier === 'dream' || s.tier === 'shared' ? 'blue' : undefined}>
                        {s.tier}
                      </Tag>
                      <span data-testid={`skill-verdict-${s.name}`}>
                        <Tag tone={s.scanVerdict === 'PASS' ? 'ok' : 'bad'}>{s.scanVerdict}</Tag>
                      </span>
                    </div>
                    <p className="mt-1.5 text-xs text-text-dim" data-free-text>
                      {s.description || s.path}
                    </p>
                    {s.scanFindings && s.scanFindings.length > 0 && (
                      <p data-testid={`skill-findings-${s.name}`} className="mt-1 text-xs text-bad" data-free-text>
                        {s.scanFindings.join(' · ')}
                      </p>
                    )}
                    <p className="mt-2 font-mono text-[11px] text-text-dim" data-free-text>
                      Prompt index size: {s.promptBytes} B · {s.tokenFootprint} tokens
                    </p>
                    {s.pinnedSha && (
                      <p className="mt-1 font-mono text-[11px] text-accent" data-free-text>
                        SHA {s.pinnedSha} · {s.licence || 'MIT'}
                      </p>
                    )}
                  </div>
                  <div className="mt-3 flex flex-wrap items-center justify-between gap-2 border-t border-border pt-2">
                    <span className="truncate font-mono text-[11px] text-text-dim" data-free-text>
                      {s.project ? `${s.project}: ` : ''}
                      {s.path}
                    </span>
                    <button type="button" className={button} onClick={() => void inspectSkill(s)}>
                      Inspect
                    </button>
                  </div>
                </div>
              ))}
            </div>
          )}
        </div>

        <aside className="min-w-0 space-y-4">
          {inspected && (
            <Card
              title={`SKILL.md · ${inspected.title}`}
              right={
                <button type="button" className={button} onClick={() => setInspected(null)}>
                  Close
                </button>
              }
            >
              <p className="mb-2 font-mono text-xs text-text-dim" data-free-text>
                {inspected.path}
              </p>
              <pre className="max-h-72 overflow-auto rounded border border-border bg-bg p-3 font-mono text-xs" data-free-text>
                {inspected.content}
              </pre>
            </Card>
          )}

          <Card title="VENDORED.md SHA Pin Manifest">
            <pre data-testid="vendored-manifest" className="max-h-56 overflow-auto rounded border border-border bg-bg p-3 font-mono text-xs" data-free-text>
              {data?.vendoredMarkdown || 'No .agents/skills/VENDORED.md pinned on main yet.'}
            </pre>
          </Card>

          <Card title="Request New Project Skill">
            <form onSubmit={(e) => void requestSkill(e)} className="space-y-2 text-sm">
              <input
                aria-label="Requested skill name"
                placeholder="Skill identifier (e.g. sql-migrations)"
                value={reqName}
                onChange={(e) => setReqName(e.target.value)}
                className="w-full rounded-md border border-border bg-bg px-2.5 py-1.5"
              />
              <textarea
                aria-label="Skill need"
                rows={3}
                placeholder="Describe when builders should load this skill…"
                value={reqGoal}
                onChange={(e) => setReqGoal(e.target.value)}
                className="w-full rounded-md border border-border bg-bg px-2.5 py-1.5"
              />
              <button type="submit" disabled={!reqName.trim() || !reqGoal.trim()} className={button}>
                Queue skill request
              </button>
            </form>
          </Card>
        </aside>
      </div>
    </div>
  )
}
