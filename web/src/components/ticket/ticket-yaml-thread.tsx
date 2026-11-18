import { Link } from '@tanstack/react-router'
import { useState } from 'react'
import { usePoll } from '../../lib/poll'
import { Card, Failed, Tag, button } from '../ui'

type ResearchBrief = {
  id: number
  artifactPath: string
  triggerReason: string
  triesAtTrigger: number
  hypothesis: string
  resolved: boolean
  createdAt: string
}

export function TicketYamlAndThreadBox({ project, ticketKey }: { project: string; ticketKey: string }) {
  const encProj = encodeURIComponent(project)
  const encKey = encodeURIComponent(ticketKey)
  const [expanded, setExpanded] = useState(true)
  const yaml = usePoll<{ key: string; path: string; yaml: string }>(`/api/projects/${encProj}/tasks/${encKey}/yaml`, 15000)
  const research = usePoll<{ briefs: ResearchBrief[] }>(`/api/projects/${encProj}/tasks/${encKey}/research`, 15000)
  const briefs = research.data?.briefs ?? []

  return (
    <Card
      title="Factory Contract (.crew/tickets YAML & Research)"
      right={
        <div className="flex items-center gap-2">
          <Link to="/p/$project/chat" params={{ project }} className={button}>
            Project Chat
          </Link>
          <button type="button" className={button} onClick={() => setExpanded((v) => !v)}>
            {expanded ? 'Hide YAML' : 'Show YAML'}
          </button>
        </div>
      }
    >
      <Failed error={yaml.error || research.error} />
      {yaml.data && (
        <div className="space-y-2">
          <div className="flex flex-wrap items-center gap-2 text-xs text-text-dim">
            <span className="font-mono" data-free-text>
              {yaml.data.path}
            </span>
            <Tag>Tasks API</Tag>
            {briefs.length > 0 && <Tag tone="amber">{briefs.length} @researcher brief</Tag>}
          </div>
          {expanded && (
            <pre className="max-h-64 overflow-auto rounded border border-border bg-bg p-3 font-mono text-xs leading-relaxed" data-free-text>
              {yaml.data.yaml}
            </pre>
          )}
        </div>
      )}
      {briefs.length > 0 && (
        <div className="mt-3 space-y-2 border-t border-border pt-3">
          <h3 className="text-xs font-semibold tracking-wide text-text-dim uppercase">@researcher Circuit-Breaker Briefs</h3>
          {briefs.map((b) => (
            <div key={b.id} className="rounded border border-border bg-bg p-2 text-xs">
              <div className="flex flex-wrap items-center justify-between gap-2">
                <span className="font-mono text-accent" data-free-text>
                  {b.artifactPath}
                </span>
                <Tag tone={b.resolved ? 'ok' : 'amber'}>{b.resolved ? 'resolved' : `tries: ${b.triesAtTrigger}`}</Tag>
              </div>
              <p className="mt-1 text-text-dim" data-free-text>
                {b.hypothesis}
              </p>
            </div>
          ))}
        </div>
      )}
    </Card>
  )
}
