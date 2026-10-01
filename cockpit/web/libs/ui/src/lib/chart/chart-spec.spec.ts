import type { MetricsSample } from '@cockpit/fleet-data'
import { TimeSeriesSpec, chartSummary, chartTable, clockTime, formatValue, machineMetricSpecs, withGaps } from './chart-spec'

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
    it('makes the four charts of the last hour: CPU, load, memory used and disk used', () => {
      const specs = machineMetricSpecs([sample(30), sample(20), sample(10)], NOW)
      expect(specs.map((spec) => [spec.title, spec.unit, spec.max])).toEqual([
        ['CPU', '%', 100],
        ['Load (1 minute)', '', undefined],
        ['Memory used', '%', 100],
        ['Disk used', '%', 100],
      ])
      expect(specs.map((spec) => spec.points.map((point) => point.value))).toEqual([[40, 40, 40], [1.25, 1.25, 1.25], [50, 50, 50], [75, 75, 75]])
      expect(specs[0]).toMatchObject({ kind: 'time-series', from: NOW - 60 * MIN, to: NOW, valueLabel: 'CPU %' })
    })

    it('drops samples outside the hour, unreadable times and readings the machine did not report', () => {
      const specs = machineMetricSpecs([sample(90), sample(30), sample(5, { sampled_at: 'garbage' }), sample(10, { memory_total_bytes: 0, disk_total_bytes: 0 })], NOW)
      expect(specs[0].points).toHaveLength(2)
      expect(specs[2].points.map((point) => point.value)).toEqual([50])
      expect(specs[3].points.map((point) => point.value)).toEqual([75])
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
  })
})
