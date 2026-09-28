# WB `internal/worktrees` coverage and refactoring campaign

**Status:** reviewed and locally green merge candidate
**Branch:** `wb-refactor`
**Compared with:** local `origin/main` merge-base `becfcbc36e0d27c26670c7ab951c4ba0a8e6d5ef`
**Candidate head:** `7159b133`
**Execution:** Codex `gpt-6-sol`, high reasoning, local Mac host

## Scope and outcome

This branch concentrated on `internal/worktrees`, the largest package-level coverage hotspot. The work changed the structure before attempting a broad test-writing sweep: it consolidated repeated Work Log, filesystem, lifecycle, session-receive, and test-fixture logic; removed unreachable supersession paths; added focused tests around the resulting shared boundaries; and fixed one relocation receipt-ordering defect found during the refactor.

Before this report, the implementation branch contains 32 commits and changes 76 implementation, test, and inventory files. Relative to the local `origin/main` merge-base, production Go code is **916 lines added and 1,358 deleted** (net **442 fewer production lines**). Tests and the unit-tier inventory are **1,859 lines added and 321 deleted** (net **1,538 more lines**). That implementation diff is 2,775 additions and 1,679 deletions.

The latest comparable full-package profiles show a real reduction in the remaining obligation:

| Full `internal/worktrees` profile | Covered | Total | Uncovered | Coverage |
|---|---:|---:|---:|---:|
| Older complete profiles | 16,633–16,635 | 20,756 | 4,121–4,123 | 80.1359–80.1455% |
| Committed checkpoint at `d8a8ad94` | 16,558 | 20,195 | 3,637 | 81.9906% |
| Final candidate at `7159b133` | 16,629 | 20,174 | 3,545 | 82.4279% |

The branch reduced the uncovered count by **576–578 statements**, or about **14.0% of the older uncovered gap**. Against the immediately preceding complete checkpoint it added 71 covered statements, removed 21 statements, cut the uncovered count by 92, and gained 0.4373 percentage points. Against the older baseline, the covered-statement count is slightly lower because the refactor deleted exercised code as well as uncovered code. The result combines architecture simplification with focused coverage gain.

The older artifacts differ by two covered statements: `/private/tmp/wb-worktrees-combined-merged.cov` records 16,633/20,756 and `/private/tmp/wb-worktrees-final-merged.cov` records 16,635/20,756. Their source and test selection were not retained well enough to attribute the variation, so this report gives a range. The `d8a8ad94` row is directly reproducible from `/private/tmp/wb-refactor-current-full.cov`.

Commit `e72e6b5f` reduced the production denominator by another 19 statements and exercised eight formerly 0%-covered functions to 100%. The final Retire batch at `7159b133` exercised 45 statements missed by the prior complete profile and removed two unreachable statements. Its only production function change, `retireCaptureTree`, reached 100% focused coverage. The exact final package profile is `/private/tmp/wb-refactor-final-full.cov`.

## Architecture and deduplication changes

| Theme | Commits | Result |
|---|---:|---|
| Work Log and claim lifecycle | 13 | Shared rendering sections, target publication, immutable claim and record readers, claim identity derivation, projection selection, cleanup-claim acquisition, repair/exclusion formatting, temporary writes, secure random tokens, and Git corroboration. This replaced parallel implementations with common policy boundaries. |
| Secure filesystem and lifecycle primitives | 8 | Shared no-follow directory openers, canonical-root validation, secure Git execution tails, lifecycle report writing, descriptor cleanup, and owned directory acquisition across cleanup and session receive. Security-sensitive descriptor ownership remains explicit at callers. |
| Session, transfer, and policy state machines | 5 | Split session receive into owned phases, shared parked-session preparation and cleanup, consolidated quarantine/path parsing, and shared dependency-manifest policy. These changes made state transitions smaller and easier to exercise. |
| Test fixtures and test runtime | 3 | Shared lifecycle, pull-request, and repository-transfer fixture stages; parallelized one renderer regression. This removed repeated real-Git setup without changing the behavior under test. |
| Correctness and dead paths | 3 | Relocation now verifies all worktrees before publishing immutable receipts, preventing later drift from causing rollback after an earlier receipt. Unreachable supersession paths and a structurally impossible `filepath.Rel` error branch were removed; the archive inspector gained one narrow read seam for deterministic unit tests. |

