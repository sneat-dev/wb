import { ChangeDetectionStrategy, Component, ComponentRef, DestroyRef, Type, ViewContainerRef, effect, inject, input, signal, untracked } from '@angular/core'

/**
 * Creates a component whose code is a lazy chunk, next to this element, as soon as this element is
 * created, and keeps its inputs current. Home's first page carries only what it must (the budget of
 * REQ:initial-script-size): the rest is requested when Home is created and appended below what is
 * already shown, so nothing on screen moves. A chunk that cannot be fetched leaves nothing behind.
 * No `@defer`: its runtime would be part of the first page.
 */
@Component({
  selector: 'app-lazy-mount',
  template: '',
  styles: ':host { display: contents; }',
  changeDetection: ChangeDetectionStrategy.OnPush,
})
export class LazyMount {
  /** `() => import('./x').then((module) => module.X)`. */
  readonly load = input.required<() => Promise<Type<unknown>>>()
  /** The inputs of the created component, by name. */
  readonly inputs = input<Readonly<Record<string, unknown>>>({})

  private readonly container = inject(ViewContainerRef)
  private readonly created = signal<ComponentRef<unknown> | undefined>(undefined)
  private alive = true
  private started = false

  constructor() {
    inject(DestroyRef).onDestroy(() => (this.alive = false))
    effect(() => {
      const load = this.load()
      if (!this.started) untracked(() => this.create(load))
    })
    effect(() => {
      const created = this.created()
      if (created !== undefined) this.apply(created, this.inputs())
    })
  }

  private create(load: () => Promise<Type<unknown>>): void {
    this.started = true
    load().then(
      (type) => {
        if (!this.alive) return
        // The inputs are set before the component's first change detection, so it never reads a required input unset.
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
