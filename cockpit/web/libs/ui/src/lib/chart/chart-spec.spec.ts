import type { MetricsSample } from '@cockpit/fleet-data'
import { TABLE_FULL_LIMIT, TABLE_STEP, StackedBarsSpec, TimeSeriesSpec, chartSummary, cleanSpec, hasData, chartTable, clockTime, formatValue, machineMetricSpecs, withGaps } from './chart-spec'

const NOW = Date.parse('2026-10-01T12:00:00Z')
const MIN = 60_000

function sample(minutesAgo: number, extra: Partial<MetricsSample> = {}): MetricsSample {
  return {
    cpu_percent: 40,
    load1: 1.25,
    memory_used_bytes: 8,
    memory_total_bytes: 16,
    disk_free_bytes: 25,
    disk_total_bytes: 100,
    sampled_at: new Date(NOW - minutesAgo * MIN).toISOString(),
    ...extra,
  }
}

describe('chart specs', () => {
  it('formats a clock time and a value with at most one decimal', () => {
    expect(clockTime(new Date(2026, 9, 1, 7, 5).getTime())).toBe('07:05')
    expect(formatValue(41.26, '%')).toBe('41.3%')
    expect(formatValue(2, '')).toBe('2')
  })

  describe('machineMetricSpecs', () => {
    it('makes the four charts of the last hour: CPU, load, memory used and free disk', () => {
      const specs = machineMetricSpecs([sample(30), sample(20), sample(10)], NOW)
      expect(specs.map((spec) => [spec.title, spec.unit, spec.max])).toEqual([
        ['CPU', '%', 100],
        ['Load (1 minute)', '', undefined],
        ['Memory used', '%', 100],
        ['Disk free', ' GB', undefined],
      ])
      expect(specs.map((spec) => spec.points.map((point) => point.value))).toEqual([[40, 40, 40], [1.25, 1.25, 1.25], [50, 50, 50], [25 / 2 ** 30, 25 / 2 ** 30, 25 / 2 ** 30]])
      expect(specs[0]).toMatchObject({ kind: 'time-series', from: NOW - 60 * MIN, to: NOW, valueLabel: 'CPU %' })
    })

    it('drops samples outside the hour, unreadable times and readings the machine did not report', () => {
      const specs = machineMetricSpecs([sample(90), sample(30), sample(5, { sampled_at: 'garbage' }), sample(10, { memory_total_bytes: 0, disk_total_bytes: 0 })], NOW)
      expect(specs[0].points).toHaveLength(2)
      expect(specs[2].points.map((point) => point.value)).toEqual([50])
      expect(specs[3].points.map((point) => point.value)).toEqual([25 / 2 ** 30])
    })

    it('leaves out a measurement the daemon omitted from a sample, instead of drawing a zero', () => {
      const { cpu_percent: _cpu, ...noCpu } = sample(30)
      const specs = machineMetricSpecs([noCpu, sample(20), { sampled_at: sample(10).sampled_at }], NOW)
      expect(specs.map((spec) => spec.points.length)).toEqual([1, 2, 2, 2])
      expect(machineMetricSpecs([{ sampled_at: sample(10).sampled_at }], NOW).map((spec) => spec.points)).toEqual([[], [], [], []])
    })

    it('breaks the line where samples are absent', () => {
      const specs = machineMetricSpecs([sample(55), sample(54), sample(53), sample(30), sample(29)], NOW)
      const values = specs[0].points.map((point) => point.value)
      expect(values).toEqual([40, 40, 40, null, 40, 40])
      const gap = specs[0].points[3]
      expect(gap.at).toBe(NOW - 53 * MIN + MIN)
    })

    it('gives empty points for a machine with no samples', () => {
      expect(machineMetricSpecs([], NOW).every((spec) => spec.points.length === 0)).toBe(true)
    })
  })

  describe('withGaps', () => {
    it('sorts the points and joins readings an ordinary interval apart', () => {
      const points = withGaps([{ at: 3 * MIN, value: 3 }, { at: MIN, value: 1 }, { at: 2 * MIN, value: 2 }], 0, 10 * MIN)
      expect(points.map((point) => point.value)).toEqual([1, 2, 3])
    })

    it('assumes a minute for fewer than two points, and does not break after the last one', () => {
      expect(withGaps([{ at: MIN, value: 1 }], 0, 10 * MIN)).toEqual([{ at: MIN, value: 1 }])
      expect(withGaps([], 0, 10 * MIN)).toEqual([])
    })
  })

  describe('the text alternative', () => {
    const series: TimeSeriesSpec = {
      kind: 'time-series',
      title: 'CPU',
      valueLabel: 'CPU %',
      unit: '%',
      from: 0,
      to: 1,
      points: [
        { at: new Date(2026, 9, 1, 10, 0).getTime(), value: 10 },
        { at: new Date(2026, 9, 1, 10, 1).getTime(), value: null },
        { at: new Date(2026, 9, 1, 10, 2).getTime(), value: 30.04 },
      ],
    }

    it('lists every point of a time series, saying "no sample" for a gap', () => {
      expect(chartTable(series)).toEqual({
        caption: 'CPU',
        columns: ['Time', 'CPU %'],
        rows: [
          { label: '10:00', value: '10%' },
          { label: '10:01', value: 'no sample' },
          { label: '10:02', value: '30%' },
        ],
      })
      expect(chartSummary(series)).toBe('CPU: latest 30%, from 10% to 30% over 2 samples')
      expect(chartSummary({ ...series, points: [{ at: 0, value: null }] })).toBe('CPU: no samples in the last hour')
    })

    it('lists the bars of both bar charts, with the link of a clickable bucket', () => {
      const link = { path: '/worktrees', query: { q: 'age:<1d' } }
      const days = chartTable({ kind: 'bars', title: 'Landed per day', valueLabel: 'Landed', bars: [{ label: '2026-09-30', value: 3 }] })
      expect(days).toEqual({ caption: 'Landed per day', columns: ['Day', 'Landed'], rows: [{ label: '2026-09-30', value: '3' }] })
      const buckets = chartTable({ kind: 'horizontal-bars', title: 'Worktree age', valueLabel: 'Worktrees', bars: [{ label: '< 1 d', value: 4, link }, { label: '1-7 d', value: 0 }] })
      expect(buckets.columns).toEqual(['Bucket', 'Worktrees'])
      expect(buckets.rows).toEqual([{ label: '< 1 d', value: '4', link }, { label: '1-7 d', value: '0', link: undefined }])
      expect(chartSummary({ kind: 'bars', title: 'Landed per day', valueLabel: 'x', bars: [{ label: 'a', value: 3 }, { label: 'b', value: 2 }] })).toBe('Landed per day: 2 days, 5 in all')
      expect(chartSummary({ kind: 'horizontal-bars', title: 'Age', valueLabel: 'x', bars: [{ label: 'a', value: 3 }] })).toBe('Age: 1 buckets, 3 in all')
      expect(chartSummary({ kind: 'bars', title: 'Landed per day', valueLabel: 'x', bars: [] })).toBe('Landed per day: no data')
    })

    it('lists each day of stacked bars with the value of every series, and sums each series in its summary', () => {
      const stacked: StackedBarsSpec = { kind: 'stacked-bars', title: 'Finished per day', valueLabel: 'Tasks per day', series: [{ name: 'finished', tone: 'primary' }, { name: 'dropped', tone: 'muted' }], bars: [{ label: '09-30', values: [3, 1] }, { label: '10-01', values: [2] }] }
      expect(chartTable(stacked)).toEqual({
        caption: 'Finished per day',
        columns: ['Day', 'Tasks per day'],
        rows: [{ label: '09-30', value: '3 finished, 1 dropped' }, { label: '10-01', value: '2 finished, 0 dropped' }],
      })
      expect(chartSummary(stacked)).toBe('Finished per day: 2 days, 5 finished, 1 dropped')
      expect(chartSummary({ ...stacked, bars: [{ label: 'x', values: [1] }] })).toBe('Finished per day: 1 days, 1 finished, 0 dropped')
    })
  })

  describe('cleanSpec and hasData', () => {
    it('turns a value that is not a finite number into a gap, drops a point with no time and a bar with no number', () => {
      const series = cleanSpec<TimeSeriesSpec>({ kind: 'time-series', title: 't', valueLabel: 'v', unit: '', from: 0, to: 1, points: [{ at: 0, value: Number.NaN }, { at: 1, value: Infinity }, { at: 2, value: null }, { at: Number.NaN, value: 1 }, { at: 3, value: 3 }] })
      expect(series.points).toEqual([{ at: 0, value: null }, { at: 1, value: null }, { at: 2, value: null }, { at: 3, value: 3 }])
      expect(cleanSpec({ kind: 'bars', title: 't', valueLabel: 'v', bars: [{ label: 'a', value: 1 }, { label: 'b', value: Number.NaN }] }).bars).toEqual([{ label: 'a', value: 1 }])
      expect(cleanSpec({ kind: 'horizontal-bars', title: 't', valueLabel: 'v', bars: [{ label: 'a', value: -Infinity }] }).bars).toEqual([])
      const stacked: StackedBarsSpec = { kind: 'stacked-bars', title: 't', valueLabel: 'v', series: [{ name: 'a', tone: 'primary' }, { name: 'b', tone: 'soft' }], bars: [{ label: 'ok', values: [1, 2] }, { label: 'bad', values: [1, Number.NaN] }] }
      expect(cleanSpec(stacked).bars).toEqual([{ label: 'ok', values: [1, 2] }])
    })

    it('says whether there is anything to draw', () => {
      expect(hasData({ kind: 'bars', title: 't', valueLabel: 'v', bars: [] })).toBe(false)
      expect(hasData({ kind: 'bars', title: 't', valueLabel: 'v', bars: [{ label: 'a', value: 0 }] })).toBe(true)
      expect(hasData({ kind: 'time-series', title: 't', valueLabel: 'v', unit: '', from: 0, to: 1, points: [{ at: 0, value: null }] })).toBe(false)
      expect(hasData({ kind: 'time-series', title: 't', valueLabel: 'v', unit: '', from: 0, to: 1, points: [{ at: 0, value: 1 }] })).toBe(true)
      const stacked = (values: number[]): StackedBarsSpec => ({ kind: 'stacked-bars', title: 't', valueLabel: 'v', series: [{ name: 'a', tone: 'primary' }], bars: [{ label: 'x', values }] })
      expect(hasData(stacked([0, 0]))).toBe(false)
      expect(hasData(stacked([0, 2]))).toBe(true)
      expect(hasData({ ...stacked([1]), bars: [] })).toBe(false)
    })
  })

  describe('a long series in the data table', () => {
    const long = (count: number, value: (index: number) => number | null): TimeSeriesSpec => ({
      kind: 'time-series',
      title: 'CPU',
      valueLabel: 'CPU %',
      unit: '%',
      from: 0,
      to: 1,
      points: Array.from({ length: count }, (_, index) => ({ at: new Date(2026, 9, 1, 10, 0).getTime() + index * 10_000, value: value(index) })),
    })

    it('is listed in full up to the limit', () => {
      expect(chartTable(long(TABLE_FULL_LIMIT, () => 5)).rows).toHaveLength(TABLE_FULL_LIMIT)
    })

    it('is summarised above it: minimum, average, maximum and latest, then every tenth point', () => {
      const table = chartTable(long(360, (index) => (index === 7 ? null : index % 100)))
      expect(table.rows.slice(0, 4)).toEqual([
        { label: 'Minimum', value: '0%' },
        { label: 'Average', value: `${Math.round((Array.from({ length: 360 }, (_, index) => (index === 7 ? null : index % 100)).filter((v): v is number => v !== null).reduce((a, b) => a + b, 0) / 359) * 10) / 10}%` },
        { label: 'Maximum', value: '99%' },
        { label: 'Latest', value: '59%' },
      ])
      expect(table.rows).toHaveLength(4 + Math.ceil(360 / TABLE_STEP))
      expect(table.rows[4].label).toBe('10:00')
      expect(table.rows[5].label).toBe('10:01')
    })

    it('says "no sample" for the summary of a long series with nothing in it', () => {
      expect(chartTable(long(100, () => null)).rows.slice(0, 4).map((row) => row.value)).toEqual(['no sample', 'no sample', 'no sample', 'no sample'])
    })
  })
})
