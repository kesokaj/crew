import type { ReactNode } from 'react'

export type Bar = { label: string; value: number }

// A stat of the screens' top row: what it counts, the number, and what the number is of.
export function Stat({ title, value, note, data }: { title: string; value: ReactNode; note?: ReactNode; data?: string }) {
  return (
    <section className="rounded-lg border border-border bg-bg-raised p-4" data-stat={data}>
      <h2 className="text-xs tracking-wide text-text-dim uppercase">{title}</h2>
      <p className="mt-2 text-3xl font-bold">{value}</p>
      {note && <p className="mt-1 text-sm text-text-dim">{note}</p>}
    </section>
  )
}

// Horizontal bars: a label, a bar as long as its share of the largest, and the value as words. free
// says the labels are the operator's or agents' words, such as a part's name, and not the app's own.
export function HBars({ bars, show = String, wide, free }: { bars: Bar[]; show?: (v: number) => string; wide?: boolean; free?: boolean }) {
  const max = Math.max(0, ...bars.map((b) => b.value))
  return (
    <div className={'grid items-center gap-x-3 gap-y-1.5 text-sm ' + (wide ? 'grid-cols-[12rem_1fr_3.5rem]' : 'grid-cols-[7rem_1fr_4rem]')}>
      {bars.map((b) => (
        <div key={b.label} className="contents" data-bar={b.label}>
          <span className="truncate" data-free-text={free || undefined}>
            {b.label}
          </span>
          <span className="h-3.5">
            <span
              className="block h-full rounded-sm bg-accent"
              style={{ width: max > 0 ? `${Math.max(b.value > 0 ? 2 : 0.5, (b.value / max) * 100)}%` : '0.5%', backgroundColor: 'var(--color-accent)' }}
            />
          </span>
          <span className="text-right tabular-nums">{show(b.value)}</span>
        </div>
      ))}
    </div>
  )
}

// Columns over time, such as tickets done per day, with the label under each.
export function Columns({ bars }: { bars: Bar[] }) {
  const max = Math.max(1, ...bars.map((b) => b.value))
  return (
    <div>
      <div className="flex h-28 items-end gap-1 border-b border-border">
        {bars.map((b) => (
          <span
            key={b.label}
            title={`${b.label}: ${b.value}`}
            className="flex-1 rounded-t-sm bg-accent"
            style={{ height: `${b.value > 0 ? Math.max(4, (b.value / max) * 100) : 1}%`, backgroundColor: 'var(--color-accent)' }}
          />
        ))}
      </div>
      <div className="mt-1 flex gap-1 text-[10px] text-text-dim">
        {bars.map((b, i) => (
          <span key={b.label} className="flex-1 text-center font-mono">
            {i % 2 === 0 ? b.label.slice(3) : ''}
          </span>
        ))}
      </div>
    </div>
  )
}

export const pct = (share: number) => `${Math.round(share * 100)}%`

// shareOf is a share of a count, and "—" of none, which 0% would show as none of them passed (0.2.3 on dave).
export const shareOf = (s: { of: number; share: number }) => (s.of ? pct(s.share) : '—')
