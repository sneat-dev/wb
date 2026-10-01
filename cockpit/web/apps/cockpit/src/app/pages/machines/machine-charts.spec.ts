import { TestBed } from '@angular/core/testing'
import { CHART_ENGINE } from '@cockpit/ui/chart'
import { NOW } from '../test-harness'
import { diskFreeSpec, MachineCharts, machineChartSpecs } from './machine-charts'
import { sample } from './machines-fixture'

const MINUTE = 60_000

describe('diskFreeSpec', () => {
  it('is the free disk in gigabytes over the last hour, with a gap where samples are missing or the disk is not reported', () => {
    const spec = diskFreeSpec([sample(50, 5), sample(49, 5), sample(48, 5), sample(47, 5), sample(10, 5), sample(9, 5, 8, { disk_total_bytes: 0 }), sample(8, 5, 8, { sampled_at: 'garbage' }), sample(120, 5)], NOW)
    expect(spec).toMatchObject({ kind: 'time-series', title: 'Disk free', unit: ' GB', from: NOW - 60 * MINUTE, to: NOW })
    expect(spec.points.map((point) => point.value)).toEqual([200, 200, 200, 200, null, 200])
    expect(spec.points[0].at).toBe(NOW - 50 * MINUTE)
  })
})

describe('machineChartSpecs', () => {
  it('is CPU, load, memory used and disk free, in that order, over the same hour', () => {
    const specs = machineChartSpecs([sample(5, 40, 8)], NOW)
    expect(specs.map((spec) => spec.title)).toEqual(['CPU', 'Load (1 minute)', 'Memory used', 'Disk free'])
    expect(specs.map((spec) => spec.points.map((point) => point.value))).toEqual([[40], [2], [50], [200]])
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
