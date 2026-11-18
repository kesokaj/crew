import { createFileRoute } from '@tanstack/react-router'
import { useState } from 'react'
import { send, type Decision, type RunDetail, type Subtask, type Summary, type TaskGit, type TaskView } from '../lib/api'
import { usePoll } from '../lib/poll'
import { WriteScope } from '../components/writebox'
import { Box, TicketHeader, type WorkspaceTab } from '../components/ticket/ticket-header'
import { DecisionsBox, SummaryCard, WhyWhatGoalBox } from '../components/ticket/ticket-spec-tab'
import { TicketYamlAndThreadBox } from '../components/ticket/ticket-yaml-thread'
import { GitPart, Outcome, RunningNow } from '../components/ticket/ticket-run-tab'
import { SubtaskTable, TicketPromptBox } from '../components/ticket/ticket-prompt-tab'
import { AnalysisBox, LinksBox, Records, SourcePlanBox, TicketHistoryBox } from '../components/ticket/ticket-history-tab'
import { AgentBox, MarkMenu, readMark, TicketChat, type Mark } from '../components/ticket/ticket-sidebar'

export { Box }
export const Route = createFileRoute('/p/$project/tickets/$key')({ component: Task })

type Stages = { stages: { id: string; optional: boolean; on: boolean }[] }

export function isCurrentTicketEnded(v: Pick<TaskView, 'ticket'> | null, key: string): boolean {
  if (!v || v.ticket.key !== key) return false
  return v.ticket.stage === 'done' || v.ticket.stage === 'closed'
}

// The task view (design 20.2, task.png, task-done.png). From top to bottom it answers what is
// happening now, what the agent was told, where the ticket comes from, and what has been done. The
// same view shows a finished ticket: the outcome replaces Running now, and every subtask keeps its
// prompt.
function Task() {
  const { project, key } = Route.useParams()
  return <TaskWorkspace key={`${project}/${key}`} project={project} ticketKey={key} />
}

