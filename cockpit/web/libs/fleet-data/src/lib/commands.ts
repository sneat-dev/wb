// The rest of the "Copy command" templates of REQ:copy-the-command (the
// per-entity and New task ones), as pure functions over the core in
// `command-core.ts` (quoting, placeholders, targets and the few templates the
// first page needs, which this module re-exports). Pages import this entry point
// (`@cockpit/fleet-data/commands`) lazily.

import { CommandTarget, CopyCommand, PLACEHOLDERS, Part, command, onThisMachine, wb } from './command-core'
import { MatchEnv, matchesTerms } from './match'
import { MAX_QUERY_LENGTH, parseQuery } from './matcher'

export * from './command-core'

// ---- worktree and task ----

export function worktreeList(task: string, target: CommandTarget = {}): CopyCommand {
  return command(target, [...wb('worktree', 'list'), { value: task }])
}

/** `wb pr create 'task' --commit-all --message='<message>'`: commits and opens the pull request. */
export function pullRequestCreate(task: string, target: CommandTarget, message: string = PLACEHOLDERS.message): CopyCommand {
  return onThisMachine(target, () => command(target, [...wb('pr', 'create'), { value: task }, { word: '--commit-all' }, { flag: '--message', value: message }]))
}

/**
 * The fleet-wide cleanup's dry-run plan: `wb worktree gc`, which plans by default and retires only with `--apply`
 * (never part of a template). It names no task, so nothing in it is left to edit.
 */
export function worktreeGc(target: CommandTarget = {}): CopyCommand {
  return command(target, wb('worktree', 'gc'))
}

/** The dry-run plan; never with `--apply`. */
export function worktreeCleanup(task: string, target: CommandTarget = {}): CopyCommand {
  return command(target, [...wb('worktree', 'cleanup'), { value: task }])
}

// ---- pull request ----


// ---- repository ----

/** What `wb worktree create` takes besides the task and repositories; the verb requires `--model` and `--original-prompt-file`. */
export interface CreateOptions {
  model?: string
  promptFile?: string
  base?: string
}

/**
 * `wb worktree create 'task' '<owner/repository>'... --model='<model>'
 * --original-prompt-file='<file>'` with `--base` when given. Both required
 * flags are always present, as the operator's placeholders until known.
 */
export function worktreeCreate(task: string, repositories: readonly string[], options: CreateOptions = {}, target: CommandTarget = {}): CopyCommand {
  return command(target, [
    ...wb('worktree', 'create'),
    { value: task },
    ...repositories.map((repository): Part => ({ value: repository })),
    { flag: '--model', value: options.model || PLACEHOLDERS.model },
    { flag: '--original-prompt-file', value: options.promptFile || PLACEHOLDERS.promptFile },
    ...(options.base ? [{ flag: '--base', value: options.base }] : []),
  ])
}

export function branchList(repository: string, branch?: string, target: CommandTarget = {}): CopyCommand {
  return command(target, [...wb('branch', 'list'), { flag: '--repo', value: repository }, ...(branch ? [{ flag: '--branch', value: branch }] : [])])
}


// ---- branch ----

/** The dry-run plan; never with `--apply`. */
export function branchCleanup(repository: string, branch: string, target: CommandTarget = {}): CopyCommand {
  return command(target, [...wb('branch', 'cleanup'), { flag: '--repo', value: repository }, { flag: '--branch', value: branch }])
}

// ---- dispatched run and session ----

export function agentStatus(agentId: string, target: CommandTarget = {}): CopyCommand {
  return command(target, [...wb('agent', 'status'), { value: agentId }])
}

export function agentLogs(agentId: string, target: CommandTarget = {}): CopyCommand {
  return command(target, [...wb('agent', 'logs'), { value: agentId }])
}

export function agentStop(agentId: string, target: CommandTarget): CopyCommand {
  return onThisMachine(target, () => command(target, [...wb('agent', 'stop'), { value: agentId }]))
}

/** The sessions registered on a machine (read only): where a session's id and state can be seen. */
export function sessionList(target: CommandTarget = {}): CopyCommand {
  return command(target, wb('session', 'list'))
}

/** A recorded successor session only: `wb session send '<wb-session-id>' --message='<message>'`. */
export function sessionSend(sessionId: string, target: CommandTarget, message: string = PLACEHOLDERS.message): CopyCommand {
  return onThisMachine(target, () => command(target, [...wb('session', 'send'), { value: sessionId }, { flag: '--message', value: message }]))
}

