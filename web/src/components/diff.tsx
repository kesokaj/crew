import type { FileDiff } from '../lib/api'

// Diff shows one file's patch, its lines coloured by what they do. What a diff holds is text an agent
// wrote: it is shown as text, never as markup (20.3), and each line is marked data-free-text (0.53).
export function Diff({ file }: { file: FileDiff }) {
  if (file.binary) return <p className="p-2 text-xs text-text-dim">A binary file: no lines to show.</p>
  return (
    <div className="mt-1 max-h-[28rem] overflow-auto rounded border border-border bg-bg font-mono text-xs" data-diff={file.path}>
      {file.patch.split('\n').map((l, i) => (
        <div
          key={i}
          className={
            'px-2 whitespace-pre ' +
            (l.startsWith('+') && !l.startsWith('+++')
              ? 'bg-green-500/10 text-ok'
              : l.startsWith('-') && !l.startsWith('---')
                ? 'bg-red-500/10 text-bad'
                : l.startsWith('@@')
                  ? 'text-accent'
                  : '')
          }
          data-free-text
        >
          {l || ' '}
        </div>
      ))}
      {file.cut && <p className="px-2 py-1 text-text-dim">The rest of this file's diff is not shown here.</p>}
    </div>
  )
}

// Omitted says how many more files changed than a diff lists (the last entry of a long one).
export function Omitted({ files }: { files: FileDiff[] }) {
  const n = omittedOf(files)
  return n ? <p className="mt-1 text-xs text-text-dim">and {n} more files, not listed here</p> : null
}

// omittedOf is how many files a diff did not list.
export const omittedOf = (files: FileDiff[]) => files.find((f) => f.omitted)?.omitted ?? 0

// listed are the files of a diff that have a path.
export const listed = (files: FileDiff[]) => files.filter((f) => !f.omitted)
