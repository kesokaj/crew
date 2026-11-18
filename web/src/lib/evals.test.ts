import { describe, expect, it } from 'vitest'
import { fitNote, suiteFor, suiteNote, withChange } from './evals'

describe('suiteNote', () => {
  it('says why a suite measured nothing before its decision', () => {
    const why = "the reviewer's run ended for Crew's own reason: quota"
    expect(suiteNote({ unmeasured: why, result: { decision: { why: '0 cases, and at least 10 are needed to tell' } } })).toBe(
      `${why}; 0 cases, and at least 10 are needed to tell`,
    )
    expect(suiteNote({ unmeasured: why, result: null })).toBe(why)
  })
  it('says a stop once', () => {
    expect(suiteNote({ unmeasured: 'the project is paused', stoppedWhy: 'the project is paused' })).toBe('the project is paused')
    expect(suiteNote({ unmeasured: "the builder's run ended", stoppedWhy: 'the project is paused' })).toBe("the builder's run ended; stopped: the project is paused")
  })
  it('gives the decision of a suite that measured', () => {
    expect(suiteNote({ result: { decision: { why: 'not worse' } } })).toBe('not worse')
    expect(suiteNote({ stoppedWhy: 'its override is withdrawn' })).toBe('stopped: its override is withdrawn')
    expect(suiteNote({})).toBe('')
  })
})

describe('fitNote', () => {
  it('says why a waiting case waits', () => {
    expect(fitNote({ status: 'waiting', fit: { state: 'pending' } })).toBe('Waits for its fit check')
    expect(fitNote({ status: 'waiting', fit: { state: 'pending', why: "the judge's run ended without an answer: quota" } })).toBe(
      "Waits for its fit check: the judge's run ended without an answer: quota",
    )
    expect(fitNote({ status: 'waiting', fit: { state: 'running' } })).toMatch(/^Its fit check runs/)
    const why = "not checked: the project's builder's model cannot be read"
    expect(fitNote({ status: 'waiting', fit: { state: 'unchecked', why } })).toBe(why)
    expect(fitNote({ status: 'waiting', fit: { state: 'checked', list: ['the type Counts'] } })).toMatch(/^Its held-out files need what its text does not state/)
    // A judge that read only part of the files leaves the case to the operator, and says so.
    expect(fitNote({ status: 'waiting', fit: { state: 'checked', list: [], why: 'the judge read only part of the held-out files' } })).toBe(
      'the judge read only part of the held-out files',
    )
  })
  it('says nothing of a case that is fit, or retired', () => {
    expect(fitNote({ status: 'active', fit: { state: 'checked', list: [] } })).toBe('')
    expect(fitNote({ status: 'retired', fit: { state: 'checked', list: ['x'] } })).toBe('')
    // A case from before the fit check has none.
    expect(fitNote({ status: 'active', fit: {} })).toBe('')
  })
  it('says an accepted case kept its list', () => {
    expect(fitNote({ status: 'active', fit: { state: 'checked', list: ['Lines'] } })).toBe('Accepted by you, with what its fit check listed')
  })
})

describe('suiteFor', () => {
  it("names a release's gate, an override, and an improvement", () => {
    expect(suiteFor({ purpose: 'release', gate: '0.3.0' })).toBe("a release's gate 0.3.0")
    expect(suiteFor({ purpose: 'override', override: 3 })).toBe('an override o_3')
    expect(suiteFor({ purpose: 'improvement', improvement: 12 })).toBe('an improvement i_0012')
    expect(suiteFor({ purpose: 'on_demand' })).toBe('on demand')
  })
})

describe('withChange', () => {
  it("shows a release suite's own side where a change shows", () => {
    expect(withChange({ result: { release: { cases: 2, passed: 0.5 } } })).toEqual({ cases: 2, passed: 0.5 })
    expect(withChange({ result: { change: { cases: 1, passed: 1 } } })).toEqual({ cases: 1, passed: 1 })
    expect(withChange({ result: null })).toBeUndefined()
  })
})
