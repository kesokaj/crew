// What the Evals tab says of a suite beside its numbers (9.6).

export type SuiteNote = {
  stoppedWhy?: string | null
  unmeasured?: string | null
  result?: { decision?: { why: string } | null } | null
}

// suiteNote is the Decision column: why a suite measured nothing comes first, because a decision on
// no case, such as "0 cases, and at least 10 are needed to tell", does not say it (review of 0.50);
// then the decision, else why the suite stopped.
export function suiteNote(s: SuiteNote): string {
  const parts: string[] = []
  if (s.unmeasured) parts.push(s.unmeasured)
  if (s.result?.decision) parts.push(s.result.decision.why)
  else if (s.stoppedWhy && s.stoppedWhy !== s.unmeasured) parts.push(`stopped: ${s.stoppedWhy}`)
  return parts.join('; ')
}

export type CaseFit = {
  status: string
  fit?: { state?: string; list?: string[]; why?: string } | null
}

// fitNote is what the Evals tab says of a case's fit check (9.6, since 0.51): why a waiting case
// waits, and for an active one only what its check listed before the operator accepted it.
export function fitNote(c: CaseFit): string {
  const fit = c.fit ?? {}
  const list = fit.list ?? []
  if (c.status === 'retired') return ''
  if (c.status === 'active') return list.length > 0 ? 'Accepted by you, with what its fit check listed' : ''
  switch (fit.state) {
    case 'running':
      return 'Its fit check runs: a judge reads its text and its held-out files'
    case 'checked':
      if (list.length > 0) return 'Its held-out files need what its text does not state. Change the text, or accept the case as it is'
      return fit.why ?? ''
    case 'unchecked':
    case 'error':
      return fit.why || 'Its fit check did not run'
    default:
      return fit.why ? `Waits for its fit check: ${fit.why}` : 'Waits for its fit check'
  }
}

export type SuiteFor = { purpose: string; gate?: string | null; override?: number | null; improvement?: number | null }

const purposeWords: Record<string, string> = { on_demand: 'on demand', override: 'an override', improvement: 'an improvement', release: "a release's gate" }

// suiteFor is the For column: what a suite ran for, with its override's, its improvement's, or its
// release's gate's name (16.2 step 5, since 0.52).
export function suiteFor(s: SuiteFor): string {
  let out = purposeWords[s.purpose] ?? s.purpose
  if (s.override) out += ` o_${s.override}`
  if (s.improvement) out += ` i_${String(s.improvement).padStart(4, '0')}`
  if (s.purpose === 'release' && s.gate) out += ` ${s.gate}`
  return out
}

export type SideNumbers = { cases: number; passed: number }

// withChange is the side the With the change column shows: an override's or an improvement's change, or
// a release suite's own side, which is compared with an earlier suite (since 0.52).
export function withChange(s: { result?: { change?: SideNumbers; release?: SideNumbers } | null }): SideNumbers | undefined {
  return s.result?.change ?? s.result?.release
}
