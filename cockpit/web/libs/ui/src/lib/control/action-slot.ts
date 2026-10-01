import { DOCUMENT } from '@angular/common'
import { ChangeDetectionStrategy, Component, DestroyRef, ElementRef, Injector, afterNextRender, computed, inject, input, output, signal, viewChild } from '@angular/core'
import { RegistryAction } from '@cockpit/fleet-data'
import { Glyph } from './glyph'

/** The one explanation an action the caller may not run carries, whatever the action (REQ:owner-gating-is-visible). */
export const OWNER_ONLY_EXPLANATION = 'Needs an owner session'

/** What activating an action reports: the action as the registry returned it and the target it was returned for. */
export interface ActionActivation {
  action: RegistryAction
  /** `<type>:<id>`, as the registry route takes it. */
  target: string
}

/** Why an action cannot be activated, in words; undefined when it can. A missing capability outranks the snapshot's applicability. */
export function disabledReason(action: RegistryAction): string | undefined {
  if (!action.permitted) return OWNER_ONLY_EXPLANATION
  return action.applicable ? undefined : (action.reason ?? 'Not available now')
}

const MENU_WIDTH = 224
const MARGIN = 8
let nextId = 0

/**
 * The action area of one pull request or worktree (REQ:action-slots). It renders
 * exactly what the registry returned and hardcodes no action: `safe` and
 * `guarded` actions are buttons, `destructive` ones sit under an overflow menu
 * ([cockpit-actions] REQ:common-actions-are-direct). An action that cannot run is
 * disabled and carries its reason (the registry's, or, for a caller without the
 * capability, the same single explanation for every action). A disabled control
 * stays focusable (`aria-disabled`) so its reason can be read.
 *
 * With no registry (`actions` undefined) or no action for the target, nothing is
 * rendered at all: no placeholder, and since the host is `display: contents` no
 * box and no gap. Activating an action only emits `activated`; the preview and
 * the execution belong to the cockpit-actions Feature, and no navigation or
 * request happens here.
 */
@Component({
  selector: 'app-action-slot',
  imports: [Glyph],
  templateUrl: './action-slot.html',
  styleUrl: './action-slot.css',
  changeDetection: ChangeDetectionStrategy.OnPush,
  host: { '(document:click)': 'outside($event)', '(window:resize)': 'close()', '(keydown.escape)': 'escape($event)' },
})
export class ActionSlot {
  /** What the registry returned for the target; undefined when the registry route is absent. */
  readonly actions = input<readonly RegistryAction[] | undefined>()
  readonly target = input.required<string>()
  readonly activated = output<ActionActivation>()

  private readonly host = inject<ElementRef<HTMLElement>>(ElementRef)
  private readonly document = inject(DOCUMENT)
  private readonly injector = inject(Injector)
  private readonly menu = viewChild<ElementRef<HTMLElement>>('menu')
  private readonly trigger = viewChild<ElementRef<HTMLElement>>('trigger')
  protected readonly uid = `action-slot-${nextId++}`
  protected readonly open = signal(false)
  protected readonly position = signal<{ left: number; top?: number; bottom?: number }>({ left: MARGIN })

  /** Each action with its reason, in the registry's order, in the direct buttons and the overflow menu. */
  private readonly entries = computed(() => (this.actions() ?? []).map((action) => ({ action, reason: disabledReason(action) })))
  protected readonly direct = computed(() => this.entries().filter((entry) => entry.action.safety !== 'destructive'))
  protected readonly overflow = computed(() => this.entries().filter((entry) => entry.action.safety === 'destructive'))
  protected readonly shown = computed(() => this.entries().length > 0)

  private readonly onScroll = (): void => this.close()

  constructor() {
    inject(DestroyRef).onDestroy(() => this.document.removeEventListener('scroll', this.onScroll, true))
  }

  protected activate(entry: { action: RegistryAction; reason: string | undefined }): void {
    if (entry.reason !== undefined) return
    this.close()
    this.activated.emit({ action: entry.action, target: this.target() })
  }

  protected toggle(trigger: HTMLElement): void {
    if (this.open()) return this.close()
    const rect = trigger.getBoundingClientRect()
    const view = this.document.defaultView as Window
    const width = Math.min(MENU_WIDTH, view.innerWidth - 2 * MARGIN)
    const left = Math.max(MARGIN, Math.min(rect.right - width, view.innerWidth - width - MARGIN))
    this.position.set(rect.bottom > view.innerHeight * 0.6 ? { left, bottom: view.innerHeight - rect.top + 4 } : { left, top: rect.bottom + 4 })
    this.open.set(true)
    this.document.addEventListener('scroll', this.onScroll, true)
    // The menu is shown by the next render; focus moves into it then.
    afterNextRender(() => this.focusFirst(), { injector: this.injector })
  }

  protected close(): void {
    this.open.set(false)
    this.document.removeEventListener('scroll', this.onScroll, true)
  }

  protected outside(event: Event): void {
    if (this.open() && !this.host.nativeElement.contains(event.target as Node)) this.close()
  }

  protected escape(event: Event): void {
    if (!this.open()) return
    event.stopPropagation()
    this.close()
    ;(this.trigger() as ElementRef<HTMLElement>).nativeElement.focus()
  }

  /** Arrow keys, Home and End move between the menu's items; focus stays in the menu until it closes. */
  protected navigate(event: KeyboardEvent): void {
    const items = [...(this.menu() as ElementRef<HTMLElement>).nativeElement.querySelectorAll<HTMLElement>('[role="menuitem"]')]
    const index = items.indexOf(this.document.activeElement as HTMLElement)
    const target = { ArrowDown: (index + 1) % items.length, ArrowUp: (index - 1 + items.length) % items.length, Home: 0, End: items.length - 1 }[event.key]
    if (target === undefined) return
    event.preventDefault()
    items[target].focus()
  }

  private focusFirst(): void {
    ;(this.menu() as ElementRef<HTMLElement>).nativeElement.querySelector<HTMLElement>('[role="menuitem"]')?.focus()
  }
}
