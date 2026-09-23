---
format: https://specscore.md/plan-specification
status: Draft
---
# Plan: wb test coverage to 100%

**Status:** Draft
**Source:** idea:quality-diff-and-thresholds
**Date:** 2026-09-23
**Owner:** alex
**Supersedes:** —

## Summary

Bring wb from 87.98% statement coverage (77,402 of 87,979 statements; 10,577 uncovered, measured by nightly run 35836520378 at 295e503f) to 100%, and keep it there.

Coverage is falling today. In the five days before 2026-09-23, 9,467 new statements landed at about 83% coverage. The single repo-wide floor was lowered from 88 to 87 in `d6f48b06`, against lesson `l10-coverage-floors-are-raised-with-real-tests-never-lowered-to-fit`. 85% of the gap sits in three packages:

| Package | Uncovered statements |
|---|---:|
| `internal/worktrees` | 4,224 |
| `cmd/wb` | 2,837 |
| `internal/orchestrate` | 1,934 |
| 94 other packages | 1,582 |

The plan changes the gate first, so progress sticks. Next it makes the suite fast and hermetic, then adds the seams that make error paths testable. Only then do package-by-package test waves close the gap. The last step switches to a hard 100% gate like specscore-cli's.

**Start condition.** The founder said: "Record plan now and wait for #10 to finish before starting implementation wb coverage increase". Implementation starts only after sneat-co/storygrapher#10 has landed. The reason is the VM limit of two concurrent Go lanes.

## Founder decisions (2026-09-23)

Each was chosen from a multiple-choice question. The chosen option is quoted.

1. **Per-change ratchet.** "Yes, per-change ratchet": every PR must cover 100% of the statements it adds or changes, and no package's uncovered count may go up. `--minimum` stays only as a backstop.
2. **#646 first.** "Yes, land #646 first": sneat-dev/wb#646 (no real sleeps, parallel by default) lands, with its shard timeouts fixed, before any coverage wave.
3. **Separate refactor PRs.** "Yes, separate refactor PRs": the testability refactors are behaviour-preserving, adversarially reviewed and land before the tests that use them. Test lanes change production code only through those seams.
4. **No cross-package credit in the gate.** "No for the gate, yes as diagnostic": `-coverpkg` is not counted. It runs once as a report to separate dead code from code tested only from other packages.
5. **Hard gate at the end.** "Yes, hard 100% at the end": once every package is at 100%, the gate becomes specscore-cli's rule of 100% or fail, with no exclusions, one script shared by CI and pre-push.

## Approach

Why agents struggled, ranked. The evidence is in the research report linked under Open Questions.

