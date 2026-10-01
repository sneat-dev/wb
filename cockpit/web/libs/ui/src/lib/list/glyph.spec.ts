import { TestBed } from '@angular/core/testing'
import { Glyph } from './glyph'

describe('Glyph', () => {
  it('draws the paths of the named glyph, hidden from assistive technology', async () => {
    const fixture = TestBed.createComponent(Glyph)
    fixture.componentRef.setInput('name', 'copy')
    await fixture.whenStable()
    const svg = fixture.nativeElement.querySelector('svg') as SVGElement
    expect(svg.getAttribute('aria-hidden')).toBe('true')
    expect(svg.querySelectorAll('path')).toHaveLength(2)
    fixture.componentRef.setInput('name', 'check')
    await fixture.whenStable()
    expect(svg.querySelectorAll('path')).toHaveLength(1)
  })
})
