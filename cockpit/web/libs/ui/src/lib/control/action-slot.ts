import { DOCUMENT } from '@angular/common'
import { ChangeDetectionStrategy, Component, DestroyRef, ElementRef, Injector, afterNextRender, computed, inject, input, signal, viewChild } from '@angular/core'
import { CopyCommand, RegistryAction } from '@cockpit/fleet-data'
import { copyWord } from './copy-label'
import { Glyph } from './glyph'
import { GLYPH_MORE } from './glyphs'
import { LazyCopy } from './lazy-copy'

/** The one explanation an action the caller may not run carries, whatever the action (REQ:owner-gating-is-visible). */
export const OWNER_ONLY_EXPLANATION = 'Needs an owner session'

/** What activating an action reports: the action as the registry returned it and the target it was returned for. */
export interface ActionActivation {
  action: RegistryAction
  /** `<type>:<id>`, as the registry route takes it. */
  target: string
}

/**
 * What a slot offers instead of a button when it cannot run the registry's action: the library's command for the
 * same intent, built when it is pressed, as a "Copy" button (or "Copy template" when it has a part to edit).
 */
export interface SlotCopy {
  build: () => Promise<CopyCommand>
  /** The accessible name, which starts with the button's word (`copyLabel`). */
  label: string
  /** The command has a placeholder to edit: the button says "Copy template". */
  template?: boolean
  /** A secondary, icon-only button, for a row that has a primary action beside it. */
  quiet?: boolean
}

/** Why an action cannot be activated, in words; undefined when it can. A missing capability outranks the snapshot's applicability. */
export function disabledReason(action: RegistryAction): string | undefined {
  if (!action.permitted) return OWNER_ONLY_EXPLANATION
  return action.applicable ? undefined : (action.reason ?? 'Not available now')
}

/** Only these two safety classes are direct buttons; anything else, a class this build does not know or none at all, goes under the overflow menu. */
export function isDirect(action: RegistryAction): boolean {
  return action.safety === 'safe' || action.safety === 'guarded'
}

const MENU_WIDTH = 224
const MARGIN = 8
let nextId = 0

interface Entry {
  action: RegistryAction
  reason: string | undefined
}

/**
 * The action area of one pull request or worktree (REQ:action-slots). It renders
 * exactly what the registry returned and hardcodes no action: `safe` and
 * `guarded` actions are buttons; every other class (`destructive`, one this build
 * does not know, or none) sits under an overflow menu, so an unknown class never
 * becomes a prominent button ([cockpit-actions] REQ:common-actions-are-direct). An
 * action that cannot run is `aria-disabled` (still focusable) with its reason read
 * once, shown in words on hover, focus and tap, and, for a caller without the
 * capability, the same single explanation for every action.
 *
 * A registry action is a live button only when BOTH the registry returned it AND the page
 * gave the slot a handler (`run`): the daemon has no actions route yet, and a button nothing
 * handles would be a lie. Without a handler the slot renders the Copy control of its `copy`
 * command instead (and nothing when it has none). With no registry (`actions` undefined) or no
 * action for the target and no `copy`, nothing is rendered at all: no placeholder, and since
 * the host is `display: contents` no box and no gap. Activating an action only calls `run`;
 * the preview and the execution belong to the cockpit-actions Feature, and no navigation or
 * request happens here.
 *
 * The overflow menu is a manual popover where the browser has them (it is then in
 * the top layer, so a transformed or clipped row cannot cut it off) and a fixed
 * box where it has not. Its document, resize and scroll listeners exist only while
 * it is open; Tab closes it; Escape and choosing an item return focus to its
 * button; focus moves with the arrow keys, Home and End over one tab stop.
 */
@Component({
  selector: 'app-action-slot',
  imports: [Glyph, LazyCopy],
  templateUrl: './action-slot.html',
  styleUrl: './action-slot.css',
  changeDetection: ChangeDetectionStrategy.OnPush,
  host: { '(keydown.escape)': 'escape($event)' },
})
export class ActionSlot {
  /** What the registry returned for the target; undefined when the registry route is absent. */
  readonly actions = input<readonly RegistryAction[] | undefined>()
  readonly target = input.required<string>()
  /** The page's handler for an activated action. Without one the registry's actions are not buttons: the slot is `copy`. */
  readonly run = input<(activation: ActionActivation) => void>()
  /** The Copy control shown when the slot has no handler (or the registry offered nothing). */
  readonly copy = input<SlotCopy>()

