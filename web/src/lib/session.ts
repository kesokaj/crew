// The sign-in of this page (design 20.3): a sign-in that is over, a device signed out, and the fresh
// sign-in a change can ask for. The server says which in a 401's details, with where the sign-in
// starts; the page shows it in one dialog (components/signin.tsx) and goes there and back.
import { getJSON, send } from './api'

export type AuthReason = 'FRESH_SIGN_IN' | 'SESSION_OVER' | 'SIGNED_OUT'

export type AuthProblem = { reason: AuthReason; signIn: string; message: string; keys?: string[] }

export const authReasons: readonly string[] = ['FRESH_SIGN_IN', 'SESSION_OVER', 'SIGNED_OUT']

// What the page knows of its own sign-in: GET /api/sessions/this.
export type SessionView = { id?: number; authAt?: string; fresh: boolean; freshUntil?: string; freshMinutes: number; signIn: string }

const listeners = new Set<(p: AuthProblem) => void>()

// onAuthProblem calls cb with each refusal that asks for a sign-in, until the function it returns is called.
export function onAuthProblem(cb: (p: AuthProblem) => void): () => void {
  listeners.add(cb)
  return () => listeners.delete(cb)
}

export function reportAuthProblem(p: AuthProblem) {
  for (const cb of listeners) cb(p)
}

// authProblemOf reads a refusal's body: the problem it names, or null for any other answer.
export function authProblemOf(code: number, body: unknown): AuthProblem | null {
  if (code !== 401) return null
  const err = (body as { error?: { message?: string; details?: { reason?: string; signIn?: string; keys?: string[] }[] } } | null)?.error
  const d = err?.details?.[0]
  if (!d?.reason || !authReasons.includes(d.reason) || !d.signIn) return null
  return { reason: d.reason as AuthReason, signIn: d.signIn, message: err?.message ?? '', keys: d.keys }
}

// signInAddress is where the browser goes: the ingress's sign-in, which comes back to here, with the
// handover's nonce when there is one; a device signed out goes through the ingress's logout as given,
// which comes back to Crew's own address and makes a new device.
export function signInAddress(p: AuthProblem, here: string, nonce?: string): string {
  if (p.reason === 'SIGNED_OUT') return p.signIn
  const back = new URL(here)
  back.searchParams.delete('handover')
  if (nonce) back.searchParams.set('handover', nonce)
  const to = new URL(p.signIn)
  to.searchParams.set('redirect_url', back.toString())
  return to.toString()
}

// goSignIn sends the browser to the sign-in. Before a fresh one it asks for a handover, so the
// device keeps its row if the sign-in brings a new id (D13); without one the sign-in is a device of
// its own, which refuses nothing.
export async function goSignIn(p: AuthProblem) {
  let nonce: string | undefined
  if (p.reason === 'FRESH_SIGN_IN') {
    nonce = await send<{ nonce: string }>('POST', '/api/handovers').then(
      (h) => h?.nonce,
      () => undefined,
    )
  }
  window.location.assign(signInAddress(p, window.location.href, nonce))
}

// takeHandover hands the sign-in the page came back with to the device that asked for it, and takes
// the nonce out of the address.
export async function takeHandover() {
  const url = new URL(window.location.href)
  const nonce = url.searchParams.get('handover')
  if (!nonce) return
  url.searchParams.delete('handover')
  window.history.replaceState(window.history.state, '', url.toString())
  await send('POST', `/api/handovers/${encodeURIComponent(nonce)}`).catch(() => null)
}

// freshFor says how long the sign-in is still fresh, in whole minutes, or 0.
export function freshFor(v: SessionView | null, now = Date.now()): number {
  if (!v?.fresh || !v.freshUntil) return 0
  return Math.max(0, Math.floor((new Date(v.freshUntil).getTime() - now) / 60000))
}

// A device on Machine settings: GET /api/sessions.
export type Device = { id: number; email: string; firstAt: string; authAt: string; lastAt: string; userAgent: string; this: boolean }

// deviceName names a device by its browser's User-Agent, as a person knows it: the browser on the
// system, such as "Chrome on macOS"; the whole string is the row's title.
export function deviceName(ua: string): string {
  const browser = /Edg\//.test(ua)
    ? 'Edge'
    : /Firefox\//.test(ua)
      ? 'Firefox'
      : /Chrome\/|CriOS\//.test(ua)
        ? 'Chrome'
        : /Safari\//.test(ua)
          ? 'Safari'
          : ''
  const system = /iPhone|iPad/.test(ua)
    ? 'iOS'
    : /Android/.test(ua)
      ? 'Android'
      : /Mac OS X|Macintosh/.test(ua)
        ? 'macOS'
        : /Windows/.test(ua)
          ? 'Windows'
          : /Linux/.test(ua)
            ? 'Linux'
            : ''
  if (browser && system) return `${browser} on ${system}`
  return browser || system || 'A browser Crew does not name'
}

// ensureFresh says whether the sign-in is fresh now; when it is not, the page's dialog offers one
// for what. A WebSocket cannot read a refusal, so a terminal asks this before it connects.
export async function ensureFresh(what: string): Promise<boolean> {
  const v = await getJSON<SessionView>('/api/sessions/this').catch(() => null)
  if (!v) return false
  if (v.fresh) return true
  reportAuthProblem({ reason: 'FRESH_SIGN_IN', signIn: v.signIn, message: `${what} needs a fresh sign-in` })
  return false
}
