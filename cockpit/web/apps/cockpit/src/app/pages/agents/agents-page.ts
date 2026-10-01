import { ChangeDetectionStrategy, Component, computed, inject } from '@angular/core'
import { RouterLink } from '@angular/router'
import { Agent, FleetStore, isRunning, selectionLink } from '@cockpit/fleet-data'
import { chipOf } from '@cockpit/fleet-data/list'
import { StateBadge, UiClock } from '@cockpit/ui/control'
import { ALWAYS, ListCell, ListChip, ListColumn, ListPanelTemplate, ListView, MachineCell } from '@cockpit/ui/list'
import { AgentPanelView } from './agent-panel'
import { agentFallback, agentName, timeCell } from './agent-text'

/** Every registered agent session and dispatched run, one row each (REQ:agents-list). */
@Component({
  selector: 'app-agents-page',
  imports: [RouterLink, ListView, ListCell, ListPanelTemplate, StateBadge, MachineCell, AgentPanelView],
  templateUrl: './agents-page.html',
  styleUrl: './agents-page.css',
  changeDetection: ChangeDetectionStrategy.OnPush,
})
export class AgentsPage {
  protected readonly store = inject(FleetStore)
  private readonly clock = inject(UiClock).now

  /** The kinds the list shows: the Kind column is worth its room only when both are there. */
  private readonly uniformKind = computed(() => new Set(this.store.document().agents.map((agent) => agent.kind)).size < 2)
  private readonly manyMachines = computed(() => this.store.document().machines.length > 1)

  /** One chip for each runtime the agents report (the vocabulary cannot list them in advance). */
  protected readonly runtimeChips = computed<ListChip[]>(() => {
    const runtimes = [...new Set(this.store.document().agents.map((agent) => agent.runtime?.toLowerCase()))]
    return runtimes.sort().flatMap((runtime) => (runtime === undefined ? [] : (chipOf('agents', `runtime-${runtime}`) ?? [])))
  })

  /** Machines of another route that publish no agent: their agents, if any, are not in this list. */
  protected readonly silentMachines = computed(() => {
    const document = this.store.document()
    const reporting = new Set(document.agents.map((agent) => agent.machine_id))
    return document.machines.filter((machine) => machine.route !== 'local' && !reporting.has(machine.id)).map((machine) => machine.machine)
  })

  protected readonly truncated = computed(() => this.store.document().agents_truncated === true)

  protected readonly name = agentName
  protected readonly panelLabel = (agent: Agent) => `Agent ${agentName(agent)}`
  protected readonly taskLink = (task: string) => selectionLink('tasks', task)
  protected readonly time = (agent: Agent): string => timeCell(agent, this.clock())
  protected readonly running = isRunning
  protected readonly tasks = (agent: Agent): string[] => this.store.model().tasksOfAgent(agent)
  protected readonly repository = (agent: Agent): string | undefined => (agent.repository === undefined ? undefined : this.store.model().repositoryName(agent.repository))
  protected readonly fallback = (agent: Agent): string => agentFallback(agent, this.clock())
  /** A state the agent can still report an activity for: a finished run has no activity to miss. */
  protected readonly reportsActivity = (agent: Agent): boolean => ['running', 'live', 'parked'].includes(agent.state)

  /**
   * Narrow lists lose Kind, then Machine, then the time; the agent and its state stay. Machine is hidden while the
   * fleet is this machine alone, Kind while every row is of one kind, the time while no agent reports one.
   */
  protected readonly columns: ListColumn<Agent>[] = [
    { id: 'agent', header: 'Agent', sort: 'label', width: 'fill', grow: 4, min: 300, priority: ALWAYS, value: (a) => `${agentName(a)} ${this.tasks(a)[0] ?? this.repository(a) ?? ''}`.trim() },
    { id: 'state', header: 'State', sort: 'activity', width: 220, min: 150, priority: ALWAYS, value: (a) => (a.activity ? `${a.state}, ${a.activity}` : a.state) },
    { id: 'time', header: 'Time', sort: 'started', width: 150, min: 120, priority: 3, value: (a) => timeCell(a, this.clock()) },
    { id: 'machine', header: 'Machine', sort: 'machine', width: 200, min: 150, priority: 2, value: (a) => a.machine, empty: (a) => a.route === 'local' && !this.manyMachines() },
    { id: 'kind', header: 'Kind', width: 90, min: 80, priority: 1, value: (a) => a.kind, empty: () => this.uniformKind() },
  ]
}
