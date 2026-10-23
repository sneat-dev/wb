import { ChangeDetectionStrategy, Component, computed, inject, input } from '@angular/core'
import { RouterLink } from '@angular/router'
import { FleetModel, FleetStore, PanelCommand, RegistryAction, TaskPanel, TaskView, agentDetailLink, agentTitle, linkTarget, repositoryDetailLink, worktreeDetailLink } from '@cockpit/fleet-data'
import { buildTaskPanel } from '@cockpit/fleet-data/panel'
import { seenElsewhereOnly, taskReason } from '@cockpit/fleet-data/task-reason'
import { ActionSlot, PrChip, StateBadge } from '@cockpit/ui/control'
import { MachineCell, OwnerStateCell, AgeText } from '@cockpit/ui/list'
import { PanelContent, PanelFact, PanelState } from '@cockpit/ui/panel'

/** The task of a name, with its panel data and the model they were read from. */
interface Loaded {
  task: TaskView
  view: TaskPanel
  model: FleetModel
}

/**
 * One task's content: the side panel of the Tasks list and the page of `/tasks/detail?task=<name>` are both
 * this component (REQ:detail-routes-share-the-panel). The header says the state and, in words, why; then the
 * pull requests (each with its action slot), the worktrees, the agents, the library's Copy commands and the
 * collapsed Raw data.
 */
@Component({
  selector: 'app-task-panel',
  imports: [RouterLink, PanelContent, PanelState, StateBadge, PrChip, ActionSlot, AgeText, MachineCell, OwnerStateCell],
  templateUrl: './task-panel.html',
  styleUrl: './task-panel.css',
  changeDetection: ChangeDetectionStrategy.OnPush,
})
export class TaskPanelView {
  readonly name = input.required<string>()
  /** The detail page, not the side panel. */
  readonly page = input(false)

  protected readonly store = inject(FleetStore)
  protected readonly target = linkTarget
  /**
   * What the action registry returned, by target (`worktree:<id>`, `pull_request:<id>`); the page that has the
   * cockpit-actions client passes it. Without it, or without an entry for a target, a slot renders nothing.
   */
  readonly registry = input<ReadonlyMap<string, readonly RegistryAction[]>>()

  /** The task and its panel data; none for a name the document does not list. */
  protected readonly data = computed<Loaded | undefined>(() => {
    const model = this.store.model()
    const task = model.taskNamed(this.name())
    return task === undefined ? undefined : { task, view: buildTaskPanel(model, task.name) as TaskPanel, model }
  })

  protected readonly reason = computed(() => {
    const { task, model } = this.data() as Loaded
    return taskReason(model, task.name) as string
  })

  /** The machines whose report decides the task, when none of its entries is on this machine (REQ:task-state, trust rule). */
  protected readonly reportedBy = computed(() => {
    const { task } = this.data() as Loaded
    return task.stateSource === 'remote' ? task.reportedBy : []
  })

  protected actionsFor(target: string): readonly RegistryAction[] | undefined {
    return this.registry()?.get(target)
  }

  protected readonly elsewhereOnly = computed(() => seenElsewhereOnly((this.data() as Loaded).task))
  /** More than one machine in the fleet: only then is a machine worth a chip in a row. */
  protected readonly manyMachines = computed(() => this.store.document().machines.length > 1)

  /** The repositories of the task's worktrees, each linking to its page. */
  protected readonly repositories = computed(() => {
    const { task, model } = this.data() as Loaded
    const seen = new Map<string, { slug: string; host: string | undefined; id: string }>()
    for (const worktree of task.worktrees) {
      const slug = model.repositoryName(worktree.repository)
      const host = model.document.repositories.find((repository) => repository.id === worktree.repository)?.host
      seen.set(`${host ?? ''}/${slug}`, {
        slug,
        host,
        id: worktree.repository,
      })
    }
    return [...seen.values()].map(({ slug, host, id }) => ({
      text: slug,
      link: repositoryDetailLink(host, slug, id),
    }))
  })

  /** The pull requests, each with the repository it is in. */
  protected readonly pullRequests = computed(() => {
    const { view, model } = this.data() as Loaded
    return view.related.pullRequests.map((pr) => ({
      pr,
      repository: pr.repository === undefined ? undefined : model.repositoryName(pr.repository),
    }))
  })

  protected readonly worktrees = computed(() => {
    const { view, model } = this.data() as Loaded
    return view.related.worktrees.map((worktree) => ({
      worktree,
      repository: model.repositoryName(worktree.repository),
      link: worktreeDetailLink(worktree.id),
    }))
  })

  protected readonly agents = computed(() =>
    (this.data() as Loaded).view.related.agents.map((agent) => ({
      agent,
      title: agentTitle(agent),
      link: agentDetailLink(agent.id),
    })),
  )

  /** The summary facts above the sections: the repositories and machines of the task, and when it last moved. */
  protected readonly facts = computed<PanelFact[]>(() => {
    const { task } = this.data() as Loaded
    return [
      { label: 'Repositories', text: '', links: this.repositories() },
      {
        label: 'Machines',
        text: '',
        machines: task.machines.map((machine) => machine.id),
      },
      { label: 'Last activity', text: '—', time: task.lastActivityAt },
    ]
  })

  /**
   * The library's task commands (a task that only another machine reports has none that change anything: the
   * library withholds them), each with where it runs.
   */
  protected readonly commands = computed<PanelCommand[]>(() => (this.data() as Loaded).view.commands)
}
