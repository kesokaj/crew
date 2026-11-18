import { describe, expect, it } from 'vitest'
import { lineDiff } from './linediff'

describe('lineDiff', () => {
  it('keeps what is the same and marks what changed', () => {
    expect(lineDiff('a\nb\nc', 'a\nx\nc')).toEqual([
      { kind: ' ', text: 'a' },
      { kind: '-', text: 'b' },
      { kind: '+', text: 'x' },
      { kind: ' ', text: 'c' },
    ])
  })
  it('adds at the end and removes at the start', () => {
    expect(lineDiff('a\nb', 'b\nc')).toEqual([
      { kind: '-', text: 'a' },
      { kind: ' ', text: 'b' },
      { kind: '+', text: 'c' },
    ])
  })
  it('gives the same file as kept lines only', () => {
    expect(lineDiff('a\nb', 'a\nb').every((l) => l.kind === ' ')).toBe(true)
  })
})
