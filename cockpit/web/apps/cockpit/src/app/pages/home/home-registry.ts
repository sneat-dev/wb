import { Injectable, inject, signal } from '@angular/core'
import { ActionsResponse, FETCH, RegistryAction } from '@cockpit/fleet-data'

/**
 * The registry route of cockpit-actions (REQ:actions-are-discovered).
 * TODO(fleet-data): the library has only the fake's copy of this path and no registry client yet;
 * replace this adapter with that client's accessor when the cockpit-actions clients land.
 */
export const ACTIONS_PATH = '/api/v1/cockpit/actions'

/** The registry actions Home puts in a row's slot: the one action of its intent, never a list of everything. */
export const PUSH_ACTION = 'branch.push'
export const LAND_ACTION = 'pr.land'

/**
 * Reads `GET /api/v1/cockpit/actions?target=<type>:<id>` for the few targets Home's rows show and
 * keeps the answers for the action slots. It reads nothing until a section asks (after the first
 * paint), and when the route is absent (404) it stops asking, so a daemon without a registry costs
 * one request. Whoever has no registry sees the "Copy command" buttons instead.
 */
@Injectable({ providedIn: 'root' })
export class HomeRegistry {
  private readonly fetcher = inject(FETCH)
  private readonly answers = signal<ReadonlyMap<string, readonly RegistryAction[]>>(new Map())
  private absent = false

  /** The registry's `id` actions for the target, or undefined when it offers none (or there is no registry). */
  offered(target: string, id: string): RegistryAction[] | undefined {
    const found = this.answers().get(target)?.filter((action) => action.id === id) ?? []
    return found.length > 0 ? found : undefined
  }

  /** Reads the actions of these targets; each answer replaces the earlier one for its target, and a target that cannot be read has none. */
  async request(targets: readonly string[]): Promise<void> {
    if (this.absent || targets.length === 0) return
    const read = await Promise.all(targets.map(async (target) => [target, await this.read(target)] as const))
    if (this.absent) return
    const next = new Map(this.answers())
    for (const [target, actions] of read) {
      if (actions === undefined) next.delete(target)
      else next.set(target, actions)
    }
    this.answers.set(next)
  }

  private async read(target: string): Promise<RegistryAction[] | undefined> {
    try {
      const response = await this.fetcher(`${ACTIONS_PATH}?target=${encodeURIComponent(target)}`, {
        headers: { Accept: 'application/json' },
        credentials: 'same-origin',
        signal: AbortSignal.timeout(5000),
      })
      if (response.status === 404) {
        this.absent = true
        return undefined
      }
      if (!response.ok) return undefined
      const body = (await response.json()) as Partial<ActionsResponse>
      return Array.isArray(body.actions) ? body.actions : undefined
    } catch {
      return undefined
    }
  }
}
