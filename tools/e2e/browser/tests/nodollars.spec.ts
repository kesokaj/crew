import { expect, test } from '@playwright/test'
import { findings } from '../gate/nodollars'

// The rules of the shared no-dollar check (design 0.53), on their own: each must-fail text is found,
// and each text the app says since 0.53 is not (review of 0.53). It opens no page.
test('the no-dollar rules find an amount, an estimate, a budget, and a cost, and pass what the app says', () => {
  for (const bad of [
    '$1.20 of $10.00 est.',
    'Cost, est.',
    '1.20 USD',
    'USD 5.00',
    '€5',
    '5 €',
    'EUR 5',
    'Estimated cost by agent',
    'Cost estimate',
    'Added budget',
    'Over budget',
    'budget over-run reached budget; ok (before 0.53)',
    '$5 (before 0.53)',
    'Ticket passed its budget\n(before 0.53)',
  ]) {
    expect(findings(bad), bad).not.toEqual([])
  }
  for (const good of [
    'Ticket passed its budget (before 0.53)',
    'At its budget (before 0.53)',
    'as well, for less (before 0.53)',
    '12.3k tokens',
    'Use: 2.1k tokens and 16 min per ticket done',
    'Tokens by agent',
  ]) {
    expect(findings(good), good).toEqual([])
  }
  // A screen of a plan's, a ticket's, or a run's own words is read for amounts alone.
  expect(findings('The budget of the cache, at a low cost, as estimated', false)).toEqual([])
  expect(findings('It cost $5', false)).not.toEqual([])
})
