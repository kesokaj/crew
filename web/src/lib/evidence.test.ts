import { describe, expect, it } from 'vitest'
import { seenIn } from './evidence'

// The improvements' "From signals in" (improvements.png): counts get their nouns, and the improver's
// own sentences are shown as it wrote them (M15 on dave added " projects" to the end of a sentence).
describe('seenIn', () => {
  it('names the nouns of bare counts', () => {
    expect(seenIn({ projects: '1 of 4', tasks: '5 of 61' })).toBe('1 of 4 projects, 5 of 61 tickets')
    expect(seenIn({ projects: '3' })).toBe('3 projects')
  })
  it('shows a sentence as it is', () => {
    const projects = '1 of 1 project with finished tickets (3 projects in Crew, 2 have none done)'
    expect(seenIn({ projects, tasks: '1 of 5 done tickets' })).toBe(projects + ', 1 of 5 done tickets')
  })
  it('says nothing was given', () => {
    expect(seenIn({})).toBe('—')
    expect(seenIn({ projects: ' ' })).toBe('—')
  })
})
