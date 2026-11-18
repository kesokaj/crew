// The server's API. Every answer that is not 2xx has the shape {"error": {"code", "status", "message"}};
// a 401 that asks for a sign-in names it in "details" (design 20.3), which the page's dialog takes.
import { authProblemOf, reportAuthProblem, type AuthProblem } from './session'
import type { ActiveTicketsSummary } from './sandbox-types'
export type {
  ActiveTicketsSummary,
  AllSandboxesView,
  EphemeralSandboxOverview,
  MachineSandboxSummary,
  ProjectSandboxOverview,
  ProjectSandboxStatus,
  ProjectSnapshot,
  SandboxActiveRun,
  SandboxRoomWait,
} from './sandbox-types'

export class ApiError extends Error {
  readonly code: number
  readonly status: string
  readonly auth: AuthProblem | null
  constructor(code: number, status: string, message: string, auth: AuthProblem | null = null) {
    super(message)
    this.code = code
    this.status = status
    this.auth = auth
  }
}

// refusal is the error of an answer that is not 2xx; one that asks for a sign-in is reported too.
function refusal(res: Response, body: unknown): ApiError {
  const err = (body as { error?: { status?: string; message?: string } } | null)?.error
  const auth = authProblemOf(res.status, body)
  if (auth) reportAuthProblem(auth)
  return new ApiError(res.status, err?.status ?? 'UNKNOWN', err?.message ?? res.statusText, auth)
}

export async function getJSON<T>(path: string): Promise<T> {
  const res = await fetch(path, { credentials: 'same-origin' })
  const body: unknown = await res.json().catch(() => null)
  if (!res.ok) throw refusal(res, body)
  return body as T
}

// send makes a change: the server takes it only from a page of Crew's own address (design 20.3), which
// the browser says with the Origin header of a same-origin fetch.
export async function send<T>(method: 'POST' | 'PUT' | 'PATCH' | 'DELETE', path: string, body?: unknown): Promise<T | null> {
  const res = await fetch(path, {
    method,
    credentials: 'same-origin',
    headers: body === undefined ? {} : { 'Content-Type': 'application/json' },
    body: body === undefined ? undefined : JSON.stringify(body),
  })
  const text = await res.text()
  const parsed: unknown = text ? JSON.parse(text) : null
  if (!res.ok) throw refusal(res, parsed)
  return parsed as T | null
}

// sendForFile makes a change whose answer is a file, such as a project's export: a refusal is an
// ApiError as send's is, so one that asks for a sign-in opens the page's dialog.
export async function sendForFile(path: string): Promise<Blob> {
  const res = await fetch(path, { method: 'POST', credentials: 'same-origin' })
  if (!res.ok) throw refusal(res, await res.json().catch(() => null))
  return res.blob()
}

// Setup (design 12.6): what is set up, never a secret.
export type SetupItem = { id: string; set: boolean; unreadable?: boolean; account?: string; updatedAt?: string; createdAt?: string }
export type MachineCheck = { docker: string; runtime: string; runtimeOK: boolean }
export type SetupStatus = { credentials: SetupItem[]; publicKey?: string; running: string; machine?: MachineCheck }
export type AppInfo = { slug: string; name: string; owner: string; installations: string[]; permissions: Record<string, string> }

// The mail (design 18): Setup's mail account, and every message Crew sent or will send.
export type MailAccount = { account: string | null; state: 'none' | 'untested' | 'ok' | 'refused'; okAt?: string; refusedAt?: string; error?: string }
export type MailLink = { label: string; url: string; main?: boolean }
// GET /api/notifications's Mail card: where the mail stands, and what the settings send.
export type MailView = {
  on: boolean
  why?: string
  to: string
  fromName: string
  account: string | null
  state: MailAccount['state']
  itemsOn: boolean
  newsOn: boolean
  quietHours: string
  remindAfterHours: number
}
export type Notification = {
  id: number
  kind: 'needs_you' | 'reminder' | 'news' | 'digest' | 'test'
  item: string | null
  project: string
  subject: string
  body: string
  links: MailLink[]
  state: 'queued' | 'sending' | 'sent' | 'failed' | 'dropped'
  error: string | null
  createdAt: string
  sentAt: string | null
  open: boolean
}

