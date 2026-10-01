import { ChangeDetectionStrategy, Component } from '@angular/core'
import { CommandPalette } from '../palette/command-palette'
import { ShortcutSheet } from '../shortcuts/shortcut-sheet'
import { OwnerPopover } from './owner-popover'

/**
 * The palette, the shortcut sheet and the sign-in card. They are a lazy chunk the shell fetches
 * as soon as it has rendered (app.ts), not a part of the initial script: the
 * page is usable before they arrive, and by the time anyone presses Cmd+K they
 * are here, so opening the palette makes no request. A key pressed before they
 * arrive is not lost: the shell state holds it and the overlay opens when it exists.
 */
@Component({
  selector: 'app-overlays',
  imports: [CommandPalette, ShortcutSheet, OwnerPopover],
  template: '<app-command-palette /><app-shortcut-sheet /><app-owner-popover />',
  changeDetection: ChangeDetectionStrategy.OnPush,
})
export class Overlays {}
