# Mechanical worktree merge

Use the receipt-backed command when source worktrees are clean, validated, and
compatible enough that no behavioral judgment or conflict resolution is
expected.

```sh
wb worktree merge <source-worktree...> --route auto --cleanup --format json
wb worktree merge prepare <source-worktree...> --target main --progress --format json
wb worktree merge land <candidate-worktree-or-receipt> --route auto --progress --format json
wb worktree merge resume <candidate-worktree-or-receipt> --progress --format json
wb worktree merge revert <landing-receipt> --route auto --progress --format json
wb worktree merge acknowledge-landed-failed <merge-receipt> --apply --actor <operator> --reason <reason>
wb worktree merge acknowledge-stranded-landing <merge-receipt> --apply --actor <operator> --reason <reason>
wb worktree merge seal-validation-failed <merge-receipt> --apply --actor <operator> --reason <reason>
wb worktree merge supersede-validation-failed <merge-receipt> <replacement-worktree> --apply --actor <operator> --reason <reason>
```

Bare `wb worktree merge <source-worktree...>` performs both phases. Prepare
creates a dedicated integration worktree and receipt without changing the
canonical target or source worktrees. Other agents may rebase onto the exact
candidate SHA while landing waits.

Progress is rendered on stderr when attached to a terminal. AI agents running
through a non-terminal tool should pass `--progress`; terminal JSON remains the
only stdout payload, while stage transitions, check observations, elapsed time,
and the next bounded poll stay visible on stderr. `--non-interactive` alone
keeps terminal-only progress disabled.

Prepare validates the candidate first. When every configured candidate check
passes, the receipt says the target baseline was not needed. When a candidate
check fails, WB validates the exact target snapshot and permits only equivalent
pre-existing failures. Repositories may declare safe process-isolated Go test
packages in `.wb/quality.yaml`; merge validation consumes that tracked policy.

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

When a historical prepare `validation_failed` receipt (such as Yardius) or a
land `landed_post_target_ci_failed` receipt (such as Contactus) is stale but
its clean candidate is proved by the immutable Work Log base, every exact clean
receipted source, and the exact freshly fetched remote target to have already
landed, use `acknowledge-landed-failed`. A post-target CI acknowledgement also
requires the receipted landing and failed exact-head CI result. It writes a
separate audited acknowledgement and frees the merger lane for a fresh forward
repair without changing the historical receipt or Work Log. A candidate or
landing that is not an ancestor of the current target, a missing active claim,
or any dirty/drifted receipt/source identity refuses closed; branch names,
patch similarity, and PR state are never substitutes for the ancestry proof.

When a land `conflict` receipt is stuck because its own landing-result read
failed on I/O or environment error -- most commonly because the candidate
worktree was already removed before a resume could confirm the server
landing -- use `acknowledge-stranded-landing` instead. It never reads or
requires the candidate or any receipted source worktree. It proves, using
only GitHub's own remote state, that the receipted pull request reports
MERGED at the exact receipted candidate head, that the server merge commit
and the receipted candidate are both contained in the freshly fetched current
remote target, and that the receipted candidate still contains its own
recorded pre-merge target. It accepts only a conflict receipt that never
recorded a landing SHA but did publish an exact candidate in a pull request; a
receipt that already has a landing SHA is `acknowledge-landed-failed`'s
territory instead. It writes a separate audited acknowledgement and frees the
merger lane without changing the historical receipt or Work Log. A pull
request that is not proved MERGED, or a merge commit or candidate not proved
contained in the current remote target, refuses closed.

When an old prepare `validation_failed` candidate did not land and its source
was later squash-landed, `seal-validation-failed` can prepare the narrow
ancestry-only replacement required by `supersede-validation-failed`. It starts
at the freshly fetched target and adds only the immutable failed-candidate claim
base, receipt target, current target, and exact clean receipted source heads via
a no-content merge. The final tree must equal the fetched target tree exactly;
all roots must be ancestors; every mutable boundary is rechecked. The operation
is dry-run by default and requires an audited actor and reason to apply. It does
not change the failed receipt or any existing Work Log and does not itself
supersede the receipt.

When an old prepare `validation_failed` candidate did not land and diverges
from its replacement, use `supersede-validation-failed`. It admits only the
prepare failure state and only when the old immutable candidate claim base,
receipt target, freshly fetched current target, and every exact clean receipted
source are ancestors of one exact clean replacement worktree with an active
claim. The old failed candidate itself is deliberately not required to be an
ancestor. WB writes a receipt-hash-bound append-only supersession artifact;
it never rewrites the failed receipt or either Work Log. Missing ancestry,
claim identity, clean worktree, or receipt integrity refuses closed.
