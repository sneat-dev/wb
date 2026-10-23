import { ChangeDetectionStrategy, Component, computed, inject } from '@angular/core'
import { FleetStore } from '@cockpit/fleet-data'
import { LazyMount } from './lazy-mount'
import { NeedsYouSection } from './needs-you-section'
import { ThroughputSection } from './throughput-section'

/** The rest of Home, one lazy chunk requested as soon as the page is created. */
const loadRest = () => import('./home-rest').then((module) => module.HomeRest)

/**
 * Home, the front door: a dispatcher's inbox under a dashboard line. "Throughput" is always the
 * first section, in a slot of fixed height that holds a skeleton while the daemon scans, the charts,
 * a calm line when there is nothing to chart or a Retry when the charts' chunk failed
 * (REQ:home-charts); its heading and slot are in the first page and the charts, with Chart.js, are a
 * lazy chunk requested right after the first paint. "Needs you" renders from the model that is
 * already loaded, with no request and no lazy code of its own. Everything after it ("Ready to
 * land", "In flight" with the machine strip, "Resume", "Cleanup", "Fleet health" when something is
 * wrong) is one lazy chunk, requested when the page is created and appended below, so nothing on
 * screen moves (REQ:initial-script-size keeps Home's first-page script under 350 kB: what is left in
 * the first page is the two sections above, the task badge and glyphs they draw, and the code that
 * mounts the lazy chunks).
 */
@Component({
  selector: 'app-home-page',
  imports: [LazyMount, NeedsYouSection, ThroughputSection],
  templateUrl: './home-page.html',
  changeDetection: ChangeDetectionStrategy.OnPush,
})
export class HomePage {
  protected readonly store = inject(FleetStore)
  protected readonly loadRest = loadRest
  protected readonly restInputs = computed(() => ({ model: this.store.model(), warming: this.store.warmingUp(), dropped: this.store.droppedEntries() }))
}
