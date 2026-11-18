import { useEffect, useState, type ReactNode } from 'react'
import { ensureFresh } from '../lib/session'
import { button } from './ui'

// FreshGate shows a terminal only once the sign-in is fresh (20.3): its connection is refused
// otherwise, and a WebSocket cannot say why. Not fresh, the page's dialog offers the sign-in, and the
// gate offers to look again.
export function FreshGate({ what, children }: { what: string; children: ReactNode }) {
  const [state, setState] = useState<'checking' | 'fresh' | 'stale'>('checking')
  const check = () => {
    setState('checking')
    void ensureFresh(what).then((ok) => setState(ok ? 'fresh' : 'stale'))
  }
  useEffect(check, [what])
  if (state === 'fresh') return <>{children}</>
  if (state === 'checking') return <p className="text-sm text-text-dim">Checking the sign-in…</p>
  return (
    <div className="py-6 text-center text-sm" data-fresh-gate>
      <p className="mb-3 text-text-dim">{what} needs a fresh sign-in.</p>
      <button className={button} onClick={check}>
        Look again
      </button>
    </div>
  )
}
