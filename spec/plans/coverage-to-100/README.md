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

**Readiness caveat.** `specscore plan readiness coverage-to-100` reports `ready: true` even while this plan's own `Status:` is `Blocked` and task-1 is unmet — it does not read this Plan's Status field or GitHub PR state (verified 2026-09-23; see task-1). Agents check this plan's `Status:` field and task-1 directly, not `specscore plan readiness`. Filed as [specscore/specscore-cli#216](https://github.com/specscore/specscore-cli/issues/216).

## Founder decisions (2026-09-23)

Each was chosen from a multiple-choice question. The chosen option is quoted.

1. **Per-change ratchet.** "Yes, per-change ratchet": every PR must cover 100% of the statements it adds or changes, and no package's uncovered count may go up. `--minimum` stays only as a backstop.
2. **#646 first.** "Yes, land #646 first": sneat-dev/wb#646 (no real sleeps, parallel by default) lands, with its shard timeouts fixed, before any coverage wave.
3. **Separate refactor PRs.** "Yes, separate refactor PRs": the testability refactors are behaviour-preserving, adversarially reviewed and land before the tests that use them. Test lanes change production code only through those seams.
4. **No cross-package credit in the gate.** "No for the gate, yes as diagnostic": `-coverpkg` is not counted. It runs once as a report to separate dead code from code tested only from other packages.
5. **Hard gate at the end.** "Yes, hard 100% at the end": once every package is at 100%, the gate becomes specscore-cli's rule of 100% or fail, with no exclusions, one script shared by CI and pre-push.

## Journey

**Actors:** the PR author, CI, the nightly job, the reviewer, and the supervisor re-measuring coverage.

**Assuming Open Questions 1–4 are confirmed by the founder:** steps 1–4 below describe task-3's proposed ratchet design (moved-code rule, baseline, #646/#629, backstop) — none of it is built yet, and none of it is settled until the founder confirms it.

