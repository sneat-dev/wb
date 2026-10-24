# Comparisons and experiments

`wb agent compare` answers "which execution configuration solves this task, at
what cost, with what quality" without trusting any single number.

## What is held equal

- the exact task bytes (the experiment records `task_sha256`);
- one base commit, pinned from the first worktree WB creates;
- the same validation suites and acceptance policy;
- a separate worktree per attempt.

What is **not** equal is recorded in `uncontrolled_differences`: harness tool
sets, context windows, provider load, and which harnesses can report usage or
cost at all. Read that list before drawing conclusions.

## How the run proceeds

1. WB resolves every profile's plan first. An unusable profile fails the whole
   command before anything is created.
2. Every attempt's agent ID is allocated and the immutable `experiment.json` is
   written. A crash still leaves an inspectable experiment.
3. Every worktree is created — sequentially, before any worker starts. The first
   created base SHA is pinned; an attempt whose base differs is recorded as
   `base_mismatch` and never started.
4. Attempts start in bounded batches (`--concurrency`) and the command waits for
   each batch.
5. `wb agent experiment <id>` re-reads the same document plus member records at
   any time.

## Reading the table

- **status** — execution status. `cancelled` and `timed_out` are not failures of
  the work.
- **validation** — objective checks; `skipped` is not a pass.
- **review** — acceptance. A comparison where nothing is accepted has no winner.
- **duration** — wall time of terminal attempts only.
- **tokens** — `n/a` means the harness reported none. Never treat it as zero.
- **cost** — only `harness-estimated`, `api-equivalent` values are shown; a
  subscription plan reports nothing.

Time and cost to an accepted solution are the metrics that matter. WB stores the
events needed to compute them later: per-attempt duration, validation and review.
