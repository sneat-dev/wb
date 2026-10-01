import { DOCUMENT } from '@angular/common'
import { ChangeDetectionStrategy, Component, ElementRef, Injector, afterNextRender, effect, inject, viewChild } from '@angular/core'
import { OwnerSignIn } from '@cockpit/ui/control'
import { ShellState } from './shell-state'

/**
 * The card under the session chip that says "Sign in as owner: run `wb cockpit`"
 * with the command to copy (REQ:owner-gating-is-visible). It is the one place
 * that says so: no action carries a sign-in message of its own. It opens from
 * the anonymous chip, takes focus, and closes on Escape (focus returns to the
 * chip), on a click elsewhere and when the chip is pressed again. It lives in the
 * lazy overlays chunk, not in the initial script.
 */
@Component({
  selector: 'app-owner-popover',
  imports: [OwnerSignIn],
  template: `@if (shell.ownerHintOpen()) {
    <div #card class="card" role="dialog" aria-label="Sign in as owner" tabindex="-1"><app-owner-signin /></div>
  }`,
  styles: `
    .card {
      position: fixed;
      z-index: 55;
      top: calc(var(--topbar-h) + var(--space-1));
      right: var(--gutter);
      width: min(22rem, calc(100vw - 2 * var(--space-2)));
      padding: var(--space-3) var(--space-4);
      border-radius: var(--radius-lg);
      background: var(--surface-raised);
      box-shadow: var(--shadow-pop);
      outline: none;
    }
  `,
  changeDetection: ChangeDetectionStrategy.OnPush,
  host: { '(document:click)': 'outside($event)', '(document:keydown.escape)': 'escape()' },
})
export class OwnerPopover {
  protected readonly shell = inject(ShellState)
  private readonly document = inject(DOCUMENT)
  private readonly host = inject<ElementRef<HTMLElement>>(ElementRef)
  private readonly card = viewChild<ElementRef<HTMLElement>>('card')

  constructor() {
    const injector = inject(Injector)
    // The card takes focus when it opens.
    effect(() => {
      if (this.shell.ownerHintOpen()) afterNextRender(() => this.card()?.nativeElement.focus(), { injector })
    })
  }

  protected outside(event: Event): void {
    if (!this.shell.ownerHintOpen()) return
    const target = event.target as Element
    // A press on the chip toggles the card itself; a press inside the card keeps it.
    if (this.host.nativeElement.contains(target) || target.closest('.session') !== null) return
    this.shell.closeOwnerHint()
  }

  protected escape(): void {
    if (!this.shell.ownerHintOpen()) return
    this.shell.closeOwnerHint()
    this.document.querySelector<HTMLElement>('.session')?.focus()
  }
}
