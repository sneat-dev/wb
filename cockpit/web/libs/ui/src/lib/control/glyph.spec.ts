import { TestBed } from '@angular/core/testing'
import { GLYPHS, Glyph, GlyphName } from './glyph'

describe('Glyph', () => {
  it('draws one path per entry of the glyph and hides itself from assistive technology', async () => {
    for (const name of Object.keys(GLYPHS) as GlyphName[]) {
      const fixture = TestBed.createComponent(Glyph)
      fixture.componentRef.setInput('name', name)
      await fixture.whenStable()
      const svg = fixture.nativeElement.querySelector('svg') as SVGElement
      expect(svg.getAttribute('aria-hidden')).toBe('true')
      expect(svg.querySelectorAll('path')).toHaveLength(GLYPHS[name].length)
      expect([...svg.querySelectorAll('path')].map((path) => path.getAttribute('d'))).toEqual([...GLYPHS[name]])
    }
  })

  it('has no inline style attribute or script: the strict content security policy stays unchanged', async () => {
    const fixture = TestBed.createComponent(Glyph)
    fixture.componentRef.setInput('name', 'check')
    await fixture.whenStable()
    expect(fixture.nativeElement.querySelector('[style], script')).toBeNull()
  })
})
