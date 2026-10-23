// The shapes of the cockpit-actions routes that the control surface renders.
// They are provisional: the registry and operations routes are specified by
// the cockpit-actions Feature (REQ:actions-are-discovered and
// REQ:runs-are-typed-daemon-operations), which fixes the field list but not the
// envelope, and the clients are written with the pages that use them.

/** The entity kinds that have a registry target type. */
export type ActionTargetType = 'worktree' | 'branch' | 'pull_request' | 'repository'

export interface ActionParameter {
  name: string
  type: string
  required: boolean
}

/** One action the registry returns for a target. */
export interface RegistryAction {
  id: string
  title: string
  target_types: ActionTargetType[]
  /** As of the snapshot; the preview decides. */
  applicable: boolean
  /** Why it is not applicable, in plain words. */
  reason?: string
  parameters: ActionParameter[]
  capability: string
  safety: 'safe' | 'guarded' | 'destructive'
  /** Whether the caller holds the capability; an action it cannot run is shown disabled. */
  permitted: boolean
}

/** `GET /api/v1/cockpit/actions?target=<type>:<id>`. */
export interface ActionsResponse {
  actions: RegistryAction[]
}

/** An operation's state: queued, running, or a terminal one. */
export type OperationState = 'queued' | 'running' | 'succeeded' | 'failed' | 'cancelled'

/** `GET /api/v1/cockpit/operations/{id}`. */
export interface OperationRecord {
  id: string
  action: string
  target: string
  state: OperationState
  /** The outcome's summary lines, once terminal. */
  summary?: string[]
}

/** Whether an operation has finished, whatever the outcome. */
export function isTerminal(state: OperationState): boolean {
  return state !== 'queued' && state !== 'running'
}
