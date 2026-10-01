import { Agent, formatAge, isRunning } from '@cockpit/fleet-data'

const MINUTE = 60_000
const HOUR = 60 * MINUTE
const DAY = 24 * HOUR

/** The parse of an RFC 3339 time; none for an absent or unreadable one (never a guess). */
export function timeOf(value: string | undefined): number | undefined {
  const time = Date.parse(value ?? '')
  return Number.isNaN(time) ? undefined : time
}

/** How long a span lasted, in the largest whole unit: `5 min`, `2 h`, `3 d` (under a minute: `under 1 min`). */
export function formatSpan(milliseconds: number): string {
  if (milliseconds < MINUTE) return 'under 1 min'
  if (milliseconds < HOUR) return `${Math.floor(milliseconds / MINUTE)} min`
  if (milliseconds < DAY) return `${Math.floor(milliseconds / HOUR)} h`
  return `${Math.floor(milliseconds / DAY)} d`
}

/** The human label of an agent: its runtime and model ("claude · sonnet-5-5"), "agent" when it reports neither. */
export function agentName(agent: Pick<Agent, 'runtime' | 'model'>): string {
  return [agent.runtime, agent.model].filter((part) => part).join(' · ') || 'agent'
}

/** What a session or run with no work link says instead of the work: "session, started 2 h ago" ("session" with no start time). */
export function agentFallback(agent: Pick<Agent, 'kind' | 'started_at'>, now: number): string {
  return timeOf(agent.started_at) === undefined ? agent.kind : `${agent.kind}, started ${formatAge(agent.started_at, now)}`
}

/** The opening word of a run that ended, by its state. */
const ENDED: Readonly<Record<string, string>> = { completed: 'Finished', failed: 'Finished', timeout: 'Timed out', abandoned: 'Abandoned' }

/**
 * The moment an agent's span is measured to: now for an entry of this machine, the snapshot's time for one read from a
 * cache (it ran that long as of then; nothing newer is known).
 */
export function referenceOf(agent: Pick<Agent, 'route' | 'observed_at'>, now: number): number {
  return (agent.route === 'cached' ? timeOf(agent.observed_at) : undefined) ?? now
}

/**
 * What the Running for column says: how long a running agent has run ("2 h"), when a finished run ended
 * ("3 h ago"), when anything else started ("started 3 d ago"); empty when the agent reports no time.
 */
export function timeCell(agent: Agent, now: number): string {
  const at = referenceOf(agent, now)
  const started = timeOf(agent.started_at)
  if (isRunning(agent)) return started === undefined ? '' : formatSpan(Math.max(0, at - started))
  if (timeOf(agent.finished_at) !== undefined) return formatAge(agent.finished_at, now)
  return started === undefined ? '' : `started ${formatAge(agent.started_at, now)}`
}

/** What is known, in plain words, for the header of the panel: "Running for 2 h on mac", "Finished 3 h ago, exit code 1". */
export function agentHeadline(agent: Agent, now: number): string {
  const at = referenceOf(agent, now)
  const asOf = agent.route === 'cached' ? ` (as of its snapshot, ${formatAge(agent.observed_at, now)})` : ''
  const started = timeOf(agent.started_at)
  const exit = agent.exit_code === undefined ? '' : `, exit code ${agent.exit_code}`
  if (isRunning(agent)) return started === undefined ? `Running on ${agent.machine}${asOf}` : `Running for ${formatSpan(Math.max(0, at - started))} on ${agent.machine}${asOf}`
  if (agent.state === 'parked') return started === undefined ? `Parked on ${agent.machine}` : `Parked on ${agent.machine}, started ${formatAge(agent.started_at, now)}`
  const word = ENDED[agent.state]
  if (word === undefined) return `State “${agent.state}” on ${agent.machine}`
  if (timeOf(agent.finished_at) === undefined) return `${word}${exit}; no end time reported`
  return `${word} ${formatAge(agent.finished_at, now)}${exit}`
}
