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
wb worktree merge acknowledge-missing-cleanup <merge-receipt> --apply --actor <operator> --reason <reason>
wb worktree merge acknowledge-stranded-landing <merge-receipt> --apply --actor <operator> --reason <reason>
wb worktree merge acknowledge-absorbed-conflict <merge-receipt> --apply --actor <operator> --reason <reason>
wb worktree merge acknowledge-receipt-collision <merge-receipt> --expected-receipt-sha256 <sha256> --expected-immutable-claim-sha256 <sha256> --expected-target <sha> --expected-candidate <sha> --expected-current-source <sha> --expected-historical-refresh-source <sha> --apply --actor <operator> --reason <reason>
wb worktree merge adopt-published-candidate <unlanded-receipt> <pull-request> --apply --actor <operator> --reason <reason>
wb worktree merge seal-validation-failed <merge-receipt> --apply --actor <operator> --reason <reason>
wb worktree merge supersede-validation-failed <merge-receipt> <replacement-worktree> --apply --actor <operator> --reason <reason>
wb worktree merge correct-self-supersession <merge-receipt> <replacement-worktree> --expected-supersession-sha256 <sha256> --expected-immutable-claim-sha256 <sha256> --apply --actor <operator> --reason <reason>
wb worktree merge prepare-published-forward-repair <failed-merge-receipt> <current-source-worktree...> --expected-receipt-sha256 <sha256> --expected-immutable-claim-sha256 <sha256> --expected-supersession-sha256 <sha256> --expected-current-target <sha> --expected-source-sha <sha> --apply --actor <operator> --reason <reason>
wb worktree merge prepare-conflict-replacement <conflict-receipt> <receipted-source-worktree...> --expected-receipt-sha256 <sha256> --expected-immutable-claim-sha256 <sha256> --expected-current-target <sha> --expected-source-sha <sha> --apply --actor <operator> --reason <reason> --progress
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

