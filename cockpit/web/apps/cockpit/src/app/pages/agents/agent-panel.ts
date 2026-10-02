import { ChangeDetectionStrategy, Component, computed, inject, input } from '@angular/core'
import { RouterLink } from '@angular/router'
import { Agent, FleetModel, FleetStore, PanelCommand, RegistryAction, linkTarget, machineDetailLink, repositoryDetailLink, routeLabel, selectionLink } from '@cockpit/fleet-data'
import { AgentPanel, buildAgentPanel } from '@cockpit/fleet-data/panel'
import { ActionSlot, PrChip, StateBadge, UiClock } from '@cockpit/ui/control'
import { MachineCell, OwnerStateCell } from '@cockpit/ui/list'
import { PanelContent, PanelFact, PanelState } from '@cockpit/ui/panel'
import { agentHeadline, agentName } from './agent-text'

/** The agent of an id, with its panel data and the model they were read from. */
interface Loaded {
  agent: Agent
  view: AgentPanel
  model: FleetModel
}

/**
 * One agent's content: the side panel of the Agents list and the page of `/agents/:id` are both this component
 * (REQ:detail-routes-share-the-panel). The header says, in words, what is known (how long it has run, whether it is
 * blocked on you, when it finished); then who it is, the work it is on (task, repository, worktrees, the pull
 * requests of those), the library's Copy commands where a real command exists, and the collapsed Raw data.
 */
@Component({
  selector: 'app-agent-panel',
  imports: [RouterLink, PanelContent, PanelState, StateBadge, PrChip, ActionSlot, MachineCell, OwnerStateCell],
  templateUrl: './agent-panel.html',
  styleUrl: './agent-panel.css',
  changeDetection: ChangeDetectionStrategy.OnPush,
})
export class AgentPanelView {
  readonly id = input.required<string>()
  /** The detail page, not the side panel. */
  readonly page = input(false)
  /** What the action registry returned, by target (`agent:<id>`); without it a slot renders nothing. */
  readonly registry = input<ReadonlyMap<string, readonly RegistryAction[]>>()

  protected readonly store = inject(FleetStore)
  protected readonly target = linkTarget
  private readonly clock = inject(UiClock).now

  /** The agent and its panel data; none for an id the document does not list. */
  protected readonly data = computed<Loaded | undefined>(() => {
    const model = this.store.model()
    const agent = model.agentById(this.id())
    return agent === undefined ? undefined : { agent, view: buildAgentPanel(model, agent.id) as AgentPanel, model }
  })

  protected readonly name = computed(() => agentName((this.data() as Loaded).agent))
  protected readonly headline = computed(() => agentHeadline((this.data() as Loaded).agent, this.clock()))
  protected readonly blocked = computed(() => (this.data() as Loaded).agent.activity === 'blocked')
  /** The id a person copies: the session's or the run's own, not the entry's. */
  protected readonly identifier = computed(() => {
    const { agent } = this.data() as Loaded
    return agent.session_id ?? agent.run_id ?? agent.id
  })
  protected readonly remote = computed(() => (this.data() as Loaded).agent.route !== 'local')
  protected readonly source = computed(() => routeLabel((this.data() as Loaded).agent, this.clock()))
  /** Who the agent is: the facts above the work (REQ:side-panel). */
  protected readonly facts = computed<PanelFact[]>(() => {
    const { agent } = this.data() as Loaded
    const facts: PanelFact[] = [
      { label: 'Kind', text: agent.kind },
      { label: 'Runtime', text: agent.runtime ?? 'not reported' },
      { label: 'Model', text: agent.model ?? 'not reported' },
      { label: 'Machine', text: agent.machine, link: machineDetailLink(agent.machine_id) },
    ]
    if (this.remote()) facts.push({ label: 'Source', text: this.source(), muted: true })
    facts.push({ label: agent.kind === 'run' ? 'Run id' : 'Session id', text: this.identifier(), copy: true })
    return facts
  })
  protected readonly manyMachines = computed(() => this.store.document().machines.length > 1)

  /** The tasks of the agent: one task is a link; several mean no single task, and the worktrees are listed with theirs. */
  protected readonly tasks = computed(() => (this.data() as Loaded).model.tasksOfAgent((this.data() as Loaded).agent).map((task) => ({ name: task, link: selectionLink('tasks', task) })))

  protected readonly repository = computed(() => {
    const { agent, model } = this.data() as Loaded
    if (agent.repository === undefined) return undefined
    const slug = model.repositoryName(agent.repository)
    const host = model.document.repositories.find((repository) => repository.id === agent.repository)?.host
    return { slug, link: repositoryDetailLink(host, slug, agent.repository) }
  })

  protected readonly worktrees = computed(() => {
    const { view, model } = this.data() as Loaded
    return view.related.worktrees.map((worktree) => ({ worktree, repository: model.repositoryName(worktree.repository), link: selectionLink('worktrees', worktree.id) }))
  })

  protected readonly pullRequests = computed(() => {
    const { view, model } = this.data() as Loaded
    return view.related.pullRequests.map((pr) => ({ pr, repository: pr.repository === undefined ? undefined : model.repositoryName(pr.repository) }))
  })

  /** The library's status, logs and stop of a dispatched run of this machine; nothing for a session, nor for a run read from another machine. */
  protected readonly commands = computed<PanelCommand[]>(() => ((this.data() as Loaded).agent.route === 'local' ? (this.data() as Loaded).view.commands : []))

  /** Why there is no command, in one plain line; none when there are commands. */
  protected readonly controlNote = computed(() => {
    const { agent } = this.data() as Loaded
    if (agent.kind === 'session') return 'This session was not started by wb; it cannot be stopped or messaged from here.'
    if (agent.route !== 'local') return `This run is reported by ${agent.machine} (${routeLabel(agent, this.clock())}); it can be inspected or stopped only from that machine.`
    return undefined
  })

  protected actionsFor(target: string): readonly RegistryAction[] | undefined {
    return this.registry()?.get(target)
  }
}
