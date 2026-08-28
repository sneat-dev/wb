# Mechanical worktree merge

Use the receipt-backed command when source worktrees are clean, validated, and
compatible enough that no behavioral judgment or conflict resolution is
expected.

```sh
wb worktree merge prepare <source-worktree...> --target main --format json
wb worktree merge land <candidate-worktree-or-receipt> --route auto --format json
wb worktree merge resume <candidate-worktree-or-receipt> --format json
wb worktree merge revert <landing-receipt> --route auto --format json
```

Bare `wb worktree merge <source-worktree...>` performs both phases. Prepare
creates a dedicated integration worktree and receipt without changing the
canonical target or source worktrees. Other agents may rebase onto the exact
candidate SHA while landing waits.

If `origin/<target>` advances before an unpublished candidate lands, WB rebases
the isolated candidate onto the exact new target, records both before/after SHA
pairs, and reruns validation. A conflict aborts the rebase without touching any
source. Once a candidate is published in a PR, WB refuses target-driven history
rewrites instead of force-pushing it.

`--route auto` direct-pushes only when authoritative GitHub branch and ruleset
evidence permits it; otherwise it uses a pull request or refuses unsupported
merge-queue policy. PR text comes from the exact candidate commits. Both routes
wait for exact-head evidence, verify the fetched remote target receipt, and
fast-forward a clean canonical checkout already on the target.

Cleanup is opt-in with `--cleanup` and occurs only after the remote receipt,
post-target checks, and required canonical synchronization. On interruption,
run the receipt's exact `resume_args`. A landed failure retains before/after
target identities; `revert` creates and lands a forward inverse candidate and
never resets or force-pushes shared history.

When post-target CI fails but a forward fix is preferable to a revert, commit
the fix on the same preserved source and rerun `merge prepare`. WB accepts only
an additive source advance, proves the fetched target still contains the failed
landing, and proves the prior candidate either by graph ancestry or by exact
tree equality with its receipted squash landing. It then advances the retained
candidate without rewriting published history, records the failed attempt in
`forward_repairs`, and opens a fresh PR.
Do not edit or terminalize the failed receipt by hand.
