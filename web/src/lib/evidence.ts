// seenIn is where an improvement's signals were seen, as the improver wrote it (10.5). A bare count,
// such as "4 of 5", gets its noun, as in improvements.png; a sentence is shown as it is, never with a
// noun added to its end (M15 on dave).
export function seenIn(e: { projects?: string; tasks?: string }): string {
  const part = (text: string | undefined, noun: string) => {
    const t = (text ?? '').trim()
    if (!t) return ''
    return /^\d+( of \d+)?$/.test(t) ? `${t} ${noun}` : t
  }
  return [part(e.projects, 'projects'), part(e.tasks, 'tickets')].filter(Boolean).join(', ') || '—'
}
