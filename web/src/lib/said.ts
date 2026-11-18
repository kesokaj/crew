// What the app says on a line, in parts: its own words, and the text it shows from outside itself,
// free, which a screen marks data-free-text (design 0.53): an error a command printed, a step's
// detail, a name an agent wrote. The gate's browser check reads every text of a screen less what is
// marked, so a line keeps its own words read and its outside text marked.
export type Part = string | { free: string }
export type Said = Part[]

export const free = (s: string): Part => ({ free: s })

// saidText is the line as it reads.
export const saidText = (s: Said): string => s.map((p) => (typeof p === 'string' ? p : p.free)).join('')