1. *(proposed)* The PR author changes a package and opens a PR. CI's coverage job checks out full history (`fetch-depth: 0`), computes the merge base against the target branch, and runs `wb coverage --changed` against that base.
2. *(proposed)* `wb coverage` reports, per package, the uncovered-statement count against the merge-base baseline (the counts go-ci's coverage job published for that exact SHA when it was itself pushed to main — see step 5) and against any line the diff adds or changes that is not a moved, unmodified line. The author sees pass/fail per package plus, on failure, the exact file:line of every newly uncovered statement.
3. *(proposed)* If the PR only moves code (per `git diff --merge-base origin/<base> -U0 --color-moved=plain`), the moved lines are held only to the "uncovered count must not rise" rule — no new 100%-of-the-move requirement. If the PR adds or changes a statement with no covering test, the job fails and names the file:line; the author adds a test and pushes again.
4. *(proposed)* Once the PR is green, it merges. The observable result is that main's published per-package uncovered-count artifact only ever moves down or stays flat for the packages the PR touched.
5. go-ci's coverage job is the ratchet's one and only baseline producer: task-3 exempts it from the validation-reuse skip on push events, so it runs on every push to main without exception and always uploads the per-package uncovered-count artifact. The nightly job is a separate, independent full-merged-suite run against main on a cron schedule (never on push) — it is the `--minimum=87` (later `=100`) backstop check, not a baseline source.
6. A separate adversarial reviewer reads each refactor and wave diff before it lands; the reviewer's observable result is a review comment or approval recorded on the PR, not a self-report from the author.
7. The supervisor (the agent or founder tracking this plan) re-measures coverage independently after each wave lands — never trusting a lane's own "it's green" claim — and updates the plan's task statuses.
8. Once every package reaches 100% (task-18 lands), the supervisor cuts task-20: the gate becomes specscore-cli's hard 100%-or-fail rule, the ratchet's per-package baseline machinery is retired, and both CI files move together.

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

The sequence follows from that ranking. Task 1 is the start gate. The CI-policy predecessor (task-2) and the gate (task-3) go next, so every later change counts. #646 (task-4), the cmd/wb per-invocation context refactor (task-5) and the run-queue/output-truncation refactor (task-6) come next and can run in parallel, so test hermeticity (task-7) can consume both. Seams (tasks 8–13) come third, so error paths are reachable without contorted tests. Waves (tasks 14–18) close the statement gap. Task 19 builds the changed-package verb the hard gate needs. Task 20 switches to the hard gate.

**Lane cap.** The founder's standing VM cap applies to every task in this plan, not only the waves: at most 3 concurrent lanes, at most 2 of them Go lanes (`VM resource limits` memory). A concrete scheduling risk: once task-4 (#646) lands, task-5 (cmd/wb context refactor) and task-6 (run-queue seam + #582 fix) both become ready at once — that is already 2 Go lanes, so nothing else Go-lane-sized should start until one of them frees a lane. Once task-7 (hermetic tests) then lands, tasks 8, 9, 10 (three more Go refactor lanes) and task-13 (failing-writer helper + cross-package diagnostic) and the long-tail waves 14/15 all become ready at once — more than 2 Go lanes' worth of ready work. Schedule Go lanes in this order to respect the cap: land task-5 and task-6 together first (2 Go lanes); once task-7 lands, land task-8 (git/exec runner) and task-9 (file-write primitive) together next (2 Go lanes), then task-10 (clock/sleep seam) or task-13 (failing-writer + diagnostic) once one of those frees a lane, then start waves 14/15 opportunistically in whatever Go lane is free — they need no refactor seam (see task-14/15). Do not start task-11, task-12 or task-19 until their own `Depends-On` tasks are landed, even if a lane is idle.

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

No other task in this plan may start until `sneat-co/storygrapher#10` is merged. As of 2026-09-23 it is open (`gh pr view 10 -R sneat-co/storygrapher` → `OPEN`, "Port StoryGrapher CLI to Go, harden per design review #8"). `specscore plan readiness coverage-to-100` currently reports `ready: true` because readiness does not read GitHub PR state or this plan's own `Status:` field (see the Readiness caveat above and [specscore/specscore-cli#216](https://github.com/specscore/specscore-cli/issues/216)); check this plan's `Status:` field and this task, not `specscore plan readiness`, before starting any other task.

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

`wb coverage` fails when any package's uncovered count rises against its baseline (below), or when a statement added or changed against the merge base is uncovered and is not a moved, unmodified line (moved-code rule, below). It reports counts rather than rounded percentages. Wire it into `.github/workflows/go-ci.yml` with `--minimum=87` kept as a backstop and `wb ci audit --target <base> --strict` enabled (task-2 lands first so this starts clean; `<base>` is the PR's target branch, not the literal word "main"). Add `fetch-depth: 0` to the coverage job's checkout step (`.github/workflows/go-ci.yml:280`, today a shallow clone with no `fetch-depth`, unlike the eligibility job at `:176`), so the merge base is resolvable. As an explicit implementation step, exempt the coverage job from the validation-reuse skip specifically on push events to `main` (`go-ci.yml:267`, currently `... && (github.event_name != 'push' || needs.validation-reuse.outputs.reuse != 'true')`), so it runs on every push to main without exception — this makes it the ratchet's one and only baseline producer; the nightly job (`nightly-coverage.yml`) runs only on `schedule`/`workflow_dispatch`, never on push, so it stays a separate full-suite `--minimum=87` backstop, not a baseline source.

**Proposed, pending founder confirmation before task-3 starts:**