// ---- new task ----

/**
 * The dispatch form of the "New task" form for one repository; the profile is a
 * placeholder because profiles are named in `wb.yaml`, and the model is not
 * passed because the verb has no such flag.
 */
export function agentDispatch(
  repository: string,
  task: string,
  options: { profile?: string; base?: string; brief?: string } = {},
  target: CommandTarget = {},
): CopyCommand {
  return command(target, [
    ...wb('agent', 'dispatch'),
    { flag: '--repo', value: repository },
    // `--task` is the text of the task prompt (the brief), not the task name; the name is the worktree.
    { flag: '--task', value: options.brief || PLACEHOLDERS.brief, multiline: true },
    { flag: '--profile', value: options.profile || PLACEHOLDERS.profile },
    { flag: '--new-worktree', value: task },
    ...(options.base ? [{ flag: '--base', value: options.base }] : []),
  ])
}

/** The names the "New task" picker offers: `owner/name` made of letters, digits, dots, underscores and hyphens. */
export const PICKABLE_REPOSITORY = /^[A-Za-z0-9._-]+\/[A-Za-z0-9._-]+$/

/** The picker's matches for a filter text: only pickable names, narrowed by the wildcard matcher over the name. */
export function pickRepositories(names: readonly string[], query: string, now: number): string[] {
  const terms = parseQuery(query)
  const env: MatchEnv = { declared: new Set(), exact: new Set(), now }
  return names.filter((name) => PICKABLE_REPOSITORY.test(name) && matchesTerms(terms, { bare: [name.toLowerCase()], fields: {} }, env))
}

/** The form's answers. */
export interface NewTaskForm {
  /** The task name: the worktree and branch name. */
  task: string
  /** The text of the task prompt, for `wb agent dispatch --task`; may be several lines. With none, the form gives the creation command. */
  brief: string
  repositories: readonly string[]
  base?: string
  /** Required for the creation command (the verb requires `--model`, and `unknown` is its explicit value); `wb agent dispatch` has no such flag. */
  model: string
  /** The agent profile of `wb agent dispatch --profile` (named in `wb.yaml`); empty: a placeholder, which is all the page cannot know. */
  profile?: string
  /** Where the commands run (an SSH route for another machine); none, or `{}`, for this machine. */
  target?: CommandTarget
}

/**
 * The commands of the form. `wb agent dispatch --new-worktree` creates the worktree itself (through `wb worktree create`'s
 * own code, and `wb worktree create` never reuses a worktree that exists), so a creation command before it would make it
 * fail: with a brief the form gives the dispatch commands alone, and without one the creation command alone.
 */
export interface NewTaskCommands {
  /** Without a brief: the creation command for every repository at once. Absent with a brief. */
  create?: CopyCommand
  /** With a brief: one dispatch command per repository, which creates its worktree. Empty without one. */
  dispatch: CopyCommand[]
}

/** The commands the "New task" form produces; with no repositories or a refused value (or, without a brief, no model), a refusal with its reason. */
export function newTaskCommands(form: NewTaskForm): NewTaskCommands {
  const dispatching = form.brief.trim() !== ''
  if (!dispatching && form.model.trim() === '') {
    const refusal: CopyCommand = { ok: false, reason: 'a model is required to create the worktrees (the verb requires --model; "unknown" is its explicit value)' }
    return { create: refusal, dispatch: [refusal] }
  }
  if (form.repositories.length === 0) {
    const refusal: CopyCommand = { ok: false, reason: 'choose at least one repository' }
    return { create: refusal, dispatch: [refusal] }
  }
  const named = form.repositories.find((name) => !PICKABLE_REPOSITORY.test(name))
  if (named !== undefined) {
    const refusal: CopyCommand = { ok: false, reason: `"${named.slice(0, MAX_QUERY_LENGTH)}" is not an owner/name made of letters, digits, dots, underscores and hyphens` }
    return { create: refusal, dispatch: [refusal] }
  }
  if (!dispatching) return { create: worktreeCreate(form.task, form.repositories, { model: form.model, base: form.base }, form.target), dispatch: [] }
  return { dispatch: form.repositories.map((repository) => agentDispatch(repository, form.task, { base: form.base, brief: form.brief, profile: form.profile }, form.target)) }
}

// ---- fleet health ----
