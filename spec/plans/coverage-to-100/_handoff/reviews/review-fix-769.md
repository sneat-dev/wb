Review-Of-Head (NOT approved): 35c3f59d7cfdb9489b6462565311d2f49efa09c5

# Verdict: REQUEST CHANGES: cov-fix-769 (fix-forward for standing PR #769)

Adversarial review of branch cov-fix-769 at 35c3f59d, diffed against origin/cov/integration 1cc1dc19. It has 2 commits: 008b74d2 (cmd/wb) and 35c3f59d (internal/orchestrate). Reviewer: Opus 5.5, Claude Code runtime, on this Linux VM.

I reviewed in my own detached worktree. Every probe ran under `timeout 300` with `GOMAXPROCS=2 GOFLAGS=-p=2`, targeted `-run`, no `./...` and no `-race`.

The ciwait half is correct and deterministic. The cmd/wb half evades the task-24 detector (B1), so this cannot be approved as is.

## Key question: is (1) evading the task-24 detector? Yes

### How (cmd/wb/ci_audit_target_test.go)

The test's own doc comment says it builds the bare remote "through runCommand plus the same gc.auto/maintenance.auto/receive.autogc config calls testenv.InitBareRemoteForTest itself makes, rather than calling that named helper directly, so this file adds no exec, git-helper or PATH pattern of its own and the pending list's counts stay exact."

The mechanism is `runCommand(t, dir, "git", …)`:
- It is a generic test-local process wrapper defined in `cmd/wb/worktree_marker_test.go:48` (`exec.Command(name, arguments...)`). It is not in `UnitTierGitHelperNames`, so its call sites are invisible to the detector.
- The fixture helpers `newRatchetFixtureRepo` (coverage_ratchet_test.go:29-31) and `repo.commitAll` (:49-51) run git through the same wrapper.

What the test actually runs:
- I ran it with `GIT_TRACE` pointed at a file: 31 git processes (init ×2, config ×5, add, commit, remote, push/receive-pack, checkout, fetch ×2, show, rev-parse ×4, …).
- The detector finds 0 matches in the file, it has no `unit_tier.pending` entry, and `TestUnitTierPendingDoesNotRegress` passes.
- So a new default-tier file runs real git with zero accounting. That is exactly the hole the exact-count guard exists to close. Swapping the named helper for inline calls to an unlisted wrapper was done specifically to keep the count flat.

### The same shape, milder, in (2) (internal/orchestrate/ciwait_deadline_unix_test.go)

- `installFakeGH` wraps `testenv.WriteExecutableFile` and `t.Setenv("PATH", …)`. The comment says so "this file's real-process footprint on … unit_tier.pending stays the two matches it had".
- The file now has 3 fake-`gh` tests but still counts 2.
- It is already on the pending list (task-17), and fake-`gh` tests are the only route to these lines before task-8's fakes exist. So I rate this a note (N1), not a blocker. The honest version is to count `installFakeGH` call sites (see N2).

## Blocking

### B1: cover ci.go:314 honestly (cmd/wb/ci_audit_target_test.go)

Option (a), pending with its true count, is not possible as is. The exact-count guard compares the entry with what the detector sees. The detector sees 0, so any nonzero entry fails as a stale entry. Option (a) therefore needs the detector to learn `runCommand` first (N2).

Recommended fix, option (b), is fake-based and needs no git at all:
- Add a package-level seam in cmd/wb/ci.go: `var ciAuditCompareAgainstTarget = ciaudit.CompareAgainstTarget` (a declaration, not a statement), and call it at line 314.
- Write a serial test that swaps the seam for a fake returning one `unit-tier-pending-total-rose` finding, runs `run([]string{"ci", "audit", dir, "--target", "main", "--strict"}, …)` on a `t.TempDir()` containing a single `app.go`, and asserts exit 1 plus the finding in stdout. That covers 314-315 and 318-319.
- A second fake returning an error covers the `return 1, err` at 316-317 as well. It is uncovered today (`ci.go:316.5,317.1 … 0`).
- Delete `TestCIAuditWithTargetReportsAPendingTotalRise`, or move it to a `//go:build e2e` file as `TestContractCIAuditWithTargetRealGit`, where real git belongs. ciaudit's `TestContractCompareAgainstTargetRealGit` already proves the real-git path end to end, so it is optional.
- The seam is a one-line production change in cmd/wb. It keeps cmd/wb's statement count unchanged and fits decision 18's direction: keep `cmd/wb` thin, with logic behind interfaces.

Option (a) remains legitimate if preferred. #769's base, main, has no list yet, so the creating-PR exemption covers the total. But it must go through N2, not a hand-written entry.

## Verified

- **ci.go:314 is covered by the current test.** Profile from `-run '^TestCIAuditWithTargetReportsAPendingTotalRise$'`: `ci.go:313.3,313.19 1 1`, `314.4,315.18 2 1`, `318.4,319.52 2 1`. `316.5,317.1` (the error return) is 0. Coverage is fine; how it is achieved is B1.
- **Both ciwait tests are deterministic and exact.** With `-count=20 -covermode=count`, both pass:

  | Block | Hits | Path |
  |---|---|---|
  | `ciwait.go:132.5,132.84` | 20 | pullRequestIdentity (the fake hangs on its first `/pulls` call, count asserted exactly `"1"`) |
  | `133.6,134.1` | 20 | same |
  | `146.5,146.84` | 20 | targetHead (`/pulls` succeeds with matching head and base, `git/ref/heads/main` hangs, count asserted exactly `"2"`) |
  | `147.6,148.1` | 20 | same |

  - The deadline is the FIFO-triggered `triggerContext` with a 5-minute slice, so no wall clock is involved.
  - Any retry or other path changes the exact count and fails loudly.
  - Both reuse `triggerContext`/`newTriggerContext` from the unix-only file.
- **Windows vet is clean.** `GOOS=windows go vet ./internal/orchestrate ./cmd/wb` exits 0.
- **Baseline.** `paralleltest_baseline.txt` gains exactly the 2 new ciwait tests, with the accurate reason "calls t.Setenv". `TestParallelBaselineDoesNotRegress`, `TestUnitTierPendingDoesNotRegress` and `TestUnitTierAllowListEntriesAreGenuineHelperProcessReexec` pass at HEAD. The new cmd/wb test is serial because `cwCovCaptureStdout` swaps `os.Stdout`, and it needs no baseline entry, since the guard passes.

## Notes

- **N1:** `installFakeGH` hides per-test fake-`gh` installs from the count (see above). Acceptable for this fix-forward only if N2's detector change follows. Otherwise the file's entry understates 3 tests' worth of process starts as 2.
- **N2, the detector gap this exposes:** generic test-local process wrappers are not counted.
  - `runCommand` execs any program, and the unit tier bans every process start, not just git. So it belongs in `UnitTierGitHelperNames` (better renamed to a process-helper list), as do `installFakeGH`-style wrappers.
  - It already hides a pre-existing file: `cmd/wb/run_changed_test.go` makes 8 `runCommand(t, …, "git", …)` calls and is on neither list. (My round-1 task-24 scan targeted method-form and qualified helpers and missed this shape.)
  - A cheap backstop: flag any call with a string-literal `"git"` or `"gh"` argument. Only string literals inside test fixtures, such as `internal/quality/unittier_test.go`'s sample sources, would false-positive, and those are not call arguments.
  - Adding `runCommand` will re-count several cmd/wb entries (coverage_ratchet_test.go, run_changed_test.go, worktree_marker_test.go callers, …). Regenerate them in the same change.
