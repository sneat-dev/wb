import { ChangeDetectionStrategy, Component, computed, inject, input } from '@angular/core'
import { RouterLink } from '@angular/router'
import { FleetModel, ReadyPullRequest, ReadyToLand, ReadyToLandRow, chipLink } from '@cockpit/fleet-data'
import { ActionSlot, GLYPH_CHECK_CIRCLE, Glyph, RelativeTime, SlotCopy, StateBadge, copyLabel } from '@cockpit/ui/control'
import { SkeletonRows } from '../../shell/skeleton-rows'
import { counted, isoOf } from './home-format'
import { minutesBetween } from './home-time'
import { HomeRegistry, LAND_ACTION } from './home-registry'

/**
 * The quiet note under "Ready to land" when the daemon's hourly budget cut the last pull request
 * pass short: how old the shown state may be, from the oldest observation among the rows. Undefined
 * when the pass was not throttled.
 */
export function throttleNote(model: FleetModel, rows: ReadyToLand): string | undefined {
  if (!model.document.pull_requests_throttled) return undefined
  const times = [...rows.ready.map((row) => row.checkedAt), ...rows.notReady.map((row) => row.checkedAt)].filter((time): time is number => time !== undefined)
  return times.length === 0 ? 'PR state may be out of date' : `PR state may be up to ${minutesBetween(Math.min(...times), model.now)} min old`
}

/**
 * Home "Ready to land" (REQ:home-ready-to-land): the tasks whose every open pull request is
 * observed, green and mergeable, each with its pull requests and, per local pull request, an action slot:
 * the registry's landing action when a handler exists for it (none does yet), else "Copy" for `wb pr land`.
 * A pull request another machine reported has no command and says "reported by <machine>". Tasks that wait on
 * checks alone are listed below, muted, with how long ago the checks were read and no action.
 */
@Component({
  selector: 'app-ready-to-land',
  imports: [ActionSlot, Glyph, RelativeTime, RouterLink, SkeletonRows, StateBadge],
  templateUrl: './ready-section.html',
  changeDetection: ChangeDetectionStrategy.OnPush,
})
export class ReadySection {
  readonly model = input.required<FleetModel>()
  /** The daemon's first scan is still running: an empty section is not yet an answer. */
  readonly warming = input(false)

  private readonly registry = inject(HomeRegistry)
  protected readonly ok = GLYPH_CHECK_CIRCLE
  protected readonly readyLink = chipLink('tasks', 'ready')
  protected readonly rows = computed(() => this.model().readyToLand)
  protected readonly empty = computed(() => this.rows().ready.length === 0 && this.rows().notReady.length === 0)
  protected readonly note = computed(() => throttleNote(this.model(), this.rows()))
  protected readonly iso = isoOf
  protected readonly counted = counted

  /** The machines that reported a task this machine has no entry of. */
  protected reportedBy(row: ReadyToLandRow): string {
    return row.reportedBy.map((machine) => machine.name).join(', ')
  }

  /** The Copy control of a land command the library built (the model gives no command for another machine's pull request). */
  protected landCopy(pullRequest: ReadyPullRequest, command: string): SlotCopy {
    return { build: async () => ({ ok: true, text: command, needsEdit: false }), label: copyLabel(false, 'wb pr land', `${pullRequest.repository}#${pullRequest.number}`) }
  }

  protected offered(id: string) {
    return this.registry.offered(`pull_request:${id}`, LAND_ACTION)
  }
}
