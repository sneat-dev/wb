// The fleet read model's document, typed from internal/cockpit/fleet/document.go
// field for field. Timestamps are RFC 3339 strings. A field the Go side omits
// when empty (`omitempty`, `omitzero`) is optional here; a pointer count that
// is nil is absent, which means "not known for this entry".

/** The routes an entry can have: this machine, another machine's published snapshot, or another machine read live. */
export type Route = 'local' | 'cached' | 'live-remote'

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
  /** Hardware facts (schema 2); omitted when the machine does not report them. */
  os?: string
  arch?: string
  cpu_count?: number
  boot_time?: string
  /** How this machine's live state is read: over its own HTTP route or through SSH. */
  transport?: 'http' | 'ssh'
  /** Why the last live export failed (a code of REMOTE_ERRORS); unknown codes are tolerated. */
  remote_error?: string
  /** How many entries the live export left out (over a bound); above zero is a Fleet health line. Absent: not reported. */
  export_dropped?: number
  /** The code of this machine's last failed or degraded periodic publish (REQ:home-fleet-health); on the local machine's entry only, absent when healthy or when publishing is off. */
  publish_error?: PublishError
  /** That machine's agents were cut; carried by the entry of a live-remote or cached machine, never by the local one (the document's own flag says it). */
  agents_truncated?: boolean
}

/** The typed codes of the local machine's `publish_error`: a closed list, never an error text. */
export const PUBLISH_ERRORS = ['collect_failed', 'store_unavailable', 'publish_failed', 'optional_fields_dropped'] as const
export type PublishError = (typeof PUBLISH_ERRORS)[number]

/** The typed codes of a machine's `remote_error` (REQ:remote-error-is-visible). */
export const REMOTE_ERRORS = [
  'remote_warming_up',
  'export_too_large',
  'http_unavailable',
  'http_auth_failed',
  'ssh_unavailable',
  'auth_failed',
  'timeout',
  'wb_missing',
  'wb_too_old',
  'daemon_not_running',
  'export_refused',
  'bad_payload',
  'self_export',
  'clock_skew',
] as const
export type RemoteError = (typeof REMOTE_ERRORS)[number]

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
  /** What the configured provider reported; present on the indexer it follows. */
  statistics?: CodeStatistics
}

/** The number of symbols of one kind. */
export interface KindCount {
  kind: string
  count: number
}

/**
 * A code index's statistics (internal/cockpit/fleet/document.go): counts only.
 * These are statistics of the index, not counts of Cockpit entities. `error`
 * is a short code when the provider could not answer.
 */
export interface CodeStatistics {
  indexed: boolean
  files: number
  symbols: number
  edges: number
  kinds: KindCount[]
  error?: string
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
  /** The newest local-branch activity; omitted for an entry cached from another machine. */
  last_activity_at?: string
  /** `https://<host>/<owner>/<name>`, only when the host and every segment validated. */
  remote_url_web?: string
}

/** The owner vocabulary of every route (REQ:owner-state-vocabulary). */
export const OWNER_STATES = ['active', 'idle', 'orphaned', 'unknown'] as const
export type OwnerState = (typeof OWNER_STATES)[number]

/** The worktree lifecycles the daemon populates (REQ:field-tables). */
export const LIFECYCLES = ['working', 'review', 'merged', 'superseded'] as const
export type Lifecycle = (typeof LIFECYCLES)[number]

