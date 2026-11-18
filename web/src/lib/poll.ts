import { useCallback, useEffect, useRef, useState } from 'react'
import { getJSON } from './api'

const inFlight = new Map<string, Promise<unknown>>()

export function fetchCoalesced<T>(path: string, fetcher: (p: string) => Promise<T> = getJSON): Promise<T> {
  const existing = inFlight.get(path) as Promise<T> | undefined
  if (existing) return existing
  const req = fetcher(path).finally(() => {
    inFlight.delete(path)
  })
  inFlight.set(path, req)
  return req
}

export function isDocumentVisible(doc?: { visibilityState?: string } | null): boolean {
  const target = doc !== undefined ? doc : typeof document !== 'undefined' ? document : null
  if (!target || !target.visibilityState) return true
  return target.visibilityState !== 'hidden'
}

// usePoll reads path now and again every ms while the page is visible, so what agents do shows
// without a reload. A path of null reads nothing. reload reads it at once, after a change.
// Across non-null path changes, previous data stays shown until the new path answers so switching
// projects or tickets never flashes an empty "Loading…" screen.
export function usePoll<T>(
  path: string | null,
  ms = 5000,
  options?: { keepPreviousData?: boolean },
): { data: T | null; error: string; reload: () => void } {
  const keepPreviousData = options?.keepPreviousData ?? true
  const [data, setData] = useState<T | null>(null)
  const [error, setError] = useState('')
  const current = useRef(path)
  current.current = path
  const reload = useCallback(() => {
    if (!path) return
    fetchCoalesced<T>(path).then(
      (d) => {
        if (current.current !== path) return
        setData(d)
        setError('')
      },
      (e: Error) => {
        if (current.current === path) setError(e.message)
      },
    )
  }, [path])
  useEffect(() => {
    if (!path) {
      setData(null)
      setError('')
      return
    }
    if (!keepPreviousData) setData(null)
    reload()
    if (!ms) return
    const tick = () => {
      if (isDocumentVisible()) reload()
    }
    const onVisibilityChange = () => {
      if (isDocumentVisible()) reload()
    }
    const t = setInterval(tick, ms)
    const hasDoc = typeof document !== 'undefined' && typeof document.addEventListener === 'function'
    if (hasDoc) document.addEventListener('visibilitychange', onVisibilityChange)
    return () => {
      clearInterval(t)
      if (hasDoc) document.removeEventListener('visibilitychange', onVisibilityChange)
    }
  }, [path, keepPreviousData, reload, ms])
  return { data, error, reload }
}
