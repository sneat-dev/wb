// A fake action registry and a fake operations route, for the pages that render
// the control surface against stubs. They answer through the injected `fetch`,
// so no page test needs a daemon.

import { ActionsResponse, OperationRecord, OperationState, RegistryAction, isTerminal } from './control.types'

export const ACTIONS_PATH = '/api/v1/cockpit/actions'
export const OPERATIONS_PATH = '/api/v1/cockpit/operations'

/** A registry action with the fields most tests do not care about filled in. */
export function registryAction(id: string, title: string, extra: Partial<RegistryAction> = {}): RegistryAction {
  return {
    id,
    title,
    target_types: ['worktree'],
    applicable: true,
    parameters: [],
    capability: 'branch.push',
    safety: 'safe',
    permitted: true,
    ...extra,
  }
}

/** Operations a test moves along by hand: started queued, then advanced to running and a terminal state. */
export class FakeOperations {
  private readonly records = new Map<string, OperationRecord>()
  private counter = 0

  start(action: string, target: string): OperationRecord {
    const record: OperationRecord = { id: `op-${++this.counter}`, action, target, state: 'queued' }
    this.records.set(record.id, record)
    return { ...record }
  }

  advance(id: string, state: OperationState, summary?: string[]): void {
    const record = this.records.get(id)
    if (record === undefined || isTerminal(record.state)) return
    this.records.set(id, { ...record, state, summary })
  }

  get(id: string): OperationRecord | undefined {
    const record = this.records.get(id)
    return record && { ...record }
  }
}

export interface FakeControlOptions {
  /** Actions by target (`worktree:wt-1`); a target not listed has none. Undefined: the route is absent (404). */
  registry?: Record<string, RegistryAction[]>
  /** Undefined: the operations route is absent (404). */
  operations?: FakeOperations
}

function json(body: unknown, status = 200): Response {
  return new Response(JSON.stringify(body), { status, headers: { 'Content-Type': 'application/json' } })
}

/** A `fetch` that serves the two control routes and passes every other request to `next`. */
export function fakeControlFetch(options: FakeControlOptions, next: typeof fetch): typeof fetch {
  return async (input, init) => {
    const url = new URL(typeof input === 'string' ? input : input instanceof URL ? input.href : input.url, 'http://cockpit.test')
    if (url.pathname === ACTIONS_PATH) {
      if (options.registry === undefined) return json({}, 404)
      const body: ActionsResponse = { actions: options.registry[url.searchParams.get('target') ?? ''] ?? [] }
      return json(body)
    }
    if (url.pathname.startsWith(`${OPERATIONS_PATH}/`)) {
      const record = options.operations?.get(decodeURIComponent(url.pathname.slice(OPERATIONS_PATH.length + 1)))
      return record ? json(record) : json({}, 404)
    }
    return next(input, init)
  }
}
