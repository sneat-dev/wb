---
format: https://specscore.md/plan-specification
status: Executing
---
# Plan: wb test coverage to 100%

**Status:** Executing
**Source:** idea:quality-diff-and-thresholds
**Date:** 2026-09-23
**Owner:** alex
**Supersedes:** —
**Reworked:** 2026-09-25, founder decisions 17–21 (two test tiers; thin `cmd/wb`; e2e on every PR; e2e tests happy paths; integration branch), then decisions 22–24 (review split; process rework for time and tokens; 6 lanes on the MacBook)

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

**Two test tiers (rework of 2026-09-25, founder decisions 17–19).** 100% comes from fast unit tests, not from end-to-end runs.
- Code reaches git, gh and every other external program through a command runner and narrow git-operation interfaces (task-8). Unit tests substitute fakes for them.
- A separate end-to-end tier runs wb against real git to prove the pieces work together (task-24, task-23). It is a required job on every PR (decision 19), but it never counts toward coverage.
- `cmd/wb` becomes a thin layer that parses flags and calls into `internal/` packages, so its own tests only check wiring (task-22).

Where things stand on 2026-09-25 (main e0dcfda6, the #768 batch; earlier main 493ff75 was 88.20%):
- Coverage is 88.42%: 91,662 statements, 10,610 uncovered. `internal/worktrees` 4,585, `cmd/wb` 2,784 and `internal/orchestrate` 1,799 hold 86% of the gap; 37 of 100 packages are at 100%.
- About 240 of 863 tracked test files start real processes: git, fake executables on `PATH`, or other subprocesses. That is a heuristic grep; task-24's check sets the exact list when it lands.

**Start condition — met 2026-09-23.** sneat-co/storygrapher#10 merged as 831f757, so task-1 is complete and the plan is Executing. The founder said: "Record plan now and wait for #10 to finish before starting implementation wb coverage increase." Task 1 records this gate where tooling can see it: no other task in this plan may start before `sneat-co/storygrapher#10` is merged. *Inference, not a founder quote:* the plan's author reads the reason as the founder's stated VM lane cap (see the `VM resource limits` / `Alex working preferences` memory: at most 3 concurrent lanes on the 4-core VM, at most 2 Go) — storygrapher#10 is itself occupying a Go lane. The founder did not state this reason; treat it as unconfirmed until the founder says otherwise.

**Readiness caveat.** `specscore plan readiness coverage-to-100` reported `ready: true` while this plan's own `Status:` was `Blocked` and task-1 was unmet (before 2026-09-23) — it does not read this Plan's Status field or GitHub PR state (verified 2026-09-23; see task-1). Agents check this plan's `Status:` field and task-1 directly, not `specscore plan readiness`. Filed as [specscore/specscore-cli#216](https://github.com/specscore/specscore-cli/issues/216).

## Founder decisions (2026-09-23)

Each was chosen from a multiple-choice question. The chosen option is quoted.

1. **Per-change ratchet.** "Yes, per-change ratchet": every PR must cover 100% of the statements it adds or changes, and no package's uncovered count may go up. `--minimum` stays only as a backstop.
2. **#646 first.** "Yes, land #646 first": sneat-dev/wb#646 (no real sleeps, parallel by default) lands, with its shard timeouts fixed, before any coverage wave.
3. **Separate refactor PRs.** "Yes, separate refactor PRs": the testability refactors are behaviour-preserving, adversarially reviewed and land before the tests that use them. Test lanes change production code only through those seams.
4. **No cross-package credit in the gate.** "No for the gate, yes as diagnostic": `-coverpkg` is not counted. It runs once as a report to separate dead code from code tested only from other packages.
5. **Hard gate at the end.** "Yes, hard 100% at the end": once every package is at 100%, the gate becomes specscore-cli's rule of 100% or fail, with no exclusions, one script shared by CI and pre-push.

## Founder decisions (2026-09-23, Open Questions 1–5)

Asked one at a time, each as a multiple-choice question after the plan landed. The chosen option's label is quoted verbatim; the option text the founder picked is summarised after it.

6. **Moved code (was Open Question 1, task-3a).** "Moved code exempt (Recommended)": lines git marks as moved and unmodified only need the package's untested count not to rise; edited or new lines need a test, named by file:line.
7. **Baseline (was Open Question 2, task-3b).** "CI artifact, run every push (Recommended)": no committed baseline file (the question said "no file in the repo holds these numbers"); go-ci's coverage job publishes per-package untested counts on every push to main, exempt from the validation-reuse skip. The founder accepted the cost of one full coverage run per merge to main.
8. **#646 and #629 (was Open Question 3, task-3c).** "Agent lanes do it (Recommended)", whose option text read: "This programme runs both as normal lanes: Sonnet rebases and adds missing tests, Opus reviews, I land." Alex does not rebase them.
9. **Backstop (was Open Question 4, task-3d).** "Keep 87, raise-only (Recommended)": `--minimum=87` stays in both `go-ci.yml` and `nightly-coverage.yml`, raised only by a normal reviewed PR, never lowered, both files moving together; task-20 replaces it with 100.
10. **Long functions (was Open Question 5, task-12).** "Only when a wave needs it (Recommended)": task-12 splits the 6 named functions; the other 50 are split only when a wave needs it to reach 100%, each in its own reviewed refactor PR.

## Founder decisions (2026-09-23/24, during task-3)

Decisions 11 and 12 were asked one at a time while #696 was in review, and the chosen option label is quoted verbatim. Decision 13 was a free-text instruction from the founder, quoted verbatim.

11. **Count rule scope.** Coverage turned out to vary run to run in some packages (e.g. `internal/sessionpark/target_store.go:558` covered in 6 of 8 identical CI runs, `_research/flaky-coverage-2026-09-24.txt`), so a docs-only PR could fail. "Only packages the PR changes (Recommended)": the per-package "uncovered count must not rise" rule applies only to packages the PR changes (editing or deleting a `_test.go` counts); a rise in any other package is a warning with its file:line, never a failure. The nightly backstop still measures every package.
12. **Flaky lines in a changed package.** `cmd/wb/daemon.go:2554` (a four-goroutine shutdown race) failed #696, which changes `cmd/wb`. The founder first chose "Both", then corrected it within minutes: "Keep rule, fix race". The per-package rule stays (no per-file scoping); the race is fixed at source in its own refactor PR (#699).
13. **Flaky tests.** Free-text instruction: "Fix all flaky tests". *The plan's reading (not a founder quote):* every statement whose coverage varies between identical runs, and every open flaky-test issue (#504, #505, #539), is fixed at source rather than exempted. See task-21.

## Founder decisions (2026-09-24, scheduling)

Asked one at a time; the chosen option label is quoted verbatim, except decision 15, a free-text instruction quoted verbatim. *The text after each quote is the plan's reading of the decision, not a founder quote.*

14. **Coverage lane before task-7.** "Start wave 1 early (Recommended)": task-14 (W1) starts before task-7 for `internal/sessionmove`, `internal/sessionpark`, `internal/runqueue`, `internal/sessionlaunch` and `internal/daemon`, provided every new test is hermetic and deterministic from day one. `internal/deps` and `internal/layout` waited for the testenv cutover lane.
15. **Lane cap.** Free-text instruction: "I allow 3d Go lane". Up to 3 Go lanes run concurrently. The per-run `free -m` ≥1500 MB check stays in every brief.
16. **Seams before waves.** W1 showed that most uncovered statements in fd-heavy packages are error returns after syscalls that need a seam. "Seam right after #646 (Recommended)": task-9 (file-write primitive) starts as soon as #646 (task-4) lands, ahead of tasks 5–7, which follow in their usual order in the other lanes.

## Founder decisions (2026-09-25, test tiers)

These are free-text answers, quoted verbatim. They are numbered by topic, not in the order given: 18 came first. *The text after each quote is the plan's reading, not a founder quote.*

17. **Two test tiers.** "And I think we should have fast unit tests and e2e integration tests. Getting 100% coverage with unit tests should be easy - define interface for external commands caller and git operations and substitute it in test. But e2e testing with real git is also important But. E2E testing is not the way to 100% test coverage. Rework the plan."
    - The coverage target is met by unit tests that use fakes for the command runner and the git operations (task-8).
    - Real-git end-to-end tests form their own tier (task-24, task-23). That tier must pass, but it is not a source of coverage.
    - At the end (task-20), the unit tier alone produces the coverage profile.
18. **Thin `cmd/wb`.** The coordinator proposed to "add a 'thin `cmd/wb`' task to the plan before task-16. Move the logic out of the biggest files (`fleet_default_branch`, `fleet_merge_policy`, `daemon`, `worktree`, `session_park`) into `internal/` packages behind interfaces, one command family per PR". It then asked "Should I add that task to the plan and route the next free lane to it, starting with `fleet_default_branch`?". The founder answered "Yes, you should."
    - That is task-22. It starts without waiting for task-8.
    - Its first PR (`fleet default-branch`) declares its own consumer-side ports in the destination package. They are backed at first by today's helpers, and switch to task-8's runner when it lands.
19. **E2E cadence (was Open Question 6).** Asked whether the e2e tier should be a required job on every PR, or run only on pushes to main and nightly, the founder answered "Every PR."
20. **E2E scope.** The founder said: "I am thinking e2e tests can test only happy path, and maybe just few of failure cases".
    - Each e2e journey tests its happy path.
    - Failure cases belong to the unit tier, against the fakes. Only a few failure cases are e2e tests; task-23 names them.
    - The founder's wording was tentative ("I am thinking", "maybe"). The closed failure list and the justification gate for adding a case are plan choices the founder can revise.
21. **Integration branch.** Asked whether the coverage programme's PRs should land in an integration branch, which then lands in main in batches, the founder answered "Yes, let's use integration branch". The coordinator had proposed this because `main` requires PR branches to be up to date. Every landing made each other open PR stale and cost it another ~17-minute CI run. The coordinator also named the costs: a large PR into main, and refactors that sit off main for up to a day.
    - The founder then asked: "We don't need pr to merge into integration branch, right? We can merge and test locally?" So lanes open no PRs. A lane branches from `cov/integration`, and before handing over it runs its own packages' tests and a coverage check on the VM.
    - An adversarial reviewer reads the lane branch's diff against `cov/integration` and records `Reviewed-Head`, just as for a PR.
    - The founder added: "You can have a single PR on the integration branch into main and track it stays green. So we test feature branches locally and integration branch after merge and push in CI PR workflow". So one standing PR runs from `cov/integration` into `main`.
    - The coordinator merges an approved lane branch into `cov/integration` locally, with a merge commit, and pushes it. Each push runs that PR's full go-ci workflow on GitHub's runners, per-change ratchet against main included. The coordinator pushes one lane per merge, so a red run points at that lane.
    - The coordinator keeps the PR green. A red run is fixed forward before the next lane merges, and decision 13 still applies: no retries as flake fixes.
    - The coordinator merges `origin/main` into `cov/integration` at least daily.
    - The PR lands through `wb pr land`, with a merge commit and an adversarial review of the whole batch, at least daily and at the end of each wave. A new standing PR then opens from `cov/integration`.
    - A task's plan status becomes complete only once its work is on `main`.
    - For coverage-programme work, older text in this plan that says "PR" (for example "refactor PR" or "test PR") now means a lane branch merged into `cov/integration` after its own review. The ~3,000-line guideline applies to each lane branch.

*Plan choices, not founder instructions:*
- folding the seven existing per-package runners into one (task-8);
- the gh port;
- the 60-statement cap on `cmd/wb` functions (task-22);
- the 10-minute e2e budget (task-23);
- the runtime guard, the pending and allow lists (task-24, task-8);
- the rule for which failure cases may be e2e tests, and one contract case per error kind a fake emulates (task-23, task-24);
- making the e2e job required in branch protection through `gh api`, and running the e2e tier nightly (task-24);
- in decision 21:
  - the lane's local test and coverage check before handover;
  - one lane per merge push;
  - fixing forward before the next lane merges;
  - the daily sync from main;
  - the landing cadence (at least daily and at each wave's end), with a batch adversarial review at each landing;
  - "complete only once on main";
  - a new standing PR after each landing;
  - the per-lane review recording `Reviewed-Head`, and the ~3,000-line guideline per lane branch;
  - standing-PR bodies and merge messages say `Refs #N`, never `Fixes #N` unless that issue should close (GitHub closes it on landing).

## Founder decisions (2026-09-25, process)

The founder's words are quoted verbatim. *The text after each quote is the plan's reading, not a founder quote.*

22. **Review split.** "From my perspective if we optimize process we probably also will have less token usage. Maybe we should have sonnet review on feature branches and opus on integration branch before merging to main?"
    - Lane branches get a Sonnet adversarial review. The batch PR from `cov/integration` into `main` gets an Opus adversarial review.
    - Decision 23 (item 5) narrows the lane half: test-only lanes pass a mechanical gate instead.
23. **Process rework.** The founder asked: "Add to handoff request to review current plan with focus on process optimization with goal of decreasing both time to completion and tokens usage." Earlier the same day: "3-5 weeks is way to much - I can't afford this." The coordinator proposed six changes and asked "Which of these changes should I write into the plan?" The founder picked all four options: "1+2+6 faster lane loop", "3 unblock the waves", "4 smaller task-22", "5 reviews by risk".
    1. **Worklists.** One wb command turns CI's coverage profile into per-function lists of uncovered blocks, cut into lane-sized units. A lane gets a list, not a package to explore (task-25).
    2. **Error-path sweeps.** Task-8's scripted runner fake and task-9's file-write injector gain a "fail call N" mode. A sweep helper reruns one happy-path unit test once per external call it makes, failing that call, and checks each outcome. One happy-path test so covers every error return along its path.
    3. **Unblock the waves.** Task-12 splits a long function only when a wave needs it, as decision 10 already says for the other 50. Tasks 7, 11 and 13 no longer block any wave: task-7 and task-11 run beside the waves, and task-13 folds into the first wave that needs its failing writer. The critical path is task-8 → the runner migration of `internal/worktrees` and `internal/orchestrate` → their waves.
    4. **Smaller task-22.** Logic moves out of only the five files named in decision 18: `fleet_default_branch`, `fleet_merge_policy`, `daemon`, `worktree` and `session_park`. The rest of `cmd/wb` gets unit tests in place, through task-5's invocation context and task-8's runner. The 9-family order and the 60-statement cap were plan choices and are dropped.
    5. **Reviews by risk.** A lane whose diff changes only `_test.go` files and `testdata/` passes a mechanical gate instead of a Sonnet review. A lane that changes any other file keeps the Sonnet review. Every batch into `main` keeps the Opus review.
    6. **Where tests run.** Lanes run tests on the MacBook. The VM runs only targeted tests of Linux-only files, through `cov-linux-test`. The handover coverage check runs on GitHub: `go-ci.yml` dispatched on the lane branch (`workflow_dispatch`), which uploads its coverage profile.
24. **Lane count.** Asked "How many implementation lanes may run at once on the Mac (18 cores, 36 GB)? Reviewers are short and come on top. Test runs queue through wb run, so they can't overload the CPU.", the founder picked "6 lanes (Recommended)".
    - Up to 6 implementation lanes run on the MacBook at once. Reviewers come on top.
    - The VM starts no lanes. The founder said: "Do not start new tasks on this vm." It only runs targeted Linux-only tests.
    - This replaces decision 15's cap of 3, which was set for the 4-core VM.

*Plan choices under decisions 22–24 (coordinator, 2026-09-25), not founder instructions:*
- **The mechanical gate** for a test-only lane passes only if all of these hold:
  - the lane's diff against `cov/integration` changes no file outside `_test.go` files and `testdata/`;
  - the lane's `go-ci.yml` dispatch run is green, and its coverage profile covers every block on the lane's worklist;
  - no package the lane touches has more uncovered statements than in the latest `cov/integration` profile;
  - task-24's exact-count guard (`TestUnitTier*`) and `wb ci audit . --target cov/integration --strict` pass;
  - `TestParallelBaselineDoesNotRegress` passes;
  - the no-assert scan (`_research/noassert/`) finds nothing;
  - `GOOS=windows go vet` is clean for the touched packages.

  A gate failure goes back to a fresh agent, as a review finding would.
- **Units.** A unit is about 300 uncovered statements. No two running units share a file.
- **Agents.** One fresh agent per unit, aiming at a 40–80k context. It retires at handover. A review fix goes to a new agent that gets only the review file.
- **Reports.** No heartbeats or interim lane reports: a lane reports once, at handover.
- **Quick wins.** Waves of small packages run in parallel with task-8.

## Journey

**Actors:** the PR author, CI, the nightly job, the reviewer, and the supervisor re-measuring coverage.

**Confirmed by the founder 2026-09-23 (decisions 6–9):** steps 1–5 below describe task-3's ratchet design (moved-code rule, baseline, #646/#629, backstop). Built in task-3 (#696, 654b8bf), with the count rule scoped to changed packages per decision 11.

1. The PR author changes a package and opens a PR. CI's coverage job checks out full history (`fetch-depth: 0`), computes the merge base against the target branch, and runs `wb coverage --changed` against that base.
2. `wb coverage` reports, per package, the uncovered-statement count against the merge-base baseline (the counts go-ci's coverage job published for that exact SHA when it was itself pushed to main — see step 5) and against any line the diff adds or changes that is not a moved, unmodified line. The author sees pass/fail per package plus, on failure, the exact file:line of every newly uncovered statement.
3. If the PR only moves code (per `git diff --merge-base origin/<base> -U0 --color-moved=plain`), the moved lines are held only to the "uncovered count must not rise" rule — no new 100%-of-the-move requirement. If the PR adds or changes a statement with no covering test, the job fails and names the file:line; the author adds a test and pushes again.
4. Once the PR is green, it merges. The observable result is that main's published per-package uncovered-count artifact only ever moves down or stays flat for the packages the PR touched.
5. go-ci's coverage job is the ratchet's one and only baseline producer: task-3 exempts it from the validation-reuse skip on push events, so it runs on every push to main without exception and always uploads the per-package uncovered-count artifact. The nightly job is a separate, independent full-merged-suite run against main on a cron schedule (never on push) — it is the `--minimum=87` (later `=100`) backstop check, not a baseline source.
6. A separate adversarial reviewer reads each refactor and wave diff before it lands; the reviewer's observable result is a review comment or approval recorded on the PR, not a self-report from the author.
7. The supervisor (the agent or founder tracking this plan) re-measures coverage independently after each wave lands — never trusting a lane's own "it's green" claim — and updates the plan's task statuses.
8. Once every package reaches 100% (task-18 lands), the supervisor cuts task-20: the gate becomes specscore-cli's hard 100%-or-fail rule, the ratchet's per-package baseline machinery is retired, and both CI files move together.

**Test tiers (decisions 17 and 19), built by tasks 8, 23 and 24:**

9. A developer or agent runs `go test ./...` locally. That is the unit tier:
   - It starts no process and uses no external network.
   - It runs in parallel.
   - Its coverage is what the ratchet and the final gate measure.
10. To check integration, they run `go test -tags e2e -run '^Test(E2E|Contract)' ./...`.
    - End-to-end tests build wb and drive it against real git repositories in temporary directories.
    - Contract tests run each fake and the real program through the same cases.
11. On every PR, CI runs both tiers as separate required jobs.
    - The coverage job runs the default tier: unit tests, plus any process-starting legacy tests still on task-24's pending list. As that list empties, the coverage profile becomes the unit tier's alone.
    - The e2e job has no coverage step.
12. When a fake and real git disagree, a contract test in the e2e job fails and names the operation. The fake is fixed before any unit test that relies on it is trusted.

## Approach

Why agents struggled, ranked. The evidence is in the research report linked under Research.

1. **The gate checks one total, not each change.** Under-covered code lands until the headroom runs out. Then an unrelated PR fails by hundredths of a percent, and each retry costs a 7–9-minute CI run. That produced filler "cover" commits and a lowered floor.
2. **The loop is slow, serial and environment-sensitive.**
   - Only 611 of 7,665 tests run in parallel, and the whole local suite took 26m43s at 69% CPU; `cmd/wb`, `internal/orchestrate` and `internal/worktrees` together used 93% of summed package time (not "10–27 minutes" per big package — that range described the whole-suite wall clock, not any one package).
   - `TestRunCommandAdmitsCPUHeavyWorkBelowFloor` deadlocks under `wb run`, because it joins the machine CPU queue behind the run that executes it.
   - On the VM, umask 002 and the real `~/.wb/worktrees` break tests that CI never sees (#587).
   - Flaky tests #504, #505 and #539 remain.
3. **The uncovered code cannot be made to fail.** 45% of the gap is error branches after file, OS, JSON, git and exec calls. `internal/worktrees` has 1,083 functions and no injection seams. Across `internal/worktrees` and `internal/orchestrate`, single functions reach 604 statements (`LandWorktreeMerge`, `internal/orchestrate/worktree_merge.go:1045`) and 450 (`Cleanup`, `internal/worktrees/lifecycle.go:2332`); `PrepareWorktreeMerge` is 381 statements at `internal/orchestrate/worktree_merge.go:426`. `LandWorktreeMerge` and `PrepareWorktreeMerge` live in `internal/orchestrate`, not `internal/worktrees` as an earlier draft of this plan implied.
4. **Measurement hides coverage.** Coverage is per package, and secure git helpers strip `GOCOVERDIR`.
5. **Coverage came in giant one-off PRs.** #554 was +90,783 lines. 286 test files are named after the campaign (`zz_cov_*`, `dqcov`, `tailcov`) rather than the behaviour they test.
6. **Tests start real processes instead of using interfaces** (decision 17; measured 2026-09-25).
   - About 240 of 863 test files start git, fake executables on `PATH`, or other subprocesses (heuristic grep).
   - That makes the suite slow and serial: a test that sets `PATH` with `t.Setenv` cannot run in parallel.
   - It makes the suite flaky: "text file busy" (ETXTBSY) on fake executables (#739), and git's automatic garbage collection racing temporary-directory cleanup (#754).
   - It makes error branches hard to reach: a shell script cannot fail in exactly the way a test needs.
   - `cmd/wb` holds real logic: about 41,000 lines, with `worktree.go`, `daemon.go` and `fleet_default_branch.go` each over 2,500. So its tests run whole commands.
   - Three packages already define narrow consumer-side git interfaces (`internal/streams/ports.go` is the model), and seven define their own command runners. The rest call `exec.Command` or git helpers directly.

*Original sequence (2026-09-23), amended by the Rework block below.* The sequence follows from that ranking. Task 1 is the start gate. The CI-policy predecessor (task-2) and the gate (task-3) go next, so every later change counts. #646 (task-4), the cmd/wb per-invocation context refactor (task-5) and the run-queue/output-truncation refactor (task-6) come next and can run in parallel, so test hermeticity (task-7) can consume both. Seams (tasks 8–13) come third (task-9 excepted: it starts right after task-4, founder decision 16), so error paths are reachable without contorted tests. Waves (tasks 14–18) close the statement gap. Task 19 builds the changed-package verb the hard gate needs. Task 20 switches to the hard gate.

**Process rework (decisions 23–24, 2026-09-25).** This supersedes the ordering below wherever they differ.
- The critical path is task-8, then the runner migration of `internal/worktrees` and `internal/orchestrate`, then their waves.
- Task-25 (worklists) runs alongside task-8, as do the quick-win waves of small packages and task-9's last PRs.
- `cmd/wb` waves run in parallel with worktrees once task-5 PR-4 and `cmd/wb`'s runner migration land.
- Task-10 (the clock, about 180 statements) runs alongside task-8, so the orchestrate and worktrees waves that need it don't wait for it.
- Tasks 7, 11 and 13 run beside the waves. Task-12 runs only on demand.

**Rework (decisions 17–19, 2026-09-25).**
- Task-24 (new) sets up the two tiers: the `e2e` tag, the required e2e CI job, and the checks that keep process-starting tests out of the unit tier. It depends only on task-3.
- Task-8 (the command runner and git interfaces) lands its interfaces, fakes and guard early; it depends on task-4 and task-24. Each package's call sites migrate in the refactor PR that precedes that package's wave or task-22 family.
- Task-10 (the clock) now depends on task-4, not task-7.
- Task-7 keeps only the hermetic setup: HOME, umask and the state directory.
- Task-22 thins `cmd/wb` and starts now (decision 18); task-16 waits for it.
- Task-23 builds the real-git e2e suite while the waves run.
- Task-20 gates the unit tier alone, once every pending list is empty.

**Lane cap (amended 2026-09-24 by founder decisions 14–16; these amendments supersede the original scheduling text below).**
- At most 3 concurrent lanes, and all 3 may be Go lanes (decision 15).
- Task-9 starts as soon as task-4 lands, ahead of tasks 5–7 (decision 16).
- W1 (task-14) already started early for five packages (decision 14); its lanes finished with #719/#720, so task-14 has no running lane until a wave slot frees.
- Superseded 2026-09-25 by decision 24: up to 6 implementation lanes on the MacBook, and none on the VM.
- Task-22's `fleet default-branch` (decision 18) waits for task-5 PR-4, because both rewrite the same `cmd/wb` files. Task-8's first PR follows task-24.
- The original text follows for its reasoning.

*Original:* The founder's standing VM cap applies to every task in this plan, not only the waves: at most 3 concurrent lanes, at most 2 of them Go lanes (`VM resource limits` memory). A concrete scheduling risk: once task-4 (#646) lands, task-5 (cmd/wb context refactor) and task-6 (run-queue seam + #582 fix) both become ready at once — that is already 2 Go lanes, so nothing else Go-lane-sized should start until one of them frees a lane. Once task-7 (hermetic tests) then lands, tasks 8, 9, 10 (three more Go refactor lanes), task-13 (failing-writer helper + cross-package diagnostic), and wave W1 (task-14, long-tail A) all become ready at once — more than 2 Go lanes' worth of ready work. Wave W2 (task-15, long-tail B) is not among them: it depends on task-11 for its one seam-needing package (`internal/hooks`), so it is not ready until task-11 lands. Schedule Go lanes in this order to respect the cap: land task-5 and task-6 together first (2 Go lanes); once task-7 lands, land task-8 (git/exec runner) and task-9 (file-write primitive) together next (2 Go lanes), then task-10 (clock/sleep seam) or task-13 (failing-writer + diagnostic) once one of those frees a lane, then start wave W1 (task-14) opportunistically in whatever Go lane is free — it needs no refactor seam. Wave W2 (task-15) becomes startable once task-11 lands. Do not start task-11, task-12 or task-19 until their own `Depends-On` tasks are landed, even if a lane is idle.

**Rules every wave brief carries:**
- The target is a list of packages, never "raise the total".
- Every test asserts an observable outcome.
- Forbidden: deleting behaviour to gain coverage, `coverage:ignore`-style markers, build-tag hiding, and lowering the backstop or any package's ratchet baseline. The backstop stays at 87, is only ever raised, and `go-ci.yml` and `nightly-coverage.yml` move together (see task-3). No package's baseline uncovered count may rise.
- Tests sit beside the code, are named after behaviour, and are safe to run in parallel.
- Every new test is a unit test, as task-24 defines it (decision 17):
  - it starts no process and uses no external network; in-process `httptest` servers and unix sockets under `t.TempDir()` are allowed;
  - git, gh and other programs through task-8's fakes, files through task-9's injector, time through task-10's clock.
  A wave never adds a process-starting test to the default tier.
- Wave ordering against the seams:
  - A package that runs external programs starts its wave only after its task-8 migration PR lands.
  - A package with retry, timeout or backoff code also waits for task-10.
  - Packages that do neither proceed as before.
- A wave may move a legacy process-starting test to the e2e tier, or delete it as redundant, only in a PR where unit tests keep the package's uncovered count flat. The ratchet enforces this.
- Only a happy-path journey, or a failure case on task-23's list, moves to the e2e tier (decision 20). A legacy test of any other failure path is rewritten as a unit test, or deleted as redundant.
- Some code only a real process can reach: the real runner, `internal/process`, task-8's allow-listed launchers, detach and `syscall.Exec` sites, and OS integration such as cgroups. It is tested with Go's helper-process pattern, where the test binary re-runs itself. Task-24 allows this, and it counts toward coverage.
- The `e2e` build tag on test files (task-24) only selects a test tier. It hides no production code, so the ban on build-tag hiding still applies to production code.
- A test PR stays under about 3,000 lines.
- Package wall time may grow by at most 10%.
- Each diff is checked by risk (decision 23): the mechanical gate for test-only lanes, a Sonnet review for any other lane, an Opus review for each batch into `main`. The supervisor re-measures coverage itself.
- If a wave cannot reach its target, it stops and reports; it does not cut scope.
- A lane works from its task-25 worklist. It covers error returns with task-8's and task-9's fail-call-N sweeps before it writes one-off failure tests.

**Lane handover checklist** (each item is a finding that recurred in reviews on 2026-09-25):
- Error text stays byte-identical across a refactor. Fault tests use `errors.Is` and check that no temporary file is left behind.
- Every added or changed statement is covered, restore and error returns included. Mutation-check by reverting each change with `go test -overlay`: a test must go red.
- Unit tests start no process: no real git or gh, no `/usr/bin` on `PATH`, and no wrapper that hides a process start from task-24's detector.
- Tests never touch the real HOME, `~/.wb`, `~/projects` or the source tree. Every invocation gets an explicit projects root.
- The test picks paths, never a clock. Tests assert exact call counts and use trigger contexts, not sleeps.
- Mode checks don't depend on the umask (#770).
- `GOOS=windows go vet` is clean. Unix-only syscalls in tests sit in `//go:build unix` files.
- A change under `.github/workflows` runs `go test -run TestGoCI ./cmd/wb` (`release_contract_test.go` pins go-ci's structure).
- `go run ./cmd/wb ci audit . --target cov/integration --strict` passes: merges into `cov/integration` run no PR CI, so the lane runs it. So do the `internal/quality` guards: `TestUnitTier*`, `TestParallelBaselineDoesNotRegress` and the filewrite-boundary guards.
- Never run `wb coverage` or whole-module tests on the VM.

**Lane procedure (decisions 21–24).**
1. `wb worktree create <task> --base cov/integration --model <exact model id> --original-prompt-file <brief>`.
2. The brief is self-contained. It gives the task, the task-25 worklist unit, the files, the helpers to use, the checklist above, and the handover step: push, report the sha, stop.
3. The lane tests on the MacBook. Linux-only files are tested on the VM with `cov-linux-test <pushed branch> -run '<regex>' ./<pkg>`.
4. The coordinator dispatches `go-ci.yml` on the pushed lane branch. Then it runs the mechanical gate or a Sonnet review (decision 23). An approving review's first line is `Reviewed-Head: <full sha>`.
5. The coordinator merges the lane into `cov/integration` with `--no-ff`, one lane per push, and keeps the standing PR green. After each batch lands on `main`, it recreates `cov/integration` from `main` and retires the lanes' worktrees and branches.

Generated proto/connect code is already at 100% and needs no exclusion. Darwin and Windows files stay outside Linux coverage, as in specscore-cli.

## Tasks

### Task 1: Start gate — storygrapher#10 merged

**Id:** task-1
**Depends-On:** —
**Status:** complete
**Implemented-by:** sneat-co/storygrapher@831f7573c9e6e25a2285a2aa51f0a9b85f93d055
**Note:** Start gate met: sneat-co/storygrapher#10 MERGED 2026-09-23 (831f757). Trust this task and the plan Status, not specscore plan readiness (specscore/specscore-cli#216).
**Evidence:** https://github.com/sneat-co/storygrapher/pull/10
**Verifies:** `gh pr view 10 -R sneat-co/storygrapher --json state` reports `"state":"MERGED"`.

No other task in this plan may start until `sneat-co/storygrapher#10` is merged. It merged on 2026-09-23 as 831f757 ("Port StoryGrapher CLI to Go, harden per design review #8"), so this gate is met. `specscore plan readiness coverage-to-100` currently reports `ready: true` because readiness does not read GitHub PR state or this plan's own `Status:` field (see the Readiness caveat above and [specscore/specscore-cli#216](https://github.com/specscore/specscore-cli/issues/216)); check this plan's `Status:` field and this task, not `specscore plan readiness`, before starting any other task.

### Task 2: Fix `wb ci audit --target main --strict` findings

**Id:** task-2
**Depends-On:** task-1
**Status:** complete
**Implemented-by:** b658697abc0a7b3e7a94272909db76cdfadb0505
**Note:** wb ci audit --target main --strict exits 0 on main at b658697 (was 34 findings): 33 actions pinned to SHA + version comment; hub/web vitest coverage thresholds 20/20/72/65 (measured 20.59/20.59/72.41/65.36, rounded down). Re-verified by the supervisor on canonical main.
**Evidence:** https://github.com/sneat-dev/wb/pull/693
**Verifies:** `wb ci audit --target main --strict` exits 0 with zero findings.

As measured 2026-09-23, `wb ci audit --target main --strict` fails with 34 findings: 33 `unpinned-tool-install` (every floating-major `uses:` in `.github/workflows/go-ci.yml`, `hub-web.yml`, `nightly-coverage.yml` and `race.yml`) and 1 `frontend-coverage-threshold`. Fix all 34 in their own commit, separate from any coverage-ratchet change, so task-3 can wire `--strict` into CI without an unrelated backlog failing every PR on day one.

### Task 3: Per-change coverage ratchet in `wb coverage`

**Id:** task-3
**Depends-On:** task-2
**Status:** complete
**Implemented-by:** 654b8bf310f06586c94a1ef6b99f58393f58e194
**Note:** Per-change ratchet landed (#696): wb coverage --changed; per-package count rule on changed packages (decision 11); CI artifact baseline on every push to main; --minimum=87 backstop unchanged; wb ci audit --strict wired. Follow-ups: #708.
**Evidence:** https://github.com/sneat-dev/wb/pull/696
**Verifies:** a fixture PR that moves an uncovered function unchanged passes; a fixture PR that adds one uncovered statement fails and names its file:line; `wb coverage` reports counts, not rounded percentages.

`wb coverage` fails when the uncovered count of any package the PR changes rises against its baseline (below; decision 11 — a rise in an unchanged package is a warning; a changed package with no baseline entry counts from 0; renames count both paths as changed), or when a statement added or changed against the merge base is uncovered and is not a moved, unmodified line (moved-code rule, below). It reports counts rather than rounded percentages. Wire it into `.github/workflows/go-ci.yml` with `--minimum=87` kept as a backstop and `wb ci audit --target <base> --strict` enabled (task-2 lands first so this starts clean; `<base>` is the PR's target branch, not the literal word "main"). Add `fetch-depth: 0` to the coverage job's checkout step (`.github/workflows/go-ci.yml:280`, today a shallow clone with no `fetch-depth`, unlike the eligibility job at `:176`), so the merge base is resolvable.

**Confirmed by the founder 2026-09-23 (decisions 6–9 above):**

- **(a) Changed statement.** A coverage block that overlaps an added line in `git diff --merge-base origin/<base> -U0 --color-moved=plain` whose lines are not marked as moved. Moved lines are held only to the per-package rule that the uncovered count must not rise. AC: a fixture PR that moves an uncovered function unchanged passes, and a fixture PR that adds one uncovered statement fails and names its file:line.
- **(b) Baseline.** No committed baseline file. The baseline is the per-package uncovered counts go-ci's coverage job publishes as a build artifact on every push to main — the sole producer. This requires exempting the coverage job from the validation-reuse skip specifically on push events to `main` (`go-ci.yml:267`, currently `... && (github.event_name != 'push' || needs.validation-reuse.outputs.reuse != 'true')`), so it runs on every push to main without exception instead of being skipped whenever validation-reuse applies; the nightly job (`nightly-coverage.yml`) runs only on `schedule`/`workflow_dispatch`, never on push, so it is not a producer either way. **This exemption is a CI-cost choice, not a free correction:** it means a full 8-shard coverage run (today budgeted at 45 minutes) executes on every push to main, including ones validation-reuse would otherwise have skipped as already-validated — the founder confirmed that cost, not just the baseline's existence (decision 7). When no artifact exists, the job measures the merge base itself, under its own wall-time budget: a hard cap (sized to fit inside the coverage job's existing 45-minute `timeout-minutes` budget), not an open-ended fallback, so a missing artifact cannot silently make every PR pay for a from-scratch merge-base run; if the fallback would exceed its budget the job fails loud instead of hanging, per lesson `a-validation-gate-needs-a-wall-time-budget-before-it-becomes-the-default`. Because the counts come from main, they ratchet down automatically and nobody edits a baseline. `fetch-depth: 0` is added to the coverage job (above). The first baseline this task produces is measured at task-3's own landing SHA on main, not backdated to the 295e503f research snapshot (23 commits behind main as of 2026-09-23) that this Summary's 87.98%/10,577-uncovered figures come from — those figures are the reason for this plan, not the number the ratchet starts from.
- **(c) #646 and #629.** Both open PRs must rebase onto the ratchet and pass it once task-3 lands. Both are authored under alex's account (`trakhimenok`); per decision 8, agent lanes of this programme rebase #646 (task-4) and #629 once task-3 merges, each as a Go lane under the lane cap.
- **(d) Backstop.** `--minimum` stays at 87 in both `.github/workflows/go-ci.yml:292` and `.github/workflows/nightly-coverage.yml:58`. It is only ever raised, and both files move together — never lowered to fit, per lesson `l10-coverage-floors-are-raised-with-real-tests-never-lowered-to-fit`.

The founder signed off all four on 2026-09-23 (decisions 6–9).

Issue #570 ("Expose changed-package scoping as a verb so agents can test only what they changed") is cited only for the pre-push gate's scope — `wb run --changed -- go test`, i.e. which packages a local run touches (built in task-19). It is test-scoping, not changed-statement coverage, and does not by itself define the moved-code or baseline rules above; those are this task's own design, filed here rather than under #570.

Docs this task must also update, because tests enforce them: `AGENTS.md:84` (the total-floor sentence — replace it with the per-package ratchet plus the 87 backstop), and, for any new `wb coverage`/`wb ci audit` flag, a row in `ai/capabilities.json` and a line in `docs/cli-flag-matrix.md` (`cmd/wb/skills_test.go` reads both and fails otherwise). This task also adds the test-design guidance paragraph the research report recommends for `AGENTS.md` (naming tests after behaviour, asserting an observable outcome, no `zz_cov_*`/`dqcov`/`tailcov`-style campaign names) — no other task in this plan covers it.

### Task 4: Land #646 (no real sleeps, parallel by default)

**Id:** task-4
**Depends-On:** task-3
**Status:** complete
**Note:** Landed #646 (80c880f): injected retry/backoff delays, t.Parallel() by default. Post-merge Go CI push run 36061108352 on 80c880f is green; Tests and coverage (8 shards) took 7m41s against its 45m budget.
**Evidence:** https://github.com/sneat-dev/wb/pull/646
**Verifies:** PR #646 is merged; `wb ci audit --target main --strict` still exits 0 after the merge; the coverage-shard jobs it touches complete within their `timeout-minutes` budget.

An agent lane (decision 8) rebases sneat-dev/wb#646 onto main (post task-3), fixes its `internal/worktrees` coverage-shard timeouts, makes it pass the ratchet, and lands it after an adversarial review. #629 is rebased the same way, as a separate lane when the cap allows; it does not block task-5 or task-6.

### Task 5: `cmd/wb` per-invocation context (refactor)

**Id:** task-5
**Depends-On:** task-4
**Status:** in_progress
**Note:** PR-1 #737 (18330e5), PR-2 #747 (acef265: nonInteractive, extraOrgs) and PR-3 #752 (493ff75: filterFlag) have landed; #750 (`--org` shadowing) was filed from PR-2. PR-4 (projectsRoot, plus AST guards against flag globals and stray `invocation{}` literals; fixes #733) is lane branch `cov-t5-ctx-4`, re-reviewed for `cov/integration`. Its old PR #760 against `main` closes when the lane merges.
**Verifies:** a mechanical check (`grep`-based, wired into CI) finds zero command handlers in `cmd/wb` reading or writing the package-level mutable state named below; existing `cmd/wb` tests pass unchanged (behaviour-preserving).

Move `cmd/wb`'s global state into a per-invocation context with injected env and cwd, building the command tree per call — the specscore-cli `run(args, cli.Run, cli.Fatal)` seam. The globals to remove, all in `cmd/wb/main.go:33-41`: `projectsRoot`, `filterFlag`, `extraOrgs`, `nonInteractive`, `commandStarted` (verified 2026-09-23; the package-level `var (...)` block also elsewhere in `cmd/wb` — `mergePolicy*`, `defaultBranch*`, `wbSkills*`, `sessionRegister*` — are already-replaceable function-variable seams, not invocation state, and are out of scope here). This is a standalone, behaviour-preserving, adversarially reviewed refactor PR per founder decision 3: it lands on its own, before task-7's test-hermeticity work depends on it, and no test-writing lane may make this change as a side effect of adding tests. This is `sneat-dev/wb#623` step 3 ("make `cmd/wb` tests parallel-safe... replace `t.Setenv`/`os.Chdir` with an injected environment, HOME, config and working-directory seam"); #646 (task-4, landed first) is #623 steps 1–2 only (inject retry/backoff delays, add `t.Parallel()`), so this task is the remaining, larger step of the same issue.

### Task 6: Test run-queue seam and coverage-output-truncation fix (refactor)

**Id:** task-6
**Depends-On:** task-4
**Status:** complete
**Note:** Landed #736 (a077486, test run-queue seam; the wb run -- self-deadlock is gone) and #735 (bc63f44, #582 truncation keeps FAIL/panic evidence; closes #582)
**Evidence:** https://github.com/sneat-dev/wb/pull/735
**Verifies:** a regression test proves `wb run --` no longer deadlocks its own tests (tests get their own run queue, separate from the machine-wide CPU admission queue `wb run` itself uses); a regression test for #582 shows a `FAIL`/`panic:` block surviving output truncation that a head+tail-only truncation would have dropped.

This is production code, not test-only, so it is its own refactor task per founder decision 3 (test lanes change production code only through already-landed seams) — it does not belong in task-7, which stays test-only. Two independent fixes:
- **Run-queue seam.** Give tests their own run queue so `TestRunCommandAdmitsCPUHeavyWorkBelowFloor` (and any test invoking `wb run`) does not join the machine-wide CPU admission queue behind the outer `wb run` that is executing it.
- **#582 (coverage output truncation drops the middle, where the test failure is; OPEN as of 2026-09-23).** Change the coverage-diagnostics truncation to keep every `^--- FAIL` block, `^FAIL\b` line and `^panic:` line with its stack, spending only the remaining budget on head/tail, instead of a head+tail-only truncation that reliably keeps only passing `ok` lines.

### Task 7: Hermetic test setup

**Id:** task-7
**Depends-On:** task-5, task-6
**Status:** planning
**Note:** Narrowed 2026-09-25 (decision 17). The unit/e2e tier split moved to task-24, and the per-package time budgets moved to task-20. Since decision 23 it runs beside the waves and blocks none of them; new tests are hermetic from day one under the wave rules. Input: two `TestCwWt` tests fail locally on main from agent-session environment leakage (review of #736, round 2).
**Verifies:**
1. Every test binary's `TestMain`, through one shared `internal/testenv` helper, sets umask 022 and a private HOME and wb state directory before any test runs. A guard test fails if the suite reads the real `~/.wb` or the real projects root: `TestMain` points HOME and the state directory at sentinel temporary directories, and asserts after `m.Run()` that the real ones' modification times did not change.
2. Issues #587, #620 and #758 are closed with linked fix commits. (#504 and #539 moved to task-21 under decision 13.)
3. A regression test proves `wb run --` no longer deadlocks its own tests, using task-6's run-queue seam.
4. #623 step 4 is done: a per-package wall-time table, before and after, recorded in the PR.

The setup is process-wide in `TestMain`, because `t.Setenv` blocks `t.Parallel()`. This task uses task-5's per-invocation context and task-6's run-queue seam, and makes no production-code changes of its own (decision 3).

*Moved to task-21 (decision 13); kept for the analysis.* #504 (`TestDaemonFileBridgeRetryRecoversSubmitAcrossTokenAndGenerationRotation`) and #539 (`TestQueueRunsDifferentRepositoriesInParallelButExcludesSameRepository`) were checked (`gh issue view`, 2026-09-23) for a hidden production dependency the way #505 has one: neither `cmd/wb/daemon_file_bridge.go` nor the daemon package has a production `time.Sleep`/`time.After`, and `internal/repositoryevents`'s queue already exposes an injectable `queue.now` plus channel-based synchronization — both fixes are test-only (tighten the deadline-polling helpers in `cmd/wb/daemon_file_bridge_test.go`, and replace `time.After` timeouts with explicit barriers in `internal/repositoryevents/receiver_test.go`), so both stay here. #505 does not: see task-10.

### Task 8: Command-runner and git-operation interfaces

**Id:** task-8
**Depends-On:** task-4, task-24
**Status:** in_progress
**Note:** Depends-On changed from task-7 to task-4 and task-24 by founder decision 17. Every unit test after this task uses its fakes, and its contract tests need task-24's e2e job. PR-1 is lane branch `cov-t8-pr1`, work in progress. It has the runner, `runnertest`, the runtime guard, the `gitcli` skeleton and the exec-site check; generating `exec_sites.pending` is next. This task is the critical path (decision 23).
**Verifies:**
1. `internal/runner` exists, built on `internal/process`, with that package's process-group cancellation preserved. It offers four operations:
   - Run: captured output;
   - Start: a handle with Wait and Signal;
   - Detach: a process that outlives wb;
   - Interactive: stdio passthrough.

   The real implementation carries task-24's runtime guard.
2. `internal/runner/runnertest` and an in-memory fake for each git port fail on demand in unit tests. `runnertest` has a fail-call-N mode and a sweep helper (decision 23): given a happy-path test body, the helper counts the external calls it makes, reruns it once per call with that call failing, and hands each run's outcome to the test to assert. The helper works for task-9's file-write injector as well.
3. Contract tests (`TestContract*`, in the e2e tier) run the same cases against `internal/gitcli` with real git and against the fakes, and pass for both.
4. A mechanical check wired into CI fails on any direct call to `exec.Command`, `exec.CommandContext`, `os.StartProcess` or `syscall.Exec` in non-test Go files, and on any git invocation, outside `internal/runner`, `internal/gitcli` and the allow-list below.
   - Exception: a file on the pending list `internal/quality/testdata/exec_sites.pending`. The list has one line per file, with its count and owning task. It follows task-24's rules: the list's total may not rise, an entry may grow only when the same PR removes at least as much from other entries, and the creating PR is exempt.
   - A git invocation is a literal `"git"` argv[0] passed to `os/exec`, `internal/process` or the runner, or a call to one of the named git helpers the check lists.
   - Type-only uses such as `exec.ExitError` are not flagged.
   - Any addition to the allow-list is named in the PR and approved in review.

Founder decision 17: "define interface for external commands caller and git operations and substitute it in test."

**`internal/runner`.** One interface for starting external programs, built on the existing `internal/process` rather than beside it (`rule:reuse-existing-sneat-code`). Its four operations, with the call sites each serves:

| Operation | What it does | Used by |
|---|---|---|
| Run | captures stdout, stderr and the exit status | most git and gh calls |
| Start | returns a handle with Wait and Signal | long-running children wb supervises |
| Detach | starts a process that outlives wb | daemon launch, `browser.go`, lifecycle hooks |
| Interactive | passes stdio through | `wb run -- …`, tmux, agent harnesses |

- `syscall.Exec` sites join the allow-list.
- `internal/runner/runnertest` is a scripted fake: it matches the argv and returns canned output or a chosen error.

**Git operations as narrow ports.**
- Each package declares the git operations it needs as a small interface in its own `ports.go`, as `internal/streams/ports.go`, `internal/streamsync/ports.go` and `internal/locallink/ports.go` already do.
- One real adapter, `internal/gitcli`, implements them over the runner. It builds the argv and parses the output, and its unit tests run against the fake runner.
- In-memory fakes implement the ports for unit tests.
- gh is reached the same way: a port per consumer, backed by the runner, or by the existing GitHub API client where one is used.
- *Plan choice:* `internal/gitops` and the seven per-package runner interfaces fold into the one runner, each in its own package's migration PR. The seven are:
  - `npmrelease.CommandRunner`
  - `remotessh.Runner`
  - `prinventory.Runner`
  - `herdr.Runner`
  - `sessioncourier.commandRunner`
  - `sessionparkcourier.commandRunner`
  - `sessionmessage.tmuxCommandRunner`

**Fake fidelity.** A fake that diverges from real git makes unit tests lie. So every git port gets contract tests: the same cases run against `internal/gitcli` with real git and against the fake.

**Git stays the CLI.** wb runs the `git` executable and imports no Go git library; the root README section "Why WB runs the `git` CLI" gives the reasons. On origin/main 493ff751, 49 of the 111 `exec.Command`/`exec.CommandContext` calls in non-test code pass a literal `"git"`. The ports keep the choice open: a library-backed adapter could implement some ports later without touching callers.

**Scope and migration.**
- This task lands five things: the runner, the adapter skeleton, the fakes, the contract-test harness, and the check with every existing site on the pending list, one migration slot per package. Its first PR is complete when those exist. The task itself completes when its own follow-up migrations (listed below) have landed.
- Each package's call sites then migrate in a behaviour-preserving refactor PR (decision 3). That PR is owned by whichever task covers the package, its wave in tasks 14–18 or its task-22 family, and lands before that package's tests are written.
- `internal/worktrees` (20 direct `exec.Command` sites in 8 files on 2026-09-25, apart from the allow-listed secure helpers) and `internal/orchestrate` (6 sites in 2 files) migrate first. They are the critical path (decision 23).
- task-8 itself owns the migration of packages no wave covers: `internal/gitops`, `internal/herdr`, `internal/remotessh`, `internal/sessionparkcourier` and `internal/syncreport`, plus the folding of the per-package runners listed above. It lands those as follow-up PRs before task-20.
- `internal/process` joins the allow-list as the runner's base.
- Per `rule:cutover-verbs-mean-full-cutover`, the cutover is complete only when the pending list is empty, and task-20 checks that. Meanwhile, the check stops any new direct call.
- Per decision 1, every PR carries tests for 100% of the statements it adds or changes, and stays under the ~3,000-line guideline.
- This makes about 1,500–2,000 uncovered statements reachable (estimate).

**Allow-list: the fd-inheriting secure git helpers.**
- These must keep calling `exec.Command`/`exec.CommandContext` directly, because they rely on fd inheritance. That holds both before and after task-11 splits each child into a thin shim plus a testable core.
- They were found by grepping every `ExtraFiles` assignment repo-wide (2026-09-23) and tracing each to its launcher, which sets `command.ExtraFiles`, and its child, which reads fd 3+ via `os.NewFile`.

| Launcher (sets `ExtraFiles`) | Child (`RunSecure*GitHelper`) |
|---|---|
| `setHooksPathAt` (`internal/hooks/git.go:123`) | `RunSecureHooksGitHelper` (`internal/hooks/git.go:135`) |
| `gitCanonicalBytes` (`internal/worktrees/worktrees.go:2252`) | `RunSecureCanonicalGitHelper` (`internal/worktrees/worktrees.go:2419`) |
| `gitCanonicalPolicyBytes` (`internal/worktrees/worktrees.go:2289`) | `RunSecureCanonicalPolicyGitHelper` (`internal/worktrees/worktrees.go:2353`) |
| `runSecureStageHelper` (`internal/worktrees/worktrees.go:3104`) | `RunSecureStageGitHelper` (`internal/worktrees/worktrees.go:3163`) |
| `runSecureStageCanonicalGitHelper` (`internal/worktrees/worktrees.go:3125`) | `RunSecureStageCanonicalGitHelper` (`internal/worktrees/worktrees.go:3227`) |
| `runSecureRenameGitBytesWithHeldWorktree` (`internal/worktrees/rename.go:124`) | `RunSecureRenameGitHelper` (`internal/worktrees/rename.go:142`) |
| `runSecureCleanupGitHelper` (`internal/worktrees/lifecycle.go:5505`) | `RunSecureCleanupGitHelper` (`internal/worktrees/lifecycle.go:5597`) |

(`internal/worktrees/patchid.go:47` is a comment referencing this table, not a call site; `internal/worktrees/worktrees.go:2180`'s `gitWithExtraFiles` is called only with a nil `extraFiles` argument today — an ordinary git subprocess wrapper, not an fd-inheriting helper — so it is not allow-listed and routes through the new runner like any other plain git call.)

### Task 9: Safe file-write primitive

**Id:** task-9
**Depends-On:** task-4
**Status:** in_progress
**Note:** Depends-On changed from task-7 to task-4 by founder decision 16.
- Landed on main: PR-1 #738 (19f33b3), PR-2 #749 (632047e, 11 cmd/wb sites), PR-3 #756 (895ac03, 12 internal/worktrees sites; `O_CLOEXEC` kept, deliberately), PR-4 #763 (eec2637, internal/orchestrate), and PR-5 and PR-6 in the #768 batch (e0dcfda6).
- PR-7 is in `cov/integration`. PR-8 (23 sites, lane branch `cov-t9-pr8`) is in re-review.
- Left: PR-9 (16 sites), and PR-10, which moves `execfile.WriteExecutableFile` onto the injector (deferred from PR-8). PR-10 exists to give the injector an interface test seam; `ForkLock` is not the reason.
- Follow-ups:
  - A 0600 assertion can't detect a missing chmod, because `CreateTemp` already makes 0600 files. The injector's chmod step should pre-set 0644, then the test asserts 0600 (#763 notes).
  - Cover the "same content already exists" branches (`worktree_merge_ack.go` near :447 and :1467), or reword their docstrings (#763 notes).
  - The `filewrite.Writer` doc overclaims byte-identical output for `*os.File` and socket `io.Copy` sources. Narrow the comment, or delegate `ReadFrom` before such a caller exists (PR-6 N3).
  - `nodeidentity` needs a chmod-preset 0600 test and a check for the node id's trailing newline. `MarkResumed`'s docstring should name the `syncDirectory` shim (PR-7 notes).
  - A parked marker left half-written by a write failure makes a retry say "already parked". This is out of task-9's scope; file an issue.
**Verifies:** a mechanical check finds zero direct temp-file write/sync/chmod/close/rename sequences outside the new package; a test exercises the injectable failure point; the injector supports task-8's fail-call-N sweep (decision 23).

Consolidate the temp-file write, sync, chmod, close and rename sequences into one package with an injectable failure point. Estimated at about 1,000–1,300 statements. Per `rule:cutover-verbs-mean-full-cutover`: inventory every current call site, remove the old inline sequences (not merely add the new package alongside them), and add the mechanical check to CI so a new inline sequence cannot be reintroduced. Per founder decision 1, this refactor PR must carry tests for 100% of every statement it adds or modifies, sized (or split) to stay within the ~3,000-line test-PR guideline above.

### Task 10: Clock and sleep seam

**Id:** task-10
**Depends-On:** task-4
**Status:** planning
**Note:** Depends-On changed from task-7 to task-4 (decision 17). This is a production seam that unit tests need, and it does not wait for the test setup.
**Verifies:** a mechanical check finds zero direct `time.Sleep`/`time.Now` calls in retry, timeout or backoff code paths outside the seam. (#505 moved to task-21 under decision 13.)

Inject time and sleep wherever retries, timeouts or backoff exist. Estimated at about 180 statements. The sleep/retry sites to inject, as measured 2026-09-23 (excluding tests): `internal/remotestate/gitrepo/clonelock.go:71`, `internal/gitops/gitops.go:240`, `internal/remotestate/gitrepo/provider.go:211`, `internal/agents/owner.go:325`, `internal/orchestrate/pr_create.go:591`, `internal/orchestrate/worktree_merge_ack.go:891`, `internal/worktrees/repository_registration_lock.go:65`, `internal/worktrees/repository_registration_lock.go:81`, `cmd/wb/daemon_process_darwin.go:140`, `cmd/wb/daemon.go:997`. #505 (flaky `internal/runqueue` heartbeat and `cmd/wb` queue-wait grace tests) was originally assigned here; under founder decision 13 it moved to task-21, which fixes it at source, with its own refactor PR if a seam is needed. Per founder decision 1, this refactor PR must carry tests for 100% of every statement it adds or modifies.

### Task 11: Secure helpers as a thin shim plus a testable core

**Id:** task-11
**Depends-On:** task-8
**Status:** planning
**Verifies:** the CI coverage profile shows non-zero coverage for the in-process core of each of the seven helpers named in task-8's allow-list (for example `RunSecureRenameGitHelper`, currently 56 of 59 statements uncovered because `GOCOVERDIR` is stripped before the fd-inheriting exec).

Split each of the seven fd-inheriting secure git helpers named in task-8's table (`RunSecureHooksGitHelper`, `RunSecureCleanupGitHelper`, `RunSecureRenameGitHelper`, `RunSecureCanonicalPolicyGitHelper`, `RunSecureCanonicalGitHelper`, `RunSecureStageGitHelper`, `RunSecureStageCanonicalGitHelper`) into a minimal shim and an in-process core, so tests exercise the core without losing `GOCOVERDIR`. Estimated at about 300 statements. Per founder decision 1, this refactor PR must carry tests for 100% of every statement it adds or modifies, sized (or split per helper) to stay within the ~3,000-line test-PR guideline above.

### Task 12: Split the largest functions into steps

**Id:** task-12
**Depends-On:** task-8, task-9
**Status:** planning
**Note:** On demand since decision 23. A function in the list below is split only when its wave needs the split to reach 100%, as decision 10 already says for the other 50. The fail-call-N sweeps reach the error returns of long functions without splitting them. No wave depends on this task.
**Verifies:** each of the six named functions below that a wave needed split is no longer a single unsplit function over 150 lines; characterization tests captured before a split pass unchanged after it.

A brace-counting pass in round 2 undercounted this pair of files at four functions; a `go/ast`-based scan (body Lbrace-to-Rbrace span, not brace counting — script and full repo-wide output committed as [`_research/long-functions/`](_research/long-functions/README.md) and [`_research/long-functions.txt`](_research/long-functions.txt), run 2026-09-23) finds six in `internal/orchestrate/worktree_merge.go` and `internal/worktrees/lifecycle.go` combined, and 56 repository-wide. The six in scope here — this is the named list, not an open-ended "and others": `LandWorktreeMerge` (`internal/orchestrate/worktree_merge.go:1045`, 897 lines / 604 statements), `Cleanup` (`internal/worktrees/lifecycle.go:2332`, 769 lines / 450 statements), `PrepareWorktreeMerge` (`internal/orchestrate/worktree_merge.go:426`, 601 lines / 381 statements), `inspectLifecycleWorktree` (`internal/worktrees/lifecycle.go:3602`, 315 lines), `listLayout` (`internal/worktrees/lifecycle.go:1911`, 194 lines) and `ListWithDiagnostics` (`internal/worktrees/lifecycle.go:1099`, 173 lines). Split each into named steps. Write characterization tests first and change no behaviour. This makes about 1,500 recovery branches cheap to test. Because `LandWorktreeMerge` and `PrepareWorktreeMerge` live in `internal/orchestrate`, not `internal/worktrees`, task-17 (the `internal/orchestrate` waves) depends on this task, not only task-18 (the `internal/worktrees` waves). Per founder decision 1, the characterization tests and whatever tests each extracted step needs must themselves cover 100% of what this PR touches; split the PR per function if a single PR would exceed the ~3,000-line test-PR guideline above.

The other 50 of the 56 functions over 150 lines (`_research/long-functions.txt`, everything outside these two files) are not split by this task. Each is split, if and when the package's wave (tasks 14–18) actually needs it to reach 100%, in its own refactor PR separate from that wave's test PRs — not split pre-emptively across the board. Per decision 10, they are not split regardless of coverage need.

### Task 13: Failing-writer helper and cross-package diagnostic

**Id:** task-13
**Depends-On:** —
**Status:** planning
**Note:** Folded into the waves by decision 23. The first wave lane that needs the failing writer adds it. The coordinator runs the `-coverpkg` diagnostic once. No wave depends on this task.
**Verifies:** the `-coverpkg` diagnostic report is published once as a build artifact; every one of the 17 exported functions currently at 0% local coverage has either a filed local-test task or a `wb deadcode` result showing zero callers.

Add a shared failing `io.Writer` test helper, worth about 160 statements. Run `-coverpkg` once as a report (founder decision 4) to separate dead code from code tested only from other packages. As measured, all 17 exported functions at 0% local coverage (`zero_funcs.txt`) that also appear cross-package-called (`zero_cross.txt`) have a caller outside their own package — none of the 17 is dead by this evidence. This task does not delete any of them; it files a local-test plan for each against the wave task that owns its package. Deletion is never done here: if a future `-coverpkg`/`wb deadcode` run finds code with zero callers anywhere, that deletion goes in its own separate PR, reviewed on its own.

### Task 14: Wave W1, long tail A

**Id:** task-14
**Depends-On:** task-3
**Status:** in_progress
**Note:** Early start (founder decision 14); #719 and #720 landed. task-7 was dropped from Depends-On by decision 23.
**Evidence:** https://github.com/sneat-dev/wb/pull/720
**Verifies:** each of the following 7 packages reports 0 uncovered statements in the CI coverage profile at the wave's merge SHA (uncovered counts as measured 2026-09-23, from `_research/pkgs_all.txt`, summing to the 731 cited below): `internal/sessionmove` (155), `internal/layout` (148), `internal/deps` (107), `internal/runqueue` (96), `internal/sessionlaunch` (91), `internal/sessionpark` (76), `internal/daemon` (58).

Seven smaller packages, 731 uncovered statements, to 100%. *Superseded 2026-09-25:* this wave was planned as seam-free, depending only on task-3 and task-7, and its progress note below shows that premise was wrong. Under the rework, a package here that runs external programs waits for its task-8 migration PR, and one with retry, timeout or backoff code waits for task-10 (see Rules). Production-code edits in this wave are forbidden except where a package's own existing structure already supports a test without a shared seam; if a package turns out to need one of tasks 8–13's seams, it moves to a later wave rather than improvising a local one. `internal/hooks` (85 uncovered) is such a package — it contains `RunSecureHooksGitHelper`, one of task-8's allow-listed fd-inheriting helpers, so it moved to task-15 (W2), which depends on task-11, instead of staying here; that move is what keeps this wave seam-free.

Progress (early start, founder decision 14): #719 and #720 landed on 2026-09-24. Uncovered statements, measured from CI coverage profiles against main's published baseline. These CI counts differ by at most four statements from the 2026-09-23 local counts in the Verifies line above (runqueue: 96 local vs 92 CI; sessionlaunch and daemon match):

| Package | Before | After |
|---|---:|---:|
| `internal/sessionmove` | 154 | 146 |
| `internal/sessionpark` | 75 | 71 |
| `internal/runqueue` | 92 | 18 |
| `internal/sessionlaunch` | 91 | 84 |
| `internal/daemon` | 58 | 50 |

The "needs no refactor seam" premise above turned out to be wrong for the fd-heavy packages. Most of what remains is error returns after `Mkdirat`/`Sync`/`Fchmod`/write calls, which need task-9's seam (decision 16). Cgroup and supervisor code in runqueue and daemon needs OS-integration seams that no current task names. The lines reachable without a seam are tracked in #729. `internal/deps` and `internal/layout` are not started.

### Task 15: Wave W2, long tail B

**Id:** task-15
**Depends-On:** task-3
**Status:** planning
**Note:** Decision 23 dropped task-7 and task-11 from Depends-On. Only `RunSecureHooksGitHelper`'s statements in `internal/hooks` wait for task-11. The rest of `internal/hooks` and the other 52 packages proceed.
**Verifies:** each of the 53 packages listed in `_research/pkgs_all.txt` with uncovered > 0, excluding the three packages in the Summary's table and task-14's 7, reports 0 uncovered statements in the CI coverage profile at the wave's merge SHA. As measured 2026-09-23 that set sums to 851 statements over 53 packages (766 over the 52 packages that do not need a seam, plus `internal/hooks` (85), moved here from task-14 — see task-14's note): `internal/hooks`, `internal/locallink`, `internal/agents`, `internal/streams`, `internal/migrate`, `internal/lifecyclehooks`, `internal/session`, `internal/wbconfig`, `internal/quality`, `internal/repopath`, `internal/archiveprune`, `hub/redeliver`, `internal/githubobserver`, `api/githubapp`, `internal/repositoryevents`, `internal/disk`, `internal/pathguard`, `internal/agentguard`, `internal/hostload`, `internal/fleetsync`, `internal/waitregistry`, `internal/policy`, `internal/sessioncustody`, `internal/streamsync`, `internal/discover`, `internal/mergeack`, `internal/retiredcandidateack`, `internal/npmrelease`, `internal/sessioncourier`, `internal/checkoutmarker`, `internal/remotestate/gitrepo`, `internal/wbhome`, `internal/dashboard`, `internal/sessionmessage`, `internal/sessionparkreceive`, `internal/sessionreceive`, `internal/canonicalrescue`, `internal/diskusage`, `internal/peers`, `internal/prwatch`, `internal/runlog`, `internal/sessionmessenger`, `internal/prsnapshot`, `internal/recipe`, `internal/testenv`, `hub`, `internal/ciaudit`, `internal/envguard`, `internal/prmeta`, `internal/remotestate/hub`, `internal/sessiontransport`, `internal/sessiontransport/transporttest`, `internal/taskoffload`.

Mostly the same seam-free rule as task-14, with one named exception: 52 of these 53 packages take no dependency on tasks 8–13 and no production-code edits beyond what their existing structure already supports. The exception is `internal/hooks`, which needs task-11's thin-shim seam for `RunSecureHooksGitHelper`. Since decision 23 only those statements wait for task-11; the wave does not. Under the rework, packages here that run external programs wait for their task-8 migration PR (see Rules). Examples are `internal/streams`, `internal/locallink`, `internal/fleetsync`, `internal/remotestate/gitrepo` and `internal/npmrelease`.

### Task 16: Waves W3–W5, `cmd/wb` by command family

**Id:** task-16
**Depends-On:** task-5, task-8, task-24
**Status:** planning
**Verifies:** `cmd/wb` reports 0 uncovered statements in the CI coverage profile at each wave's merge SHA.

`cmd/wb` goes to 100% in parallel with the `internal/worktrees` waves (decision 23).
- Code in task-22's five files is covered in its destination package after its task-22 PR moves it (decision 18).
- The rest of `cmd/wb` gets unit tests in place. Tests build the command through task-5's invocation context and task-8's runner fake, and never run whole commands against real programs.
- A family's wave starts once `cmd/wb`'s task-8 migration has landed for the files it touches.
- The 2026-09-23 estimate of three waves (about 730, 900 and 1,200 statements) is replaced by task-25's worklist units.

### Task 17: Waves W6–W7, `internal/orchestrate`

**Id:** task-17
**Depends-On:** task-8, task-9, task-10
**Status:** planning
**Verifies:** `internal/orchestrate` reports 0 uncovered statements in the CI coverage profile at each wave's merge SHA.

`internal/orchestrate` to 100%, in task-25's worklist units. `LandWorktreeMerge` and `PrepareWorktreeMerge` are in this package (`internal/orchestrate/worktree_merge.go`). Since decision 23 their error returns are reached by fail-call-N sweeps, and task-12 splits one only if a unit cannot reach 100% without the split. Coverage comes from unit tests against task-8's fakes (decision 17). A test that needs real git belongs in the e2e tier.

### Task 18: Waves W8–W11, `internal/worktrees`

**Id:** task-18
**Depends-On:** task-8, task-9, task-10
**Status:** planning
**Verifies:** `internal/worktrees` reports 0 uncovered statements in the CI coverage profile at each wave's merge SHA.

`internal/worktrees` to 100%, in task-25's worklist units (4,585 uncovered at e0dcfda6). Coverage comes from unit tests against task-8's fakes, with fail-call-N sweeps for error returns (decisions 17 and 23). A test that needs real git belongs in the e2e tier. Only the seven allow-listed secure helpers' statements wait for task-11. Task-12 splits a function only if a unit needs it.

### Task 19: Build #570 — `wb run --changed` and `wb coverage --changed`

**Id:** task-19
**Depends-On:** task-3
**Status:** complete
**Note:** Landed #726 (807267f): wb run --changed and a shared changed-package computation; wb coverage --changed keeps its ratchet semantics
**Evidence:** https://github.com/sneat-dev/wb/pull/726
**Verifies:** `wb run --changed -- go test` runs only the packages a local diff touched (measured against a fixture branch with a known changed-package set); `wb coverage --changed` reuses the same changed-package computation.

Promote the changed-package computation that today lives embedded as shell inside the pre-commit hook template (`internal/hooks/config.go:476`) into a first-class, tested Go verb per issue #570, and expose it as `wb run --changed -- <command>`. Wire `wb coverage --changed` to reuse the same changed-package detection so task-20's pre-push invocation has one implementation to call. This is test-scoping (which packages a local run touches), distinct from task-3's changed-*statement* ratchet design — see task-3's citation note.

### Task 20: Hard 100% gate

**Id:** task-20
**Depends-On:** task-7, task-8, task-14, task-15, task-16, task-17, task-18, task-19, task-22, task-23, task-24
**Status:** planning
**Verifies:**
1. `wb coverage --minimum=100` is the only coverage gate invoked by both `.github/workflows/go-ci.yml` and the pre-push hook, with no exclusions.
2. Both `go-ci.yml` and `nightly-coverage.yml` show `--minimum=100`, not `--minimum=87`.
3. Every pending list is empty:
   - task-24's `unit_tier.pending`;
   - task-8's `exec_sites.pending`.
4. So the coverage job runs the unit tier only, and the CI unit job runs with `git` and `gh` removed from `PATH`.
5. The unit tier of `cmd/wb`, `internal/worktrees` and `internal/orchestrate` each runs in at most 3 minutes on the VM, and the CI coverage job in at most 6.
6. The e2e tier (task-23, task-24) passes as a separate required job.

The gate measures the unit tier alone (decision 17). No process-starting test contributes coverage, apart from the helper-process tests task-24 allows. The e2e tier stays a required pass/fail job with no coverage threshold.

Once every package is at 100%, replace the per-change ratchet and its 87 backstop with specscore-cli's gate: 100% or fail, with no exclusions. Per Task 3's decision, both the ratchet and this hard gate live in `wb coverage` — this task does not introduce a separate `scripts/coverage-gate.sh`; CI and the pre-push hook call `wb coverage --minimum=100`, with the pre-push invocation scoped to changed packages via task-19's `wb run --changed -- go test` / `wb coverage --changed`, because the full suite is too slow for a pre-push hook (`.wb/templates/go-sharded-pre-push.sh` has no coverage step today, and the full local suite takes about 27 minutes). `--minimum=87` is removed from both files, replaced by `--minimum=100` — not just lowered or left as dead configuration; this is a cutover per `rule:cutover-verbs-mean-full-cutover`, so this task also lists every workflow reference to the old `--minimum=87` invocation and updates each one. The nightly job keeps its cron schedule and full-merged-suite run; its threshold moves to 100, and since task-24 it also runs the e2e tier, since it stays the independent backstop that catches a regression within 24 hours even though the PR-path gate is now scoped to changed packages. The per-package ratchet and its baseline-publishing machinery (task-3, including the push-event validation-reuse exemption) are retired once this gate lands — a single repo-wide 100% requirement makes a per-package baseline redundant. The hook comment must not suggest `--no-verify`, per `rule:hooks-are-never-bypassed`.

### Task 21: Deterministic coverage — fix all flaky tests

**Id:** task-21
**Depends-On:** task-1
**Status:** in_progress
**Verifies:** eight identical full-suite coverage runs on one main commit show 0 statements whose coverage differs between runs (`_research/flaky_analyze.py`), and issues #504, #505 and #539 are closed with linked fix commits.

Founder decision 13. The ratchet (task-3) turns coverage that varies between identical runs into random PR failures, so every such statement is made deterministic at source, never exempted. Inventory ([`_research/flaky-coverage-2026-09-24.txt`](_research/flaky-coverage-2026-09-24.txt)): eight parallel `nightly-coverage.yml` runs dispatched on main 825693b (one throwaway branch per run, since the workflow's concurrency group is per ref; the branches are deleted afterwards), with each run's `profile.cov` compared by `_research/flaky_analyze.py`, found 21 blocks / 22 statements in 7 packages, and no failing tests: `internal/orchestrate/ciwait.go` (9), `internal/runqueue/heavy.go` (4), `cmd/wb/daemon.go` (3), `internal/worktrees/worklog.go` (3), `hub/peer_admin.go:173`, `internal/sessionmove/store.go:829`, `internal/sessionpark/target_store.go:558`. Landed so far (2026-09-24):
- Race and flaky-line fixes: #699 (`serveDashboard` shutdown race), #700 (state-lock clock seam), #702 (`ciwait.go`), #703 (closes #504), #705 (`runqueue/heavy.go`), #710 (closes #539), #712 (worklog race hooks; it also carried #714's hook seams, so #714 was closed rather than merged), #726 (a deterministic test for `cmd/wb/daemon_file_bridge.go:307`), #713 (`hub`), #715 (sessionmove/sessionpark race hooks), #646 (a deterministic test for `internal/runqueue/visibility.go:232`), #736 (a deterministic test for `cmd/wb/wait.go:387`).
- The systemic git auto-maintenance TempDir fix. Detached `gc --auto`/`maintenance run --auto` wrote into `.git/objects` after test cleanup had started. #711 fixed `cmd/wb/remote_test.go` with local helpers, #717 added the shared `internal/testenv` helpers, #724, #727 and #730 switch every git-creating test package over to them.
- #718 (go-ci summary expected coverage to be skipped on reused pushes, which turned main red).
- #731 (fixes #728; the issue was closed by hand because the PR body said "Refs"): race.yml sharded into orchestrate, worktrees and the rest, with one shared shard script and a guard that fails on gaps or overlaps; the `prinventory` fake race is fixed.

- #744 (b9806cb, closes #741): the fd-number-reuse flake.
- #740 (60944ea, closes #739):
  - fake executables are written through `internal/execfile` (temp file, rename, with `ForkLock` held), so ETXTBSY is gone;
  - the exec fence unlocks before close;
  - a guard rejects any exec-bit write outside that writer.

Still open:
- #505's `cmd/wb` half.
- #742: a main-push coverage step timed out after 35m, probably a hung test whose package the old truncation hid.
- #745: the go-ci PR race job covers only five packages, so races in other packages reach main.
- #733: parallel `cmd/wb` tests race on the package-level invocation globals in `cmd/wb/main.go`. One global (`projectsRoot`) remains, and task-5's PR-4, #760, removes it.
- #759's fixes landed on main in the #768 batch (lane `cov-t21-batch2`):
  - #748: `internal/orchestrate/worktree_merge.go:4550` covered only by timing;
  - #753: runqueue `TestReadTicketsReapsAnOldOrphanedTempFile`;
  - #754: git auto-gc on a bare test remote racing temporary-directory cleanup;
  - #751: the smoke test leaking a `wb` binary in `/tmp`.
- Left from #759's review:
  - Three bare-clone fixtures don't turn automatic maintenance off: `archive_test.go:100`, `session_park_test.go:453` and `zz_cov_layout_gc_test.go:71`.
  - A doc comment is misplaced at `testenv_test.go:366-372`.
- Left from #744's review (N1): both fd-reuse tests should assert `errors.Is(err, unix.EBADF)`, on the `lock.go:317` and `state.go:1128` paths.
- #766: `ciwait` coverage varied between runs. It is fixed by lane `cov-fix-769`, which is in `cov/integration`.
- #765: `TestSelfHostedBenchWholeJourney` fails on the VM only.
- #770: lifecyclehooks tests fail under umask 002.
- #755: the PR ratchet times out measuring the merge base when main's baseline artifact is not yet published.
- #757: the exec-bit guard sees only single-file, in-function writes.
- #758: `internal/worktrees` tests set `WB_HOME`, which wb now ignores, so they scan the real `~/.wb`. Task-7's private state directory fixes it.
- A final eight-run re-probe.

Many of these flakes come from tests that start real processes. Decision 17 moves such tests to the e2e tier, or replaces them with unit tests, which removes the class rather than patching each instance.

Production changes go through their own behaviour-preserving refactor PRs first (decision 3); a fix that makes coverage depend on a sleep instead of a barrier does not count. This pulls #504/#539 forward from task-7 and #505 forward from task-10.

### Task 22: Thin `cmd/wb`: logic moves into `internal/` behind interfaces

**Id:** task-22
**Depends-On:** task-5
**Status:** planning
**Note:** It starts before task-8 (decision 18). Each family declares consumer-side ports in its destination package, backed at first by today's helpers, and switches to task-8's runner when that package's slot lands. Decision 23 limits it to the five files the coordinator named in decision 18. The 60-statement cap and its pending list (a plan choice) are dropped.
**Verifies:**
1. No non-test file in `cmd/wb` imports `os/exec` or runs git (task-8's check).
2. The logic of `fleet_default_branch.go`, `fleet_merge_policy.go`, `daemon.go`, `worktree.go` and `session_park.go` lives in `internal/` packages. Each of those `cmd/wb` files keeps only flag parsing, dependency wiring and one call per command.
3. Each moved command family has unit tests in its destination package that use fakes. `cmd/wb`'s tests for that family check only flag parsing and dispatch.
4. Each family's behaviour is unchanged: its existing tests pass across the move, with only mechanical edits (package qualifier and import), or through a temporary forwarder in `cmd/wb`.

**Why (founder decision 18).** `cmd/wb` is 127 files and about 41,000 lines, a fifth of wb, and it holds real logic. The largest files, as of 2026-09-25:

| File | Lines |
|---|---:|
| `worktree.go` | 2,926 |
| `daemon.go` | 2,762 |
| `fleet_default_branch.go` | 2,592 |
| `worktree_merge.go` | 1,450 |
| `fleet_merge_policy.go` | 1,148 |
| `deps.go` | 1,065 |

Its tests therefore run whole commands, with fake scripts on `PATH` and real git. All 165 of its test files are white-box (`package main`).

**What changes.**
- Each command family's logic moves into an `internal/` package.
- That package receives its dependencies as parameters: git ports, a gh port, the command runner, the clock, the file writer and the output writer.
- `cmd/wb` is left to parse flags, build those dependencies from the invocation context (task-5), and call one function.
- Print and format helpers over the cap may be split in place rather than moved.
- Once task-8's first PR has landed, new task-22 code calls the runner and the git ports directly, not today's helpers.

**How it lands.**
- One command family per PR. Each is a behaviour-preserving refactor with its own adversarial review (decision 3).
- Order, starting with the family with the most uncovered blocks in `cmd/wb` (195, 2026-09-25 baseline):
  1. `fleet default-branch`
  2. `fleet merge-policy`
  3. `daemon`
  4. `worktree`
  5. `session park`
- Other command families (`hooks`, `ci`, `stream`, `deps` and the rest) stay in `cmd/wb` and are covered in place by task-16 (decision 23).
- **Moves are not free under the ratchet.** A cross-package move raises the destination package's uncovered count, or counts a new package from 0. Decision 6 exempts only moves within a package, and changing a signature to inject dependencies makes a line count as edited. So each PR carries unit tests, using fakes, that keep the destination's uncovered count at or below its baseline.
- A family too large for one PR is split across several, each under the ~3,000-line guideline. `fleet_default_branch.go` alone is 2,592 lines.
- A family whose logic already lives mostly in `internal/` (for example `worktree`, over `internal/worktrees`) moves only its remaining `cmd/wb` logic.
- The logic lands in the existing `internal/` package that owns the domain (for example `internal/worktrees`, `internal/daemon` or `internal/deps`). Otherwise it goes in a new package named after the domain.

### Task 23: Real-git end-to-end suite

**Id:** task-23
**Depends-On:** task-24
**Status:** planning
**Verifies:**
- `go test -tags e2e -run '^TestE2E' ./e2e/...` builds wb and runs each journey below against real git in temporary directories.
- It is green in its own required CI job on every PR.
- Each journey asserts observable outcomes (refs, files, exit codes, output), not coverage.
- Each journey tests its happy path. The suite has no more failure cases than task-23's list, below.
- The suite stays within a 10-minute CI budget, recorded in the PR.

Founder decision 17: "e2e testing with real git is also important".

**The suite.**
- A curated set of user journeys, driven through the built `wb` binary.
- Each runs against throwaway repositories and a local bare remote.
- gh is replaced by a fake gh executable or a local HTTP server, never the real GitHub.

**Proposed first journeys:**
1. Clone and `wb sync` a small fleet.
2. `wb worktree create`, commit, push, then `wb worktree land` into the local target.
3. `wb worktree` rename and retire, with cleanup.
4. `wb deps propagate local`.

**Failure cases (decision 20).** The e2e tier has only a few, listed here:
1. A merge that conflicts, which wb must refuse without changing the target branch.
2. A push the remote rejects as non-fast-forward, which wb must report without retiring the branch.

Adding a case to this list needs a PR that says why no fake can reproduce the behaviour faithfully. Every other failure path is tested in the unit tier.

**Scope.**
- Legacy process-starting tests that task-24's pending list assigns to task-23 move here once their package's unit tests cover the same statements.
- The suite also carries the contract tests of task-8's git adapter. Contract tests are not journeys: they have one case for each error kind a fake emulates (for example a conflict, a missing ref or a rejected push). That keeps the unit tier's failure tests honest. Contract cases count against the same 10-minute budget.
- This tier is not measured for coverage (decision 17).

### Task 24: Test tiers: the unit tier and the real-git e2e tier

**Id:** task-24
**Depends-On:** task-3
**Status:** in_progress
**Note:** PR-1 (the tiers, the detector, the pending and allow lists, the e2e job) is in `cov/integration`. Lane `cov-fix-769` taught the detector the `runCommand` and `installFakeGH` wrappers, which raised the pending total from 4,772 to 4,841.
**Verifies:**
1. **E2E tier.** It consists of `_test.go` files with `//go:build e2e`, whose tests are named `TestE2E*` or `TestContract*`. CI runs them with `go test -tags e2e -run '^Test(E2E|Contract)' ./...` as their own job. The job is required on every PR (decision 19) and has no coverage step.
2. **Static check.** A check in `internal/quality` fails on any default-tier `_test.go` that uses one of these patterns, beyond the file's count on the pending list:
   - `exec.Command`, `exec.CommandContext` or `os.StartProcess`;
   - `testenv.WriteExecutableFile`;
   - `t.Setenv("PATH"`;
   - `runnertest.AllowRealProcess`;
   - a named git or process helper. The check keeps the full list, starting with `runGit`, `gitOutput`, `gitRawOutput`, `runGitIn`, `runGitWithFilesystemCapability`, `gitWithExtraFiles`, `runCommand` and `installFakeGH`;
   - the helper-process marker (`GO_WANT_HELPER_PROCESS`), so that only allow-listed files use it.
3. **Allow list.** Helper-process tests stay permanently, in a separate committed and reviewed file, `internal/quality/testdata/unit_tier.allow`. Every addition is named in its PR and approved in review.
   - It covers tests of `internal/runner`, `internal/process`, task-8's allow-listed launchers, and detach, `syscall.Exec` and OS-integration sites.
   - Files on it may call `exec.Command(os.Args[0], …)` and `runnertest.AllowRealProcess`.
   - Its tests re-run only the test binary itself, never real git or gh, so they pass in task-20's unit job, where `git` and `gh` are not on `PATH`.
   - It is not a pending list, and task-20 does not require it to be empty.
4. **Pending list.** It is a committed file, `internal/quality/testdata/unit_tier.pending`, with one line per test file giving its match count and owning task. A CI step compares it with the base branch's copy and fails if the list's total count rises. A package's total, or a file's count, may rise only when the same PR removes at least as many counts from other entries, for example when task-22 moves a test file from `cmd/wb` into an `internal/` package. The PR that creates the list is exempt. An entry is removed when its count reaches 0.
5. **Coverage source.** The CI coverage job measures the default tier only.

Founder decisions 17 and 19.

**Unit tier: the default `go test ./...`.** It is hermetic and fast, and it is the coverage source.
- Tests start no process and use no external network. In-process `httptest` servers and unix sockets under `t.TempDir()` are allowed.
- Tests reach git, gh and other programs through task-8's fakes.
- The exception is Go's helper-process pattern, where the test binary re-runs itself, for code only a real process can reach. That covers tests of `internal/runner`, `internal/process`, task-8's allow-listed launchers, and detach, `syscall.Exec` and OS-integration sites. Those tests count toward coverage.
- `t.Setenv` and `os.Chdir` stay under the repo's existing rule (never in a parallel test). They are not banned outright.

**E2E tier.** It holds two kinds of test:
- contract tests beside each real adapter, for example task-8's git adapter against real git, proving each fake behaves like the program it replaces;
- black-box journey tests in a top-level `e2e/` package (task-23).

Its rules:
- Decision 13 applies: no retries or quarantine, and task-21's final re-probe includes the e2e job.
- The coordinator landing this task makes the job required in branch protection (via `gh api`), or the founder does, where that needs admin rights.
- The nightly job also runs the e2e tier.
- The build tag selects a test tier and never excludes production code, so the plan's ban on build-tag hiding is unchanged.

**Runtime guard.** A static check cannot see a test that reaches git by calling production code.
- So, once task-8's runner exists, its real implementation refuses to start a process while `testing.Testing()` is true.
- It makes three exceptions:
  - the test is built with the `e2e` tag;
  - it calls `runnertest.AllowRealProcess(t)`, which only files on the pending list or the allow list may call;
  - the process is the re-run helper itself, marked by an environment variable such as `GO_WANT_HELPER_PROCESS=1`, since it has no `t` to call `AllowRealProcess` with.
- After task-8's cutover, the CI unit job runs with `git` and `gh` removed from `PATH`, so any missed direct call fails loudly.

**Transition.**
- The check's first run writes today's process-starting files to the pending list. A heuristic grep on 2026-09-25 counted about 240.
- Each entry names the task that converts it: task-8 (for example `internal/process`, `prinventory` and `gitops`), task-9 (`execfile`), task-22, a wave in tasks 14–18, or task-23.
- *Plan choice (coordinator, 2026-09-25): detector changes.*
  - A PR that widens the detector may raise the list's total. The rise may come only from matches in lines that PR doesn't add. The reviewer checks it entry by entry: the PR's diff of each file whose count rose adds no new process-starting call.
  - Named exception: lane `cov-fix-769` added 3 counted fake-`gh` calls in `internal/orchestrate/ciwait_deadline_unix_test.go` (owner task-17). Before task-8 there was no `gh` fake, and those tests fix the #769 ratchet at `ciwait.go:146-147`. Task-17 converts them.
- Follow-ups:
  - Wire the e2e tier into the nightly job.
  - Fail when the base branch has `unit_tier.pending` and the head doesn't. Otherwise a PR that deletes the list counts as the list-creating PR.
  - Test helpers shaped `run(t, dir, name, args...)` in `canonicalrescue`, `archiveprune` and `layout` still hide from the detector. Matching the bare name `run` would clash with `cmd/wb`'s `run(args, …)`, so the detector needs to match on the signature.
  - Make the guards' Windows build pass: `GOOS=windows go vet ./internal/quality` fails at `quality_test.go:1862` (`syscall.Kill`). Find out why CI's Windows job misses it.
- A file's count falls in one of three ways:
  - its tests are rewritten as unit tests;
  - they move to the e2e tier in a PR whose unit tests keep the package's uncovered count flat (the ratchet enforces this);
  - they are deleted as redundant, under the same condition.
- While the list is non-empty, the coverage profile still includes those legacy tests. When it is empty, the profile is the unit tier's alone, and that is what task-20 gates.

### Task 25: Coverage worklists

**Id:** task-25
**Depends-On:** task-3
**Status:** planning
**Note:** Added by decision 23 (item 1). It runs in parallel with task-8, and every wave lane after it starts from its output.
**Verifies:**
1. A `wb coverage` mode reads a Go coverage profile, for example the `profile.cov` that `go-ci.yml` uploads, and the module's source. It prints every uncovered block with its file, line range, statement count and enclosing function.
2. It groups the blocks into units of a requested size, about 300 statements by default. Each unit takes whole functions and, where it can, whole files. It marks units that share a file, so the coordinator never runs them at the same time.
3. On a fixture profile the output is deterministic, and every uncovered block appears in exactly one unit.
4. The new flags have rows in `ai/capabilities.json` and lines in `docs/cli-flag-matrix.md`, and the new code is covered 100% (decision 1).

The unit list replaces exploration: a lane's brief carries its unit's blocks. The coordinator regenerates the lists from the latest `cov/integration` profile before cutting new units.

## Estimates

**Process rework, 2026-09-25 (decisions 23–24; an inference, not a measurement).**
- The measured cost before it: on 2026-09-25, about 7,600 API calls and 1.56 billion cache-read tokens, 81% of them in Sonnet lanes. Two lanes lived for hours at about 215k context with over 2,200 calls each, and 194 statements were newly covered on `main`.
- Expected after it: about 2 more weeks at 6 lanes, not 4–6. That assumes about 40 worklist units of ~300 statements and about 15 seam and refactor lanes (tasks 8–11, 22 and 23). At about 20M cache-read tokens per unit, reviews and fixes included, the remainder costs about 1 billion tokens. That is less than one day at the old rate.
- The earlier estimates below are kept for the record.

These are inferences, not measurements. The plan needs about 21 agent lanes over 3–5 calendar weeks, with at most two Go lanes at a time on the 4-core VM (three since founder decision 15). That is about 55–85k new test lines and roughly 100–200M tokens; no per-lane token data exists yet, so the token figure is a guess. The main risk is refactoring the landing and cleanup code agents use daily. It is mitigated by characterization tests first and a separate adversarial review per refactor.

**Rework, 2026-09-25 (inference).**
- Decisions 17–19 add work: task-8's interface cutover over 111 `exec.Command`/`exec.CommandContext` sites (origin/main 493ff751) plus the git helpers, task-22's move of most of the ~41,000 `cmd/wb` lines that are not flag parsing and dispatch, and task-23's e2e suite.
- They make the waves cheaper. Once code takes interfaces, the error branches that were 45% of the gap become one-line fake failures instead of contrived subprocess setups.
- Net: about 25 agent lanes over 4–6 calendar weeks, at up to three Go lanes.

## Open Questions

Open Questions 1–5 were answered on 2026-09-23 and are recorded as founder decisions 6–10.

Open Question 6 (e2e job cadence) was answered on 2026-09-25 and is recorded as founder decision 19.

## Research

The [research report](_research/REPORT.md) and its evidence are in `spec/plans/coverage-to-100/_research/` (copied from the coordinator session scratchpad, `wb-coverage/REPORT.md`, 2026-09-23):

- **PRs:** #554, #557, #559, #571, #646, #677.
- **Issues:** #504, #505, #539, #570, #582, #587, #620, #623.
- **Floor-only CI failures:** runs 35626416785, 35756625554, 35782829695–35800668724.

---
*This document follows the https://specscore.md/plan-specification*
