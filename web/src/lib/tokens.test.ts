import { describe, expect, it } from 'vitest'
import { tokenCount, tokens } from './tokens'

describe('tokenCount', () => {
  it('says a count short, with one decimal below a hundred of its unit', () => {
    expect(tokenCount(0)).toBe('0')
    expect(tokenCount(950)).toBe('950')
    expect(tokenCount(12345)).toBe('12.3k')
    expect(tokenCount(12000)).toBe('12k')
    expect(tokenCount(123456)).toBe('123k')
    expect(tokenCount(4_500_000)).toBe('4.5M')
    expect(tokenCount(2_000_000_000)).toBe('2B')
  })
  it('says a count that rounds up to a thousand in the next unit', () => {
    expect(tokenCount(999.6)).toBe('1k')
    expect(tokenCount(999_960)).toBe('1M')
    expect(tokenCount(99_960)).toBe('100k')
  })
  it('rounds an average to a whole token', () => {
    expect(tokenCount(41.6)).toBe('42')
  })
  it('says a dash for no count', () => {
    expect(tokenCount(undefined)).toBe('—')
    expect(tokenCount(null)).toBe('—')
    expect(tokenCount(Number.NaN)).toBe('—')
  })
})

describe('tokens', () => {
  it('names the unit, and never a currency', () => {
    expect(tokens(12345)).toBe('12.3k tokens')
    expect(tokens(1)).toBe('1 token')
    expect(tokens(0)).toBe('0 tokens')
    expect(tokens(null)).toBe('—')
    expect(tokens(3.75)).not.toContain('$')
  })
})
