import type { ReactNode } from 'react'

// A card of the screens, with its title in small capitals, as in the simulation (docs/plan/screens/).
export function Card({ title, children, right }: { title: string; children: ReactNode; right?: ReactNode }) {
  return (
    <section className="rounded-lg border border-border bg-bg-raised p-4" aria-label={title}>
      <div className="mb-3 flex items-center gap-2">
        <h2 className="text-xs tracking-wide text-text-dim uppercase">{title}</h2>
        {right && <div className="ml-auto text-xs">{right}</div>}
      </div>
      {children}
    </section>
  )
}

export function Check({ on }: { on: boolean }) {
  return <span className={'w-4 shrink-0 ' + (on ? 'text-ok' : 'text-text-dim')}>{on ? '✓' : '·'}</span>
}

// Rows of a label and what it says. narrow is for a card a third of the page wide.
export function Rows({ rows, narrow }: { rows: [string, ReactNode][]; narrow?: boolean }) {
  return (
    <dl className={'grid gap-x-3 gap-y-2 text-sm ' + (narrow ? 'grid-cols-[6.5rem_1fr]' : 'grid-cols-[10rem_1fr]')}>
      {rows.map(([k, v]) => (
        <div key={k} className="contents">
          <dt className="text-text-dim">{k}</dt>
          <dd className="min-w-0 break-words">{v}</dd>
        </div>
      ))}
    </dl>
  )
}

// when says a time in the operator's own clock and calendar.
export function when(iso?: string): string {
  if (!iso) return ''
  const d = new Date(iso)
  const today = new Date()
  const time = d.toLocaleTimeString([], { hour: '2-digit', minute: '2-digit', hourCycle: 'h23' })
  return d.toDateString() === today.toDateString() ? time : `${d.toLocaleDateString()} ${time}`
}

// A small count: amber when it needs you, blue when agents are at work (the simulation's menu).
export function Badge({ tone, children }: { tone: 'amber' | 'blue' | 'grey'; children: ReactNode }) {
  const c =
    tone === 'amber' ? 'bg-amber text-black' : tone === 'blue' ? 'bg-accent-soft text-accent' : 'bg-bg-sunk text-text-dim'
  return <span className={'min-w-5 rounded-full px-1.5 text-center text-xs leading-5 font-semibold ' + c}>{children}</span>
}

// The head of a screen: where it is, its title, what it is for, and its buttons.
export function PageHead({ crumbs, title, children, actions }: { crumbs?: ReactNode; title: ReactNode; children?: ReactNode; actions?: ReactNode }) {
  return (
    <div className="mb-5">
      {crumbs && <p className="mb-1 font-mono text-xs text-text-dim">{crumbs}</p>}
      <div className="flex flex-wrap items-start gap-3">
        <h1 className="text-3xl font-bold tracking-tight">{title}</h1>
        {actions && <div className="ml-auto flex flex-wrap gap-2">{actions}</div>}
      </div>
      {children && <div className="mt-1 max-w-3xl text-text-dim">{children}</div>}
    </div>
  )
}

export function Failed({ error }: { error: string }) {
  if (!error) return null
  return (
    <p className="my-3 rounded-md border border-bad p-3 text-sm" data-failed>
      {error}
    </p>
  )
}

// A label in a rounded box, such as a ticket's kind or a CLI with its model.
export function Tag({ children, mono, tone }: { children: ReactNode; mono?: boolean; tone?: 'blue' | 'amber' | 'ok' | 'bad' }) {
  const c =
    tone === 'blue'
      ? 'border-accent bg-accent-soft text-accent'
      : tone === 'amber'
        ? 'border-amber bg-amber-soft text-amber-text'
        : tone === 'ok'
          ? 'border-ok text-ok'
          : tone === 'bad'
            ? 'border-bad text-bad'
            : 'border-border text-text-dim'
  return <span className={'inline-block rounded-full border px-2 text-xs leading-5 ' + c + (mono ? ' font-mono' : '')}>{children}</span>
}

export const button =
  'min-h-8 rounded-md border border-border bg-bg-raised px-3 py-1.5 text-sm transition-colors hover:bg-bg-sunk focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-accent disabled:opacity-50'
export const mainButton =
  'min-h-8 rounded-md border border-amber bg-amber px-3 py-1.5 text-sm font-semibold text-black transition-all hover:brightness-95 focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-accent disabled:opacity-50'

// ago says how long ago a time was, in the words of the screens: "41 min", "21 h 50 min".
export function ago(iso?: string, now = Date.now()): string {
  if (!iso) return ''
  const at = new Date(iso).getTime()
  // Go's zero time, or what cannot be read: no time is known.
  if (!(at > 0)) return ''
  const mins = Math.max(0, Math.floor((now - at) / 60000))
  if (mins < 1) return 'now'
  if (mins < 60) return `${mins} min`
  const h = Math.floor(mins / 60)
  if (h < 48) return mins % 60 ? `${h} h ${mins % 60} min` : `${h} h`
  return `${Math.floor(h / 24)} days`
}
