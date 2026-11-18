import { renderToStaticMarkup } from 'react-dom/server'
import { describe, expect, it } from 'vitest'
import { ProjectEndView } from './projectend'

const noop = () => {}

describe('ProjectEndView', () => {
  it('shows the Protected toggle off by default and enables Archive and Delete when name matches', () => {
    const html = renderToStaticMarkup(
      <ProjectEndView
        project="shop"
        isProtected={false}
        isLocked={false}
        name="shop"
        busy={false}
        error=""
        onNameChange={noop}
        onToggleProtected={noop}
        onArchive={noop}
        onExport={noop}
        onDelete={noop}
      />,
    )
    expect(html).toContain('data-protected="off"')
    expect(html).toContain('aria-label="Protected: off"')
    expect(html).toContain('Off by default.')
    expect(html).not.toContain('Protected · locked')
  })

  it('shows the Protected bubble and disables Archive and Delete when Protected is on', () => {
    const html = renderToStaticMarkup(
      <ProjectEndView
        project="shop"
        isProtected={true}
        isLocked={false}
        name="shop"
        busy={false}
        error=""
        onNameChange={noop}
        onToggleProtected={noop}
        onArchive={noop}
        onExport={noop}
        onDelete={noop}
      />,
    )
    expect(html).toContain('data-protected="on"')
    expect(html).toContain('aria-label="Protected: on"')
    expect(html).toContain('switch Protected off before archiving or deleting it')
    expect(html).toMatch(/<button[^>]*disabled=""[^>]*>Archive<\/button>/)
    expect(html).toMatch(/<button[^>]*disabled=""[^>]*>Delete shop<\/button>/)
  })

  it("locks Protected on for Crew's own project with no toggle button", () => {
    const html = renderToStaticMarkup(
      <ProjectEndView
        project="crew"
        isProtected={true}
        isLocked={true}
        name="crew"
        busy={false}
        error=""
        onNameChange={noop}
        onToggleProtected={noop}
        onArchive={noop}
        onExport={noop}
        onDelete={noop}
      />,
    )
    expect(html).toContain('data-protected="locked"')
    expect(html).toContain('Protected · locked')
    expect(html).toContain("Crew&#x27;s own project is always protected")
    expect(html).not.toContain('aria-label="Protected:')
    expect(html).toMatch(/<button[^>]*disabled=""[^>]*>Archive<\/button>/)
    expect(html).toMatch(/<button[^>]*disabled=""[^>]*>Delete crew<\/button>/)
  })
})
