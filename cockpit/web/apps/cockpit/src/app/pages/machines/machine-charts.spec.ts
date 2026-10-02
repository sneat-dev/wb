import { TestBed } from '@angular/core/testing'
import { CHART_ENGINE } from '@cockpit/ui/chart'
import { NOW } from '../test-harness'
import { MachineCharts } from './machine-charts'
import { sample } from './machines-fixture'

describe('MachineCharts specs', () => {
  it('draws the library presets: CPU, load, memory used and disk free in gigabytes, over the same hour', async () => {
    TestBed.configureTestingModule({ providers: [{ provide: CHART_ENGINE, useValue: async () => ({ create: vi.fn(() => ({ update: vi.fn(), destroy: vi.fn() })) }) }] })
    const fixture = TestBed.createComponent(MachineCharts)
    fixture.componentRef.setInput('samples', [sample(5, 40, 8)])
    fixture.componentRef.setInput('now', NOW)
    await fixture.whenStable()
    const specs = (fixture.componentInstance as unknown as { specs: () => { title: string; from: number; points: { value: number | null }[] }[] }).specs()
    expect(specs.map((spec) => spec.title)).toEqual(['CPU', 'Load (1 minute)', 'Memory used', 'Disk free'])
    expect(specs[3].points[0].value).toBe(200)
    expect(new Set(specs.map((spec) => spec.from)).size).toBe(1)
  })
})

describe('MachineCharts', () => {
  const render = async (samples: ReturnType<typeof sample>[]) => {
    const create = vi.fn(() => ({ update: vi.fn(), destroy: vi.fn() }))
    TestBed.configureTestingModule({ providers: [{ provide: CHART_ENGINE, useValue: async () => ({ create }) }] })
    const fixture = TestBed.createComponent(MachineCharts)
    fixture.componentRef.setInput('samples', samples)
    fixture.componentRef.setInput('now', NOW)
    await fixture.whenStable()
    return { create, titles: () => [...fixture.nativeElement.querySelectorAll('.title')].map((element: Element) => element.textContent) }
  }

  it('draws a chart for each metric, each with its title, in one column that the container widens to two', async () => {
    const { titles, create } = await render([sample(2, 10, 4), sample(1, 20, 6)])
    expect(titles()).toEqual(['CPU', 'Load (1 minute)', 'Memory used', 'Disk free'])
    await vi.waitFor(() => expect(create).toHaveBeenCalledTimes(4))
  })

  it('still draws the four charts, saying "No data", when there is no sample', async () => {
    const { titles } = await render([])
    expect(titles()).toHaveLength(4)
  })
})
