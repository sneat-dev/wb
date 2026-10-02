import { TestBed } from '@angular/core/testing'
import { CHART_ENGINE } from '@cockpit/ui/chart'
import { fleet, modelOf } from './home-testing'
import { ThroughputCharts } from './throughput-charts'

async function render(document = fleet()) {
  TestBed.resetTestingModule()
  TestBed.configureTestingModule({ providers: [{ provide: CHART_ENGINE, useValue: async () => ({ create: () => ({ update: vi.fn(), destroy: vi.fn() }) }) }] })
  const fixture = TestBed.createComponent(ThroughputCharts)
  fixture.componentRef.setInput('model', modelOf(document))
  await fixture.whenStable()
  return fixture.nativeElement as HTMLElement
}

describe('ThroughputCharts', () => {
  it('draws the two charts from the throughput block of the model', async () => {
    const root = await render()
    expect(root.querySelectorAll('app-chart')).toHaveLength(2)
  })

  it('draws nothing for a model without a throughput block', async () => {
    const root = await render(fleet('no-throughput'))
    expect(root.querySelector('app-home-charts')).toBeNull()
  })
})
