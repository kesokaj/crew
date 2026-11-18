import { Fragment } from 'react'
import type { Said } from '../lib/said'

// SaidView shows a line of the app (lib/said.ts): its own words as they are, and each free part in an
// element marked data-free-text, which the gate's no-dollar check does not read as the app's.
export function SaidView({ said }: { said: Said }) {
  return (
    <>
      {said.map((p, i) =>
        typeof p === 'string' ? (
          <Fragment key={i}>{p}</Fragment>
        ) : (
          <span key={i} data-free-text>
            {p.free}
          </span>
        ),
      )}
    </>
  )
}
