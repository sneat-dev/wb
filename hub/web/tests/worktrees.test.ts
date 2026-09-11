import { describe, expect, it } from 'vitest';
import {
  flattenFleetWorktrees,
  isFleetSnapshot,
  matchesWorktreeFilters,
  worktreeFiltersFromSearch,
  worktreeNeedsAttention,
  writeWorktreeFilters,
} from '../src/data/worktrees';

const snapshot = {
  machines: [
    {
      name: 'laptop', stale: false, last_seen_at: '2026-09-06T10:00:00Z',
      worktrees: [{ task: 'dashboard', repository: 'sneat-co/workbench-web', branch: 'feat/dashboard', owner_state: 'active' }],
    },
    {
      name: 'vm', state: 'offline', published_at: '2026-09-05T10:00:00Z',
      worktrees: [{
        task: 'release', stream: 'cli-rollout', repository: 'sneat-dev/wb', branch: 'stream/cli-rollout',
        status: 'blocked', needs_attention: true, attention_reason: 'CI failed', last_activity_at: '2026-09-05T09:55:00Z',
        pull_request: { number: 431, url: 'https://github.com/sneat-dev/wb/pull/431', state: 'open' },
      }],
    },
  ],
} as const;

describe('fleet worktree dashboard contract', () => {
  it('accepts WB-compatible machine snapshots and preserves freshness evidence', () => {
    expect(isFleetSnapshot(snapshot)).toBe(true);
    if (!isFleetSnapshot(snapshot)) throw new Error('snapshot should be valid');
    const rows = flattenFleetWorktrees(snapshot);
    expect(rows).toHaveLength(2);
    expect(rows[0]).toMatchObject({ machine: 'laptop', machine_state: 'online', machine_last_seen_at: '2026-09-06T10:00:00Z' });
    expect(rows[1]).toMatchObject({ machine: 'vm', machine_state: 'offline', machine_last_seen_at: '2026-09-05T10:00:00Z' });
    expect(worktreeNeedsAttention(rows[1])).toBe(true);
  });

  it('rejects unsafe or incomplete worktree links', () => {
    expect(isFleetSnapshot({ machines: [{ name: 'vm', worktrees: [{ task: 'x', repository: 'a/b', branch: 'x', pull_request: { number: 1, url: 'javascript:alert(1)', state: 'open' } }] }] })).toBe(false);
    expect(isFleetSnapshot({ machines: [{ name: 'vm', worktrees: [{ task: 'x', repository: 'a/b' }] }] })).toBe(false);
  });

  it('round-trips URL filters and matches machine, repo, status, task or stream, and attention', () => {
    if (!isFleetSnapshot(snapshot)) throw new Error('snapshot should be valid');
    const rows = flattenFleetWorktrees(snapshot);
    const filters = worktreeFiltersFromSearch('?range=30d&machine=vm&repository=sneat-dev%2Fwb&status=blocked&task=cli&attention=1');
    expect(rows.filter((row) => matchesWorktreeFilters(row, filters))).toEqual([rows[1]]);
    const params = writeWorktreeFilters(new URLSearchParams('range=30d'), filters);
    expect(params.get('range')).toBe('30d');
    expect(params.get('machine')).toBe('vm');
    expect(params.get('attention')).toBe('1');
  });
});
