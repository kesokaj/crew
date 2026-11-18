// marks splits the rewrite into its pieces, each said to be new or kept against the original: the
// words of the longest common sequence of words are kept, the rest are new. Spaces go with the word
// before them. A text too long to compare comes back as one kept piece.
export type Piece = { text: string; added: boolean }

export function marks(original: string, rewrite: string, limit = 3000): Piece[] {
  const a = original.split(/\s+/).filter(Boolean)
  const b = tokens(rewrite)
  const words = b.filter((t) => t.trim() !== '')
  if (a.length * words.length > limit * limit) return [{ text: rewrite, added: false }]
  // The table of the longest common sequence, from the ends.
  const n = a.length
  const m = words.length
  const t: number[][] = Array.from({ length: n + 1 }, () => new Array<number>(m + 1).fill(0))
  for (let i = n - 1; i >= 0; i--)
    for (let j = m - 1; j >= 0; j--) t[i]![j] = a[i] === words[j] ? t[i + 1]![j + 1]! + 1 : Math.max(t[i + 1]![j]!, t[i]![j + 1]!)
  const kept = new Set<number>()
  for (let i = 0, j = 0; i < n && j < m; ) {
    if (a[i] === words[j]) {
      kept.add(j)
      i++
      j++
    } else if (t[i + 1]![j]! >= t[i]![j + 1]!) i++
    else j++
  }
  const out: Piece[] = []
  let w = -1
  for (const tok of b) {
    if (tok.trim() === '') {
      if (out.length) out[out.length - 1]!.text += tok
      else out.push({ text: tok, added: false })
      continue
    }
    w++
    const added = !kept.has(w)
    const last = out[out.length - 1]
    if (last && last.added === added) last.text += tok
    else out.push({ text: tok, added })
  }
  return out
}

function tokens(s: string): string[] {
  return s.split(/(\s+)/).filter((t) => t !== '')
}
