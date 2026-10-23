import { DOCUMENT } from '@angular/common'
import { ChangeDetectionStrategy, Component, InjectionToken, computed, effect, inject, input, signal, untracked } from '@angular/core'
import type { FleetModel } from '@cockpit/fleet-data'
import { LazyMount } from './lazy-mount'

/** The charts' chunk: the series (home-details) and the charts with Chart.js behind them, requested right after the first paint. */
const loadCharts = () => import('./throughput-charts').then((module) => module.ThroughputCharts)

/**
 * Reloads the page. A browser remembers a module that could not be fetched until the page is loaded
 * again (a second `import()` of it fails at once, with no request), so that is what "Retry" must do.
 */
export const RELOAD_PAGE = new InjectionToken<() => void>('reload the page', {
  providedIn: 'root',
  factory: () => {
    const view = inject(DOCUMENT).defaultView
    return () => view?.location.reload()
  },
})

/** What the slot of Throughput holds: the skeleton, nothing to chart yet, or the charts (from their lazy chunk, which may fail: `failed`). */
export type ThroughputState = 'pending' | 'none' | 'charts' | 'failed'

/** Whether the document has throughput to chart: a block with a day or a task in it (REQ:throughput-block); the series itself is built in the lazy chunk. */
export function hasThroughput(model: FleetModel): boolean {
  const block = model.document.throughput
  return block !== undefined && (block.per_day.length > 0 || block.slowest.length > 0)
}

/**
 * Throughput is the first section of Home, always, in one slot whose height never depends on its
 * content (REQ:home-charts): the daemon's first complete document may come without the block and
 * gain it on a later publish, so the block coming and going must not move "Needs you" by a pixel.
 */
export function throughputState(model: FleetModel, warming: boolean): Exclude<ThroughputState, 'failed'> {
  if (warming) return 'pending'
  return hasThroughput(model) ? 'charts' : 'none'
}

/**
 * Home's "Throughput" section (REQ:home-charts), the first section of Home. Its heading and its slot
 * are in the first page: the slot has a fixed height at every breakpoint (`.throughput-slot` in
 * home.css) and holds exactly one of a skeleton while the daemon's first scan runs, the two charts
 * (a lazy chunk with Chart.js, requested as soon as the section is created), a calm centred line when
 * there is nothing to chart yet, or "Charts unavailable" with a Retry (a page reload, see RELOAD_PAGE) when the chunk could not be fetched.
 */
@Component({
  selector: 'app-throughput',
  imports: [LazyMount],
  template: `<section class="home-section" aria-labelledby="home-charts-h" [attr.aria-busy]="state() === 'pending' ? 'true' : null">
    <h2 id="home-charts-h" class="home-h">Throughput</h2>
    <div class="throughput-slot" [attr.data-state]="state()">
      @switch (state()) {
        @case ('pending') {
          <span class="visually-hidden" role="status">Loading throughput charts</span>
          <div class="throughput-skeleton" aria-hidden="true"></div>
        }
        @case ('none') {
          <p class="home-calm throughput-note">No charts: the daemon reports no throughput (finished and dropped work) yet.</p>
        }
        @case ('failed') {
          <p class="home-calm throughput-note">Charts unavailable. <button type="button" class="home-act" (click)="retry()">Retry</button></p>
        }
        @default {
          <app-lazy-mount [load]="loadCharts" [inputs]="chartInputs()" (loadFailed)="failed.set(true)" />
        }
      }
    </div>
  </section>`,
  changeDetection: ChangeDetectionStrategy.OnPush,
})
export class ThroughputSection {
  readonly model = input.required<FleetModel>()
  /** The daemon's first scan is not done (or there is no document yet): the answer is not known. */
  readonly warming = input(false)

  protected readonly loadCharts = loadCharts
  protected readonly failed = signal(false)
  private readonly reload = inject(RELOAD_PAGE)
  private readonly settled = computed(() => throughputState(this.model(), this.warming()))
  protected readonly state = computed<ThroughputState>(() => (this.settled() === 'charts' && this.failed() ? 'failed' : this.settled()))
  protected readonly chartInputs = computed(() => ({ model: this.model() }))

  constructor() {
    // A failure belongs to the charts it happened to: once the slot holds something else it is forgotten.
    effect(() => {
      if (this.settled() !== 'charts') untracked(() => this.failed.set(false))
    })
  }

  /** The page is loaded again, which is the only way to ask for a chunk that failed. */
  protected retry(): void {
    this.reload()
  }
}