Representative completed changes include:

- `session_receive.go` now expresses the receive workflow as owned phases, with state-focused tests around the phase boundary.
- Work Log readers and claim derivation now have one implementation instead of repeated decode, identity, and selection paths across lifecycle commands.
- Cleanup, canonical, adopted, relocated, and staged worktree paths reuse descriptor-anchored directory openers while preserving their distinct ownership rules.
- Lifecycle report writers and partial descriptor cleanup use common primitives instead of duplicating mkdir, serialization, temporary-file, rename, and close sequences.
- Repository relocation validates the whole move before publishing any immutable receipt and rechecks each entry during publication.
- The final zero-function batch removed a branch-only supersession wrapper that had no caller and simplified functions whose error result could never be non-nil.
- The Retire batch exercised 21 missing or partially covered functions in one run-sized unit, added remote-ref, durable-intent, archive, claim, capture, and deletion safety cases, and brought the refactored tree-capture function to 100%.

## Coverage evidence

The final package-wide measurement at exact source head `7159b133` is:

```text
16,629 covered / 20,174 total = 82.427877%
3,545 uncovered
elapsed: 977.094 seconds
```

It was generated with:

```sh
GOCACHE=/private/tmp/wb-final-gocache go test ./internal/worktrees -parallel=16 -timeout=30m -coverprofile=/private/tmp/wb-refactor-final-full.cov
```

The preceding successful run at `d8a8ad94` was:

```text
16,558 covered / 20,195 total = 81.990592%
3,637 uncovered
elapsed: 1,049.068 seconds
```

The recorded command was:

```sh
GOCACHE=/private/tmp/wb-checkpoint-gocache go test ./internal/worktrees -parallel=16 -timeout=30m -coverprofile=/private/tmp/wb-refactor-current-full.cov
```

The profile itself does not encode a Git SHA. Its association with `d8a8ad94` comes from the campaign receipt and must be re-established from the exact checked-out SHA for the final merge-candidate profile.

The later `e72e6b5f` focused run is useful only as target evidence:

```text
focused tests: PASS in 9.324 seconds
focused profile: 4,077 / 20,176 = 20.2072%
```

Its low aggregate percentage is expected because the run selected nine tests. It must not be compared with a full-package profile. The target receipt showed 100% for the eight previously uncovered functions exercised by that batch:

| Target function or boundary | Focused outcome |
|---|---|
| `underPath` | exercised across empty, exact, nested, sibling-prefix, and parent cases |
| `(*ParkedLocalCustody).ResolvedWorktreeDirs` | nil, mapping, and defensive-copy behavior exercised |
| `SetInvokedCommand` | publish and restore behavior exercised |
| `ParkedSessionWorkLogReference` | exercised through the session checkpoint fixture |
| non-Linux `BusyProcessReason` | unsupported-platform behavior exercised |
| `WithParkedLocalResumeCustody` | public wrapper exercised |
| `WithParkedLocalResumeCustodyForAttempt` | public wrapper exercised |
| `inspectRetiredArchiveRepository` | exact API request, private/public payload, malformed payload, and read error exercised |

The largest remaining misses in the final `7159b133` full profile are:

| Function | Missed / total statements |
|---|---:|
| `Create` | 57 / 302 |
| `Retire` | 54 / 225 |
| `RelocateRepository` | 43 / 215 |
| `reconcileClaimBranch` | 39 / 136 |
| `applyCleanupTask` | 31 / 192 |
| `retirePublishArchive` | 29 / 97 |
| `retireEmptyUnscopedLocalStagesWithHooks` | 31 / 95 |
| `rollbackRenamePlan` | 30 / 64 |
| `PrepareExternalSessionWorkLog` | 29 / 117 |
| `recoverInterruptedSessionReceivePublication` | 29 / 80 |
| `applyRetiredStageRecovery` | 29 / 55 |
| `addWorktreeAtSecureDestination` | 28 / 166 |
| `validateDependencyDeltas` | 28 / 79 |

