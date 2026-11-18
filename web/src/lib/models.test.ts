import { describe, expect, it } from 'vitest'
import { familyName, reasonFor } from './models'

// Why Crew would choose an entry (12.3): since 0.53 results alone decide. Better is the builder's
// passed share, and for an agent that checks fewer defects escaped; "for less" is only on old choices.
describe('reasonFor', () => {
  it('words better by the role', () => {
    expect(reasonFor('builder', 'better')).toBe('passed more often')
    for (const role of ['reviewer', 'tester', 'e2e', 'verifier']) {
      expect(reasonFor(role, 'better')).toBe('fewer defects escaped, as many fixes asked')
    }
    expect(reasonFor('reviewer', 'cheaper')).toBe('as well, for less (before 0.53)')
    expect(reasonFor('builder', 'newer')).toBe('newer in its bracket')
    expect(reasonFor('builder', 'odd')).toBe('odd')
  })
})

describe('familyName', () => {
  it('names all three known model families on the Models screen', () => {
    expect(familyName.gemini).toBe('Gemini')
    expect(familyName.claude).toBe('Claude')
    expect(familyName.gpt).toBe('GPT')
  })
})
