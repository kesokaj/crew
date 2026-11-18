import { describe, expect, it } from 'vitest'
import { marks } from './textdiff'

describe('marks', () => {
  it('marks the words the rewrite added, and keeps the rest', () => {
    const p = marks('add a weekly summary email', 'add a weekly summary email, as one more ticket')
    expect(p.filter((x) => x.added).map((x) => x.text.trim())).toEqual(['email, as one more ticket'])
    expect(p.map((x) => x.text).join('')).toBe('add a weekly summary email, as one more ticket')
  })
  it('keeps every word of a rewrite that only reorders nothing', () => {
    expect(marks('same text', 'same text')).toEqual([{ text: 'same text', added: false }])
  })
  it('gives up on texts too long to compare', () => {
    const long = Array.from({ length: 50 }, (_, i) => `w${i}`).join(' ')
    expect(marks(long, long + ' more', 10)).toEqual([{ text: long + ' more', added: false }])
  })
})