  private readonly host = inject<ElementRef<HTMLElement>>(ElementRef)
  private readonly document = inject(DOCUMENT)
  private readonly injector = inject(Injector)
  private readonly menu = viewChild<ElementRef<HTMLElement>>('menu')
  protected readonly uid = `action-slot-${nextId++}`
  protected readonly more = GLYPH_MORE
  protected readonly open = signal(false)
  protected readonly position = signal<{ left: number; top?: number; bottom?: number }>({ left: MARGIN })
  /** The item of the menu that is the one tab stop. */
  protected readonly active = signal(0)
  /** The reason of the disabled action under the pointer, focus or finger, in words. */
  protected readonly shownReason = signal<string | undefined>(undefined)

  private readonly entries = computed<Entry[]>(() => (this.actions() ?? []).map((action) => ({ action, reason: disabledReason(action) })))
  protected readonly direct = computed(() => this.entries().filter((entry) => isDirect(entry.action)))
  protected readonly overflow = computed(() => this.entries().filter((entry) => !isDirect(entry.action)))
  /** Live buttons need both a registry entry and a handler. */
  protected readonly shown = computed(() => this.entries().length > 0 && this.run() !== undefined)
  protected readonly copyWord = computed(() => copyWord(this.copy()?.template === true))

  /** The button the open menu hangs from. */
  private anchor: HTMLElement | undefined
  private native = false
  private readonly onDocumentClick = (event: Event): void => {
    if (!this.host.nativeElement.contains(event.target as Node)) this.close()
  }
  private readonly onResize = (): void => this.close()
  /** The menu is fixed, so a scroll moves it with its button instead of leaving it behind. */
  private readonly onScroll = (): void => this.place(this.anchor as HTMLElement)

  constructor() {
    inject(DestroyRef).onDestroy(() => this.release())
  }

  protected showReason(entry: Entry): void {
    this.shownReason.set(entry.reason === undefined ? undefined : `${entry.action.title}: ${entry.reason}`)
  }

  protected hideReason(): void {
    this.shownReason.set(undefined)
  }

  protected activate(entry: Entry): void {
    if (entry.reason !== undefined) return this.showReason(entry)
    this.run()?.({ action: entry.action, target: this.target() })
  }

  protected choose(entry: Entry): void {
    if (entry.reason !== undefined) return
    this.close(true)
    this.run()?.({ action: entry.action, target: this.target() })
  }

  private place(trigger: HTMLElement): void {
    const rect = trigger.getBoundingClientRect()
    const view = this.document.defaultView as Window
    const width = Math.min(MENU_WIDTH, view.innerWidth - 2 * MARGIN)
    const left = Math.max(MARGIN, Math.min(rect.right - width, view.innerWidth - width - MARGIN))
    this.position.set(rect.bottom > view.innerHeight * 0.6 ? { left, bottom: view.innerHeight - rect.top + 4 } : { left, top: rect.bottom + 4 })
  }

  protected toggle(trigger: HTMLElement): void {
    if (this.open()) return this.close(true)
    this.anchor = trigger
    this.place(trigger)
    this.active.set(0)
    this.open.set(true)
    this.document.addEventListener('click', this.onDocumentClick)
    this.document.addEventListener('scroll', this.onScroll, true)
    this.document.defaultView?.addEventListener('resize', this.onResize)
    // The menu is shown by the next render; it goes to the top layer and takes focus then.
    afterNextRender(
      () => {
        const element = (this.menu() as ElementRef<HTMLElement>).nativeElement
        if (typeof element.showPopover === 'function') {
          element.showPopover()
          this.native = true
        }
        element.querySelector<HTMLElement>('[role="menuitem"]')?.focus()
      },
      { injector: this.injector },
    )
  }

  /** Lets go of the listeners and the top layer. */
  private release(): void {
    this.document.removeEventListener('click', this.onDocumentClick)
    this.document.removeEventListener('scroll', this.onScroll, true)
    this.document.defaultView?.removeEventListener('resize', this.onResize)
    if (this.native) {
      this.menu()?.nativeElement.hidePopover()
      this.native = false
    }
  }

  /** Closes the menu; `refocus` puts focus back on its button. */
  protected close(refocus = false): void {
    this.release()
    this.open.set(false)
    if (refocus) this.anchor?.focus()
  }

  protected escape(event: Event): void {
    if (!this.open()) return
    event.stopPropagation()
    this.close(true)
  }

  /** Arrow keys, Home and End move between the menu's items; Tab closes the menu and goes on. */
  protected navigate(event: KeyboardEvent): void {
    if (event.key === 'Tab') return this.close()
    const items = [...(this.menu() as ElementRef<HTMLElement>).nativeElement.querySelectorAll<HTMLElement>('[role="menuitem"]')]
    const index = items.indexOf(this.document.activeElement as HTMLElement)
    const last = items.length - 1
    const target = { ArrowDown: index < 0 || index === last ? 0 : index + 1, ArrowUp: index <= 0 ? last : index - 1, Home: 0, End: last }[event.key]
    if (target === undefined) return
    event.preventDefault()
    items[target].focus()
  }
}