- **(a) Changed statement.** A coverage block that overlaps an added line in `git diff --merge-base origin/<base> -U0 --color-moved=plain` whose lines are not marked as moved. Moved lines are held only to the per-package rule that the uncovered count must not rise. AC: a fixture PR that moves an uncovered function unchanged passes, and a fixture PR that adds one uncovered statement fails and names its file:line.
- **(b) Baseline.** No committed baseline file. The baseline is the per-package uncovered counts go-ci's coverage job publishes as a build artifact on every push to main (the sole producer, per the push-event exemption above — the nightly job is not a producer, since it never runs on push). When no artifact exists, the job measures the merge base itself, under its own wall-time budget: a hard cap (sized to fit inside the coverage job's existing 45-minute `timeout-minutes` budget), not an open-ended fallback, so a missing artifact cannot silently make every PR pay for a from-scratch merge-base run; if the fallback would exceed its budget the job fails loud instead of hanging, per lesson `a-validation-gate-needs-a-wall-time-budget-before-it-becomes-the-default`. Because the counts come from main, they ratchet down automatically and nobody edits a baseline. `fetch-depth: 0` is added to the coverage job (above). The first baseline this task produces is measured at task-3's own landing SHA on main, not backdated to the 295e503f research snapshot (23 commits behind main as of 2026-09-23) that this Summary's 87.98%/10,577-uncovered figures come from — those figures are the reason for this plan, not the number the ratchet starts from.
- **(c) #646 and #629.** Both open PRs must rebase onto the ratchet and pass it once task-3 lands. Both are authored by alex (`trakhimenok`), who is also this plan's owner; alex rebases #646 (task-4 depends on this) and #629 once task-3 merges.
- **(d) Backstop.** `--minimum` stays at 87 in both `.github/workflows/go-ci.yml:292` and `.github/workflows/nightly-coverage.yml:58`. It is only ever raised, and both files move together — never lowered to fit, per lesson `l10-coverage-floors-are-raised-with-real-tests-never-lowered-to-fit`.

These four points are also listed under Open Questions for explicit founder sign-off.

Issue #570 ("Expose changed-package scoping as a verb so agents can test only what they changed") is cited only for the pre-push gate's scope — `wb run --changed -- go test`, i.e. which packages a local run touches (built in task-19). It is test-scoping, not changed-statement coverage, and does not by itself define the moved-code or baseline rules above; those are this task's own design, filed here rather than under #570.

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
**Verifies:** a mechanical check (`grep`-based, wired into CI) finds zero command handlers in `cmd/wb` reading or writing the package-level mutable state named below; existing `cmd/wb` tests pass unchanged (behaviour-preserving).

Move `cmd/wb`'s global state into a per-invocation context with injected env and cwd, building the command tree per call — the specscore-cli `run(args, cli.Run, cli.Fatal)` seam. The globals to remove, all in `cmd/wb/main.go:33-41`: `projectsRoot`, `filterFlag`, `extraOrgs`, `nonInteractive`, `commandStarted` (verified 2026-09-23; the package-level `var (...)` block also elsewhere in `cmd/wb` — `mergePolicy*`, `defaultBranch*`, `wbSkills*`, `sessionRegister*` — are already-replaceable function-variable seams, not invocation state, and are out of scope here). This is a standalone, behaviour-preserving, adversarially reviewed refactor PR per founder decision 3: it lands on its own, before task-7's test-hermeticity work depends on it, and no test-writing lane may make this change as a side effect of adding tests. This is `sneat-dev/wb#623` step 3 ("make `cmd/wb` tests parallel-safe... replace `t.Setenv`/`os.Chdir` with an injected environment, HOME, config and working-directory seam"); #646 (task-4, landed first) is #623 steps 1–2 only (inject retry/backoff delays, add `t.Parallel()`), so this task is the remaining, larger step of the same issue.

### Task 6: Test run-queue seam and coverage-output-truncation fix (refactor)

**Id:** task-6
**Depends-On:** task-4
**Status:** planning
**Verifies:** a regression test proves `wb run --` no longer deadlocks its own tests (tests get their own run queue, separate from the machine-wide CPU admission queue `wb run` itself uses); a regression test for #582 shows a `FAIL`/`panic:` block surviving output truncation that a head+tail-only truncation would have dropped.

This is production code, not test-only, so it is its own refactor task per founder decision 3 (test lanes change production code only through already-landed seams) — it does not belong in task-7, which stays test-only. Two independent fixes:
- **Run-queue seam.** Give tests their own run queue so `TestRunCommandAdmitsCPUHeavyWorkBelowFloor` (and any test invoking `wb run`) does not join the machine-wide CPU admission queue behind the outer `wb run` that is executing it.
- **#582 (coverage output truncation drops the middle, where the test failure is; OPEN as of 2026-09-23).** Change the coverage-diagnostics truncation to keep every `^--- FAIL` block, `^FAIL\b` line and `^panic:` line with its stack, spending only the remaining budget on head/tail, instead of a head+tail-only truncation that reliably keeps only passing `ok` lines.

