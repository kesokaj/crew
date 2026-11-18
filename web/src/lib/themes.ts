import { useCallback, useEffect, useState } from 'react'

export type ThemeGroup = 'Product & AI-Native' | 'shadcn/ui Official Presets' | 'Dotfile & Editor Classics'

export type ThemeMeta = {
  readonly id: string
  readonly title: string
  readonly group: ThemeGroup
  readonly swatches: readonly [string, string, string, string]
}

export const DEFAULT_THEME = 'catppuccin'
export const THEME_STORAGE_KEY = 'crew.theme.v2'

export const THEME_GROUPS: readonly ThemeGroup[] = [
  'Product & AI-Native',
  'shadcn/ui Official Presets',
  'Dotfile & Editor Classics',
]

// Canonical 40 runtime-switchable themes locked in docs/plan/simulation/ui-sim/DECISION.md.
export const THEMES: readonly ThemeMeta[] = [
  { id: 'linear', title: 'Linear (Graphite & Indigo)', group: 'Product & AI-Native', swatches: ['#0f1014', '#16181f', '#818cf8', '#34d399'] },
  { id: 'claude', title: 'Claude (Warm Paper / Anthropic Dark)', group: 'Product & AI-Native', swatches: ['#191816', '#22201d', '#e07a58', '#52b788'] },
  { id: 'vercel', title: 'Vercel (Geist High-Contrast)', group: 'Product & AI-Native', swatches: ['#0a0a0a', '#121212', '#fafafa', '#50e3c2'] },
  { id: 'warp', title: 'Warp (Obsidian & Electric Iris)', group: 'Product & AI-Native', swatches: ['#0c0e16', '#141826', '#8e8eff', '#36d399'] },
  { id: 'supabase', title: 'Supabase (Carbon & Emerald)', group: 'Product & AI-Native', swatches: ['#121212', '#1c1c1c', '#3ecf8e', '#60a5fa'] },
  { id: 'vesper', title: 'Vesper (Velvet Amber & Mint)', group: 'Product & AI-Native', swatches: ['#101010', '#181818', '#ffc799', '#ffc799'] },
  { id: 'flexoki', title: 'Flexoki (Warm Paper & Ink)', group: 'Product & AI-Native', swatches: ['#100f0f', '#1c1b1a', '#4385be', '#3aa99f'] },
  { id: 'oxocarbon', title: 'IBM Oxocarbon (Carbon Blue & Teal)', group: 'Product & AI-Native', swatches: ['#161616', '#222222', '#78a9ff', '#3ddbd9'] },
  { id: 'poimandres', title: 'Poimandres (Storm & Seafoam Teal)', group: 'Product & AI-Native', swatches: ['#1b1e28', '#232735', '#5de4c7', '#89ddff'] },
  { id: 'night-owl', title: 'Night Owl / Light Owl', group: 'Product & AI-Native', swatches: ['#011627', '#0b253a', '#82aaff', '#7fdbca'] },
  { id: 'synthwave', title: "Synthwave '84 (Neon Dusk)", group: 'Product & AI-Native', swatches: ['#262335', '#2e2a40', '#ff7edb', '#ff7edb'] },

  { id: 'default', title: 'shadcn Default (Neutral)', group: 'shadcn/ui Official Presets', swatches: ['#ffffff', '#f4f4f5', '#171717', '#3b82f6'] },
  { id: 'shadcn-zinc-blue', title: 'shadcn Zinc · Blue', group: 'shadcn/ui Official Presets', swatches: ['#ffffff', '#f4f4f5', '#2563eb', '#3b82f6'] },
  { id: 'shadcn-stone-amber', title: 'shadcn Stone · Amber', group: 'shadcn/ui Official Presets', swatches: ['#ffffff', '#f5f5f4', '#d97706', '#f59e0b'] },
  { id: 'shadcn-zinc-violet', title: 'shadcn Zinc · Violet', group: 'shadcn/ui Official Presets', swatches: ['#09090b', '#18181b', '#7c3aed', '#8b5cf6'] },
  { id: 'shadcn-olive-emerald', title: 'shadcn Olive · Emerald', group: 'shadcn/ui Official Presets', swatches: ['#fafbf7', '#eff2ea', '#059669', '#10b981'] },
  { id: 'shadcn-mauve-rose', title: 'shadcn Mauve · Rose', group: 'shadcn/ui Official Presets', swatches: ['#fbf9fc', '#f3eef6', '#e11d48', '#f43f5e'] },
  { id: 'shadcn-mist-cyan', title: 'shadcn Mist · Cyan', group: 'shadcn/ui Official Presets', swatches: ['#f7fafb', '#eaf1f4', '#0891b2', '#06b6d4'] },
  { id: 'shadcn-taupe-orange', title: 'shadcn Taupe · Orange', group: 'shadcn/ui Official Presets', swatches: ['#fcfaf8', '#f4efeb', '#ea580c', '#f97316'] },
  { id: 'shadcn-zinc-teal', title: 'shadcn Zinc · Teal', group: 'shadcn/ui Official Presets', swatches: ['#09090b', '#18181b', '#0d9488', '#14b8a6'] },

  { id: 'catppuccin', title: 'Catppuccin (Latte / Mocha)', group: 'Dotfile & Editor Classics', swatches: ['#1e1e2e', '#313244', '#cba6f7', '#a6e3a1'] },
  { id: 'catppuccin-macchiato', title: 'Catppuccin (Latte / Macchiato)', group: 'Dotfile & Editor Classics', swatches: ['#24273a', '#363a4f', '#c6a0f6', '#a6da95'] },
  { id: 'catppuccin-frappe', title: 'Catppuccin (Latte / Frappé)', group: 'Dotfile & Editor Classics', swatches: ['#303446', '#414559', '#ca9ee6', '#a6d189'] },
  { id: 'tokyo-night', title: 'Tokyo Night (Day / Night)', group: 'Dotfile & Editor Classics', swatches: ['#1a1b26', '#24283b', '#7aa2f7', '#9ece6a'] },
  { id: 'tokyo-night-storm', title: 'Tokyo Night (Day / Storm)', group: 'Dotfile & Editor Classics', swatches: ['#24283b', '#292e42', '#7aa2f7', '#9ece6a'] },
  { id: 'tokyo-night-moon', title: 'Tokyo Night (Day / Moon)', group: 'Dotfile & Editor Classics', swatches: ['#222436', '#2f334d', '#82aaff', '#c3e88d'] },
  { id: 'dracula', title: 'Dracula (Alucard / Dracula)', group: 'Dotfile & Editor Classics', swatches: ['#282a36', '#343746', '#bd93f9', '#50fa7b'] },
  { id: 'one-dark', title: 'One Light / One Dark', group: 'Dotfile & Editor Classics', swatches: ['#282c34', '#21252b', '#61afef', '#98c379'] },
  { id: 'github', title: 'GitHub (Primer Light / Dark)', group: 'Dotfile & Editor Classics', swatches: ['#0d1117', '#161b22', '#58a6ff', '#3fb950'] },
  { id: 'gruvbox', title: 'Gruvbox (Light / Dark)', group: 'Dotfile & Editor Classics', swatches: ['#282828', '#3c3836', '#fe8019', '#b8bb26'] },
  { id: 'nord', title: 'Nord (Snow Storm / Polar Night)', group: 'Dotfile & Editor Classics', swatches: ['#2e3440', '#3b4252', '#88c0d0', '#a3be8c'] },
  { id: 'rose-pine', title: 'Rosé Pine (Dawn / Main)', group: 'Dotfile & Editor Classics', swatches: ['#191724', '#1f1d2e', '#ebbcba', '#9ccfd8'] },
  { id: 'rose-pine-moon', title: 'Rosé Pine (Dawn / Moon)', group: 'Dotfile & Editor Classics', swatches: ['#232136', '#2a273f', '#ea9a97', '#9ccfd8'] },
  { id: 'kanagawa', title: 'Kanagawa (Lotus / Wave)', group: 'Dotfile & Editor Classics', swatches: ['#1f1f28', '#2a2a37', '#7e9cd8', '#98bb6c'] },
  { id: 'kanagawa-dragon', title: 'Kanagawa (Lotus / Dragon)', group: 'Dotfile & Editor Classics', swatches: ['#181616', '#282727', '#8ba4b0', '#8a9a7b'] },
  { id: 'solarized', title: 'Solarized (Light / Dark)', group: 'Dotfile & Editor Classics', swatches: ['#002b36', '#073642', '#268bd2', '#859900'] },
  { id: 'everforest', title: 'Everforest (Light / Dark)', group: 'Dotfile & Editor Classics', swatches: ['#2d353b', '#343f44', '#a7c080', '#83c092'] },
  { id: 'ayu', title: 'Ayu (Light / Dark)', group: 'Dotfile & Editor Classics', swatches: ['#0b0e14', '#11151c', '#e6b450', '#aad94c'] },
  { id: 'ayu-mirage', title: 'Ayu (Light / Mirage)', group: 'Dotfile & Editor Classics', swatches: ['#1f2430', '#242936', '#ffcc66', '#bae67e'] },
  { id: 'monokai', title: 'Monokai (Derived Light / Dark)', group: 'Dotfile & Editor Classics', swatches: ['#272822', '#3e3d32', '#a6e22e', '#66d9ef'] },
] as const

