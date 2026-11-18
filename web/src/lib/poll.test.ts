import { describe, expect, it, vi } from 'vitest'
import { fetchCoalesced, isDocumentVisible } from './poll'

describe('usePoll helpers (visibility & in-flight request coalescing)', () => {
  it('treats missing document or visible state as visible, and hidden as not visible', () => {
    expect(isDocumentVisible(null)).toBe(true)
    expect(isDocumentVisible({})).toBe(true)
    expect(isDocumentVisible({ visibilityState: 'visible' })).toBe(true)
    expect(isDocumentVisible({ visibilityState: 'hidden' })).toBe(false)
  })

  it('coalesces concurrent in-flight requests to the same path and allows new requests after completion', async () => {
    let calls = 0
    const fetcher = vi.fn(async (p: string) => {
      calls++
      await new Promise((r) => setTimeout(r, 10))
      return { path: p, calls }
    })

    const [a, b] = await Promise.all([fetchCoalesced('/api/frame', fetcher), fetchCoalesced('/api/frame', fetcher)])
    expect(fetcher).toHaveBeenCalledTimes(1)
    expect(a).toEqual({ path: '/api/frame', calls: 1 })
    expect(b).toEqual({ path: '/api/frame', calls: 1 })

    const c = await fetchCoalesced('/api/frame', fetcher)
    expect(fetcher).toHaveBeenCalledTimes(2)
    expect(c).toEqual({ path: '/api/frame', calls: 2 })
  })
})
