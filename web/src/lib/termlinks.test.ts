import { describe, expect, it } from 'vitest'
import { findSpans, type Row } from './termlinks'

const rows = (...t: (string | [string, true])[]): Row[] =>
  t.map((r) => (typeof r === 'string' ? { text: r, wrapped: false } : { text: r[0], wrapped: true }))

describe('findSpans', () => {
  it('finds a link the terminal wrapped, from its first cell to its last', () => {
    const s = findSpans(rows('    https://accounts.google.com/o/oau', ['th2/auth?state=x', true], '', 'Enter code: '))
    expect(s).toEqual([{ url: 'https://accounts.google.com/o/oauth2/auth?state=x', start: { x: 4, y: 0 }, end: { x: 15, y: 1 } }])
  })

  it('finds a link a CLI broke over rows itself, as agy does', () => {
    const s = findSpans(
      rows('Your browser should open automatically. If not:', '', 'https://accounts.google.com/o/oauth2/auth?a=1&client_id=10710   ', 'ssin2h21lcre&scope=x%3Ay', 'oid&state=W23F........  ', '', 'Copy and paste the URL'),
    )
    expect(s.map((x) => x.url)).toEqual(['https://accounts.google.com/o/oauth2/auth?a=1&client_id=10710ssin2h21lcre&scope=x%3Ayoid&state=W23F'])
    expect(s[0]?.start).toEqual({ x: 0, y: 2 })
    expect(s[0]?.end).toEqual({ x: 13, y: 4 })
  })

  it('does not take the next sentence into a link, and keeps https only', () => {
    const s = findSpans(rows('see https://a.example/1.', 'Then more text here', 'and http://plain.example/x'))
    expect(s.map((x) => x.url)).toEqual(['https://a.example/1'])
  })

  it('does not run on into a row after a link that ended with a space', () => {
    const s = findSpans(rows('open https://a.example/x now', 'ABCDEF'))
    expect(s.map((x) => x.url)).toEqual(['https://a.example/x'])
  })
})
