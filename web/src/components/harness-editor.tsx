import { useEffect, useState } from 'react'
import { getJSON, send } from '../lib/api'
import { button, Card, Failed, mainButton, Tag } from './ui'

export type GlobalHarnessEntry = {
  path: string
  kind: string
  name: string
  description?: string
  customized: boolean
  builtIn: boolean
  updatedAt?: string
}

export type BaselineFileItem = {
  path: string
  mayChange: boolean
  note?: string
  customized?: boolean
}

type GlobalFileDetail = {
  path: string
  kind: string
  content: string
  defaultContent: string
  customized: boolean
  builtIn: boolean
}

const STARTER_TEMPLATES: Record<'skill' | 'rule' | 'agent', (name: string) => string> = {
  skill: (name) =>
    `---\nname: ${name}\ndescription: Describe when and why to load the ${name} skill.\n---\n\n# ${name}\n\nInstructions for the agent when working with ${name}.\n`,
  rule: (name) =>
    `---\ntrigger: always_on\ndescription: Describe the rule enforced by ${name}.\n---\n\n# ${name}\n\nState the rule clearly.\n`,
  agent: (name) =>
    `---\nname: ${name}\ndescription: Describe when to dispatch the ${name} subagent.\n---\n\nYou are the ${name} subagent.\n`,
}

