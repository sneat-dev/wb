import { Injectable, InjectionToken, inject } from '@angular/core'
import {
  BRANCHES_PATH,
  BranchesResponse,
  FLEET_PATH,
  FleetDocument,
  MACHINE_METRICS_PATH,
  MachineMetrics,
  README_PATH,
  SCHEMA_VERSION,
  SESSION_PATH,
  Session,
} from './fleet.types'

/** The fetch the client uses; tests replace it, so no test needs a network. */
export const FETCH = new InjectionToken<typeof fetch>('fetch', {
  providedIn: 'root',
  factory: () => (input, init) => fetch(input, init),
})

/** How long a request may take before it is given up, so a hung one cannot stop polling. */
export const REQUEST_TIMEOUT_MS = 10_000

/** A fleet read that was answered with a status other than 200 or 304. */
export class FleetRequestError extends Error {
  constructor(readonly status: number) {
    super(
      status === 401 || status === 403
        ? `The daemon refused the request (status ${status}).`
        : `The daemon answered the request with status ${status}.`,
    )
    this.name = 'FleetRequestError'
  }
}

/** A 200 that is not a fleet document this page can read. */
export class FleetFormatError extends Error {
  constructor() {
    super('The daemon sent a fleet document this page cannot read; showing the last good one.')
    this.name = 'FleetFormatError'
  }
}

/** A README read that was answered with other than 200: the status and the daemon's short error code ('' when it sent none). */
export class ReadmeRequestError extends Error {
  constructor(
    readonly status: number,
    readonly code: string,
  ) {
    super(`The daemon answered the README request with status ${status}.`)
    this.name = 'ReadmeRequestError'
  }
}

/** Which side of a schema mismatch is older, so the page can say what to do. */
export type SchemaMismatch = 'daemon-older' | 'page-older'

export const UPDATE_WB_MESSAGE = 'update wb on this machine'
export const RELOAD_MESSAGE = 'reload'

/** A fleet document of a schema version this page does not read (REQ:schema-version-2). */
export class FleetSchemaError extends Error {
  constructor(
    readonly mismatch: SchemaMismatch,
    readonly daemonVersion: number,
    readonly pageVersion: number,
  ) {
    super(mismatch === 'daemon-older' ? UPDATE_WB_MESSAGE : RELOAD_MESSAGE)
    this.name = 'FleetSchemaError'
  }
}

const COLLECTIONS = ['machines', 'repositories', 'worktrees', 'pull_requests', 'agents'] as const

/** Whether `value` is an object with the collections of a fleet document (the version is checked separately). */
export function hasFleetShape(value: unknown): value is FleetDocument {
  if (typeof value !== 'object' || value === null) return false
  const candidate = value as Record<string, unknown>
  return COLLECTIONS.every((name) => Array.isArray(candidate[name]))
}

/**
 * Whether `value` is a fleet document of exactly `expected`, with every
 * collection present.
 */
export function isFleetDocument(value: unknown, expected: number = SCHEMA_VERSION): value is FleetDocument {
  return hasFleetShape(value) && value.schema_version === expected
}

/** A cheap fingerprint of a response body, so an identical body keeps the document's identity. */
export function digestOf(text: string): string {
  let hash = 0x811c9dc5
  for (let index = 0; index < text.length; index++) {
    hash = Math.imul(hash ^ text.charCodeAt(index), 0x01000193) >>> 0
  }
  return `${text.length}:${hash.toString(16)}`
}

export type FleetRead =
  | { kind: 'changed'; document: FleetDocument; etag: string; digest: string }
  | { kind: 'unchanged' }

@Injectable({ providedIn: 'root' })
export class FleetClient {
  private readonly fetcher = inject(FETCH)

  /** Reads the fleet document; with the last ETag, an unchanged one costs a 304. */
  async readFleet(etag?: string, expected: number = SCHEMA_VERSION): Promise<FleetRead> {
    const headers: Record<string, string> = { Accept: 'application/json' }
    if (etag) headers['If-None-Match'] = etag
    const response = await this.fetcher(FLEET_PATH, {
      headers,
      credentials: 'same-origin',
      signal: AbortSignal.timeout(REQUEST_TIMEOUT_MS),
    })
    if (response.status === 304) return { kind: 'unchanged' }
    if (!response.ok) throw new FleetRequestError(response.status)
    const text = await response.text()
    let document: unknown
    try {
      document = JSON.parse(text)
    } catch {
      throw new FleetFormatError()
    }
    const version = typeof document === 'object' && document !== null ? (document as Record<string, unknown>)['schema_version'] : undefined
    if (typeof version !== 'number') throw new FleetFormatError()
    if (version !== expected) throw new FleetSchemaError(version < expected ? 'daemon-older' : 'page-older', version, expected)
    if (!hasFleetShape(document)) throw new FleetFormatError()
    return { kind: 'changed', document, etag: response.headers.get('ETag') ?? '', digest: digestOf(text) }
  }

  /** The branches of one repository checkout, read lazily when a repository page or panel opens. */
  async readBranches(repository: string): Promise<BranchesResponse> {
    const response = await this.fetcher(`${BRANCHES_PATH}?repository=${encodeURIComponent(repository)}`, {
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

  /** The samples of one machine: local history, live remote, one cached sample, or none. */
  async readMachineMetrics(machine: string): Promise<MachineMetrics> {
    const response = await this.fetcher(`${MACHINE_METRICS_PATH}?machine=${encodeURIComponent(machine)}`, {
      headers: { Accept: 'application/json' },
      credentials: 'same-origin',
      signal: AbortSignal.timeout(REQUEST_TIMEOUT_MS),
    })
    if (!response.ok) throw new FleetRequestError(response.status)
    const body: unknown = await response.json().catch(() => null)
    const samples = typeof body === 'object' && body !== null ? (body as Record<string, unknown>)['samples'] : undefined
    if (!Array.isArray(samples)) throw new FleetFormatError()
    return body as MachineMetrics
  }

  async readSession(): Promise<Session> {
    const response = await this.fetcher(SESSION_PATH, {
      headers: { Accept: 'application/json' },
      credentials: 'same-origin',
      signal: AbortSignal.timeout(REQUEST_TIMEOUT_MS),
    })
    if (!response.ok) throw new FleetRequestError(response.status)
    return (await response.json()) as Session
  }

  /**
   * Reads a repository's README as Markdown text: the owner-only route, so
   * callers ask only with an owner session. The text is untrusted.
   */
  async readReadme(repository: string): Promise<string> {
    const response = await this.fetcher(`${README_PATH}?repository=${encodeURIComponent(repository)}`, {
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
}
