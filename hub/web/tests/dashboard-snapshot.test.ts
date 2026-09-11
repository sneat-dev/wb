import { describe, expect, it } from 'vitest';
import {
  authorizedDashboardTimestamp,
  isAuthorizedDashboardResponse,
  isAuthorizedDashboardSummary,
  isEmptyAuthorizedDashboardSummary,
} from '../src/data/dashboard';

const repositoryScope = { kind: 'repo', host: 'github.com', org: 'acme', repo: 'widgets' } as const;

describe('Workbench dashboard authorized snapshots', () => {
  it('requires complete non-negative integer counts', () => {
    const populated = {
      repositories: 1, open_pulls: 2, merged_pulls: 3, open_issues: 4, releases: 5,
    };
    expect(isAuthorizedDashboardSummary(populated)).toBe(true);
    expect(isEmptyAuthorizedDashboardSummary(populated)).toBe(false);
    expect(isEmptyAuthorizedDashboardSummary({
      repositories: 0, open_pulls: 0, merged_pulls: 0, open_issues: 0, releases: 0,
    })).toBe(true);
    expect(isAuthorizedDashboardSummary({ ...populated, open_pulls: -1 })).toBe(false);
    expect(isAuthorizedDashboardSummary({ ...populated, releases: 1.5 })).toBe(false);
  });

  it('binds scoped responses to the exact requested subject and timestamp', () => {
    const summary = { repositories: 1, open_pulls: 2, merged_pulls: 3, open_issues: 4, releases: 5 };
    const user = { generated_at: '2026-09-06T06:00:00Z', summary };
    const repository = {
      scope: 'repository', id: 'github.com/acme/widgets', display_name: 'Widgets', updated_at: '2026-09-06T06:05:00Z', summary,
    };
    expect(isAuthorizedDashboardResponse(user, { kind: 'user' })).toBe(true);
    expect(isAuthorizedDashboardResponse(repository, repositoryScope)).toBe(true);
    if (isAuthorizedDashboardResponse(user, { kind: 'user' })) {
      expect(authorizedDashboardTimestamp(user, { kind: 'user' })).toBe('2026-09-06T06:00:00Z');
    }
    if (isAuthorizedDashboardResponse(repository, repositoryScope)) {
      expect(authorizedDashboardTimestamp(repository, repositoryScope)).toBe('2026-09-06T06:05:00Z');
    }
    expect(isAuthorizedDashboardResponse({ ...repository, id: 'github.com/acme/other' }, repositoryScope)).toBe(false);
    expect(isAuthorizedDashboardResponse({ summary }, { kind: 'user' })).toBe(false);
  });
});
