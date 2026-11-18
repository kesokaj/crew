import { createRootRoute, HeadContent, Link, Outlet, Scripts, useHydrated, useRouterState } from '@tanstack/react-router'
import { useEffect, useState, type ReactNode } from 'react'
import type { Frame } from '../lib/api'
import { usePoll } from '../lib/poll'
import { DEFAULT_THEME, useThemePrefs } from '../lib/themes'
import { Badge } from '../components/ui'
import { CommandPalette } from '../components/command-palette'
import { SignInDialog } from '../components/signin'
import { ThemeGalleryModal } from '../components/theme-gallery'
import appCss from '../index.css?url'

export const Route = createRootRoute({
  head: () => ({
    meta: [
      { charSet: 'utf-8' },
      { name: 'viewport', content: 'width=device-width, initial-scale=1.0' },
      { title: 'Crew' },
    ],
    links: [{ rel: 'stylesheet', href: appCss }],
  }),
  component: RootDocument,
  notFoundComponent: NotFound,
})

// A route the UI does not have yet: the screens come with their milestones (design 20.1).
function NotFound() {
  return (
    <div className="mx-auto max-w-2xl p-8">
      <h1 className="mb-2 text-xl font-semibold">Not here yet</h1>
      <p className="text-text-dim">This page of Crew does not exist yet.</p>
    </div>
  )
}

