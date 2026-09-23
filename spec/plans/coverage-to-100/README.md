---
format: https://specscore.md/plan-specification
status: Blocked
---
# Plan: wb test coverage to 100%

**Status:** Blocked
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

**Start condition — blocked.** The founder said: "Record plan now and wait for #10 to finish before starting implementation wb coverage increase." Task 1 records this gate where tooling can see it: no other task in this plan may start before `sneat-co/storygrapher#10` is merged. *Inference, not a founder quote:* the plan's author reads the reason as the founder's stated VM lane cap (see the `VM resource limits` / `Alex working preferences` memory: at most 3 concurrent lanes on the 4-core VM, at most 2 Go) — storygrapher#10 is itself occupying a Go lane. The founder did not state this reason; treat it as unconfirmed until the founder says otherwise.

## Founder decisions (2026-09-23)

Each was chosen from a multiple-choice question. The chosen option is quoted.

1. **Per-change ratchet.** "Yes, per-change ratchet": every PR must cover 100% of the statements it adds or changes, and no package's uncovered count may go up. `--minimum` stays only as a backstop.
2. **#646 first.** "Yes, land #646 first": sneat-dev/wb#646 (no real sleeps, parallel by default) lands, with its shard timeouts fixed, before any coverage wave.
3. **Separate refactor PRs.** "Yes, separate refactor PRs": the testability refactors are behaviour-preserving, adversarially reviewed and land before the tests that use them. Test lanes change production code only through those seams.
4. **No cross-package credit in the gate.** "No for the gate, yes as diagnostic": `-coverpkg` is not counted. It runs once as a report to separate dead code from code tested only from other packages.
5. **Hard gate at the end.** "Yes, hard 100% at the end": once every package is at 100%, the gate becomes specscore-cli's rule of 100% or fail, with no exclusions, one script shared by CI and pre-push.

## Journey

**Actors:** the PR author, CI, the nightly job, the reviewer, and the supervisor re-measuring coverage.

