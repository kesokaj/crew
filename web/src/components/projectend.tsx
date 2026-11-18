import { useNavigate } from '@tanstack/react-router'
import { useState } from 'react'
import { send, sendForFile } from '../lib/api'
import { button, Card, Failed, Tag } from './ui'

export type ProjectEndViewProps = {
  project: string
  isProtected: boolean
  isLocked: boolean
  name: string
  busy: boolean
  error: string
  onNameChange: (name: string) => void
  onToggleProtected: (next: boolean) => void
  onArchive: () => void
  onExport: () => void
  onDelete: () => void
}

// ProjectEndView renders the Archive or delete card (design 7.13, 16.2). A protected project
// disables Archive and Delete while leaving Export enabled; Crew's own project is locked Protected.
export function ProjectEndView({
  project,
  isProtected,
  isLocked,
  name,
  busy,
  error,
  onNameChange,
  onToggleProtected,
  onArchive,
  onExport,
  onDelete,
}: ProjectEndViewProps) {
  return (
    <Card
      title="Archive or delete this project"
      right={isProtected ? <Tag tone="blue">{isLocked ? 'Protected · locked' : 'Protected'}</Tag> : undefined}
    >
      <div className="mb-4 border-b border-border pb-3">
        <div className="flex items-center justify-between gap-2">
          <span className="text-sm font-medium">Protected</span>
          {isLocked ? (
            <span data-protected="locked">
              <Tag tone="blue">Protected · locked</Tag>
            </span>
          ) : (
            <button
              type="button"
              data-protected={isProtected ? 'on' : 'off'}
              className={
                'rounded-full border px-2.5 py-0.5 text-xs ' +
                (isProtected ? 'border-accent bg-accent-soft text-accent' : 'border-border text-text-dim')
              }
              aria-pressed={isProtected}
              aria-label={`Protected: ${isProtected ? 'on' : 'off'}`}
              disabled={busy}
              onClick={() => onToggleProtected(!isProtected)}
            >
              {isProtected ? 'Protected' : 'Off'}
            </button>
          )}
        </div>
        <p className="mt-1 text-xs text-text-dim">
          {isLocked
            ? "Crew's own project is always protected: it is never archived or deleted."
            : isProtected
              ? 'This project is protected: switch Protected off before archiving or deleting it.'
              : 'Off by default. Switch Protected on so this project cannot be archived or deleted by mistake.'}
        </p>
      </div>
      <p className="mb-2 text-sm">Archive hides the project from the lists. Nothing is deleted, and nothing starts in it.</p>
      <button className={button} disabled={busy || isProtected} onClick={onArchive}>
        Archive
      </button>
      <p className="mt-4 mb-2 text-sm">
        Delete removes its repositories and every row of it from this machine, its history too. The repository on GitHub is not
        touched. Write the project out first:
      </p>
      <button className={button} disabled={busy} onClick={onExport}>
        Export it as files
      </button>
      <label className="mt-3 block text-sm">
        Type <span className="font-mono">{project}</span> to delete it
        <input
          aria-label="The project's name, to delete it"
          className="mt-1 block w-full rounded-md border border-border bg-bg px-2 py-1 font-mono text-sm disabled:opacity-50"
          value={name}
          disabled={busy || isProtected}
          onChange={(e) => onNameChange(e.target.value)}
        />
      </label>
      <button
        className={button + ' mt-2 border-bad text-bad'}
        disabled={busy || isProtected || name !== project}
        onClick={onDelete}
      >
        Delete {project}
      </button>
      <Failed error={error} />
    </Card>
  )
}

// ProjectEnd archives or deletes the project (design 7.13). Archive hides it and starts nothing.
// Delete asks for its name, offers an export first, and removes its repositories and rows; the
// repository on GitHub is not touched.
export function ProjectEnd({
  project,
  base,
  isProtected,
  isLocked,
  onChanged,
}: {
  project: string
  base: string
  isProtected: boolean
  isLocked: boolean
  onChanged: () => void
}) {
  const navigate = useNavigate()
  const [name, setName] = useState('')
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState('')
  const run = (f: () => Promise<unknown>) => {
    setBusy(true)
    setError('')
    f()
      .catch((e: Error) => setError(e.message))
      .finally(() => setBusy(false))
  }
  const exportFirst = () =>
    run(async () => {
      const url = URL.createObjectURL(await sendForFile(base + '/export'))
      const a = document.createElement('a')
      a.href = url
      a.download = `${project}.tar`
      a.click()
      URL.revokeObjectURL(url)
    })
  return (
    <ProjectEndView
      project={project}
      isProtected={isProtected}
      isLocked={isLocked}
      name={name}
      busy={busy}
      error={error}
      onNameChange={setName}
      onToggleProtected={(next) =>
        run(() =>
          send('PUT', base + '/protected', { protected: next }).then(() => {
            onChanged()
          }),
        )
      }
      onArchive={() => run(() => send('POST', base + '/archive', {}).then(() => navigate({ to: '/' })))}
      onExport={exportFirst}
      onDelete={() => run(() => send('DELETE', base, { name }).then(() => navigate({ to: '/' })))}
    />
  )
}