// The Agent CLIs screen (design 12.6, 20.1).
export type CLIState = {
  cli: string
  state: 'unknown' | 'signed_in' | 'signed_out'
  failures: number
  checkedAt?: string
  result: string
  realAt?: string
  version?: string
  checking: boolean
  setUp: boolean
  account?: string
  signedAt?: string
}

// The network modes of a sandbox (design 13.6).
export type NetworkMode = 'internet' | 'model_only' | 'lan'

// Runs (design 8.1, 8.3, 12.4, 12.10). A run is one agent CLI in one microVM, on a subtask.
export type RunView = {
  id: string
  subtask: number
  number: number
  title: string
  subtaskState: string
  waitFor?: string
  state: 'starting' | 'working' | 'idle' | 'ended'
  stopping?: string
  stopReason?: string
  stopDetail?: string
  cli: string
  cliVersion?: string
  model?: string
  effort?: string
  crewVersion: string
  image: string
  sandbox?: string
  mode?: NetworkMode | ""
  conversation: string
  cliConversation?: string
  way: 'new' | 'same' | 'files' | 'rebuilt'
  continues?: string
  branch: string
  base?: string
  wip?: string
  turn: number
  events: number
  toolSteps: number
  maxTurns: number
  timeoutSeconds: number
  tokensIn: number
  tokensOut: number
  tokensCached: number
  lastAction?: string
  warnings: { at: string; text: string }[]
  startedAt: string
  lastEventAt?: string
  endedAt?: string
  exitCode?: number
  promptSha256?: string
  role: string
  kind: string
  project?: string
  ticket?: string
  subtaskNumber: number
  push: boolean
  tools: boolean
  modelFrom?: string
  // The size of its sandbox (13.10); absent for a run from before 0.47.
  size?: 'small' | 'large'
}

export type RunList = { running: RunView[]; waiting: RunView[]; finished: RunView[] }

export type Turn = { number: number; run?: string; speaker: string; text: string; toolCalls: string[]; tokens: number; rebuilt: boolean; at: string }

export type CLIFile = { path: string; bytes: number; savedAt: string; keepUntil: string }

export type PromptPart = { part: string; source: string; text: string }

export type RunDetail = RunView & {
  prompt: string
  parts: PromptPart[]
  command: string[]
  turns: Turn[]
  runs: RunView[]
  cliFiles: CLIFile[]
  // What a microVM of its size has, by the machine's settings now, with the 1 CPU and 2 GB of every microVM.
  sizing?: { cpus: number; memMB: number; docker: boolean }
}

export type RunEvent = { seq: number; at: string; type: string; payload: Record<string, unknown>; raw?: string }

export type RunOptions = { clis: { cli: string; signedIn: boolean; models: string[] }[]; project: string }

// The frame of every page (design 20): the top bar and the menu.
export type FrameProject = { name: string; key: string; state: string; needsYou: number; working: number }
export type Frame = {
  email: string
  version: string
  leadSlots: { cli: string; used: number; slots: number }[]
  activeTickets?: ActiveTicketsSummary
  sandboxes: number
  inbox: number
  drafts: number
  projects: FrameProject[]
  // Why nothing new starts, while Crew is paused, stopped, held because microVMs do not boot, or its
  // disk is full (design 7.9, 21).
  held?: string
  // The release that waits to be installed (design 16.3).
  nextVersion?: string
}

// Pause all, Stop all, the disk guard's stop, and microVMs that do not boot (design 7.9, 16.10, 21).
export type HoldState = {
  state: {
    pausedAt?: string
    pausedBy?: string
    why?: 'pause' | 'stop'
    diskFull?: string
    diskAt?: string
    bootFailed?: string
    bootFailedAt?: string
    bootFailures: number
    // A restart for security updates (16.10): wanted since, waited 7 days since, and going down now.
    rebootWanted?: string
    restartWait?: string
    restarting?: boolean
  }
  held: string
}

