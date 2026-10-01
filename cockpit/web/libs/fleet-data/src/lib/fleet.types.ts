// The fleet read model's document, typed from internal/cockpit/fleet/document.go
// field for field. Timestamps are RFC 3339 strings. A field the Go side omits
// when empty (`omitempty`, `omitzero`) is optional here; a pointer count that
// is nil is absent, which means "not known for this entry".

/** The two routes an entry can have. */
export type Route = 'local' | 'cached'

export interface Entry {
  id: string
  /** The machine's name; two logins can publish under one name. */
  machine: string
  /** The id of the machine entry this belongs to; unique, and what filters use. */
  machine_id: string
  route: Route
  observed_at?: string
}

export interface Machine extends Entry {
  wb_version?: string
  repository_count: number
  worktree_count: number
}

/** The six states a code index can be in (internal/cockpit/fleet/document.go). */
export type CodeIndexState = 'fresh' | 'stale' | 'diverged' | 'pending' | 'failed' | 'never'

/**
 * One indexer's index of a checkout: its configured name, its state, for a
 * stale index the number of commits behind, and the time of the receipt the
 * state was read from.
 */
export interface CodeIndex {
  indexer: string
  state: CodeIndexState
  behind?: number
  receipt_at?: string
}

export interface Repository extends Entry {
  host?: string
  name: string
  default_branch?: string
  worktree_count: number
  local_branch_count?: number
  remote_branch_count?: number
  open_pull_request_count?: number
  active_agent_count?: number
  error?: string
  /** One state per configured indexer; absent when not known for this entry. */
  code_index?: CodeIndex[]
}

export interface Worktree extends Entry {
  repository: string
  task: string
  stream?: string
  branch: string
  lifecycle?: string
  owner_state?: 'active' | 'idle'
  last_activity_at?: string
  code_index?: CodeIndex[]
}

export interface Branch extends Entry {
  repository: string
  name: string
  scope: 'local' | 'remote'
  task?: string
  worktree?: string
  upstream?: string
  ahead?: number
  behind?: number
  upstream_gone?: boolean
  last_activity_at?: string
}

export interface PullRequest extends Entry {
  repository?: string
  worktree?: string
  branch?: string
  number: number
  state?: string
  url?: string
}

export interface Agent extends Entry {
  kind: 'session' | 'run'
  session_id?: string
  run_id?: string
  runtime?: string
  model?: string
  state: string
  repository?: string
}

export interface FleetDocument {
  schema_version: number
  snapshot_at?: string
  warming_up: boolean
  repositories_total: number
  repositories_scanned: number
  diagnostics: number
  error?: string
  machines: Machine[]
  repositories: Repository[]
  worktrees: Worktree[]
  branches: Branch[]
  pull_requests: PullRequest[]
  agents: Agent[]
  agents_truncated?: boolean
}

/**
 * GET /api/v1/cockpit/session: who the caller is, what it may do, and the
 * configured code browser base (internal/cockpit/server.go sessionResponse).
 */
export interface Session {
  principal: string
  capabilities: string[]
  code_browser_url: string
}

/** The document error code for a Git older than the oldest safe version. */
export const ERROR_GIT_TOO_OLD = 'git_too_old'
export const ERROR_REPOSITORIES_UNREADABLE = 'repositories_unreadable'

/** The agent run state that internal/cockpit/fleet counts as an active agent. */
export const RUNNING_STATE = 'running'

export const FLEET_PATH = '/api/v1/cockpit/fleet'
export const SESSION_PATH = '/api/v1/cockpit/session'
