import { isFleetSnapshot, type FleetSnapshot } from './worktrees';

export const dashboardApiOrigin = 'https://wb-github-app.sneat.dev';
export const dashboardApiPath = '/v0/workbench/dashboard';
export const dashboardApiURL = `${dashboardApiOrigin}${dashboardApiPath}`;

export function dashboardScopeID(scope: DashboardScope): string | undefined {
  if (scope.kind === 'user') return undefined;
  return scope.kind === 'org'
    ? `${scope.host}/${scope.org}`
    : `${scope.host}/${scope.org}/${scope.repo}`;
}

export function dashboardApiURLForScope(scope: DashboardScope): string {
  const id = dashboardScopeID(scope);
  if (!id) return dashboardApiURL;
  const encodedID = encodeURIComponent(id);
  const scopeName = scope.kind === 'org' ? 'organization' : 'repository';
  return `${dashboardApiOrigin}/v0/workbench/stats/${scopeName}/${encodedID}`;
}

export function dashboardScopeFromPath(pathname: string): DashboardScope | undefined {
  const path = pathname.replace(/^\/bench(?=\/|$)/, '').replace(/\/+$/, '') || '/';
  const parts = path.split('/').filter(Boolean).map((part) => {
    try { return decodeURIComponent(part); } catch { return ''; }
  });
  if (parts.length === 1 && parts[0] === 'dashboard') return { kind: 'user' };
  if (parts.length === 3 && parts[0] === 'org' && parts[1] && parts[2]) {
    return { kind: 'org', host: parts[1], org: parts[2] };
  }
  if (parts.length === 4 && parts[0] === 'repo' && parts[1] && parts[2] && parts[3]) {
    return { kind: 'repo', host: parts[1], org: parts[2], repo: parts[3] };
  }
  return undefined;
}

export type DashboardRange = '7d' | '30d' | '90d' | 'all';
export type DashboardScope =
  | { kind: 'user' }
  | { kind: 'org'; host: string; org: string }
  | { kind: 'repo'; host: string; org: string; repo: string };

export interface TrendPoint {
  label: string;
  value: number;
}

export interface Metric {
  label: string;
  value: string;
  detail: string;
  trend?: 'up' | 'down' | 'flat';
}

export interface Phase {
  name: string;
  duration: string;
  share: number;
}

export interface MergeRecord {
  title: string;
  pullRequest: { label: string; href: string };
  issue?: { label: string; href: string };
  mergeCommit: { label: string; href: string };
  release?: { label: string; href: string };
  receipt: { label: string; href: string };
  landedAt: string;
  outcome: 'Healthy' | 'Attention';
}

export interface Leaderboard {
  id: string;
  label: string;
  description: string;
  entries: Array<{ rank: number; name: string; value: string; detail: string }>;
}

/**
 * A privacy-safe CodeGrapher projection. The dashboard service, not the browser,
 * authorizes the repository before it returns one of these records. It may expose
 * aggregate counts and already-authorized identifiers, but never a graph snapshot
 * or an indexing credential.
 */
export interface CodeGraphFreshness {
  access: 'available' | 'not-authorized' | 'unavailable';
  state: 'fresh' | 'stale' | 'incomplete' | 'failed';
  indexedCommit?: string;
  targetCommit?: string;
  indexedAt?: string;
  commitsBehind?: number;
  /** A safe, operator-facing summary. Do not pass backend error bodies through. */
  diagnostic?: string;
}

export interface CodeGraphChange {
  name: string;
  /** Repo-relative path for files, qualified identifier for symbols, or route. */
  locator: string;
}

export interface CodeGraphImpact {
  affectedSymbols: number;
  affectedTests: number;
  /** A ranked, advisory estimate from the graph. It is never a test receipt. */
  likelyAffectedTests: number;
}

export interface CodeGraphDashboard {
  freshness: CodeGraphFreshness;
  changed: {
    files: CodeGraphChange[];
    packages: CodeGraphChange[];
    types: CodeGraphChange[];
    functions: CodeGraphChange[];
    routes: CodeGraphChange[];
  };
  impact: CodeGraphImpact;
  explorer?: {
    href: string;
    label: string;
  };
}

