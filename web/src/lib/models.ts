// The models, the agents, and Crew's choices (design 12.3, 12.7), as the server's routes give them.
// Since 0.53 nothing is priced: no model has a price, and no number of a choice is a cost.

export type ModelRow = {
  name: string
  model: string
  efforts: string[]
  inName: boolean
  display?: string
  resolved?: string
  family: string
  group: string
  groupName: string
  offered: boolean
  refused: boolean
  addedBy: string
  firstSeen: string
  goneAt?: string
}

export type QuotaWindow = {
  cli: string
  group: string
  groupName: string
  window: string
  used: number
  resetsAt?: string
  reachedUntil?: string
  source: string
  readAt: string
  close: boolean
  running: number
}

export type CLIModels = {
  cli: string
  signedIn: boolean
  inUse: boolean
  readAt?: string
  short: boolean
  failures: number
  error?: string
  models: ModelRow[]
  windows: QuotaWindow[]
}

export type Choice = {
  CLI: string
  Model: string
  Effort: string
  Name: string
  Flag: boolean
  ModelFrom: string
  EffortFrom: string
  Off: boolean
  Ignored?: string[]
  Short: boolean
}

export type EntryView = { entry: string; offered: boolean; canRun: boolean; why?: string; family?: string; short?: boolean }

export type AgentView = {
  role: string
  entries: EntryView[]
  effort: string
  state: 'automatic' | 'set_by_you' | 'list'
  runsOn?: Choice
  waits?: string
  checkRule?: string
}

export type Pick = { entry: string; effort: string }

// A card stored before 0.53 may still hold costUsd: it is not read.
export type Stat = { units: number; passed: number; fixRounds: number; seconds: number; escaped: number; askedFix?: number; checkers?: string[] }

export type Card = { from: Pick; to: Pick; fromStats: Stat; toStats: Stat; watched?: Stat }

export type ModelChoice = {
  id: number
  role: string
  kind: 'change' | 'back' | 'recommend'
  reason: string
  from: Pick
  to: Pick
  numbers: Card
  state: string
  who: string
  at: string
  decidedAt?: string
  hideUntil?: string
}

export type Comparison = { id: number; role: string; own: Pick; other: Pick; state: string; numbers: Card; startedAt: string; endedAt?: string }

export type Numbers = {
  role: string
  cli: string
  model: string
  effort: string
  kind?: string
  units: number
  passed: number
  askedFix: number
  escaped: number
  fixRounds: number
  seconds: number
  checkers?: string[]
}

export type ProjectModels = { name: string; key: string; roles: Record<string, Choice>; waits: Record<string, string>; own: string[] }

export type ModelsPage = {
  clis: CLIModels[]
  agents: AgentView[]
  choices: ModelChoice[]
  comparisons: Comparison[]
  results: Numbers[]
  projects: ProjectModels[]
  auto: { on: boolean; setByYou: string[]; windowDays: number; minRuns: number; exploreShare: number; compareRuns: number }
  chosen: string[]
  autoRoles: string[]
}

export type OfferedModel = { entry: string; cli: string; model: string; display?: string; family: string; efforts: string[] }

// What each agent does, as the Models screen lists it (12.3).
export const agentDoes: Record<string, { name: string; does: string }> = {
  curator: { name: 'Curator', does: 'Analyses tickets, looks again at a ticket that cannot get through, keeps knowledge healthy, writes the final report' },
  lead: { name: 'Lead', does: 'Breaks the plan down, analyses tickets, looks again at a ticket that cannot get through, writes the final report' },
  builder: { name: 'Builder', does: "Builds a ticket on its own branch, and fixes it in place" },
  reviewer: { name: 'Reviewer', does: 'Reads the change and gives a verdict, on another family than the builder' },
  tester: { name: 'Tester', does: 'Adds the tests that are missing and runs them in a fresh sandbox' },
  e2e: { name: 'E2E tester', does: 'Starts the whole application and drives its user flows' },
  verifier: { name: 'Verifier', does: 'Checks the result against the goal and the plan, on another family than the builder' },
  summary: { name: 'Summary writer', does: 'Writes the summary of a finished ticket: one call, no tools' },
  prompt: { name: 'Prompt writer', does: 'Proposes a better wording of what you write in planning; you choose which text is sent' },
  planner: { name: 'Planning agent', does: 'Develops a plan with you, and breaks an approved plan into tickets' },
  assistant: { name: 'Assistant', does: 'Talks with you in a conversation, and in the chat of a ticket' },
  judge: { name: 'Judge', does: 'Runs judge: checks' },
  improver: { name: 'Improver', does: 'Reads the signals and proposes improvements' },
}

export const agentName = (role: string) => agentDoes[role]?.name ?? role

// Where a run's model came from, in words (15.3, the run record).
export function fromWords(from?: string): string {
  switch (from) {
    case 'ticket':
      return 'pinned on the ticket'
    case 'project':
      return "the project's setting"
    case 'operator':
      return 'what you set for the machine'
    case 'automatic':
      return "Crew's automatic choice"
    case 'list':
      return "the agent's list"
    case 'try':
      return 'a try'
    case 'comparison':
      return 'a comparison'
    case 'check_rule':
      return 'the rule for checks'
    case 'limit':
      return 'moved at a limit or a lost sign-in'
    case 'nearest':
      return 'the nearest effort the model has'
    case 'machine':
      return "the machine's entry"
  }
  return from ?? ''
}

// reasonFor is reasonWords for the role: an agent that checks is better when fewer defects escaped it (12.3).
export function reasonFor(role: string, reason: string): string {
  if (reason === 'better' && role !== 'builder') return 'fewer defects escaped, as many fixes asked'
  return reasonWords[reason] ?? reason
}

export const reasonWords: Record<string, string> = {
  better: 'passed more often',
  // Only on choices made before 0.53: since then results alone decide (12.3).
  cheaper: 'as well, for less (before 0.53)',
  worse: 'the watched tickets did worse',
  operator: 'you went back',
  newer: 'newer in its bracket',
}

// pct says a share as whole percent.
export const pct = (n: number, d: number) => (d ? `${Math.round((100 * n) / d)}%` : '—')

// familyName is the heading of a model family on the Models screen (20.1, models.png).
export const familyName: Record<string, string> = { gemini: 'Gemini', claude: 'Claude', gpt: 'GPT' }

// choiceName is a choice as a short tag: "agy · gemini-3.8-flash-high", "agy · claude-opus-5-5-high".
export function choiceName(c?: Choice): string {
  if (!c) return ''
  return c.Flag || (c.Name === c.Model && c.Effort) ? `${c.CLI} · ${c.Name} · ${c.Effort}` : `${c.CLI} · ${c.Name}`
}
