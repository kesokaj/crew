// Project warm Kata microVM + agy acp server lifecycle, persistent home snapshots, and /sandboxes types (design 1.37, 1.38, 13.3, 15.4, 20.1).

export type ActiveTicketsSummary = {
  active: number
  max: number
  set: number
  projectActive?: number
  projectMax?: number
}

export type ProjectSnapshot = {
  id: string
  projectId: string
  label: string
  archivePath?: string
  sha256: string
  sizeBytes: number
  imageTag: string
  createdAt: string
}

export type ProjectSandboxStatus = {
  projectId: string
  sandboxId: string
  state: 'stopped' | 'running' | 'paused'
  imageTag: string
  snapshotId: string
  activeSessions: number
  cpus?: number
  memoryGB?: number
  diskGB?: number
  suspendAfterMinutes?: number
  stopAfterMinutes?: number
  maxSnapshots?: number
  lastActiveAt?: string
  updatedAt?: string
  snapshots: ProjectSnapshot[]
}

export type SandboxActiveRun = {
  run: string
  role: string
  cli: string
  model?: string
  effort?: string
  subtask: string
  ticket?: string
  title: string
  sandbox?: string
  size?: string
  state: string
  startedAt?: string
  endedAt?: string
  tokens: number
}

export type MachineSandboxSummary = {
  cores: number
  totalMemMB: number
  roomCPUs: number
  roomMemMB: number
  usedCPUs: number
  usedMemMB: number
  activeTickets: number
  maxActiveTickets: number
  setMaxActiveTickets: number
  runningRuns?: number
  maxConcurrentRuns?: number
  setMaxConcurrentRuns?: number
}

export type ProjectSandboxOverview = {
  project: string
  projectId: string
  projectKey: string
  projectState: string
  sandboxId: string
  container: string
  state: 'stopped' | 'running' | 'paused'
  activeSessions: number
  activeTickets: number
  maxActiveTickets: number
  cpus: number
  usedCPUs: number
  cpuPercent: number
  memoryGB: number
  memUsedMB: number
  memLimitMB: number
  diskGB: number
  diskUsedMB: number
  snapshotsCount: number
  snapshotsBytes: number
  maxSnapshots: number
  suspendAfterMinutes: number
  stopAfterMinutes: number
  imageTag: string
  lastActiveAt?: string
  runs: SandboxActiveRun[]
}

export type EphemeralSandboxOverview = {
  id: string
  container: string
  kind: string
  project?: string
  detail?: string
  cpus: number
  cpuPercent: number
  memUsedMB: number
  memLimitMB: number
  startedAt: string
}

export type SandboxRoomWait = {
  key: string
  size: string
  priority: number
  project?: string
  ticket?: string
  what: string
  since: string
}

export type AllSandboxesView = {
  machine: MachineSandboxSummary
  projectSandboxes: ProjectSandboxOverview[]
  ephemeralSandboxes: EphemeralSandboxOverview[]
  waiting: SandboxRoomWait[]
}