### Task 7: Hermetic, fast test environment

**Id:** task-7
**Depends-On:** task-5, task-6
**Status:** planning
**Verifies:** `cmd/wb`, `internal/worktrees` and `internal/orchestrate` each run in at most 3 minutes on the VM; the CI coverage job runs in at most 6 minutes; issues #587, #620, #504, #505 and #539 are each closed with a linked fix commit; a regression test proves `wb run --` no longer deadlocks its own tests (consuming task-6's run-queue seam).

In shared test setup, set umask 022 and a private HOME and WB_HOME. Fix #587, #620, #504, #505 and #539. This task consumes task-5's per-invocation context to make `cmd/wb` tests run in parallel, and task-6's run-queue seam to stop the `wb run` deadlock; it makes no production-code changes of its own — per founder decision 3, test lanes change production code only through already-landed seams. This is #623 step 4 (measure a per-package wall-time table before and after, recorded in the PR).

### Task 8: Git/exec runner seam

**Id:** task-8
**Depends-On:** task-7
**Status:** planning
**Verifies:** a mechanical check finds zero direct `exec.Command`/`os/exec` call sites outside the runner package and outside an explicit allow-list — the six fd-inheriting secure-git-helper functions, which must keep calling `exec.Command` directly for fd inheritance both before and after task-11 splits each into a thin shim plus a testable core: `RunSecureCleanupGitHelper` (`internal/worktrees/lifecycle.go:5597`), `RunSecureRenameGitHelper` (`internal/worktrees/rename.go:142`), `RunSecureCanonicalPolicyGitHelper` (`internal/worktrees/worktrees.go:2353`), `RunSecureCanonicalGitHelper` (`internal/worktrees/worktrees.go:2419`), `RunSecureStageGitHelper` (`internal/worktrees/worktrees.go:3163`), `RunSecureStageCanonicalGitHelper` (`internal/worktrees/worktrees.go:3227`) — plus whatever else this task's own pre-change inventory finds and names into the allow-list; a test exercises the fake runner failing on demand.

Route git, gh and other subprocess calls through one runner interface, with a fake that can fail on demand. This is behaviour-preserving and makes about 1,500–2,000 uncovered statements reachable (estimate). Per `rule:cutover-verbs-mean-full-cutover`: this task inventories every current direct-call site first (the mechanical check above, run once before the change to size the work and again after to prove completion), removes the old direct-call path rather than adding the runner alongside it, and the mechanical check is wired into CI so a new direct call cannot be reintroduced. Per founder decision 1, this refactor PR is itself a change and must carry tests for 100% of every statement it adds or modifies — including the call sites it rewrites to go through the runner, not only new production code; size the PR (or split it along package lines) to stay within the ~3,000-line test-PR guideline above.

### Task 9: Safe file-write primitive

**Id:** task-9
**Depends-On:** task-7
**Status:** planning
**Verifies:** a mechanical check finds zero direct temp-file write/sync/chmod/close/rename sequences outside the new package; a test exercises the injectable failure point.

Consolidate the temp-file write, sync, chmod, close and rename sequences into one package with an injectable failure point. Estimated at about 1,000–1,300 statements. Per `rule:cutover-verbs-mean-full-cutover`: inventory every current call site, remove the old inline sequences (not merely add the new package alongside them), and add the mechanical check to CI so a new inline sequence cannot be reintroduced. Per founder decision 1, this refactor PR must carry tests for 100% of every statement it adds or modifies, sized (or split) to stay within the ~3,000-line test-PR guideline above.

### Task 10: Clock and sleep seam

**Id:** task-10
**Depends-On:** task-7
**Status:** planning
**Verifies:** a mechanical check finds zero direct `time.Sleep`/`time.Now` calls in retry, timeout or backoff code paths outside the seam.

