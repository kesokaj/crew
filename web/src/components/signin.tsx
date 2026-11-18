import { useEffect, useState } from 'react'
import { goSignIn, onAuthProblem, takeHandover, type AuthProblem } from '../lib/session'
import { button, mainButton } from './ui'

// SignInDialog is the one place the page asks for a sign-in (design 20.3): a fresh one a change
// asked for, which can wait; a sign-in that is over, or a device signed out, which cannot, as every
// request is refused. Sign in again goes to the ingress and back to this page. As the page loads it
// hands a fresh sign-in it came back with to its device.
export function SignInDialog() {
  const [p, setP] = useState<AuthProblem | null>(null)
  const [going, setGoing] = useState(false)
  useEffect(() => onAuthProblem((x) => setP((cur) => (cur && cur.reason !== 'FRESH_SIGN_IN' ? cur : x))), [])
  useEffect(() => {
    void takeHandover()
  }, [])
  if (!p) return null
  const text: Record<AuthProblem['reason'], [string, string]> = {
    FRESH_SIGN_IN: [
      'A fresh sign-in',
      `${p.message.charAt(0).toUpperCase() + p.message.slice(1)}. Google asks which account you sign in with, and you come back to this page to do it again.`,
    ],
    SESSION_OVER: ['Your sign-in is over', `${p.message.charAt(0).toUpperCase() + p.message.slice(1)}.`],
    SIGNED_OUT: ['This device was signed out', 'It was signed out on Machine settings. Signing in again makes it a new device.'],
  }
  const [title, body] = text[p.reason]
  return (
    <div className="fixed inset-0 z-50 flex items-center justify-center bg-black/40 p-4" role="dialog" aria-modal="true" aria-label={title} data-signin={p.reason}>
      <div className="w-full max-w-md rounded-lg border border-border bg-bg-raised p-5 shadow-lg">
        <h2 className="mb-2 text-lg font-semibold">{title}</h2>
        <p className="mb-4 text-sm text-text-dim">{body}</p>
        <div className="flex gap-2">
          <button
            className={mainButton}
            disabled={going}
            onClick={() => {
              setGoing(true)
              void goSignIn(p)
            }}
          >
            Sign in again
          </button>
          {p.reason === 'FRESH_SIGN_IN' && (
            <button className={button} onClick={() => setP(null)}>
              Not now
            </button>
          )}
        </div>
      </div>
    </div>
  )
}
