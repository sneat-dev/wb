import { ChangeDetectionStrategy, Component, computed, inject } from '@angular/core'
import { toSignal } from '@angular/core/rxjs-interop'
import { ActivatedRoute, RouterLink } from '@angular/router'
import { FleetStore, MachineView, machineDetailLink, machineLoad } from '@cockpit/fleet-data'
import { machineAgentsLink, machineRepositoriesLink, machineWorktreesLink, parseListQuery } from '@cockpit/fleet-data/list'
import { remoteErrorText } from '@cockpit/fleet-data/home-details'
import { StateBadge, UiClock } from '@cockpit/ui/control'
import { ALWAYS, CountLink, ListCell, ListColumn, ListPanelTemplate, ListView } from '@cockpit/ui/list'
import { MetricsPoller, watchMetrics } from '../../metrics/metrics-poller'
import { MachineCounts, machineCounts } from './machine-counts'
import { MachinePanelView } from './machine-panel'
import { barsOf, reachWords, sampleWords } from './machine-text'

/** What a machine's row says, worked out once per snapshot, clock minute and metrics round. */
export interface MachineRow {
  reach: string
  stale: boolean
  /** The `remote_error` in words; none when the last read worked. */
  warning: string | undefined
  load: ReturnType<typeof machineLoad>
  bars: ReturnType<typeof barsOf>
  sample: string
  counts: MachineCounts
}

/**
 * This machine and every other machine the fleet has a snapshot for (REQ:machines-list). The first column header is
 * the page's visible title ("Machines", in the section-title type size), so there is no separate heading. CPU and
 * memory come from the latest polled sample (`MetricsPoller`, every 10 seconds while the page is shown) and are
 * empty, never zero, for a machine that reports none.
 *
 * TODO(ui list): the list shows at most 7 columns, so the load verdict and the CPU and memory bars share one
 * "Load" column. A `maxColumns` input would let them be separate columns as REQ:machines-list lists them. TODO(ui list):
 * a `ListColumn.title` flag would size the first header without the `::ng-deep` rule of machines-page.css.
 */
@Component({
  selector: 'app-machines-page',
  imports: [RouterLink, ListView, ListCell, ListPanelTemplate, CountLink, StateBadge, MachinePanelView],
  templateUrl: './machines-page.html',
  styleUrl: './machines-page.css',
  changeDetection: ChangeDetectionStrategy.OnPush,
})
export class MachinesPage {
  protected readonly store = inject(FleetStore)
  private readonly poller = inject(MetricsPoller)
  private readonly clock = inject(UiClock).now

  /** The machines the address narrows the list to (its machine chips); none means every machine. */
  private readonly chosen = toSignal(inject(ActivatedRoute).queryParamMap, { requireSync: true })

  constructor() {
    // The metrics of the machines shown are read every 10 seconds while this page is shown (REQ:machine-metrics-polling).
    // The text filter is not followed: the machines are few, and a hidden one is read as well.
    watchMetrics(() => {
      const chosen = parseListQuery('machines', { machine: this.chosen().get('machine') ?? undefined }).machines
      return this.store
        .document()
        .machines.map((machine) => machine.id)
        .filter((id) => chosen.length === 0 || chosen.includes(id))
    })
  }

  private readonly counts = computed(() => machineCounts(this.store.model()))
  protected readonly rows = computed(() => {
    const now = this.clock()
    const entries = this.poller.entries()
    const counts = this.counts()
    return new Map(
      this.store.model().machines.map((view): [string, MachineRow] => {
        const load = machineLoad(entries.get(view.machine.id)?.metrics)
        const code = view.machine.remote_error
        return [
          view.machine.id,
          {
            reach: reachWords(view, now),
            stale: view.state === 'stale',
            warning: code === undefined ? undefined : remoteErrorText(code),
            load,
            bars: barsOf(load),
            sample: sampleWords(load, now),
            counts: counts.get(view.machine.id) as MachineCounts,
          },
        ]
      }),
    )
  })

  protected readonly row = (view: MachineView): MachineRow => this.rows().get(view.machine.id) as MachineRow
  protected readonly panelLabel = (view: MachineView): string => `Machine ${view.machine.machine}`
  protected readonly detail = (view: MachineView) => machineDetailLink(view.machine.id)
  protected readonly links = (view: MachineView) => ({ repositories: machineRepositoriesLink(view.machine.id), worktrees: machineWorktreesLink(view.machine.id), agents: machineAgentsLink(view.machine.id) })

  /**
   * The machine and its state stay at every width. A panel or a narrow window takes first the Agents count, then
   * Repositories, the load (CPU and memory), Worktrees and last the version, whose "older" mark is what the list
   * exists to show; what is hidden is in the panel. The widths are the least that read.
   */
  protected readonly columns: ListColumn<MachineView>[] = [
    { id: 'machine', header: 'Machines', sort: 'machine', width: 'fill', grow: 2, min: 150, priority: ALWAYS, value: (view) => view.machine.machine },
    { id: 'state', header: 'State', sort: 'state', width: 'fill', grow: 3, min: 220, priority: ALWAYS, value: (view) => this.row(view).reach },
    { id: 'version', header: 'WB version', sort: 'version', width: 136, min: 132, priority: 7, value: (view) => view.machine.wb_version ?? '' },
    { id: 'repositories', header: 'Repositories', width: 112, min: 104, priority: 4, align: 'end', value: (view) => String(this.row(view).counts.repositories) },
    { id: 'worktrees', header: 'Worktrees', width: 96, min: 88, priority: 6, align: 'end', value: (view) => String(this.row(view).counts.worktrees) },
    { id: 'agents', header: 'Agents', width: 80, min: 72, priority: 3, align: 'end', value: (view) => String(this.row(view).counts.agents) },
    { id: 'load', header: 'Load', hint: 'Free, busy or unknown, with the CPU and memory of the latest sample', width: 330, min: 316, priority: 5, value: (view) => this.row(view).load.state },
  ]
}