Inject time and sleep wherever retries, timeouts or backoff exist. Estimated at about 180 statements. The sleep/retry sites to inject, as measured 2026-09-23 (excluding tests): `internal/remotestate/gitrepo/clonelock.go:71`, `internal/gitops/gitops.go:240`, `internal/remotestate/gitrepo/provider.go:211`, `internal/agents/owner.go:325`, `internal/orchestrate/pr_create.go:591`, `internal/orchestrate/worktree_merge_ack.go:891`, `internal/worktrees/repository_registration_lock.go:65`, `internal/worktrees/repository_registration_lock.go:81`, `cmd/wb/daemon_process_darwin.go:140`, `cmd/wb/daemon.go:997`. Per founder decision 1, this refactor PR must carry tests for 100% of every statement it adds or modifies.

### Task 11: Secure helpers as a thin shim plus a testable core

**Id:** task-11
**Depends-On:** task-8
**Status:** planning
**Verifies:** the CI coverage profile shows non-zero coverage for the in-process core of each of the six helpers named in task-8's allow-list (for example `RunSecureRenameGitHelper`, currently 56 of 59 statements uncovered because `GOCOVERDIR` is stripped before the fd-inheriting exec).

Split each of the six fd-inheriting secure git helpers named in task-8 (`RunSecureCleanupGitHelper`, `RunSecureRenameGitHelper`, `RunSecureCanonicalPolicyGitHelper`, `RunSecureCanonicalGitHelper`, `RunSecureStageGitHelper`, `RunSecureStageCanonicalGitHelper`) into a minimal shim and an in-process core, so tests exercise the core without losing `GOCOVERDIR`. Estimated at about 300 statements.

### Task 12: Split the largest functions into steps

**Id:** task-12
**Depends-On:** task-8, task-9
**Status:** planning
**Verifies:** none of the four named functions below remains a single unsplit function over 150 lines; characterization tests captured before the split pass unchanged after it.

As measured 2026-09-23 (parsed by function brace-depth), exactly four functions in these two files exceed 150 lines — this is the named list, not an open-ended "and others": `PrepareWorktreeMerge` (`internal/orchestrate/worktree_merge.go:426`, 601 lines / 381 statements), `LandWorktreeMerge` (`internal/orchestrate/worktree_merge.go:1045`, 897 lines / 604 statements), `Cleanup` (`internal/worktrees/lifecycle.go:2332`, 769 lines / 450 statements) and `ListWithDiagnostics` (`internal/worktrees/lifecycle.go:1099`, 173 lines). Split each into named steps. Write characterization tests first and change no behaviour. This makes about 1,500 recovery branches cheap to test. Because `LandWorktreeMerge` and `PrepareWorktreeMerge` live in `internal/orchestrate`, not `internal/worktrees`, task-17 (the `internal/orchestrate` waves) depends on this task, not only task-18 (the `internal/worktrees` waves). Per founder decision 1, the characterization tests and whatever tests each extracted step needs must themselves cover 100% of what this PR touches; split the PR per function if a single PR would exceed the ~3,000-line test-PR guideline above.

### Task 13: Failing-writer helper and cross-package diagnostic

**Id:** task-13
**Depends-On:** task-7
**Status:** planning
**Verifies:** the `-coverpkg` diagnostic report is published once as a build artifact; every one of the 17 exported functions currently at 0% local coverage has either a filed local-test task or a `wb deadcode` result showing zero callers.

Add a shared failing `io.Writer` test helper, worth about 160 statements. Run `-coverpkg` once as a report (founder decision 4) to separate dead code from code tested only from other packages. As measured, all 17 exported functions at 0% local coverage (`zero_funcs.txt`) that also appear cross-package-called (`zero_cross.txt`) have a caller outside their own package — none of the 17 is dead by this evidence. This task does not delete any of them; it files a local-test plan for each against the wave task that owns its package. Deletion is never done here: if a future `-coverpkg`/`wb deadcode` run finds code with zero callers anywhere, that deletion goes in its own separate PR, reviewed on its own.

### Task 14: Wave W1, long tail A

**Id:** task-14
**Depends-On:** task-3, task-7
**Status:** planning
**Verifies:** each of the following 8 packages reports 0 uncovered statements in the CI coverage profile at the wave's merge SHA (uncovered counts as measured 2026-09-23, from `_research/pkgs_all.txt`, summing to the 816 cited below): `internal/sessionmove` (155), `internal/layout` (148), `internal/deps` (107), `internal/runqueue` (96), `internal/sessionlaunch` (91), `internal/hooks` (85), `internal/sessionpark` (76), `internal/daemon` (58).