// The frame of every page (design 20): the top bar with the logo and Crew's version on the left, and
// the lead slots of each CLI, the running sandboxes, and the inbox on the right. On the left, the
// screens of the machine, the projects, and the screens of the project that is open. Each screen
// comes with its milestone (20.1).
function RootDocument() {
  const { data: f } = usePoll<Frame>('/api/frame', 10000)
  const path = useRouterState({ select: (s) => s.location.pathname })
  const open = /^\/p\/([^/]+)/.exec(path)?.[1]
  // The shell every page loads is rendered at "/", and hydration keeps its attributes: a link, which
  // marks the page it goes to, is made by the client alone.
  const hydrated = useHydrated()
  const [mobileNavOpen, setMobileNavOpen] = useState(false)
  const [paletteOpen, setPaletteOpen] = useState(false)
  const [themeGalleryOpen, setThemeGalleryOpen] = useState(false)
  const [sidebarCollapsed, setSidebarCollapsed] = useState(false)
  const { theme, dark, setTheme, toggleDark } = useThemePrefs()

  useEffect(() => {
    setMobileNavOpen(false)
    setPaletteOpen(false)
    setThemeGalleryOpen(false)
  }, [path])

  useEffect(() => {
    const onKey = (e: KeyboardEvent) => {
      if ((e.metaKey || e.ctrlKey) && e.key.toLowerCase() === 'k') {
        e.preventDefault()
        setPaletteOpen((v) => !v)
      } else if (e.key === 'Escape' && mobileNavOpen) {
        setMobileNavOpen(false)
      }
    }
    window.addEventListener('keydown', onKey)
    return () => window.removeEventListener('keydown', onKey)
  }, [mobileNavOpen])

  return (
    <html lang="en" data-theme={hydrated ? theme : DEFAULT_THEME} className={hydrated ? (dark ? 'dark' : undefined) : 'dark'}>
      <head>
        <HeadContent />
      </head>
      <body>
        <div className="flex h-full flex-col">
          <header className="flex h-[52px] shrink-0 items-center gap-2.5 border-b border-border bg-bg-raised px-3 sm:gap-3 sm:px-4">
            {hydrated && (
              <button
                type="button"
                className="inline-flex min-h-8 min-w-8 items-center justify-center rounded-md border border-border px-2 text-sm text-text hover:bg-bg-sunk focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-accent sm:hidden"
                aria-label={mobileNavOpen ? 'Close navigation menu' : 'Open navigation menu'}
                aria-expanded={mobileNavOpen}
                onClick={() => setMobileNavOpen((v) => !v)}
              >
                <span aria-hidden="true">{mobileNavOpen ? '×' : '☰'}</span>
              </button>
            )}
            {hydrated ? (
              <Link to="/" className="flex items-center gap-1.5 text-xl font-bold tracking-tight">
                <Logo />
                crew
              </Link>
            ) : (
              <a href="/" className="flex items-center gap-1.5 text-xl font-bold tracking-tight">
                <Logo />
                crew
              </a>
            )}
            {f && <span className="hidden rounded border border-border px-1.5 font-mono text-xs text-text-dim sm:inline">{f.version}</span>}
            {open && (
              <span className="max-w-24 truncate rounded bg-accent-soft px-1.5 py-0.5 font-mono text-xs font-semibold text-accent sm:hidden">
                {decodeURIComponent(open)}
              </span>
            )}
            {f && (
              <div className="ml-auto flex items-center gap-1.5 text-sm sm:gap-2">
                {hydrated && (
                  <>
                    <button
                      type="button"
                      data-open-themes
                      onClick={() => setThemeGalleryOpen(true)}
                      className="inline-flex min-h-8 items-center gap-1.5 rounded-md border border-border bg-bg px-2 py-1 text-xs text-text transition-colors hover:border-accent sm:px-2.5"
                      title="Open Themes (40) Swatch Gallery"
                    >
                      <span className="hidden sm:inline">Themes (40)</span>
                      <span className="sm:hidden">Themes</span>
                    </button>
                    <button
                      type="button"
                      data-toggle-dark
                      onClick={toggleDark}
                      aria-label="Toggle light or dark mode"
                      title="Toggle light or dark mode (d)"
                      className="inline-flex min-h-8 items-center rounded-md border border-border bg-bg px-2 py-1 text-xs text-text-dim transition-colors hover:border-accent hover:text-text sm:px-2.5"
                    >
                      {dark ? 'Dark' : 'Light'}
                    </button>
                    <button
                      type="button"
                      aria-label="Open command palette"
                      onClick={() => setPaletteOpen(true)}
                      className="hidden min-h-8 items-center gap-2 rounded-md border border-border bg-bg px-2.5 py-1 text-xs text-text-dim transition-colors hover:border-accent hover:text-text focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-accent sm:inline-flex"
                    >
                      <span>Jump to…</span>
                      <kbd className="rounded bg-bg-sunk px-1 py-0.5 font-mono text-[11px]">⌘K</kbd>
                    </button>
                  </>
                )}
                {f.activeTickets && (
                  <Link
                    to="/sandboxes"
                    className="hidden items-center gap-1.5 rounded-md border border-border px-3 py-1.5 transition-colors hover:border-accent hover:bg-accent-soft/40 md:flex"
                    title="Active tickets across all projects (7.9)"
                  >
                    Tickets{' '}
                    <b className={f.activeTickets.active > 0 ? 'text-accent' : ''}>
                      {f.activeTickets.active}/{f.activeTickets.max}
                    </b>
                  </Link>
                )}
                <Link
                  to="/"
                  className="hidden items-center gap-2 rounded-md border border-border px-3 py-1.5 transition-colors hover:border-accent hover:bg-accent-soft/40 md:flex"
                  title="Lead slots in use, for each CLI (7.9)"
                >
                  Leads
                  {f.leadSlots.map((l) => (
                    <span key={l.cli} className="flex items-baseline gap-1">
                      <span className="font-mono text-xs text-text-dim">{l.cli}</span>
                      <b className={l.used > 0 ? 'text-accent' : ''}>
                        {l.used}/{l.slots}
                      </b>
                    </span>
                  ))}
                </Link>
                <Link
                  to="/sandboxes"
                  className="hidden rounded-md border border-border px-3 py-1.5 transition-colors hover:border-accent hover:bg-accent-soft/40 sm:inline-flex"
                  title="Sandboxes running on this machine"
                >
                  Sandboxes <b className="ml-1">{f.sandboxes}</b>
                </Link>
                {f.held && (
                  // Two microVMs in a row that did not boot (21): the inbox has the check and Resume. A restart
                  // for security updates (16.10): the feed on All projects has its news.
                  <Link to={f.held.startsWith('MicroVMs') ? '/inbox' : '/'} data-held className="rounded-md border border-bad px-2 py-1 text-xs text-bad sm:px-3 sm:py-1.5 sm:text-sm" title={f.held}>
                    {f.held.startsWith('MicroVMs')
                      ? 'MicroVMs do not start'
                      : f.held.startsWith('The disk')
                        ? 'Disk full'
                        : f.held.startsWith('A restart')
                          ? 'Restart for updates'
                          : f.held.includes('stopped')
                            ? 'Stopped'
                            : 'Paused'}
                  </Link>
                )}
                <Link
                  to="/inbox"
                  className={
                    'rounded-md border px-2 py-1 text-xs sm:px-3 sm:py-1.5 sm:text-sm ' +
                    (f.inbox > 0 ? 'border-amber bg-amber-soft text-amber-text' : 'border-border')
                  }
                >
                  Inbox <b>{f.inbox}</b>
                </Link>
                <span className="hidden text-xs text-text-dim lg:inline" title="Signed in">
                  {f.email}
                </span>
              </div>
            )}
          </header>
          <div className="relative flex min-h-0 flex-1">
            <nav
              className={
                'hidden shrink-0 flex-col gap-0.5 overflow-auto border-r border-border bg-sidebar p-3 text-[15px] text-sidebar-foreground transition-all sm:flex ' +
                (hydrated && sidebarCollapsed ? 'w-[60px]' : 'w-[232px]')
              }
            >
              {hydrated && (
                <div className="mb-1 flex items-center justify-between px-1">
                  <span className={sidebarCollapsed ? 'sr-only' : 'text-[11px] font-semibold tracking-wider text-text-dim uppercase'}>
                    Control Plane
                  </span>
                  <button
                    type="button"
                    data-toggle-sidebar
                    aria-label={sidebarCollapsed ? 'Expand sidebar' : 'Collapse sidebar'}
                    onClick={() => setSidebarCollapsed((v) => !v)}
                    className="rounded px-1.5 py-0.5 text-xs text-text-dim hover:bg-bg-sunk hover:text-text"
                  >
                    {sidebarCollapsed ? '→' : '←'}
                  </button>
                </div>
              )}
              {hydrated && !sidebarCollapsed && <SidebarMenu f={f} open={open} />}
            </nav>
            {hydrated && mobileNavOpen && (
              <div className="fixed inset-0 z-50 flex sm:hidden" data-mobile-drawer>
                <div
                  className="fixed inset-0 bg-black/40"
                  aria-hidden="true"
                  onClick={() => setMobileNavOpen(false)}
                />
                <aside
                  role="dialog"
                  aria-modal="true"
                  aria-label="Mobile navigation drawer"
                  className="relative z-10 flex h-full w-[260px] max-w-[82vw] flex-col gap-0.5 overflow-auto border-r border-border bg-bg-raised p-3 text-[15px] shadow-xl"
                >
                  <div className="mb-2 flex items-center justify-between border-b border-border pb-2">
                    <span className="font-semibold text-text">Navigation</span>
                    <button
                      type="button"
                      className="min-h-8 min-w-8 rounded-md px-2 text-xs text-text-dim hover:bg-bg-sunk hover:text-text"
                      aria-label="Close navigation menu"
                      onClick={() => setMobileNavOpen(false)}
                    >
                      Close
                    </button>
                  </div>
                  <SidebarMenu f={f} open={open} />
                </aside>
              </div>
            )}
            <main className="min-h-0 flex-1 overflow-auto">
              <Outlet />
            </main>
          </div>
        </div>
        {hydrated && paletteOpen && (
          <CommandPalette open={paletteOpen} onClose={() => setPaletteOpen(false)} frame={f} activeProject={open ? decodeURIComponent(open) : undefined} />
        )}
        {hydrated && themeGalleryOpen && (
          <ThemeGalleryModal
            open={themeGalleryOpen}
            theme={theme}
            dark={dark}
            onSelectTheme={setTheme}
            onToggleDark={toggleDark}
            onClose={() => setThemeGalleryOpen(false)}
          />
        )}
        {hydrated && <SignInDialog />}
        <Scripts />
      </body>
    </html>
  )
}

