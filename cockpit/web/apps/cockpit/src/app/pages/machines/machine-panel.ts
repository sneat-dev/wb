import { ChangeDetectionStrategy, Component, Type, computed, inject, input } from '@angular/core'
import { RouterLink } from '@angular/router'
import { FleetModel, FleetStore, Worktree, agentDetailLink, agentTitle, machineLoad, selectionLink } from '@cockpit/fleet-data'
import { machineAgentsLink, machineRepositoriesLink, machineWorktreesLink } from '@cockpit/fleet-data/list'
import { MachinePanel, buildMachinePanel } from '@cockpit/fleet-data/panel'
import { StateBadge, UiClock } from '@cockpit/ui/control'
import { AgeText, CountLink } from '@cockpit/ui/list'
import { PanelContent } from '@cockpit/ui/panel'
import { MetricsPoller, watchMetrics } from '../../metrics/metrics-poller'
import { ViewportMount } from '../home/viewport-mount'
import { attentionOf } from './machine-attention'
import { MachineCounts, machineCounts } from './machine-counts'
import { metricsView, reachWords, uptimeWords } from './machine-text'

/** The machine of an id with its panel data and the model they were read from. */
interface Loaded {
  view: MachinePanel
  model: FleetModel
}

/**
 * One machine's content: the side panel of the Machines list and the page of `/machines/:id` are both this component
 * (REQ:detail-routes-share-the-panel). The summary (how it is reached, OS, architecture, CPUs, WB version, uptime), the
 * metrics block with its source stated plainly and the last hour's charts (a lazy chunk, fetched when they scroll
 * near the viewport), what needs attention with the command to copy and where it runs, counts that link to the
 * filtered lists, the running agents, the most recent worktrees, and the collapsed Raw data.
 *
 * It polls the machine's metrics only while it exists, through the shell's `MetricsPoller` (10 seconds).
 *
 * TODO(ui PanelContent): it has no slot above its facts, so the sections are projected whole and the facts are
 * drawn here; a `[panelHeader]` slot would let `facts` and `related` be used.
 */
@Component({
  selector: 'app-machine-panel',
  imports: [RouterLink, PanelContent, StateBadge, AgeText, CountLink, ViewportMount],
  templateUrl: './machine-panel.html',
  styleUrl: './machine-panel.css',
  changeDetection: ChangeDetectionStrategy.OnPush,
})
export class MachinePanelView {
  readonly id = input.required<string>()
  /** The detail page, not the side panel. */
  readonly page = input(false)

  protected readonly store = inject(FleetStore)
  private readonly poller = inject(MetricsPoller)
  private readonly clock = inject(UiClock).now

  constructor() {
    watchMetrics(() => [this.id()])
  }

  protected readonly data = computed<Loaded | undefined>(() => {
    const model = this.store.model()
    const view = buildMachinePanel(model, this.id())
    return view === undefined ? undefined : { view, model }
  })

  protected readonly name = computed(() => (this.data() as Loaded).view.summary.machine.machine)
  protected readonly reach = computed(() => reachWords((this.data() as Loaded).view.summary, this.clock()))
  protected readonly uptime = computed(() => uptimeWords((this.data() as Loaded).view.summary))
  protected readonly attention = computed(() => attentionOf((this.data() as Loaded).model, this.id()))
  protected readonly counts = computed(() => machineCounts((this.data() as Loaded).model).get(this.id()) as MachineCounts)
  protected readonly links = computed(() => ({ repositories: machineRepositoriesLink(this.id()), worktrees: machineWorktreesLink(this.id()), agents: machineAgentsLink(this.id()) }))
  protected readonly truncated = computed(() => this.store.document().agents_truncated === true)

  protected readonly metrics = computed(() => metricsView(this.poller.entries().get(this.id()), this.clock()))
  protected readonly load = computed(() => machineLoad(this.poller.metricsOf(this.id())).state)
  /** The inputs of the lazy charts. */
  protected readonly chartInputs = computed(() => ({ samples: this.metrics().samples, now: this.metrics().readAt }))
  /** `() => import(...)`: the charts' chunk, which holds Chart.js's wrapper. */
  protected readonly loadCharts = (): Promise<Type<unknown>> => import('./machine-charts').then((module) => module.MachineCharts)

  protected readonly agents = computed(() => (this.data() as Loaded).view.related.runningAgents.map((agent) => ({ agent, title: agentTitle(agent), link: agentDetailLink(agent.id) })))
  protected readonly worktrees = computed(() => {
    const { view, model } = this.data() as Loaded
    return view.related.recentWorktrees.map((worktree: Worktree) => ({ worktree, repository: model.repositoryName(worktree.repository), link: selectionLink('worktrees', worktree.id) }))
  })
}
