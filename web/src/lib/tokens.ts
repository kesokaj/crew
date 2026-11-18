// How a use of tokens is said (design 0.53): Crew shows no cost, and where a cost was, tokens are. A
// count is short, with one decimal below a hundred of its unit: 950, 12.3k, 123k, 4.5M.
const units = ['', 'k', 'M', 'B']

const shown = (v: number, u: number) => (u === 0 ? String(Math.round(v)) : v < 100 ? v.toFixed(1).replace(/\.0$/, '') : v.toFixed(0))

export function tokenCount(n?: number | null): string {
  if (n === undefined || n === null || !Number.isFinite(n)) return '—'
  let v = Math.abs(n)
  let u = 0
  // A value that rounds up to a thousand of its unit is said in the next one: 999,960 is 1M, not 1000k.
  while (u < units.length - 1 && Number(shown(v, u)) >= 1000) {
    v /= 1000
    u++
  }
  return (n < 0 ? '-' : '') + shown(v, u) + units[u]
}

// tokens is a count with its word, for a place whose heading does not say it: "12.3k tokens".
export function tokens(n?: number | null): string {
  if (n === undefined || n === null || !Number.isFinite(n)) return '—'
  return `${tokenCount(n)} ${Math.round(n) === 1 ? 'token' : 'tokens'}`
}