// The inbox (design 7.8): what needs the operator.
export type InboxAlternative = { label: string; action?: string; body?: Record<string, unknown>; link?: string; main?: boolean }
export type InboxItem = {
  id: string
  kind: string
  project?: string
  title: string
  body: string
  since: string
  alternatives: InboxAlternative[]
  // When its open episode was mailed (18.1): the first mail, and a reminder.
  notified: { kind: string; sentAt: string }[]
}

// GET /api/inbox/item: the item a mail's link names, and what was pressed about it (18.3).
export type InboxItemAnswer = { item: InboxItem | null; decisions: { label: string; by: string; at: string }[]; notified: { kind: string; sentAt: string }[] }

// One line of the activity feed (design 20.1).
export type ActivityLine = { at: string; kind: string; project: string; ticket?: string | null; text: string }

// A ticket and what the task view reads of it (design 6.3, 20.2).
export type Link = { kind: string; to: string }
export type CheckSpec = { kind: string; spec: string; keeps?: boolean }
export type Ticket = {
  project: string
  key: string
  number: number
  title: string
  kind: string
  part?: string
  source?: { file: string; heading?: string; quote?: string }
  why: string
  what: string
  goal: string
  approach?: string
  docs: string[]
  stage: string
  closedReason?: string
  skips: string[]
  skipsReason?: string
  // The ticket's own network mode, or inherit for its project's (design 13.6).
  network?: NetworkMode | 'inherit'
  origin: string
  createdBy: string
  analysis: { status: string; decidedBy?: string; reason?: string }
  priority: number
  hold: boolean
  noticeUntil?: string
  branch?: string
  fixRounds: number
  // 1 once the lead gave the ticket a new approach (design 7.7).
  approaches: number
  waiting?: string
  stuck?: string
  stageAt: string
  createdAt: string
  checks: CheckSpec[]
  links: Link[]
}
export type Change = { Seq: number; At: string; Who: string; Table: string; RowKey: string; Before: Record<string, unknown> | null; After: Record<string, unknown> | null }
export type TicketRecord = {
  id: number
  kind: string
  author: string
  // The marked text an operator's comment is about, and where it was marked (11.9, D27).
  quote?: string
  quoteSource?: QuoteSource
  run?: string
  subtask?: number
  commit?: string
  verdict?: string
  result?: string
  body: string
  inReplyTo?: number
  resolved: boolean
  createdAt: string
}
export type CheckRun = {
  id: number
  purpose: string
  commit: string
  state: string
  results: { name: string; code: number; passed: boolean; error?: string; output?: string }[]
  detail?: string
  flaky: boolean
  startedAt: string
  endedAt?: string
}
export type TaskView = {
  ticket: Ticket
  history: Change[]
  records: TicketRecord[]
  checkRuns: CheckRun[]
  linkedFrom: Link[]
  // The images kept with entries, by the entry's id: an E2E tester's screenshots (design 7.4).
  files?: Record<string, { name: string; type: string }[]>
}
export type TaskRun = {
  id: string
  state: string
  cli: string
  model?: string
  effort?: string
  modelFrom?: string
  way: string
  stopReason?: string
  // Its tokens in and out (design 0.53: no cost).
  tokens: number
  startedAt: string
  endedAt?: string
}
export type Subtask = {
  id: string
  kind: string
  role: string
  title: string
  state: string
  commit?: string
  createdAt: string
  steps: { text: string; done: boolean }[]
  runs: TaskRun[]
  endedAt?: string
  createdBy: string
  createdIn?: string
  branch?: string
  touches?: string[]
  joinedAt?: string
  parent?: string
  added: boolean
}
export type Summary = {
  ticket: string
  title: string
  outcome: string
  facts: Record<string, unknown>
  whatWasDone?: string
  howItWent?: string
  whatToKnowNextTime?: string
  missing: boolean
  writtenAt?: string
}
export type Decision = {
  id: number
  ticket?: string
  role: string
  run?: string
  question: string
  alternatives: string[]
  choice: string
  reason: string
  createdAt: string
  changedTo?: string
  changeTicket?: string
  changedAt?: string
}
export type Commit = { id: string; parents: string[]; author: string; email: string; when: string; subject: string; body: string; ref?: string; cut?: boolean }
export type FileDiff = { path: string; added: number; removed: number; binary: boolean; patch: string; cut: boolean; omitted?: number }
export type TreeEntry = { path: string; size: number }
export type BlobView = { path: string; size: number; binary: boolean; content: string; cut: boolean }
export type OverviewSandbox = {
  id: string
  kind: string
  size: string
  state: string
  project?: string
  ticket?: string
  run?: string
  role?: string
  title?: string
  startedAt: string
}
export type PushGroup = { subtask: string; title: string; role: string; who: string; from: string; to: string; at: string; commits: Commit[] }
export type TaskGit = {
  branch?: string
  pushed: boolean
  mergedAs?: string
  pull?: PullRequest
  groups: PushGroup[]
  other: Commit[]
  files: FileDiff[]
  omitted?: number
}

