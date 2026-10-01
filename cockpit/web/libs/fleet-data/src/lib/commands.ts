// The "Copy command" templates of REQ:copy-the-command, as pure functions. Each
// one is an exact `wb` invocation that exists in the command manifest
// (`ai/capabilities.json`; a unit test parses every template against it, with
// the flags it needs), built only from identifiers in the read model and
// angle-bracket placeholders. Every interpolated value is POSIX single-quoted,
// flags take the `--flag=value` form, and a value with a control character, or
// one that starts with `-`, is refused: nothing is ever copied that could be
// read as an option or break out of its quotes. Nothing here executes anything.

import { MAX_QUERY_LENGTH, MatchEnv, matchesTerms, parseQuery } from './matcher'

/** What the operator supplies, shown as a placeholder in angle brackets until known. */
export const PLACEHOLDERS = {
  message: '<message>',
  model: '<model>',
  promptFile: '<file>',
  profile: '<profile>',
  hubUrl: '<hub-url>',
} as const

// Control characters, and the bidirectional controls that can reorder text on screen.
// eslint-disable-next-line no-control-regex
const FORBIDDEN = /[\u0000-\u001f\u007f-\u009f‎‏‪-‮⁦-⁩]/

/** Why a value cannot be copied into a command; undefined when it can. */
export function valueProblem(value: string): string | undefined {
  if (value === '') return 'an empty value cannot be copied into a command'
  if (FORBIDDEN.test(value)) return 'a value with a control character is not copied into a command'
  if (value.startsWith('-')) return 'a value that starts with "-" could be read as an option, so it is not copied into a command'
  return undefined
}

/** The value as one POSIX shell word: single-quoted, with each inner quote written `'\''`. */
export function shellQuote(value: string): string {
  return `'${value.replace(/'/g, () => `'\\''`)}'`
}

/** A word the shell reads literally without quotes. */
const SHELL_SAFE = /^[A-Za-z0-9_@%+=:,./-]+$/

/** How to reach a machine that has an SSH route (REQ:remote-ssh-fetch); `wbPath` empty means `wb`. */
export interface SshRoute {
  host: string
  user: string
  wbPath?: string
}

/** Where a command is to run. A machine other than this one is named; one with an SSH route gets the ssh prefix. */
export interface CommandTarget {
  /** The machine's name; undefined for this machine. */
  machine?: string
  ssh?: SshRoute
}

export type CopyCommand =
  | {
      ok: true
      /** The text to copy. */
      text: string
      /** "run on <machine>" for another machine without an SSH route; absent otherwise. */
      label?: string
    }
  | { ok: false; reason: string }

/** One piece of a command: a fixed word, an interpolated value, or a `--flag=value`. */
type Part = { word: string } | { value: string } | { flag: string; value: string }

/**
 * One interpolated value as the text to copy. Locally it is single-quoted. For
 * ssh, whose arguments the remote shell splits again, a value that is not
 * shell-safe is quoted twice, so the remote shell reads it as the same one word.
 */
function quoted(value: string, remote: boolean): string {
  const once = shellQuote(value)
  return remote && !SHELL_SAFE.test(value) ? shellQuote(once) : once
}

function render(parts: readonly Part[], remote: boolean): { ok: true; words: string[] } | { ok: false; reason: string } {
  const words: string[] = []
  for (const part of parts) {
    if ('word' in part) {
      words.push(part.word)
      continue
    }
    const problem = valueProblem(part.value)
    if (problem) return { ok: false, reason: problem }
    words.push('flag' in part ? `${part.flag}=${quoted(part.value, remote)}` : quoted(part.value, remote))
  }
  return { ok: true, words }
}

/** `ssh <user>@<host> <wb_path> <arguments>`, refusing a route with a hostile part. */
function sshCommand(route: SshRoute, words: readonly string[]): CopyCommand {
  for (const value of [route.host, route.user, route.wbPath ?? 'wb']) {
    const problem = valueProblem(value)
    if (problem) return { ok: false, reason: problem }
  }
  const destination = `${route.user}@${route.host}`
  const executable = route.wbPath ?? 'wb'
  return {
    ok: true,
    text: [
      'ssh',
      SHELL_SAFE.test(destination) ? destination : shellQuote(destination),
      SHELL_SAFE.test(executable) ? executable : shellQuote(shellQuote(executable)),
      ...words.slice(1),
    ].join(' '),
  }
}

/**
 * Builds the command for a target. For another machine without an SSH route the
 * text is the command itself, labelled "run on <machine>"; with one it is the
 * ssh form above.
 */
function command(target: CommandTarget, parts: readonly Part[]): CopyCommand {
  const rendered = render(parts, target.ssh !== undefined)
  if (!rendered.ok) return rendered
  if (target.ssh !== undefined) return sshCommand(target.ssh, rendered.words)
  const text = rendered.words.join(' ')
  return target.machine === undefined ? { ok: true, text } : { ok: true, text, label: `run on ${target.machine}` }
}

const wb = (...words: string[]): Part[] => ['wb', ...words].map((word) => ({ word }))

// ---- worktree and task ----

export function worktreeList(task: string, target: CommandTarget = {}): CopyCommand {
  return command(target, [...wb('worktree', 'list'), { value: task }])
}

/** `wb pr create '<task>' --commit-all --message='<message>'`: commits and opens the pull request. */
export function pullRequestCreate(task: string, message: string = PLACEHOLDERS.message, target: CommandTarget = {}): CopyCommand {
  return command(target, [...wb('pr', 'create'), { value: task }, { word: '--commit-all' }, { flag: '--message', value: message }])
}

/** The dry-run plan; never with `--apply`. */
export function worktreeCleanup(task: string, target: CommandTarget = {}): CopyCommand {
  return command(target, [...wb('worktree', 'cleanup'), { value: task }])
}

// ---- pull request ----

export function pullRequestLand(repository: string, number: number, target: CommandTarget = {}): CopyCommand {
  return command(target, [...wb('pr', 'land'), { value: `${repository}#${number}` }])
}

