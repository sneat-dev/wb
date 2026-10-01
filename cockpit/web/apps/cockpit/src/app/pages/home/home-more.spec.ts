import { TestBed } from '@angular/core/testing'
import { provideRouter } from '@angular/router'
import { CHART_ENGINE } from '@cockpit/ui/chart'
import { ClipboardWriter } from '@cockpit/ui/control'
import { FIXED_CLOCK, fleet, modelOf } from './home-testing'
import { HomeMore } from './home-more'

async function render(document = fleet(), dropped = 0) {
  const create = vi.fn(() => ({ update: vi.fn(), destroy: vi.fn() }))
  TestBed.resetTestingModule()
  TestBed.configureTestingModule({
    providers: [FIXED_CLOCK, provideRouter([]), { provide: CHART_ENGINE, useValue: async () => ({ create }) }, { provide: ClipboardWriter, useValue: { copy: vi.fn() } }],
  })
  const fixture = TestBed.createComponent(HomeMore)
  fixture.componentRef.setInput('model', modelOf(document))
  fixture.componentRef.setInput('dropped', dropped)
  await fixture.whenStable()
  // The charts are a lazy chunk of their own: wait until the place that keeps them has been replaced.
  await vi.waitFor(async () => {
    await fixture.whenStable()
    const root: HTMLElement = fixture.nativeElement
    if (root.querySelector('.home-lazy-slot') !== null) throw new Error('the charts have not arrived')
  })
  return { fixture, root: fixture.nativeElement as HTMLElement, create }
}

const headings = (root: HTMLElement) => [...root.querySelectorAll('h2')].map((heading) => (heading.textContent ?? '').replace(/\s+/g, ' ').trim())

describe('HomeMore', () => {
  it('shows Cleanup, Fleet health when something is wrong, and the throughput charts, in that order', async () => {
    const { root, create } = await render()
    expect(headings(root)).toEqual(['Cleanup', 'Fleet health 2', 'Throughput'])
    expect(root.querySelectorAll('app-chart')).toHaveLength(2)
    await vi.waitFor(() => expect(create).toHaveBeenCalledTimes(2))
  })

  it('shows no Fleet health for a fleet with nothing wrong, and the dropped entries when this page left some out', async () => {
    const calm = await render(fleet('healthy'))
    expect(headings(calm.root)).toEqual(['Cleanup', 'Throughput'])
    const dropped = await render(fleet('healthy'), 3)
    expect(headings(dropped.root)).toEqual(['Cleanup', 'Fleet health 1', 'Throughput'])
    expect(dropped.root.textContent).toContain('3 entries of the fleet document were invalid and left out')
  })

  it('says why there are no charts when the daemon sent no throughput block', async () => {
    const { root } = await render(fleet('no-throughput'))
    expect(root.querySelectorAll('app-chart')).toHaveLength(0)
    expect(root.querySelector('.home-calm')?.textContent).toContain('No charts: the daemon reports no throughput')
  })
})
