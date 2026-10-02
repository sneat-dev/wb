import { ParamMap } from '@angular/router'
import { FleetDocument, FleetModel, PanelCommand, SshRoute } from '@cockpit/fleet-data'
import { PICKABLE_REPOSITORY, newTaskCommands, valueProblem } from '@cockpit/fleet-data/commands'

/** The form's answers that live in the address (the brief does not: it stays in memory). */
export interface NewTaskState {
  /** Chosen `owner/name`s, in the order they were chosen. */
  repositories: string[]
  task: string
  /** Empty: the repository's default branch. */
  base: string
  model: string
  /** The id of another machine to run on; none is this machine. */
  machine: string | undefined
}

/** The model ids the form offers before the fleet's own: free text is always accepted. `unknown` is the verb's explicit value. */
export const KNOWN_MODELS = ['opus', 'sonnet', 'haiku', 'codex', 'gemini', 'unknown'] as const

/** What a task name may be made of: it is a worktree and a branch name. */
export const SAFE_TASK_NAME = /^[A-Za-z0-9._-]+$/

export const EMPTY_STATE: NewTaskState = { repositories: [], task: '', base: '', model: '', machine: undefined }

/** The form of an address: a repository that cannot be picked, or twice, is dropped. */
export function stateOf(params: ParamMap): NewTaskState {
  const repositories = [...new Set(params.getAll('repo').filter((name) => PICKABLE_REPOSITORY.test(name)))]
  return { repositories, task: params.get('task') ?? '', base: params.get('base') ?? '', model: params.get('model') ?? '', machine: params.get('machine') ?? undefined }
}

/** The address of a form: a field that is empty is left out. */
export function queryOf(state: NewTaskState): Record<string, string | string[] | null> {
  return {
    repo: state.repositories.length === 0 ? null : state.repositories,
    task: state.task === '' ? null : state.task,
    base: state.base === '' ? null : state.base,
    model: state.model === '' ? null : state.model,
    machine: state.machine ?? null,
  }
}

/** Why a task name cannot be used, in words; none when it can (or when it is not given yet, which `commandsOf` says). */
export function nameProblem(name: string): string | undefined {
  if (name === '') return undefined
  const library = valueProblem(name)
  if (library !== undefined) return library
  return SAFE_TASK_NAME.test(name) ? undefined : 'a task name is made of letters, digits, dots, underscores and hyphens'
}

/** The models to offer: the known ones, then every model an agent of the fleet reports (most used first), without repeats. */
export function modelsOffered(document: FleetDocument): string[] {
  const counts = new Map<string, number>()
  for (const agent of document.agents) if (agent.model) counts.set(agent.model, (counts.get(agent.model) ?? 0) + 1)
  const reported = [...counts].sort((a, b) => b[1] - a[1] || a[0].localeCompare(b[0])).map(([model]) => model)
  return [...new Set([...KNOWN_MODELS, ...reported])]
}

/** The default branch of what is chosen: the one the repositories share, or none when they differ or none says. */
export function defaultBranchOf(document: FleetDocument, slugs: readonly string[]): string | undefined {
  const branches = new Set(slugs.map((slug) => document.repositories.find((repository) => repository.name === slug)?.default_branch))
  const [only] = branches
  return branches.size === 1 ? only : undefined
}

/** Where the commands run: this machine, or another with an SSH route. */
export interface Target {
  machine?: string
  ssh?: SshRoute
}

const refusal = (reason: string): PanelCommand[] => [{ title: 'Commands', command: { ok: false, reason } }]

/**
 * The commands of the form, from the library's `newTaskCommands` (`wb worktree create` once for every repository,
 * then `wb agent dispatch` once for each), in that order. A field the library refuses (no model, a value that starts
 * with `-` or holds a control character) gives its reason and no command; nothing is ever run.
 * For another machine the form carries its `target`, and the library puts the SSH route in front of each command.
 */
export function commandsOf(state: NewTaskState, brief: string, target: Target = {}): PanelCommand[] {
  if (state.task === '') return refusal('name the task: it is the worktree and the branch')
  const problem = nameProblem(state.task)
  if (problem !== undefined) return refusal(problem)
  const form = { task: state.task, brief, repositories: state.repositories, base: state.base === '' ? undefined : state.base, model: state.model.trim(), target }
  const built = newTaskCommands(form)
  if (!built.create.ok) return refusal(built.create.reason)
  return [
    { title: 'Create the worktrees', command: built.create },
    ...built.dispatch.map((command, index) => ({ title: `Dispatch an agent in ${form.repositories[index]}`, command })),
  ]
}

/** One place the commands can run. */
export interface MachineChoice {
  /** The machine entry id; none for this machine. */
  id: string | undefined
  name: string
  /** The entry whose metrics say whether it can take another agent. */
  metricsId: string | undefined
  target: Target
}

/**
 * This machine, and every other machine the session has an SSH route to (an owner session's `machine_routes`; an
 * anonymous reader has none, so offers only this machine): the only places a copied command can be run from here.
 */
export function machineChoices(model: FleetModel): MachineChoice[] {
  const machines = model.document.machines
  const local = machines.find((machine) => machine.route === 'local')
  const choices: MachineChoice[] = [{ id: undefined, name: local === undefined ? 'This machine' : `This machine (${local.machine})`, metricsId: local?.id, target: {} }]
  for (const machine of machines) {
    const ssh = machine.route === 'local' ? undefined : model.sshRouteFor(machine)
    if (ssh !== undefined) choices.push({ id: machine.id, name: machine.machine, metricsId: machine.id, target: { machine: machine.machine, ssh } })
  }
  return choices
}
