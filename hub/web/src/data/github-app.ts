import { dashboardApiOrigin } from './dashboard';

/**
 * Provisional read contract with the Workbench control plane. Keep this path in
 * one adapter until the host route is final. Connection actions are always
 * server-provided GitHub URLs; the browser does not invent a mutation route.
 */
export const githubAppStatusPath = '/v0/workbench/github/status';
export const githubAppStatusURL = `${dashboardApiOrigin}${githubAppStatusPath}`;
export const machineEnrollmentPath = '/v0/workbench/machines/enroll';
export const machineEnrollmentURL = `${dashboardApiOrigin}${machineEnrollmentPath}`;

export type GitHubAppConnectionState = 'connected' | 'disconnected' | 'attention';
export type GitHubInstallationState = 'installed' | 'suspended' | 'revoked';
export type GitHubStatusFilter = 'all' | 'pending' | 'error';

export interface GitHubAppInstallation {
  id: string;
  account: string;
  account_type?: 'organization' | 'user';
  state: GitHubInstallationState;
  repository_selection?: 'all' | 'selected';
  repositories?: number;
  manage_url?: string;
}

export interface RegisteredMachine {
  id: string;
  name: string;
  state?: 'online' | 'offline' | 'unknown';
  last_seen_at?: string;
}

export interface DeliveryMarker {
  delivery_id?: string;
  event?: string;
  occurred_at: string;
}

export interface PendingRepositoryRefresh {
  id: string;
  repository: string;
  event?: string;
  queued_at: string;
  installation_id?: string;
  machine_id?: string;
}

export interface GitHubAppStatusError {
  code: string;
  message: string;
  action?: string;
  action_url?: string;
  installation_id?: string;
}

export interface GitHubAppStatus {
  generated_at: string;
  connection: {
    state: GitHubAppConnectionState;
    app_name?: string;
    account?: string;
    connect_url?: string;
  };
  installations: GitHubAppInstallation[];
  machines: RegisteredMachine[];
  delivery?: {
    last_received?: DeliveryMarker;
    last_acknowledged?: DeliveryMarker;
  };
  pending_refreshes: PendingRepositoryRefresh[];
  errors: GitHubAppStatusError[];
}

export interface MachineEnrollment {
  machine: { id: string; name: string };
  identity: { id: string; display_name?: string };
  token: string;
  enrolled_at: string;
}

function isRecord(value: unknown): value is Record<string, unknown> {
  return typeof value === 'object' && value !== null && !Array.isArray(value);
}

function optionalString(value: unknown): value is string | undefined {
  return value === undefined || typeof value === 'string';
}

function safeGitHubURL(value: unknown): string | undefined {
  if (typeof value !== 'string') return undefined;
  try {
    const url = new URL(value);
    return url.protocol === 'https:' && url.hostname === 'github.com' ? url.toString() : undefined;
  } catch {
    return undefined;
  }
}

function parseInstallation(value: unknown): GitHubAppInstallation | undefined {
  if (!isRecord(value) || typeof value.id !== 'string' || typeof value.account !== 'string') return undefined;
  if (!['installed', 'suspended', 'revoked'].includes(String(value.state))) return undefined;
  if (value.account_type !== undefined && !['organization', 'user'].includes(String(value.account_type))) return undefined;
  if (value.repository_selection !== undefined && !['all', 'selected'].includes(String(value.repository_selection))) return undefined;
  if (value.repositories !== undefined && (!Number.isSafeInteger(value.repositories) || Number(value.repositories) < 0)) return undefined;
  return {
    id: value.id,
    account: value.account,
    state: value.state as GitHubInstallationState,
    ...(value.account_type ? { account_type: value.account_type as 'organization' | 'user' } : {}),
    ...(value.repository_selection ? { repository_selection: value.repository_selection as 'all' | 'selected' } : {}),
    ...(value.repositories !== undefined ? { repositories: Number(value.repositories) } : {}),
    ...(safeGitHubURL(value.manage_url) ? { manage_url: safeGitHubURL(value.manage_url) } : {}),
  };
}