export interface DashboardView {
  scope: DashboardScope;
  title: string;
  eyebrow: string;
  description: string;
  range: DashboardRange;
  metrics: Metric[];
  phases: Phase[];
  trends: {
    tokens: TrendPoint[];
    cost: TrendPoint[];
    landed: TrendPoint[];
  };
  merges: MergeRecord[];
  leaderboards: Leaderboard[];
  /** Omitted when the current scope has no repository-level graph projection. */
  codeGraph?: CodeGraphDashboard;
  privacyNote: string;
  updatedAt: string;
  availability?: 'live' | 'loading' | 'empty' | 'unavailable';
}

/**
 * CodeGrapher understands a forge/org/repository route, an optional repository
 * path, and `branch` plus `q` query context. A commit SHA is intentionally placed
 * in `branch`: that pins the view without asking the public viewer to discover a
 * newer remote ref. Callers only create a link from an authorized server result.
 */
export function codeGrapherExplorerHref(
  host: string,
  org: string,
  repo: string,
  commit: string,
  scopePath = '',
  symbol = '',
): string {
  const safePath = scopePath.split('/').filter(Boolean).map(encodeURIComponent).join('/');
  const url = new URL(`https://codegrapher.dev/${encodeURIComponent(host)}/${encodeURIComponent(org)}/${encodeURIComponent(repo)}${safePath ? `/${safePath}` : ''}`);
  url.searchParams.set('branch', commit);
  if (symbol) url.searchParams.set('q', symbol);
  return url.toString();
}

export interface DashboardDataProvider {
  getDashboard(scope: DashboardScope, range?: DashboardRange): Promise<DashboardView>;
}

export interface AuthorizedDashboardSummary {
  repositories: number;
  open_pulls: number;
  merged_pulls: number;
  open_issues: number;
  releases: number;
}

export interface ControlPlaneDashboard {
  generated_at: string;
  summary: AuthorizedDashboardSummary;
  /** Optional until every registered machine publishes the WB fleet projection. */
  fleet?: FleetSnapshot;
}

export interface ControlPlaneStat {
  scope: 'repository' | 'organization' | 'user';
  id: string;
  display_name: string;
  summary: AuthorizedDashboardSummary;
  updated_at: string;
  fleet?: FleetSnapshot;
}

export type AuthorizedDashboardResponse = ControlPlaneDashboard | ControlPlaneStat;

const summaryFields = ['repositories', 'open_pulls', 'merged_pulls', 'open_issues', 'releases'] as const;

function isRecord(value: unknown): value is Record<string, unknown> {
  return typeof value === 'object' && value !== null && !Array.isArray(value);
}

function isCount(value: unknown): value is number {
  return typeof value === 'number' && Number.isSafeInteger(value) && value >= 0;
}

export function isAuthorizedDashboardSummary(value: unknown): value is AuthorizedDashboardSummary {
  if (!isRecord(value)) return false;
  return summaryFields.every((field) => isCount(value[field]));
}

export function isEmptyAuthorizedDashboardSummary(summary: AuthorizedDashboardSummary): boolean {
  return summaryFields.every((field) => summary[field] === 0);
}

export function isAuthorizedDashboardResponse(value: unknown, scope: DashboardScope): value is AuthorizedDashboardResponse {
  if (!isRecord(value) || !isAuthorizedDashboardSummary(value.summary)) return false;
  if (value.fleet !== undefined && !isFleetSnapshot(value.fleet)) return false;
  if (scope.kind === 'user') return typeof value.generated_at === 'string';
  const expectedScope = scope.kind === 'org' ? 'organization' : 'repository';
  return value.scope === expectedScope &&
    value.id === dashboardScopeID(scope) &&
    typeof value.display_name === 'string' &&
    typeof value.updated_at === 'string';
}

export function authorizedDashboardTimestamp(response: AuthorizedDashboardResponse, scope: DashboardScope): string {
  return scope.kind === 'user'
    ? (response as ControlPlaneDashboard).generated_at
    : (response as ControlPlaneStat).updated_at;
}

