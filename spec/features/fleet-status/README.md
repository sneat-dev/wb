---
format: https://specscore.md/feature-specification
status: Implementing
---

# Feature: Fleet Status

> [SpecScore.**Studio**](https://specscore.studio): | [Explore](https://specscore.studio/app/github.com/sneat-dev/wb/spec/features/fleet-status?op=explore) | [Edit](https://specscore.studio/app/github.com/sneat-dev/wb/spec/features/fleet-status?op=edit) | [Ask question](https://specscore.studio/app/github.com/sneat-dev/wb/spec/features/fleet-status?op=ask) | [Request change](https://specscore.studio/app/github.com/sneat-dev/wb/spec/features/fleet-status?op=request-change) |

**Status:** Implementing
**Source Ideas:** —

## Summary

`wb status` inspects every local repository by default and reports the ones
needing attention because of modified, untracked, conflicted, stashed, or
unpushed work. Clean checkouts are counted rather than listed, so the report is
a worklist; `--all` includes them, and a positional repository path narrows the
same command to one checkout.

## Problem

Fleet work such as sync, migration, and verification needs a preflight view of
local state. Requiring a `--fleet` flag for the common status question makes
the command less direct, while an ad-hoc loop over `git status` hides stashes
and commits that have not reached a remote. A row per repository is also the
wrong default at fleet scale: hundreds of `clean` rows bury the handful that
need a decision, in a terminal and in an agent's context alike.

## Behavior

### Fleet-first selection

#### REQ: default-fleet

Without a positional path, `wb status` MUST inspect every local Git repository
below `--projects-root`. It MUST NOT require or offer a `--fleet` flag. With
one positional repository path, it MUST inspect only that checkout.

#### REQ: filter-compatible

Fleet status MUST compose the existing substring `--filter` with optional
`--match` glob and `--regex` filters on each `org/repo` slug. Results MUST be
sorted by slug regardless of parallel completion order.

### Local-state index

#### REQ: non-mutating-git-state

Status MUST read local Git state only. It MUST NOT fetch, pull, checkout,
modify, commit, push, create a worktree, or contact GitHub.

#### REQ: attention-conditions

A repository MUST have status `attention` when it has modified, untracked,
conflicted, stashed, or unpushed work. A repository with none of those
conditions MUST have status `clean`; a Git inspection failure MUST have status
`error` and cause a non-zero process exit after the full index is produced.

#### REQ: attention-only-by-default

A fleet run MUST report only the repositories whose status is not `clean`, in
every output format, and MUST state how many clean repositories it left out.
`--all` MUST report every inspected repository. A run narrowed to one
positional repository path MUST report that repository whether or not it is
clean, because naming a checkout is a direct question about it.

#### REQ: concise-and-detailed-output

Default Markdown output MUST show one row per reported repository with a
concise state summary. YAML and JSON MUST include the underlying path lists.
`--details` MAY expand the Markdown report with those individual paths and Git
entries.

## Interaction with Other Features

[Fleet Quality](../fleet-quality/README.md) verifies source health; Fleet
Status reports whether a checkout is safe to act on before a quality,
migration, or sync operation starts.

## Acceptance Criteria

### AC: actionable-local-fleet-index

**Requirements:** fleet-status#req:default-fleet, fleet-status#req:filter-compatible, fleet-status#req:non-mutating-git-state, fleet-status#req:attention-conditions, fleet-status#req:attention-only-by-default, fleet-status#req:concise-and-detailed-output

One default command gives an actionable, sorted worklist of the checkouts that
need attention, without changing any checkout. A user can filter or narrow it,
see concise attention reasons, ask for the clean repositories with `--all`, and
obtain detailed machine-readable evidence when needed.

## Open Questions

- Should a later status slice include remote ahead/behind information only when
  an explicit `--fetch` action authorizes network access?
- Should the status index consume the latest Fleet Quality report as a cached
  health column, or keep command results strictly separate?

---
*This document follows the https://specscore.md/feature-specification*
