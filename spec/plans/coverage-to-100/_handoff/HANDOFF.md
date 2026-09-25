# Coverage-to-100 handoff: VM coordinator → MacBook coordinator

Written 2026-09-25 by the VM coordinator session (Opus 5.5). This directory, `spec/plans/coverage-to-100/_handoff/` on `cov/integration`, is the handoff and it's yours now. The VM has an identical copy at `/home/ai/.wb/reports/coverage-handoff/`, plus the 1.7 MB baseline JSON, which is kept out of git.

## Why the move

The founder said: "Maybe we should move this work to my MacBook m5 max it has 36gb memory". Then: "We can get majority on Mac and then finish on vm. Also Mac has ssh access to this vm so can run tests remotely? Actually that would allow us to drive work across both Mac and vm?" Then: "Yes, organize work movement from vm to Mac."

The VM has 4 CPUs and 8 GB of RAM. Targeted test runs take 5–10 minutes there, and lanes are capped at 3.

## Source of truth

- The plan is `spec/plans/coverage-to-100/README.md` on branch `cov/integration`. Read decisions 13 and 17–21, the task list and the wave rules.
- Two edits are not yet in the plan text:
  - Decision 22.
  - A queue of small edits in `plan-next-edits.md`, in this directory.

  Apply both in one plan commit on `cov/integration`. Quote the founder verbatim, and label your own rules as plan choices.
- Coverage is measured by CI on Linux: go-ci's coverage job, plus the per-change ratchet on PRs. Local numbers on macOS are only guidance.

## Founder decisions in force

