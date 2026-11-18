// A line is one line of a diff: kept, added, or removed.
export type Line = { kind: ' ' | '+' | '-'; text: string }

// lineDiff is the lines of before and after as a diff: the longest run of kept lines, and the rest
// added or removed in place. The files are a few hundred lines, so the table is small.
export function lineDiff(before: string, after: string): Line[] {
  const a = before.split('\n')
  const b = after.split('\n')
  const n = a.length
  const m = b.length
  const w = m + 1
  // lcs[i*w+j] is the longest run of kept lines from a[i] and b[j] on.
  const lcs = new Int32Array((n + 1) * w)
  const at = (i: number, j: number) => lcs[i * w + j] ?? 0
  for (let i = n - 1; i >= 0; i--) {
    for (let j = m - 1; j >= 0; j--) {
      lcs[i * w + j] = a[i] === b[j] ? at(i + 1, j + 1) + 1 : Math.max(at(i + 1, j), at(i, j + 1))
    }
  }
  const out: Line[] = []
  let i = 0
  let j = 0
  while (i < n && j < m) {
    if (a[i] === b[j]) {
      out.push({ kind: ' ', text: a[i] ?? '' })
      i++
      j++
    } else if (at(i + 1, j) >= at(i, j + 1)) {
      out.push({ kind: '-', text: a[i++] ?? '' })
    } else {
      out.push({ kind: '+', text: b[j++] ?? '' })
    }
  }
  while (i < n) out.push({ kind: '-', text: a[i++] ?? '' })
  while (j < m) out.push({ kind: '+', text: b[j++] ?? '' })
  return out
}
