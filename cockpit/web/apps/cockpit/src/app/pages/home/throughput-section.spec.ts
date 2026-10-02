import { TestBed } from '@angular/core/testing'
import { provideRouter } from '@angular/router'
import { CHART_ENGINE } from '@cockpit/ui/chart'
import { FIXED_CLOCK, fleet, modelOf } from './home-testing'
import { ThroughputSection, hasThroughput, throughputOnTop } from './throughput-section'

async function render(document = fleet()) {
  TestBed.resetTestingModule()
  TestBed.configureTestingModule({ providers: [FIXED_CLOCK, provideRouter([]), { provide: CHART_ENGINE, useValue: async () => ({ create: () => ({ update: vi.fn(), destroy: vi.fn() }) }) }] })
  const fixture = TestBed.createComponent(ThroughputSection)
  fixture.componentRef.setInput('model', modelOf(document))
  await fixture.whenStable()
  return { fixture, root: fixture.nativeElement as HTMLElement }
}

describe('throughputOnTop', () => {
  it('is true only for a document with a throughput block once the first scan is done', () => {
    expect(hasThroughput(modelOf(fleet()))).toBe(true)
    expect(hasThroughput(modelOf(fleet('no-throughput')))).toBe(false)
    expect(throughputOnTop(modelOf(fleet()), false)).toBe(true)
    expect(throughputOnTop(modelOf(fleet()), true)).toBe(false)
    expect(throughputOnTop(modelOf(fleet('no-throughput')), false)).toBe(false)
  })
})

describe('ThroughputSection', () => {
  it('shows the heading and a slot that keeps the charts place, then the two charts from their lazy chunk', async () => {
    const { fixture, root } = await render()
    expect(root.querySelector('h2')?.textContent?.trim()).toBe('Throughput')
    expect(root.querySelector('section')?.getAttribute('aria-labelledby')).toBe(root.querySelector('h2')?.id)
    expect(root.querySelector('.throughput-slot')).not.toBeNull()
    await vi.waitFor(async () => {
      await fixture.whenStable()
      expect(root.querySelectorAll('.throughput-slot app-chart')).toHaveLength(2)
    })
    expect(root.querySelector('.home-calm')).toBeNull()
  })

  it('says why there are no charts, in the same section, when the document has no throughput block', async () => {
    const { root } = await render(fleet('no-throughput'))
    expect(root.querySelector('h2')?.textContent?.trim()).toBe('Throughput')
    expect(root.querySelector('.throughput-slot')).toBeNull()
    expect(root.querySelector('.home-calm')?.textContent).toContain('No charts: the daemon reports no throughput')
  })
})
