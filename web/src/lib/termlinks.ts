// The links in a terminal's rows, so they can be clicked where they are printed. A long link is
// often on several rows: the terminal wraps it (a wrapped row), or the CLI breaks it itself, as agy
// does. A row is read as the rest of a link when the link reached the end of the row above
// and the row holds nothing but characters of a link. Only https links are found.
export type Row = { text: string; wrapped: boolean }
export type Cell = { x: number; y: number } // column and row, from 0
export type Span = { url: string; start: Cell; end: Cell } // end is the link's last cell

const linkChars = /^[A-Za-z0-9\-._~:/?#[\]@!$&()*+,;=%']+$/
const link = /https:\/\/[^\s"'<>`]+/g
const openLink = /https:\/\/[^\s"'<>`]*$/

export function findSpans(rows: Row[]): Span[] {
  const chars: string[] = []
  const at: Cell[] = []
  const add = (text: string, y: number, from = 0) => {
    for (let x = from; x < text.length; x++) {
      chars.push(text.charAt(x))
      at.push({ x, y })
    }
  }
  const trimEnd = () => {
    while (chars.length && /\s/.test(chars.at(-1) ?? '')) {
      chars.pop()
      at.pop()
    }
  }
  rows.forEach((row, y) => {
    if (y > 0 && row.wrapped) return add(row.text, y)
    const body = row.text.trim()
    if (y > 0 && body && linkChars.test(body)) {
      trimEnd()
      if (openLink.test(chars.join(''))) return add(row.text, y, row.text.length - row.text.trimStart().length)
    }
    if (y > 0) {
      trimEnd()
      chars.push('\n')
      at.push({ x: -1, y })
    }
    add(row.text, y)
  })
  const text = chars.join('')
  const out: Span[] = []
  for (const m of text.matchAll(link)) {
    const url = m[0].replace(/[.,;:!?)\]]+$/, '')
    const i = m.index ?? 0
    const start = at[i]
    const end = at[i + url.length - 1]
    if (start && end) out.push({ url, start, end })
  }
  return out
}