export function isThemeId(id: string): boolean {
  return THEMES.some((t) => t.id === id)
}

export function findTheme(id: string): ThemeMeta {
  return THEMES.find((t) => t.id === id) ?? THEMES.find((t) => t.id === DEFAULT_THEME) ?? THEMES[0]!
}

export function applyThemeToDOM(theme: string, dark: boolean): void {
  if (typeof document === 'undefined') return
  const resolved = isThemeId(theme) ? theme : DEFAULT_THEME
  const root = document.documentElement
  root.dataset.theme = resolved
  root.classList.toggle('dark', dark)
}

function readLocalTheme(): { theme: string; dark: boolean } {
  if (typeof window === 'undefined') return { theme: DEFAULT_THEME, dark: true }
  try {
    const raw = window.localStorage.getItem(THEME_STORAGE_KEY)
    if (!raw) return { theme: DEFAULT_THEME, dark: true }
    const parsed = JSON.parse(raw) as { theme?: unknown; dark?: unknown }
    return {
      theme: typeof parsed.theme === 'string' && isThemeId(parsed.theme) ? parsed.theme : DEFAULT_THEME,
      dark: typeof parsed.dark === 'boolean' ? parsed.dark : true,
    }
  } catch {
    return { theme: DEFAULT_THEME, dark: true }
  }
}

