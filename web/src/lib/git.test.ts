import { describe, expect, it } from 'vitest'
import { trailers } from './git'

describe('trailers', () => {
  it('are the last paragraph when every line of it reads "Key: value"', () => {
    expect(trailers('Fix the lock.\n\nTicket: SF-7\nChecks: passed on main')).toBe('Ticket: SF-7\nChecks: passed on main')
  })
  it('are nothing when the last paragraph is prose', () => {
    expect(trailers('Fix the lock.\n\nSee: the plan says so.\nAnd a second line that is prose.')).toBe('')
    expect(trailers('')).toBe('')
  })
})
