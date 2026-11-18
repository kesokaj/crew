import { Link } from '@tanstack/react-router'
import { useState } from 'react'
import type { ActivityLine } from '../lib/api'
import { usePoll } from '../lib/poll'
import { button, Failed, when } from './ui'

const kindWord: Record<string, string> = {
  move: 'MOVE',
  agent: 'AGENT',
  check: 'CHECK',
  ticket: 'TICKET',
  decision: 'DECISION',
  git: 'GIT',
  news: 'NEWS',
}

export function stripBackticks(text: string): string {
  return text.replace(/`([^`]+)`/g, '$1')
}

// The activity feed (design 20.1, activity.png): one line per thing that happened, newest first,
// with a link to its ticket. Across all projects each line names its project.
export function Feed({
  project,
  allProjects,
  pageSize = 25,
}: {
  project?: string
  allProjects?: boolean
  pageSize?: number
}) {
  const path = allProjects ? '/api/activity' : `/api/projects/${encodeURIComponent(project ?? '')}/activity`
  const { data, error } = usePoll<{ lines: ActivityLine[] }>(path, 5000)
  const [visibleCount, setVisibleCount] = useState(pageSize)
  const lines = data?.lines ?? []
  const shownLines = lines.slice(0, visibleCount)
  return (
    <div>
      <Failed error={error} />
      {data && lines.length === 0 && <p className="text-text-dim">Nothing has happened yet.</p>}
      <FeedTable lines={shownLines} allProjects={allProjects} />
      {lines.length > visibleCount && (
        <button
          type="button"
          className={button + ' mt-3'}
          onClick={() => setVisibleCount((c) => c + pageSize)}
        >
          Show 25 more ({lines.length - visibleCount} remaining)
        </button>
      )}
    </div>
  )
}

export function FeedTable({ lines, allProjects }: { lines: ActivityLine[]; allProjects?: boolean }) {
  return (
    <table className="w-full text-sm">
      <tbody>
        {lines.map((l, i) => (
          <tr key={i} className="border-b border-border/60 align-top">
            <td className="py-1.5 pr-3 font-mono text-xs whitespace-nowrap text-text-dim">{when(l.at)}</td>
            <td className={'w-20 py-1.5 pr-3 font-mono text-xs whitespace-nowrap ' + (l.kind === 'move' || l.kind === 'git' ? 'text-text-dim' : 'text-accent')}>
              {kindWord[l.kind] ?? l.kind}
            </td>
            <td className="py-1.5">
              {/* News of no project, such as a restart for security updates, is on the Crew level (inbox.png's "crew level"). */}
              {allProjects && <span className="mr-2 font-mono text-xs text-text-dim">{l.project || 'crew level'}</span>}
              {/* What an agent did or said, and news with its stored text: data, not the app's own words. */}
              <span data-free-text>{stripBackticks(l.text)}</span>{' '}
              {l.ticket && (
                <Link
                  to="/p/$project/tickets/$key"
                  params={{ project: l.project, key: l.ticket }}
                  className="font-mono text-xs text-accent underline"
                >
                  {l.ticket}
                </Link>
              )}
            </td>
          </tr>
        ))}
      </tbody>
    </table>
  )
}
