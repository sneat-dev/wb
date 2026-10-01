import { ChangeDetectionStrategy, Component, computed, input } from '@angular/core'
import { RouterLink } from '@angular/router'
import { FleetModel } from '@cockpit/fleet-data'
import { GLYPH_CHECK_CIRCLE, Glyph, TaskStateBadge } from '@cockpit/ui/state'
import { SkeletonRows } from '../../shell/skeleton-rows'
import { LazyMount } from './lazy-mount'
import { needsYouRows } from './needs-you-rows'

/** The secondary action of work at risk (the registry's Push or a "Copy template" icon): a lazy chunk, since it depends on the registry. */
const loadWorkAction = () => import('./work-action').then((module) => module.WorkAction)

/**
 * Home "Needs you" (REQ:home-needs-you): one row per task that needs the operator, worst first, at
 * most five, each with its reason in words and exactly one primary action; "+n more" opens Tasks
 * filtered to the same set. It looks urgent only when it has rows: with none it is one calm line.
 * It is part of the first page and renders from the model alone: the rows' primary actions are links,
 * and the secondary action of work at risk (the registry's push, or a copy icon) is a lazy chunk.
 */
@Component({
  selector: 'app-needs-you',
  imports: [RouterLink, Glyph, LazyMount, SkeletonRows, TaskStateBadge],
  templateUrl: './needs-you-section.html',
  changeDetection: ChangeDetectionStrategy.OnPush,
})
export class NeedsYouSection {
  readonly model = input.required<FleetModel>()
  /** The daemon's first scan is still running: an empty section is not yet an answer. */
  readonly warming = input(false)

  protected readonly ok = GLYPH_CHECK_CIRCLE
  protected readonly loadWork = loadWorkAction
  protected readonly rows = computed(() => needsYouRows(this.model()))
  protected readonly needs = computed(() => this.model().needsYou)
  protected readonly empty = computed(() => this.rows().length === 0)
}
