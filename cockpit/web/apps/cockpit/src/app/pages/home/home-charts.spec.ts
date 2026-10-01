import { TestBed } from '@angular/core/testing'
import { buildThroughput } from '@cockpit/fleet-data/home-details'
import { CHART_ENGINE } from '@cockpit/ui/chart'
import { fleet, modelOf } from './home-testing'
import { HomeCharts, durationCaption, seriesNames, throughputSpecs } from './home-charts'

const series = (document = fleet()) => buildThroughput(modelOf(document))!

async function render(throughput = series()) {
  const create = vi.fn(() => ({ update: vi.fn(), destroy: vi.fn() }))
  TestBed.resetTestingModule()
  TestBed.configureTestingModule({ providers: [{ provide: CHART_ENGINE, useValue: async () => ({ create }) }] })
  const fixture = TestBed.createComponent(HomeCharts)
  fixture.componentRef.setInput('series', throughput)
  await fixture.whenStable()
  const root: HTMLElement = fixture.nativeElement
  return { fixture, root, create }
}

const text = (element: Element | null | undefined) => (element?.textContent ?? '').replace(/\s+/g, ' ').trim()

describe('durationCaption', () => {
  it('writes the median and the 90th percentile, whichever the daemon reported', () => {
    expect(durationCaption({ medianSeconds: 15 * 60, p90Seconds: 15 * 3600 })).toBe('median 15 min · p90 15 h')
    expect(durationCaption({ medianSeconds: 90 })).toBe('median 1 min')
    expect(durationCaption({ p90Seconds: 7200 })).toBe('p90 2 h')
    expect(durationCaption({})).toBeUndefined()
  })
})

describe('throughputSpecs', () => {
  it('stacks finished and dropped per day over the window, and ranks the slowest tasks in hours', () => {
    const specs = throughputSpecs(series())
    expect(specs.perDay).toMatchObject({
      kind: 'stacked-bars',
      title: 'Finished per day, last 30 days',
      series: [
        { name: 'finished', tone: 'primary' },
        { name: 'dropped', tone: 'muted' },
      ],
    })
    expect(specs.perDay.bars).toHaveLength(30)
    expect(specs.perDay.bars[0].label).toMatch(/^\d\d-\d\d$/)
    expect(specs.perDay.bars.every((bar) => bar.values.length === 2)).toBe(true)
    expect(specs.slowest).toMatchObject({ kind: 'horizontal-bars', title: 'Time to finish' })
    expect(specs.slowest.bars.map((bar) => [bar.label, bar.value])).toEqual([
      ['migrate-auth', 15],
      ['rewrite-index', 9],
      ['bump-deps', 4],
      ['fix-ci-race', 1.6],
      ['add-search', 0.7],
    ])
    expect(specs.slowest.bars.every((bar) => !('link' in bar))).toBe(true)
  })

  it('splits finished work in landed and the rest only when the daemon counted landed work', () => {
    const throughput = series()
    expect(seriesNames(throughput)).toEqual(['finished', 'dropped'])
    const landed = { ...throughput, hasLanded: true, perDay: throughput.perDay.map((day) => ({ ...day, landed: Math.min(day.finished, 1) })) }
    expect(seriesNames(landed)).toEqual(['landed', 'finished', 'dropped'])
    const specs = throughputSpecs(landed)
    expect(specs.perDay.series).toEqual([
      { name: 'landed', tone: 'primary' },
      { name: 'finished', tone: 'soft' },
      { name: 'dropped', tone: 'muted' },
    ])
    expect(specs.perDay.bars.find((bar) => bar.values[0] + bar.values[1] > 1)?.values.length).toBe(3)
    const day = landed.perDay.find((entry) => entry.finished > 1)!
    expect(specs.perDay.bars.find((bar) => bar.label === day.date.slice(5))?.values).toEqual([1, day.finished - 1, day.dropped])
  })
})

describe('HomeCharts', () => {
  it('shows "Finished per day" with its text legend and "Time to finish" with the median and p90, each with its data table', async () => {
    const { root } = await render()
    expect(root.querySelectorAll('app-chart')).toHaveLength(2)
    expect([...root.querySelectorAll('.title')].map((title) => text(title))).toEqual(['Finished per day, last 30 days', 'Time to finish'])
    const legends = [...root.querySelectorAll('.chart-legend')].map((legend) => text(legend))
    expect(legends).toEqual(['finished dropped', 'median 15 min · p90 15 h'])
    expect(root.querySelectorAll('.swatch.primary, .swatch.muted')).toHaveLength(2)
    expect(root.querySelectorAll('.data table')).toHaveLength(2)
    expect(root.querySelectorAll('canvas')).toHaveLength(2)
    expect(root.querySelectorAll('.data button')).toHaveLength(0)
  })

  it('says when the daemon capped its scan, and has no caption when no task finished', async () => {
    const capped = { ...series(), capped: true, medianSeconds: undefined, p90Seconds: undefined, slowest: [] }
    const { root } = await render(capped)
    const legends = [...root.querySelectorAll('.chart-legend')].map((legend) => text(legend))
    expect(legends).toEqual(['finished dropped', 'The daemon capped its scan: older sealed tasks may be missing.'])
  })

  it('says "No data" for a window with nothing finished or dropped', async () => {
    const empty = { ...series(fleet('only-dropped')), perDay: series().perDay.map((day) => ({ ...day, finished: 0, dropped: 0, landed: 0 })) }
    const { root } = await render(empty)
    expect([...root.querySelectorAll('.overlay')].map((overlay) => text(overlay))).toContain('No data')
  })

  it('draws days with only dropped work', async () => {
    const { root } = await render(series(fleet('only-dropped')))
    const [perDay, slowest] = [...root.querySelectorAll('app-chart')]
    expect(perDay.querySelector('.overlay')).toBeNull()
    expect(text(slowest.querySelector('.overlay'))).toBe('No data')
    expect([...root.querySelectorAll('.chart-legend')].map((legend) => text(legend))[0]).toBe('finished dropped')
  })
})
