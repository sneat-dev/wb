---
format: https://specscore.md/feature-specification
status: Draft
---

# Default branch migration

> [SpecScore.**Studio**](https://specscore.studio): | [Explore](https://specscore.studio/app/github.com/sneat-dev/wb/spec/features/default-branch-migration?op=explore) | [Edit](https://specscore.studio/app/github.com/sneat-dev/wb/spec/features/default-branch-migration?op=edit) | [Ask question](https://specscore.studio/app/github.com/sneat-dev/wb/spec/features/default-branch-migration?op=ask) | [Request change](https://specscore.studio/app/github.com/sneat-dev/wb/spec/features/default-branch-migration?op=request-change) |
**Status:** Draft

## Outcome

WB audits and, only with explicit scope and `--apply`, migrates a repository's
GitHub default branch to the configured fleet or organization policy.

## Safety contract

The audit records exact repository default and branch-head observations. Apply
re-observes them before a rename or same-SHA default switch, then proves the
new default and head after GitHub completes the operation. A branch with a
different target SHA is never promoted or overwritten.

Archived repositories, fork outbound-pull-request uncertainty, source-branch
pull requests, Pages, protection/rules effects, and concrete workflow
references to the source branch are exception rows. A repository with no
initial commit is also an exception row even if GitHub advertises a default
branch name. They require a separate,
reviewed migration; this command never performs a broad `master` replacement.

An archived repository may be included only with `--temporarily-unarchive`.
WB records its numeric GitHub repository ID and the initial default/head before
the unarchive request, persists each state around the remote mutation, and
restores `archived=true` before canonical-clone reconciliation. A partial run
can be recovered only with `--restore-archive-from` and the exact caller-held
SHA-256 of that apply report. Recovery accepts one exact `--repo`, verifies the
recorded ID/default/head, writes a new receipt, and changes only the archive
state.

When a remote change succeeds, WB refreshes every matching local canonical
clone and only renames its old local branch when it is clean, has no unpublished
commits or linked worktrees, has no destination branch, and exactly matches the
fresh remote head. A prior apply report may be supplied with `--reconcile-from`
and its caller-held exact-byte `--reconcile-sha256` to resume that bounded
local action on a later host. The receipt is trusted operator input; its digest
is an integrity binding for the caller's selected bytes, not a signature or
authentication mechanism. WB preserves divergent or
otherwise active local state and records the blocker instead of resetting,
stashing, or rewriting it.

## Policy

`fleet.default_branch` supplies the fleet default. An organization override at
`fleet.organizations.<owner>.default_branch` wins, and `--branch` wins for one
invocation. `--all-orgs` explicitly selects every accessible organization;
personal repositories remain opt-in with `--user`.

## Open Questions

None at this time.
