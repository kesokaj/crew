import { describe, expect, it } from 'vitest'
import { saidText } from './said'
import { copied, lastChange, type Ended, type Copyable } from './updates'

const says = (q: Ended) => saidText(lastChange(q))
const copiedText = (r: Copyable, publish: boolean) => saidText(copied(r, publish))

describe('lastChange', () => {
  it('says an install and a roll back switched, or why not', () => {
    for (const action of ['install', 'rollback']) {
      expect(says({ action, state: 'done', version: '0.2.15', from: '0.2.13' })).toBe(
        'Checked beside 0.2.13, switched, and healthy for ten minutes.',
      )
      expect(says({ action, state: 'switched_back', version: '0.2.15', from: '0.2.13', error: 'not healthy' })).toBe(
        'Switched back: not healthy',
      )
      expect(says({ action, state: 'failed', version: '0.2.15', from: '0.2.13', error: 'the snapshot' })).toBe(
        'Not installed: the snapshot. 0.2.13 kept running.',
      )
    }
  })

  // The gates of 0.2.14 and 0.2.15 on dave read "Not installed" and "switched, and healthy for ten
  // minutes": nothing had switched.
  it("words a gate's and a check's request as a check beside the running version, never an install", () => {
    expect(says({ action: 'gate', state: 'done', version: '0.2.15', from: '0.2.13' })).toBe(
      "The gate's steps passed beside 0.2.13. Nothing was installed.",
    )
    expect(says({ action: 'gate', state: 'failed', version: '0.2.14', from: '0.2.13', error: 'Check: the browser tests: EROFS' })).toBe(
      'The gate did not pass: Check: the browser tests: EROFS. Nothing was installed.',
    )
    expect(says({ action: 'gate', state: 'stopped', version: '0.2.14' })).toBe('The gate did not pass: stopped. Nothing was installed.')
    expect(says({ action: 'check', state: 'done', version: '0.2.9', from: '0.2.8' })).toBe('Checked beside 0.2.8 for the gate.')
    expect(says({ action: 'check', state: 'failed', version: '0.2.9', error: 'not healthy' })).toBe(
      'The check for the gate did not pass: not healthy. Nothing was installed.',
    )
    for (const state of ['done', 'failed', 'stopped', 'switched_back']) {
      for (const action of ['gate', 'check']) {
        expect(says({ action, state, version: '0.2.15', from: '0.2.13', error: 'x' })).not.toMatch(/switched|Not installed|kept running/)
      }
    }
  })

  // The gates of 0.2.17 and 0.2.18 failed at the check that no screen shows a dollar, and their error
  // quotes it: what the updater or a command said is marked, the app's own words are not.
  it("gives the updater's error as a free part, and its own words as its own", () => {
    const error = 'Check: the browser tests: no screen shows a dollar, an estimate, or a budget'
    expect(lastChange({ action: 'gate', state: 'failed', version: '0.2.17', error })).toEqual([
      'The gate did not pass: ',
      { free: error },
      '. Nothing was installed.',
    ])
    expect(lastChange({ action: 'install', state: 'switched_back', version: '0.2.17', error: 'not healthy' })).toEqual(['Switched back: ', { free: 'not healthy' }])
    expect(lastChange({ action: 'check', state: 'failed', version: '0.2.17', error: 'x' })).toContainEqual({ free: 'x' })
    expect(lastChange({ action: 'install', state: 'failed', version: '0.2.17', error: 'x' })).toContainEqual({ free: 'x' })
    expect(lastChange({ action: 'gate', state: 'done', version: '0.2.17', from: '0.2.16' }).some((p) => typeof p !== 'string')).toBe(false)
  })

  it("cleans the updater's error of control characters", () => {
    expect(says({ action: 'gate', state: 'failed', version: '0.2.14', error: 'a\u001b[31mb\u0007c' })).toBe(
      'The gate did not pass: abc. Nothing was installed.',
    )
  })
})

describe('copied', () => {
  it("gives what the copy's failure said as a free part", () => {
    expect(copied({ version: '0.2.16', publishError: 'gh: 401', gate: { passed: true }, byHand: false }, true)).toContainEqual({ free: 'gh: 401' })
  })

  const gate = { passed: true }
  it('says a copied, a failed, and a waiting copy while copying is on', () => {
    expect(copiedText({ version: '0.2.16', published: { done: '2026-10-21' }, gate, byHand: false }, true)).toBe(
      'v0.2.16 · release on GitHub with notes · images in the registry',
    )
    expect(copiedText({ version: '0.2.16', publishError: 'the tag', gate, byHand: false }, true)).toBe(
      'not yet copied to GitHub: the tag. Crew tries again.',
    )
    expect(copiedText({ version: '0.2.16', gate, byHand: false }, true)).toBe('being copied to GitHub')
    expect(copiedText({ version: '0.2.16', byHand: true }, true)).toBe('built by hand: not copied to GitHub')
    expect(copiedText({ version: '0.2.16', byHand: false }, true)).toBe('no gate: not copied to GitHub')
  })

  // On dave, with updates.publish off, Versions said "being copied to GitHub" for every release with a
  // gate, and nothing was copied.
  it('never says a release is being copied while copying is off', () => {
    for (const r of [{ version: '0.2.16', gate, byHand: false }, { version: '0.2.16', publishError: 'the tag', gate, byHand: false }]) {
      expect(copiedText(r, false)).toBe('not copied to GitHub: copying is off (updates.publish)')
    }
    expect(copiedText({ version: '0.2.16', published: { done: '2026-10-21' }, gate, byHand: false }, false)).toBe(
      'v0.2.16 · release on GitHub with notes · images in the registry',
    )
  })
})