export function dashboardErrorMessage(code: unknown): string | undefined {
  switch (code) {
    case 'control_plane_not_configured':
      return 'The authorized dashboard source is not configured yet.';
    case 'viewer_unavailable':
      return 'The authorized dashboard source could not verify your access.';
    case 'not_found':
      return 'This dashboard scope is unavailable or you do not have access to it.';
    default:
      return undefined;
  }
}

function unavailableDashboard(scope: DashboardScope, range: DashboardRange, description = 'Live Workbench signals will appear here once the authorized dashboard service is connected.'): DashboardView {
  return {
    scope,
    availability: 'unavailable',
    title: scope.kind === 'repo'
      ? `${scope.host}/${scope.org}/${scope.repo}`
      : scope.kind === 'org'
        ? `${scope.host}/${scope.org}`
        : 'Your Workbench',
    eyebrow: 'Dashboard foundation',
    description,
    range,
    metrics: [], phases: [],
    trends: { tokens: [], cost: [], landed: [] },
    merges: [], leaderboards: [],
    privacyNote: 'No telemetry is displayed until an authorized live provider responds. This page does not substitute sample data for your work.',
    updatedAt: 'Waiting for an authorized data source',
  };
}

/** A static dashboard shell never queries an API for its build-time route. */
export function dashboardShell(scope: DashboardScope, range: DashboardRange = '30d'): DashboardView {
  return unavailableDashboard(scope, range);
}

function overviewDashboard(scope: DashboardScope, range: DashboardRange, body: ControlPlaneDashboard): DashboardView {
  const summary = body.summary;
  const title = scope.kind === 'repo' ? `${scope.host}/${scope.org}/${scope.repo}` : scope.kind === 'org' ? `${scope.host}/${scope.org}` : 'Your Workbench';
  return {
    scope, availability: 'live', title, eyebrow: 'Live Workbench data',
    description: 'Authorized control-plane summary from the Workbench GitHub App.', range,
    metrics: [
      { label: 'Repositories', value: String(summary.repositories), detail: 'Authorized repositories' },
      { label: 'Open pull requests', value: String(summary.open_pulls), detail: 'Currently open' },
      { label: 'Merged pull requests', value: String(summary.merged_pulls), detail: 'Recorded by the control plane' },
      { label: 'Open issues', value: String(summary.open_issues), detail: 'Currently open' },
      { label: 'Releases', value: String(summary.releases), detail: 'Recorded releases' },
    ],
    phases: [], trends: { tokens: [], cost: [], landed: [] }, merges: [], leaderboards: [],
    privacyNote: 'Only data disclosed by the authorized Workbench control plane is shown.',
    updatedAt: body.generated_at,
  };
}

function statDashboard(scope: DashboardScope, range: DashboardRange, body: ControlPlaneStat): DashboardView {
  return overviewDashboard(scope, range, {
    generated_at: body.updated_at,
    summary: body.summary,
  });
}

class ApiDashboardProvider implements DashboardDataProvider {
  async getDashboard(scope: DashboardScope, range: DashboardRange = '30d'): Promise<DashboardView> {
    const response = await fetch(dashboardApiURLForScope(scope), {
      headers: { accept: 'application/json' },
    });
    if (!response.ok) {
      const body = await response.json().catch(() => ({})) as { error?: unknown; message?: unknown };
      const description = dashboardErrorMessage(body.error ?? body.message);
      if (description) return unavailableDashboard(scope, range, description);
      throw new Error(`Dashboard request failed (${response.status})`);
    }
    const body = await response.json() as ControlPlaneDashboard | ControlPlaneStat;
    return scope.kind === 'user'
      ? overviewDashboard(scope, range, body as ControlPlaneDashboard)
      : statDashboard(scope, range, body as ControlPlaneStat);
  }
}

class UnavailableDashboardProvider implements DashboardDataProvider {
  async getDashboard(scope: DashboardScope, range: DashboardRange = '30d'): Promise<DashboardView> {
    return unavailableDashboard(scope, range);
  }
}

export function dashboardProvider(): DashboardDataProvider {
  if (import.meta.env.WB_DASHBOARD_SOURCE === 'remote') return new ApiDashboardProvider();
  return new UnavailableDashboardProvider();
}
