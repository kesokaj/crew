import { useEffect, useState } from 'react'
import { THEME_GROUPS, THEMES } from '../lib/themes'
import { button } from './ui'

export function ThemeGalleryModal({
  open,
  theme,
  dark,
  onSelectTheme,
  onToggleDark,
  onClose,
}: {
  open: boolean
  theme: string
  dark: boolean
  onSelectTheme: (id: string) => void
  onToggleDark: () => void
  onClose: () => void
}) {
  const [filter, setFilter] = useState('')

  useEffect(() => {
    if (!open) return
    const onKey = (e: KeyboardEvent) => {
      if (e.key === 'Escape') onClose()
    }
    window.addEventListener('keydown', onKey)
    return () => window.removeEventListener('keydown', onKey)
  }, [open, onClose])

  if (!open) return null

  const q = filter.trim().toLowerCase()
  const filtered = THEMES.filter(
    (t) => !q || t.title.toLowerCase().includes(q) || t.id.toLowerCase().includes(q) || t.group.toLowerCase().includes(q),
  )

  return (
    <div className="fixed inset-0 z-50 flex items-center justify-center p-4" data-theme-gallery>
      <div className="fixed inset-0 bg-black/50" aria-hidden="true" onClick={onClose} />
      <section
        role="dialog"
        aria-modal="true"
        aria-label="Themes (40) Swatch Gallery"
        className="relative z-10 flex max-h-[85vh] w-full max-w-4xl flex-col overflow-hidden rounded-lg border border-border bg-bg-raised text-text shadow-2xl"
      >
        <div className="flex flex-wrap items-center gap-3 border-b border-border px-4 py-3">
          <div>
            <h2 className="text-base font-semibold">Themes ({THEMES.length})</h2>
            <p className="text-xs text-text-dim">Switch anytime · light &amp; dark saved per operator</p>
          </div>
          <input
            type="search"
            aria-label="Filter themes"
            value={filter}
            onChange={(e) => setFilter(e.target.value)}
            placeholder="Search themes…"
            className="ml-auto min-w-44 rounded-md border border-border bg-bg px-2.5 py-1 text-sm text-text"
          />
          <button type="button" className={button} onClick={onToggleDark} data-gallery-toggle-dark>
            {dark ? 'Mode: Dark (d)' : 'Mode: Light (d)'}
          </button>
          <button type="button" className={button} onClick={onClose}>
            Close
          </button>
        </div>
        <div className="overflow-y-auto p-4 space-y-5">
          {THEME_GROUPS.map((group) => {
            const list = filtered.filter((t) => t.group === group)
            if (list.length === 0) return null
            return (
              <div key={group}>
                <h3 className="mb-2 text-xs font-semibold tracking-wide text-text-dim uppercase">
                  {group} ({list.length})
                </h3>
                <div className="grid gap-2 sm:grid-cols-2 lg:grid-cols-3">
                  {list.map((t) => {
                    const active = t.id === theme
                    return (
                      <button
                        key={t.id}
                        type="button"
                        data-theme-choice={t.id}
                        aria-pressed={active}
                        onClick={() => onSelectTheme(t.id)}
                        className={
                          'flex items-center justify-between gap-2 rounded-md border px-3 py-2 text-left text-sm transition-colors ' +
                          (active
                            ? 'border-accent bg-accent-soft font-semibold text-accent'
                            : 'border-border bg-bg hover:bg-bg-sunk')
                        }
                      >
                        <div className="min-w-0">
                          <p className="truncate">{t.title}</p>
                          <p className="truncate font-mono text-[11px] text-text-dim">{t.id}</p>
                        </div>
                        <svg width="48" height="14" viewBox="0 0 48 14" className="shrink-0" aria-hidden="true">
                          {t.swatches.map((hex, i) => (
                            <rect key={hex + String(i)} x={i * 12} y="0" width="10" height="14" rx="2" fill={hex} />
                          ))}
                        </svg>
                      </button>
                    )
                  })}
                </div>
              </div>
            )
          })}
          {filtered.length === 0 && <p className="text-sm text-text-dim">No matching themes.</p>}
        </div>
      </section>
    </div>
  )
}
