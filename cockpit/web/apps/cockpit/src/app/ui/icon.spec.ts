import { TestBed } from '@angular/core/testing'
import { Icon } from './icon'

describe('Icon', () => {
  it('draws the paths of its name as an inline SVG hidden from assistive technology', async () => {
    const fixture = TestBed.createComponent(Icon)
    fixture.componentRef.setInput('name', 'search')
    await fixture.whenStable()
    const svg = fixture.nativeElement.querySelector('svg') as SVGElement
    expect(svg.getAttribute('aria-hidden')).toBe('true')
    expect(svg.querySelectorAll('path')).toHaveLength(2)
  })

  it('follows the name', async () => {
    const fixture = TestBed.createComponent(Icon)
    fixture.componentRef.setInput('name', 'plus')
    await fixture.whenStable()
    expect(fixture.nativeElement.querySelectorAll('path')).toHaveLength(1)
    fixture.componentRef.setInput('name', 'folder')
    await fixture.whenStable()
    expect(fixture.nativeElement.querySelector('path').getAttribute('d')).toContain('M3 7')
  })
})
