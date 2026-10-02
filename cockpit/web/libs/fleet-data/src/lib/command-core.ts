// The "Copy command" templates of REQ:copy-the-command, as pure functions. Each
// one is an exact `wb` invocation that exists in the command manifest
// (`ai/capabilities.json`; a unit test parses every template against it, with
// the flags it needs), built only from identifiers in the read model and
// `<<<edit:name>>>` placeholders, which are left bare and unquoted. A placeholder
// is a shell syntax error wherever it stands (bash, zsh and POSIX sh alike), so
// pasting a template unedited fails to parse instead of running (the entry is
// flagged `needsEdit`). Every other interpolated value is POSIX single-quoted,
// flags take the `--flag=value` form, and a value with a control character, or
// one that starts with `-`, is refused: nothing is ever copied that could be
// read as an option or break out of its quotes. Nothing here executes anything.

import { MachineRoute } from './fleet.types'

/**
 * What the operator supplies, shown as a placeholder until known. Each is
 * `<<<edit:name>>>`: `<<<` opens a here-string and `>>>` is a redirection with
 * no target, so the shell refuses the whole line wherever the placeholder
 * stands, even in the middle of a command or after `--flag=`. (A shorter
 * `<<edit:name>>` is NOT enough: followed by another word, `>>` takes that word
 * as its file and the line parses.) The UI marks these exact values.
 */
export const PLACEHOLDERS = {
  message: '<<<edit:message>>>',
  model: '<<<edit:model>>>',
  promptFile: '<<<edit:file>>>',
  profile: '<<<edit:profile>>>',
  hubUrl: '<<<edit:hub-url>>>',
  brief: '<<<edit:brief>>>',
  task: '<<<edit:task>>>',
} as const

const PLACEHOLDER_VALUES: ReadonlySet<string> = new Set(Object.values(PLACEHOLDERS))

// Control characters, the bidirectional and invisible characters that can reorder or hide text, the no-break and
// other space look-alikes (U+00A0, U+2000-200A, U+3000), soft hyphen, word joiners and invisible operators
// (U+2060-2064), the blank Hangul fillers (U+115F, U+3164), variation selectors and the tag characters.
const INVISIBLE =
  '\\u007f-\\u009f\\u00a0\\u00ad\\u061c\\u115f\\u2000-\\u200f\\u202a-\\u202e\\u2060-\\u2064\\u2066-\\u2069\\u3000\\u3164\\ufe00-\\ufe0f\\ufeff\\u{e0000}-\\u{e007f}\\u{e0100}-\\u{e01ef}'
// The variation selectors and tag characters are exactly what this refuses, so the rule that flags them in a class is off here.
// eslint-disable-next-line no-misleading-character-class
const FORBIDDEN = new RegExp(`[\\u0000-\\u001f\\u2028\\u2029${INVISIBLE}]`, 'u')
// A multi-line text (a brief) may hold tabs and line breaks, and nothing else of the above.
// eslint-disable-next-line no-misleading-character-class
const FORBIDDEN_IN_TEXT = new RegExp(`[\\u0000-\\u0008\\u000b\\u000c\\u000e-\\u001f\\u2028\\u2029${INVISIBLE}]`, 'u')

/** Why a value cannot be copied into a command; undefined when it can. `multiline` allows tabs and line breaks (a brief). */
export function valueProblem(value: string, multiline = false): string | undefined {
  if (value === '') return 'an empty value cannot be copied into a command'
  if ((multiline ? FORBIDDEN_IN_TEXT : FORBIDDEN).test(value)) return 'a value with a control character is not copied into a command'
  if (value.startsWith('-')) return 'a value that starts with "-" could be read as an option, so it is not copied into a command'
  return undefined
}

