import type { ReactNode } from 'react'

// An improvement of the baseline as the Crew level shows it (10.5).
export type Improvement = {
  number: number
  title: string
  status: string
  waitsFor?: string | null
  target: string[]
  evidence: { signal?: string; window?: string; projects?: string; tasks?: string; tickets?: string[]; text?: string }
  body: string
  expected: string
  measure: string
  refusedWhy?: string | null
  // Evals of before 0.53 may hold costBefore and costAfter: since 0.53 they are not shown.
  // unmeasuredRuns, since 1.10, are runs of the change that measured nothing, counted as goals not met.
  evals?: {
    projects?: number
    cases?: number
    passedBefore?: number
    passedAfter?: number
    margin?: number
    verdict?: string
    why?: string
    unmeasuredRuns?: number
  } | null
  decidedBy?: string | null
  ticket?: string | null
  releaseFrom?: string | null
  releaseTo?: string | null
  result?: { before: number; after: number; verdict: string; why: string; ticketsBefore: number; ticketsAfter: number } | null
  createdAt: string
  examples?: { rewrite: number; original: string; old: string; new: string }[]
}

export const iNum = (n: number) => 'i_' + String(n).padStart(4, '0')

// status says where an improvement is, in the words of the screens (improvements.png).
export function status(x: Improvement): ReactNode {
  switch (x.status) {
    case 'refused':
      return <span className="text-bad">refused by the checks</span>
    case 'testing':
      // Evals that could not tell for runs that measured nothing are not helped by more cases (since 1.10).
      if (x.waitsFor === 'cases' && (x.evals?.unmeasuredRuns ?? 0) > 0) return <span className="text-text-dim">waits: its evals could not tell</span>
      return x.waitsFor === 'cases' ? <span className="text-text-dim">waits for more eval cases</span> : <span className="text-accent">being checked</span>
    case 'proposed':
      return x.waitsFor === 'operator' ? <span className="text-amber-text">waits for you</span> : <span className="text-accent">being checked</span>
    case 'passed':
      if (x.waitsFor === 'operator') return <span className="text-amber-text">passed, waits for you</span>
      if (x.waitsFor === 'crew_project') return <span className="text-accent">passed, waits for Crew's project</span>
      return <span className="text-accent">passed{x.ticket ? `, ticket ${x.ticket}` : ''}</span>
    case 'applied':
      return (
        <span className="text-ok">
          ✓ applied <span className="font-mono text-xs">{x.releaseTo}</span>
        </span>
      )
    case 'removed':
      return <span className="text-text-dim">taken out by you</span>
    case 'rolled_back':
      return <span className="text-text-dim">rolled back by Crew</span>
  }
  return x.status
}