- **Decision 17: two test tiers.** Unit tests with fakes, via task-8's runner and git ports, are the only way to reach 100%. The real-git e2e tier (`//go:build e2e`, `TestE2E*`/`TestContract*`) must pass but never counts toward coverage.
- **Decision 18: thin `cmd/wb`** (task-22).
- **Decision 19: e2e runs on every PR.**
- **Decision 20: e2e covers happy paths** plus a closed list of failure cases.
- **Decision 21: integration branch.**
  - Lanes branch from `cov/integration` and open no PRs.
  - The coordinator merges each approved lane branch locally with `--no-ff` and pushes one lane per push.
  - ONE standing PR, `cov/integration` → `main` (currently **#769**), runs full CI on every push. Keep it green, and fix forward before the next merge.
  - Land it with `wb pr land` (merge commit) at least daily and at each wave's end. `wb pr land` deletes `cov/integration`: recreate it from `main`. The next standing PR opens on the first lane merge; `merge-to-int.sh` does this.
- **Decision 22** (founder, verbatim): "From my perspective if we optimize process we probably also will have less token usage. Maybe we should have sonnet review on feature branches and opus on integration branch before merging to main?"
  - Lane branches get a Sonnet adversarial review.
  - The batch PR into `main` gets an Opus adversarial review.
- **Standing constraints:**
  - Merge commits only.
  - Forbidden: `coverage:ignore`, build tags that hide production code, deleting behaviour, lowering floors, `--no-verify`, `hooksPath` changes, sleeps or retries as flake fixes.
  - Never `git stash`: the stash stack is shared.
  - The founder has approved autonomous work ("drive to completion — you have my approvals").

## Token discipline (from the 2026-09-25 audit)

Today's usage: about 7,600 API calls and 1.56 billion cache-read tokens, against 1.0M output tokens. 81% came from Sonnet implementation lanes. Two of them lived for hours, at about 215k context, with over 2,200 calls each. The result was only 194 statements newly covered on `main`. Cost grows with calls × context, so:

- **One fresh agent per work unit.** Retire it at handover. A review fix goes to a new agent that gets only the review file. Aim for a 40–80k context.
- **Self-contained briefs.** Give the exact uncovered blocks (from the CI coverage profile), the files and the helpers to use. No exploration.
- **Keep output small.** No heartbeats. No "waiting on tests" reports; lanes report only at handover. Keep command output short (`| tail`) and read files by line range.
- **You (coordinator):** restart with a compact handoff once your own context grows large.

## Where coverage stands

`main` e0dcfda6 is at **88.42%**: 91,662 statements, 10,610 uncovered. The per-package baseline is in `coverage-baseline-e0dcfda6.json`. It's on the VM only, in `/home/ai/.wb/reports/coverage-handoff/`. It's the same data as CI's `wb-coverage-baseline` artifact for e0dcfda6.

- 86% of the gap is in three packages:

  | Package | Uncovered statements |
  |---|---:|
  | `internal/worktrees` | 4,585 |
  | `cmd/wb` | 2,784 |
  | `internal/orchestrate` | 1,799 |

- The next group: layout 148, sessionmove 138, deps 107, sessionlaunch 84, hooks 77, sessionpark 65, locallink 55, agents 54, streams 54, migrate 53, daemon 50, lifecyclehooks 48, session 46, wbconfig 44.
- 44 small packages have 30 or fewer uncovered statements, 344 in total. 37 of 100 packages are at 100%.
- **Critical path:**
  - task-8, the runner, git/gh ports and fakes, unlocks `worktrees` and `orchestrate`.
  - task-22, thin `cmd/wb`, unlocks `cmd/wb`, but only after task-5 PR-4 lands, since both rewrite the same files.

  Run them in parallel with the Mac's extra lanes.

## Branch state at handover

| Branch | Head | Status |
|---|---|---|
| `cov/integration` | 1cc1dc19 | Holds t9 PR-7, t24 PR-1 and a coordinator fix of `cmd/wb/release_contract_test.go` (still to be reviewed in the batch review). **#769 is red**: the ratchet flags `cmd/wb/ci.go:314` and `internal/orchestrate/ciwait.go:146-147`. |
| `cov-fix-769` | **5875404b**, fixes pushed (the fixer's notes, and one decision for you, are in `STATUS.md`) | Fixes #769. The Opus review found the new cmd/wb test ran real git through `runCommand`, which the detector doesn't know, so its 31 git processes counted as 0. A fresh Sonnet agent is fixing this on the VM: comparator-parameter unit test, delete that test, teach the detector `runCommand`/`installFakeGH`, regenerate `unit_tier.pending`. Review: `reviews/review-fix-769.md`. |
| `cov-t9-pr8` | **4e66aba5**, fixes pushed | task-9 PR-8 (23 filewrite sites). The fixer reports the first review's B1 fixed with a `syncDirectoryInjected` shim, which is a no-op on Windows. It also reports B2 fixed: a new locallink test, and `wbconfig/remote.go` reverted to `_ = temporary.Close()`. N1, N4, N6 and N7 are done; N2, N3 and N5 were left out. The fixer's claims are unverified. **Next: Sonnet re-review against `reviews/review-t9-pr8.md`, then merge.** `execfile.WriteExecutableFile` is deferred to a "PR-10" that the plan doesn't list yet. task-9 also has **PR-9 (16 sites)** left. |
| `cov-t5-ctx-4` | **837ff178**, fixes pushed | task-5 PR-4 (old PR #760, which targets main; close it after the merge). Review B1–B5 in `reviews/review-t5-pr4.md`. B2 was serious: tests reached the real `~/projects/.wb`. The lane committed fixes for B1–B5 and then stopped before its own verification run finished; I (VM coordinator) pushed the commit as it was. **Unverified**: PR #760's CI on GitHub is its first full run. **Next: Sonnet re-review, with checks that B2's leaks are gone, then merge.** The lane is retired. |
| `cov-t8-pr1` | d90945a5 | task-8 PR-1 WIP: the runner, runnertest, the runtime guard, the gitcli skeleton, and `exec_sites.pending` generation (146 matches, 67 files) as the next step. Paused while its lane did the #769 fix. |
| `cov-t9-pr7`, `cov-t24-pr1` | — | Already merged into `cov/integration`. Delete them once the batch reaches `main`. |

**Landing owner: the Mac, from now on.** The founder said: "Do not start new tasks on this vm. I'll start work on Mac once handoff is ready". The VM coordinator merges nothing more, starts no reviews and starts no lanes. No agents are running on the VM now. The last one, the `cov-fix-769` fixer, finished and pushed; `STATUS.md` has its notes.

**First moves on the Mac, in order:**

1. Set up the Mac (next section).
2. **Review the plan for process speed and token cost** (the founder's request, section below). Do this before step 7 starts any new lanes. Steps 3–6 need no new process and may run alongside it.
3. Sonnet re-review of `cov-fix-769` after it's done, then `merge-to-int.sh`. Wait for #769 to go green. That first merge also tests the Mac setup end to end.
4. Sonnet re-review of `cov-t9-pr8`, then merge. Then `cov-t5-ctx-4`: merge, then close #760.
5. One plan commit: decision 22 plus `plan-next-edits.md`. **The same commit deletes this `_handoff/` directory.** It's temporary coordination state and shouldn't reach `main` of this public repo; git history keeps it.
6. Opus batch review of #769. It covers my unreviewed `1cc1dc19`: check it against `.github/workflows/go-ci.yml`. Then run `land-when-green.sh` and recreate `cov/integration`.
7. Start lanes: the task-8 PR-1 continuation from `cov-t8-pr1` (the critical path), task-9 PR-9, and task-22 once task-5 is on `main`.

Before step 7, ask the founder how many concurrent lanes the Mac may run. The founder's cap of 3 lanes was set for the 4-core VM.

## Mac setup (step 1)

- **Go 1.27**, as in `go.mod` (`go 1.27.0`).
- **golangci-lint** at the same source commit CI uses; release v2.12.2 panics on Go 1.27:
  `go install github.com/golangci/golangci-lint/v2/cmd/golangci-lint@1b907273167eec8b3d84cae636b7798ced4433d5`
- **wb** built from `main` of sneat-dev/wb: `go install ./cmd/wb` in the canonical clone. Get the clone with `wb sync --filter sneat-dev/wb`.
- **gh 2.99 or newer**, logged in, with push and merge rights on sneat-dev/wb. `wb pr land` needs a recent gh.
- **git hooks:** leave wb's hooks active. Never bypass them.
- **A helper worktree** for `merge-to-int.sh`, on a local branch such as `cov-int-setup`. Export `COV_INT_WT=<its path>` and `WB_CLONE=<canonical clone>`.
- **SSH to the VM.** Check that `ssh <vm> /home/ai/.local/bin/cov-linux-test cov/integration -run TestSyncDir ./internal/filewrite` prints `ok` and `exit 0`. Use the full path, because non-interactive SSH has a bare PATH.
- **`caffeinate -dims`** for the whole run, so macOS doesn't sleep in the middle of a lane.

## Founder request: review the plan for speed and token cost (step 2)

The founder, verbatim: "Add to handoff request to review current plan with focus on process optimization with goal of decreasing both time to completion and tokens usage." For context, earlier the same day: "3-5 weeks is way to much - I can't afford this." And: "From my perspective if we optimize process we probably also will have less token usage."

Read the whole plan with fresh eyes, alongside the token audit above and the coverage concentration. Then send the founder a **short proposal**: the changes, and for each one the estimated effect on calendar time and on tokens. The founder approves it before any plan text changes. Ask one question at a time, in plain words.

The standing constraints and decisions 17–21 are not up for change unless the founder reopens them. Questions worth answering:

- **Critical path.** Can task-8 (runner and git/gh fakes) go first and alone, so that the `worktrees` and `orchestrate` waves (6,400 statements) become mechanical? Which tasks in the plan block nothing and can wait?
- **Worklists.** Can each coverage wave be generated from CI's `wb-go-coverage` profile as per-file lists of uncovered blocks, so a lane gets a list and needs no exploration? Can one script do this?
- **Unit size.** How big should a lane be? Too small and fixed overhead dominates (worktree, review, merge, CI); too large and context grows. Today's data point: two 2,200-call lanes produced 194 statements.
- **Reviews.** Are two review layers (Sonnet per lane, Opus per batch) right for mechanical test-only lanes? Could a mechanical gate replace the Sonnet review for pure-test diffs: ratchet green, unit-tier detector, `ci audit --strict`, mutation check?
- **Where tests run.** How much of local and VM test time could move to GitHub Actions via `workflow_dispatch`? How much does the Mac's CPU change the answer?
- **Parallelism.** How many lanes, and which work runs in parallel without file conflicts? For example, `cmd/wb` waits on task-5, then task-22.
- **Remaining non-coverage tasks.** Are any of them larger than the coverage they unlock?

## How a lane runs

1. `wb worktree create <task> --base cov/integration --model <exact model id> --original-prompt-file <file>`.
2. The lane gets a self-contained brief: task, exact scope, files, the uncovered blocks, the checklist below, and "push, report the sha, stop".
3. Tests run locally on the Mac. **Linux-only code** runs on the VM:
   `ssh <vm> /home/ai/.local/bin/cov-linux-test <pushed-branch> -run '<regex>' ./internal/<pkg>`
   - It uses a separate clone per slot, 3 slots, with CPU admission through `wb run`, a throwaway HOME, and umask 022.
   - Env knobs: `TIMEOUT` (seconds), `TAIL` (lines).
   - Packages with Linux-only production files: `internal/worktrees` (10 files), `internal/session`, `internal/daemon`, `internal/process`, `internal/hostload`, `internal/hooks`, `internal/filewrite`, `internal/unixcompat`, `internal/agents`.
   - Windows: `GOOS=windows go vet <pkgs>`.
4. A Sonnet review of the lane diff against `origin/cov/integration`. The review file's first line is `Reviewed-Head: <full sha>` only when it approves.
5. `COV_INT_WT=… merge-to-int.sh <branch> <review-file> [pr#]` does the rest:
   - refuses unless the review's `Reviewed-Head` equals the branch head;
   - resets the helper branch to `origin/cov/integration` (wb hooks refuse commits on a detached HEAD);
   - merges with `--no-ff` and pushes;
   - opens the standing PR if none is open;
   - closes an old lane PR if you give its number.

   To land a batch: `WB_CLONE=… land-when-green.sh <pr#> <opus-review-file>`, then recreate the branch: `git push origin origin/main:refs/heads/cov/integration`.
6. After the batch lands on `main`: `wb worktree end <task> --apply`, `wb worktree gc`, and delete remote lane branches that `main` already contains.

## Pre-handover checklist (every recurring review finding from 2026-09-25)

- Error text is byte-identical to before a refactor. Fault tests use `errors.Is`, and check that no temp file is left behind.
- Every added or changed statement is covered, including restore and error returns. Mutation-check by reverting each change via `go test -overlay`: the test must go red.
- Unit tests start **no processes**:
  - no real git or gh;
  - no `/usr/bin` on PATH, which exposes the real `gh`;
  - no wrapper tricks. The task-24 detector plus `unit_tier.pending` and `unit_tier.allow` enforce this: counts must match exactly, and hiding matches is evasion.
- Tests never touch the real HOME, `~/.wb` or `~/projects`, and never write into the source tree. Always build invocations with an explicit projects root.
- Paths are chosen by the test, never by a clock: exact call counts, and trigger contexts rather than sleeps.
- Mode checks don't depend on umask (the VM's umask is 002; see #770).
- `GOOS=windows go vet` is clean. Keep Unix-only syscalls such as Mkfifo in `//go:build unix` test files.
- If `.github/workflows` changed, run `go test -run TestGoCI ./cmd/wb`: `release_contract_test.go` pins the go-ci structure.
- Run `go run ./cmd/wb ci audit . --target cov/integration --strict` and the `internal/quality` guards: `TestUnitTier*`, `TestParallelBaselineDoesNotRegress`, and the filewrite-boundary guards.
- Never run `wb coverage` or whole-module tests on the VM.

## Known issues

- **#765:** `TestSelfHostedBenchWholeJourney` fails on the VM only.
- **#766:** ciwait coverage varied between runs; mostly fixed, with `cov-fix-769` finishing it.
- **#770:** lifecyclehooks tests fail under umask 002.
- **Detector evasions** are listed as TODOs in `internal/quality/unittier.go`.
- **Stray lock directories:** `/home/ai/projects/.wb/worktrees/deps-npm-publish-cwfixture` and `npm-publish-claim-6ed7ed808c5ba937` on the VM were created by a test. Delete them after task-5's B2 fix merges.
