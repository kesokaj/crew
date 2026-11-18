// The settings screens' side of the settings API (design 14.1 to 14.3): a document of settings is
// read and sent whole, and a key is named by its dotted path, as the server names it.
export type Doc = Record<string, unknown>

// What a screen needs of a schema beyond the values (GET /api/settings/schema).
export type SchemaView = { enums: Record<string, string[]>; nullable: Record<string, string>; fixed: string[]; floats: string[] }
export type Schemas = { machine: SchemaView; project: SchemaView }

// A key's history (14.2): its lines, newest first. A value that is not set is the key's default.
export type HistoryLine = { seq: number; at: string; who: string; before: unknown; after: unknown; beforeSet: boolean; afterSet: boolean }
export type History = { key: string; default: unknown; changes: HistoryLine[] }

const isDoc = (v: unknown): v is Doc => typeof v === 'object' && v !== null && !Array.isArray(v)

// valueAt reads a value of d by its path.
export function valueAt(d: Doc | null | undefined, path: string): unknown {
  let cur: unknown = d
  for (const p of path.split('.')) {
    if (!isDoc(cur)) return undefined
    cur = cur[p]
  }
  return cur
}

// withValue is d with the value at path replaced: the groups on the way are copied, d is not changed.
export function withValue(d: Doc, path: string, v: unknown): Doc {
  const [head = '', ...rest] = path.split('.')
  const out = { ...d }
  out[head] = rest.length ? withValue(isDoc(d[head]) ? (d[head] as Doc) : {}, rest.join('.'), v) : v
  return out
}

// problemsAt sorts the problems of a refused change, one "path: reason" a line as the server writes
// them, by their path. A line with no path is kept under "".
export function problemsAt(message: string): Record<string, string> {
  const out: Record<string, string> = {}
  for (const line of message.split('\n')) {
    const m = /^([A-Za-z0-9_.[\]*-]+): (.+)$/.exec(line.trim())
    const [key, why] = m ? [m[1] ?? '', m[2] ?? ''] : ['', line.trim()]
    if (!why) continue
    out[key] = out[key] ? `${out[key]}; ${why}` : why
  }
  return out
}

// problemFor is what of the problems belongs under the field of path: its own, and one of a group it
// is in, such as "disk: clean_at must be below stop_at" under disk.clean_at.
export function problemFor(problems: Record<string, string>, path: string): string {
  return Object.entries(problems)
    .filter(([k]) => k && (k === path || path.startsWith(k + '.') || k.startsWith(path + '[')))
    .map(([, v]) => v)
    .join('; ')
}

// shown says a value in words: none for null, a list with commas, on and off for a switch.
export function shown(v: unknown): string {
  if (v === null || v === undefined) return 'none'
  if (typeof v === 'boolean') return v ? 'on' : 'off'
  if (Array.isArray(v)) return v.length ? v.map(shown).join(', ') : 'none'
  if (isDoc(v)) return JSON.stringify(v)
  return String(v)
}

// lineText says one line of a key's history: what the value was before it and after it.
export function lineText(l: HistoryLine): string {
  const side = (v: unknown, set: boolean) => (set ? shown(v) : 'the default')
  return `${side(l.before, l.beforeSet)} → ${side(l.after, l.afterSet)}`
}
