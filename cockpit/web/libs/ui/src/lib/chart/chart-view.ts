import { DOCUMENT } from '@angular/common'
import { ChangeDetectionStrategy, Component, DestroyRef, ElementRef, InjectionToken, afterNextRender, computed, effect, inject, input, output, signal, viewChild } from '@angular/core'
import type { AppLink } from '@cockpit/fleet-data'
import type { ChartEngine, ChartInstance } from './chart-engine'
import { chartConfiguration } from './chart-config'
import { ChartSpec, HorizontalBarsSpec, chartSummary, chartTable } from './chart-spec'
import { readChartTheme } from './chart-theme'

/**
 * How the Chart.js chunk is loaded. The default is a dynamic import, so Chart.js
 * is its own chunk, fetched only when a chart is shown; tests provide a fake.
 */
export const CHART_ENGINE = new InjectionToken<() => Promise<ChartEngine>>('CHART_ENGINE', {
  providedIn: 'root',
  factory: () => () => import('./chart-engine'),
})

const DARK = '(prefers-color-scheme: dark)'
const REDUCED_MOTION = '(prefers-reduced-motion: reduce)'

/**
 * A chart on a canvas with a text alternative (REQ:look-dependencies,
 * REQ:strict-csp-unchanged). The three presets are chosen by the spec's `kind`.
 * It draws to canvas only, with no inline script and no style element; it is
 * themed from the design tokens and redrawn when the colour scheme changes; it
 * does not animate when the viewer asked for reduced motion; and it always
 * carries a visually hidden data table with the same numbers. In horizontal bars
 * a bar with a link is clickable, and the same links are buttons in the table, so
 * the keyboard reaches them (the table is shown while one has focus).
 */
@Component({
  selector: 'app-chart',
  templateUrl: './chart-view.html',
  styleUrl: './chart-view.css',
  changeDetection: ChangeDetectionStrategy.OnPush,
})
export class ChartView {
  readonly spec = input.required<ChartSpec>()
  /** The height of the plot in rem; fixed, so the chart arriving moves nothing. */
  readonly height = input(9)
  /** Emits the link of a clicked bucket. */
  readonly bucketSelected = output<AppLink>()

  private readonly document = inject(DOCUMENT)
  private readonly view = this.document.defaultView as Window
  private readonly load = inject(CHART_ENGINE)
  private readonly canvas = viewChild.required<ElementRef<HTMLCanvasElement>>('canvas')
  private readonly box = viewChild.required<ElementRef<HTMLElement>>('box')
  private engine: ChartEngine | undefined
  private chart: ChartInstance | undefined
  private alive = true
  /** Counts colour scheme and motion changes, so the chart is redrawn. */
  private readonly scheme = signal(0)

  protected readonly table = computed(() => chartTable(this.spec()))
  protected readonly summary = computed(() => chartSummary(this.spec()))

  constructor() {
    const watched = [this.view.matchMedia?.(DARK), this.view.matchMedia?.(REDUCED_MOTION)].filter((query): query is MediaQueryList => query !== undefined)
    const changed = (): void => this.scheme.update((count) => count + 1)
    for (const query of watched) query.addEventListener('change', changed)
    inject(DestroyRef).onDestroy(() => {
      this.alive = false
      for (const query of watched) query.removeEventListener('change', changed)
      this.chart?.destroy()
    })
    afterNextRender(async () => {
      this.engine = await this.load()
      if (this.alive) this.draw()
    })
    // A new spec or a new colour scheme redraws the chart that exists.
    effect(() => {
      this.spec()
      this.scheme()
      if (this.engine !== undefined) this.draw()
    })
  }

  private draw(): void {
    const spec = this.spec()
    const view = this.view
    const config = chartConfiguration(spec, {
      theme: readChartTheme(this.box().nativeElement, view),
      reducedMotion: view.matchMedia?.(REDUCED_MOTION).matches ?? false,
      // Only horizontal bars are clickable, so only they report an index.
      onSelect: (index) => {
        const link = (spec as HorizontalBarsSpec).bars[index]?.link
        if (link !== undefined) this.bucketSelected.emit(link)
      },
    })
    if (this.chart === undefined) this.chart = (this.engine as ChartEngine).create(this.canvas().nativeElement, config)
    else this.chart.update(config)
  }

  protected select(link: AppLink): void {
    this.bucketSelected.emit(link)
  }
}
