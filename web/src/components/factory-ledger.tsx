import { Link } from '@tanstack/react-router'
import { useState } from 'react'
import { getJSON } from '../lib/api'
import { usePoll } from '../lib/poll'
import { Card, Failed, Tag, ago, button } from './ui'

type FactoryTask = {
  id: string
  key: string
  kind: string
  title: string
  role: string
  state: string
  ticketKey?: string
  specRef?: string
  originRoom?: string
  triesUsed: number
  maxTries: number
  tokensIn: number
  tokensOut: number
  artifactPath?: string
  createdAt: string
}

type TasksLedgerView = {
  project: string
  key: string
  kanbanTickets: { key: string; title: string; kind: string; stage: string; triesUsed: number; maxTries: number }[]
  factoryTasks: FactoryTask[]
}

export function FactoryTasksLedger({ project }: { project: string }) {
  const enc = encodeURIComponent(project)
  const { data, error } = usePoll<TasksLedgerView>(`/api/projects/${enc}/tasks-ledger`, 8000)

  return (
    <div className="mt-6">
      <Card
        title="Tracked Tasks API Ledger (R217 Non-Ticket Compute)"
        right={
          data && (
            <span className="text-xs text-text-dim">
              {data.kanbanTickets.length} board · {data.factoryTasks.length} off-board
            </span>
          )
        }
      >
        <Failed error={error} />
        {!data ? (
          <p className="text-sm text-text-dim">Loading tasks ledger…</p>
        ) : data.factoryTasks.length === 0 ? (
          <p className="text-sm text-text-dim">No off-board Tasks API records yet. Zero untracked compute is permitted.</p>
        ) : (
          <div className="overflow-x-auto">
            <table className="w-full text-sm" aria-label="Off-board factory tasks">
              <thead>
                <tr className="border-b border-border text-left text-xs text-text-dim uppercase">
                  <th className="py-1.5 pr-3">Task Key</th>
                  <th className="py-1.5 pr-3">Kind</th>
                  <th className="py-1.5 pr-3">Role</th>
                  <th className="py-1.5 pr-3">Title</th>
                  <th className="py-1.5 pr-3">State</th>
                  <th className="py-1.5 pr-3">Tries</th>
                  <th className="py-1.5 pr-3">Tokens</th>
                  <th className="py-1.5">Age</th>
                </tr>
              </thead>
              <tbody>
                {data.factoryTasks.slice(0, 20).map((ft) => (
                  <tr key={ft.id} className="border-b border-border last:border-b-0">
                    <td className="py-1.5 pr-3 font-mono text-xs">{ft.key}</td>
                    <td className="py-1.5 pr-3">
                      <Tag>{ft.kind}</Tag>
                    </td>
                    <td className="py-1.5 pr-3 font-mono text-xs">{ft.role}</td>
                    <td className="py-1.5 pr-3" data-free-text>
                      {ft.title}
                      {ft.specRef && <span className="ml-2 font-mono text-xs text-text-dim">{ft.specRef}</span>}
                    </td>
                    <td className="py-1.5 pr-3">
                      <Tag tone={ft.state === 'done' ? 'ok' : ft.state === 'failed' ? 'bad' : 'blue'}>{ft.state}</Tag>
                    </td>
                    <td className="py-1.5 pr-3 font-mono text-xs">
                      {ft.triesUsed}/{ft.maxTries}
                    </td>
                    <td className="py-1.5 pr-3 font-mono text-xs tabular-nums">{(ft.tokensIn + ft.tokensOut).toLocaleString()}</td>
                    <td className="py-1.5 text-xs text-text-dim">{ago(ft.createdAt)}</td>
                  </tr>
                ))}
              </tbody>
            </table>
          </div>
        )}
      </Card>
    </div>
  )
}

export function TicketYamlModal({ project, ticketKey, onClose }: { project: string; ticketKey: string; onClose: () => void }) {
  const { data, error } = usePoll<{ key: string; path: string; yaml: string }>(
    `/api/projects/${encodeURIComponent(project)}/tasks/${encodeURIComponent(ticketKey)}/yaml`,
    15000,
  )
  const [copied, setCopied] = useState(false)

  const copyYaml = () => {
    if (!data?.yaml || typeof navigator === 'undefined') return
    void navigator.clipboard?.writeText(data.yaml).then(() => setCopied(true))
  }

  return (
    <div className="fixed inset-0 z-50 flex items-center justify-center p-4" data-ticket-yaml-modal>
      <div className="fixed inset-0 bg-black/50" aria-hidden="true" onClick={onClose} />
      <section
        role="dialog"
        aria-modal="true"
        aria-label="Ticket YAML Contract"
        className="relative z-10 flex max-h-[80vh] w-full max-w-2xl flex-col overflow-hidden rounded-lg border border-border bg-bg-raised p-4 text-text shadow-2xl"
      >
        <div className="mb-3 flex flex-wrap items-center justify-between gap-2 border-b border-border pb-2">
          <div>
            <h2 className="text-base font-semibold">
              YAML Contract · <span className="font-mono">{ticketKey}</span>
            </h2>
            <p className="font-mono text-xs text-text-dim" data-free-text>
              {data?.path ?? `.crew/tickets/${ticketKey}.md`}
            </p>
          </div>
          <div className="flex items-center gap-2">
            <button type="button" className={button} onClick={copyYaml}>
              {copied ? 'Copied' : 'Copy'}
            </button>
            <Link to="/p/$project/tickets/$key" params={{ project, key: ticketKey }} className={button}>
              Open Ticket
            </Link>
            <button type="button" className={button} onClick={onClose}>
              Close
            </button>
          </div>
        </div>
        <Failed error={error} />
        {!data ? (
          <p className="text-sm text-text-dim">Loading YAML contract…</p>
        ) : (
          <pre className="overflow-auto rounded border border-border bg-bg p-3 font-mono text-xs leading-relaxed" data-free-text>
            {data.yaml}
          </pre>
        )}
      </section>
    </div>
  )
}

export async function fetchTicketYaml(project: string, ticketKey: string) {
  return getJSON<{ key: string; path: string; yaml: string }>(
    `/api/projects/${encodeURIComponent(project)}/tasks/${encodeURIComponent(ticketKey)}/yaml`,
  )
}
