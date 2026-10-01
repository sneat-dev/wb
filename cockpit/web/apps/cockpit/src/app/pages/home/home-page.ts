import { ChangeDetectionStrategy, Component, computed, inject } from '@angular/core'
import { FleetStore } from '@cockpit/fleet-data'
import { LazyMount } from './lazy-mount'
import { NeedsYouSection } from './needs-you-section'

/** The rest of Home, one lazy chunk requested as soon as the page is created. */
const loadRest = () => import('./home-rest').then((module) => module.HomeRest)

/**
 * Home, the front door: a dispatcher's inbox. "Needs you" is the first page: it renders from the
 * model that is already loaded, with no request and no lazy code of its own, so it is there at
 * first paint. Everything below it ("Ready to land", "In flight" with the machine strip,
 * "Resume", "Cleanup", "Fleet health" when something is wrong, the throughput charts) is one lazy
 * chunk, requested when the page is created and appended below, so nothing on screen moves
 * (REQ:initial-script-size keeps Home's first-page script under 350 kB: what is left in the first
 * page is "Needs you", the task badge and glyphs it draws, and the code that mounts the lazy chunk). Chart.js loads only when the charts scroll near the viewport.
 */
@Component({
  selector: 'app-home-page',
  imports: [LazyMount, NeedsYouSection],
  templateUrl: './home-page.html',
  changeDetection: ChangeDetectionStrategy.OnPush,
})
export class HomePage {
  protected readonly store = inject(FleetStore)
  protected readonly loadRest = loadRest
  protected readonly restInputs = computed(() => ({ model: this.store.model(), warming: this.store.warmingUp(), dropped: this.store.droppedEntries() }))
}
