export const worktreeStatuses = [
  'active',
  'idle',
  'ready',
  'blocked',
  'landed',
  'cleanup_pending',
  'recovery_needed',
  'orphaned',
  'unknown',
] as const;

export type WorktreeStatus = typeof worktreeStatuses[number];

export interface FleetPullRequest {
  number: number;
  url: string;
  state: 'draft' | 'open' | 'merged' | 'closed';
}

/**
 * Browser projection of WB's published WorktreeState. The first five fields
 * preserve the WB remote-state JSON names. The remaining optional fields let
 * the control plane add lifecycle and forge evidence without making older
 * machine snapshots invalid.
 */
export interface FleetWorktree {
  task: string;
  repository: string;
  branch: string;
  head_sha?: string;
  owner_state?: 'active' | 'orphaned' | 'unknown';
  stream?: string;
  status?: WorktreeStatus;
  last_activity_at?: string;
  pull_request?: FleetPullRequest;
  needs_attention?: boolean;
  attention_reason?: string;
}

export interface FleetMachine {
  name: string;
  wb_version?: string;
  published_at?: string;
  last_seen_at?: string;
  stale?: boolean;
  state?: 'online' | 'stale' | 'offline' | 'unknown';
  worktrees: FleetWorktree[];
}

export interface FleetSnapshot {
  machines: FleetMachine[];
}

export interface FleetWorktreeRow extends FleetWorktree {
  machine: string;
  machine_state: 'online' | 'stale' | 'offline' | 'unknown';
  machine_last_seen_at?: string;
}

export interface WorktreeFilters {
  machine: string;
  repository: string;
  status: string;
  task: string;
  attention: boolean;
}

const emptyFilters: WorktreeFilters = {
  machine: '', repository: '', status: '', task: '', attention: false,
};

function isRecord(value: unknown): value is Record<string, unknown> {
  return typeof value === 'object' && value !== null && !Array.isArray(value);
}

function isOptionalString(value: unknown): value is string | undefined {
  return value === undefined || typeof value === 'string';
}

function isSafeURL(value: unknown): value is string {
  if (typeof value !== 'string') return false;
  try {
    const url = new URL(value);
    return url.protocol === 'https:' || url.protocol === 'http:';
  } catch {
    return false;
  }
}

function isPullRequest(value: unknown): value is FleetPullRequest {
  if (!isRecord(value)) return false;
  return Number.isSafeInteger(value.number) && Number(value.number) > 0 &&
    isSafeURL(value.url) && ['draft', 'open', 'merged', 'closed'].includes(String(value.state));
}

export function isFleetWorktree(value: unknown): value is FleetWorktree {
  if (!isRecord(value)) return false;
  if (typeof value.task !== 'string' || typeof value.repository !== 'string' || typeof value.branch !== 'string') return false;
  if (!isOptionalString(value.head_sha) || !isOptionalString(value.stream) || !isOptionalString(value.last_activity_at) || !isOptionalString(value.attention_reason)) return false;
  if (value.owner_state !== undefined && !['active', 'orphaned', 'unknown'].includes(String(value.owner_state))) return false;
  if (value.status !== undefined && !worktreeStatuses.includes(value.status as WorktreeStatus)) return false;
  if (value.needs_attention !== undefined && typeof value.needs_attention !== 'boolean') return false;
  return value.pull_request === undefined || isPullRequest(value.pull_request);
}

export function isFleetSnapshot(value: unknown): value is FleetSnapshot {
  if (!isRecord(value) || !Array.isArray(value.machines)) return false;
  return value.machines.every((machine) => isRecord(machine) &&
    typeof machine.name === 'string' && machine.name.length > 0 &&
    isOptionalString(machine.wb_version) && isOptionalString(machine.published_at) && isOptionalString(machine.last_seen_at) &&
    (machine.stale === undefined || typeof machine.stale === 'boolean') &&
    (machine.state === undefined || ['online', 'stale', 'offline', 'unknown'].includes(String(machine.state))) &&
    Array.isArray(machine.worktrees) && machine.worktrees.every(isFleetWorktree));
}

/** Returns undefined when an authorized response has no fleet projection yet. */
export function fleetSnapshotFromResponse(value: unknown): FleetSnapshot | undefined {
  if (!isRecord(value) || value.fleet === undefined) return undefined;
  return isFleetSnapshot(value.fleet) ? value.fleet : undefined;
}

export function flattenFleetWorktrees(snapshot: FleetSnapshot): FleetWorktreeRow[] {
  return snapshot.machines.flatMap((machine) => machine.worktrees.map((worktree) => ({
    ...worktree,
    machine: machine.name,
    machine_state: machine.state ?? (machine.stale === true ? 'stale' : machine.stale === false ? 'online' : 'unknown'),
    machine_last_seen_at: machine.last_seen_at ?? machine.published_at,
  })));
}

export function worktreeStatus(worktree: FleetWorktree): WorktreeStatus {
  return worktree.status ?? worktree.owner_state ?? 'unknown';
}

export function worktreeNeedsAttention(worktree: FleetWorktreeRow): boolean {
  return worktree.needs_attention === true || ['stale', 'offline'].includes(worktree.machine_state) ||
    ['blocked', 'cleanup_pending', 'recovery_needed', 'orphaned'].includes(worktreeStatus(worktree));
}

export function worktreeFiltersFromSearch(search: string | URLSearchParams): WorktreeFilters {
  const params = typeof search === 'string' ? new URLSearchParams(search) : search;
  return {
    machine: params.get('machine')?.trim() ?? '',
    repository: params.get('repository')?.trim() ?? '',
    status: params.get('status')?.trim() ?? '',
    task: params.get('task')?.trim() ?? '',
    attention: params.get('attention') === '1',
  };
}

export function writeWorktreeFilters(params: URLSearchParams, filters: WorktreeFilters): URLSearchParams {
  const next = new URLSearchParams(params);
  for (const key of Object.keys(emptyFilters) as Array<keyof WorktreeFilters>) {
    const value = filters[key];
    if (typeof value === 'boolean' ? value : value.length > 0) next.set(key, typeof value === 'boolean' ? '1' : value);
    else next.delete(key);
  }
  return next;
}

export function matchesWorktreeFilters(worktree: FleetWorktreeRow, filters: WorktreeFilters): boolean {
  const task = `${worktree.stream ?? ''} ${worktree.task}`.toLocaleLowerCase();
  return (!filters.machine || worktree.machine === filters.machine) &&
    (!filters.repository || worktree.repository === filters.repository) &&
    (!filters.status || worktreeStatus(worktree) === filters.status) &&
    (!filters.task || task.includes(filters.task.toLocaleLowerCase())) &&
    (!filters.attention || worktreeNeedsAttention(worktree));
}