export interface Worktree extends Entry {
  repository: string
  task: string
  stream?: string
  branch: string
  lifecycle?: string
  owner_state?: OwnerState
  last_activity_at?: string
  code_index?: CodeIndex[]
  /** The task again, never a path (schema 2). */
  name?: string
  /** Sync facts of the branch: only for worktrees of the machine the Cockpit runs on. */
  ahead?: number
  behind?: number
  upstream_gone?: boolean
  /** Whether the branch has an upstream (local only); `false` with commits is unpushed work. */
  has_upstream?: boolean
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

/** The merge states a pull request entry can carry. */
export const MERGEABLE_STATES = ['clean', 'blocked', 'dirty', 'behind', 'unstable', 'has_hooks', 'draft', 'unknown'] as const
export type Mergeable = (typeof MERGEABLE_STATES)[number]

/** The pull request states of REQ:pull-request-fields; `draft` is an open draft. */
export const PULL_REQUEST_STATES = ['open', 'merged', 'closed', 'draft'] as const
export type PullRequestState = (typeof PULL_REQUEST_STATES)[number]

export interface PullRequest extends Entry {
  repository?: string
  worktree?: string
  branch?: string
  number: number
  url?: string
  /** Every field below is omitted until one observation has succeeded: then the state is "not reported". */
  state?: PullRequestState
  /** GitHub's merge state, a closed enum; otherwise omitted. */
  mergeable?: Mergeable
  checks_total?: number
  checks_passed?: number
  /** Failed and cancelled. */
  checks_failed?: number
  checks_pending?: number
  /** Skipped checks, counted apart from the passed ones. */
  checks_skipped?: number
  /** The verdict of `prsnapshot.Snapshot.Green`; never re-derived from the counts. */
  checks_green?: boolean
  failed_check?: string
  checked_at?: string
}

/** What a herdr-backed session is doing (REQ:agent-activity); absent: not reported. */
export const AGENT_ACTIVITIES = ['working', 'blocked', 'idle', 'done', 'unknown'] as const
export type AgentActivity = (typeof AGENT_ACTIVITIES)[number]

/** The terminal states of a dispatched run, plus `running`. */
export type RunState = 'running' | 'completed' | 'failed' | 'timeout' | 'abandoned'

export interface Agent extends Entry {
  kind: 'session' | 'run'
  session_id?: string
  run_id?: string
  runtime?: string
  model?: string
  /** A run's state (`running`, `completed`, `failed`, `timeout`, `abandoned`) or a session's (`live`, `parked`). */
  state: string
  repository?: string
  activity?: AgentActivity
  /** Worktree ids the agent works on; never guessed. */
  worktrees?: string[]
  task?: string
  started_at?: string
  /** A dispatched run's exit code; never its free-text failure. */
  exit_code?: number
  /** When a dispatched run ended. */
  finished_at?: string
}

/** One UTC day of the throughput block: the tasks finished and dropped that day; `landed` (a subset of `finished`) only when above zero. */
export interface ThroughputDay {
  date: string
  finished: number
  dropped: number
  landed?: number
}

/**
 * The sealed-work throughput block (REQ:throughput-block); omitted when no record is usable. `per_day` lists only
 * the days with a sealing, `landed_at` of a slowest entry is the time the task was sealed.
 */
export interface Throughput {
  window_days: number
  per_day: ThroughputDay[]
  slowest: { task: string; duration_seconds: number; landed_at: string }[]
  /** Median and 90th percentile (nearest rank) of the finished durations; absent when no task finished. */
  median_seconds?: number
  p90_seconds?: number
  /** True only when a safety bound cut the scan. */
  capped?: boolean
}

export interface FleetDocument {
  schema_version: number
  snapshot_at?: string
  /** The daemon's snapshot refresh interval; the freshness chip uses it. */
  refresh_interval_seconds?: number
  warming_up: boolean
  repositories_total: number
  repositories_scanned: number
  diagnostics: number
  error?: string
  /** The configured code-index provider's name; absent when none is configured. */
  code_index_provider?: string
  machines: Machine[]
  repositories: Repository[]
  worktrees: Worktree[]
  /** No `branches` collection since schema 2: see BranchesResponse. */
  pull_requests: PullRequest[]
  agents: Agent[]
  /** The agents of a machine are capped; true when some were left out. */
  agents_truncated?: boolean
  /** True when the pull request watcher was rate-limited, so some observations are older than usual. */
  pull_requests_throttled?: boolean
  throughput?: Throughput
}

/**
 * Whether the agents of `machine` were cut: the document's own flag for the local machine (the Go side never sets it on
 * the local entry), the machine entry's flag for another machine. The `pull_requests_throttled` flag stays the
 * document's: only this machine's own pull requests are observed.
 */
export function agentsTruncated(document: Pick<FleetDocument, 'agents_truncated'>, machine: Pick<Machine, 'route' | 'agents_truncated'>): boolean {
  return (machine.route === 'local' ? document.agents_truncated : machine.agents_truncated) === true
}

/**
 * Whether the agents of any machine were cut: a count that includes them is then "at least" that many. The document's
 * own flag is the local machine's, and each other machine's flag is on its entry.
 */
export function anyAgentsTruncated(document: Pick<FleetDocument, 'agents_truncated' | 'machines'>): boolean {
  return document.agents_truncated === true || document.machines.some((machine) => agentsTruncated(document, machine))
}

/** The one schema version this page reads (REQ:schema-version-2). */
export const SCHEMA_VERSION = 2

/** `GET /api/v1/cockpit/branches?repository=<id>`: the lazy branches of one repository checkout. */
export interface BranchesResponse {
  branches: Branch[]
  /** Why the list is empty, for a cached repository. */
  reason?: string
}

/** Where a machine's samples came from (REQ:machine-metrics-route). */
export type MetricsRoute = 'local' | 'live-remote' | 'cached' | 'none'

/**
 * One metrics sample, and nothing else. Every measurement is optional, as the daemon omits what could not be read
 * (REQ:machine-metrics-route): `cpu_percent` on the first sample after a start, a pair of totals that could not be read.
 * An absent one is "not reported", never a zero.
 */
export interface MetricsSample {
  cpu_percent?: number
  load1?: number
  memory_used_bytes?: number
  memory_total_bytes?: number
  disk_free_bytes?: number
  disk_total_bytes?: number
  sampled_at: string
}

/** `GET /api/v1/cockpit/machine-metrics?machine=<id>`. */
export interface MachineMetrics {
  machine: string
  route: MetricsRoute
  fetched_at?: string
  /** Oldest first; the last element is the latest sample. */
  samples: MetricsSample[]
  reason?: string
}

/**
 * GET /api/v1/cockpit/session: who the caller is, what it may do, and the
 * configured code browser base (internal/cockpit/server.go sessionResponse).
 */
export interface Session {
  principal: string
  capabilities: string[]
  code_browser_url: string
  /**
   * OWNER-ONLY: how to reach the machines that have an SSH route, for the copied
   * `ssh ...` commands. Anonymous readers never receive it.
   */
  machine_routes?: MachineRoute[]
}

/** One machine's SSH route from the session response; `user` and `wb_path` may be empty or absent. */
export interface MachineRoute {
  machine_id: string
  ssh: { host: string; user?: string; wb_path?: string }
}

/** The document error code for a Git older than the oldest safe version. */
export const ERROR_GIT_TOO_OLD = 'git_too_old'
export const ERROR_REPOSITORIES_UNREADABLE = 'repositories_unreadable'

/** The agent run state that internal/cockpit/fleet counts as an active agent. */
export const RUNNING_STATE = 'running'

/** The state of a live registered session. */
export const LIVE_STATE = 'live'

/** The client's notion of "running": a session that is `live` or a dispatched run that is `running`. */
export function isRunning(agent: Pick<Agent, 'kind' | 'state'>): boolean {
  return agent.kind === 'session' ? agent.state === LIVE_STATE : agent.state === RUNNING_STATE
}

export const FLEET_PATH = '/api/v1/cockpit/fleet'
export const SESSION_PATH = '/api/v1/cockpit/session'
export const BRANCHES_PATH = '/api/v1/cockpit/branches'
export const MACHINE_METRICS_PATH = '/api/v1/cockpit/machine-metrics'

/** The capability that lets a caller read repository content, such as a README. */
export const CAPABILITY_REPO_CONTENT_READ = 'repo.content.read'

/** The owner-only README route; the repository id travels in the `repository` query parameter. */
export const README_PATH = '/api/v1/cockpit/readme'

/** Where the session read is: not answered yet, answered, or failed (and retried on the next poll). */
export type SessionStatus = 'loading' | 'ready' | 'failed'
