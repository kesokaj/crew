import { describe, expect, it } from 'vitest'
import { authProblemOf, deviceName, freshFor, signInAddress, type AuthProblem } from './session'

const fresh: AuthProblem = { reason: 'FRESH_SIGN_IN', signIn: 'https://auth.crew.test/oauth2/google/?prompt=select_account', message: 'this needs a fresh sign-in' }

describe('authProblemOf', () => {
  it('reads the sign-in a 401 asks for, with the keys it names', () => {
    const body = { error: { code: 401, status: 'UNAUTHENTICATED', message: 'a change of sessions.days needs a fresh sign-in', details: [{ reason: 'FRESH_SIGN_IN', signIn: fresh.signIn, keys: ['sessions.days'] }] } }
    expect(authProblemOf(401, body)).toEqual({ reason: 'FRESH_SIGN_IN', signIn: fresh.signIn, message: 'a change of sessions.days needs a fresh sign-in', keys: ['sessions.days'] })
  })
  it('takes no other answer for one', () => {
    expect(authProblemOf(403, { error: { details: [{ reason: 'FRESH_SIGN_IN', signIn: 'x' }] } })).toBeNull()
    expect(authProblemOf(401, { error: { message: 'this door is for the ingress' } })).toBeNull()
    expect(authProblemOf(401, { error: { details: [{ reason: 'SOMETHING', signIn: 'x' }] } })).toBeNull()
    expect(authProblemOf(401, null)).toBeNull()
  })
})

describe('signInAddress', () => {
  it('comes back to the same page, with the handover, through the ingress', () => {
    const to = new URL(signInAddress(fresh, 'https://crew.test/settings?tab=a&handover=old', 'n0nce'))
    expect(to.origin + to.pathname).toBe('https://auth.crew.test/oauth2/google/')
    expect(to.searchParams.get('prompt')).toBe('select_account')
    expect(to.searchParams.get('redirect_url')).toBe('https://crew.test/settings?tab=a&handover=n0nce')
  })
  it('comes back without a handover when there is none', () => {
    const to = new URL(signInAddress({ ...fresh, reason: 'SESSION_OVER' }, 'https://crew.test/inbox'))
    expect(to.searchParams.get('redirect_url')).toBe('https://crew.test/inbox')
  })
  it('sends a device signed out through the logout as the server gave it', () => {
    const out = 'https://auth.crew.test/oauth2/google/logout?redirect_uri=https%3A%2F%2Fcrew.test%2F'
    expect(signInAddress({ reason: 'SIGNED_OUT', signIn: out, message: '' }, 'https://crew.test/settings', 'n')).toBe(out)
  })
})

describe('freshFor', () => {
  it('counts the whole minutes left of a fresh sign-in', () => {
    const now = Date.parse('2026-10-24T12:00:00Z')
    expect(freshFor({ fresh: true, freshUntil: '2026-10-24T12:09:30Z', freshMinutes: 15, signIn: '' }, now)).toBe(9)
    expect(freshFor({ fresh: true, freshUntil: '2026-10-24T11:59:00Z', freshMinutes: 15, signIn: '' }, now)).toBe(0)
    expect(freshFor({ fresh: false, freshMinutes: 15, signIn: '' }, now)).toBe(0)
    expect(freshFor(null, now)).toBe(0)
  })
})

describe('deviceName', () => {
  it('names the browser on its system', () => {
    expect(deviceName('Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/131.0.0.0 Safari/537.36')).toBe('Chrome on macOS')
    expect(deviceName('Mozilla/5.0 (iPhone; CPU iPhone OS 18_0 like Mac OS X) AppleWebKit/605.1.15 (KHTML, like Gecko) Version/18.0 Mobile/15E148 Safari/604.1')).toBe('Safari on iOS')
    expect(deviceName('Mozilla/5.0 (X11; Linux x86_64; rv:133.0) Gecko/20100101 Firefox/133.0')).toBe('Firefox on Linux')
    expect(deviceName('Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/131.0 Safari/537.36 Edg/131.0')).toBe('Edge on Windows')
    expect(deviceName('')).toBe('A browser Crew does not name')
  })
})