Resuming a `prepare/validation_failed` (or interrupted `prepare/preparing`)
receipt re-runs candidate validation for the exact candidate SHA before doing
anything else, using the receipt's stored validation timeouts unless
`--prepare-timeout`/`--check-timeout`/`--shard-attempt-timeout` explicitly
override them; it never treats a stale failure as an automatic pass. Resuming
a receipt that is already in the land phase — an open pull request or a
direct-push target already recorded — behaves the same way whenever its
candidate has moved past the SHA that was last proven or published: a
`validation_failed` status, a validation report recorded against a different
revision, or a validation identity that no longer names the exact candidate
SHA all trigger the same exact-candidate re-validation before any push,
skipped only when `--stop-before-merge` already re-validated the preserved
candidate earlier in the same call, or when the candidate has already been
pushed at its exact current SHA (that push's own remote-CI proof stands).
Every publish and landing transition — pushing the candidate branch, a direct
push of the target, opening or adopting a pull request, and merging it — then
passes through one guard that refuses unless the receipt has left
`validation_failed` for that exact candidate and the recorded validation
identity still names that exact SHA, naming the candidate and its validation
status and pointing back at `wb worktree merge resume <receipt>` to
re-validate. The only carve-out is an already-published candidate whose
recorded `PublishedCandidateSHA` still names the exact current candidate SHA
and whose status has not itself reverted to `validation_failed`: that push's
pre-push gate and the remote CI checks that followed are the proof, not a
repeated local validation. It does NOT cover an advance — once the candidate
SHA moves past `PublishedCandidateSHA`, the new head is unproven and must
pass through the land-phase re-validation above before this guard will ever
let it publish.

After a verified batch landing, WB uses the exact source commits preserved in
the candidate merge graph to find their pull requests. It closes an open source
pull request only when its current head still equals that exact commit and its
base repository and branch still equal the landed target. The landing receipt
records the observation, and an idempotently marked comment links the source PR
to the batch PR and landing commit. An advanced head or changed base stays open.

If a legacy landed cleanup removed every receipted worktree and local and
remote branch but failed to retain terminal Work Log evidence, first run
`acknowledge-missing-cleanup` without `--apply`. It re-fetches the exact remote
target, proves the receipted landing is still contained, and checks every exact
asset is absent. Applying with an actor and reason writes a separate immutable
acknowledgement. A subsequent `merge resume --cleanup` revalidates the receipt,
target ancestry, acknowledgement, and absent assets before completing. It never
creates replacement Work Logs or weakens cleanup for live or partial assets.

When post-target CI fails but a forward fix is preferable to a revert, commit
the fix on the same preserved source and rerun `merge prepare`. WB accepts only
an additive source advance, proves the fetched target still contains the failed
landing, and proves the prior candidate either by graph ancestry or by exact
tree equality with its receipted squash landing. It then advances the retained
candidate without rewriting published history, records the failed attempt in
`forward_repairs`, and opens a fresh PR.
Do not edit or terminalize the failed receipt by hand.

If a `prepare/conflict` receipt lost only WB's publication fields after its
candidate was externally pushed and an open pull request was created, use
`adopt-published-candidate` before retrying prepare. First run it without
`--apply`; apply only after it proves the exact receipt repository, target,
candidate branch and SHA, active claims, clean worktrees, and remote branch.
It writes an append-only acknowledgement, never force-pushes or rewrites the
receipt. A normal prepare can then consume that proof when one source advances
cleanly by descent, preserving the published predecessor and pull request.

For the one audited preparing-receipt collision recovery, use
`acknowledge-receipt-collision` only with all six explicit expected digests and
revisions. It writes only the append-only acknowledgement beside the receipt;
the historical `validation_failed` state is an operator assertion because the
pre-mutation receipt bytes are unavailable. Normal prepare stays blocked after
that acknowledgement and the audited rebatch path rechecks it before use.

An explicit `prepare --rebatch-receipt` may replace an exact open, published,
unlanded candidate after the target advances by proven fast-forward ancestry.
The source set must still add a distinct branch and retain every original
source by ancestry. Candidate/ref drift, a closed or merged PR, target rewind
or divergence, and landed candidates remain refusals. The replacement starts
from the freshly fetched target and its append-only acknowledgement records
both target SHAs; the original receipt and candidate stay unchanged. An
unpublished prepared receipt still requires an unchanged target.

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

When an old unpublished prepare `validation_failed` or `conflict` candidate did
not land and diverges from its replacement, use `supersede-validation-failed`.
It admits only those prepare failure states and only when the old immutable candidate claim base,
receipt target, freshly fetched current target, and every exact clean receipted
source are ancestors of one exact clean replacement worktree with an active
claim. The old failed candidate itself is deliberately not required to be an
ancestor. WB writes a receipt-hash-bound append-only supersession artifact;
it never rewrites the failed receipt or either Work Log. Missing ancestry,
claim identity, clean worktree, or receipt integrity refuses closed.
If an unpublished conflict candidate has advanced to a clean strict descendant,
WB records that observed commit in the supersession and requires the replacement
to contain both the receipted candidate and the observed descendant.

When an unpublished prepare `conflict` receipt is stuck because every one of
its receipted source worktrees is already gone -- so neither resume nor
`prepare-conflict-replacement`/`supersede-validation-failed` can recover it,
since both require an exact clean receipted source worktree to still exist --
use `acknowledge-absorbed-conflict`. It proves, source by source, that each
receipted source's exact content is already reachable from the freshly
fetched current remote target: either the receipted source SHA is a graph
ancestor of that target, or path by path relative to its merge-base with the
target: an identical blob there (an unrelated later commit landed the same
content), or, automatically for a `*.jsonl` append-only ledger path, every
line the source added relative to the merge-base present verbatim as a line
in the target's copy (`lines_absorbed`, with per-path added/matched line
counts recorded in the sidecar). Repeatable `--derived-path <path>` audits an
operator exclusion for one known generated-index shape -- exactly
`README.md` nested anywhere under a repo-root `spec/` directory
(`spec/**/README.md`) -- and only when that exact path also exists on the
fetched target; every other shape, or a path absent from the target, refuses
closed. Every excused path is recorded in the sidecar alongside the actor and
reason, and both the dry-run and applied reports list them. It never reads or
requires a receipted source worktree, never rewrites the historical receipt
or any Work Log, and never deletes the preserved, unpublished candidate
worktree. It writes a separate audited acknowledgement and frees the merger
lane for a fresh candidate. A source worktree that still exists, a receipt
that already published a candidate or recorded a landing SHA, an invalid or
absent `--derived-path`, or any path whose content cannot be proved reachable
refuses closed.

If a historical supersession acknowledgement incorrectly named the failed
candidate as its own replacement, do not edit it. Use
`correct-self-supersession` only with the exact existing acknowledgement and
immutable-claim SHA256 values plus a distinct replacement. It creates one
hash-pinned correction artifact; missing, malformed, tampered, or conflicting
corrections leave the self-supersession ineffective and refuse closed.

When that exact self-supersession is the only reason a distinct replacement
cannot yet be prepared, `prepare-published-forward-repair` is the sole cycle
breaker. Pin the immutable failed receipt and claim, the corrupt acknowledgement,
the exact current target, and every current source. WB reads receipted and
source-refresh tuples from the immutable receipt as historical DAG roots; they
need not remain live worktrees. Each supplied source is instead checked as an
exact clean WB-managed current worktree with an active claim and pinned HEAD.
It creates a new clean candidate only after proving every historical source,
claim base, target, and current source is an ancestor; it writes no merge
receipt and never changes the historical receipt, claim, acknowledgement, or
collision evidence. Pass its candidate only to `correct-self-supersession`;
normal `prepare` remains blocked.

When an unlanded conflict receipt still owns the lane that a replacement needs,
use `prepare-conflict-replacement`. Pin the immutable receipt and claim, the
fresh target, and every receipted source in receipt order. It admits only an
unpublished clean conflict candidate; an observed strict descendant is retained
as a required root. The command creates no receipt or acknowledgement, so pass
the resulting candidate to `supersede-validation-failed` to append the only
lane-releasing transition. Use `--progress` for heartbeat updates during long
Git operations while JSON output remains stable.
