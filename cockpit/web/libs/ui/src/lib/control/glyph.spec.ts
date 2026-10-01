import { TestBed } from '@angular/core/testing'
import { Glyph } from './glyph'
import * as glyphs from './glyphs'

const ALL = Object.entries(glyphs).filter(([name]) => name.startsWith('GLYPH_')) as [string, glyphs.GlyphPaths][]

describe('Glyph', () => {
  it('has one export per glyph, so a page bundles only those it names', () => {
    expect(ALL.length).toBeGreaterThanOrEqual(24)
    for (const [name, paths] of ALL) {
      expect(name).toMatch(/^GLYPH_[A-Z_]+$/)
      expect(paths.length).toBeGreaterThan(0)
    }
  })

  it('draws one path per entry of the glyph and hides itself from assistive technology', async () => {
    for (const [name, paths] of ALL) {
      const fixture = TestBed.createComponent(Glyph)
      fixture.componentRef.setInput('paths', paths)
      await fixture.whenStable()
      const svg = fixture.nativeElement.querySelector('svg') as SVGElement
      expect(svg.getAttribute('aria-hidden'), name).toBe('true')
      expect([...svg.querySelectorAll('path')].map((path) => path.getAttribute('d')), name).toEqual([...paths])
    }
  })

  it('has no inline style attribute or script: the strict content security policy stays unchanged', async () => {
    const fixture = TestBed.createComponent(Glyph)
    fixture.componentRef.setInput('paths', glyphs.GLYPH_CHECK)
    await fixture.whenStable()
    expect(fixture.nativeElement.querySelector('[style], script')).toBeNull()
  })
})
