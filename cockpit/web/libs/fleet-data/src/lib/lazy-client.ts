// The three reads that only some pages make, as functions of the fetch (`inject(FETCH)`): a repository's
// branches, a machine's metrics samples and a repository's README. They are in their own entry point,
// `@cockpit/fleet-data/lazy-client`, which a page imports with `import()`, so the first page does not carry
// them (REQ:initial-script-fits-the-budget).

import { FleetFormatError, FleetRequestError, REQUEST_TIMEOUT_MS, ReadmeRequestError } from './fleet-client'
import { BRANCHES_PATH, BranchesResponse, MACHINE_METRICS_PATH, MachineMetrics, README_PATH } from './fleet.types'

export const AGENT_SESSION_STATS_PATH = '/api/v1/cockpit/agent-session-stats'

export interface AgentSessionStatsBucket {
  session_id: string
  session_name?: string
  repository: string
  runs: number
  completed: number
  failed: number
  pending: number
  duration_ms: number
  usage_runs: number
  input_tokens: number
  output_tokens: number
  cost_runs: number
  estimated_usd: number
}

export interface AgentSessionStatsReport {
  total: AgentSessionStatsBucket
  by_session_repository: AgentSessionStatsBucket[]
}

function statsBucket(value: unknown): value is AgentSessionStatsBucket {
  if (typeof value !== 'object' || value === null) return false
  const row = value as Record<string, unknown>
  return typeof row['session_id'] === 'string' && typeof row['repository'] === 'string' &&
    (row['session_name'] === undefined || typeof row['session_name'] === 'string') &&
    ['runs', 'completed', 'failed', 'pending', 'duration_ms', 'usage_runs', 'input_tokens', 'output_tokens', 'cost_runs', 'estimated_usd']
      .every((key) => typeof row[key] === 'number' && Number.isFinite(row[key]) && (row[key] as number) >= 0)
}

/** Owner-only local usage: the session/repository rows are already grouped by WB. */
export async function readAgentSessionStats(fetcher: typeof fetch): Promise<AgentSessionStatsReport> {
  const response = await fetcher(AGENT_SESSION_STATS_PATH, {
    headers: { Accept: 'application/json' },
    credentials: 'same-origin',
    signal: AbortSignal.timeout(REQUEST_TIMEOUT_MS),
  })
  if (!response.ok) throw new FleetRequestError(response.status)
  const body: unknown = await response.json().catch(() => null)
  if (typeof body !== 'object' || body === null) throw new Error('Invalid session usage report')
  const report = body as Record<string, unknown>
  if (!statsBucket(report['total']) || !Array.isArray(report['by_session_repository']) ||
    !report['by_session_repository'].every(statsBucket)) throw new Error('Invalid session usage report')
  return body as AgentSessionStatsReport
}

/** The branches of one repository checkout, read lazily when a repository page or panel opens. */
export async function readBranches(fetcher: typeof fetch, repository: string): Promise<BranchesResponse> {
  const response = await fetcher(`${BRANCHES_PATH}?repository=${encodeURIComponent(repository)}`, {
    headers: { Accept: 'application/json' },
    credentials: 'same-origin',
    signal: AbortSignal.timeout(REQUEST_TIMEOUT_MS),
  })
  if (!response.ok) throw new FleetRequestError(response.status)
  const body: unknown = await response.json().catch(() => null)
  const branches = typeof body === 'object' && body !== null ? (body as Record<string, unknown>)['branches'] : undefined
  if (!Array.isArray(branches)) throw new FleetFormatError()
  return body as BranchesResponse
}

/**
 * The samples of one machine: local history, live remote, one cached sample, or none.
 * `signal` cancels the read (the poller does when it stops): the request is aborted and the
 * returned promise rejects with the signal's abort error; the request timeout still applies.
 */
export async function readMachineMetrics(fetcher: typeof fetch, machine: string, signal?: AbortSignal): Promise<MachineMetrics> {
  const timeout = AbortSignal.timeout(REQUEST_TIMEOUT_MS)
  const response = await fetcher(`${MACHINE_METRICS_PATH}?machine=${encodeURIComponent(machine)}`, {
    headers: { Accept: 'application/json' },
    credentials: 'same-origin',
    signal: signal === undefined ? timeout : AbortSignal.any([timeout, signal]),
  })
  if (!response.ok) throw new FleetRequestError(response.status)
  const body: unknown = await response.json().catch(() => null)
  const samples = typeof body === 'object' && body !== null ? (body as Record<string, unknown>)['samples'] : undefined
  if (!Array.isArray(samples)) throw new FleetFormatError()
  return body as MachineMetrics
}

/**
 * Reads a repository's README as Markdown text: the owner-only route, so
 * callers ask only with an owner session. The text is untrusted.
 */
export async function readReadme(fetcher: typeof fetch, repository: string): Promise<string> {
  const response = await fetcher(`${README_PATH}?repository=${encodeURIComponent(repository)}`, {
    headers: { Accept: 'text/markdown' },
    credentials: 'same-origin',
    signal: AbortSignal.timeout(REQUEST_TIMEOUT_MS),
  })
  if (!response.ok) {
    const body: unknown = await response.json().catch(() => null)
    const code = typeof body === 'object' && body !== null ? (body as Record<string, unknown>)['error'] : undefined
    throw new ReadmeRequestError(response.status, typeof code === 'string' ? code : '')
  }
  return response.text()
}
