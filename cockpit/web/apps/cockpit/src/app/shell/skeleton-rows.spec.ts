import { TestBed } from '@angular/core/testing'
import { SkeletonRows } from './skeleton-rows'

describe('SkeletonRows', () => {
  it('draws six rows by default, hidden from assistive technology, and as many as asked', async () => {
    const fixture = TestBed.createComponent(SkeletonRows)
    await fixture.whenStable()
    const root = fixture.nativeElement as HTMLElement
    expect(root.querySelectorAll('.skeleton-row')).toHaveLength(6)
    expect(root.querySelector('.skeleton')?.getAttribute('aria-hidden')).toBe('true')
    fixture.componentRef.setInput('count', 3)
    await fixture.whenStable()
    expect(root.querySelectorAll('.skeleton-row')).toHaveLength(3)
  })
})
