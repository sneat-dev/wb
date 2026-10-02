import { TestBed } from '@angular/core/testing'
import { provideRouter } from '@angular/router'
import { ClipboardWriter } from '@cockpit/ui/control'
import { FIXED_CLOCK, fleet, modelOf } from './home-testing'
import { HomeMore } from './home-more'

async function render(document = fleet(), dropped = 0) {
  TestBed.resetTestingModule()
  TestBed.configureTestingModule({ providers: [FIXED_CLOCK, provideRouter([]), { provide: ClipboardWriter, useValue: { copy: vi.fn() } }] })
  const fixture = TestBed.createComponent(HomeMore)
  fixture.componentRef.setInput('model', modelOf(document))
  fixture.componentRef.setInput('dropped', dropped)
  await fixture.whenStable()
  return { fixture, root: fixture.nativeElement as HTMLElement }
}

const headings = (root: HTMLElement) => [...root.querySelectorAll('h2')].map((heading) => (heading.textContent ?? '').replace(/\s+/g, ' ').trim())

describe('HomeMore', () => {
  it('shows Cleanup and Fleet health when something is wrong, and no Throughput: that is the first section of Home', async () => {
    const { root } = await render()
    expect(headings(root)).toEqual(['Cleanup', 'Fleet health 2'])
    expect(root.querySelector('app-throughput')).toBeNull()
  })

  it('shows no Fleet health for a fleet with nothing wrong, and the dropped entries when this page left some out', async () => {
    const calm = await render(fleet('healthy'))
    expect(headings(calm.root)).toEqual(['Cleanup'])
    const dropped = await render(fleet('healthy'), 3)
    expect(headings(dropped.root)).toEqual(['Cleanup', 'Fleet health 1'])
    expect(dropped.root.textContent).toContain('3 entries of the fleet document were invalid and left out')
  })
})
