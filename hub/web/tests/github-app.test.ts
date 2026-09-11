import { readFileSync } from 'node:fs';
import { describe, expect, it } from 'vitest';
import {
  githubAppStatusPath,
  githubStatusErrorMessage,
  installationMatchesFilter,
  machineEnrollmentPath,
  machineNameError,
  parseGitHubAppStatus,
  parseMachineEnrollment,
} from '../src/data/github-app';

const pageSource = readFileSync(new URL('../src/pages/dashboard/github.astro', import.meta.url), 'utf8');
const viewSource = readFileSync(new URL('../src/components/GitHubAppStatusView.astro', import.meta.url), 'utf8');

const connectedResponse = {
  generated_at: '2026-09-06T18:00:00Z',
  connection: {
    state: 'connected',
    app_name: 'Workbench',
    account: 'alex',
    connect_url: 'https://github.com/settings/installations/123',
  },
  installations: [
    { id: 'installed-1', account: 'sneat-dev', account_type: 'organization', state: 'installed', repository_selection: 'all', repositories: 4 },
    { id: 'suspended-1', account: 'acme', state: 'suspended' },
  ],
  machines: [{ id: 'laptop', name: 'Alex laptop', state: 'online', last_seen_at: '2026-09-06T17:59:00Z' }],
  delivery: {
    last_received: { delivery_id: 'delivery-8', event: 'push', occurred_at: '2026-09-06T17:58:00Z' },
    last_acknowledged: { delivery_id: 'delivery-7', occurred_at: '2026-09-06T17:57:00Z' },
  },
  pending_refreshes: [{ id: 'refresh-1', repository: 'sneat-dev/wb', queued_at: '2026-09-06T17:58:01Z', installation_id: 'installed-1' }],
  errors: [{ code: 'installation_suspended', message: 'Restore this installation.', installation_id: 'suspended-1', action: 'Review', action_url: 'https://github.com/settings/installations/456' }],
} as const;

describe('GitHub App status adapter', () => {
  it('keeps the provisional GET path isolated and connection actions server supplied', () => {
    expect(githubAppStatusPath).toBe('/v0/workbench/github/status');
    expect(machineEnrollmentPath).toBe('/v0/workbench/machines/enroll');
    expect(pageSource).toContain('<GitHubAppStatusView />');
    expect(viewSource).toContain('data-endpoint={githubAppStatusURL}');
    expect(viewSource).toContain('status.connection.connect_url');
    expect(viewSource).toContain('await enrollMachine(machineName.value)');
  });

  it('accepts an authorized status and filters installations by operational state', () => {
    const status = parseGitHubAppStatus(connectedResponse);
    expect(status).toBeDefined();
    expect(status?.installations).toHaveLength(2);
    expect(status?.delivery?.last_received?.delivery_id).toBe('delivery-8');
    expect(status && status.installations.filter((item) => installationMatchesFilter(item, status, 'pending')).map(({ id }) => id)).toEqual(['installed-1']);
    expect(status && status.installations.filter((item) => installationMatchesFilter(item, status, 'error')).map(({ id }) => id)).toEqual(['suspended-1']);
  });

  it('tolerates absent optional delivery and list fields without inventing data', () => {
    expect(parseGitHubAppStatus({ generated_at: connectedResponse.generated_at, connection: { state: 'disconnected' } })).toEqual({
      generated_at: connectedResponse.generated_at,
      connection: { state: 'disconnected' },
      installations: [],
      machines: [],
      pending_refreshes: [],
      errors: [],
    });
  });

  it('fails closed for malformed records and unsafe action links', () => {
    expect(parseGitHubAppStatus({ ...connectedResponse, connection: { state: 'unknown' } })).toBeUndefined();
    expect(parseGitHubAppStatus({ ...connectedResponse, generated_at: undefined })).toBeUndefined();
    expect(parseGitHubAppStatus({ ...connectedResponse, installations: [{ id: '1', account: 'sneat-dev', state: 'installed', repositories: -1 }] })).toBeUndefined();
    expect(parseGitHubAppStatus({ ...connectedResponse, connection: { state: 'disconnected', connect_url: 'https://example.com/connect' } })?.connection.connect_url).toBeUndefined();
    expect(githubStatusErrorMessage(401)).toContain('Sign in');
    expect(githubStatusErrorMessage(403)).toContain('authorized account');
  });

  it('validates enrollment names and requires the one-time token response shape', () => {
    expect(machineNameError('  ')).toContain('Enter a name');
    expect(machineNameError('a'.repeat(129))).toContain('128 characters');
    expect(machineNameError('Alex laptop')).toContain('letters, numbers');
    expect(machineNameError('alex-laptop')).toBeUndefined();
    expect(parseMachineEnrollment({
      machine: { id: 'machine-1', name: 'alex-laptop' },
      identity: { id: 'viewer-1', display_name: 'Alex' },
      token: 'opaque-token',
      enrolled_at: '2026-09-06T18:00:00Z',
    })?.token).toBe('opaque-token');
    expect(parseMachineEnrollment({ machine: { id: 'machine-1', name: 'alex-laptop' }, identity: { id: 'viewer-1' }, enrolled_at: '2026-09-06T18:00:00Z' })).toBeUndefined();
    expect(viewSource).not.toContain('localStorage');
    expect(viewSource).not.toContain('sessionStorage');
  });

  it('provides accessible states, URL filters, and responsive table labels', () => {
    expect(viewSource).toContain('aria-busy="true"');
    expect(viewSource).toContain('role="alert"');
    expect(viewSource).toContain('aria-current');
    expect(viewSource).toContain("url.searchParams.set('state', filter)");
    expect(viewSource).toContain("cell.dataset.label = label");
    expect(viewSource).toContain('No receipt reported yet.');
    expect(viewSource).toContain('No registered machines were returned');
  });
});
