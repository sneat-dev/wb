// Home's sections that are not above the fold: the Cleanup line and its age
// bars, Fleet health (shown only when something is wrong), and the throughput
// numbers of the charts. Free functions over a model, memoised on it, in their
// own entry point (`@cockpit/fleet-data/home-details`) so that the shell and the
// first paint of Home do not carry them: Home imports it after it has rendered
// (`import()`), and the list pages read the cleanup sets through it.

import { CommandTarget, CopyCommand, SshRoute, cockpitExport, daemonStart, fleetStatus, remoteEnroll, remotePublish, selfUpdate } from './command-core'
import type { FleetModel } from './fleet-model'
import { buildRepositories } from './model-repositories'
import { ageTermOf, idleOver30Days, wholeDays } from './match'
import { AGE_TERMS } from './matcher'
import { interimAtRiskWorktrees } from './task-state'
import { Cleanup, FleetHealth, HealthItem, ScanErrorItem, ThroughputSeriesDay, ThroughputSeries } from './view-types'
import { ageLink, chipLink, machineLink } from './vocabulary'

const EMPTY_LINK = { path: '/', query: {} }

function toTime(value: string | undefined): number | undefined {
  const time = value ? Date.parse(value) : Number.NaN
  return Number.isNaN(time) ? undefined : time
}
const EMPTY_CLEANUP: Cleanup = { safeCount: 0, lookCount: 0, safeIds: new Set(), lookIds: new Set(), indicative: true, reviewLink: EMPTY_LINK, bars: [], unknownAge: 0 }
const EMPTY_HEALTH: FleetHealth = { ok: true, staleMachines: [], olderWb: [], remoteErrors: [], exportDropped: [], scanErrors: [] }

const AGE_LABELS: Record<(typeof AGE_TERMS)[number], string> = {
  '<1d': 'today',
  '1-7d': '1 to 7 days',
  '8-30d': '8 to 30 days',
  '31-90d': '31 to 90 days',
  '>90d': 'over 90 days',
}


/** Home "Cleanup": the indicative safe and look counts, the sets behind them, and the age bars. */
export function buildCleanup(model: FleetModel): Cleanup {
  return model.memo('cleanup', EMPTY_CLEANUP, () => {
    const landed = new Set(model.tasks.filter((task) => task.state === 'landed').map((task) => task.name))
    // The worktrees of at-risk tasks that "Needs you" no longer lists because they are old.
    const olderAtRisk = new Set(model.tasks.filter((task) => task.state === 'at-risk' && !model.isRecent(task)).flatMap((task) => interimAtRiskWorktrees(task.worktrees).map((worktree) => worktree.id)))
    const safeIds = new Set<string>()
    const lookIds = new Set<string>()
    const counts = AGE_TERMS.map(() => 0)
    let unknownAge = 0
    for (const worktree of model.document.worktrees) {
      const activity = toTime(worktree.last_activity_at)
      if (landed.has(worktree.task) && worktree.ahead === 0 && worktree.owner_state !== 'active') safeIds.add(worktree.id)
      else if (worktree.owner_state === 'orphaned' || worktree.owner_state === 'unknown' || idleOver30Days(activity, model.now) || olderAtRisk.has(worktree.id)) lookIds.add(worktree.id)
      if (activity === undefined) unknownAge++
      else {
        counts[AGE_TERMS.indexOf(ageTermOf(wholeDays(activity, model.now)))]++
      }
    }
    return {
      safeCount: safeIds.size,
      lookCount: lookIds.size,
      safeIds,
      lookIds,
      indicative: true,
      reviewLink: chipLink('worktrees', 'safe'),
      bars: AGE_TERMS.map((term, index) => ({ term, label: AGE_LABELS[term], count: counts[index], link: ageLink('worktrees', term) })),
      unknownAge,
    }
  })
}


/** Home "Fleet health": shown only when `ok` is false. */
export function buildHealth(model: FleetModel): FleetHealth {
  return model.memo('health', EMPTY_HEALTH, () => {
    const staleMachines: HealthItem[] = []
    const olderWb: HealthItem[] = []
    const remoteErrors: HealthItem[] = []
    const exportDropped: HealthItem[] = []
    for (const view of model.machines) {
      const machine = view.machine
      const base = { machine: machine.machine, machineId: machine.id, link: machineLink('machines', machine.id) }
      // With an SSH route the command is the ssh form, run from here; without one it runs on the machine.
      const ssh = model.sshRouteFor(machine)
      const target: CommandTarget = ssh === undefined ? {} : { ssh }
      if (view.state === 'stale') {
        staleMachines.push({ ...base, text: `${machine.machine} has not published for over 24 hours`, command: healthCommand(remotePublish(target), machine.machine, ssh !== undefined), link: chipLink('machines', 'stale') })
      }
      if (view.outdated) {
        olderWb.push({ ...base, text: `${machine.machine} runs an older WB (${machine.wb_version})`, command: healthCommand(selfUpdate(target), machine.machine, ssh !== undefined), link: chipLink('machines', 'outdated') })
      }
      if (machine.export_dropped !== undefined && machine.export_dropped > 0) {
        const count = machine.export_dropped
        exportDropped.push({ ...base, text: `${count} ${count === 1 ? 'entry' : 'entries'} left out of ${machine.machine}'s export`, command: healthCommand(cockpitExport(target), machine.machine, ssh !== undefined) })
      }
      if (machine.remote_error !== undefined) {
        // Enrolling configures this machine, so it runs here whatever the route.
        const here = ssh !== undefined || machine.remote_error === 'http_auth_failed'
        remoteErrors.push({ ...base, text: `${machine.machine}: ${remoteErrorText(machine.remote_error)}`, command: healthCommand(remoteFix(machine.remote_error, ssh), machine.machine, here) })
      }
    }
    const scanErrors: ScanErrorItem[] = buildRepositories(model)
      .filter((repository) => repository.errors.length > 0)
      .map((repository) => {
        const command = fleetStatus(repository.slug)
        return { repository: repository.slug, command: command.ok ? { text: command.text } : { reason: command.reason }, link: chipLink('repositories', 'errors') }
      })
    return { ok: staleMachines.length + olderWb.length + remoteErrors.length + exportDropped.length + scanErrors.length === 0, staleMachines, olderWb, remoteErrors, exportDropped, scanErrors }
  })
}


