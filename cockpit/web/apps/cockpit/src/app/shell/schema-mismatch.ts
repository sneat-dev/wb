import { DOCUMENT } from '@angular/common'
import { ChangeDetectionStrategy, Component, InjectionToken, computed, inject, input } from '@angular/core'
import { CopyCommand, RELOAD_MESSAGE, SchemaMismatch as Mismatch, UPDATE_WB_MESSAGE, selfUpdate } from '@cockpit/fleet-data'
import { Icon } from '../ui/icon'

/** The location of the page, which a reload goes through; a test replaces it. */
export const PAGE_LOCATION = new InjectionToken<Pick<Location, 'reload'>>('page location', {
  providedIn: 'root',
  factory: () => inject(DOCUMENT).location,
})

/** The text of a command to copy, or nothing when the command was refused: the state then names no command. */
export function commandText(command: CopyCommand): string | undefined {
  return command.ok ? command.text : undefined
}

/**
 * What the shell shows instead of any data when the fleet document has another
 * schema version (REQ:schema-version-2): "update wb on this machine" when the
 * daemon is older, "reload" when this page is. It renders no entity.
 */
@Component({
  selector: 'app-schema-mismatch',
  imports: [Icon],
  templateUrl: './schema-mismatch.html',
  styleUrl: './schema-mismatch.css',
  changeDetection: ChangeDetectionStrategy.OnPush,
})
export class SchemaMismatch {
  readonly mismatch = input.required<Mismatch>()
  private readonly location = inject(PAGE_LOCATION)

  protected readonly daemonOlder = computed(() => this.mismatch() === 'daemon-older')
  protected readonly updateMessage = UPDATE_WB_MESSAGE
  protected readonly reloadMessage = RELOAD_MESSAGE
  /** `wb self-update`, to run on the machine whose daemon is older. */
  protected readonly command = commandText(selfUpdate())

  protected reload(): void {
    this.location.reload()
  }
}
