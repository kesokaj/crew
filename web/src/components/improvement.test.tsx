import { renderToStaticMarkup } from 'react-dom/server'
import { describe, expect, it } from 'vitest'
import { type Improvement, status } from './improvement'

const x = (evals: Improvement['evals']): Improvement => ({
  number: 2,
  title: 't',
  status: 'testing',
  waitsFor: 'cases',
  target: [],
  evidence: {},
  body: '',
  expected: '',
  measure: 'fix_rounds_per_ticket',
  evals,
  createdAt: '2026-10-30T00:00:00Z',
})

// An improvement whose evals could not tell because runs of the change measured nothing does not wait
// for more cases, which would not help (10.5, since 1.10); one with too few cases still does.
describe('an improvement that waits in testing says for what', () => {
  it('says its evals could not tell when runs measured nothing', () => {
    const html = renderToStaticMarkup(<>{status(x({ verdict: 'too_few', why: 'w', unmeasuredRuns: 1 }))}</>)
    expect(html).toContain('waits: its evals could not tell')
    expect(html).not.toContain('more eval cases')
  })
  it('waits for more cases otherwise', () => {
    expect(renderToStaticMarkup(<>{status(x({ verdict: 'too_few', why: '9 cases' }))}</>)).toContain('waits for more eval cases')
    expect(renderToStaticMarkup(<>{status(x(null))}</>)).toContain('waits for more eval cases')
  })
})