function parseMachine(value: unknown): RegisteredMachine | undefined {
  if (!isRecord(value) || typeof value.id !== 'string' || typeof value.name !== 'string') return undefined;
  if (value.state !== undefined && !['online', 'offline', 'unknown'].includes(String(value.state))) return undefined;
  if (!optionalString(value.last_seen_at)) return undefined;
  return {
    id: value.id,
    name: value.name,
    ...(value.state ? { state: value.state as RegisteredMachine['state'] } : {}),
    ...(value.last_seen_at ? { last_seen_at: value.last_seen_at } : {}),
  };
}

function parseDeliveryMarker(value: unknown): DeliveryMarker | undefined {
  if (!isRecord(value) || typeof value.occurred_at !== 'string') return undefined;
  if (!optionalString(value.delivery_id) || !optionalString(value.event)) return undefined;
  return {
    occurred_at: value.occurred_at,
    ...(value.delivery_id ? { delivery_id: value.delivery_id } : {}),
    ...(value.event ? { event: value.event } : {}),
  };
}

function parsePendingRefresh(value: unknown): PendingRepositoryRefresh | undefined {
  if (!isRecord(value) || typeof value.id !== 'string' || typeof value.repository !== 'string' || typeof value.queued_at !== 'string') return undefined;
  if (![value.event, value.installation_id, value.machine_id].every(optionalString)) return undefined;
  return {
    id: value.id,
    repository: value.repository,
    queued_at: value.queued_at,
    ...(value.event ? { event: value.event as string } : {}),
    ...(value.installation_id ? { installation_id: value.installation_id as string } : {}),
    ...(value.machine_id ? { machine_id: value.machine_id as string } : {}),
  };
}

function parseError(value: unknown): GitHubAppStatusError | undefined {
  if (!isRecord(value) || typeof value.code !== 'string' || typeof value.message !== 'string') return undefined;
  if (!optionalString(value.action) || !optionalString(value.installation_id)) return undefined;
  return {
    code: value.code,
    message: value.message,
    ...(value.action ? { action: value.action } : {}),
    ...(safeGitHubURL(value.action_url) ? { action_url: safeGitHubURL(value.action_url) } : {}),
    ...(value.installation_id ? { installation_id: value.installation_id } : {}),
  };
}

function parseList<T>(value: unknown, parser: (item: unknown) => T | undefined): T[] | undefined {
  if (value === undefined) return [];
  if (!Array.isArray(value)) return undefined;
  const parsed = value.map(parser);
  return parsed.every(Boolean) ? parsed as T[] : undefined;
}

export function parseGitHubAppStatus(value: unknown): GitHubAppStatus | undefined {
  if (!isRecord(value) || typeof value.generated_at !== 'string' || !isRecord(value.connection)) return undefined;
  if (!['connected', 'disconnected', 'attention'].includes(String(value.connection.state))) return undefined;
  if (![value.connection.app_name, value.connection.account].every(optionalString)) return undefined;

  const installations = parseList(value.installations, parseInstallation);
  const machines = parseList(value.machines, parseMachine);
  const pendingRefreshes = parseList(value.pending_refreshes, parsePendingRefresh);
  const errors = parseList(value.errors, parseError);
  if (!installations || !machines || !pendingRefreshes || !errors) return undefined;

  let delivery: GitHubAppStatus['delivery'];
  if (value.delivery !== undefined) {
    if (!isRecord(value.delivery)) return undefined;
    const lastReceived = value.delivery.last_received === undefined ? undefined : parseDeliveryMarker(value.delivery.last_received);
    const lastAcknowledged = value.delivery.last_acknowledged === undefined ? undefined : parseDeliveryMarker(value.delivery.last_acknowledged);
    if (value.delivery.last_received !== undefined && !lastReceived) return undefined;
    if (value.delivery.last_acknowledged !== undefined && !lastAcknowledged) return undefined;
    delivery = {
      ...(lastReceived ? { last_received: lastReceived } : {}),
      ...(lastAcknowledged ? { last_acknowledged: lastAcknowledged } : {}),
    };
  }

  return {
    generated_at: value.generated_at,
    connection: {
      state: value.connection.state as GitHubAppConnectionState,
      ...(value.connection.app_name ? { app_name: value.connection.app_name as string } : {}),
      ...(value.connection.account ? { account: value.connection.account as string } : {}),
      ...(safeGitHubURL(value.connection.connect_url) ? { connect_url: safeGitHubURL(value.connection.connect_url) } : {}),
    },
    installations,
    machines,
    ...(delivery ? { delivery } : {}),
    pending_refreshes: pendingRefreshes,
    errors,
  };
}

