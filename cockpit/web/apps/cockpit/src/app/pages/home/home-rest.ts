import { DOCUMENT } from '@angular/common'
import { ChangeDetectionStrategy, Component, DestroyRef, ViewEncapsulation, computed, effect, inject, input, signal, untracked } from '@angular/core'
import { FleetModel, FleetStore } from '@cockpit/fleet-data'
import { GLYPH_CHEVRON_DOWN, Glyph } from '@cockpit/ui/control'
import { watchMetrics } from '../../metrics/metrics-poller'
import { HomeMore } from './home-more'
import { HomeRegistry } from './home-registry'
import { InFlightSection } from './in-flight-section'
import { needsYouRows } from './needs-you-rows'
import { ReadySection } from './ready-section'
import { ResumeSection } from './resume-section'

/** The width at and below which Home is a phone: sections 1 to 3 are cards and the rest is behind "More". */
export const PHONE_QUERY = '(max-width: 480px)'

/**
 * The capabilities that cockpit-actions adds. A session that holds one has a daemon with an action registry.
 * TODO(cockpit-actions): the session does not say whether the registry route exists, and asking a daemon
 * without one is a 404 (a console error on every load). Until the session advertises the registry, only a
 * session that holds an action capability is asked; the others see the "Copy" buttons.
 */
export const ACTION_CAPABILITIES: readonly string[] = ['git.commit', 'branch.push', 'branch.delete', 'worktree.discard', 'pr.create', 'pr.land']

/** The registry targets whose actions Home's rows can show: the worktrees at risk and the pull requests ready to land. */
export function registryTargets(model: FleetModel): string[] {
  const atRisk = needsYouRows(model).flatMap((row) => row.work?.worktrees.map((worktree) => `worktree:${worktree.id}`) ?? [])
  const ready = model.readyToLand.ready.flatMap((row) => row.pullRequests.filter((pullRequest) => !pullRequest.remote).map((pullRequest) => `pull_request:${pullRequest.id}`))
  return [...atRisk, ...ready]
}

/**
 * Everything on Home after "Needs you", as one lazy chunk that the page requests as soon as it is
 * created (REQ:initial-script-size keeps it out of the first page): "Ready to land", "In flight"
 * with the machine strip, "Resume", and below the fold "Cleanup", "Fleet health" and the charts (which wait until the
 * daemon's first scan is done: counts of a partial scan mean nothing, and nothing below the first three sections may move). It
 * also asks the registry for the actions of the rows that can show them. On a phone (480 px or
 * less) sections 1 to 3 are cards and the rest sits behind one "More" disclosure (REQ:home-phone).
 */
@Component({
  selector: 'app-home-rest',
  imports: [Glyph, HomeMore, InFlightSection, ReadySection, ResumeSection],
  templateUrl: './home-rest.html',
  styleUrl: './home-rest.css',
  // The sections are children of this component and of the page, and share their row styles.
  encapsulation: ViewEncapsulation.None,
  changeDetection: ChangeDetectionStrategy.OnPush,
})
export class HomeRest {
  readonly model = input.required<FleetModel>()
  readonly warming = input(false)
  /** How many entries of the last document were left out as invalid. */
  readonly dropped = input(0)

  private readonly registry = inject(HomeRegistry)
  private readonly store = inject(FleetStore)
  protected readonly phone = signal(false)
  protected readonly moreOpen = signal(false)
  protected readonly chevron = GLYPH_CHEVRON_DOWN
  /** The same list again (a poll that changes nothing about the rows) is not news: the registry is not asked again. */
  private readonly targets = computed(() => registryTargets(this.model()), { equal: (a, b) => a.length === b.length && a.every((target, index) => target === b[index]) })

  constructor() {
    // The machines are polled every 10 seconds while Home is shown (REQ:machine-metrics-polling); the strip that shows them is in this chunk.
    watchMetrics(() => this.store.document().machines.map((machine) => machine.id))
    effect(() => {
      const targets = this.targets()
      const asks = this.store.session()?.capabilities.some((capability) => ACTION_CAPABILITIES.includes(capability)) ?? false
      if (asks) untracked(() => void this.registry.request(targets))
    })
    const query = inject(DOCUMENT).defaultView?.matchMedia?.(PHONE_QUERY)
    if (query !== undefined) {
      this.phone.set(query.matches)
      const changed = (event: MediaQueryListEvent): void => this.phone.set(event.matches)
      query.addEventListener('change', changed)
      inject(DestroyRef).onDestroy(() => query.removeEventListener('change', changed))
    }
  }

  protected toggleMore(): void {
    this.moreOpen.update((open) => !open)
  }
}