/** The value as one POSIX shell word: single-quoted, with each inner quote written `'\''`. */
export function shellQuote(value: string): string {
  return `'${value.replace(/'/g, () => `'\\''`)}'`
}

/** A word the shell reads literally without quotes. */
const SHELL_SAFE = /^[A-Za-z0-9_@+:,./-]+$/

/** How to reach a machine that has an SSH route (REQ:remote-ssh-fetch); `wbPath` empty means `wb`. */
export interface SshRoute {
  host: string
  /** Empty or absent when the configuration has none: the destination is then just the host. */
  user?: string
  wbPath?: string
}

/** Where a command is to run. A machine other than this one is named; one with an SSH route gets the ssh prefix. */
export interface CommandTarget {
  /** The machine's name; undefined for this machine. */
  machine?: string
  ssh?: SshRoute
}

/** The SSH route of a machine from the session response (owner-only), when it has one. */
export function sshRouteOf(routes: readonly MachineRoute[] | undefined, machineId: string): SshRoute | undefined {
  const route = routes?.find((candidate) => candidate.machine_id === machineId)
  return route && { host: route.ssh.host, user: route.ssh.user, wbPath: route.ssh.wb_path }
}

/**
 * Where an entity's command runs: here for this machine's own entries, else
 * through the machine's SSH route when the session carries one (anonymous readers
 * get none), else labelled "run on <machine>".
 */
export function commandTarget(entry: { route: string; machine: string; machine_id: string }, routes?: readonly MachineRoute[]): CommandTarget {
  if (entry.route === 'local') return {}
  return { machine: entry.machine, ssh: sshRouteOf(routes, entry.machine_id) }
}

export type CopyCommand =
  | {
      ok: true
      /** The text to copy. */
      text: string
      /** "run on <machine>" for another machine without an SSH route; absent otherwise. */
      label?: string
      /** The text holds a placeholder the operator must replace: pasted unedited it is a shell syntax error. */
      needsEdit: boolean
      /**
       * An ssh command with a placeholder: the remote shell splits the arguments again, so what the operator types
       * in place of the placeholder must be quoted twice (a placeholder cannot sit inside quotes of its own: it
       * would then parse unedited, which is what it exists to prevent). The page says so beside the command.
       */
      quoteTwice?: boolean
    }
  | { ok: false; reason: string }

/** One piece of a command: a fixed word, an interpolated value, or a `--flag=value`; `multiline` is for a brief. */
export type Part = { word: string } | { value: string; multiline?: boolean } | { flag: string; value: string; multiline?: boolean }

/**
 * One interpolated value as the text to copy. Locally it is single-quoted. For
 * ssh, whose arguments the remote shell splits again, a value that is not
 * shell-safe is quoted twice, so the remote shell reads it as the same one word.
 */
function quoted(value: string, remote: boolean): string {
  const once = shellQuote(value)
  return remote && !SHELL_SAFE.test(value) ? shellQuote(once) : once
}

function render(parts: readonly Part[], remote: boolean): { ok: true; words: string[]; needsEdit: boolean } | { ok: false; reason: string } {
  const words: string[] = []
  let needsEdit = false
  for (const part of parts) {
    if ('word' in part) {
      words.push(part.word)
      continue
    }
    // A placeholder is left bare: an unedited paste is a shell syntax error.
    const placeholder = PLACEHOLDER_VALUES.has(part.value)
    const problem = placeholder ? undefined : valueProblem(part.value, part.multiline)
    if (problem) return { ok: false, reason: problem }
    needsEdit ||= placeholder
    const text = placeholder ? part.value : quoted(part.value, remote)
    words.push('flag' in part ? `${part.flag}=${text}` : text)
  }
  return { ok: true, words, needsEdit }
}

/** `ssh [<user>@]<host> <wb_path> <arguments>`, refusing a route with a hostile part. */
function sshCommand(route: SshRoute, words: readonly string[], needsEdit: boolean): CopyCommand {
  const user = route.user ?? ''
  for (const value of [route.host, user === '' ? 'x' : user, route.wbPath || 'wb']) {
    const problem = valueProblem(value)
    if (problem) return { ok: false, reason: problem }
  }
  const destination = user === '' ? route.host : `${user}@${route.host}`
  const executable = route.wbPath || 'wb'
  return {
    ok: true,
    needsEdit,
    ...(needsEdit ? { quoteTwice: true } : {}),
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
export function command(target: CommandTarget, parts: readonly Part[]): CopyCommand {
  const rendered = render(parts, target.ssh !== undefined)
  if (!rendered.ok) return rendered
  if (target.ssh !== undefined) return sshCommand(target.ssh, rendered.words, rendered.needsEdit)
  const text = rendered.words.join(' ')
  const { needsEdit } = rendered
  return target.machine === undefined ? { ok: true, text, needsEdit } : { ok: true, text, needsEdit, label: `run on ${target.machine}` }
}

/**
 * The one place that keeps a command which changes something on this machine's own entries. A target with a machine
 * (another machine, with or without an SSH route) gets a refusal instead of a command, so no page can offer to push,
 * land, stop or send from here to a machine it only reads. Reading commands never go through this. Every builder that
 * goes through it takes the target as a required parameter, so a caller cannot leave it out and skip the guard.
 */
export function onThisMachine(target: CommandTarget, build: () => CopyCommand): CopyCommand {
  if (target.machine === undefined && target.ssh === undefined) return build()
  return { ok: false, reason: `this changes things, so it is only offered for this machine's own entries${target.machine === undefined ? '' : `: it is ${target.machine}'s, run it there`}` }
}

export const wb = (...words: string[]): Part[] => ['wb', ...words].map((word) => ({ word }))

// ---- the templates the first page needs: Home's ready-to-land and fleet-health lines ----

/** `wb pr land`: only for a pull request of this machine (a refusal for another machine's). */
export function pullRequestLand(repository: string, number: number, target: CommandTarget): CopyCommand {
  return onThisMachine(target, () => command(target, [...wb('pr', 'land'), { value: `${repository}#${number}` }]))
}

export function fleetStatus(repository: string, target: CommandTarget = {}): CopyCommand {
  return command(target, [...wb('fleet', 'status'), { flag: '--filter', value: repository }])
}

export function remotePublish(target: CommandTarget = {}): CopyCommand {
  return command(target, wb('remote', 'publish'))
}

/** What this machine's own publish would send, without sending it: the first thing to run after a `collect_failed`. */
export function remotePublishDryRun(target: CommandTarget = {}): CopyCommand {
  return command(target, [...wb('remote', 'publish'), { word: '--dry-run' }])
}

/** The cross-machine worklist from the store, which fails when the store cannot be opened. */
export function remoteStatus(target: CommandTarget = {}): CopyCommand {
  return command(target, wb('remote', 'status'))
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
