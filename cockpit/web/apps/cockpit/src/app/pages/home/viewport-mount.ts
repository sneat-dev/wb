import { DOCUMENT } from '@angular/common'
import {
  ChangeDetectionStrategy,
  Component,
  ComponentRef,
  DestroyRef,
  ElementRef,
  Type,
  ViewContainerRef,
  effect,
  inject,
  input,
  signal,
  untracked,
  viewChild,
} from '@angular/core'

/** How near the viewport (in pixels) a viewport mount starts loading, so the code is there when it scrolls into view. */
export const VIEWPORT_MARGIN = 200

/**
 * Like `LazyMount`, but the component is requested only when the place it will occupy scrolls near
 * the viewport (an `IntersectionObserver`; where there is none, at once). The place keeps its height
 * until the component is there, so nothing moves when it arrives. Used for the charts, so Chart.js
 * is fetched only by a viewer who scrolls to them.
 */
@Component({
  selector: 'app-viewport-mount',
  template: '@if (!shown()) {<div #slot class="home-lazy-slot"></div>}',
  styles: ':host { display: contents; }',
  changeDetection: ChangeDetectionStrategy.OnPush,
})
export class ViewportMount {
  /** `() => import('./x').then((module) => module.X)`. */
  readonly load = input.required<() => Promise<Type<unknown>>>()
  /** The inputs of the created component, by name. */
  readonly inputs = input<Readonly<Record<string, unknown>>>({})

  private readonly container = inject(ViewContainerRef)
  private readonly view = inject(DOCUMENT).defaultView as Window & typeof globalThis
  private readonly slot = viewChild<ElementRef<HTMLElement>>('slot')
  private readonly created = signal<ComponentRef<unknown> | undefined>(undefined)
  protected readonly shown = signal(false)
  private watcher: IntersectionObserver | undefined
  private started = false
  private alive = true

  constructor() {
    inject(DestroyRef).onDestroy(() => {
      this.alive = false
      this.watcher?.disconnect()
    })
    effect(() => {
      const load = this.load()
      const slot = this.slot()
      if (this.started) return
      if (this.view.IntersectionObserver === undefined) return untracked(() => this.create(load))
      if (slot === undefined) return
      this.watcher ??= new this.view.IntersectionObserver(
        (entries) => {
          if (entries.some((entry) => entry.isIntersecting)) untracked(() => this.create(load))
        },
        { rootMargin: `${VIEWPORT_MARGIN}px` },
      )
      this.watcher.observe(slot.nativeElement)
    })
    effect(() => {
      const created = this.created()
      if (created !== undefined) this.apply(created, this.inputs())
    })
  }

  private create(load: () => Promise<Type<unknown>>): void {
    this.started = true
    this.watcher?.disconnect()
    load().then(
      (type) => {
        if (!this.alive) return
        this.shown.set(true)
        const created = this.container.createComponent(type)
        this.apply(created, untracked(this.inputs))
        this.created.set(created)
      },
      () => undefined,
    )
  }

  private apply(created: ComponentRef<unknown>, inputs: Readonly<Record<string, unknown>>): void {
    for (const [name, value] of Object.entries(inputs)) created.setInput(name, value)
  }
}
