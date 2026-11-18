import { describe, expect, it } from 'vitest'
import { markedUnder } from './marked'

describe('markedUnder', () => {
  it('names the label the text was marked under', () => {
    expect(markedUnder({ part: 'why', label: 'Why, what, goal' })).toBe('marked under Why, what, goal')
  })
  it('names the part when the label is empty', () => {
    expect(markedUnder({ part: 'checks', label: '' })).toBe('marked under checks')
    expect(markedUnder({ part: 'log', label: '  ' })).toBe('marked under log')
  })
  it('says marked text with no source', () => {
    expect(markedUnder()).toBe('marked text')
  })
})
