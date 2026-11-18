import { renderToStaticMarkup } from 'react-dom/server'
import { describe, expect, it } from 'vitest'
import { DEFAULT_THEME, THEMES, findTheme, isThemeId } from '../lib/themes'
import { ThemeGalleryModal } from './theme-gallery'

describe('M25 40-theme catalog and gallery', () => {
  it('ships all 40 Direction A themes across the 3 locked groups', () => {
    expect(THEMES).toHaveLength(40)
    expect(THEMES.filter((t) => t.group === 'Product & AI-Native')).toHaveLength(11)
    expect(THEMES.filter((t) => t.group === 'shadcn/ui Official Presets')).toHaveLength(9)
    expect(THEMES.filter((t) => t.group === 'Dotfile & Editor Classics')).toHaveLength(20)
    for (const t of THEMES) {
      expect(isThemeId(t.id)).toBe(true)
      expect(findTheme(t.id).id).toBe(t.id)
    }
    expect(isThemeId(DEFAULT_THEME)).toBe(true)
    expect(findTheme('missing').id).toBe(DEFAULT_THEME)
    expect(isThemeId('nonexistent-theme')).toBe(false)
  })

  it('renders ThemeGalleryModal with all 40 swatches and zero inline HTML style attributes', () => {
    const html = renderToStaticMarkup(
      <ThemeGalleryModal
        open={true}
        theme="catppuccin"
        dark={true}
        onSelectTheme={() => {}}
        onToggleDark={() => {}}
        onClose={() => {}}
      />,
    )
    const buttons = html.match(/data-theme-choice="/g) ?? []
    expect(buttons).toHaveLength(40)
    expect(html).not.toMatch(/\sstyle="/i)
  })
})