function writeLocalTheme(theme: string, dark: boolean): void {
  if (typeof window === 'undefined') return
  try {
    window.localStorage.setItem(THEME_STORAGE_KEY, JSON.stringify({ theme, dark }))
  } catch {
    // Storage may be unavailable in private windows; StateDB still persists.
  }
}

function isEditableTarget(el: EventTarget | null): boolean {
  if (!(el instanceof HTMLElement)) return false
  const tag = el.tagName.toLowerCase()
  return tag === 'input' || tag === 'textarea' || tag === 'select' || el.isContentEditable
}

export function useThemePrefs() {
  const [theme, setThemeState] = useState<string>(DEFAULT_THEME)
  const [dark, setDarkState] = useState<boolean>(true)
  const [hydrated, setHydrated] = useState<boolean>(false)

  useEffect(() => {
    const local = readLocalTheme()
    setThemeState(local.theme)
    setDarkState(local.dark)
    applyThemeToDOM(local.theme, local.dark)
    setHydrated(true)

    let active = true
    fetch('/api/prefs', { credentials: 'same-origin' })
      .then(async (r) => {
        if (!r.ok) return null
        return (await r.json()) as { theme?: string; dark?: boolean }
      })
      .then((pref) => {
        if (!active || !pref) return
        const nextTheme = pref.theme && isThemeId(pref.theme) ? pref.theme : local.theme
        const nextDark = typeof pref.dark === 'boolean' ? pref.dark : local.dark
        setThemeState(nextTheme)
        setDarkState(nextDark)
        applyThemeToDOM(nextTheme, nextDark)
        writeLocalTheme(nextTheme, nextDark)
      })
      .catch(() => {})
    return () => {
      active = false
    }
  }, [])

  const persist = useCallback((nextTheme: string, nextDark: boolean) => {
    const clean = isThemeId(nextTheme) ? nextTheme : DEFAULT_THEME
    setThemeState(clean)
    setDarkState(nextDark)
    applyThemeToDOM(clean, nextDark)
    writeLocalTheme(clean, nextDark)
    void fetch('/api/prefs', {
      method: 'PUT',
      credentials: 'same-origin',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ theme: clean, dark: nextDark, direction: 'A' }),
    }).catch(() => {})
  }, [])

  const setTheme = useCallback(
    (nextTheme: string) => {
      persist(nextTheme, dark)
    },
    [dark, persist],
  )

  const toggleDark = useCallback(() => {
    persist(theme, !dark)
  }, [dark, persist, theme])

  useEffect(() => {
    if (!hydrated) return
    const onKey = (e: KeyboardEvent) => {
      if (e.metaKey || e.ctrlKey || e.altKey || isEditableTarget(e.target)) return
      if (e.key === 'd' || e.key === 'D') {
        e.preventDefault()
        persist(theme, !dark)
      }
    }
    window.addEventListener('keydown', onKey)
    return () => window.removeEventListener('keydown', onKey)
  }, [dark, hydrated, persist, theme])

  return { theme, dark, setTheme, toggleDark, current: findTheme(theme) }
}
