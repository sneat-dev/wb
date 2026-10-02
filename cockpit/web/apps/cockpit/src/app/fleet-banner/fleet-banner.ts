import { ChangeDetectionStrategy, Component, computed, inject } from '@angular/core'
import { ERROR_GIT_TOO_OLD, ERROR_REPOSITORIES_UNREADABLE, FleetStore, anyAgentsTruncated } from '@cockpit/fleet-data'
import { Icon } from '../ui/icon'

/**
 * What the fleet document says about itself that the rest of the shell does
 * not: a failed read, an error code, pull requests it could not place and a
 * capped agent list. The progress of the first scan is the freshness chip and
 * the skeleton rows; a schema mismatch has its own state. It never hides the
 * pages, because the document is published incrementally.
 */
@Component({
  selector: 'app-fleet-banner',
  imports: [Icon],
  templateUrl: './fleet-banner.html',
  styleUrl: './fleet-banner.css',
  changeDetection: ChangeDetectionStrategy.OnPush,
})
export class FleetBanner {
  protected readonly store = inject(FleetStore)
  protected readonly document = this.store.document
  /** Some machine's agents were cut (this machine's flag is the document's, another machine's is on its entry). */
  protected readonly agentsCut = computed(() => anyAgentsTruncated(this.document()))
  /** What the document's own error code means for the operator; any code gets a notice. */
  protected readonly documentError = computed(() => {
    const code = this.document().error
    if (code === undefined) return null
    if (code === ERROR_REPOSITORIES_UNREADABLE) return 'The repositories could not be listed on this machine.'
    if (code === ERROR_GIT_TOO_OLD) return 'Git on this machine is older than Cockpit supports, so repository details are not read.'
    return `The daemon reported a problem reading the fleet (${code}); what is listed may be incomplete.`
  })
}
