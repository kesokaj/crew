import { useEffect, useState } from 'react'
import { Link } from '@tanstack/react-router'
import { getJSON, send, type LayerItem, type SkillsView } from '../lib/api'
import { usePoll } from '../lib/poll'
import { stageName } from '../lib/words'
import { button, Card, Failed, mainButton, Tag } from './ui'

const PROJECT_TEMPLATES: Record<'context' | 'skill' | 'rule', (name: string) => string> = {
  context: (project) =>
    `# Working on ${project}\n\nProject-specific conventions, commands, and architectural rules for agents working in ${project}.\n`,
  skill: (name) =>
    `---\nname: ${name}\ndescription: Describe when and why to load the ${name} skill in this project.\n---\n\n# ${name}\n\nStep-by-step instructions and patterns for ${name}.\n`,
  rule: (name) =>
    `---\ntrigger: always_on\ndescription: Describe the project rule enforced by ${name}.\n---\n\n# ${name}\n\nState the project rule clearly.\n`,
}

export function ProjectSkillsSection({ project, base }: { project: string; base: string }) {
  const { data, error, reload } = usePoll<SkillsView>(base + '/skills', 30000)
  const counts = data?.baseCounts ?? {}
  const [baseFilter, setBaseFilter] = useState('')
  const [modalMode, setModalMode] = useState<'view-base' | 'edit-project' | 'create-skill' | 'create-rule' | 'request-skill' | null>(null)
  const [targetPath, setTargetPath] = useState('')
  const [newName, setNewName] = useState('')
  const [content, setContent] = useState('')
  const [missingSkill, setMissingSkill] = useState('')
  const [wantedSkill, setWantedSkill] = useState('')
  const [loading, setLoading] = useState(false)
  const [saving, setSaving] = useState(false)
  const [failed, setFailed] = useState('')
  const [notice, setNotice] = useState('')

  useEffect(() => {
    if (modalMode !== 'view-base' && modalMode !== 'edit-project') return
    if (!targetPath) return
    setLoading(true)
    setFailed('')
    const isBase = modalMode === 'view-base'
    getJSON<{ content: string }>(`${base}/skills/file?path=${encodeURIComponent(targetPath)}${isBase ? '&base=1' : ''}`)
      .then((res) => setContent(res.content))
      .catch((e: Error) => {
        if (!isBase && targetPath === 'AGENTS.md') {
          setContent(PROJECT_TEMPLATES.context(project))
        } else {
          setFailed(e.message)
        }
      })
      .finally(() => setLoading(false))
  }, [base, modalMode, project, targetPath])

  const openProjectFile = (pth: string) => {
    setTargetPath(pth)
    setModalMode('edit-project')
    setNotice('')
  }

  const openBaseFile = (it: LayerItem) => {
    const pth =
      it.path ||
      (it.kind === 'context'
        ? 'context/AGENTS.md'
        : it.kind === 'skill'
          ? `skills/${it.name}/SKILL.md`
          : it.kind === 'rule'
            ? `rules/${it.name}.md`
            : `agents/${it.name}.md`)
    setTargetPath(pth)
    setModalMode('view-base')
    setNotice('')
  }

  const startCreate = (kind: 'skill' | 'rule') => {
    setNewName('')
    setTargetPath('')
    setContent(PROJECT_TEMPLATES[kind](`project-${kind}`))
    setModalMode(kind === 'skill' ? 'create-skill' : 'create-rule')
    setFailed('')
    setNotice('')
  }

  const saveProjectFile = async () => {
    const pth =
      modalMode === 'create-skill'
        ? `.agents/skills/${newName.trim()}/SKILL.md`
        : modalMode === 'create-rule'
          ? `.agents/rules/${newName.trim()}.md`
          : targetPath
    if (!pth) return
    setSaving(true)
    setFailed('')
    try {
      await send('PUT', `${base}/skills/file`, { path: pth, content })
      setModalMode(null)
      setNotice(`Saved ${pth} on main. Running sandboxes pick it up on their next turn.`)
      reload()
    } catch (e) {
      setFailed((e as Error).message)
    } finally {
      setSaving(false)
    }
  }

  const deleteProjectFile = async () => {
    if (!targetPath || targetPath === 'AGENTS.md') return
    setSaving(true)
    setFailed('')
    try {
      await send('DELETE', `${base}/skills/file?path=${encodeURIComponent(targetPath)}`)
      setModalMode(null)
      setNotice(`Removed ${targetPath} from main.`)
      reload()
    } catch (e) {
      setFailed((e as Error).message)
    } finally {
      setSaving(false)
    }
  }

  const submitSkillRequest = async () => {
    setSaving(true)
    setFailed('')
    try {
      const res = await send<{ key: string }>('POST', `${base}/skills/request`, {
        missing: missingSkill.trim(),
        wanted: wantedSkill.trim(),
      })
      setModalMode(null)
      setMissingSkill('')
      setWantedSkill('')
      setNotice(`Created ticket ${res?.key ?? ''} to find or write the skill.`)
      reload()
    } catch (e) {
      setFailed((e as Error).message)
    } finally {
      setSaving(false)
    }
  }

  const q = baseFilter.trim().toLowerCase()
  const baseList = (data?.base ?? []).filter(
    (it) => !q || it.name.toLowerCase().includes(q) || it.kind.toLowerCase().includes(q) || (it.description ?? '').toLowerCase().includes(q),
  )

  return (
    <section className="mt-8" aria-label="Skills and context">
      <div className="flex flex-wrap items-center justify-between gap-2">
        <h2 className="mb-1 text-xl font-semibold">
          Skills and context <span className="text-sm font-normal text-text-dim">the base comes with Crew; this project adds its own</span>
        </h2>
        <div className="flex flex-wrap gap-1.5">
          <button type="button" className={button} onClick={() => openProjectFile('AGENTS.md')} data-edit-agents-md>
            Edit AGENTS.md
          </button>
          <button type="button" className={button} onClick={() => startCreate('skill')} data-add-project-skill>
            + Add project skill
          </button>
          <button type="button" className={button} onClick={() => startCreate('rule')} data-add-project-rule>
            + Add project rule
          </button>
          <button
            type="button"
            className={button}
            onClick={() => {
              setModalMode('request-skill')
              setFailed('')
            }}
            data-ask-agent-skill
          >
            Ask an agent for a skill
          </button>
        </div>
      </div>
      <Failed error={error} />
      {notice && <p className="mt-2 text-xs text-accent">{notice}</p>}
      <div className="mt-3 grid gap-4 lg:grid-cols-[minmax(0,2fr)_minmax(0,1fr)]">
        <Card title="This project's own layer, on main">
          <table className="w-full text-sm">
            <thead className="text-left text-xs tracking-wide text-text-dim uppercase">
              <tr className="border-b border-border">
                <th className="py-1">File</th>
                <th>What</th>
                <th>Came from</th>
                <th className="text-right">Action</th>
              </tr>
            </thead>
            <tbody>
              {data?.project.map((it) => (
                <tr key={it.path} className="border-b border-border/60 align-top" data-layer={it.path}>
                  <td className="py-2 pr-2 font-mono text-xs">{it.kind === 'skill' ? it.path?.replace(/SKILL\.md$/, '') : it.path}</td>
                  <td className="py-2 pr-2">{it.description || it.name}</td>
                  <td className="py-2 pr-2">
                    {it.ticket ? (
                      <Link to="/p/$project/tickets/$key" params={{ project, key: it.ticket }} className="font-mono text-xs text-accent underline">
                        {it.ticket}
                      </Link>
                    ) : (
                      <span className="text-text-dim">written in this project</span>
                    )}
                  </td>
                  <td className="py-2 text-right whitespace-nowrap">
                    {it.path && (
                      <button type="button" className="text-xs font-medium text-accent underline" onClick={() => openProjectFile(it.path!)}>
                        View / Edit
                      </button>
                    )}
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
          {data && data.project.length === 0 && <p className="text-sm text-text-dim">main has no AGENTS.md, skill, or rule yet.</p>}
          <p className="mt-2 text-xs text-text-dim">
            Sideloaded: every sandbox of this project gets these when it starts, and running sandboxes pick up changes on their next turn. No image is rebuilt.
          </p>
        </Card>
        <Card title="The base, in the runtime image">
          <p className="text-sm">
            AGENTS.md (the global context), {counts.skill ?? 0} skills, {counts.agent ?? 0} subagents, and {counts.rule ?? 0}{' '}
            {counts.rule === 1 ? 'rule' : 'rules'}. From Crew&apos;s built-in harness, the same for every project.
          </p>
          <input
            type="search"
            value={baseFilter}
            onChange={(e) => setBaseFilter(e.target.value)}
            placeholder="Browse base skills, rules, subagents..."
            className="mt-2 w-full rounded border border-border bg-bg px-2.5 py-1 text-xs"
          />
          <div className="mt-2 max-h-48 overflow-y-auto rounded border border-border/60 bg-bg-sunk p-2 text-xs">
            {baseList.map((it) => (
              <div key={`${it.kind}:${it.name}`} className="flex items-center justify-between gap-2 border-b border-border/40 py-1 last:border-b-0">
                <div className="min-w-0 truncate">
                  <Tag>{it.kind}</Tag> <span className="font-mono font-medium">{it.name}</span>
                </div>
                <button type="button" className=" shrink-0 text-accent underline" onClick={() => openBaseFile(it)}>
                  View
                </button>
              </div>
            ))}
          </div>
          <p className="mt-3 text-xs tracking-wide text-text-dim uppercase">When an agent misses a skill</p>
          <ol className="list-decimal space-y-1 pl-5 text-sm">
            <li>It calls request_skill and carries on.</li>
            <li>Crew creates a ticket that finds or writes the skill.</li>
            <li>The reviewer reads it as instructions. A skill from outside is pinned, with its source and licence.</li>
            <li>When the ticket is Done, every later run of this project has it.</li>
          </ol>
          {data && data.asked.length > 0 && (
            <>
              <p className="mt-3 text-xs tracking-wide text-text-dim uppercase">Asked for</p>
              <ul className="space-y-1 text-sm">
                {data.asked.map((a) => (
                  <li key={a.ticket}>
                    <Link to="/p/$project/tickets/$key" params={{ project, key: a.ticket }} className="font-mono text-xs text-accent underline">
                      {a.ticket}
                    </Link>{' '}
                    {a.title}{' '}
                    <span className="text-text-dim">
                      · {stageName[a.stage] ?? a.stage} · {a.requests.length} {a.requests.length === 1 ? 'request' : 'requests'}
                    </span>
                  </li>
                ))}
              </ul>
            </>
          )}
        </Card>
      </div>

      {modalMode && (
        <div className="fixed inset-0 z-50 flex items-center justify-center bg-black/60 p-4" role="dialog" aria-label="Skill or context editor">
          <div className="flex max-h-[88vh] w-full max-w-4xl flex-col rounded-lg border border-border bg-bg p-5 shadow-xl">
            <div className="mb-3 flex items-center justify-between gap-2 border-b border-border pb-2">
              <h3 className="font-mono text-sm font-bold">
                {modalMode === 'view-base'
                  ? `Base harness: ${targetPath}`
                  : modalMode === 'create-skill'
                    ? 'New project skill (.agents/skills/<name>/SKILL.md)'
                    : modalMode === 'create-rule'
                      ? 'New project rule (.agents/rules/<name>.md)'
                      : modalMode === 'request-skill'
                        ? 'Ask an agent to find or write a skill'
                        : `${project}: ${targetPath}`}
              </h3>
              <button type="button" className={button} onClick={() => setModalMode(null)}>
                Close
              </button>
            </div>
            <Failed error={failed} />
            {modalMode === 'request-skill' ? (
              <div className="grid gap-3 text-sm">
                <label className="grid gap-1">
                  <span className="text-text-dim">What capability or tool is missing?</span>
                  <input
                    value={missingSkill}
                    onChange={(e) => setMissingSkill(e.target.value)}
                    placeholder="e.g. Stripe webhook signature verification in Go"
                    className="rounded border border-border bg-bg-sunk px-2.5 py-1.5 text-sm"
                  />
                </label>
                <label className="grid gap-1">
                  <span className="text-text-dim">What should the skill teach agents to do?</span>
                  <textarea
                    value={wantedSkill}
                    onChange={(e) => setWantedSkill(e.target.value)}
                    rows={4}
                    placeholder="Describe the conventions, libraries, or checks the skill should cover..."
                    className="rounded border border-border bg-bg-sunk p-2.5 text-sm"
                  />
                </label>
                <div className="flex gap-2">
                  <button
                    type="button"
                    className={mainButton}
                    disabled={saving || !missingSkill.trim() || !wantedSkill.trim()}
                    onClick={submitSkillRequest}
                  >
                    {saving ? 'Creating ticket...' : 'Create skill ticket'}
                  </button>
                </div>
              </div>
            ) : (
              <>
                {(modalMode === 'create-skill' || modalMode === 'create-rule') && (
                  <label className="mb-3 grid gap-1 text-xs">
                    <span className="text-text-dim">Name (letters, digits, hyphens; must not duplicate a base skill name)</span>
                    <input
                      value={newName}
                      onChange={(e) => {
                        const v = e.target.value
                        setNewName(v)
                        if (v.trim()) setContent(PROJECT_TEMPLATES[modalMode === 'create-skill' ? 'skill' : 'rule'](v.trim()))
                      }}
                      placeholder={modalMode === 'create-skill' ? 'my-project-skill' : 'my-project-rule'}
                      className="w-72 rounded border border-border bg-bg-sunk px-2.5 py-1 font-mono text-xs"
                    />
                  </label>
                )}
                {loading ? (
                  <p className="py-8 text-sm text-text-dim">Loading file...</p>
                ) : (
                  <textarea
                    value={content}
                    readOnly={modalMode === 'view-base'}
                    onChange={(e) => setContent(e.target.value)}
                    className="min-h-[360px] flex-1 rounded border border-border bg-bg-sunk p-3 font-mono text-xs leading-5"
                  />
                )}
                {modalMode !== 'view-base' && (
                  <div className="mt-3 flex flex-wrap items-center justify-between gap-2">
                    <div className="flex gap-2">
                      <button
                        type="button"
                        className={mainButton}
                        disabled={
                          saving ||
                          loading ||
                          !content.trim() ||
                          ((modalMode === 'create-skill' || modalMode === 'create-rule') && !newName.trim())
                        }
                        onClick={saveProjectFile}
                      >
                        {saving ? 'Saving to main...' : 'Commit to main'}
                      </button>
                      {modalMode === 'edit-project' && targetPath !== 'AGENTS.md' && (
                        <button type="button" className={button} disabled={saving} onClick={deleteProjectFile}>
                          Delete from main
                        </button>
                      )}
                    </div>
                    <span className="text-xs text-text-dim">Commits directly to {project}&apos;s main branch.</span>
                  </div>
                )}
              </>
            )}
          </div>
        </div>
      )}
    </section>
  )
}
