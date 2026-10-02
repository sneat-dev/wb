import { ChangeDetectionStrategy, Component, computed, inject } from '@angular/core'
import { toSignal } from '@angular/core/rxjs-interop'
import { ActivatedRoute, RouterLink } from '@angular/router'
import { FleetStore, MachineView, linkTarget, machineDetailLink, machineLoad } from '@cockpit/fleet-data'
import { machineAgentsLink, machineRepositoriesLink, machineWorktreesLink, parseListQuery } from '@cockpit/fleet-data/list'
import { publishErrorWords, remoteErrorText } from '@cockpit/fleet-data/home-details'
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
  /** The first read of this machine's metrics has not answered yet: the CPU and Memory columns hold their place meanwhile. */
  reading: boolean
  /** The `publish_error` of this machine's own entry in words; none when publishing works or is off. */
  publishWarning: string | undefined
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
        const load = machineLoad(entries.get(view.machine.id)?.metrics, now)
        const code = view.machine.remote_error
        const publish = view.machine.publish_error
        return [
          view.machine.id,
          {
            reach: reachWords(view, now),
            stale: view.state === 'stale',
            warning: code === undefined ? undefined : remoteErrorText(code),
            reading: !entries.has(view.machine.id),
            publishWarning: publish === undefined ? undefined : publishErrorWords(publish),
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
  private readonly cpu = (view: MachineView): string => {
    const { bars, reading } = this.row(view)
    // While the first read is out the column keeps its place (a column that appears with the numbers moves the rows).
    return bars === undefined ? (reading ? '…' : '') : `${bars.cpu}%`
  }
  private readonly memory = (view: MachineView): string => {
    const { bars, reading } = this.row(view)
    return bars === undefined ? (reading ? '…' : '') : `${bars.memory}%`
  }
  protected readonly target = linkTarget
  protected readonly detail = (view: MachineView) => machineDetailLink(view.machine.id)
  protected readonly links = (view: MachineView) => ({ repositories: machineRepositoriesLink(view.machine.id), worktrees: machineWorktreesLink(view.machine.id), agents: machineAgentsLink(view.machine.id) })

  /**
   * The machine and its state stay at every width. A panel or a narrow window takes first the CPU and Memory bars, then
   * the Agents count, Repositories, the load verdict, Worktrees and last the version, whose "older" mark is what the list
   * exists to show; what is hidden is in the panel. The widths are the least that read.
   */
  protected readonly columns: ListColumn<MachineView>[] = [
    { id: 'machine', header: 'Machines', title: true, sort: 'machine', width: 'fill', grow: 2, min: 150, priority: ALWAYS, value: (view) => view.machine.machine },
    { id: 'state', header: 'State', sort: 'state', width: 'fill', grow: 3, min: 220, priority: ALWAYS, value: (view) => this.row(view).reach },
    { id: 'version', header: 'WB version', sort: 'version', width: 136, min: 132, priority: 7, value: (view) => view.machine.wb_version ?? '' },
    { id: 'repositories', header: 'Repositories', width: 112, min: 104, priority: 4, align: 'end', value: (view) => String(this.row(view).counts.repositories) },
    { id: 'worktrees', header: 'Worktrees', width: 96, min: 88, priority: 6, align: 'end', value: (view) => String(this.row(view).counts.worktrees) },
    { id: 'agents', header: 'Agents', width: 80, min: 72, priority: 3, align: 'end', value: (view) => String(this.row(view).counts.agents) },
    { id: 'load', header: 'Load', hint: 'Free, busy or unknown, from the latest sample', width: 140, min: 132, priority: 5, value: (view) => this.row(view).load.state },
    { id: 'cpu', header: 'CPU', hint: 'Processor use in the latest sample', width: 128, min: 120, priority: 1, value: (view) => this.cpu(view) },
    { id: 'memory', header: 'Memory', hint: 'Memory used in the latest sample', width: 128, min: 120, priority: 2, value: (view) => this.memory(view) },
  ]
}
