import type { QuoteSource } from './api'

// The part of the task view a text was marked in, as the operator reads it: its label, or its part
// when the server kept no label (CheckQuoteSource bounds a label, and lets it be empty).
export function markedUnder(s?: QuoteSource): string {
  if (!s) return 'marked text'
  const where = s.label.trim() || s.part
  return `marked under ${where}`
}
