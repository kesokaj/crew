import { useEffect, useMemo, useState } from 'react'
import { getJSON, type BlobView, type FileDiff, type TreeEntry } from '../lib/api'
import { listed } from './diff'
import { button } from './ui'

export function FileExplorerModal({
  project,
  refName,
  files = [],
  initialPath,
  initialMode,
  onClose,
}: {
  project: string
  refName: string
  files?: FileDiff[]
  initialPath?: string | null
  initialMode?: 'diff' | 'file'
  onClose: () => void
}) {
  const changed = useMemo(() => listed(files), [files])
  const changedByPath = useMemo(() => new Map(changed.map((f) => [f.path, f])), [changed])
  const [tab, setTab] = useState<'changed' | 'all'>(changed.length > 0 && initialMode !== 'file' ? 'changed' : 'all')
  const [filter, setFilter] = useState('')
  const [tree, setTree] = useState<TreeEntry[]>([])
  const [treeLoading, setTreeLoading] = useState(false)
  const [selected, setSelected] = useState<string>(initialPath ?? changed[0]?.path ?? '')
  const [mode, setMode] = useState<'diff' | 'file'>(initialMode ?? (changed.length > 0 ? 'diff' : 'file'))
  const [wrap, setWrap] = useState(true)
  const [blob, setBlob] = useState<BlobView | null>(null)
  const [blobError, setBlobError] = useState('')
  const [copied, setCopied] = useState(false)

  useEffect(() => {
    if (!project || !refName) return
    setTreeLoading(true)
    getJSON<{ ref: string; files: TreeEntry[] }>(
      `/api/projects/${encodeURIComponent(project)}/git/tree?ref=${encodeURIComponent(refName)}`,
    )
      .then((res) => {
        setTree(res.files ?? [])
        if (!selected && res.files?.[0]) setSelected(res.files[0].path)
      })
      .catch(() => {})
      .finally(() => setTreeLoading(false))
  }, [project, refName])

  const hasDiff = changedByPath.has(selected)
  const activeMode = hasDiff ? mode : 'file'

  useEffect(() => {
    if (!selected || activeMode !== 'file' || !project || !refName) return
    setBlob(null)
    setBlobError('')
    getJSON<BlobView>(
      `/api/projects/${encodeURIComponent(project)}/git/blob?ref=${encodeURIComponent(refName)}&path=${encodeURIComponent(selected)}`,
    ).then(setBlob, (e: Error) => setBlobError(e.message))
  }, [project, refName, selected, activeMode])

  const visibleList = useMemo(() => {
    const q = filter.trim().toLowerCase()
    if (tab === 'changed') {
      return changed
        .filter((f) => !q || f.path.toLowerCase().includes(q))
        .map((f) => ({ path: f.path, added: f.added, removed: f.removed, size: undefined as number | undefined }))
    }
    return tree
      .filter((t) => !q || t.path.toLowerCase().includes(q))
      .map((t) => {
        const d = changedByPath.get(t.path)
        return { path: t.path, added: d?.added, removed: d?.removed, size: t.size }
      })
  }, [tab, filter, changed, tree, changedByPath])

  useEffect(() => {
    const onKey = (e: KeyboardEvent) => {
      if (e.key === 'Escape') {
        e.preventDefault()
        onClose()
        return
      }
      if ((e.key === 'ArrowDown' || e.key === 'ArrowUp') && visibleList.length > 0) {
        const tag = (e.target as HTMLElement | null)?.tagName
        if (tag === 'INPUT' || tag === 'TEXTAREA') return
        e.preventDefault()
        const idx = visibleList.findIndex((x) => x.path === selected)
        const next = e.key === 'ArrowDown' ? Math.min(visibleList.length - 1, idx + 1) : Math.max(0, idx - 1)
        const item = visibleList[next]
        if (item) setSelected(item.path)
      }
    }
    window.addEventListener('keydown', onKey)
    return () => window.removeEventListener('keydown', onKey)
  }, [visibleList, selected, onClose])

  const diffFile = changedByPath.get(selected)
  const copyText = activeMode === 'diff' ? (diffFile?.patch ?? '') : (blob?.content ?? '')

  return (
    <div
      className="fixed inset-0 z-50 flex items-center justify-center bg-black/55 p-3 sm:p-6"
      role="dialog"
      aria-modal="true"
      aria-label="File and diff explorer"
      onClick={(e) => {
        if (e.target === e.currentTarget) onClose()
      }}
    >
      <div className="flex h-[86vh] w-[94vw] max-w-6xl flex-col overflow-hidden rounded-lg border border-border bg-bg-raised shadow-2xl">
        <div className="flex flex-wrap items-center justify-between gap-2 border-b border-border bg-bg/60 px-4 py-2.5">
          <div className="flex min-w-0 items-center gap-2">
            <span className="rounded border border-border bg-bg-raised px-2 py-0.5 font-mono text-xs text-text-dim">{refName}</span>
            <span className="truncate font-mono text-sm font-semibold" data-free-text>
              {selected || 'Select a file'}
            </span>
          </div>
          <div className="flex items-center gap-2 text-xs">
            {hasDiff && (
              <div className="inline-flex overflow-hidden rounded-md border border-border">
                <button
                  type="button"
                  className={'px-2.5 py-1 ' + (activeMode === 'diff' ? 'bg-text text-bg font-medium' : 'bg-bg-raised hover:bg-bg-sunk')}
                  onClick={() => setMode('diff')}
                >
                  Diff
                </button>
                <button
                  type="button"
                  className={'px-2.5 py-1 ' + (activeMode === 'file' ? 'bg-text text-bg font-medium' : 'bg-bg-raised hover:bg-bg-sunk')}
                  onClick={() => setMode('file')}
                >
                  Full file
                </button>
              </div>
            )}
            <button
              type="button"
              className={'rounded-md border border-border px-2.5 py-1 ' + (wrap ? 'bg-accent-soft text-accent font-medium' : 'bg-bg-raised')}
              onClick={() => setWrap(!wrap)}
            >
              Wrap: {wrap ? 'On' : 'Off'}
            </button>
            <button
              type="button"
              className={button}
              onClick={() => {
                void navigator.clipboard?.writeText(copyText)
                setCopied(true)
                setTimeout(() => setCopied(false), 1500)
              }}
            >
              {copied ? 'Copied' : 'Copy'}
            </button>
            <button type="button" className={button} onClick={onClose}>
              Close
            </button>
          </div>
        </div>
        <div className="flex min-h-0 flex-1">
          <aside className="flex w-72 shrink-0 flex-col border-r border-border bg-bg/40">
            <div className="flex flex-col gap-2 border-b border-border p-2.5">
              {changed.length > 0 && (
                <div className="grid grid-cols-2 overflow-hidden rounded-md border border-border text-xs">
                  <button
                    type="button"
                    className={'py-1 ' + (tab === 'changed' ? 'bg-text text-bg font-medium' : 'bg-bg-raised hover:bg-bg-sunk')}
                    onClick={() => setTab('changed')}
                  >
                    Changed ({changed.length})
                  </button>
                  <button
                    type="button"
                    className={'py-1 ' + (tab === 'all' ? 'bg-text text-bg font-medium' : 'bg-bg-raised hover:bg-bg-sunk')}
                    onClick={() => setTab('all')}
                  >
                    All files ({treeLoading ? '…' : tree.length})
                  </button>
                </div>
              )}
              <input
                type="search"
                aria-label="Filter files"
                placeholder="Filter files…"
                value={filter}
                onChange={(e) => setFilter(e.target.value)}
                className="w-full rounded border border-border bg-bg-raised px-2 py-1 text-xs"
              />
            </div>
            <ul className="min-h-0 flex-1 overflow-auto p-1.5 space-y-0.5">
              {visibleList.map((item) => {
                const isSelected = item.path === selected
                return (
                  <li key={item.path}>
                    <button
                      type="button"
                      onClick={() => setSelected(item.path)}
                      className={
                        'flex w-full items-center justify-between gap-2 rounded px-2 py-1.5 text-left font-mono text-xs transition-colors ' +
                        (isSelected
                          ? 'border-l-4 border-l-accent bg-accent-soft text-accent font-semibold'
                          : 'hover:bg-bg-sunk/80 text-text')
                      }
                    >
                      <span className="truncate" data-free-text>
                        {item.path}
                      </span>
                      <span className="shrink-0 text-[11px]">
                        {item.added !== undefined || item.removed !== undefined ? (
                          <>
                            <span className="text-ok">+{item.added ?? 0}</span>{' '}
                            <span className="text-bad">−{item.removed ?? 0}</span>
                          </>
                        ) : item.size !== undefined ? (
                          <span className="text-text-dim">{formatBytes(item.size)}</span>
                        ) : null}
                      </span>
                    </button>
                  </li>
                )
              })}
              {visibleList.length === 0 && <li className="p-2 text-xs text-text-dim">No matching files.</li>}
            </ul>
          </aside>
          <div className="min-w-0 flex-1 overflow-auto bg-bg font-mono text-xs" data-diff={selected}>
            {activeMode === 'diff' && diffFile ? (
              diffFile.binary ? (
                <p className="p-4 text-sm text-text-dim">A binary file: no lines to show.</p>
              ) : (
                <div className="py-2">
                  {diffFile.patch.split('\n').map((l, i) => (
                    <div
                      key={i}
                      className={
                        'flex px-3 leading-5 ' +
                        (wrap ? 'whitespace-pre-wrap break-words [overflow-wrap:anywhere] ' : 'whitespace-pre ') +
                        (l.startsWith('+') && !l.startsWith('+++')
                          ? 'bg-green-500/10 text-ok'
                          : l.startsWith('-') && !l.startsWith('---')
                            ? 'bg-red-500/10 text-bad'
                            : l.startsWith('@@')
                              ? 'bg-accent-soft/40 text-accent'
                              : '')
                      }
                    >
                      <span className="w-10 shrink-0 select-none pr-3 text-right text-text-dim/60">{i + 1}</span>
                      <span className="min-w-0 flex-1" data-free-text>
                        {l || ' '}
                      </span>
                    </div>
                  ))}
                  {diffFile.cut && <p className="px-4 py-2 text-text-dim">The rest of this file's diff is cut at 64 KB.</p>}
                </div>
              )
            ) : blobError ? (
              <p className="p-4 text-sm text-bad">{blobError}</p>
            ) : !blob ? (
              <p className="p-4 text-sm text-text-dim">Loading file…</p>
            ) : blob.binary ? (
              <p className="p-4 text-sm text-text-dim">Binary file ({formatBytes(blob.size)}).</p>
            ) : (
              <div className="py-2">
                {blob.content.split('\n').map((l, i) => (
                  <div
                    key={i}
                    className={
                      'flex px-3 leading-5 hover:bg-bg-raised/50 ' +
                      (wrap ? 'whitespace-pre-wrap break-words [overflow-wrap:anywhere]' : 'whitespace-pre')
                    }
                  >
                    <span className="w-12 shrink-0 select-none pr-3 text-right text-text-dim/60">{i + 1}</span>
                    <span className="min-w-0 flex-1" data-free-text>
                      {l || ' '}
                    </span>
                  </div>
                ))}
                {blob.cut && <p className="px-4 py-2 text-text-dim">File truncated at 256 KB ({formatBytes(blob.size)} total).</p>}
              </div>
            )}
          </div>
        </div>
      </div>
    </div>
  )
}

function formatBytes(n: number): string {
  if (n < 1024) return `${n} B`
  if (n < 1024 * 1024) return `${(n / 1024).toFixed(1)} KB`
  return `${(n / (1024 * 1024)).toFixed(1)} MB`
}
