import { describe, expect, it } from 'vitest'
import { ago, button, mainButton } from './ui'

describe('ago', () => {
  const now = Date.parse('2026-10-15T12:00:00Z')
  it('says minutes, then hours and minutes, then days, as the screens do', () => {
    expect(ago('2026-10-15T11:59:40Z', now)).toBe('now')
    expect(ago('2026-10-15T11:19:00Z', now)).toBe('41 min')
    expect(ago('2026-10-14T14:10:00Z', now)).toBe('21 h 50 min')
    expect(ago('2026-10-15T09:00:00Z', now)).toBe('3 h')
    expect(ago('2026-10-12T12:00:00Z', now)).toBe('3 days')
  })
  it('says nothing of no time, and never a time in the future', () => {
    expect(ago(undefined, now)).toBe('')
    // Go's zero time, which a cause with no time of its own sends: no time, not 739000 days.
    expect(ago('0001-01-01T00:00:00Z', now)).toBe('')
    expect(ago('1970-01-01T00:00:00Z', now)).toBe('')
    expect(ago('2026-10-15T12:05:00Z', now)).toBe('now')
  })
})

describe('button and mainButton accessibility tokens', () => {
  it('includes min-h-8 target sizing and focus-visible outlines', () => {
    expect(button).toContain('min-h-8')
    expect(button).toContain('focus-visible:outline-2')
    expect(mainButton).toContain('min-h-8')
    expect(mainButton).toContain('focus-visible:outline-2')
  })
})
