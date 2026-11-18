import { describe, expect, it } from 'vitest'
import html from '../dist/client/_shell.html?raw'

// The build prerenders one shell at "/", and crew-server sends it for every route of the UI. React
// does not correct an attribute while it hydrates, so a mark the shell holds stays on whatever page
// was opened by its address (0.2.1 marked All projects on every page). make check builds before it
// runs these tests.
describe('the shell every page loads', () => {
  it('marks no link as the page, since it is not the page of any one of them', () => {
    expect(html, 'the shell has the menu').toMatch(/<nav[\s>]/)
    expect(html).not.toMatch(/data-status="active"|aria-current/)
  })
})