Eight smaller packages, 816 uncovered statements, to 100%. This wave needs no refactor seam (tasks 8–13): it depends only on the ratchet (task-3) and hermetic tests (task-7). Production-code edits in this wave are forbidden except where a package's own existing structure already supports a test without a shared seam; if a package turns out to need one of tasks 8–13's seams, it moves to a later wave rather than improvising a local one.

### Task 15: Wave W2, long tail B

**Id:** task-15
**Depends-On:** task-3, task-7
**Status:** planning
**Verifies:** each of the 52 packages listed in `_research/pkgs_all.txt` with uncovered > 0, excluding the three packages in the Summary's table and task-14's 8, reports 0 uncovered statements in the CI coverage profile at the wave's merge SHA. As measured 2026-09-23 that set sums to 766 statements over 52 packages (not 55 — corrected from an earlier draft's estimate; see `_research/pkgs_all.txt` for the row-by-row source): `internal/locallink`, `internal/agents`, `internal/streams`, `internal/migrate`, `internal/lifecyclehooks`, `internal/session`, `internal/wbconfig`, `internal/quality`, `internal/repopath`, `internal/archiveprune`, `hub/redeliver`, `internal/githubobserver`, `api/githubapp`, `internal/repositoryevents`, `internal/disk`, `internal/pathguard`, `internal/agentguard`, `internal/hostload`, `internal/fleetsync`, `internal/waitregistry`, `internal/policy`, `internal/sessioncustody`, `internal/streamsync`, `internal/discover`, `internal/mergeack`, `internal/retiredcandidateack`, `internal/npmrelease`, `internal/sessioncourier`, `internal/checkoutmarker`, `internal/remotestate/gitrepo`, `internal/wbhome`, `internal/dashboard`, `internal/sessionmessage`, `internal/sessionparkreceive`, `internal/sessionreceive`, `internal/canonicalrescue`, `internal/diskusage`, `internal/peers`, `internal/prwatch`, `internal/runlog`, `internal/sessionmessenger`, `internal/prsnapshot`, `internal/recipe`, `internal/testenv`, `hub`, `internal/ciaudit`, `internal/envguard`, `internal/prmeta`, `internal/remotestate/hub`, `internal/sessiontransport`, `internal/sessiontransport/transporttest`, `internal/taskoffload`.

Same seam-free rule as task-14: no dependency on tasks 8–13, and no production-code edits beyond what a package's existing structure already supports.

### Task 16: Waves W3–W5, `cmd/wb` by command family

**Id:** task-16
**Depends-On:** task-7, task-8, task-13
**Status:** planning
**Verifies:** `cmd/wb` reports 0 uncovered statements in the CI coverage profile at each wave's merge SHA.

`cmd/wb` to 100% in three waves of about 730, 900 and 1,200 statements.

### Task 17: Waves W6–W7, `internal/orchestrate`

**Id:** task-17
**Depends-On:** task-8, task-9, task-10, task-12
**Status:** planning
**Verifies:** `internal/orchestrate` reports 0 uncovered statements in the CI coverage profile at each wave's merge SHA.

`internal/orchestrate` to 100% in two waves of about 1,000 and 930 statements. `LandWorktreeMerge` and `PrepareWorktreeMerge` are in this package (`internal/orchestrate/worktree_merge.go`), so this task depends on task-12 (their split into named steps), not only on the git/exec, file-write and clock seams.

### Task 18: Waves W8–W11, `internal/worktrees`

**Id:** task-18
**Depends-On:** task-8, task-9, task-10, task-11, task-12
**Status:** planning
**Verifies:** `internal/worktrees` reports 0 uncovered statements in the CI coverage profile at each wave's merge SHA.

`internal/worktrees` to 100% in four waves of about 800, 1,100, 1,150 and 1,170 statements.

### Task 19: Build #570 — `wb run --changed` and `wb coverage --changed`

**Id:** task-19
**Depends-On:** task-3
**Status:** planning
**Verifies:** `wb run --changed -- go test` runs only the packages a local diff touched (measured against a fixture branch with a known changed-package set); `wb coverage --changed` reuses the same changed-package computation.

