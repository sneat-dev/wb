import { Injectable, signal } from '@angular/core'

/** What the shell overlays show: the palette and the shortcut sheet. */
@Injectable({ providedIn: 'root' })
export class ShellState {
  readonly paletteOpen = signal(false)
  readonly sheetOpen = signal(false)
  /** The "Sign in as owner" card under the session chip; it does not hold the keyboard, so it is not modal. */
  readonly ownerHintOpen = signal(false)

  toggleOwnerHint(): void {
    this.ownerHintOpen.update((open) => !open)
  }

  closeOwnerHint(): void {
    this.ownerHintOpen.set(false)
  }

  openPalette(): void {
    this.sheetOpen.set(false)
    this.paletteOpen.set(true)
  }

  closePalette(): void {
    this.paletteOpen.set(false)
  }

  openSheet(): void {
    this.paletteOpen.set(false)
    this.sheetOpen.set(true)
  }

  closeSheet(): void {
    this.sheetOpen.set(false)
  }

  /** Whether an overlay holds the keyboard. */
  modalOpen(): boolean {
    return this.paletteOpen() || this.sheetOpen()
  }
}
