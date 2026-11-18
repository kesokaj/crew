import { describe, expect, it } from 'vitest'
import { lineText, problemFor, problemsAt, valueAt, withValue } from './settings'

describe('settings', () => {
  it('reads and replaces a value by its path without changing the document', () => {
    const d = { disk: { clean_at: 85, stop_at: 95 }, max_concurrent_runs: 8 }
    expect(valueAt(d, 'disk.clean_at')).toBe(85)
    expect(valueAt(d, 'disk.nothing.deeper')).toBeUndefined()
    const e = withValue(d, 'disk.clean_at', 70)
    expect(e).toEqual({ disk: { clean_at: 70, stop_at: 95 }, max_concurrent_runs: 8 })
    expect(d.disk.clean_at).toBe(85)
    expect(e.max_concurrent_runs).toBe(d.max_concurrent_runs)
    expect(withValue({}, 'a.b', 1)).toEqual({ a: { b: 1 } })
  })

  it("puts each of a refusal's problems under its field, and a group's under its keys", () => {
    const p = problemsAt('disk: clean_at must be above 0 and below stop_at, and stop_at at most 100\nmax_concurrent_runs: must be from 1 to 64\nsomething else')
    expect(problemFor(p, 'disk.clean_at')).toContain('below stop_at')
    expect(problemFor(p, 'disk.stop_at')).toContain('below stop_at')
    expect(problemFor(p, 'max_concurrent_runs')).toBe('must be from 1 to 64')
    expect(problemFor(p, 'diskette')).toBe('')
    expect(p['']).toBe('something else')
    expect(problemFor(problemsAt('sandbox.sources[2]: must be a word'), 'sandbox.sources')).toBe('must be a word')
  })

  it('says a line of a history with the default where the key was not set', () => {
    expect(lineText({ seq: 1, at: '', who: 'x', before: null, after: 70, beforeSet: false, afterSet: true })).toBe('the default → 70')
    expect(lineText({ seq: 2, at: '', who: 'x', before: ['a', 'b'], after: null, beforeSet: true, afterSet: true })).toBe('a, b → none')
  })
})
