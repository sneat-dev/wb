// The three reads that only some pages make, as functions of the fetch (`inject(FETCH)`): a repository's
// branches, a machine's metrics samples and a repository's README. They are in their own entry point,
// `@cockpit/fleet-data/lazy-client`, which a page imports with `import()`, so the first page does not carry
// them (REQ:initial-script-fits-the-budget).

import { FleetFormatError, FleetRequestError, REQUEST_TIMEOUT_MS, ReadmeRequestError } from './fleet-client'
import { BRANCHES_PATH, BranchesResponse, MACHINE_METRICS_PATH, MachineMetrics, README_PATH } from './fleet.types'

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
