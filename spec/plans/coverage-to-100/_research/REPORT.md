# wb test coverage: why agents struggled and a plan to reach 100%

**Date:** 2026-09-23 · **Repository:** sneat-dev/wb (origin/main a01e2e3c; nightly coverage profile from run 35836520378 at 295e503f) · **Reference:** specscore/specscore-cli (origin/main 2faf7b8)

**[M]** marks a measured fact and **[I]** an inference or estimate. The coordinator re-checked three claims: the 88→87 floor drop in `d6f48b06` is on main, #646 is still open, and the run and SHA of the nightly artifact are as stated.

## Summary

- **[M]** wb is at **87.98%**: 77,402 of 87,979 statements covered, **10,577 uncovered**.
- **[M]** **85% of the gap is in 3 packages**: `internal/worktrees` 4,224, `cmd/wb` 2,837 and `internal/orchestrate` 1,934. The other 94 packages hold 1,582. 34 packages are already at 100%.
- **[M]** Coverage is **falling**: 88.53% on 09-18, 87.98% on 09-23. In those 5 days 9,467 statements landed at about 83% coverage.
- **[M]** When the headroom ran out, 12 PR CI runs failed at 87.89–88.00% against a floor of 88. PR #677 then added 9 "cover" commits and lowered the floor to 87 (`d6f48b06`). Backstage lesson L10 says a floor is raised with real tests and never lowered to fit.
- **[M]** specscore-cli reached 100% in 2 days at 20k non-test lines, then **kept 100% on every change** while growing 4.2× to 83.7k. wb has 198.5k non-test lines and is trying to catch up afterwards.
- **[M]** `wb run --`, the officially required way to run tests (agentguard enforces it), **deadlocks** `cmd/wb`'s own tests on this VM.

## Why agents struggled, ranked

1. **The gate checks the whole-repo total instead of each change [M].**
   - A single 87–88% repo-wide floor has no per-package ratchet and no rule for new code.
   - Under-covered code lands until headroom runs out, then an unrelated PR fails by hundredths of a percent. Each retry costs a 7–9-minute CI run, which led to filler commits and a lowered floor in #677.
   - The fix is already described in the Draft idea `quality-diff-and-thresholds` and in issue #570, but neither was built.
   - `wb ci audit --target`, which should catch floor drops, is not wired into CI.