1. The PR author changes a package and opens a PR. CI's coverage job checks out full history (`fetch-depth: 0`), computes the merge base against the target branch, and runs `wb coverage --changed` against that base.
2. `wb coverage` reports, per package, the uncovered-statement count against the merge-base baseline (main's own published per-package counts) and against any line the diff adds or changes that is not a moved, unmodified line. The author sees pass/fail per package plus, on failure, the exact file:line of every newly uncovered statement.
3. If the PR only moves code (per `git diff --merge-base origin/<base> -U0 --color-moved=plain`), the moved lines are held only to the "uncovered count must not rise" rule — no new 100%-of-the-move requirement. If the PR adds or changes a statement with no covering test, the job fails and names the file:line; the author adds a test and pushes again.
4. Once the PR is green, it merges. The observable result is that main's published per-package uncovered-count artifact (produced by the coverage job on every push to main) only ever moves down or stays flat for the packages the PR touched.
5. The nightly job re-runs the full merged suite against main, publishes the per-package table as the artifact the next PR's ratchet reads, and is the backstop `--minimum=87` check.
6. A separate adversarial reviewer reads each refactor and wave diff before it lands; the reviewer's observable result is a review comment or approval recorded on the PR, not a self-report from the author.
7. The supervisor (the agent or founder tracking this plan) re-measures coverage independently after each wave lands — never trusting a lane's own "it's green" claim — and updates the plan's task statuses.
8. Once every package reaches 100% (task-17 lands), the supervisor cuts task-18: the gate becomes specscore-cli's hard 100%-or-fail rule, the ratchet's per-package baseline machinery is retired, and both CI files move together.

## Approach

Why agents struggled, ranked. The evidence is in the research report linked under Open Questions.

1. **The gate checks one total, not each change.** Under-covered code lands until the headroom runs out. Then an unrelated PR fails by hundredths of a percent, and each retry costs a 7–9-minute CI run. That produced filler "cover" commits and a lowered floor.
2. **The loop is slow, serial and environment-sensitive.**
   - Only 611 of 7,665 tests run in parallel, and the whole local suite took 26m43s at 69% CPU; `cmd/wb`, `internal/orchestrate` and `internal/worktrees` together used 93% of summed package time (not "10–27 minutes" per big package — that range described the whole-suite wall clock, not any one package).
   - `TestRunCommandAdmitsCPUHeavyWorkBelowFloor` deadlocks under `wb run`, because it joins the machine CPU queue behind the run that executes it.
   - On the VM, umask 002 and the real `~/.wb/worktrees` break tests that CI never sees (#587).
   - Flaky tests #504, #505 and #539 remain.
3. **The uncovered code cannot be made to fail.** 45% of the gap is error branches after file, OS, JSON, git and exec calls. `internal/worktrees` has 1,083 functions and no injection seams. Across `internal/worktrees` and `internal/orchestrate`, single functions reach 604 statements (`LandWorktreeMerge`, `internal/orchestrate/worktree_merge.go:1045`) and 450 (`Cleanup`, `internal/worktrees/lifecycle.go:2332`); `PrepareWorktreeMerge` is 381 statements at `internal/orchestrate/worktree_merge.go:426`. `LandWorktreeMerge` and `PrepareWorktreeMerge` live in `internal/orchestrate`, not `internal/worktrees` as an earlier draft of this plan implied.
4. **Measurement hides coverage.** Coverage is per package, and secure git helpers strip `GOCOVERDIR`.
5. **Coverage came in giant one-off PRs.** #554 was +90,783 lines. 286 test files are named after the campaign (`zz_cov_*`, `dqcov`, `tailcov`) rather than the behaviour they test.

The sequence follows from that ranking. Task 1 is the start gate. The CI-policy predecessor (task-2) and the gate (task-3) go next, so every later change counts. #646 (task-4), the cmd/wb per-invocation context refactor (task-5) and test hermeticity (task-6) come next, so lanes can iterate quickly and in parallel. Seams (tasks 7–12) come third, so error paths are reachable without contorted tests. Waves (tasks 13–17) close the statement gap. Task 18 switches to the hard gate.

**Lane cap.** The founder's standing VM cap applies to every task in this plan, not only the waves: at most 3 concurrent lanes, at most 2 of them Go lanes (`VM resource limits` memory). A concrete scheduling risk: once task-6 (hermetic tests) lands, tasks 7, 8, 9 (three Go refactor lanes) and the long-tail waves 13/14 all become ready at once — that is more than 2 Go lanes' worth of ready work. Schedule Go lanes in this order to respect the cap: land task-7 (git/exec runner) and task-8 (file-write primitive) together first (2 Go lanes), then task-9 (clock/sleep seam) once one of those frees a lane, then start waves 13/14 opportunistically in whatever Go lane is free — they need no refactor seam (see task-13/14). Do not start task-10, task-11 or task-12 until their own `Depends-On` tasks are landed, even if a lane is idle.

**Rules every wave brief carries:**
- The target is a list of packages, never "raise the total".
- Every test asserts an observable outcome.
- Forbidden: deleting behaviour to gain coverage, `coverage:ignore`-style markers, build-tag hiding, and lowering the backstop or any package's ratchet baseline. The backstop stays at 87, is only ever raised, and `go-ci.yml` and `nightly-coverage.yml` move together (see task-3). No package's baseline uncovered count may rise.
- Tests sit beside the code, are named after behaviour, and are safe to run in parallel.
- A test PR stays under about 3,000 lines.
- Package wall time may grow by at most 10%.
- A separate adversarial reviewer reads each diff, and the supervisor re-measures coverage itself.
- If a wave cannot reach its target, it stops and reports; it does not cut scope.

Generated proto/connect code is already at 100% and needs no exclusion. Darwin and Windows files stay outside Linux coverage, as in specscore-cli.

## Tasks

### Task 1: Start gate — storygrapher#10 merged

**Id:** task-1
**Depends-On:** —
**Status:** blocked
**Verifies:** `gh pr view 10 -R sneat-co/storygrapher --json state` reports `"state":"MERGED"`.

No other task in this plan may start until `sneat-co/storygrapher#10` is merged. As of 2026-09-23 it is open (`gh pr view 10 -R sneat-co/storygrapher` → `OPEN`, "Port StoryGrapher CLI to Go, harden per design review #8"). `specscore plan readiness coverage-to-100` currently reports `ready: true` because readiness does not read GitHub PR state; this task is the record the tooling *can* see once its own status is kept current by whoever is tracking the gate.

### Task 2: Fix `wb ci audit --target main --strict` findings

**Id:** task-2
**Depends-On:** task-1
**Status:** planning
**Verifies:** `wb ci audit --target main --strict` exits 0 with zero findings.

As measured 2026-09-23, `wb ci audit --target main --strict` fails with 34 findings: 33 `unpinned-tool-install` (every floating-major `uses:` in `.github/workflows/go-ci.yml`, `hub-web.yml`, `nightly-coverage.yml` and `race.yml`) and 1 `frontend-coverage-threshold`. Fix all 34 in their own commit, separate from any coverage-ratchet change, so task-3 can wire `--strict` into CI without an unrelated backlog failing every PR on day one.

### Task 3: Per-change coverage ratchet in `wb coverage`

**Id:** task-3
**Depends-On:** task-2
**Status:** planning
**Verifies:** a fixture PR that moves an uncovered function unchanged passes; a fixture PR that adds one uncovered statement fails and names its file:line; `wb coverage` reports counts, not rounded percentages.

`wb coverage` fails when any package's uncovered count rises against its baseline (below), or when a statement added or changed against the merge base is uncovered and is not a moved, unmodified line (moved-code rule, below). It reports counts rather than rounded percentages. Wire it into `.github/workflows/go-ci.yml` with `--minimum=87` kept as a backstop and `wb ci audit --target <base> --strict` enabled (task-2 lands first so this starts clean; `<base>` is the PR's target branch, not the literal word "main"). Add `fetch-depth: 0` to the coverage job's checkout step (`.github/workflows/go-ci.yml:280`, today a shallow clone with no `fetch-depth`, unlike the eligibility job at `:176`), so the merge base is resolvable. The nightly job (`nightly-coverage.yml`) publishes the per-package uncovered-count table as a build artifact on every push to main.

**Proposed, pending founder confirmation before task-2 starts:**

- **(a) Changed statement.** A coverage block that overlaps an added line in `git diff --merge-base origin/<base> -U0 --color-moved=plain` whose lines are not marked as moved. Moved lines are held only to the per-package rule that the uncovered count must not rise. AC: a fixture PR that moves an uncovered function unchanged passes, and a fixture PR that adds one uncovered statement fails and names its file:line.
- **(b) Baseline.** No committed baseline file. The baseline is the per-package uncovered counts from main's CI coverage profile at the merge base, published as an artifact on every push to main. When no artifact exists, the job measures the merge base itself. Because the counts come from main, they ratchet down automatically and nobody edits a baseline. `fetch-depth: 0` is added to the coverage job (above). The first baseline this task produces is measured at task-3's own landing SHA on main, not backdated to the 295e503f research snapshot (23 commits behind main as of 2026-09-23) that this Summary's 87.98%/10,577-uncovered figures come from — those figures are the reason for this plan, not the number the ratchet starts from.
- **(c) #646 and #629.** Both open PRs must rebase onto the ratchet and pass it once task-3 lands. Both are authored by alex (`trakhimenok`), who is also this plan's owner; alex rebases #646 (task-4 depends on this) and #629 once task-3 merges.
- **(d) Backstop.** `--minimum` stays at 87 in both `.github/workflows/go-ci.yml:292` and `.github/workflows/nightly-coverage.yml:58`. It is only ever raised, and both files move together — never lowered to fit, per lesson `l10-coverage-floors-are-raised-with-real-tests-never-lowered-to-fit`.

These four points are also listed under Open Questions for explicit founder sign-off.

Issue #570 ("Expose changed-package scoping as a verb so agents can test only what they changed") is cited only for the pre-push gate's scope — `wb run --changed -- go test`, i.e. which packages a local run touches. It is test-scoping, not changed-statement coverage, and does not by itself define the moved-code or baseline rules above; those are this task's own design, filed here rather than under #570.

Docs this task must also update, because tests enforce them: `AGENTS.md:84` (the total-floor sentence — replace it with the per-package ratchet plus the 87 backstop), and, for any new `wb coverage`/`wb ci audit` flag, a row in `ai/capabilities.json` and a line in `docs/cli-flag-matrix.md` (`cmd/wb/skills_test.go` reads both and fails otherwise). This task also adds the test-design guidance paragraph the research report recommends for `AGENTS.md` (naming tests after behaviour, asserting an observable outcome, no `zz_cov_*`/`dqcov`/`tailcov`-style campaign names) — no other task in this plan covers it.

### Task 4: Land #646 (no real sleeps, parallel by default)

**Id:** task-4
**Depends-On:** task-3
**Status:** planning
**Verifies:** PR #646 is merged; `wb ci audit --target main --strict` still exits 0 after the merge; the coverage-shard jobs it touches complete within their `timeout-minutes` budget.

Alex rebases sneat-dev/wb#646 onto main (post task-3), fixes its `internal/worktrees` coverage-shard timeouts, and lands it.

### Task 5: `cmd/wb` per-invocation context (refactor)

**Id:** task-5
**Depends-On:** task-4
**Status:** planning
**Verifies:** a mechanical check (`grep`-based, wired into CI) finds zero command handlers in `cmd/wb` reading or writing package-level mutable state; existing `cmd/wb` tests pass unchanged (behaviour-preserving).

Move `cmd/wb`'s global state into a per-invocation context with injected env and cwd, building the command tree per call — the specscore-cli `run(args, cli.Run, cli.Fatal)` seam. This is a standalone, behaviour-preserving, adversarially reviewed refactor PR per founder decision 3: it lands on its own, before task-6's test-hermeticity work depends on it, and no test-writing lane may make this change as a side effect of adding tests. This is `sneat-dev/wb#623` step 3 ("make `cmd/wb` tests parallel-safe... replace `t.Setenv`/`os.Chdir` with an injected environment, HOME, config and working-directory seam"); #646 (task-4, landed first) is #623 steps 1–2 only (inject retry/backoff delays, add `t.Parallel()`), so this task is the remaining, larger step of the same issue.

### Task 6: Hermetic, fast test environment

**Id:** task-6
**Depends-On:** task-4, task-5
**Status:** planning
**Verifies:** `cmd/wb`, `internal/worktrees` and `internal/orchestrate` each run in at most 3 minutes on the VM; the CI coverage job runs in at most 6 minutes; issues #587, #620, #504, #505, #539 and #582 are each closed with a linked fix commit; a regression test proves `wb run --` no longer deadlocks its own tests.

In shared test setup, set umask 022 and a private HOME and WB_HOME. Give tests their own run queue so `wb run` cannot deadlock its own tests. Fix #587, #620, #504, #505, #539 and #582. This task consumes task-5's per-invocation context to make `cmd/wb` tests run in parallel; it makes no further production-code changes of its own (per founder decision 3, test lanes change production code only through already-landed seams). This is #623 step 4 (measure a per-package wall-time table before and after, recorded in the PR).

### Task 7: Git/exec runner seam

**Id:** task-7
**Depends-On:** task-6
**Status:** planning
**Verifies:** a mechanical check finds zero direct `exec.Command`/`os/exec` call sites outside the runner package; a test exercises the fake runner failing on demand.

Route git, gh and other subprocess calls through one runner interface, with a fake that can fail on demand. This is behaviour-preserving and makes about 1,500–2,000 uncovered statements reachable (estimate). Per `rule:cutover-verbs-mean-full-cutover`: this task inventories every current direct-call site first (the mechanical check above, run once before the change to size the work and again after to prove completion), removes the old direct-call path rather than adding the runner alongside it, and the mechanical check is wired into CI so a new direct call cannot be reintroduced.

### Task 8: Safe file-write primitive

**Id:** task-8
**Depends-On:** task-6
**Status:** planning
**Verifies:** a mechanical check finds zero direct temp-file write/sync/chmod/close/rename sequences outside the new package; a test exercises the injectable failure point.

Consolidate the temp-file write, sync, chmod, close and rename sequences into one package with an injectable failure point. Estimated at about 1,000–1,300 statements. Per `rule:cutover-verbs-mean-full-cutover`: inventory every current call site, remove the old inline sequences (not merely add the new package alongside them), and add the mechanical check to CI so a new inline sequence cannot be reintroduced.

### Task 9: Clock and sleep seam

**Id:** task-9
**Depends-On:** task-6
**Status:** planning
**Verifies:** a mechanical check finds zero direct `time.Sleep`/`time.Now` calls in retry, timeout or backoff code paths outside the seam.

Inject time and sleep wherever retries, timeouts or backoff exist. Estimated at about 180 statements.

### Task 10: Secure helpers as a thin shim plus a testable core

**Id:** task-10
**Depends-On:** task-7
**Status:** planning
**Verifies:** the CI coverage profile shows non-zero coverage for the in-process core of each split helper (for example `RunSecureRenameGitHelper`, currently 56 of 59 statements uncovered because `GOCOVERDIR` is stripped before the fd-inheriting exec).

Split each fd-inheriting secure git helper into a minimal shim and an in-process core, so tests exercise the core without losing `GOCOVERDIR`. Estimated at about 300 statements.

### Task 11: Split the largest functions into steps

**Id:** task-11
**Depends-On:** task-7, task-8
**Status:** planning
**Verifies:** a mechanical check finds no function over 150 lines remaining in `internal/orchestrate/worktree_merge.go` or in `internal/worktrees/lifecycle.go`'s `Cleanup`; characterization tests captured before the split pass unchanged after it.

Split `LandWorktreeMerge` (604 statements, `internal/orchestrate/worktree_merge.go:1045`), `PrepareWorktreeMerge` (381 statements, `internal/orchestrate/worktree_merge.go:426`), `Cleanup` (450 statements, `internal/worktrees/lifecycle.go:2332`) and the other functions over 150 lines into named steps. Write characterization tests first and change no behaviour. This makes about 1,500 recovery branches cheap to test. Because `LandWorktreeMerge` and `PrepareWorktreeMerge` live in `internal/orchestrate`, not `internal/worktrees`, task-16 (the `internal/orchestrate` waves) depends on this task, not only task-17 (the `internal/worktrees` waves).

### Task 12: Failing-writer helper and cross-package diagnostic

**Id:** task-12
**Depends-On:** task-6
**Status:** planning
**Verifies:** the `-coverpkg` diagnostic report is published once as a build artifact; every one of the 17 exported functions currently at 0% local coverage has either a filed local-test task or a `wb deadcode` result showing zero callers.

Add a shared failing `io.Writer` test helper, worth about 160 statements. Run `-coverpkg` once as a report (founder decision 4) to separate dead code from code tested only from other packages. As measured, all 17 exported functions at 0% local coverage (`zero_funcs.txt`) that also appear cross-package-called (`zero_cross.txt`) have a caller outside their own package — none of the 17 is dead by this evidence. This task does not delete any of them; it files a local-test plan for each against the wave task that owns its package. Deletion is never done here: if a future `-coverpkg`/`wb deadcode` run finds code with zero callers anywhere, that deletion goes in its own separate PR, reviewed on its own.

### Task 13: Wave W1, long tail A

**Id:** task-13
**Depends-On:** task-3, task-6
**Status:** planning
**Verifies:** each of the 8 named packages reports 0 uncovered statements in the CI coverage profile at the wave's merge SHA.

Eight smaller packages, 816 uncovered statements, to 100%. This wave needs no refactor seam (tasks 7–12): it depends only on the ratchet (task-3) and hermetic tests (task-6). Production-code edits in this wave are forbidden except where a package's own existing structure already supports a test without a shared seam; if a package turns out to need one of tasks 7–12's seams, it moves to a later wave rather than improvising a local one.

### Task 14: Wave W2, long tail B

**Id:** task-14
**Depends-On:** task-3, task-6
**Status:** planning
**Verifies:** each of the 55 named packages reports 0 uncovered statements in the CI coverage profile at the wave's merge SHA.

55 packages, 766 uncovered statements, to 100%. Same seam-free rule as task-13: no dependency on tasks 7–12, and no production-code edits beyond what a package's existing structure already supports.

### Task 15: Waves W3–W5, `cmd/wb` by command family

**Id:** task-15
**Depends-On:** task-6, task-7, task-12
**Status:** planning
**Verifies:** `cmd/wb` reports 0 uncovered statements in the CI coverage profile at each wave's merge SHA.

`cmd/wb` to 100% in three waves of about 730, 900 and 1,200 statements.

### Task 16: Waves W6–W7, `internal/orchestrate`

**Id:** task-16
**Depends-On:** task-7, task-8, task-9, task-11
**Status:** planning
**Verifies:** `internal/orchestrate` reports 0 uncovered statements in the CI coverage profile at each wave's merge SHA.

`internal/orchestrate` to 100% in two waves of about 1,000 and 930 statements. `LandWorktreeMerge` and `PrepareWorktreeMerge` are in this package (`internal/orchestrate/worktree_merge.go`), so this task depends on task-11 (their split into named steps), not only on the git/exec, file-write and clock seams.

### Task 17: Waves W8–W11, `internal/worktrees`

**Id:** task-17
**Depends-On:** task-7, task-8, task-9, task-10, task-11
**Status:** planning
**Verifies:** `internal/worktrees` reports 0 uncovered statements in the CI coverage profile at each wave's merge SHA.

`internal/worktrees` to 100% in four waves of about 800, 1,100, 1,150 and 1,170 statements.

### Task 18: Hard 100% gate

**Id:** task-18
**Depends-On:** task-13, task-14, task-15, task-16, task-17
**Status:** planning
**Verifies:** `wb coverage --minimum=100` is the only coverage gate invoked by both `.github/workflows/go-ci.yml` and the pre-push hook, with no exclusions; both workflow files no longer contain a `--minimum=87` backstop.

Once every package is at 100%, replace the per-change ratchet and its 87 backstop with specscore-cli's gate: 100% or fail, with no exclusions. Per Task 3's decision, both the ratchet and this hard gate live in `wb coverage` — this task does not introduce a separate `scripts/coverage-gate.sh`; CI and the pre-push hook call `wb coverage --minimum=100`, with the pre-push invocation scoped to changed packages (issue #570, `wb run --changed -- go test` / `wb coverage --changed`) because the full suite is too slow for a pre-push hook (`.wb/templates/go-sharded-pre-push.sh` has no coverage step today, and the full local suite takes about 27 minutes). The per-package ratchet and its baseline-publishing machinery (task-3) are retired once this gate lands — a single repo-wide 100% requirement makes a per-package baseline redundant, and this task removes the baseline-publishing step from the nightly job. `--minimum` is removed from both `go-ci.yml` and `nightly-coverage.yml`, not just lowered or left as dead configuration; this is a cutover per `rule:cutover-verbs-mean-full-cutover`, so this task also lists every workflow reference to the old `--minimum=87` invocation and removes each one. The hook comment must not suggest `--no-verify`, per `rule:hooks-are-never-bypassed`.

## Estimates

These are inferences, not measurements. The plan needs about 21 agent lanes over 3–5 calendar weeks, with at most two Go lanes at a time on the 4-core VM. That is about 55–85k new test lines and roughly 100–200M tokens; no per-lane token data exists yet, so the token figure is a guess. The main risk is refactoring the landing and cleanup code agents use daily. It is mitigated by characterization tests first and a separate adversarial review per refactor.

## Open Questions

1. **Changed-statement definition (task-3a).** Confirm: a coverage block overlapping an added line in `git diff --merge-base origin/<base> -U0 --color-moved=plain` whose lines are not marked as moved; moved lines are held only to the per-package "uncovered count must not rise" rule.
2. **Baseline (task-3b).** Confirm: no committed baseline file — the baseline is main's own per-package uncovered counts, published as a nightly/push-to-main artifact and read by the ratchet; when absent, the job measures the merge base itself.
3. **#646 and #629 (task-3c).** Confirm alex (their author and this plan's owner) rebases both onto the ratchet once task-3 lands.
4. **Backstop (task-3d).** Confirm the backstop stays at `--minimum=87` in both `go-ci.yml` and `nightly-coverage.yml`, raised only, never lowered, both files moving together.

The [research report](_research/REPORT.md) and its evidence are in `spec/plans/coverage-to-100/_research/` (copied from the coordinator session scratchpad, `wb-coverage/REPORT.md`, 2026-09-23):

- **PRs:** #554, #557, #559, #571, #646, #677.
- **Issues:** #504, #505, #539, #570, #582, #587, #620, #623.
- **Floor-only CI failures:** runs 35626416785, 35756625554, 35782829695–35800668724.

---
*This document follows the https://specscore.md/plan-specification*
