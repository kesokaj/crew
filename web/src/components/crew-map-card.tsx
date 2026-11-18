import { Link } from '@tanstack/react-router'
import { usePoll } from '../lib/poll'
import { Card } from './ui'

type OverviewProject = {
  name: string
  key: string
  goal: string
  state: string
  byStage: Record<string, number>
  working: number
}

// CrewMapCard shows the live Crew map in the right column of a normal conversation outside a single
// project (design 11.8, 20.1): every active project, its stage counts, and its running sandboxes.
export function CrewMapCard() {
  const { data } = usePoll<{ projects: OverviewProject[] }>('/api/overview', 10000)
  const projects = data?.projects ?? []
  return (
    <Card title="Live Crew map">
      <p className="mb-2 text-xs text-text-dim">
        The assistant reads the map and any non-archived project through <code>get_crew_map</code>, <code>list_projects</code>, and the project read tools, and can clone a project read-only into <code>/home/agent/conversation</code>.
      </p>
      {projects.length === 0 ? (
        <p className="text-sm text-text-dim">No active project yet.</p>
      ) : (
        <ul className="space-y-2 text-sm">
          {projects.map((p) => {
            const backlog = p.byStage?.backlog ?? 0
            const inProg = p.byStage?.in_progress ?? 0
            const review = p.byStage?.review ?? 0
            const done = p.byStage?.done ?? 0
            return (
              <li key={p.key} className="rounded border border-border bg-bg p-2" data-map-project={p.name}>
                <div className="flex items-center justify-between gap-2">
                  <Link to="/p/$project/board" params={{ project: p.name }} className="font-semibold text-accent hover:underline">
                    {p.name} <span className="font-mono text-xs text-text-dim">({p.key})</span>
                  </Link>
                  <span className="text-xs text-text-dim">
                    {p.working > 0 ? `${p.working} running` : p.state}
                  </span>
                </div>
                <p className="mt-1 font-mono text-xs text-text-dim">
                  backlog {backlog} · in progress {inProg} · review {review} · done {done}
                </p>
              </li>
            )
          })}
        </ul>
      )}
    </Card>
  )
}
