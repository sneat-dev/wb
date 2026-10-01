import { BarController, BarElement, CategoryScale, Chart, Filler, LineController, LineElement, LinearScale, PointElement, Tooltip } from 'chart.js'
import type { ChartConfiguration } from 'chart.js'

// The only module that imports Chart.js, and only the pieces the three presets
// use, so the bundler keeps nothing else: bar and line, the category and linear
// scales, the area fill under a line, and the tooltip. It is loaded with a
// dynamic import, by the chart component alone, so Chart.js is its own lazy
// chunk that no other page fetches (REQ:look-dependencies).
Chart.register(BarController, BarElement, LineController, LineElement, PointElement, CategoryScale, LinearScale, Filler, Tooltip)

/** A chart drawn on a canvas. */
export interface ChartInstance {
  /** Redraws with new data and colours, without animation. */
  update(config: ChartConfiguration): void
  destroy(): void
}

/** What the chart component needs of the Chart.js chunk. */
export interface ChartEngine {
  create(canvas: HTMLCanvasElement, config: ChartConfiguration): ChartInstance
}

export function create(canvas: HTMLCanvasElement, config: ChartConfiguration): ChartInstance {
  const chart = new Chart(canvas, config)
  return {
    update(next) {
      chart.data = next.data
      chart.options = next.options ?? {}
      chart.update('none')
    },
    destroy() {
      chart.destroy()
    },
  }
}
