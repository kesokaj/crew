// The names the screens use for the board's words (design 7.4, 11.1).
export const stageName: Record<string, string> = {
  planning: 'Planning',
  backlog: 'Backlog',
  analysis: 'Analysis',
  in_progress: 'In progress',
  review: 'Review',
  test: 'Test',
  e2e: 'E2E',
  verification: 'Verification',
  done: 'Done',
  closed: 'Closed',
}

// A ticket's stuck text that is a plain wait, which needs nobody: for a lead slot, for room on the
// machine, for a turn on a group of models close to its limit, or for the project's setup to be fixed
// (7.9, 12.7, 13.5). Every other stuck text needs the operator.
const plainWaits = [
  /waits for a (lead|run) slot/,
  /waits for room on the machine/,
  /is close to its limit, and one run starts on it at a time/,
  /which fixes the project's setup/,
  /waits while Crew runs the project's setup on main/,
]

export const plainWait = (stuck: string) => plainWaits.some((w) => w.test(stuck))

export const roleName = (role: string) => role.charAt(0).toUpperCase() + role.slice(1).replaceAll('_', ' ')

// What an agent of a role is doing while its run works, as a card says it.
const doing: Record<string, string> = {
  curator: 'analysing',
  lead: 'analysing',
  builder: 'building',
  reviewer: 'reviewing',
  tester: 'testing',
  summary: 'writing the summary',
  e2e: 'running the end-to-end flows',
  verifier: 'verifying',
  coordinator: 'sequencing the backlog',
  researcher: 'investigating prior tries',
  planner: 'drafting the spec',
  dreamer: 'synthesising skills',
}

export function agentDoing(role: string, state: string): string {
  if (state === 'starting') return `${roleName(role)} is starting`
  if (state === 'idle') return `${roleName(role)} waits for its next turn`
  return `${roleName(role)} is ${doing[role] ?? 'working'}`
}

// The network modes (design 13.6), as the screens name them and say what each reaches.
export const modeName: Record<string, string> = { internet: 'Internet', model_only: 'Model only', lan: 'LAN' }

const modeReaches: Record<string, string> = {
  internet: 'Reaches the internet. This machine, the local network, and other sandboxes are blocked.',
  model_only: "Reaches the agent's model service and Crew, and nothing else. It resolves no name itself.",
  lan: 'Reaches the internet and the local network. You set this on purpose.',
}

// networkText says what a sandbox in a mode reaches. A sandbox from before the modes had the internet.
export function networkText(mode?: string): string {
  const m = mode || 'internet'
  return `${modeName[m] ?? m}. ${modeReaches[m] ?? ''}`.trim()
}

// cut is s at most n characters long, by its characters and not its UTF-16 units, ending in an
// ellipsis when it was longer.
export function cut(s: string, n: number): string {
  const chars = Array.from(s)
  return chars.length <= n ? s : chars.slice(0, n).join('').trimEnd() + '…'
}

// The purposes of a check run (board.Purpose*, design 13.9), as the task view names them.
export const purposeText: Record<string, string> = {
  base: 'On main, before the build',
  exit: 'On the branch, to leave the stage',
  test: 'In Test',
  merge: 'On top of main, before the merge',
  part: "A part's own check, on its branch",
  join: "A part's check, on its join",
  judge: 'A judge read the branch, for the judge: checks',
  e2e: 'The end-to-end tests, in E2E',
  stage: 'On the branch, for the stage',
  stage_judge: 'A judge read the branch, for the stage',
  outside: 'On main, after the pull request was merged on GitHub',
}

// isJudgePurpose says whether a check run's results are a judge's answers (met / not met) rather than
// a command's exit code.
export const isJudgePurpose = (purpose: string): boolean => purpose === 'judge' || purpose === 'stage_judge'

// plainText removes the file:// markdown links an agent CLI's own system prompt writes around a file or
// symbol name ([README.md](file:///home/agent/work/README.md) -> README.md).
export const plainText = (s: string): string => s.replace(/\[([^\]]+)\]\(file:\/\/[^)\s]*\)/g, '$1')
