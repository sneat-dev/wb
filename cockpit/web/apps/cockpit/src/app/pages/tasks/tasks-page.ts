import { ChangeDetectionStrategy, Component, computed, inject } from '@angular/core'
import { Agent, FleetStore, TaskView, isRunning, taskDetailLink } from '@cockpit/fleet-data'
import { taskWorktreesLink } from '@cockpit/fleet-data/list'
import { StateBadge } from '@cockpit/ui/control'
import { ALWAYS, AgeText, CountLink, IdentityCell, ListCell, ListColumn, ListPanelTemplate, ListView, MachineCell } from '@cockpit/ui/list'
import { TaskPanelView } from './task-panel'
import { TaskPrCell } from './task-pr-cell'

/** How many repositories and machines a Tasks row names before "+n". */
export const NAMES_SHOWN = 2

/** Every task of the fleet, one row each (REQ:tasks-list): the primary object of the Cockpit. */
@Component({
  selector: 'app-tasks-page',
  imports: [ListView, ListCell, ListPanelTemplate, IdentityCell, StateBadge, TaskPrCell, MachineCell, CountLink, AgeText, TaskPanelView],
  templateUrl: './tasks-page.html',
  styleUrl: './tasks-page.css',
  changeDetection: ChangeDetectionStrategy.OnPush,
})
export class TasksPage {
  protected readonly store = inject(FleetStore)

  /** The machines of the fleet that this one does not run on. */
  private readonly remote = computed(() => new Set(this.store.document().machines.filter((machine) => machine.route !== 'local').map((machine) => machine.id)))
  /** The running agents of a task, once per task of a model. */
  private readonly running = new WeakMap<TaskView, Agent[]>()

  protected readonly taskLink = (task: TaskView) => taskDetailLink(task.name)
  protected readonly worktreesLink = (task: TaskView) => taskWorktreesLink(task.name)
  protected readonly panelLabel = (task: TaskView) => `Task ${task.name}`

  /** The first two repositories and "+n" (REQ:tasks-list). */
  protected readonly repositories = (task: TaskView): string => {
    const more = task.repositories.length - NAMES_SHOWN
    return task.repositories.slice(0, NAMES_SHOWN).join(', ') + (more > 0 ? ` +${more}` : '')
  }

  protected readonly runningAgents = (task: TaskView): Agent[] => {
    let agents = this.running.get(task)
    if (agents === undefined) {
      agents = task.agents.filter(isRunning)
      this.running.set(task, agents)
    }
    return agents
  }

  /** Whether the task is anywhere but on this machine alone: only then are its machines worth showing. */
  protected readonly anyRemote = (task: TaskView): boolean => task.machines.some((machine) => this.remote().has(machine.id))
  /** The machines that report a task decided by another machine alone; none for a task decided here. */
  protected readonly reporters = (task: TaskView): string => (task.stateSource === 'remote' ? task.reportedBy.map((machine) => machine.name).join(', ') : '')
  protected readonly machinesShown = (task: TaskView) => task.machines.slice(0, NAMES_SHOWN)
  protected readonly machinesMore = (task: TaskView) => Math.max(0, task.machines.length - NAMES_SHOWN)

  /**
   * Narrow lists (a panel beside, a tablet) lose Machines first, then Agents, then Pull requests, then Worktrees; Task, State
   * and Last activity stay. A column empty for every visible row is hidden: Machines on a fleet that runs on one machine,
   * Agents while none runs.
   */
  protected readonly columns: ListColumn<TaskView>[] = [
    { id: 'task', header: 'Task', sort: 'task', width: 'fill', grow: 4, min: 280, priority: ALWAYS, value: (t) => `${t.name} · ${this.repositories(t)}` },
    { id: 'state', header: 'State', sort: 'state', width: 150, min: 136, priority: ALWAYS, value: (t) => t.stateInfo.label },
    { id: 'pr', header: 'Pull requests', width: 190, min: 150, priority: 3, value: (t) => t.pullRequests.map((pr) => `#${pr.number}`).join(' '), empty: (t) => t.pullRequests.length === 0 },
    { id: 'agents', header: 'Agents', width: 150, min: 120, priority: 2, value: (t) => this.runningAgents(t).map((agent) => agent.runtime ?? 'agent').join(' '), empty: (t) => this.runningAgents(t).length === 0 },
    { id: 'worktrees', header: 'Worktrees', sort: 'worktrees', width: 96, min: 90, priority: 4, align: 'end', value: (t) => String(t.worktrees.length) },
    { id: 'machines', header: 'Machines', width: 200, min: 150, priority: 1, value: (t) => t.machines.map((machine) => machine.name).join(' '), empty: (t) => !this.anyRemote(t) },
    { id: 'activity', header: 'Last activity', sort: 'activity', width: 104, min: 96, priority: ALWAYS },
  ]
}
