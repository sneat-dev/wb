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

const isText = (value: unknown): boolean => typeof value === 'string'
const isCount = (value: unknown): boolean => typeof value === 'number' && Number.isFinite(value)

function isEntry(value: unknown): value is Record<string, unknown> {
  if (typeof value !== 'object' || value === null) return false
  const entry = value as Record<string, unknown>
  return isText(entry['id']) && isText(entry['machine']) && isText(entry['machine_id']) && isText(entry['route'])
}

/** The fields each kind must carry to be shown at all; unknown enum values are tolerated. */
const ENTRY_CHECKS: Record<(typeof COLLECTIONS)[number], (entry: Record<string, unknown>) => boolean> = {
  machines: (e) => isCount(e['repository_count']) && isCount(e['worktree_count']),
  repositories: (e) => isText(e['name']) && isCount(e['worktree_count']),
  worktrees: (e) => isText(e['repository']) && isText(e['task']) && isText(e['branch']),
  pull_requests: (e) => isCount(e['number']),
  agents: (e) => isText(e['kind']) && isText(e['state']),
}

/**
 * Keeps the entries that carry their required fields, and counts the rest: a
 * bad entry is dropped, not thrown. The same document object comes back when
 * nothing was dropped.
 */
export function dropBadEntries(document: FleetDocument): { document: FleetDocument; dropped: number } {
  let dropped = 0
  const cleaned: Record<string, unknown> = {}
  for (const name of COLLECTIONS) {
    const rows = document[name] as unknown[]
    const kept = rows.filter((row) => isEntry(row) && ENTRY_CHECKS[name](row))
    dropped += rows.length - kept.length
    cleaned[name] = kept
  }
  return dropped === 0 ? { document, dropped } : { document: { ...document, ...cleaned }, dropped }
}

/** The document-level facts every schema 2 document carries. */
function hasDocumentFacts(document: FleetDocument): boolean {
  return (
    typeof document.warming_up === 'boolean' &&
    isCount(document.repositories_total) &&
    isCount(document.repositories_scanned) &&
    isCount(document.diagnostics)
  )
}

export type FleetRead =
  /** `dropped` counts the entries left out for lacking a required field. */
  | { kind: 'changed'; document: FleetDocument; etag: string; digest: string; dropped?: number }
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
    if (!hasFleetShape(document) || !hasDocumentFacts(document)) throw new FleetFormatError()
    const cleaned = dropBadEntries(document)
    return { kind: 'changed', document: cleaned.document, dropped: cleaned.dropped, etag: response.headers.get('ETag') ?? '', digest: digestOf(text) }
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
