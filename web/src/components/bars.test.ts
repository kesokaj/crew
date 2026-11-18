import { describe, expect, it } from 'vitest'
import { shareOf } from './bars'

describe('shareOf', () => {
  it('is "—" of no ticket, not 0% (0.2.3 on dave), and the share of some', () => {
    expect(shareOf({ of: 0, share: 0 })).toBe('—')
    expect(shareOf({ of: 4, share: 0 })).toBe('0%')
    expect(shareOf({ of: 3, share: 2 / 3 })).toBe('67%')
  })
})