// ---- repository ----

/** What `wb worktree create` takes besides the task and repositories; the verb requires `--model` and `--original-prompt-file`. */
export interface CreateOptions {
  model?: string
  promptFile?: string
  base?: string
}

/**
 * `wb worktree create '<task>' '<owner/repository>'... --model='<model>'
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

export function fleetStatus(repository: string, target: CommandTarget = {}): CopyCommand {
  return command(target, [...wb('fleet', 'status'), { flag: '--filter', value: repository }])
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

export function agentStop(agentId: string, target: CommandTarget = {}): CopyCommand {
  return command(target, [...wb('agent', 'stop'), { value: agentId }])
}

/** A recorded successor session only: `wb session send '<wb-session-id>' --message='<message>'`. */
export function sessionSend(sessionId: string, message: string = PLACEHOLDERS.message, target: CommandTarget = {}): CopyCommand {
  return command(target, [...wb('session', 'send'), { value: sessionId }, { flag: '--message', value: message }])
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
  options: { profile?: string; base?: string } = {},
  target: CommandTarget = {},
): CopyCommand {
  return command(target, [
    ...wb('agent', 'dispatch'),
    { flag: '--repo', value: repository },
    { flag: '--task', value: task },
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
  task: string
  repositories: readonly string[]
  base?: string
  /** Required: the verb requires `--model`, and `unknown` is its explicit value. */
  model: string
}

export interface NewTaskCommands {
  /** The creation command for every repository at once. */
  create: CopyCommand
  /** One dispatch command per repository. */
  dispatch: CopyCommand[]
}

/** The commands the "New task" form produces; with no model, no repositories or a refused value, a refusal with its reason. */
export function newTaskCommands(form: NewTaskForm): NewTaskCommands {
  if (form.model.trim() === '') {
    const refusal: CopyCommand = { ok: false, reason: 'a model is required (the verb requires --model; "unknown" is its explicit value)' }
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
  return {
    create: worktreeCreate(form.task, form.repositories, { model: form.model, base: form.base }),
    dispatch: form.repositories.map((repository) => agentDispatch(repository, form.task, { base: form.base })),
  }
}

// ---- fleet health ----

export function remotePublish(target: CommandTarget = {}): CopyCommand {
  return command(target, wb('remote', 'publish'))
}

export function selfUpdate(target: CommandTarget = {}): CopyCommand {
  return command(target, wb('self-update'))
}

export function daemonStart(target: CommandTarget = {}): CopyCommand {
  return command(target, wb('daemon', 'start'))
}

/** For a refused HTTP read: enrol this machine with the hub, the token read from standard input. */
export function remoteEnroll(hubUrl: string = PLACEHOLDERS.hubUrl, target: CommandTarget = {}): CopyCommand {
  return command(target, [...wb('remote', 'enroll'), { flag: '--url', value: hubUrl }, { word: '--token-stdin' }])
}

/** The export to try for a remote error. The verb is added by the export task; until then the manifest test lists it as pending. */
export function cockpitExport(target: CommandTarget = {}): CopyCommand {
  return command(target, [...wb('cockpit', 'export'), { flag: '--format', value: 'json' }])
}
