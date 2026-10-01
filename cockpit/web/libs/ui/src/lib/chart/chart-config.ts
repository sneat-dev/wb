import type { ChartConfiguration, ChartOptions } from 'chart.js'
import { BarsSpec, ChartSpec, HorizontalBarsSpec, StackedBarsSpec, TimeSeriesSpec, clockTime, formatValue } from './chart-spec'
import type { ChartTheme } from './chart-theme'

/** What the config builders need besides the data. */
export interface ChartContext {
  theme: ChartTheme
  /** The viewer asked for no motion (`prefers-reduced-motion`). */
  reducedMotion: boolean
  /** Called with the index of a clicked bar (horizontal bars only). */
  onSelect?: (index: number) => void
}

const TICK_SIZE = 11

function common(context: ChartContext): ChartOptions {
  const { theme } = context
  return {
    responsive: true,
    maintainAspectRatio: false,
    // No motion at all for a viewer who asked for none; a short, quiet one otherwise.
    animation: context.reducedMotion ? false : { duration: 240 },
    plugins: {
      legend: { display: false },
      tooltip: {
        backgroundColor: theme.tooltipBackground,
        titleColor: theme.tooltipText,
        bodyColor: theme.tooltipText,
        displayColors: false,
        padding: 8,
        cornerRadius: 6,
        titleFont: { family: theme.font, size: 12, weight: 600 },
        bodyFont: { family: theme.font, size: 12 },
      },
    },
  }
}

const tick = (theme: ChartTheme) => ({ color: theme.tick, font: { family: theme.font, size: TICK_SIZE } })

function timeSeries(spec: TimeSeriesSpec, context: ChartContext): ChartConfiguration {
  const { theme } = context
  const options = common(context)
  return {
    type: 'line',
    data: {
      datasets: [
        {
          label: spec.valueLabel,
          data: spec.points.map((point) => ({ x: point.at, y: point.value })),
          borderColor: theme.line,
          backgroundColor: theme.fill,
          borderWidth: 1.75,
          pointRadius: 0,
          pointHoverRadius: 4,
          pointHoverBackgroundColor: theme.line,
          tension: 0.25,
          fill: 'origin',
          spanGaps: false,
        },
      ],
    },
    options: {
      ...options,
      interaction: { mode: 'nearest', axis: 'x', intersect: false },
      plugins: {
        ...options.plugins,
        tooltip: {
          ...options.plugins?.tooltip,
          callbacks: {
            title: (items) => clockTime(Number(items[0]?.parsed.x)),
            label: (item) => formatValue(Number(item.parsed.y), spec.unit),
          },
        },
      },
      scales: {
        x: {
          type: 'linear',
          min: spec.from,
          max: spec.to,
          grid: { display: false },
          border: { color: theme.grid },
          ticks: { ...tick(theme), maxTicksLimit: 5, maxRotation: 0, callback: (value) => clockTime(Number(value)) },
        },
        y: {
          min: 0,
          max: spec.max,
          grid: { color: theme.grid },
          border: { display: false },
          ticks: { ...tick(theme), maxTicksLimit: 5, callback: (value) => formatValue(Number(value), spec.unit) },
        },
      },
    },
  }
}

function bars(spec: BarsSpec | HorizontalBarsSpec, context: ChartContext, horizontal: boolean): ChartConfiguration {
  const { theme } = context
  const options = common(context)
  const clickable = horizontal && spec.bars.some((bar) => 'link' in bar && bar.link !== undefined)
  const value = {
    beginAtZero: true,
    grid: { color: theme.grid },
    border: { display: false },
    ticks: { ...tick(theme), precision: 0, maxTicksLimit: 5, callback: (value: string | number) => formatValue(Number(value), '') },
  }
  const category = { grid: { display: false }, border: { color: theme.grid }, ticks: { ...tick(theme), autoSkip: !horizontal, maxRotation: 0 } }
  return {
    type: 'bar',
    data: {
      labels: spec.bars.map((bar) => bar.label),
      datasets: [
        {
          label: spec.valueLabel,
          data: spec.bars.map((bar) => bar.value),
          backgroundColor: theme.bar,
          hoverBackgroundColor: theme.barHover,
          borderRadius: 3,
          borderSkipped: false,
          maxBarThickness: horizontal ? 22 : 28,
        },
      ],
    },
    options: {
      ...options,
      indexAxis: horizontal ? 'y' : 'x',
      scales: horizontal ? { x: value, y: category } : { x: category, y: value },
      onClick: (_event, elements) => {
        if (clickable && elements[0] !== undefined) context.onSelect?.(elements[0].index)
      },
      onHover: (event, elements) => {
        const target = event.native?.target as HTMLElement | null | undefined
        if (target) target.style.cursor = clickable && elements.length > 0 ? 'pointer' : 'default'
      },
    },
  }
}

function stacked(spec: StackedBarsSpec, context: ChartContext): ChartConfiguration {
  const { theme } = context
  const options = common(context)
  return {
    type: 'bar',
    data: {
      labels: spec.bars.map((bar) => bar.label),
      datasets: spec.series.map((series, index) => ({
        label: series.name,
        data: spec.bars.map((bar) => bar.values[index] ?? 0),
        backgroundColor: { primary: theme.bar, soft: theme.barSoft, muted: theme.barMuted }[series.tone],
        borderRadius: 2,
        borderSkipped: false,
        maxBarThickness: 18,
      })),
    },
    options: {
      ...options,
      interaction: { mode: 'index', intersect: false },
      plugins: { ...options.plugins, tooltip: { ...options.plugins?.tooltip, displayColors: true } },
      scales: {
        x: { stacked: true, grid: { display: false }, border: { color: theme.grid }, ticks: { ...tick(theme), autoSkip: true, maxTicksLimit: 8, maxRotation: 0 } },
        y: { stacked: true, beginAtZero: true, grid: { color: theme.grid }, border: { display: false }, ticks: { ...tick(theme), precision: 0, maxTicksLimit: 5 } },
      },
    },
  }
}

/** The Chart.js configuration of a spec: one preset per kind, themed from `context`. */
export function chartConfiguration(spec: ChartSpec, context: ChartContext): ChartConfiguration {
  if (spec.kind === 'time-series') return timeSeries(spec, context)
  if (spec.kind === 'stacked-bars') return stacked(spec, context)
  return bars(spec, context, spec.kind === 'horizontal-bars')
}