export function SidebarMenu({ f, open }: { f: Frame | null; open?: string }) {
  return (
    <>
      <Section>Crew</Section>
      <NavLink to="/" exact>
        All projects
      </NavLink>
      <NavLink to="/inbox" right={f && f.inbox > 0 ? <Badge tone="amber">{f.inbox}</Badge> : null}>
        Inbox
      </NavLink>
      <NavLink to="/conversations">Conversations</NavLink>
      <NavLink
        to="/plans"
        right={
          f && f.drafts > 0 ? (
            <span className="text-xs text-text-dim">
              {f.drafts} {f.drafts === 1 ? 'draft' : 'drafts'}
            </span>
          ) : null
        }
      >
        Plans
      </NavLink>
      <NavLink to="/crew">Crew level</NavLink>
      <NavLink to="/versions" right={f?.nextVersion ? <Badge tone="amber">{f.nextVersion}</Badge> : null}>
        Versions
      </NavLink>
      <NavLink to="/clis">Agent CLIs</NavLink>
      <NavLink to="/models">Models</NavLink>
      <NavLink to="/setup">Setup</NavLink>
      <NavLink to="/crew-mcp">Crew MCP</NavLink>
      <NavLink to="/database">Database</NavLink>
      <NavLink to="/sandboxes" right={f && f.sandboxes > 0 ? <Badge tone="blue">{f.sandboxes}</Badge> : null}>
        Sandboxes
      </NavLink>
      <NavLink to="/runs">Runs</NavLink>
      <NavLink to="/notifications">Notifications</NavLink>
      <NavLink to="/settings">Machine settings</NavLink>
      <NavLink to="/storage">Runtimes &amp; Storage</NavLink>
      {f && f.projects.length > 0 && <Section>Projects</Section>}
      {f?.projects.map((p) => (
        <NavLink
          key={p.name}
          to="/p/$project/board"
          params={{ project: p.name }}
          bold={p.name === open}
          right={
            <span className="flex gap-1">
              {p.needsYou > 0 && <Badge tone="amber">{p.needsYou}</Badge>}
              {p.working > 0 && <Badge tone="blue">{p.working}</Badge>}
            </span>
          }
        >
          {p.name}
        </NavLink>
      ))}
      {open && <ProjectNav project={decodeURIComponent(open)} />}
    </>
  )
}