These counts were refreshed from the final profile. `retirePublishArchive` is reported at its current source line by `go tool cover`; the temporary ranking index retained the pre-edit line by one line, without affecting its statement counts.

The final Retire focused profile at `7159b133` is target evidence, not a package-wide percentage:

```text
focused tests: PASS in 36.990 seconds
old complete profile union with focused profile: 45 formerly missed statements exercised
dead statements removed: 2
retireCaptureTree: 100.0%
```

### Refactored-function coverage rule

The founder introduced the explicit rule during the final Retire batch: every production function refactored in a coverage batch must reach 100% before that batch commits. The final batch meets it: `retireCaptureTree` is the only production function changed and is 100% in the post-edit focused profile. Earlier commits predate the rule and were reviewed and tested per batch, but this campaign did not retain a complete changed-function coverage inventory proving every earlier refactored function at 100%. The next campaign should make that inventory a per-commit artifact instead of trying to reconstruct it at the end.

## Validation and review evidence

| Check | Receipt | Meaning |
|---|---|---|
| Previous full `internal/worktrees` package | Passed at `d8a8ad94` in 1,049.068 seconds | Earlier package-level behavioral and coverage checkpoint used for comparison |
| `e72e6b5f` focused batch | Passed in 9.324 seconds | The selected zero-function tests are green; this is not a package-wide verdict |
| `go vet ./internal/worktrees` for `e72e6b5f` | Passed; log is empty | No vet findings in that committed batch |
| Unit-tier/parallel quality guard for `e72e6b5f` | Passed: `internal/quality` in 0.472 seconds | That batch's tests satisfy the repository's test-tier guard |
| Final Retire focused batch | Passed in 36.990 seconds | Twenty-one partial functions were handled together; 45 prior misses were exercised |
| Final Retire vet and quality guards | Passed | `go vet ./internal/worktrees`, unit-tier inventory, and parallel guard are green |
| Final Retire adversarial review | 0 blockers, 0 majors, 0 minors; land=yes | Separate Sol High review checked safety behavior, assertions, duplication, and the process ledger |
| Final full `internal/worktrees` package | Passed in 977.094 seconds; 16,629/20,174 | Exact candidate package receipt; 3,545 statements remain uncovered |
| Branch diff integrity | `git diff --check` passed | No whitespace errors in the committed diff |
| Object integrity | `git fsck --no-dangling HEAD` passed | Local commit graph and referenced objects are readable |
| Combined branch adversarial review | 0 blockers, 0 majors, 0 minors; land=yes | Separate Sol High reviewer inspected `becfcbc3..7159b133`, prioritizing descriptor ownership, claims/receipts, session state, cleanup, Git helpers, relocation ordering, and assertions |
| Stale-receipt recovery focused tests | Passed in 30.807 seconds | When a missing candidate worktree has a recorded SHA, it is accepted only if that SHA is an ancestor of the freshly fetched target; both extracted helpers are 100% covered |
| Stale-receipt recovery vet and quality guards | Passed | `go vet ./internal/orchestrate`, unit-tier inventory, parallel-test guard, gofmt, and `git diff --check` are green |
| Landing changed-line batch | Passed in 48.477 seconds | Covered 40 flagged statements across a 21-function batch and removed two invariant-dead checks; all eight edited production functions are 100% in the focused profile |
| Landing batch quality guards | Passed | Unit-tier and parallel-test guards, vet, package lint, gofmt, and `git diff --check` are green; unit-tier pending stays flat at 4,907 versus `origin/main` |

A separate package attempt in `/private/tmp/wb-refactor-worktrees-checkpoint.log` failed after 900.314 seconds while waiting on a child process in a supersession test. A later full checkpoint completed successfully, but the failed attempt is material process evidence: the full package loop is too expensive to use after every small edit, and real-process tests remain a runtime and flake risk.

