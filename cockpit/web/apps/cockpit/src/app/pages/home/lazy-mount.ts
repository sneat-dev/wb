import { DOCUMENT } from '@angular/common'
import { ChangeDetectionStrategy, Component, ComponentRef, DestroyRef, ElementRef, Type, ViewContainerRef, effect, inject, input, signal, untracked, viewChild } from '@angular/core'

/** How near the viewport (in pixels) a `viewport` mount starts loading, so the code is there when it scrolls into view. */
export const VIEWPORT_MARGIN = 200

/**
 * Creates a component whose code is a lazy chunk, next to this element, and keeps its inputs
 * current. Home's first page carries only what it must (the budget of REQ:initial-script-size): the
 * rest is requested when Home is created (or, with `viewport`, when the place it will occupy
 * scrolls near the viewport) and appended below what is already shown, so nothing on screen moves.
 * A chunk that cannot be fetched leaves nothing behind. No `@defer`: its runtime would be part of
 * the first page.
 */
@Component({
  selector: 'app-lazy-mount',
  template: '@if (!shown()) {<div #slot class="home-lazy-slot" [class.reserve]="viewport()"></div>}',
  styles: ':host { display: contents; }',
  changeDetection: ChangeDetectionStrategy.OnPush,
})
export class LazyMount {
  /** `() => import('./x').then((module) => module.X)`. */
  readonly load = input.required<() => Promise<Type<unknown>>>()
  /** The inputs of the created component, by name. */
  readonly inputs = input<Readonly<Record<string, unknown>>>({})
  /** Wait until this place is near the viewport before loading. */
  readonly viewport = input(false)

  private readonly container = inject(ViewContainerRef)
  private readonly view = inject(DOCUMENT).defaultView
  private readonly slot = viewChild<ElementRef<HTMLElement>>('slot')
  private readonly created = signal<ComponentRef<unknown> | undefined>(undefined)
  protected readonly shown = signal(false)
  private watcher: IntersectionObserver | undefined
  private alive = true
  private started = false

  constructor() {
    inject(DestroyRef).onDestroy(() => {
      this.alive = false
      this.watcher?.disconnect()
    })
    effect(() => {
      const load = this.load()
      const slot = this.slot()
      if (this.viewport() && slot !== undefined && this.view?.IntersectionObserver !== undefined) {
        this.watcher ??= new this.view.IntersectionObserver(
          (entries) => {
            if (entries.some((entry) => entry.isIntersecting)) {
              this.watcher?.disconnect()
              untracked(() => this.create(load))
            }
          },
          { rootMargin: `${VIEWPORT_MARGIN}px` },
        )
        this.watcher.observe(slot.nativeElement)
      } else if (!this.viewport() || this.view?.IntersectionObserver === undefined) {
        untracked(() => this.create(load))
      }
    })
    effect(() => {
      const created = this.created()
      const inputs = this.inputs()
      if (created !== undefined) this.apply(created, inputs)
    })
  }

  private apply(created: ComponentRef<unknown>, inputs: Readonly<Record<string, unknown>>): void {
    for (const [name, value] of Object.entries(inputs)) created.setInput(name, value)
  }

  private create(load: () => Promise<Type<unknown>>): void {
    if (this.started) return
    this.started = true
    load().then(
      (type) => {
        if (!this.alive) return
        this.shown.set(true)
        // The inputs are set before the component's first change detection, so it never reads a required input unset.
        const created = this.container.createComponent(type)
        this.apply(created, untracked(this.inputs))
        this.created.set(created)
      },
      () => undefined,
    )
  }
}
