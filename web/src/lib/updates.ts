// What the Updater card's Last changes say of a request that ended (16.3, 16.5): an install and a
// roll back switch, a gate's and a check's request only check beside the running version, and
// install nothing. What the updater said is a free part (said.ts).

import { clean } from './gate'
import { free, type Said } from './said'

export type Ended = { action: string; state: string; version: string; from?: string; error?: string }

export function lastChange(q: Ended): Said {
  const why = free(clean(q.error ?? q.state))
  if (q.action === 'gate') {
    return q.state === 'done'
      ? [`The gate's steps passed beside ${q.from ?? 'the running version'}. Nothing was installed.`]
      : ['The gate did not pass: ', why, '. Nothing was installed.']
  }
  if (q.action === 'check') {
    return q.state === 'done' ? [`Checked beside ${q.from ?? 'the running version'} for the gate.`] : ['The check for the gate did not pass: ', why, '. Nothing was installed.']
  }
  if (q.state === 'done') return [`Checked beside ${q.from ?? 'the version before'}, switched, and healthy for ten minutes.`]
  if (q.state === 'switched_back') return ['Switched back: ', free(clean(q.error ?? ''))]
  return ['Not installed: ', why, `. ${q.from ?? 'The running version'} kept running.`]
}

// What a release's line says of its copy on GitHub (16.9): copied, failed and tried again, never copied
// (no gate, or built by hand), or being copied. With updates.publish off nothing is copied, and the
// line says so, not that it is being copied (since 0.53, seen on dave with publish off).
export type Copyable = {
  version: string
  published?: { done?: unknown } | null
  publishError?: string | null
  gate?: unknown
  byHand: boolean
}

export function copied(r: Copyable, publish: boolean): Said {
  if (r.published?.done) return [`v${r.version} · release on GitHub with notes · images in the registry`]
  if (!r.gate) return [r.byHand ? 'built by hand: not copied to GitHub' : 'no gate: not copied to GitHub']
  if (!publish) return ['not copied to GitHub: copying is off (updates.publish)']
  if (r.publishError) return ['not yet copied to GitHub: ', free(r.publishError), '. Crew tries again.']
  return ['being copied to GitHub']
}
