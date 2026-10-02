import { ChangeDetectionStrategy, Component, computed, input } from '@angular/core'
import type { FleetModel } from '@cockpit/fleet-data'
import { LazyMount } from './lazy-mount'

/** The charts' chunk: the series (home-details) and the charts with Chart.js behind them, requested right after the first paint. */
const loadCharts = () => import('./throughput-charts').then((module) => module.ThroughputCharts)

/** Whether the document has a throughput block at all (REQ:throughput-block); the series itself is built in the lazy chunk. */
export function hasThroughput(model: FleetModel): boolean {
  return model.document.throughput !== undefined
}

/**
 * Whether Throughput is Home's first section: the block is there, or it is not known yet (no document
 * yet, or the daemon's first scan is still running), so the place is kept and nothing moves when the
 * answer is yes. A complete document without a block takes it off the top.
 */
export function throughputOnTop(model: FleetModel, warming: boolean): boolean {
  return warming || hasThroughput(model)
}

/**
 * Home's "Throughput" section (REQ:home-charts). With a throughput block it is the first section of
 * Home: the heading and a slot that keeps the height of the charts (`.throughput-slot`, in home.css)
 * are in the first page, and the charts, with Chart.js, are a lazy chunk requested as soon as the
 * section is created, so "Needs you" below never moves when they arrive. While it is not known whether
 * there is a block (`warming`), it is the heading and an empty slot of the same height. Without a block
 * it is the calm line "No charts", and the page places it at the bottom, where it used to be. One component
 * for both places, so the heading and the line are written once.
 */
@Component({
  selector: 'app-throughput',
  imports: [LazyMount],
  template: `<section class="home-section" aria-labelledby="home-charts-h">
    <h2 id="home-charts-h" class="home-h">Throughput</h2>
    @if (warming()) {
      <div class="throughput-slot pending" aria-hidden="true"></div>
    } @else if (present()) {
      <div class="throughput-slot"><app-lazy-mount [load]="loadCharts" [inputs]="chartInputs()" /></div>
    } @else {
      <p class="home-calm">No charts: the daemon reports no throughput (finished and dropped work) yet.</p>
    }
  </section>`,
  changeDetection: ChangeDetectionStrategy.OnPush,
})
export class ThroughputSection {
  readonly model = input.required<FleetModel>()
  /** The daemon's first scan is not done (or there is no document yet): the answer is not known. */
  readonly warming = input(false)

  protected readonly loadCharts = loadCharts
  protected readonly present = computed(() => hasThroughput(this.model()))
  protected readonly chartInputs = computed(() => ({ model: this.model() }))
}