function TaskWorkspace({ project, ticketKey: key }: { project: string; ticketKey: string }) {
  const base = `/api/projects/${encodeURIComponent(project)}/tasks/${encodeURIComponent(key)}`
  const { data: rawView, error, reload } = usePoll<TaskView>(base, 4000)
  const v = rawView && rawView.ticket.key === key ? rawView : null
  const { data: subs } = usePoll<{ subtasks: Subtask[] }>(base + '/subtasks', 4000)
  const { data: git } = usePoll<TaskGit>(base + '/git', 10000)
  const { data: stages } = usePoll<Stages>(`/api/projects/${encodeURIComponent(project)}/stages`, 0)
  const { data: decisions } = usePoll<{ decisions: Decision[] }>(`/api/projects/${encodeURIComponent(project)}/decisions`, 10000)
  const ended = isCurrentTicketEnded(v, key)
  const { data: summary } = usePoll<Summary>(ended ? base + '/summary' : null, 10000)
  const live = (subs?.subtasks ?? []).flatMap((s) => s.runs.filter((r) => r.state !== 'ended').map((r) => ({ s, r })))
  const lastRun = (subs?.subtasks ?? []).flatMap((s) => s.runs).at(-1)
  // A finished ticket shows the prompt the builder was given last, as task-done.png does.
  const lastBuild = (subs?.subtasks ?? []).filter((s) => s.role === 'builder').flatMap((s) => s.runs).at(-1)
  const shown = live[0]?.r.id ?? lastBuild?.id ?? lastRun?.id
  const { data: run } = usePoll<RunDetail>(shown ? `/api/runs/${shown}` : null, live.length ? 4000 : 0)
  const [failed, setFailed] = useState('')
  const [marked, setMarked] = useState<Mark | null>(null)
  const [menu, setMenu] = useState<(Mark & { x: number; y: number }) | null>(null)
  const [activeTab, setActiveTab] = useState<WorkspaceTab>('all')

  if (!v) return <div className="p-6 text-text-dim">{error || 'Loading…'}</div>
  const t = v.ticket
  const patch = (body: unknown) =>
    fetch(base, {
      method: 'PATCH',
      credentials: 'same-origin',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify(body),
    }).then(async (r) => {
      if (!r.ok) setFailed(((await r.json().catch(() => null)) as { error?: { message?: string } } | null)?.error?.message ?? r.statusText)
      else setFailed('')
      reload()
    })
  const decide = (choice: string) =>
    send('POST', base + '/decision', { choice }).then(
      () => {
        setFailed('')
        reload()
      },
      (e: Error) => setFailed(e.message),
    )
  const mine = (decisions?.decisions ?? []).filter((d) => d.ticket === t.key)
  const comment = () => {
    if (!menu) return
    setMarked({ quote: menu.quote, source: menu.source })
    setMenu(null)
    window.getSelection()?.removeAllRanges()
    document.getElementById('chat')?.scrollIntoView({ behavior: 'smooth', block: 'start' })
    document.getElementById('chat-box')?.focus()
  }

  const showSpec = activeTab === 'all' || activeTab === 'spec'
  const showRun = activeTab === 'all' || activeTab === 'run'
  const showPrompt = activeTab === 'all' || activeTab === 'prompt'
  const showHistory = activeTab === 'all' || activeTab === 'history'

  return (
    <div className="mx-auto max-w-7xl p-6" onMouseUp={() => setMenu(readMark())} onKeyUp={() => setMenu(readMark())}>
      {menu && <MarkMenu at={menu} onComment={comment} />}
      <TicketHeader
        project={project}
        v={v}
        stages={stages?.stages ?? []}
        live={live}
        ended={ended}
        error={error}
        failed={failed}
        activeTab={activeTab}
        onTabChange={setActiveTab}
        onPatch={(body) => void patch(body)}
        onDecide={(choice) => void decide(choice)}
      />

      <div className="mt-4 grid gap-4 lg:grid-cols-[minmax(0,1fr)_340px]">
        <div className="min-w-0 space-y-4">
          {showRun && (ended ? <Outcome project={project} t={v} git={git} lastRun={lastRun} /> : <RunningNow project={project} v={v} live={live} run={run} />)}
          {showSpec && <WhyWhatGoalBox v={v} />}
          {showSpec && <TicketYamlAndThreadBox project={project} ticketKey={t.key} />}
          {activeTab === 'all' && (
            <WriteScope.Provider value={{ project }}>
              <TicketChat base={base} project={project} ticket={t.key} ended={ended} marked={marked} setMarked={setMarked} onTold={reload} />
            </WriteScope.Provider>
          )}
          {showSpec && ended && summary && <SummaryCard s={summary} />}
          {showSpec && <DecisionsBox project={project} mine={mine} />}
          {showRun && <SubtaskTable subs={subs?.subtasks ?? []} />}
          {showRun && <GitPart project={project} t={v} git={git} />}
          {showPrompt && <TicketPromptBox run={run} subs={subs?.subtasks ?? []} isLive={live.length > 0} />}
          {showHistory && <Records records={v.records} files={v.files ?? {}} project={project} ended={ended} />}
          {activeTab === 'history' && <TicketHistoryBox history={v.history} />}
        </div>

        <aside className="min-w-0 space-y-4">
          {activeTab !== 'all' && (
            <WriteScope.Provider value={{ project }}>
              <TicketChat base={base} project={project} ticket={t.key} ended={ended} marked={marked} setMarked={setMarked} onTold={reload} />
            </WriteScope.Provider>
          )}
          <AgentBox base={base} ended={ended} subs={subs?.subtasks ?? []} network={t.network ?? 'inherit'} onChange={reload} />
          <SourcePlanBox project={project} t={t} />
          <AnalysisBox t={t} />
          <LinksBox project={project} v={v} />
          {activeTab !== 'history' && <TicketHistoryBox history={v.history} />}
        </aside>
      </div>
    </div>
  )
}
