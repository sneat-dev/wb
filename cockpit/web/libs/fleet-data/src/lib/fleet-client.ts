import { Injectable, InjectionToken, inject } from '@angular/core'
import {
  AGENT_ACTIVITIES,
  FLEET_PATH,
  FleetDocument,
  LIFECYCLES,
  MERGEABLE_STATES,
  OWNER_STATES,
  PUBLISH_ERRORS,
  PULL_REQUEST_STATES,
  SCHEMA_VERSION,
  SESSION_PATH,
  Session,
  Throughput,
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

const isBool = (value: unknown): boolean => typeof value === 'boolean'
const isOneOf =
  (values: readonly string[]) =>
  (value: unknown): boolean =>
    typeof value === 'string' && values.includes(value)
const isIdList = (value: unknown): boolean => Array.isArray(value) && value.every(isText)

/**
 * The optional fields a kind may carry, with what a valid value is. A field of the wrong type, or an enum value
 * outside its set, is removed from the entry: it reads as "not reported" (REQ:field-tables, an out-of-set value is
 * dropped, not rendered); the entry itself stays. `remote_error` is not here: an unknown code is "unknown error".
 */
const OPTIONAL_CHECKS: Partial<Record<(typeof COLLECTIONS)[number], Record<string, (value: unknown) => boolean>>> = {
  machines: { export_dropped: isCount, remote_error: isText, wb_version: isText, publish_error: isOneOf(PUBLISH_ERRORS), agents_truncated: isBool },
  // A value outside its set is removed, so the field reads "not reported": an `owner_state` the page does not know is
  // never "not active" (which would make a task at risk), nor a pull request `state` it does not know "not open".
  worktrees: {
    owner_state: isOneOf(OWNER_STATES),
    lifecycle: isOneOf(LIFECYCLES),
    ahead: isCount,
    behind: isCount,
    upstream_gone: isBool,
    has_upstream: isBool,
    last_activity_at: isText,
  },
  pull_requests: {
    mergeable: isOneOf(MERGEABLE_STATES),
    state: isOneOf(PULL_REQUEST_STATES),
    checks_total: isCount,
    checks_passed: isCount,
    checks_failed: isCount,
    checks_pending: isCount,
    checks_skipped: isCount,
    checks_green: isBool,
    checked_at: isText,
    failed_check: isText,
  },
  agents: {
    activity: isOneOf(AGENT_ACTIVITIES),
    task: isText,
    worktrees: isIdList,
    started_at: isText,
    finished_at: isText,
    exit_code: isCount,
  },
}

/** The entry without the fields whose values are invalid; the same object when all are fine. */
function withoutBadFields(entry: Record<string, unknown>, checks: Record<string, (value: unknown) => boolean>): Record<string, unknown> {
  const bad = Object.keys(checks).filter((field) => entry[field] !== undefined && !checks[field](entry[field]))
  if (bad.length === 0) return entry
  const cleaned = { ...entry }
  for (const field of bad) delete cleaned[field]
  return cleaned
}

/** The throughput block when it is well formed (bad days and entries are left out, bad optional numbers removed); undefined otherwise. */
export function cleanThroughput(value: unknown): Throughput | undefined {
  if (typeof value !== 'object' || value === null) return undefined
  const block = value as Record<string, unknown>
  if (!isCount(block['window_days']) || !Array.isArray(block['per_day']) || !Array.isArray(block['slowest'])) return undefined
  const days = (block['per_day'] as unknown[]).filter(
    (day): day is Record<string, unknown> =>
      typeof day === 'object' && day !== null && isText((day as Record<string, unknown>)['date']) && isCount((day as Record<string, unknown>)['finished']) && isCount((day as Record<string, unknown>)['dropped']),
  )
  const slowest = (block['slowest'] as unknown[]).filter(
    (entry): entry is Record<string, unknown> =>
      typeof entry === 'object' && entry !== null && isText((entry as Record<string, unknown>)['task']) && isCount((entry as Record<string, unknown>)['duration_seconds']) && isText((entry as Record<string, unknown>)['landed_at']),
  )
  const cleaned: Record<string, unknown> = { window_days: block['window_days'], per_day: days.map((day) => withoutBadFields(day, { landed: isCount })), slowest }
  if (isCount(block['median_seconds'])) cleaned['median_seconds'] = block['median_seconds']
  if (isCount(block['p90_seconds'])) cleaned['p90_seconds'] = block['p90_seconds']
  if (isBool(block['capped'])) cleaned['capped'] = block['capped']
  return cleaned as unknown as Throughput
}

/**
 * Removes the optional fields of the document and its entries that are of the wrong type or out of their set, and
 * a malformed throughput block. The same object comes back when nothing needed removing.
 */
export function cleanOptionalFields(document: FleetDocument): FleetDocument {
  let changed = false
  const next: Record<string, unknown> = { ...document }
  for (const name of COLLECTIONS) {
    const checks = OPTIONAL_CHECKS[name]
    if (checks === undefined) continue
    const rows = document[name] as unknown as Record<string, unknown>[]
    const cleaned = rows.map((row) => withoutBadFields(row, checks))
    if (cleaned.some((row, index) => row !== rows[index])) {
      next[name] = cleaned
      changed = true
    }
  }
  for (const flag of ['agents_truncated', 'pull_requests_throttled'] as const) {
    if (document[flag] !== undefined && !isBool(document[flag])) {
      delete next[flag]
      changed = true
    }
  }
  if (document.throughput !== undefined) {
    const throughput = cleanThroughput(document.throughput)
    changed = true
    if (throughput === undefined) delete next['throughput']
    else next['throughput'] = throughput
  }
  return changed ? (next as unknown as FleetDocument) : document
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
    cleaned.document = cleanOptionalFields(cleaned.document)
    return { kind: 'changed', document: cleaned.document, dropped: cleaned.dropped, etag: response.headers.get('ETag') ?? '', digest: digestOf(text) }
  }

  // The three reads that only some pages make (branches, a machine's metrics, a README) are functions of the fetch in
  // `@cockpit/fleet-data/lazy-client`, which a page imports with `import()`, so the first page does not carry them.

  async readSession(): Promise<Session> {
    const response = await this.fetcher(SESSION_PATH, {
      headers: { Accept: 'application/json' },
      credentials: 'same-origin',
      signal: AbortSignal.timeout(REQUEST_TIMEOUT_MS),
    })
    if (!response.ok) throw new FleetRequestError(response.status)
    return (await response.json()) as Session
  }
}
