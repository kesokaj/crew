import { describe, expect, it } from 'vitest'
import { listed, omittedOf } from './diff'
import type { FileDiff } from '../lib/api'

const file = (path: string): FileDiff => ({ path, added: 1, removed: 0, binary: false, patch: '', cut: false })

describe('a long diff', () => {
  it('lists the files with a path, and says how many more there are', () => {
    const files = [file('a.go'), file('b.go'), { ...file(''), omitted: 7 }]
    expect(listed(files).map((f) => f.path)).toEqual(['a.go', 'b.go'])
    expect(omittedOf(files)).toBe(7)
  })
  it('says nothing more of a diff that lists every file', () => {
    expect(omittedOf([file('a.go')])).toBe(0)
    expect(listed([])).toEqual([])
  })
})
