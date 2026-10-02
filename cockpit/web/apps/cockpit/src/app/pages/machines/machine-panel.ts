import { ChangeDetectionStrategy, Component, Type, computed, inject, input } from '@angular/core'
import { AppLink, FleetModel, FleetStore, Worktree, agentDetailLink, agentTitle, machineLoad, selectionLink } from '@cockpit/fleet-data'
import { LinkResult, machineAgentsLink, machineRepositoriesLink, machineWorktreesLink } from '@cockpit/fleet-data/list'
import { MachinePanel, buildMachinePanel } from '@cockpit/fleet-data/panel'
import { StateBadge, UiClock } from '@cockpit/ui/control'
import { PanelContent, PanelFact, PanelRelated, PanelRelatedItem, PanelState } from '@cockpit/ui/panel'
import { MetricsPoller, watchMetrics } from '../../metrics/metrics-poller'
import { ViewportMount } from '@cockpit/ui/viewport-mount'
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
 */
@Component({
  selector: 'app-machine-panel',
  imports: [PanelContent, PanelState, StateBadge, ViewportMount],
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
  protected readonly attention = computed(() => attentionOf((this.data() as Loaded).model, this.id()))
  protected readonly truncated = computed(() => this.store.document().agents_truncated === true)

  protected readonly metrics = computed(() => metricsView(this.poller.entries().get(this.id()), this.clock()))
  protected readonly load = computed(() => machineLoad(this.poller.metricsOf(this.id())).state)
  /** The inputs of the lazy charts. */
  protected readonly chartInputs = computed(() => ({ samples: this.metrics().samples, now: this.metrics().readAt }))
  /** `() => import(...)`: the charts' chunk, which holds Chart.js's wrapper. */
  protected readonly loadCharts = (): Promise<Type<unknown>> => import('./machine-charts').then((module) => module.MachineCharts)

  /** The summary: how it is reached, OS, architecture, CPUs, WB version, uptime, what its export left out. */
  protected readonly facts = computed<PanelFact[]>(() => {
    const { summary } = (this.data() as Loaded).view
    const { machine } = summary
    const reported = (label: string, text: string | undefined): PanelFact => (text === undefined || text === '' ? { label, text: 'not reported', muted: true } : { label, text })
    const dropped = machine.export_dropped
    return [
      reported('OS', machine.os),
      reported('Architecture', machine.arch),
      reported('CPUs', machine.cpu_count === undefined ? undefined : String(machine.cpu_count)),
      reported('WB version', machine.wb_version),
      reported('Uptime', uptimeWords(summary)),
      { label: 'Reached by', text: machine.route === 'local' ? 'this machine' : (machine.transport ?? 'a published snapshot') },
      ...(machine.route === 'local' || machine.observed_at === undefined ? [] : [{ label: 'Observed', time: Date.parse(machine.observed_at) }]),
      ...(dropped ? [{ label: 'Left out', text: `${dropped} ${dropped === 1 ? 'entry' : 'entries'} of its export` }] : []),
      ...(this.truncated() ? [{ label: 'Agents', text: 'The agent list is capped at the first 200 of each machine.' }] : []),
    ]
  })

  /** What is on the machine, the running agents and the most recent worktrees, as links to the lists and the entries. */
  protected readonly related = computed<PanelRelated[]>(() => {
    const { view, model } = this.data() as Loaded
    const counts = machineCounts(model).get(this.id()) as MachineCounts
    const links = { repositories: machineRepositoriesLink(this.id()), worktrees: machineWorktreesLink(this.id()), agents: machineAgentsLink(this.id()) }
    const count = (n: number, one: string, many: string, link: LinkResult, extra = ''): PanelRelatedItem => ({ text: `${n} ${n === 1 ? one : many}${extra}`, ...(n === 0 ? {} : { link: (link as { link: AppLink }).link }) })
    return [
      {
        title: 'On this machine',
        items: [count(counts.repositories, 'repository', 'repositories', links.repositories), count(counts.worktrees, 'worktree', 'worktrees', links.worktrees), count(counts.agents, 'agent', 'agents', links.agents, `, ${view.summary.runningAgents} running`)],
      },
      { title: 'Running agents', items: view.related.runningAgents.map((agent) => ({ text: agentTitle(agent), link: agentDetailLink(agent.id), title: `Agent state: ${agent.state}` })) },
      {
        title: 'Recent worktrees',
        items: view.related.recentWorktrees.map((worktree: Worktree) => ({ text: `${worktree.task} in ${model.repositoryName(worktree.repository)}`, link: selectionLink('worktrees', worktree.id) })),
      },
    ]
  })
}
