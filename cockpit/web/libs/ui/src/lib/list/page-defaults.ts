// What a list derives from its `page` alone: the noun, the rows, the quick-filter chips with their words,
// the address of an entity's own page and the name `c` copies. Labels and hints of the chips live here
// keyed by page and chip id until the vocabulary in fleet-data carries them (a note for that library).

import {
  Agent,
  FleetModel,
  ListPageId,
  ListRow,
  MachineView,
  MergedRepository,
  TaskView,
  VOCABULARY,
  Worktree,
  AppLink,
  agentDetailLink,
  agentTitle,
  machineDetailLink,
  repositoryDetailLink,
  taskDetailLink,
  worktreeDetailLink,
} from '@cockpit/fleet-data'
import { ListChip } from './list-state'

export const PAGE_NOUN: Readonly<Record<ListPageId, string>> = {
  tasks: 'tasks',
  repositories: 'repositories',
  worktrees: 'worktrees',
  agents: 'agents',
  machines: 'machines',
}

const THIS_MACHINE = 'known for this machine only'

type Words = Readonly<Record<string, { label: string; hint?: string }>>

export const CHIP_WORDS: Readonly<Record<ListPageId, Words>> = {
  tasks: {
    'needs-you': { label: 'Needs you', hint: 'The tasks Home lists under "Needs you"' },
    ready: { label: 'Ready to land' },
    working: { label: 'Working' },
    agent: { label: 'Has an agent' },
    pr: { label: 'Has a pull request' },
    multirepo: { label: 'Several repositories' },
    idle30: { label: 'Idle 30 d+', hint: 'No activity for more than 30 days' },
  },
  repositories: {
    worktrees: { label: 'Has worktrees' },
    agents: { label: 'Has agents' },
    prs: { label: 'Open pull requests' },
    index: { label: 'Index needs attention', hint: 'The code index is stale, diverged or failed' },
    errors: { label: 'Has errors' },
  },
  worktrees: {
    active: { label: 'Active' },
    orphaned: { label: 'Orphaned' },
    unpushed: { label: 'Unpushed', hint: `Commits not pushed; ${THIS_MACHINE}` },
    gone: { label: 'Upstream gone', hint: `The upstream branch vanished; ${THIS_MACHINE}` },
    pr: { label: 'Pull request' },
    idle30: { label: 'Idle 30 d+', hint: 'No activity for more than 30 days' },
    safe: { label: 'Safe to clean', hint: 'Home’s cleanup: worktrees that are safe to remove' },
    look: { label: 'Look first', hint: 'Home’s cleanup: worktrees to look at before removing' },
  },
  agents: {
    running: { label: 'Running' },
    blocked: { label: 'Blocked' },
  },
  machines: {
    stale: { label: 'Stale', hint: 'The snapshot is older than 24 hours' },
    outdated: { label: 'Outdated', hint: 'Runs an older WB than the newest in the fleet' },
  },
}

/** The chips of a page, from the vocabulary's ids and the words above; an id without words is shown as its id. */
export function chipsOf(page: ListPageId): ListChip[] {
  return VOCABULARY[page].chips.map((id) => ({ id, label: CHIP_WORDS[page][id]?.label ?? id, hint: CHIP_WORDS[page][id]?.hint }))
}

/** The rows the view model prepared for a page. */
export function rowsOf(model: FleetModel, page: ListPageId): readonly ListRow<unknown>[] {
  switch (page) {
    case 'tasks':
      return model.taskRows
    case 'repositories':
      return model.repositoryRows
    case 'worktrees':
      return model.worktreeRows
    case 'agents':
      return model.agentRows
    case 'machines':
      return model.machineRows
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