Promote the changed-package computation that today lives embedded as shell inside the pre-commit hook template (`internal/hooks/config.go:476`) into a first-class, tested Go verb per issue #570, and expose it as `wb run --changed -- <command>`. Wire `wb coverage --changed` to reuse the same changed-package detection so task-20's pre-push invocation has one implementation to call. This is test-scoping (which packages a local run touches), distinct from task-3's changed-*statement* ratchet design — see task-3's citation note.

### Task 20: Hard 100% gate

**Id:** task-20
**Depends-On:** task-14, task-15, task-16, task-17, task-18, task-19
**Status:** planning
**Verifies:** `wb coverage --minimum=100` is the only coverage gate invoked by both `.github/workflows/go-ci.yml` and the pre-push hook, with no exclusions; both `go-ci.yml` and `nightly-coverage.yml` show `--minimum=100`, not `--minimum=87`.

Once every package is at 100%, replace the per-change ratchet and its 87 backstop with specscore-cli's gate: 100% or fail, with no exclusions. Per Task 3's decision, both the ratchet and this hard gate live in `wb coverage` — this task does not introduce a separate `scripts/coverage-gate.sh`; CI and the pre-push hook call `wb coverage --minimum=100`, with the pre-push invocation scoped to changed packages via task-19's `wb run --changed -- go test` / `wb coverage --changed`, because the full suite is too slow for a pre-push hook (`.wb/templates/go-sharded-pre-push.sh` has no coverage step today, and the full local suite takes about 27 minutes). `--minimum=87` is removed from both files, replaced by `--minimum=100` — not just lowered or left as dead configuration; this is a cutover per `rule:cutover-verbs-mean-full-cutover`, so this task also lists every workflow reference to the old `--minimum=87` invocation and updates each one. The nightly job keeps running unchanged in shape (same cron schedule, same full-merged-suite run) — only its threshold moves to 100, since it stays the independent backstop that catches a regression within 24 hours even though the PR-path gate is now scoped to changed packages. The per-package ratchet and its baseline-publishing machinery (task-3, including the push-event validation-reuse exemption) are retired once this gate lands — a single repo-wide 100% requirement makes a per-package baseline redundant. The hook comment must not suggest `--no-verify`, per `rule:hooks-are-never-bypassed`.

## Estimates

These are inferences, not measurements. The plan needs about 21 agent lanes over 3–5 calendar weeks, with at most two Go lanes at a time on the 4-core VM. That is about 55–85k new test lines and roughly 100–200M tokens; no per-lane token data exists yet, so the token figure is a guess. The main risk is refactoring the landing and cleanup code agents use daily. It is mitigated by characterization tests first and a separate adversarial review per refactor.

## Open Questions

1. **Changed-statement definition (task-3a).** Confirm: a coverage block overlapping an added line in `git diff --merge-base origin/<base> -U0 --color-moved=plain` whose lines are not marked as moved; moved lines are held only to the per-package "uncovered count must not rise" rule.
2. **Baseline (task-3b).** Confirm: no committed baseline file — the baseline is the per-package uncovered counts go-ci's coverage job publishes on every push to main (exempted from the validation-reuse skip), with a wall-time-budgeted merge-base fallback when no artifact exists; the nightly job is not a baseline producer.
3. **#646 and #629 (task-3c).** Confirm alex (their author and this plan's owner) rebases both onto the ratchet once task-3 lands.
4. **Backstop (task-3d).** Confirm the backstop stays at `--minimum=87` in both `go-ci.yml` and `nightly-coverage.yml`, raised only, never lowered, both files moving together.

The [research report](_research/REPORT.md) and its evidence are in `spec/plans/coverage-to-100/_research/` (copied from the coordinator session scratchpad, `wb-coverage/REPORT.md`, 2026-09-23):

- **PRs:** #554, #557, #559, #571, #646, #677.
- **Issues:** #504, #505, #539, #570, #582, #587, #620, #623.
- **Floor-only CI failures:** runs 35626416785, 35756625554, 35782829695–35800668724.

---
*This document follows the https://specscore.md/plan-specification*
