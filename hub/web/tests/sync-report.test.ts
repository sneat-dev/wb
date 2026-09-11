import { describe, expect, it } from 'vitest';
import { syncReportLocation } from '../src/data/sync-report';

describe('sync report locator', () => {
  it('builds an immutable GitHub directory from WB report coordinates', () => {
    const sha = '5183c5fe476ebc009717a38a5070798f74a22be7';
    const location = syncReportLocation(new URLSearchParams({
      repo: 'sneat-co/workbench', ref: sha, report: 'sync-20260908T150034Z',
    }));
    expect(location).toEqual({
      repository: 'sneat-co/workbench',
      commitSHA: sha,
      reportID: 'sync-20260908T150034Z',
      githubDirectoryURL: `https://github.com/sneat-co/workbench/tree/${sha}/sync-reports/%24records`,
      recordPrefix: 'sync-20260908T150034Z--',
    });
  });

  it.each([
    { repo: '../secret', ref: 'a'.repeat(40), report: 'sync-1' },
    { repo: 'sneat-co/workbench', ref: 'main', report: 'sync-1' },
    { repo: 'sneat-co/workbench', ref: 'a'.repeat(40), report: '../sync' },
  ])('refuses unsafe or mutable coordinates: $repo $ref $report', (input) => {
    expect(syncReportLocation(new URLSearchParams(input))).toBeUndefined();
  });
});