// The Git screen (design 6.7).
export type GitBranch = { name: string; head: string; ahead: number; behind: number; when: string; ticket?: string; stage?: string }
export type GitPush = { remote: string; refs: string[]; state: string; tries: number; detail: string; createdAt: string; doneAt?: string }
// A ticket's pull request, as Crew keeps it (design 7.12).
export type PullRequest = { number?: number; ticket: string; title: string; state: 'opening' | 'open' | 'merged' | 'closed'; branch: string; url?: string; held: boolean }
// What came in on the remote's main from outside Crew (design 6.6, 7.12).
export type RemoteMain = { remote: string; kind: 'ahead' | 'diverged'; state: string; detail?: string; ticket?: string; at: string }
export type ProjectGit = {
  source: string
  remote?: string
  main: string
  pushes: GitPush[]
  pulls?: PullRequest[]
  usesPulls?: boolean
  fetchedAt?: string
  fetchDetail?: string
  outside?: RemoteMain[]
}

// The prompt writer (design 7.2).
export type Rewrite = {
  id: number
  kind: 'first' | 'improve' | 'review'
  draft?: string
  project?: string
  round: number
  original: string
  state: 'writing' | 'ready' | 'good' | 'late' | 'failed' | 'off'
  rewrite?: string
  changes: string[]
  assumptions: string[]
  questions: string[]
  notes: string[]
  detail?: string
  choice?: string
  sent?: string
  cli?: string
  model?: string
  effort?: string
}

// A plan draft (design 7.2, 20.1 Plans).
export type DraftMessage = { id: number; text: string; at: string; quote?: string; ask?: string; rewrite?: number; taken: boolean }
export type DraftTurn = { number: number; speaker: string; text: string; run?: string; cli?: string; model?: string; effort?: string; at: string; rebuilt: boolean }
export type Draft = {
  id: string
  title: string
  state: string
  repo?: string
  project?: string
  createdAt: string
  updatedAt: string
  version: number
  plan: string
  waiting: DraftMessage[]
  messages: DraftMessage[]
  firstRewrite?: number
  turns: DraftTurn[]
  agent?: { run: string; state: string; cli: string; model?: string; effort?: string; stopReason?: string }
}
export type DraftSummary = { id: string; title: string; state: string; project?: string; version: number; waiting: number; updatedAt: string }