2. **The test loop takes 10–27 minutes, runs mostly one test at a time, and fails for environmental reasons [M].**
   - Only 611 of 7,665 tests run in parallel, and 3 of 912 in `internal/worktrees`.
   - A local run took 26 min 43 s at 69% CPU. `cmd/wb`, `orchestrate` and `worktrees` used 93% of summed package time.
   - **The deadlock:** `TestRunCommandAdmitsCPUHeavyWorkBelowFloor` joined the machine-wide CPU queue behind the outer `wb run` that was running it.
   - **False failures on the VM:** umask 002 breaks 38 `lifecyclehooks` tests, 8 `worktrees` tests read the real `~/.wb/worktrees` (#587), and there are further failures in `migrate` and `cmd/wb`.
   - Flaky tests #504, #505 and #539 are still open.
   - #646 (no real sleeps, parallel by default) has been open since 09-18 with its coverage shards timing out.
   - agentguard forbids plain `go test`, which pushed the agent working on #646 to create unmanaged worktrees.
3. **The code that holds the gap cannot have failures injected [M].**
   - 45% of uncovered statements (about 4,800) are error branches after temp-file write/sync/close, `unix.Openat`/`Fstat`, JSON marshal, `rand.Read`, git and `runCommand`.
   - `worktrees` has 1,083 functions, only 3 replaceable function variables and 0 interfaces. specscore's `internal/cli` has 207 such seams for 779 functions.
   - Very large functions: `LandWorktreeMerge` 604 statements, `Cleanup` 450, `PrepareWorktreeMerge` 381.
4. **The measurement hides some real coverage [M].**
   - Coverage is counted per package without `-coverpkg`, so 17 exported functions show 0% even though other packages call them.
   - Production code strips GOCOVERDIR from the secure git helpers (`lifecycle.go:5520`), so their coverage is lost; `RunSecureRenameGitHelper` has 56 of 59 statements uncovered.
   - The coverage tool itself had bugs along the way (#575, the #578 sharding failure).
5. **Coverage was pushed in huge one-off PRs [M].**
   - #554 was +90,783 lines (72.46% → 84.90%). #557 added +20,718 lines of test files that #554 had left untracked.
   - 286 test files (about 113k lines, 40% of all test code) are named after the campaign (`zz_cov_*`, `dqcov`, `tailcov`) rather than the behaviour they test.
   - Assertion-free tests are rare (0.2%).
   - AGENTS.md has one sentence on coverage and no test-design guidance.
   - Context: wb adds about 1,600 statements a day.

## specscore-cli comparison

- **How it holds 100% [M]:**
  - `scripts/coverage-gate.sh` sets `THRESHOLD=100`, runs uncached, and is shared by CI and the opt-in pre-push hook.
  - No exclusions and no `-coverpkg`.
  - `main` is `run(os.Args, cli.Run, cli.Fatal)`, and `cli.Run` builds a fresh command tree per call.
  - Package-level `xxxFn = pkg.Func` variables (126) are swapped in tests via `t.Cleanup`.
  - Dead code was deleted rather than marked.
  - Tests are mostly serial but packages are small: the slowest takes 43 s and the whole CI job 3 min 37 s.
- **What carries over directly:** a shared gate script, a per-call command tree to replace `cmd/wb`'s globals, and deleting provably dead code.
- **What needs adapting:** a pre-push gate only on changed packages, because the full suite is too slow. Replaceable function variables work only in small leaf packages, because they block parallel tests; in `worktrees` and `orchestrate`, inject dependencies through the existing option structs.
- **What does not apply:** daemons, fd-inheriting secure helpers, multi-repo state. Do not copy specscore's `--no-verify` hint (`rule:hooks-are-never-bypassed`).

## Plan

- **Stage 0 (1 lane, about 1 day): a ratchet.**
  - `wb coverage` fails when any package's *uncovered count* rises, against a committed baseline.
  - **100% coverage of every statement a PR adds or changes.**
  - Report counts instead of rounded percentages, and keep `--minimum=88` as a backstop.
  - Wire `wb ci audit --target --strict` into CI, and publish a per-package table nightly.
  - The ratchet and the later hard gate both live in `wb coverage`, so CI and local runs share one implementation.
- **Stage 1 (3–4 lanes): hermetic, fast tests.**
  - Land #646.
  - Set umask 022 and a private HOME/WB_HOME in the shared test setup, and give tests their own CPU queue so the `wb run` deadlock goes away.
  - Fix #587, #620, #504, #505, #539 and #582.
  - Move `cmd/wb`'s global state into a per-invocation context, and inject env/cwd.
  - Build #570 as `wb coverage --changed`.
  - Target: `cmd/wb`, `worktrees` and `orchestrate` each ≤3 min on the VM, and the CI coverage job ≤6 min.
- **Stage 2 (6 refactor PRs, separate from test PRs, each with adversarial review):**

  | refactor | statements it makes reachable [I] |
  | --- | --- |
  | 2a: git/exec runner with a fake that can fail on demand | ~1,500–2,000 |
  | 2b: one safe-file-write primitive package | ~1,000–1,300 |
  | 2c: clock/sleep seam | ~180 |
  | 2d: split each secure helper into a thin fd shim plus a testable core | ~300 |
  | 2e: split the large functions into steps, characterization tests first | makes ~1,500 recovery branches cheap |
  | 2f: failing-writer test helper | ~160 |

- **Stage 3 (11 waves, at most 2 Go lanes at once):**

  | wave | scope | uncovered |
  | --- | --- | --- |
  | W1 | long tail A (8 packages) | 816 |
  | W2 | long tail B (55 packages) | 766 |
  | W3–W5 | `cmd/wb`, by command family | ~730 / 900 / 1,200 |
  | W6–W7 | `orchestrate` | ~1,000 / 930 |
  | W8–W11 | `worktrees` | ~800 / 1,100 / 1,150 / 1,170 |

  - Each wave ends at 100%, or with a PR that designs the leftover code out; exclusions are never used.
  - Tests sit beside the code, are named after behaviour, and are safe to run in parallel.
  - Package wall time grows less than 10%, and an adversarial reviewer reads each wave's diff.
- **Stage 4:** once every package is at 100%, switch to specscore's hard 100% gate with no exclusions.
- **Brief guardrails:**
  - The target is a package list, never "raise the total".
  - Production changes are allowed only for named seams.
  - Every test asserts an outcome.
  - Deleting behaviour, `coverage:ignore`, build-tag hiding and lowering a floor are all forbidden; if a target can't be met, stop and report.
  - Test PRs stay under about 3k lines.
  - The supervisor re-measures coverage itself.
- **Generated and platform code:**
  - Generated proto/connect code (579 statements) is already 100% covered.
  - Darwin/Windows files stay outside Linux coverage, the same as specscore.
- **Estimates [I]:** about 21 lanes over 3–5 calendar weeks; about 55–85k new test lines; about 100–200M tokens (a rough guess, since no measured token data per lane exists). **Main risk:** refactoring the landing and cleanup code that agents rely on daily.

## Founder decisions

1. Should every wb PR have to cover 100% of the statements it adds or changes, with each package's uncovered count only allowed to go down, instead of one 87% repo-wide total? *Recommend: yes.*
2. Should #646 (no real sleeps, parallel tests) land before any coverage wave starts? *Recommend: yes.*
3. May the testability refactors land as their own reviewed PRs before the test waves? *Recommend: yes, kept separate from test-writing lanes.*
4. Should a test in one package count as covering code it runs in another package (`-coverpkg`)? *Recommend: no for the gate; run it once as a diagnostic to find truly dead code.*
5. Once every package reaches 100%, should wb switch to specscore's hard "100% or fail" rule? *Recommend: yes.*

## Evidence and reproduction

- **PRs:** #554, #557, #559, #646, #677, #571.
- **Issues:** #623, #570, #582, #587, #620, #504, #505, #539, #621.
- **Floor-only CI failures:** 35626416785, 35756625554, 35782829695–35800668724.
- **Floor history:** 58 (08-28), 72, 84, 88 (09-17), 87 (09-23).
- **Lessons:** `l10-coverage-floors-are-raised-with-real-tests-never-lowered-to-fit`, `a-validation-gate-needs-a-wall-time-budget-before-it-becomes-the-default`.
- **Data:** `spec/plans/coverage-to-100/_research/` in this repository — `pkgs_all.txt`, `files_all.txt`, `categories.txt`, `zero_funcs.txt`, `zero_cross.txt`, `failed_runs.txt`, and the analysis scripts (`analyze.py`, `classify*.py`, `errsrc.py`, `seams/`, `noassert/`). `local.json`, `local.cov`, `nightly/`, `src295/`, `ss/` and `hist/` were the coordinator session's large raw working files and were not copied here.
