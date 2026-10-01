// What a list derives from its `page` alone: the noun, the rows, the quick-filter chips (from the
// vocabulary), the address of an entity's own page and the name `c` copies.

import { Agent, AppLink, ListPageId, MachineView, MergedRepository, TaskView, Worktree, FleetModel, agentDetailLink, agentTitle, machineDetailLink, repositoryDetailLink, taskDetailLink, worktreeDetailLink } from '@cockpit/fleet-data'
import { ListRow, VOCABULARY, buildAgentRows, buildMachineRows, buildRepositoryRows, buildTaskRows, buildWorktreeRows } from '@cockpit/fleet-data/list'
import { ListChip } from './list-state'

export const PAGE_NOUN: Readonly<Record<ListPageId, string>> = {
  tasks: 'tasks',
  repositories: 'repositories',
  worktrees: 'worktrees',
  agents: 'agents',
  machines: 'machines',
}

/** The chips of a page, from the vocabulary (ids, labels and hints). */
export function chipsOf(page: ListPageId): ListChip[] {
  return VOCABULARY[page].chips.map((chip) => ({ ...chip }))
}

/** The rows the view model prepared for a page. */
export function rowsOf(model: FleetModel, page: ListPageId): readonly ListRow<unknown>[] {
  switch (page) {
    case 'tasks':
      return buildTaskRows(model)
    case 'repositories':
      return buildRepositoryRows(model)
    case 'worktrees':
      return buildWorktreeRows(model)
    case 'agents':
      return buildAgentRows(model)
    case 'machines':
      return buildMachineRows(model)
  }
}

/** The address of an entity's own page. */
export function detailOf(page: ListPageId, item: unknown): AppLink {
  switch (page) {
    case 'tasks':
      return taskDetailLink((item as TaskView).name)
    case 'repositories':
      return repositoryDetailLink((item as MergedRepository).host, (item as MergedRepository).slug)
    case 'worktrees':
      return worktreeDetailLink((item as Worktree).id)
    case 'agents':
      return agentDetailLink((item as Agent).id)
    case 'machines':
      return machineDetailLink((item as MachineView).machine.id)
  }
}

/** The name of an entity that `c` copies: a task, a branch's worktree, a repository, an agent's title, a machine. */
export function nameOf(page: ListPageId, item: unknown): string {
  switch (page) {
    case 'tasks':
      return (item as TaskView).name
    case 'repositories':
      return (item as MergedRepository).slug
    case 'worktrees':
      return (item as Worktree).task
    case 'agents':
      return agentTitle(item as Agent)
    case 'machines':
      return (item as MachineView).machine.machine
  }
}
