import { chartConfiguration, ChartContext } from './chart-config'
import { BarsSpec, HorizontalBarsSpec, StackedBarsSpec, TimeSeriesSpec } from './chart-spec'
import type { ChartTheme } from './chart-theme'

const theme: ChartTheme = {
  line: 'rgb(1, 1, 1)',
  fill: 'rgb(2, 2, 2)',
  bar: 'rgb(3, 3, 3)',
  barHover: 'rgb(4, 4, 4)',
  barMuted: 'rgb(9, 9, 9)',
  barSoft: 'rgb(10, 10, 10)',
  grid: 'rgb(5, 5, 5)',
  tick: 'rgb(6, 6, 6)',
  tooltipBackground: 'rgb(7, 7, 7)',
  tooltipText: 'rgb(8, 8, 8)',
  font: 'Inter',
}
const context = (extra: Partial<ChartContext> = {}): ChartContext => ({ theme, reducedMotion: false, ...extra })

const series: TimeSeriesSpec = {
  kind: 'time-series',
  title: 'CPU',
  valueLabel: 'CPU %',
  unit: '%',
  max: 100,
  from: new Date(2026, 9, 1, 10, 0).getTime(),
  to: new Date(2026, 9, 1, 11, 0).getTime(),
  points: [
    { at: new Date(2026, 9, 1, 10, 0).getTime(), value: 20 },
    { at: new Date(2026, 9, 1, 10, 30).getTime(), value: null },
  ],
}
const days: BarsSpec = { kind: 'bars', title: 'Landed', valueLabel: 'Landed', bars: [{ label: 'Mon', value: 2 }, { label: 'Tue', value: 5 }] }
const stackedDays: StackedBarsSpec = {
  kind: 'stacked-bars',
  title: 'Finished',
  valueLabel: 'Tasks',
  series: [{ name: 'landed', tone: 'primary' }, { name: 'finished', tone: 'soft' }, { name: 'dropped', tone: 'muted' }],
  bars: [{ label: '09-30', values: [1, 2, 3] }, { label: '10-01', values: [0, 1] }],
}
const link = { path: '/worktrees', query: { q: 'age:<1d' } }
const buckets: HorizontalBarsSpec = { kind: 'horizontal-bars', title: 'Age', valueLabel: 'Worktrees', bars: [{ label: '< 1 d', value: 4, link }, { label: '1-7 d', value: 1, link }] }

// The chart-library types make every option optional; the tests read what the presets set.
type Loose = any // eslint-disable-line @typescript-eslint/no-explicit-any