// The screens of one project (20.1), as they come with the milestones.
function ProjectNav({ project }: { project: string }) {
  const screens = [
    ['factory', 'Factory Line'],
    ['board', 'Board'],
    ['specs', 'Specs & WORKFLOW'],
    ['chat', 'Project Chat'],
    ['skills', 'Skills Registry'],
    ['tickets', 'Tickets'],
    ['plan', 'Plan'],
    ['activity', 'Activity'],
    ['agents', 'Agents'],
    ['docs', 'Documentation'],
    ['summaries', 'Summaries'],
    ['trajectories', 'Trajectories'],
    ['knowledge', 'Knowledge'],
    ['analytics', 'Analytics'],
    ['git', 'Git'],
    ['hosting', 'Hosting'],
    ['terminal', 'Terminal'],
    ['settings', 'Settings'],
  ] as const
  return (
    <>
      <Section>{project}</Section>
      {screens.map(([to, label]) => (
        <Link
          key={to}
          to={`/p/$project/${to}`}
          params={{ project }}
          className="rounded-md border border-transparent px-2 py-1.5 pl-5 text-sm transition-colors hover:bg-bg-sunk/80 hover:text-text"
          activeProps={{
            className:
              'rounded-md border border-accent/40 border-l-4 border-l-accent bg-accent-soft px-2 py-1.5 pl-5 text-sm font-semibold text-accent shadow-xs',
          }}
        >
          {label}
        </Link>
      ))}
    </>
  )
}

function Section({ children }: { children: ReactNode }) {
  return <p className="px-2 pt-3 pb-1 text-xs tracking-wide text-text-dim uppercase first:pt-0">{children}</p>
}

function NavLink({
  to,
  params,
  children,
  right,
  exact,
  bold,
}: {
  to: string
  params?: Record<string, string>
  children: ReactNode
  right?: ReactNode
  exact?: boolean
  bold?: boolean
}) {
  const base = 'flex items-center justify-between rounded-md px-2 py-1.5 transition-colors hover:bg-bg-sunk/80 '
  const active = 'border border-accent/40 border-l-4 border-l-accent bg-accent-soft font-semibold text-accent shadow-xs'
  return (
    <Link
      to={to}
      params={params}
      activeOptions={{ exact: !!exact }}
      className={base + (bold ? active : 'border border-transparent')}
      activeProps={{ className: base + active }}
    >
      <span className="truncate">{children}</span>
      {right}
    </Link>
  )
}

function Logo() {
  return (
    <svg width="18" height="18" viewBox="0 0 18 18" aria-hidden="true">
      <rect x="1" y="3" width="5" height="14" rx="1.5" fill="var(--color-amber)" />
      <rect x="8" y="1" width="4" height="9" rx="1.5" fill="var(--color-text)" />
      <rect x="14" y="1" width="4" height="5" rx="1.5" fill="var(--color-accent)" />
    </svg>
  )
}