export function githubStatusErrorMessage(status: number, code?: unknown): string {
  if (status === 401 || status === 403 || code === 'viewer_unavailable') {
    return 'Sign in with an authorized account to view GitHub App delivery status.';
  }
  if (code === 'control_plane_not_configured') {
    return 'GitHub App status is not available because the Workbench control plane is not configured.';
  }
  if (status === 404 || code === 'not_found') {
    return 'No GitHub App enrollment is available for this account.';
  }
  return 'GitHub App status could not be loaded. Retry when the control plane is available.';
}

export function machineNameError(value: string): string | undefined {
  const name = value.trim();
  if (!name) return 'Enter a name for this machine.';
  if (name.length > 128) return 'Use 128 characters or fewer.';
  if (!/^[A-Za-z0-9][A-Za-z0-9._-]*$/.test(name)) return 'Use letters, numbers, dots, dashes, or underscores; start with a letter or number.';
  return undefined;
}

export function parseMachineEnrollment(value: unknown): MachineEnrollment | undefined {
  if (!isRecord(value) || !isRecord(value.machine) || !isRecord(value.identity)) return undefined;
  if (typeof value.machine.id !== 'string' || typeof value.machine.name !== 'string') return undefined;
  if (typeof value.identity.id !== 'string' || !optionalString(value.identity.display_name)) return undefined;
  if (typeof value.token !== 'string' || value.token.length === 0 || typeof value.enrolled_at !== 'string') return undefined;
  return {
    machine: { id: value.machine.id, name: value.machine.name },
    identity: {
      id: value.identity.id,
      ...(value.identity.display_name ? { display_name: value.identity.display_name } : {}),
    },
    token: value.token,
    enrolled_at: value.enrolled_at,
  };
}

export async function enrollMachine(name: string, request: typeof fetch = fetch): Promise<MachineEnrollment> {
  const validationError = machineNameError(name);
  if (validationError) throw new Error(validationError);
  const response = await request(machineEnrollmentURL, {
    method: 'POST',
    headers: { accept: 'application/json', 'content-type': 'application/json' },
    credentials: 'include',
    cache: 'no-store',
    body: JSON.stringify({ name: name.trim() }),
  });
  const body: unknown = await response.json().catch(() => undefined);
  const code = body && typeof body === 'object' && 'error' in body ? (body as { error?: unknown }).error : undefined;
  if (!response.ok) {
    if (response.status === 401 || response.status === 403 || code === 'viewer_unavailable') {
      throw new Error('Sign in with an authorized account before enrolling this machine.');
    }
    if (response.status === 409 || code === 'machine_name_conflict') {
      throw new Error('A machine already uses this name. Choose another name or re-enroll that machine.');
    }
    throw new Error('This machine could not be enrolled. Retry when the control plane is available.');
  }
  const enrollment = parseMachineEnrollment(body);
  if (!enrollment) throw new Error('The enrollment service returned an invalid response. No token was stored.');
  return enrollment;
}

export function installationMatchesFilter(installation: GitHubAppInstallation, status: GitHubAppStatus, filter: GitHubStatusFilter): boolean {
  if (filter === 'all') return true;
  if (filter === 'error') {
    return installation.state !== 'installed' || status.errors.some((error) => error.installation_id === installation.id);
  }
  return status.pending_refreshes.some((refresh) => refresh.installation_id === installation.id);
}