describe('chartConfiguration', () => {
  it('themes every colour from the resolved tokens, and never from a literal', () => {
    for (const spec of [series, days, buckets, stackedDays]) {
      const text = JSON.stringify(chartConfiguration(spec, context()), (_key, value) => (typeof value === 'function' ? 'fn' : value))
      expect(text).toContain('rgb(5, 5, 5)')
      expect(text).toContain('rgb(6, 6, 6)')
      expect(text).toContain('rgb(7, 7, 7)')
      expect(text).not.toMatch(/#[0-9a-f]{3,6}"/i)
    }
  })

  it('has no animation for a viewer who asked for reduced motion, and a short one otherwise', () => {
    expect(chartConfiguration(series, context({ reducedMotion: true })).options?.animation).toBe(false)
    expect(chartConfiguration(days, context({ reducedMotion: true })).options?.animation).toBe(false)
    expect(chartConfiguration(series, context()).options?.animation).toEqual({ duration: 240 })
  })

  it('has no legend and no canvas-external element: tooltips are drawn on the canvas', () => {
    const options = chartConfiguration(days, context()).options as Loose
    expect(options.plugins.legend.display).toBe(false)
    expect(options.plugins.tooltip.external).toBeUndefined()
    expect(options.responsive).toBe(true)
    expect(options.maintainAspectRatio).toBe(false)
  })

  describe('the time series', () => {
    const config = chartConfiguration(series, context()) as Loose

    it('is a line over a fixed window, with the gap kept as an absent value', () => {
      expect(config.type).toBe('line')
      expect(config.data.datasets[0].data).toEqual([{ x: series.points[0].at, y: 20 }, { x: series.points[1].at, y: null }])
      expect(config.data.datasets[0].spanGaps).toBe(false)
      expect(config.options.scales.x).toMatchObject({ type: 'linear', min: series.from, max: series.to })
      expect(config.options.scales.y).toMatchObject({ min: 0, max: 100 })
    })

    it('labels its axis with clock times and values with the unit, and its tooltip likewise', () => {
      expect(config.options.scales.x.ticks.callback(series.from)).toBe('10:00')
      expect(config.options.scales.y.ticks.callback(40)).toBe('40%')
      expect(config.options.scales.y.ticks.callback(0.30000000000000004)).toBe('0.3%')
      const callbacks = config.options.plugins.tooltip.callbacks
      expect(callbacks.title([{ parsed: { x: series.from } }])).toBe('10:00')
      expect(callbacks.title([])).toBe('NaN:NaN')
      expect(callbacks.label({ parsed: { y: 41.26 } })).toBe('41.3%')
    })
  })

  it('has the bars by day as a vertical bar chart with whole-number counts', () => {
    const config = chartConfiguration(days, context()) as Loose
    expect(config.type).toBe('bar')
    expect(config.options.indexAxis).toBe('x')
    expect(config.data.labels).toEqual(['Mon', 'Tue'])
    expect(config.data.datasets[0].data).toEqual([2, 5])
    expect(config.options.scales.y.ticks.precision).toBe(0)
    expect(config.options.scales.y.ticks.callback(4)).toBe('4')
    expect(config.options.scales.x.ticks.autoSkip).toBe(true)
  })

  describe('the stacked bars', () => {
    const config = chartConfiguration(stackedDays, context()) as Loose

    it('stacks one dataset per series on both axes, each in the colour of its tone, with a missing value as zero', () => {
      expect(config.type).toBe('bar')
      expect(config.data.labels).toEqual(['09-30', '10-01'])
      expect(config.data.datasets.map((dataset: Loose) => [dataset.label, dataset.backgroundColor, dataset.data])).toEqual([
        ['landed', 'rgb(3, 3, 3)', [1, 0]],
        ['finished', 'rgb(10, 10, 10)', [2, 1]],
        ['dropped', 'rgb(9, 9, 9)', [3, 0]],
      ])
      expect(config.options.scales.x.stacked).toBe(true)
      expect(config.options.scales.y).toMatchObject({ stacked: true, beginAtZero: true })
      expect(config.options.scales.y.ticks.precision).toBe(0)
    })

    it('leaves the value axis to the library, unless the spec asks for it to fit: the tallest stack rounded up to an even number, two steps', () => {
      expect(config.options.scales.y.max).toBeUndefined()
      expect(config.options.scales.y.ticks.stepSize).toBeUndefined()
      const fitted = (bars: StackedBarsSpec['bars']) => (chartConfiguration({ ...stackedDays, bars, fitAxis: true }, context()) as Loose).options.scales.y
      expect(fitted(stackedDays.bars)).toMatchObject({ max: 6, ticks: { stepSize: 3 } })
      expect(fitted([{ label: 'a', values: [1, 2, 2] }])).toMatchObject({ max: 6, ticks: { stepSize: 3 } })
      expect(fitted([{ label: 'a', values: [3, 2] }])).toMatchObject({ max: 6, ticks: { stepSize: 3 } })
      expect(fitted([{ label: 'a', values: [1, 2] }])).toMatchObject({ max: 4, ticks: { stepSize: 2 } })
      expect(fitted([{ label: 'a', values: [0, 0] }, { label: 'b', values: [1] }])).toMatchObject({ max: 2, ticks: { stepSize: 1 } })
      expect(fitted([])).toMatchObject({ max: 2 })
    })

    it('shows all the series of a day in one tooltip, with their colours, and no legend of its own', () => {
      expect(config.options.interaction).toEqual({ mode: 'index', intersect: false })
      expect(config.options.plugins.tooltip.displayColors).toBe(true)
      expect(config.options.plugins.legend.display).toBe(false)
    })

    it('is never clickable', () => {
      expect(config.options.onClick).toBeUndefined()
    })
  })

  describe('the horizontal bars', () => {
    it('lays the buckets out on the vertical axis and reports a click on a bar through onSelect', () => {
      const onSelect = vi.fn()
      const config = chartConfiguration(buckets, context({ onSelect })) as Loose
      expect(config.options.indexAxis).toBe('y')
      expect(config.options.scales.y.ticks.autoSkip).toBe(false)
      config.options.onClick({}, [{ index: 1 }])
      expect(onSelect).toHaveBeenCalledWith(1)
      config.options.onClick({}, [])
      expect(onSelect).toHaveBeenCalledTimes(1)
    })

    it('shows a pointer over a clickable bar only', () => {
      const config = chartConfiguration(buckets, context()) as Loose
      const target = document.createElement('canvas')
      config.options.onHover({ native: { target } }, [{ index: 0 }])
      expect(target.style.cursor).toBe('pointer')
      config.options.onHover({ native: { target } }, [])
      expect(target.style.cursor).toBe('default')
      config.options.onHover({ native: null }, [])
    })

    it('is not clickable when no bucket carries a link, and a click without a listener does nothing', () => {
      const onSelect = vi.fn()
      const plain = chartConfiguration({ ...buckets, bars: [{ label: 'a', value: 1 }] }, context({ onSelect })) as Loose
      plain.options.onClick({}, [{ index: 0 }])
      expect(onSelect).not.toHaveBeenCalled()
      const target = document.createElement('canvas')
      plain.options.onHover({ native: { target } }, [{ index: 0 }])
      expect(target.style.cursor).toBe('default')
      ;(chartConfiguration(buckets, context()) as Loose).options.onClick({}, [{ index: 0 }])
    })

    it('is never clickable as bars by day', () => {
      const onSelect = vi.fn()
      ;(chartConfiguration(days, context({ onSelect })) as Loose).options.onClick({}, [{ index: 0 }])
      expect(onSelect).not.toHaveBeenCalled()
    })
  })
})