## Process lessons

### What worked

1. **Architecture-first work reduced the test obligation.** The complete-profile denominator fell by 561 statements and the uncovered count fell by 484–486 before the final focused batch. Consolidating policy and I/O sequences was more productive than writing nearly identical tests for every duplicate branch.
2. **Focused batches made verification cheap.** The final 21-function batch completed in 36.990 seconds, compared with 1,049.068 seconds for the prior successful full package run. Running focused tests, vet, and the quality guard at a batch boundary gave useful feedback without paying the full-package cost repeatedly.
3. **Narrow seams were effective when they represented a real boundary.** Passing the existing GitHub read function into the retired-archive inspector exposed unavailable and malformed-response behavior without adding a broad mock framework or altering production behavior.
4. **Shared fixtures reduced both test code and runtime setup.** Reusing lifecycle, pull-request, parked-session, and repository-transfer setup avoided another layer of one-off coverage fixtures.
5. **Coverage work found a correctness bug.** Reviewing relocation as a state machine exposed receipt publication before whole-operation verification. The fix improves behavior independently of its coverage effect.
6. **Static reachability review paid off.** The supersession wrapper and impossible error channels could be removed instead of receiving artificial seams and tests.

### What did not work well

1. **Frequent full package runs had poor return on time and tokens.** One successful checkpoint took about 17.5 minutes and one attempt failed after 15 minutes. Repeating this loop per function or per small commit is not economical.
2. **Small landings and worktree management dominated the work.** Repeated branch synchronization, review, CI, landing, and cleanup made tiny coverage gains expensive. The founder therefore directed this tranche to accumulate reviewed local commits on one branch. This removed coordination steps, but the campaign did not run a controlled time/token comparison proving that one owner is inherently cheaper.
3. **Blind duplicate scanning reached diminishing returns.** Low-threshold `dupl` results were mostly struct literals, sort comparators, formatting, and short security-sensitive sequences. Abstracting them would save only a few statements while obscuring policy or descriptor ownership.
4. **One-line uncovered blocks are a poor planning unit.** They are often error exits spread across many large orchestration functions. Planning by behavioral function and external-call path provides enough context to cover several branches with one table or fail-call sweep.
5. **Raw percentage can hide the source of progress.** The checkpoint rose 1.85 percentage points while covered statements fell by 75. Future reports must always show covered, total, and uncovered statements together.
6. **The checked-in plan no longer describes the active tranche workflow.** `spec/plans/coverage-to-100/README.md` still records six lanes, an integration branch, lane review/coverage, and a standing PR. Later founder instructions moved this `internal/worktrees` tranche to one local branch, one package owner, larger batches, and one merge checkpoint. The final report should update or annotate the plan so the next coordinator does not restore the superseded workflow. The runtime evidence supports less frequent full-package testing; it does not by itself prove an optimal agent count.
7. **A binary ignore rule hid an accidental nested clone.** Local inspection on 2026-09-28 found that the root `/wb` rule, intended for the built executable, also ignored a 39 MB clone at `wb/wb`. Its reflog recorded a plain clone on 2026-09-04; status, refs, and object checks found no unique local work. It was deleted and the canonical checkout was reverified. Repository hygiene needs a cheap nested-`.git` detector because ordinary status did not surface it.
8. **The changed-line gate ran too late.** The reviewed package profile still left changed statements uncovered, so the first landing attempts paid the full remote coverage runtime before reporting them. Run the exact `wb coverage --changed --target <base>` policy locally once at the merge checkpoint, after the focused batches and before publishing a PR. This preserves the cheap inner loop while avoiding repeated remote discovery cycles.

## Risks and limits

