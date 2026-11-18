import { describe, expect, it } from 'vitest'
import { cut, isJudgePurpose, modeName, networkText, plainText, plainWait, purposeText } from './words'

// The network modes as the screens name them (design 13.6): the run view and Settings say what a
// sandbox in each reaches, and a sandbox from before the modes had the internet.
describe('networkText', () => {
  it('names each mode and what it reaches', () => {
    expect(networkText('internet')).toBe('Internet. Reaches the internet. This machine, the local network, and other sandboxes are blocked.')
    expect(networkText('model_only')).toBe("Model only. Reaches the agent's model service and Crew, and nothing else. It resolves no name itself.")
    expect(networkText('lan')).toBe('LAN. Reaches the internet and the local network. You set this on purpose.')
  })
  it('takes a sandbox with no mode for one with the internet', () => {
    expect(networkText(undefined)).toBe(networkText('internet'))
    expect(networkText('')).toBe(networkText('internet'))
  })
  it('names a mode it does not know as it is', () => {
    expect(networkText('open')).toBe('open.')
    expect(modeName.model_only).toBe('Model only')
  })
})

describe('plainWait', () => {
  it('knows the waits that need nobody, as the pipeline writes them', () => {
    expect(plainWait('its analysis waits for a lead slot of agy')).toBe(true)
    expect(plainWait('Build: waits for room on the machine for a large sandbox')).toBe(true)
    expect(plainWait("Crew's checks: waits for room on the machine for a large sandbox")).toBe(true)
    expect(plainWait('Build: agy/gemini is close to its limit, and one run starts on it at a time; blog has its turn first')).toBe(true)
    expect(plainWait("Build: waits for SF-3, which fixes the project's setup: it fails on main")).toBe(true)
    expect(plainWait("Crew's checks: waits while Crew runs the project's setup on main: it failed twice in a row")).toBe(true)
  })
  it('leaves what needs the operator to the operator', () => {
    expect(plainWait('Crew could not run its checks on 0123456789ab three times: the sandbox did not start')).toBe(false)
    expect(plainWait('main has its branch already: there is nothing to merge')).toBe(false)
  })
})

// A long line of Latest signals is cut, the whole text on hover (dave, 0.2.6: the improver's note filled
// ten lines of the table).
describe('cut', () => {
  it('keeps a short text as it is', () => {
    expect(cut('The tests failed', 200)).toBe('The tests failed')
    expect(cut('x'.repeat(200), 200)).toBe('x'.repeat(200))
  })
  it('cuts a long one by its characters, with an ellipsis', () => {
    expect(cut('x'.repeat(201), 200)).toBe('x'.repeat(200) + '…')
    expect(cut('é'.repeat(150) + '😀'.repeat(100), 200)).toBe('é'.repeat(150) + '😀'.repeat(50) + '…')
    expect(cut('word '.repeat(60), 200)).toBe('word '.repeat(40).trimEnd() + '…')
  })
})

describe('purposeText and isJudgePurpose', () => {
  it('names every check-run purpose and marks judge and stage_judge as judge answers', () => {
    for (const p of ['base', 'exit', 'test', 'merge', 'part', 'join', 'judge', 'e2e', 'stage', 'stage_judge', 'outside']) {
      expect(purposeText[p]).toBeTruthy()
    }
    expect(isJudgePurpose('judge')).toBe(true)
    expect(isJudgePurpose('stage_judge')).toBe(true)
    expect(isJudgePurpose('exit')).toBe(false)
    expect(isJudgePurpose('stage')).toBe(false)
  })
})

describe('plainText', () => {
  it('strips file:// markdown links written by the CLI while keeping the label and other text', () => {
    expect(plainText('Updated [README.md](file:///home/agent/work/README.md) and [`table.go`](file:///home/agent/work/internal/format/table.go#L1-L40).')).toBe(
      'Updated README.md and `table.go`.',
    )
    expect(plainText('Keep [docs](https://example.com/docs) and [array] intact.')).toBe('Keep [docs](https://example.com/docs) and [array] intact.')
  })
})
