import { Injectable, InjectionToken, inject } from '@angular/core'
import { FLEET_PATH, FleetDocument, README_PATH, SESSION_PATH, Session } from './fleet.types'

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

const COLLECTIONS = ['machines', 'repositories', 'worktrees', 'branches', 'pull_requests', 'agents'] as const

/** Whether `value` is a version 1 document with every collection present. */
export function isFleetDocument(value: unknown): value is FleetDocument {
  if (typeof value !== 'object' || value === null) return false
  const candidate = value as Record<string, unknown>
  return candidate['schema_version'] === 1 && COLLECTIONS.every((name) => Array.isArray(candidate[name]))
}

export type FleetRead =
  | { kind: 'changed'; document: FleetDocument; etag: string }
  | { kind: 'unchanged' }

@Injectable({ providedIn: 'root' })
export class FleetClient {
  private readonly fetcher = inject(FETCH)

  /** Reads the fleet document; with the last ETag, an unchanged one costs a 304. */
  async readFleet(etag?: string): Promise<FleetRead> {
    const headers: Record<string, string> = { Accept: 'application/json' }
    if (etag) headers['If-None-Match'] = etag
    const response = await this.fetcher(FLEET_PATH, {
      headers,
      credentials: 'same-origin',
      signal: AbortSignal.timeout(REQUEST_TIMEOUT_MS),
    })
    if (response.status === 304) return { kind: 'unchanged' }
    if (!response.ok) throw new FleetRequestError(response.status)
    const document: unknown = await response.json()
    if (!isFleetDocument(document)) throw new FleetFormatError()
    return { kind: 'changed', document, etag: response.headers.get('ETag') ?? '' }
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
