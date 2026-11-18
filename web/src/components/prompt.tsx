import { useState } from 'react'
import type { PromptPart } from '../lib/api'

// What each part of a saved prompt is, in the words of the task view (8.6, 20.2).
const partName: Record<string, string> = {
  stage: 'Stage prompt',
  agent: 'Agent definition',
  context: 'Project',
  override: 'Project override',
  ticket: 'Ticket',
  plan: 'Source plan',
  records: 'Records',
  memory: 'Memory index',
  docs: 'Documentation',
  comments: 'Comments',
  crew: 'From Crew',
  operator: 'From you',
}

const defaultOpenParts = new Set(['ticket', 'task', 'Task', 'operator', 'comments'])

export function formatPartSize(text: string): string {
  const len = text.length
  if (len < 1024) return `${len} chars`
  return `${(len / 1024).toFixed(1)} KB`
}

export function initialOpenParts(parts: PromptPart[]): Record<number, boolean> {
  const hasNamedOpen = parts.some((p) => defaultOpenParts.has(p.part))
  return Object.fromEntries(parts.map((p, i) => [i, hasNamedOpen ? defaultOpenParts.has(p.part) : i === 0]))
}

// PromptView shows a run's prompt in its parts, each with its source and its version, or as one
// text that can be copied (20.2). A prompt whose parts were not saved is shown as one text. A saved
// prompt is a record of what was sent: the ticket's, the plan's and the agents' words, and Crew's own
// text of its version; its text is marked data-free-text (0.53), and Crew's own prompt files and the
// strings of its Go code are kept free of amounts by baseline's tests.
export function PromptView({ prompt, parts, version }: { prompt: string; parts: PromptPart[]; version?: string }) {
  const partsSignature = parts.map((p) => p.part).join('\0')
  const [prevSignature, setPrevSignature] = useState(partsSignature)
  const [whole, setWhole] = useState(parts.length === 0)
  const [openParts, setOpenParts] = useState<Record<number, boolean>>(() => initialOpenParts(parts))
  if (partsSignature !== prevSignature) {
    setPrevSignature(partsSignature)
    setWhole(parts.length === 0)
    setOpenParts(initialOpenParts(parts))
  }
  const allOpen = parts.length > 0 && parts.every((_, i) => !!openParts[i])
  const toggleAll = () => {
    const next = !allOpen
    setOpenParts(Object.fromEntries(parts.map((_, i) => [i, next])))
  }
  return (
    <div>
      {parts.length > 0 && (
        <div className="mb-3 flex flex-wrap items-center gap-3">
          <div className="inline-flex overflow-hidden rounded-md border border-border text-sm">
            <button
              type="button"
              className={'min-h-8 px-3 py-1 ' + (!whole ? 'bg-text text-bg' : '')}
              onClick={() => setWhole(false)}
            >
              In parts
            </button>
            <button
              type="button"
              className={'min-h-8 px-3 py-1 ' + (whole ? 'bg-text text-bg' : '')}
              onClick={() => setWhole(true)}
            >
              As one text
            </button>
          </div>
          {!whole && (
            <button
              type="button"
              className="min-h-8 rounded-md border border-border bg-bg-raised px-2.5 py-1 text-xs text-accent hover:bg-bg-sunk focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-accent"
              onClick={toggleAll}
            >
              {allOpen ? 'Collapse all' : `Expand all (${parts.length})`}
            </button>
          )}
        </div>
      )}
      {whole ? (
        <div>
          <button
            type="button"
            className="mb-1 text-xs text-accent hover:underline"
            onClick={() => void navigator.clipboard?.writeText(prompt)}
          >
            Copy
          </button>
          <pre
            className="max-h-[32rem] overflow-auto rounded-md border border-border bg-bg p-3 text-xs whitespace-pre-wrap break-words [overflow-wrap:anywhere]"
            data-free-text
          >
            {prompt}
          </pre>
        </div>
      ) : (
        <div className="space-y-2">
          {parts.map((p, i) => {
            const isOpen = !!openParts[i]
            return (
              <section key={i} className="rounded-md border border-border p-3" data-part={p.part} data-open={isOpen}>
                <h4 className="flex flex-wrap items-center justify-between gap-3 text-sm">
                  <button
                    type="button"
                    className="flex flex-wrap items-baseline gap-2.5 text-left hover:text-accent focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-accent"
                    aria-expanded={isOpen}
                    onClick={() => setOpenParts({ ...openParts, [i]: !isOpen })}
                  >
                    <b>{partName[p.part] ?? p.part}</b>
                    <span className="font-mono text-xs text-text-dim">
                      {p.source}
                      {version && p.source.startsWith('baseline/') ? ` · Crew ${version}` : ''}
                    </span>
                    <span className="rounded bg-bg-sunk px-1.5 py-0.5 font-mono text-[11px] text-text-dim">
                      {formatPartSize(p.text)}
                    </span>
                  </button>
                  <button
                    type="button"
                    className="text-xs text-text-dim hover:text-accent focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-accent"
                    aria-expanded={isOpen}
                    onClick={() => setOpenParts({ ...openParts, [i]: !isOpen })}
                  >
                    {isOpen ? 'Hide' : 'Show'}
                  </button>
                </h4>
                <pre
                  hidden={!isOpen}
                  className={
                    (!isOpen ? 'hidden ' : '') +
                    'mt-2 max-h-72 overflow-auto rounded bg-bg p-2 text-xs whitespace-pre-wrap break-words [overflow-wrap:anywhere]'
                  }
                  data-free-text
                >
                  {p.text}
                </pre>
              </section>
            )
          })}
        </div>
      )}
    </div>
  )
}
