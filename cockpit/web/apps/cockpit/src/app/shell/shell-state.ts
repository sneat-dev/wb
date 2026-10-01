import { Injectable, signal } from '@angular/core'

/** What the shell overlays show: the palette and the shortcut sheet. */
@Injectable({ providedIn: 'root' })
export class ShellState {
  readonly paletteOpen = signal(false)
  readonly sheetOpen = signal(false)

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
