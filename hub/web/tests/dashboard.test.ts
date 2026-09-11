import { readFileSync } from 'node:fs';
import { describe, expect, it } from 'vitest';
import { codeGrapherExplorerHref, dashboardApiOrigin, dashboardApiPath, dashboardApiURLForScope, dashboardErrorMessage, dashboardProvider, dashboardScopeFromPath, dashboardShell } from '../src/data/dashboard';

const dashboardSource = readFileSync(new URL('../src/pages/dashboard.astro', import.meta.url), 'utf8');
const repoRouteSource = readFileSync(new URL('../src/pages/repo/[host]/[org]/[repo].astro', import.meta.url), 'utf8');
const orgRouteSource = readFileSync(new URL('../src/pages/org/[host]/[org].astro', import.meta.url), 'utf8');
const viewSource = readFileSync(new URL('../src/components/DashboardView.astro', import.meta.url), 'utf8');
const fleetSource = readFileSync(new URL('../src/components/FleetWorktrees.astro', import.meta.url), 'utf8');
const dataSource = readFileSync(new URL('../src/data/dashboard.ts', import.meta.url), 'utf8');
const astroConfigSource = readFileSync(new URL('../astro.config.mjs', import.meta.url), 'utf8');

describe('dashboard public surface', () => {
  it('keeps the public entry point and parameterised repository and organisation routes', () => {
    expect(dashboardSource).toContain('<DashboardView {dashboard} />');
    expect(dashboardSource).toContain('path="/dashboard/"');
    expect(repoRouteSource).toContain("kind: 'repo'");
    expect(orgRouteSource).toContain("kind: 'org'");
    expect(astroConfigSource).toContain("base: '/bench'");
  });

  it('keeps live data unavailable by default and never substitutes sample data', async () => {
    expect(dashboardApiOrigin).toBe('https://wb-github-app.sneat.dev');
    expect(dashboardApiPath).toBe('/v0/workbench/dashboard');
    const dashboard = await dashboardProvider().getDashboard({ kind: 'user' });
    expect(dashboard.availability).toBe('unavailable');
    expect(dashboard.metrics).toHaveLength(0);
    expect(dashboard.merges).toHaveLength(0);
    expect(dashboard.privacyNote).toContain('No telemetry');
    expect(dataSource).toContain('new UnavailableDashboardProvider()');
    expect(dataSource).not.toContain('FixtureDashboardProvider');
    expect(dataSource).not.toContain("availability: 'demo'");
  });

  it('maps provider error codes to honest unavailable messages', () => {
    expect(dashboardErrorMessage('control_plane_not_configured')).toBe('The authorized dashboard source is not configured yet.');
    expect(dashboardErrorMessage('viewer_unavailable')).toBe('The authorized dashboard source could not verify your access.');
    expect(dashboardErrorMessage('control_plane_error')).toBeUndefined();
    expect(dashboardErrorMessage('not_found')).toBe('This dashboard scope is unavailable or you do not have access to it.');
    expect(dataSource).toContain('body.error ?? body.message');
    expect(viewSource).toContain('responseBody.error');
    expect(viewSource).toContain('failureMessage ||');
  });

  it('recognises public scope routes and uses the provider scoped contract', () => {
    expect(dashboardScopeFromPath('/bench/dashboard')).toEqual({ kind: 'user' });
    expect(dashboardScopeFromPath('/bench/org/github.com/acme')).toEqual({ kind: 'org', host: 'github.com', org: 'acme' });
    expect(dashboardScopeFromPath('/bench/repo/github.com/acme/widgets')).toEqual({ kind: 'repo', host: 'github.com', org: 'acme', repo: 'widgets' });
    expect(dashboardApiURLForScope({ kind: 'org', host: 'github.com', org: 'acme' })).toBe('https://wb-github-app.sneat.dev/v0/workbench/stats/organization/github.com%2Facme');
    expect(dashboardApiURLForScope({ kind: 'repo', host: 'github.com', org: 'acme', repo: 'widgets' })).toBe('https://wb-github-app.sneat.dev/v0/workbench/stats/repository/github.com%2Facme%2Fwidgets');
    expect(repoRouteSource).toContain('dashboardShell');
    expect(orgRouteSource).toContain('dashboardShell');
    expect(dashboardShell({ kind: 'repo', host: 'github.com', org: 'acme', repo: 'widgets' }).availability).toBe('unavailable');
  });

  it('pins CodeGrapher explorer links to the authorized repository revision and scope', () => {
    expect(codeGrapherExplorerHref('github.com', 'acme', 'widgets', 'abc123', 'src/main.ts', 'Widget')).toBe(
      'https://codegrapher.dev/github.com/acme/widgets/src/main.ts?branch=abc123&q=Widget',
    );
  });

  it('provides non-visual alternatives and all data availability states', () => {
    expect(viewSource).toContain('View chart data as a table');
    expect(viewSource).toContain('View phase timing as a table');
    expect(viewSource).toContain('data-dashboard-state="loading"');
    expect(viewSource).toContain('data-dashboard-state="empty"');
    expect(viewSource).toContain('data-dashboard-state="error"');
    expect(viewSource).toContain('Change scope and impact');
    expect(viewSource).toContain('Exact graph revision');
    expect(viewSource).toContain('Impact ranks graph neighbours for review');
    expect(viewSource).toContain('data-dashboard-refresh');
    expect(viewSource).toContain('data-dashboard-refresh-button');
    expect(viewSource).toContain('Authorized snapshot');
    expect(viewSource).toContain('<FleetWorktrees />');
    expect(fleetSource).toContain('Worktrees across machines');
    expect(fleetSource).toContain('data-worktree-filter="machine"');
    expect(fleetSource).toContain('data-worktree-filter="repository"');
    expect(fleetSource).toContain('data-worktree-filter="status"');
    expect(fleetSource).toContain('data-worktree-filter="task"');
    expect(fleetSource).toContain('data-worktree-filter="attention"');
    expect(viewSource).not.toContain('new EventSource');
    expect(viewSource).not.toContain("params.get('state')");
    expect(viewSource).toContain("setAttribute('aria-current', 'page')");
    expect(viewSource).toContain('Showing the last authorized snapshot from');
    expect(viewSource).toContain('No published signals in this aggregate snapshot');
    expect(viewSource).not.toContain('Choose a wider date range');
    expect(dataSource).toContain('summary.open_pulls');
    expect(dataSource).toContain('trends: { tokens: [], cost: [], landed: [] }');
    expect(viewSource).not.toContain('139 / 18');
    expect(viewSource).not.toContain('1,247');
    expect(viewSource).not.toContain('Accepted lines / 1k tokens');
    expect(viewSource).not.toContain('Landed tasks / 1M tokens');
  });
});
