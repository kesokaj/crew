import { renderToStaticMarkup } from 'react-dom/server'
import { describe, expect, it } from 'vitest'
import { CLIModel, OmittedCommits } from './runs'
import { Brackets, Field, machineAPI, SettingControl, type Settings } from './settingfield'
import { formatPartSize, initialOpenParts, PromptView } from './prompt'
import { stripBackticks } from './activity'
import { formatCardModels } from '../routes/index'

// A ticket's Git part keeps its newest 1,000 commits and counts the rest (6.1, since 1.09): the page
// says how many it left out, and says nothing when it left out none.
describe("a ticket's Git part says what it left out", () => {
  it('counts the older commits it does not list', () => {
    expect(renderToStaticMarkup(<OmittedCommits n={300} />)).toContain('and 300 older commits, not listed here')
  })
  it('says nothing when none is left out', () => {
    expect(renderToStaticMarkup(<OmittedCommits n={0} />)).toBe('')
    expect(renderToStaticMarkup(<OmittedCommits />)).toBe('')
  })
})

describe('Brackets and CLIModel', () => {
  it('renders bracket fields and add-bracket controls on /settings', () => {
    const s: Settings = {
      api: machineAPI,
      doc: { agents: { brackets: { pro: ['agy/claude-opus-5-5', 'agy/gemini-4-argon'], flash: ['agy/gemini-3.8-flash'] } } },
      schema: null,
      problems: {},
      error: '',
      saved: 0,
      session: null,
      save: async () => {},
      change: async () => {},
      putBack: async () => {},
    }
    const html = renderToStaticMarkup(<Brackets s={s} />)
    expect(html).toContain('Bracket pro')
    expect(html).toContain('Bracket flash')
    expect(html).toContain('Add bracket')
  })

  it('keeps CLIModel chips on one line', () => {
    expect(renderToStaticMarkup(<CLIModel r={{ cli: 'agy', model: 'gemini-3.8-flash', effort: 'high' }} />)).toContain('whitespace-nowrap')
  })
})

describe('PromptView progressive disclosure and SettingControl enum ergonomics', () => {
  it('opens only ticket/Task part by default or first part when none matches, with Expand all and size badges', () => {
    const parts = [
      { part: 'stage', source: 'baseline/stages/build.md', text: 'A'.repeat(2048) },
      { part: 'ticket', source: 'ticket WC-1', text: 'Do the task.' },
    ]
    expect(initialOpenParts(parts)).toEqual({ 0: false, 1: true })
    expect(initialOpenParts([{ part: 'stage', source: 's', text: 'x' }, { part: 'agent', source: 'a', text: 'y' }])).toEqual({
      0: true,
      1: false,
    })
    expect(formatPartSize('hello')).toBe('5 chars')
    expect(formatPartSize('A'.repeat(2048))).toBe('2.0 KB')
    const html = renderToStaticMarkup(<PromptView prompt="full" parts={parts} />)
    expect(html).toContain('Expand all (2)')
    expect(html).toContain('2.0 KB')
    expect(html).toContain('data-part="stage" data-open="false"')
    expect(html).toContain('data-part="ticket" data-open="true"')
  })

  it('renders segmented pill buttons for <=4 choices, <select> for >4 choices, and compact ↺ History button', () => {
    const s: Settings = {
      api: machineAPI,
      doc: { mode: 'a', big: 'one' },
      schema: {
        enums: {
          mode: ['a', 'b', 'c'],
          big: ['one', 'two', 'three', 'four', 'five'],
        },
        floats: [],
        fixed: [],
        nullable: {},
      },
      problems: {},
      error: '',
      saved: 0,
      session: null,
      save: async () => {},
      change: async () => {},
      putBack: async () => {},
    }
    const seg = renderToStaticMarkup(<Field s={s} path="mode" label="Mode" />)
    expect(seg).toContain('role="group"')
    expect(seg).toContain('aria-label="History"')
    expect(seg).toContain('↺')

    const sel = renderToStaticMarkup(
      <SettingControl s={s} path="big" value="one" choices={['one', 'two', 'three', 'four', 'five']} label="Big enum" />,
    )
    expect(sel).toContain('<select')
    expect(sel).toContain('aria-label="Big enum"')
  })

  it('caps project card models at 2 + extra count and strips raw Markdown backticks', () => {
    expect(formatCardModels([])).toEqual({ shown: 'no run yet', extra: 0 })
    expect(formatCardModels(['m1', 'm2'])).toEqual({ shown: 'm1 + m2', extra: 0 })
    expect(formatCardModels(['m1', 'm2', 'm3', 'm4'])).toEqual({ shown: 'm1 + m2', extra: 2 })
    expect(stripBackticks('`wordcount` runs `go test ./...`')).toBe('wordcount runs go test ./...')
  })
})
