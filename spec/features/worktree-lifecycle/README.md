---
format: https://specscore.md/feature-specification
status: Implementing
---

# Feature: Worktree Lifecycle

> [SpecScore.**Studio**](https://specscore.studio): | [Explore](https://specscore.studio/app/github.com/sneat-dev/wb/spec/features/worktree-lifecycle?op=explore) | [Edit](https://specscore.studio/app/github.com/sneat-dev/wb/spec/features/worktree-lifecycle?op=edit) | [Ask question](https://specscore.studio/app/github.com/sneat-dev/wb/spec/features/worktree-lifecycle?op=ask) | [Request change](https://specscore.studio/app/github.com/sneat-dev/wb/spec/features/worktree-lifecycle?op=request-change) |

**Status:** Implementing
**Source Ideas:** —

## Summary

`wb worktree` creates, guards, inventories, and safely cleans task worktrees.
Its default checkout is `<canonical-repository>/.worktrees/<task>`, while
`WB_HOME` remains the user-scoped private home for Work Logs, locks, receipts,
and reports. A user-only absolute shared root may instead place checkouts at
`<root>/<task>/<owner>/<repository>`. `wb worktree list` reports local Git
state with optional GitHub PR evidence; `wb worktree cleanup` safely plans or
applies removal of clean task worktrees and exact merged branch refs.

## Problem

Worktrees inside canonical repository directories protect canonical clones, but
completed tasks accumulate linked checkouts and branches. Ad-hoc cleanup can discard uncommitted work,
delete a reused branch, or remove one repository while a coordinated task is
still active elsewhere. A default-layout migration must not either continue
creating work under an obsolete projects-root directory or strand linked
worktrees and pre-existing hooks. The guard must also distinguish a real,
short-lived Git rebase from an arbitrary detached development checkout.

## Behavior

### Fast local inventory

#### REQ: nonmutating-verified-base

Before creating a new worktree, WB MUST verify the requested
`refs/heads/<base>` by fetching it from `origin`, without requiring the
canonical clone to be clean or on the base branch. It MUST create the feature
branch from the exact verified commit, not an unverified or moving
local/remote ref. Creation MUST NOT switch, pull, reset, fast-forward, stage,
or otherwise alter the canonical checkout, any local base branch, or a nested
linked worktree. A stale local base branch, active local canonical changes, or
one checked out in another linked worktree is not a blocker; an inaccessible,
missing, non-commit, or otherwise unverifiable remote base MUST fail before WB
creates a branch or worktree.

#### REQ: offline-list-default

`wb worktree list [task]` MUST inspect only the local, resolver-recognized
worktree hierarchies and local Git state by default. It MUST contact GitHub and
the remote only when `--github` is explicit.

#### REQ: authoritative-write-home

New Work Logs, task locks, receipts, and cleanup reports MUST use the resolver's
write home: `~/.wb` by default, or the exact directory named by `WB_HOME` when
that variable is set. A populated `<projects-root>/.wb` MUST NOT silently
become the write home. `WB_HOME` MUST remain authoritative for commands later
started by a managed hook installed from that environment, but MUST NOT choose
the default physical checkout location.

#### REQ: local-default-and-user-shared-root

Without a user `worktrees.root` setting, creation MUST place a new checkout at
`<canonical-repository>/.worktrees/<task>`. An explicit root may come only
from the user's `$XDG_CONFIG_HOME/wb/worktrees.yaml` or
`~/.config/wb/worktrees.yaml`; after `~` expansion it MUST be absolute and
places a checkout at `<root>/<task>/<owner>/<repository>`. Repository policy
MUST NOT set or override the root. The creator needs permissions for both the
private `WB_HOME` state and the selected physical checkout directory.

#### REQ: migration-layout-compatibility

Guard, inventory, and cleanup MUST continue to validate and operate on existing
local, configured-shared, and historic `<projects-root>/.wb/worktrees` linked
worktrees governed by the same `WB_HOME`, using their actual placement.
Changing `worktrees.root` MUST NOT relocate them or stop their discovery. A
managed hook that pins the normal default home MUST preserve that compatibility
without treating a user-selected `WB_HOME` as non-authoritative.

#### REQ: legacy-mixed-inventory

Inventory MUST recognize default local `<canonical-repository>/.worktrees/<task>`
entries, configured shared `<task>/<owner>/<repository>` entries, and historic
direct-repository `<task>/<repository>` entries. Once a Git root is recognized,
traversal MUST stop below it. Malformed candidates MUST yield deterministic
diagnostics without hiding valid sibling repositories whenever the command's
result API permits.

#### REQ: validated-identity

Each result MUST be a real linked worktree at the expected task, owner, and
repository path for either supported layout, backed by the expected canonical
clone. Results MUST include task, repository, branch, head, cleanliness, lock
state, last commit time, and local merge state.

### Guard and hooks

#### REQ: point-of-read-canonical-freshness

When `wb worktree guard` is run against a canonical clone, WB MUST fetch the
configured `origin/<base>` target before comparing the exact local `HEAD` with
that fetched ref using left/right commit counts. The result MUST be a
machine-readable freshness receipt containing the target, local and remote
commit IDs, counts, and an explicit status for current, ahead, stale, or
diverged history. A failed fetch, unreachable remote, missing target, or target
that moves during the check MUST be represented explicitly and MUST NOT be
reported as current. The command MUST warn on every non-current status while
leaving the canonical branch, index, and working tree unchanged. Internal hook
callers MAY omit the network check; their checkout-policy result MUST retain
its existing local-only behavior.

#### REQ: verified-publication-after-push

Git offers no post-push hook and runs `pre-push` only when it has refs to
update, so no hook can observe a push that updates nothing. WB MUST therefore
offer an explicit, opt-in verification for a linked worktree: fetch that
worktree's own branch and compare the exact local `HEAD` with it.

The verification MUST distinguish, with a separate remedy for each: `HEAD`
exactly at `origin/<branch>`; `HEAD` ahead of it, meaning local work is not on
the remote; a branch that has never been pushed at all; the remote branch ahead
of `HEAD`; and divergence. It MUST exit non-zero for every state other than
exactly-published, and its refusal MUST name the command that resolves that
specific state.

A state WB could not observe — unreachable remote, failed fetch, or a ref that
moves during the check — MUST be reported as unverified and MUST NEVER be
reported as published. The verification MUST leave the branch, index, and
working tree unchanged, and MUST remain opt-in so no Git hook depends on
reaching the remote.

The guard's detached-`HEAD` refusal MUST state the consequence, not only the
policy: a commit made on a detached `HEAD` is reachable from no branch, so a
subsequent `git push` can truthfully report that everything is up to date while
the work is orphaned.

#### REQ: guarded-transient-rebase

The guard MUST reject detached development by default. It MAY allow a detached
linked worktree only while Git proves a live rebase through its real
`rebase-merge` or `rebase-apply` state. The transient allowance MUST retain all
canonical/common-directory and resolver-layout checks, and MUST end when that
Git state ends.

#### REQ: hook-home-stability

New managed hook shims MUST persist the resolved WB home as well as the
projects root, so their guard invocation uses the same authoritative layout as
installation. Hooks installed by the prior release MUST remain usable after
upgrade through the migration-compatible resolver.

#### REQ: hook-executable-stability

Hook installation or automatic refresh MUST reject a transient executable such
as the binary produced by `go run`. A managed shim MUST point only to a durable
candidate or installed WB executable; otherwise a successful repair can leave
the next Git operation unable to run its guard.

#### REQ: attested-canonical-rescue-push

`wb worktree rescue --push` MUST remain usable through WB's installed managed
pre-push hook while the canonical clone is deliberately still dirty. This is
an explicit rescue route, not a generic hook bypass: the hook MUST accept only
one `refs/heads/rescue/*` update whose exact local commit has the canonical
`HEAD` as its sole parent and whose tree equals a fresh complete capture of the
canonical index and working tree. A missing or partial attestation, another
ref, another commit, a tree mismatch, or an occupied remote rescue ref MUST
refuse. Success MUST be followed by a fresh exact remote-ref receipt before
restore can remove local work.

### Conservative cleanup plan

#### REQ: dry-run-default

`wb worktree cleanup` MUST require one task or `--all-merged` and MUST be a dry
run unless `--apply` is explicit. A 24-hour merged-PR safety window MUST apply
by default; zero MUST explicitly disable it.

#### REQ: exact-remote-target-evidence

A repository MUST be eligible only when it is clean and unlocked, has no open
PR for its branch, and the current local branch head is an ancestor of the
exact freshly fetched `origin/<target>` SHA. A matching merged GitHub PR MAY
supply merge-time evidence for the age window, but a direct push to the target
MUST be a supported integration path. A local-only merge is `awaiting_push`
and ineligible. An existing remote source branch MUST still point to the exact
local head.

#### REQ: resumable-interrupted-operation-lock

A killed operation leaves its task lock behind, because no deferred release
runs. That remnant MUST NOT be indistinguishable from a live operation: WB MUST
decide by whether a process still holds the lock, not by whether the lock entry
exists. A lock held by a live process MUST refuse every caller, and MUST be
reported distinctly from an abandoned one.

Reclaiming an abandoned lock MUST be restricted to resuming a durable backlog
record, which MUST independently revalidate that the worktree path is gone, its
registration is gone, the remote branch is gone, and the local branch still
points at the exact recorded head. A stray lock on a task whose checkout is
still present MUST keep that task reported as locked and ineligible, because no
record describes what remains to finish. A reclaimed lock MUST be retired on
completion exactly as a normally acquired one is.

#### REQ: absorbed-integration-containment-evidence

A target branch that requires linear history forces a merger to batch several
completed candidates onto one integration branch and land that branch once, so
a candidate's own head is absent from the target by construction. Such a
candidate MUST still be eligible, on evidence only.

WB MUST accept a landing receipt from GitHub's own commit-to-pull-request index
for the immutable source commit, and MUST NOT require that pull request's head
to equal the candidate head; a branch name MUST NOT be treated as evidence. The
receipt MUST name a merged pull request into the exact base whose merge commit
is contained in the freshly fetched exact origin target.

Every receipt MUST additionally be proved locally: a three-way merge of the
candidate into the landing commit MUST succeed and produce exactly that
commit's tree, and the same merge into the fetched target MUST produce exactly
the target's tree. Work that landed and was later reverted, or that landed only
in part, MUST therefore remain `awaiting_push`. The proof MUST NOT mutate any
ref, index, or working tree.

`--absorbed-by` MAY name the merged pull request or exact landing commit for an
absorption GitHub cannot associate, such as content cherry-picked rather than
merged into the integration branch. It MUST select which receipt to verify and
MUST NOT substitute for one: every proof above still applies, and the named
commit MUST additionally be exactly where the work entered the target, so the
flag cannot degrade into a bare content assertion. A pointer that fails any
verification MUST refuse only its own candidate, with the failing verification
reported as that candidate's reason, and MUST NOT be reported as a malformed
worktree or abort a fleet sweep.

When `wb worktree abort --disposition discarded --absorbed-by <PR>` uses a
merged pull request as that receipt, WB MUST read the authoritative PR number,
merged timestamp, base target, head SHA, and merge SHA; fetch the exact PR head
from the configured origin without updating a branch ref; prove the exact
source head is an ancestor of that PR head; prove the merge SHA is contained in
the freshly fetched base target; and prove the PR-head tree equals the merge
tree. It MUST then retain the ordinary content/revert, clean-worktree, and
branch-unchanged checks through the removal boundary. Missing metadata, target
drift, source advancement, non-containment, or unequal trees MUST refuse the
one candidate. Commit messages, PR titles, and branch names are not evidence.

#### REQ: coordinated-task-safety

If any repository in a task is ineligible, cleanup MUST mark every repository
in that task ineligible. It MUST preserve skipped work and explain the
blocking evidence.

#### REQ: trusted-supersession-terminalization

An intentionally superseded split branch MAY be terminalized only through an
explicit named-task supersession receipt. The receipt MUST bind the exact
original source head, exact freshly fetched target head, replacement PRs or
commits, and a machine-readable inventory that classifies every source commit
outside the target as replaced, obsolete, regressive, or cosmetic. Every
residual MUST carry a reviewer reason and an explicit reviewed marker. The
receipt MUST also carry a trusted approving actor, approval decision, approval
time, and unique receipt ID. WB MUST NOT infer this disposition from green CI,
a closed PR, branch names, patch identity, or prose. Dry-run and apply MUST
evaluate the same receipt; missing or unclassified residuals, untrusted or
incomplete approval, a changed source ref, a changed target ref, or a
replacement commit not contained in the exact target MUST refuse without
deleting local, remote, worktree, or Work Log state. Before deletion, WB MUST
embed the verified receipt in the archived terminal Work Log, with a distinct
`superseded` disposition that does not claim the original head landed intact.

When the receipt supersedes a dependency pull request, it MUST additionally
record each source PR's exact consumer, ecosystem, manifest/importer selector,
before range, and requested-after range. The integrated candidate MUST be
re-read at the exact target head and prove the same direct package or module,
including its applicable resolved lockfile entry. Family names are not
equivalent (`nx` does not prove `@nx/*` and vice versa). A missing, indirect,
family-only, or source-head-drifted delta MUST refuse terminalization. WB MUST
provide deterministic per-source-PR JSON and Markdown renderings of this
evidence so the independent merger verifier and campaign report consume the
same receipt.

### Long sweep feedback

#### REQ: incremental-sweep-progress

A fleet-wide sweep — `wb worktree cleanup --all-merged`, or any inventory run
with `--github` — fetches and queries a hosted API once per candidate and can
run for minutes. Such a run MUST emit incremental progress to **stderr**,
flushed per event rather than buffered until the report is complete. Each event
MUST name the repository or task being inspected and carry a running `[n/N]`
count, and the run MUST close with a summary carrying totals and elapsed time.

stdout MUST remain reserved for the report, so `--format json` stdout stays
machine-parseable. Progress MUST NOT be suppressed merely because stderr is not
a terminal: an agent reads stderr, and a multi-minute silence is
indistinguishable from a hang. The observed consequence is concrete — a killed
and retried sweep leaves task locks behind that then require
`--resume-interrupted` to clear.

### Audited application

#### REQ: recheck-and-compare-delete

Before mutation, WB MUST refetch the exact remote target, reacquire optional PR
and source-branch evidence, recheck cleanliness, and refuse a moved head. It
MUST durably seal/archive the Work Log before any remote or local deletion,
remove only the identified linked worktree, and use a compare-and-delete
operation for the exact local branch ref.

#### REQ: remote-opt-in

Remote deletion MUST require both `--apply` and `--remote`. It MUST use
force-with-lease against the observed head so an advanced branch is preserved.

#### REQ: evidence-gated-remote-retirement

Finishing a named task MUST NOT leave its source branch on origin, because a
surviving branch is backlog nobody can see. WB MUST decide that from the branch
it actually observed, never from the invocation's flag shape alone: a candidate
whose origin branch is still present MUST be refused with a reason naming that
branch and `--remote`, and a candidate whose origin branch is already gone —
deleted by the merge that landed it, or by an earlier cleanup — MUST be
retired without `--remote`, because there is nothing left to retire. Ordinary
cleanup safety MUST be evaluated before this policy, so a refusal always states
the most specific condition WB observed rather than sending an operator to the
wrong fix.

#### REQ: durable-audit

An apply attempt MUST write a machine-readable plan before its first
destructive Git operation and update the same report with applied or failed
state. The report MUST retain repository, branch, head, PR URL, decision, and
outcome evidence.

#### REQ: resumable-post-removal-backlog

Before Git removes a linked checkout, WB MUST persist a private machine-readable
recovery stage carrying the exact task, repository, worktree registration,
branch, head, remote-ref evidence, and disposition. If the process stops after
worktree removal but before compare-and-delete of the local branch, the same
named cleanup or discarded-abort journey MUST expose that backlog and resume
only after proving the worktree registration is absent, the remote source
branch is absent, and the local ref is either absent or still equals the
recorded head. A worktree path that still exists MUST NOT by itself withhold
the backlog from resumption: registration, not the path, distinguishes a
refused removal from residue (see
worktree-lifecycle#req:unregistered-residue-removal). Completion MUST remain
discoverable even when live worktree inventory no longer contains the task.

A record whose task namespace no longer exists cannot be locked at all. When
every subject it names is also already absent — no checkout at the recorded
path, no registration, no remote source branch, no local ref — WB MUST close
the record without that lock, because the only remaining operation is a write
to WB's own private journal rather than a deletion. If any subject is still
present WB MUST refuse, so that one unresumable record cannot fail every later
fleet sweep and no record still owing a deletion is quietly marked done.

#### REQ: unregistered-residue-removal

Git removes a linked worktree's registration even when it fails partway through
deleting the working tree, and exits non-zero. On a failed removal WB MUST
distinguish the two outcomes by the registration: a worktree Git still
registers was refused and MUST still fail the task, and one Git no longer
registers MUST be treated as WB's own residue and removed by WB itself, after
which the lifecycle MUST continue to the exact local branch deletion rather
than stranding the task.

Residue removal derives its authority from the gates the task already passed —
clean checkout, head integrated into the exact origin target, and a path WB
created below its own worktrees root and still holds a validated descriptor
for. WB MUST NOT gate it on the names of directories inside the checkout;
ignored build output is in scope exactly as it is when Git's own removal
succeeds. WB MUST descend through retained descriptors, MUST NOT follow a
symlink out of the checkout, and MAY grant itself write permission on a
directory of the residue that denies an unlink. A residue directory WB cannot
open MUST be reported with the permission needed, never worked around by name.
An apply report MUST record that WB, rather than Git, removed the checkout.

Because a checkout Git no longer registers is invisible to every enumeration
that reads Git's worktree registry, `wb worktree orphans` MUST additionally
walk WB's own worktrees roots and report each checkout there that no canonical
clone lists, with the evidence for the claim and the exact command that
finishes it. That sweep MUST stay read-only and MUST NOT gain a removal path
of its own: removal is owned by the backlog record and `wb worktree cleanup`.
It MUST report only a recognizable repository checkout — one carrying Git
metadata, or one whose path names a repository that has a canonical clone — so
that a task's own working directories are never reported as lost work.

#### REQ: empty-task-namespace-retirement

A terminal cleanup MUST retire the `<worktrees-root>/<task>` directory it
empties, and MUST report and retire under `--apply` any task namespace an
earlier release left behind, so a finished task leaves no directory at all
rather than one empty shell. Retirement MUST be atomic against any other
writer: a namespace holding anything — a repository, a live lock, a file a
person left — MUST survive untouched. `--filter` selects by owner/repository
slug and cannot scope a namespace with no repository under it, so a filtered
run MUST report such a namespace without retiring it.

Because a namespace can now be retired between the moment an operation opens
its task directory and the moment it takes that directory's lock, every
operation MUST refuse a task directory that was unlinked while it was
starting, rather than build a task hierarchy under a pathname nothing can
reach. That refusal MUST be a retryable error, not a silent success.

#### REQ: internal-stage-terminalization

Inventory MUST classify reserved `.wb-stage-*` and `.wb-retired-stage-*`
entries as WB control-plane artifacts before considering legacy dot-prefixed
Git worktrees. A dry run MUST expose their exact disposition. Under the held
task descriptor/lock, apply MAY atomically archive only the exact recognized
stage that is still empty at the retirement boundary. A non-empty, symlinked,
replaced, or invalid stage MUST remain explicit blocking cleanup backlog and
MUST NOT be silently ignored, deleted, or treated as a repository. A terminal
task MUST have no such artifact left in its active task namespace.

### Audited abort and recycle

#### REQ: discarded-abort-boundary

`wb worktree abort --disposition discarded --apply` MUST also require
`--remote`. Before the first deletion across a coordinated task, WB MUST
corroborate every immutable Work Log claim and live checkout. Immediately
before each removal it MUST repeat clean/head/registration checks, seal the
local archive/outbox, retire only an exact unchanged remote source ref with
force-with-lease, and remove the worktree/local ref through descriptor-anchored
Git operations. Handoff and `not_landed` MUST retain dirty resumable state and
bind exactly one successor instead of deleting it. Before an applied transfer
publishes anything, its creator MUST supply the successor's exact model or
explicit `unknown`, plus independently known optional CLI/provider route
identifiers; WB MUST NOT copy the predecessor's route. Automatic recycle
rollback recovery MUST use explicit unknown model/provenance and no route.
An `--absorbed-by` discarded abort MUST be clean and must persist the verified
PR and landing identity in its ordinary lifecycle result/receipt projection so
the terminal record names evidence rather than an operator assertion.

#### REQ: dirty-discard-sealing

When a discarded abort finds tracked or non-ignored untracked working-tree
bytes, it MUST capture those exact bytes in the private Work Log recovery
archive before deleting any remote ref, worktree, or local branch. The capture
MUST be independent of Git hooks, use the existing private Work Log file
protections and SHA-256 content-digest primitive, and enforce a conservative
bounded total and per-file size before allocating or writing. A dry-run MUST
compute and report the same digest, byte count, and file count that apply will
recheck; apply MUST refuse if any of those values changed. Failed or oversized
captures MUST leave the worktree and every Git ref untouched. The public
receipt MAY expose only bounded capture metadata (digest, size, and count),
never source bytes or local paths.

#### REQ: recycle-transaction

`wb worktree rename --apply` MUST require `--remote`, fetch and pin the fresh
base, preflight every repository before terminalizing the first, retire old
local and exact remote source branches, reset the Work Log projection, and
carry only explicitly allow-listed cache paths. An ordinary runtime failure on
repository N MUST roll repositories 1..N back to their old paths/branches and
active recovery claims so the same coordinated rename is retryable. Durable
terminal/outbox history MUST remain append-only. A process crash MAY require
recovery from those records until automatic journal replay is implemented.
Before the first destination checkout claim exists, rename MUST inventory the
locked destination before reserving its new prompt so its own reservation is
never treated as a collision. An interrupted or refused pre-apply reservation
MUST remain auditable and `wb worktree abort <destination> --disposition
discarded --apply` MUST terminalize that reservation without `--remote`, while
retaining its immutable prompt archive and refusing any non-WB task-shell
content.

#### REQ: explicit-layout-relocation

`wb worktree relocate` MUST be the sole supported way to move an active
WB-managed checkout between repository-local and configured shared placement.
It MUST plan by default and require `--apply`; `--to=local` selects
`<canonical>/.worktrees/<task>`, while `--to=shared` requires the current
user-configured absolute shared root. Changing layout configuration MUST never
move, hide, or implicitly select an existing checkout.

The selector MUST resolve only registry-and-claim corroborated managed
worktrees across repository-local, configured shared, and historic layouts.
Adopted external worktrees MUST be reported but remain ineligible. Before each
move WB MUST hold the task lock and recheck clean state, branch/head, registry
membership, lock state, source path, and an absent destination. It MUST use the
descriptor-anchored no-replace move plus Git repair and registration
verification, preserving task, branch, immutable claim ID, and Work Log
identity.

Apply MUST append a durable relocation receipt before reporting success. The
receipt binds the immutable claim, source and destination paths, branch, head,
target layout, and timestamp, and makes an exact retry a no-op. A failed or
partial move MUST remain recoverable from the receipt and Git registry; it MUST
never rewrite or delete the original claim. For a batch or any operation that
lasts over ten seconds, progress MUST be emitted to stderr at least every ten
seconds while stdout remains parseable with `--format=json`.

## Interaction with Other Features

[Fleet Status](../fleet-status/README.md) reports canonical repository health.
Worktree Lifecycle owns the separate inventory and cleanup rules for linked
task checkouts.

## Acceptance Criteria

### AC: a-push-is-verified-not-assumed

**Requirements:** worktree-lifecycle#req:verified-publication-after-push, worktree-lifecycle#req:guarded-transient-rebase, worktree-lifecycle#req:point-of-read-canonical-freshness

**Given** a real bare remote and a managed linked worktree
**When** publication is verified after pushing, after committing without
pushing, before the branch has ever been pushed, and while the remote cannot be
reached
**Then** only the pushed case is reported published and exits zero; the
committed-but-unpushed case exits non-zero naming the push that publishes
`HEAD`; the never-pushed case exits non-zero with the upstream-setting push
instead; the unreachable case is reported unverified rather than published; no
verification changes the branch, index, or working tree; and a guard run
without the opt-in performs no remote access at all.

### AC: safe-real-git-lifecycle

**Requirements:** worktree-lifecycle#req:offline-list-default, worktree-lifecycle#req:nonmutating-verified-base, worktree-lifecycle#req:authoritative-write-home, worktree-lifecycle#req:migration-layout-compatibility, worktree-lifecycle#req:legacy-mixed-inventory, worktree-lifecycle#req:validated-identity, worktree-lifecycle#req:point-of-read-canonical-freshness, worktree-lifecycle#req:guarded-transient-rebase, worktree-lifecycle#req:hook-home-stability, worktree-lifecycle#req:hook-executable-stability, worktree-lifecycle#req:attested-canonical-rescue-push, worktree-lifecycle#req:dry-run-default, worktree-lifecycle#req:exact-remote-target-evidence, worktree-lifecycle#req:resumable-interrupted-operation-lock, worktree-lifecycle#req:absorbed-integration-containment-evidence, worktree-lifecycle#req:coordinated-task-safety, worktree-lifecycle#req:trusted-supersession-terminalization, worktree-lifecycle#req:incremental-sweep-progress, worktree-lifecycle#req:recheck-and-compare-delete, worktree-lifecycle#req:remote-opt-in, worktree-lifecycle#req:evidence-gated-remote-retirement, worktree-lifecycle#req:durable-audit, worktree-lifecycle#req:resumable-post-removal-backlog, worktree-lifecycle#req:unregistered-residue-removal, worktree-lifecycle#req:empty-task-namespace-retirement, worktree-lifecycle#req:internal-stage-terminalization, worktree-lifecycle#req:discarded-abort-boundary, worktree-lifecycle#req:recycle-transaction, worktree-lifecycle#req:explicit-layout-relocation

Integration tests using real bare remotes, clones, commits, branches, merges,
linked worktrees, rebases, and refs prove that creation fetches and pins the
remote base without changing a dirty, off-base canonical checkout, its staged
index, unstaged and untracked files, or a nested live linked worktree; new creation uses the
authoritative home even when legacy state exists; legacy and current worktrees
remain guardable, listable, and safely cleanable; direct legacy repository
roots do not recurse into source directories; arbitrary detached work is
rejected while a live rebase is accepted only transiently; prior-release hooks
remain compatible without persisting an ephemeral executable; an exact rescue
branch passes the real managed pre-push hook while any differently named ref
using the same attestation refuses; dry runs preserve state; exact merged heads can be cleaned;
dirty or advanced branches survive; a fleet sweep writes incremental per-repository progress to stderr before its report and leaves stdout parseable as JSON; local and optional remote refs are removed
with comparison guards; a named terminal apply without `--remote` is refused
while the observed origin branch still exists and completes when that branch is
already gone, with ordinary safety reasons reported ahead of the
remote-retirement guidance; interruption after worktree removal is resumed from a
durable exact-ref backlog; a removal Git unregisters but cannot finish deleting
is completed by WB and the task still reaches its branch deletion, while a
removal Git refused still fails; exact empty internal stages are archived while
non-empty ones remain blocking backlog; a terminal task leaves no namespace
directory behind and an operation whose namespace is retired underneath it
refuses instead of writing where nothing can reach; and apply writes durable evidence. Hosted PR metadata
MAY be supplied by a deterministic test double.

### AC: mixed-layout-relocation-preserves-active-identity

**Requirements:** worktree-lifecycle#req:migration-layout-compatibility, worktree-lifecycle#req:legacy-mixed-inventory, worktree-lifecycle#req:explicit-layout-relocation

**Given** managed local, configured shared, and historic registered worktrees
with active immutable Work Log claims
**When** an operator plans and applies a relocation to the other supported
layout, then retries the same invocation
**Then** the dry run leaves every checkout in place; apply moves only selected,
clean, unlocked managed worktrees with Git registration repaired; task, branch,
head, and immutable claim identity remain unchanged; the original claim stays
readable through the new path via an append-only relocation receipt; retry is a
no-op with the same receipt; and adopted external, dirty, locked, mismatched,
or destination-colliding worktrees remain at their source with an explicit
reason. JSON stdout remains one document while batch progress is sent to
stderr.

## Open Questions

### Decision record: issue #173 dirty discard

Approved: a discarded abort may remove dirty tracked and untracked bytes only
after a bounded, hook-independent private Work Log capture is durably written
and represented by an immutable terminal/outbox receipt. The existing local
mode-0700/0600 Work Log protections and SHA-256 digest are the applicable
privacy and integrity primitives; WB must not invent or expose encryption or
signing secrets. A changed capture, oversize capture, or failed retention
write remains fail-closed and leaves Git state in place.

- Should a future cleanup mode archive reports after a retention period?
- **Resolved 2026-08-29 in issue #97:** supersession is an explicit,
  named-task-only `wb worktree cleanup --superseded-by <receipt.json>` path.
  WB accepts only a trusted-reviewer receipt binding exact source and target
  heads, replacement PRs or commits, a complete reviewed residual inventory,
  and the approving actor/receipt ID. It rechecks the receipt at dry-run and
  apply boundaries, refuses any missing or unreviewed classification or ref
  drift, embeds the receipt in the terminal Work Log, and never exposes the
  disposition to `--all-merged` sweeps.

---
*This document follows the https://specscore.md/feature-specification*