// The operator's conversations and the tickets' chats (design 11.8, 11.9; plan M20): one shape for
// both, read at /api/conversations/{id} and at /api/projects/{p}/tasks/{t}/chat.
export type QuoteSource = { part: string; label: string; run?: string; subtask?: number }
// The author of a comment sent on with Tell the agent, and of a memory entry kept with Remember this,
// from an answer of the assistant's: board.ChatAuthor (11.9, plan M20 D11).
export const chatAuthor = 'assistant (from the chat)'
export type ConvEntry = {
  kind: 'you' | 'agent'
  id?: number
  text: string
  quote?: string
  source?: QuoteSource
  run?: string
  turn?: number
  tools?: string[]
  way?: 'new' | 'same' | 'files' | 'rebuilt' | ''
  cli?: string
  model?: string
  effort?: string
  ended?: { reason: string; detail: string }
  at: string
  tickets?: string[]
}
export type ConvView = {
  id?: string
  purpose: 'operator' | 'chat'
  project?: string // its key
  projectName?: string
  ticket?: string
  title: string
  closedAt?: string
  savedAt?: string
  network?: string
  agent: { cli?: string; model?: string; effort?: string; pickedByYou: boolean }
  cliId?: string
  turns: number
  tokens: number
  sandbox: { state: 'none' | 'running' | 'idle' | 'saved' | 'closed'; id?: string; size?: string; network?: string }
  waiting?: string
  entries: ConvEntry[]
  cameOf: { tickets: { key: string; title: string; adopted: boolean; project?: string; projectName?: string }[]; draft?: string }
  workFolder?: { bytes: number; files: number; savedAt: string; note?: string }
}
export type ConvSummary = {
  id: string
  title: string
  last: string
  about?: string // the project's key
  project?: string // its name
  savedAt?: string
  network?: string
  cli?: string
  model?: string
  turns: number
  lastAt: string
  sandbox: string
  tickets?: number
  draft?: string
}

// A project's knowledge (design 9): memory, bugs, feedback, the documentation, and skills.
export type MemoryEntry = {
  number: number
  kind: string
  title: string
  body: string
  appliesTo: string[]
  paths: string[]
  keywords: string[]
  sourceRun?: string
  sourceTicket?: string
  sourceFeedback?: number
  author: string
  status: 'active' | 'retired'
  retiredReason?: string
  changedBy?: string
  createdAt: string
}
export type Bug = {
  number: number
  title: string
  body: string
  severity: 'low' | 'medium' | 'high'
  status: 'open' | 'in_work' | 'fixed' | 'closed'
  foundBy: string
  foundIn?: string
  inTicket?: string
  fixTicket?: string
  regressionCheck?: string
  closedReason?: string
  closedNote?: string
  flakyCmd?: string
  repeats: number
  createdAt: string
}
export type FeedbackPiece = {
  number: number
  from: string
  fromRun?: string
  about: string
  kind: 'praise' | 'correction' | 'rule'
  ticket?: string
  record?: string
  body: string
  status: 'new' | 'applied' | 'dismissed'
  outcome?: string
  resolvedBy?: string
  createdAt: string
}
export type DocPage = {
  path: string
  title: string
  kind: string
  covers: string[]
  blob: string
  lastCommit: string
  lastChangedAt: string
  lastTicket?: string
  outdatedSince?: string
  outdatedBy?: string
  body?: string
  tickets?: string[]
}
export type DocHit = { path: string; title: string; kind: string; snippet: string; current: boolean }
export type LayerItem = { kind: string; name: string; path?: string; description: string; ticket?: string }
export type SkillsView = {
  base: LayerItem[]
  baseCounts: Record<string, number>
  project: LayerItem[]
  asked: { ticket: string; title: string; stage: string; requests: { missing: string; seenIn: string; wanted: string }[] }[]
}

// Hosting (design 19, host.png): a project's hosting as its screen reads it; values by name only.
export type HostingDeploy = {
  number: number
  commit: string
  image?: string
  why: string
  tickets: string[]
  state: string
  detail: string
  startedAt: string
  liveAt?: string
  canRollBack: boolean
  canRestoreData: boolean
}
export type HostingView = {
  state: string
  refused?: string
  address?: string
  certificate?: string
  access: string
  allowed: string[]
  ticket?: { key: string; stage: string }
  live?: {
    deploy: number
    commit: string
    image: string
    tag: string
    sandbox: string
    tickets: number
    liveSince: string
    restarts: number
    healthy: boolean
    downSince?: string
  }
  followMain: boolean
  internet: boolean
  dockerfile: string
  port: number
  health: string
  data: string
  size: string
  sizeText: string
  logDays: number
  keepImages: number
  compose: boolean
  values: string[]
  valuesLost: boolean
  deploys: HostingDeploy[]
  running: number
}
