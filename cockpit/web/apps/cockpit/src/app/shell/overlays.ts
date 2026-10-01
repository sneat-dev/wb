import { ChangeDetectionStrategy, Component } from '@angular/core'
import { CommandPalette } from '../palette/command-palette'
import { ShortcutSheet } from '../shortcuts/shortcut-sheet'

/**
 * The palette and the shortcut sheet. They are a lazy chunk the shell fetches
 * as soon as it has rendered (app.ts), not a part of the initial script: the
 * page is usable before they arrive, and by the time anyone presses Cmd+K they
 * are here, so opening the palette makes no request. A key pressed before they
 * arrive is not lost: the shell state holds it and the overlay opens when it exists.
 */
@Component({
  selector: 'app-overlays',
  imports: [CommandPalette, ShortcutSheet],
  template: '<app-command-palette /><app-shortcut-sheet />',
  changeDetection: ChangeDetectionStrategy.OnPush,
})
export class Overlays {}