- This is a package campaign, not a repository-wide 100% result. No current repo-wide coverage percentage was measured for this branch.
- The latest full `internal/worktrees` package profile is from exact source head `7159b133`. A later landing-gate repair changes that package and has focused coverage for all flagged statements and 100% for every edited function, but no newer local package-wide percentage was measured. The `internal/orchestrate` recovery change has its own focused test, coverage, vet, quality-guard, and review receipts.
- The final Retire batch is committed and independently approved. Its refactored production function is 100%; earlier commits predate the explicit per-refactor 100% rule and do not have a retained changed-function inventory.
- The older baseline is represented by two complete artifacts that differ by two covered statements, so comparisons correctly use a range rather than a single reproducible number.
- The report compares against the locally available `origin/main`. Refresh the remote and re-evaluate the merge base before final review and merge.
- Refactors in `internal/worktrees` operate around claims, immutable receipts, no-follow filesystem access, cleanup, and repository relocation. The final full package test and combined source review passed, but the package still has 3,545 uncovered statements.
- Per-batch focused coverage receipts live in transient campaign logs; this report preserves their conclusions rather than the artifacts themselves.
- The campaign still contains many real-Git/process tests. They provide useful contract confidence, but their runtime and failure modes make them unsuitable as the inner coverage loop.

## Prioritized next steps

1. **Continue from the new full profile with the founder-directed tranche loop.** Give one package owner an ordered list of uncovered functions. Select at least 20 missing or partial functions; inspect architecture and reachability; refactor true duplication; bring every refactored function to 100%; write the complete test batch without intermediate executions; then run one focused batch, fix, and commit. Use a full package run only at a merge checkpoint.
2. **Prioritize by statement gain and seam reuse.** Start with the largest remaining functions and clusters that share fixtures or I/O boundaries. `Create`, `Retire`, `RelocateRepository`, claim reconciliation, cleanup application, rename rollback, and recovery paths were the largest clusters at the last complete checkpoint. Split only where phases own clear state and can be exercised independently.
3. **Standardize deterministic failure sweeps.** Extend existing narrow command, Git, filesystem-write, and GitHub-read ports with fail-call-N tables where several error returns share one happy-path setup. Keep real-Git tests as a smaller contract tier and use in-process fakes for the coverage tier.
4. **Record changed-function coverage per commit.** Generate the inventory when each refactor is committed, so the 100% rule has a durable receipt and does not need reconstruction at campaign end.
5. **Add nested-repository hygiene detection.** Teach an existing WB audit or guard to report ignored directories containing `.git`, with an explicit allowlist for registered linked worktree roots. This would have exposed the accidental `wb/wb` clone without changing the executable ignore rule.
6. **Reconcile the checked-in plan with the active workflow.** Record that the large single-package tranche used one branch, one owner, a strict 20-function minimum, and infrequent full-package runs. Preserve multi-agent pattern sweeps only where files do not overlap and the measured gain justifies coordination cost.
7. **Enforce 100% only after the worklist is empty.** Once every package profile is verified at 100%, enable the hard regression gate described in `spec/plans/coverage-to-100/README.md`. The gate should report function and source-block regressions and use the same command locally and in CI.

## Landing-recovery tooling change

The accumulated branch exposed a WB lifecycle gap before landing: an old unpublished `prepare/conflict` receipt still owned the `sneat-dev/wb` `main` lane after both receipted worktrees and their local branches were gone, even though its recorded candidate commit was already an ancestor of current `main`. Existing recovery refused solely because the missing candidate had a non-empty recorded SHA.

The branch now permits the audited `acknowledge-absorbed-conflict` path for this case only after fetching the target and proving that exact candidate SHA is its ancestor. Existing checks still require the source worktrees to be absent, the candidate branch to remain unpublished, and every source to be content-absorbed. When the candidate worktree is missing, its local branch and every source local branch must also be absent. An unavailable or uncontained candidate SHA refuses closed. The two refactored helpers reached 100% focused coverage.

## Landing record

This report will be committed as part of the candidate, so the immutable WB landing receipt, exact remote `main` SHA, CI verdict, and source cleanup necessarily follow it. They belong in the coordinating thread's final delivery receipt. The next campaign should add repository-wide coverage from main CI when that artifact becomes available.
