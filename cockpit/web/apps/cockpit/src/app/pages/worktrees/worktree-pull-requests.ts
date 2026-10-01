import { FleetDocument, PullRequest } from '@cockpit/fleet-data'

const byWorktree = new WeakMap<FleetDocument, Map<string, PullRequest[]>>()

/**
 * The pull requests of a worktree: the row's PR cell and the panel's related
 * list both ask here, so they always agree. Grouped once per document.
 * TODO(cv-lib2-fleet-data): replace with `model.worktreePullRequests(id)` when the library has it.
 */
export function pullRequestsOf(document: FleetDocument, worktreeId: string): readonly PullRequest[] {
  let groups = byWorktree.get(document)
  if (groups === undefined) {
    groups = new Map()
    for (const pullRequest of document.pull_requests) {
      if (pullRequest.worktree !== undefined) groups.set(pullRequest.worktree, [...(groups.get(pullRequest.worktree) ?? []), pullRequest])
    }
    byWorktree.set(document, groups)
  }
  return groups.get(worktreeId) ?? NONE
}

const NONE: readonly PullRequest[] = []
