import { ChangeDetectionStrategy, Component, signal } from '@angular/core'
import { AppLink, ageLink, hrefOf } from '@cockpit/fleet-data'
import {
  ActionActivation,
  ActionSlot,
  ChartSpec,
  ChartView,
  CopyCommandList,
  MachineChip,
  OwnerSignIn,
  PrChip,
  RelativeTime,
  StateBadge,
  SyncBadges,
  HorizontalBarsSpec,
  machineMetricSpecs,
} from '@cockpit/ui'
import {
  BADGE_ROWS,
  GALLERY_AGE_BUCKETS,
  SYNC_ROWS,
  galleryAnonymousRegistry,
  galleryCommandLists,
  galleryLandedPerDay,
  galleryMachines,
  galleryMetrics,
  galleryPullRequests,
  galleryRegistry,
} from './gallery-data'

/**
 * Every component of the control surface in every state, on one page: the
 * visual vocabulary to look at, in light and dark. It is in the preview build
 * only (gallery-routes.preview.ts) and has no entry in the navigation.
 */
@Component({
  selector: 'app-gallery',
  imports: [ActionSlot, ChartView, CopyCommandList, MachineChip, OwnerSignIn, PrChip, RelativeTime, StateBadge, SyncBadges],
  templateUrl: './gallery-page.html',
  styleUrl: './gallery-page.css',
  changeDetection: ChangeDetectionStrategy.OnPush,
})
export class GalleryPage {
  protected readonly now = Date.now()
  protected readonly badgeRows = BADGE_ROWS
  protected readonly syncRows = SYNC_ROWS
  protected readonly pullRequests = galleryPullRequests(this.now)
  protected readonly machines = galleryMachines(this.now)
  protected readonly commandLists = galleryCommandLists()
  protected readonly registry = galleryRegistry()
  protected readonly anonymousRegistry = galleryAnonymousRegistry()
  protected readonly times = [0.5, 5, 95, 60 * 30, 60 * 24 * 40].map((minutes) => new Date(this.now - minutes * 60_000).toISOString())

  protected readonly metrics = machineMetricSpecs(galleryMetrics(this.now).samples, this.now)
  protected readonly landed: ChartSpec = { kind: 'bars', title: 'Landed per day', valueLabel: 'Tasks landed', bars: galleryLandedPerDay(this.now) }
  protected readonly ages: HorizontalBarsSpec = {
    kind: 'horizontal-bars',
    title: 'Worktrees by age',
    valueLabel: 'Worktrees',
    bars: GALLERY_AGE_BUCKETS.map((bucket) => ({ label: bucket.label, value: bucket.value, link: ageLink('worktrees', bucket.term) })),
  }

  /** What the last interaction did, so the page shows that a control only emits. */
  protected readonly lastAction = signal('No action activated yet.')
  protected readonly lastBucket = signal('No bucket selected yet.')
  protected readonly lastCopied = signal('Nothing copied yet.')

  protected activated(activation: ActionActivation): void {
    this.lastAction.set(`Emitted ${activation.action.id} for ${activation.target}; nothing ran.`)
  }

  protected selected(link: AppLink): void {
    this.lastBucket.set(`Emitted the link ${hrefOf(link)}.`)
  }

  protected copied(text: string): void {
    this.lastCopied.set(`Copied: ${text}`)
  }
}
