import { ChangeDetectionStrategy, Component, inject } from '@angular/core'
import { FleetStore } from '@cockpit/fleet-data'
import { CommandPalette } from '../palette/command-palette'
import { ShortcutSheet } from '../shortcuts/shortcut-sheet'
import { OwnerPopover } from './owner-popover'
import { SchemaMismatch } from './schema-mismatch'

/**
 * The palette, the shortcut sheet and the schema-mismatch state: the parts of the
 * shell that are not needed to render the first page. They are a lazy chunk the
 * shell fetches when the browser is idle after the first render (overlay-loader.ts),
 * or at once when one is wanted: the page is usable before they arrive, and by the
 * time anyone presses Cmd+K they are here, so opening the palette makes no request.
 * A key pressed before they arrive is not lost: the shell state holds it.
 *
 * With another schema version of the fleet document the shell shows no page, and
 * the explanation (update wb, or reload) is shown here, below the empty page.
 */
@Component({
  selector: 'app-overlays',
  imports: [CommandPalette, ShortcutSheet, SchemaMismatch, OwnerPopover],
  template: `
    @if (store.schemaMismatch(); as mismatch) {
      <app-schema-mismatch [mismatch]="mismatch" />
    }
    <app-command-palette />
    <app-shortcut-sheet />
    <app-owner-popover />
  `,
  changeDetection: ChangeDetectionStrategy.OnPush,
})
export class Overlays {
  protected readonly store = inject(FleetStore)
}