/** The two Home charts' numbers; undefined when the document has no throughput block. */
export function buildThroughput(model: FleetModel): ThroughputSeries | undefined {
  return model.memo('throughput', undefined, () => {
    const block = model.document.throughput
    if (block === undefined) return undefined
    const byDate = new Map(block.per_day.map((day) => [day.date, day]))
    const perDay: ThroughputSeriesDay[] = []
    for (let offset = block.window_days - 1; offset >= 0; offset--) {
      const date = isoDay(new Date(model.now - offset * 86_400_000))
      const day = byDate.get(date)
      perDay.push({ date, finished: day?.finished ?? 0, dropped: day?.dropped ?? 0, landed: day?.landed ?? 0 })
    }
    return {
      windowDays: block.window_days,
      perDay,
      totalFinished: perDay.reduce((total, day) => total + day.finished, 0),
      totalDropped: perDay.reduce((total, day) => total + day.dropped, 0),
      maxPerDay: Math.max(0, ...perDay.map((day) => day.finished + day.dropped)),
      hasLanded: perDay.some((day) => day.landed > 0),
      totalLanded: perDay.reduce((total, day) => total + day.landed, 0),
      maxLanded: Math.max(0, ...perDay.map((day) => day.landed)),
      slowest: [...block.slowest].sort((a, b) => b.duration_seconds - a.duration_seconds).slice(0, 5).map((entry) => ({ task: entry.task, durationSeconds: entry.duration_seconds, landedAt: entry.landed_at })),
      medianSeconds: block.median_seconds,
      p90Seconds: block.p90_seconds,
      capped: block.capped === true,
    }
  })
}

function isoDay(date: Date): string {
  return date.toISOString().slice(0, 10)
}

/** The command of a health line, labelled where it runs: "run here" for the ssh form and enrolling, "run on <machine>" otherwise. */
function healthCommand(command: CopyCommand, machine: string, here: boolean): HealthItem['command'] {
  return command.ok ? { text: command.text, label: here ? 'run here' : `run on ${machine}`, needsEdit: command.needsEdit } : { reason: command.reason }
}

/** A `remote_error` in words; a code this page does not know is an unknown error. */
export function remoteErrorText(code: string): string {
  switch (code) {
    case 'remote_warming_up':
      return 'is still warming up and has no export yet'
    case 'export_too_large':
      return 'its export is too large to read'
    case 'http_unavailable':
      return 'cannot be read over HTTP (connection error, timeout, 404, 429, 5xx or a redirect)'
    case 'http_auth_failed':
      return 'the HTTP read was refused (401 or 403) or has no credential'
    case 'ssh_unavailable':
      return 'cannot reach it over ssh (no ssh here, or the host is unreachable)'
    case 'auth_failed':
      return 'ssh login was refused'
    case 'timeout':
      return 'its export timed out'
    case 'wb_missing':
      return 'wb is not found on it'
    case 'wb_too_old':
      return 'its wb is too old to export'
    case 'daemon_not_running':
      return 'its daemon is not running'
    case 'export_refused':
      return 'its daemon refuses anonymous reads'
    case 'bad_payload':
      return 'its export was refused as invalid'
  }
  return 'unknown error'
}

/**
 * The command that fixes (or lets the operator investigate) a `remote_error`:
 * enrolling for a refused HTTP read, starting the daemon, updating wb, and for
 * the others the export to try, through ssh when the machine has an SSH route.
 */
export function remoteFix(code: string, ssh: SshRoute | undefined): CopyCommand {
  if (code === 'http_auth_failed') return remoteEnroll()
  if (code === 'remote_warming_up') return { ok: false, reason: 'nothing to run: it clears when the machine finishes its first scan' }
  const target: CommandTarget = ssh === undefined ? {} : { ssh }
  if (code === 'daemon_not_running') return daemonStart(target)
  if (code === 'wb_too_old') return selfUpdate(target)
  return cockpitExport(target)
}