export function GlobalHarnessEditorSection({
  files,
  harnessFiles,
  onSaved,
}: {
  files: BaselineFileItem[]
  harnessFiles: GlobalHarnessEntry[]
  onSaved: () => void
}) {
  const [filter, setFilter] = useState('')
  const [kindFilter, setKindFilter] = useState<string>('all')
  const [editingPath, setEditingPath] = useState<string | null>(null)
  const [creatingKind, setCreatingKind] = useState<'skill' | 'rule' | 'agent' | null>(null)
  const [newName, setNewName] = useState('')
  const [detail, setDetail] = useState<GlobalFileDetail | null>(null)
  const [content, setContent] = useState('')
  const [loading, setLoading] = useState(false)
  const [saving, setSaving] = useState(false)
  const [failed, setFailed] = useState('')

  useEffect(() => {
    if (!editingPath) {
      setDetail(null)
      return
    }
    setLoading(true)
    setFailed('')
    getJSON<GlobalFileDetail>(`/api/crew/baseline/file?path=${encodeURIComponent(editingPath)}`)
      .then((d) => {
        setDetail(d)
        setContent(d.content)
      })
      .catch((e: Error) => setFailed(e.message))
      .finally(() => setLoading(false))
  }, [editingPath])

  const startCreate = (k: 'skill' | 'rule' | 'agent') => {
    setCreatingKind(k)
    setNewName('')
    setEditingPath(null)
    setDetail(null)
    setContent(STARTER_TEMPLATES[k]('my-' + k))
    setFailed('')
  }

  const saveGlobal = async () => {
    const targetPath =
      creatingKind === 'skill'
        ? `harness/skills/${newName.trim()}/SKILL.md`
        : creatingKind === 'rule'
          ? `harness/rules/${newName.trim()}.md`
          : creatingKind === 'agent'
            ? `harness/agents/${newName.trim()}.md`
            : editingPath
    if (!targetPath) return
    setSaving(true)
    setFailed('')
    try {
      await send('PUT', '/api/crew/baseline/file', { path: targetPath, content })
      setCreatingKind(null)
      setEditingPath(targetPath)
      onSaved()
    } catch (e) {
      setFailed((e as Error).message)
    } finally {
      setSaving(false)
    }
  }

  const resetGlobal = async () => {
    if (!editingPath) return
    setSaving(true)
    setFailed('')
    try {
      await send('DELETE', `/api/crew/baseline/file?path=${encodeURIComponent(editingPath)}`)
      setEditingPath(null)
      onSaved()
    } catch (e) {
      setFailed((e as Error).message)
    } finally {
      setSaving(false)
    }
  }

  const q = filter.trim().toLowerCase()
  const visibleHarness = harnessFiles.filter(
    (h) =>
      (kindFilter === 'all' || h.kind === kindFilter) &&
      (!q || h.name.toLowerCase().includes(q) || h.path.toLowerCase().includes(q) || (h.description ?? '').toLowerCase().includes(q)),
  )

  return (
    <div className="grid gap-3">
      <Card title="Global harness (skills, rules, subagents & context)">
        <div className="mb-3 flex flex-wrap items-center justify-between gap-2">
          <div className="flex flex-wrap items-center gap-1.5">
            {(['all', 'context', 'skill', 'rule', 'agent'] as const).map((k) => (
              <button
                key={k}
                type="button"
                onClick={() => setKindFilter(k)}
                className={`rounded px-2.5 py-1 text-xs font-medium ${kindFilter === k ? 'bg-text text-bg' : 'border border-border text-text-dim hover:text-text'}`}
              >
                {k === 'all' ? 'All' : k + 's'}
              </button>
            ))}
            <input
              type="search"
              value={filter}
              onChange={(e) => setFilter(e.target.value)}
              placeholder="Filter skills, rules, subagents..."
              className="ml-1 rounded border border-border bg-bg px-2.5 py-1 text-xs"
            />
          </div>
          <div className="flex flex-wrap gap-1.5">
            <button type="button" className={button} onClick={() => startCreate('skill')} data-add-global="skill">
              + Add global skill
            </button>
            <button type="button" className={button} onClick={() => startCreate('rule')} data-add-global="rule">
              + Add global rule
            </button>
            <button type="button" className={button} onClick={() => startCreate('agent')} data-add-global="agent">
              + Add subagent
            </button>
          </div>
        </div>
        <div className="max-h-80 overflow-y-auto">
          <table className="w-full text-sm" data-global-harness>
            <thead className="text-left text-xs tracking-wide text-text-dim uppercase">
              <tr className="border-b border-border">
                <th className="py-1 pr-2">Kind</th>
                <th className="py-1 pr-2">Name / Path</th>
                <th className="py-1 pr-2">Description</th>
                <th className="py-1 text-right">Action</th>
              </tr>
            </thead>
            <tbody>
              {visibleHarness.map((h) => (
                <tr key={h.path} className="border-b border-border/60 align-top" data-harness-item={h.path}>
                  <td className="py-1.5 pr-2 whitespace-nowrap">
                    <Tag>{h.kind}</Tag> {h.customized && <Tag tone="blue">customized</Tag>}
                  </td>
                  <td className="py-1.5 pr-2 font-mono text-xs">
                    <span className="font-semibold text-text">{h.name}</span>
                    <span className="block text-text-dim">{h.path}</span>
                  </td>
                  <td className="py-1.5 pr-2 text-xs text-text-dim">{h.description || '—'}</td>
                  <td className="py-1.5 text-right whitespace-nowrap">
                    <button
                      type="button"
                      className="text-xs font-medium text-accent underline"
                      onClick={() => {
                        setCreatingKind(null)
                        setEditingPath(h.path)
                      }}
                    >
                      View / Edit
                    </button>
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      </Card>

      <Card title="Baseline stage & role prompts">
        <div className="max-h-72 overflow-y-auto">
          <table className="w-full text-sm" data-baseline-files>
            <thead className="text-left text-xs tracking-wide text-text-dim uppercase">
              <tr className="border-b border-border">
                <th className="py-1 pr-2">File</th>
                <th className="py-1 pr-2">Rule</th>
                <th className="py-1 text-right">Action</th>
              </tr>
            </thead>
            <tbody>
              {files.map((f) => (
                <tr key={f.path} className="border-b border-border/60" data-baseline-file={f.path}>
                  <td className="py-1.5 pr-2 font-mono text-xs">
                    {f.path} {f.customized && <Tag tone="blue">customized</Tag>}
                  </td>
                  <td className="py-1.5 pr-2 text-xs text-text-dim">{f.note || 'stage or role prompt'}</td>
                  <td className="py-1.5 text-right whitespace-nowrap">
                    <button
                      type="button"
                      className="text-xs font-medium text-accent underline"
                      onClick={() => {
                        setCreatingKind(null)
                        setEditingPath(f.path)
                      }}
                    >
                      View / Edit
                    </button>
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      </Card>

      {(editingPath || creatingKind) && (
        <div className="fixed inset-0 z-50 flex items-center justify-center bg-black/60 p-4" role="dialog" aria-label="Edit global harness file">
          <div className="flex max-h-[88vh] w-full max-w-4xl flex-col rounded-lg border border-border bg-bg p-5 shadow-xl">
            <div className="mb-3 flex items-center justify-between gap-2 border-b border-border pb-2">
              <div>
                <h3 className="font-mono text-sm font-bold">
                  {creatingKind ? `New global ${creatingKind}` : editingPath}
                </h3>
                {detail?.customized && <span className="text-xs text-accent">Customized on this machine (applies immediately to new runs)</span>}
              </div>
              <button
                type="button"
                className={button}
                onClick={() => {
                  setEditingPath(null)
                  setCreatingKind(null)
                }}
              >
                Close
              </button>
            </div>
            <Failed error={failed} />
            {creatingKind && (
              <label className="mb-3 grid gap-1 text-xs">
                <span className="text-text-dim">Name (letters, digits, hyphens; must match frontmatter when applicable)</span>
                <input
                  value={newName}
                  onChange={(e) => {
                    const v = e.target.value
                    setNewName(v)
                    if (v.trim()) setContent(STARTER_TEMPLATES[creatingKind](v.trim()))
                  }}
                  placeholder={`my-${creatingKind}`}
                  className="w-72 rounded border border-border bg-bg-sunk px-2.5 py-1 font-mono text-xs"
                />
              </label>
            )}
            {loading ? (
              <p className="py-8 text-sm text-text-dim">Loading file...</p>
            ) : (
              <textarea
                value={content}
                onChange={(e) => setContent(e.target.value)}
                className="min-h-[380px] flex-1 rounded border border-border bg-bg-sunk p-3 font-mono text-xs leading-5"
              />
            )}
            <div className="mt-3 flex flex-wrap items-center justify-between gap-2">
              <div className="flex gap-2">
                <button
                  type="button"
                  className={mainButton}
                  disabled={saving || loading || !content.trim() || (!!creatingKind && !newName.trim())}
                  onClick={saveGlobal}
                >
                  {saving ? 'Saving...' : 'Save to global harness'}
                </button>
                {detail?.customized && (
                  <button type="button" className={button} disabled={saving} onClick={resetGlobal}>
                    {detail.builtIn ? 'Reset to built-in default' : 'Delete custom file'}
                  </button>
                )}
              </div>
              <span className="text-xs text-text-dim">
                Saved files take effect on the next run without rebuilding the runtime image.
              </span>
            </div>
          </div>
        </div>
      )}
    </div>
  )
}