1. **The gate checks one total, not each change.** Under-covered code lands until the headroom runs out. Then an unrelated PR fails by hundredths of a percent, and each retry costs a 7–9-minute CI run. That produced filler "cover" commits and a lowered floor.
2. **The loop is slow, serial and environment-sensitive.**
   - Only 611 of 7,665 tests run in parallel, and the big packages take 10–27 minutes.
   - `TestRunCommandAdmitsCPUHeavyWorkBelowFloor` deadlocks under `wb run`, because it joins the machine CPU queue behind the run that executes it.
   - On the VM, umask 002 and the real `~/.wb/worktrees` break tests that CI never sees (#587).
   - Flaky tests #504, #505 and #539 remain.
3. **The uncovered code cannot be made to fail.** 45% of the gap is error branches after file, OS, JSON, git and exec calls. `internal/worktrees` has 1,083 functions and no injection seams. Single functions reach 604 statements (`LandWorktreeMerge`).
4. **Measurement hides coverage.** Coverage is per package, and secure git helpers strip `GOCOVERDIR`.
5. **Coverage came in giant one-off PRs.** #554 was +90,783 lines. 286 test files are named after the campaign (`zz_cov_*`, `dqcov`, `tailcov`) rather than the behaviour they test.

The sequence follows from that ranking. The gate goes first (Task 1), so every later change counts. Speed and hermeticity come second (Tasks 2–3), so lanes can iterate. Seams come third (Tasks 4–9), so error paths are reachable without contorted tests. Waves (Tasks 10–20) run at most two Go lanes at a time and are ordered by statements unlocked per unit of effort.

**Rules every wave brief carries:**
- The target is a list of packages, never "raise the total".
- Every test asserts an observable outcome.
- Forbidden: deleting behaviour to gain coverage, `coverage:ignore`-style markers, build-tag hiding, and lowering any floor or baseline.
- Tests sit beside the code, are named after behaviour, and are safe to run in parallel.
- A test PR stays under about 3,000 lines.
- Package wall time may grow by at most 10%.
- A separate adversarial reviewer reads each diff, and the supervisor re-measures coverage itself.
- If a wave cannot reach its target, it stops and reports; it does not cut scope.

Generated proto/connect code is already at 100% and needs no exclusion. Darwin and Windows files stay outside Linux coverage, as in specscore-cli.

## Tasks

### Task 1: Per-change coverage ratchet in `wb coverage`

**Id:** task-1
**Depends-On:** —
**Status:** planning

Add a committed per-package baseline of uncovered statement counts. `wb coverage` fails when any package's uncovered count rises, or when any statement added or changed against the merge base is uncovered (issue #570, `wb coverage --changed`). It reports counts rather than rounded percentages. Wire it into `.github/workflows/go-ci.yml` with `--minimum` kept as a backstop and `wb ci audit --target --strict` enabled. The nightly job publishes the per-package table. The ratchet and the later hard gate both live in `wb coverage`, so CI and local runs share one implementation.

### Task 2: Land #646 (no real sleeps, parallel by default)

**Id:** task-2
**Depends-On:** 1
**Status:** planning

Rebase sneat-dev/wb#646, fix its `worktrees` coverage-shard timeouts, and land it.

### Task 3: Hermetic, fast test environment

**Id:** task-3
**Depends-On:** 2
**Status:** planning

- In shared test setup, set umask 022 and a private HOME and WB_HOME.
- Give tests their own run queue so `wb run` cannot deadlock its own tests.
- Fix #587, #620, #504, #505, #539 and #582.
- Move `cmd/wb` global state into a per-invocation context with injected env and cwd, building the command tree per call (the specscore-cli `run(args, cli.Run, cli.Fatal)` seam), so `cmd/wb` tests can run in parallel.
- Target: `cmd/wb`, `internal/worktrees` and `internal/orchestrate` each at most 3 minutes on the VM, and the CI coverage job at most 6 minutes.

### Task 4: Git/exec runner seam

**Id:** task-4
**Depends-On:** 3
**Status:** planning

Route git, gh and other subprocess calls through one runner interface, with a fake that can fail on demand. This is behaviour-preserving and makes about 1,500–2,000 uncovered statements reachable (estimate).

### Task 5: Safe file-write primitive

**Id:** task-5
**Depends-On:** 3
**Status:** planning

Consolidate the temp-file write, sync, chmod, close and rename sequences into one package with an injectable failure point. Estimated at about 1,000–1,300 statements.

### Task 6: Clock and sleep seam

**Id:** task-6
**Depends-On:** 3
**Status:** planning

Inject time and sleep wherever retries, timeouts or backoff exist. Estimated at about 180 statements.

### Task 7: Secure helpers as a thin shim plus a testable core

**Id:** task-7
**Depends-On:** 4
**Status:** planning

Split each fd-inheriting secure git helper into a minimal shim and an in-process core, so tests exercise the core without losing `GOCOVERDIR`. Estimated at about 300 statements.

### Task 8: Split the largest functions into steps

**Id:** task-8
**Depends-On:** 4, 5
**Status:** planning

Split `LandWorktreeMerge` (604 statements), `Cleanup` (450), `PrepareWorktreeMerge` (381) and the other functions over 150 lines into named steps. Write characterization tests first and change no behaviour. This makes about 1,500 recovery branches cheap to test.

### Task 9: Failing-writer helper and cross-package diagnostic

**Id:** task-9
**Depends-On:** 3
**Status:** planning

Add a shared failing `io.Writer` test helper, worth about 160 statements. Run `-coverpkg` once as a report (founder decision 4). For each of the 17 exported functions at 0% locally, either delete it as dead code or plan local tests.

### Task 10: Wave W1, long tail A

**Id:** task-10
**Depends-On:** 1, 3
**Status:** planning

Eight smaller packages, 816 uncovered statements, to 100%.

### Task 11: Wave W2, long tail B

**Id:** task-11
**Depends-On:** 1, 3
**Status:** planning

55 packages, 766 uncovered statements, to 100%.

### Task 12: Waves W3–W5, `cmd/wb` by command family

**Id:** task-12
**Depends-On:** 3, 4, 9
**Status:** planning

`cmd/wb` to 100% in three waves of about 730, 900 and 1,200 statements.

### Task 13: Waves W6–W7, `internal/orchestrate`

**Id:** task-13
**Depends-On:** 4, 5, 6
**Status:** planning

`internal/orchestrate` to 100% in two waves of about 1,000 and 930 statements.

### Task 14: Waves W8–W11, `internal/worktrees`

**Id:** task-14
**Depends-On:** 4, 5, 6, 7, 8
**Status:** planning

`internal/worktrees` to 100% in four waves of about 800, 1,100, 1,150 and 1,170 statements.

### Task 15: Hard 100% gate

**Id:** task-15
**Depends-On:** 10, 11, 12, 13, 14
**Status:** planning

Once every package is at 100%, replace the ratchet's backstop with specscore-cli's gate: a `scripts/coverage-gate.sh`-style check (or `wb coverage --minimum=100`) at 100%, with no exclusions, shared by CI and the pre-push hook. The hook comment must not suggest `--no-verify`, per `rule:hooks-are-never-bypassed`.

## Estimates

These are inferences, not measurements. The plan needs about 21 agent lanes over 3–5 calendar weeks, with at most two Go lanes at a time on the 4-core VM. That is about 55–85k new test lines and roughly 100–200M tokens; no per-lane token data exists yet, so the token figure is a guess. The main risk is refactoring the landing and cleanup code agents use daily. It is mitigated by characterization tests first and a separate adversarial review per refactor.

## Open Questions

None at this time. The research report is in the coordinator session scratchpad (`wb-coverage/REPORT.md`, 2026-09-23), with the evidence behind every number above:

- **PRs:** #554, #557, #559, #571, #646, #677.
- **Issues:** #504, #505, #539, #570, #582, #587, #620, #623.
- **Floor-only CI failures:** runs 35626416785, 35756625554, 35782829695–35800668724.

---
*This document follows the https://specscore.md/plan-specification*
