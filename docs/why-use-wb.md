# Why/when to use WB CLI? How it saves you time and money.

WB CLI is a delivery control plane for work that crosses repositories, worktrees,
GitHub checks, and releases. It does not make a compiler or GitHub Actions faster
by itself. It saves time by making the needed work explicit, running independent
steps together, preserving durable receipts, and stopping repetition when the
same fact has already been observed for the exact revision.

The practical promise is simple: spend people and CI time on a change once, then
carry forward the proof needed to publish, land, recover, or explain it.

The core idea is that delivery cost is larger than command runtime. It also
includes waiting, repeating the same proof, reading logs, recovering interrupted
work, coordinating parallel agents, and cleaning up afterward. WB is useful when
that coordination cost is material. Keep the fast feedback close to the edit;
move repeated, slow, and recoverable work into one governed pipeline.

## When WB helps

Use WB when a change needs one or more of these:

- an isolated, claimed worktree instead of a shared checkout;
- repeatable local commands with timing and admission control;
- exact-head CI evidence before a PR is merged or a release is accepted;
- several repositories or 3–7 independent streams that must converge;
- durable plans, checkpoints, receipts, or a safe resume after interruption;
- clear failure output that can be acted on without rediscovering the failing
  job.

For a small one-file change in one clean repository, ordinary Git plus that
repository's test command is often enough. WB should not be added merely to
wrap a command, and it cannot remove an inherently slow compile, integration
test, review, or external approval.

| Situation | Start with | Why |
| --- | --- | --- |
| One clean repository, one quick command, no parallel work | Ordinary Git and the repository command | Coordination costs less than introducing another lifecycle. |
| An agent may be interrupted or replaced | WB worktree and Work Log | The next agent can resume from recorded identities and receipts. |
| Several agents share one machine | WB worktrees plus governed runs | Isolation and CPU admission prevent checkout collisions and process stampedes. |
| A change crosses repositories or dependency levels | WB stream or dependency campaign | Publication order, batching, and downstream state stay explicit. |
| A PR must prove an exact head before landing | WB CI wait and receipt-backed landing | The merge decision uses remote evidence for the intended revision. |
| The same exact tree would be validated again | WB exact-tree receipt reuse | Reuse removes duplicate work while retaining the original proof. |

## The two useful journeys

### One person, one stream

1. Create a claimed worktree from a known target revision.
2. Run the focused local gate and record its outcome.
3. Push a reviewable branch and wait on the exact CI head.
4. Land only when the remote target receipt proves the intended integration.
5. Clean or deliberately recycle the worktree with the receipt still available.

The gain is fewer ambiguous states: a green local command is not mistaken for a
remote receipt, and an interrupted run can resume from its recorded identity.

### An orchestrator with 3–7 parallel streams

1. Give each independent change its own claimed worktree and branch.
2. Run focused local gates concurrently within the machine's budget.
3. Publish each finished branch, then collect exact-head CI receipts.
4. Batch compatible finished changes into one current-target integration instead
   of repeatedly refreshing and testing each branch in isolation.
5. Keep failed or uncertain streams separate, with their diagnostics and
   checkpoints intact.

The gain is throughput and lower coordination load. The orchestrator can see
which stream needs attention without sharing one mutable checkout or treating a
chat update as proof that a remote action completed.

## Where the savings come from

| Resource | What WB changes | Honest boundary |
| --- | --- | --- |
| Human time | Reusable receipts and compact failure evidence remove repeated status checks and rediscovery. | It does not replace code review or product decisions. |
| CI runner time | Focused validation and exact-tree reuse avoid builds that prove the same tree again. | Savings depend on unchanged inputs and the workflow's reuse contract. |
| Tokens | Deterministic plans, receipts, and extracted failures give an agent a small factual input instead of a full log or repeated investigation. | No token count is claimed here; the reduction is qualitative until measured. |
| Cognitive load | Claims, checkpoints, and exact target/head identities make ownership and recovery visible. | The team still needs to choose scope, quality gates, and landing authority. |

Money follows from measured avoided usage, not from a universal WB price claim:

```text
estimated saving =
  avoided CI runner hours × the user's runner rate
  + avoided attended waiting hours × the user's actual labor rate
  + avoided agent tokens × the user's actual model rate
```

Keep those inputs separate and show them beside the estimate. The current
evidence supports time and compute reductions; it does not yet support one
currency figure that applies to every user, machine, repository, or model.

## Maintained evidence

Only measured results or clearly marked estimates belong here. Add the command,
receipt, date, and limitation when refreshing a row; do not turn one sample into
a fleet-wide claim.

| Date | Class | Evidence | Result | Traceable source | Limitation |
| --- | --- | --- | --- | --- | --- |
| 2026-09-04 | Baseline opportunity | Local WB push validation | 501 push attempts consumed about 23.4 machine-hours in 30 days; p50 94.2 seconds, p90 422.8 seconds, p95 602.4 seconds | [Agent SDLC Throughput measured baseline](../spec/features/agent-sdlc-throughput/README.md#measured-baseline) | Historical local activity; mixes repositories and change sizes and does not equal human attention time. |
| 2026-09-04 | Baseline safeguard | Local WB commit checks | p50 76 milliseconds, p90 407 milliseconds, p95 787 milliseconds | [Agent SDLC Throughput measured baseline](../spec/features/agent-sdlc-throughput/README.md#measured-baseline) | Historical local activity; supports keeping focused formatting and static feedback near edits, not removing it. |
| 2026-09-04 | Baseline opportunity | Laptop and VM lifecycle completion | Laptop: 2,126 claims, 84.57% sealed. VM: 383 claims, 78.07% sealed. | [Agent SDLC Throughput measured baseline](../spec/features/agent-sdlc-throughput/README.md#measured-baseline) | A sealed claim is lifecycle evidence, not proof that the delivered change was valuable. |
| 2026-09-06 | Mechanism evidence | Focused WB tests | Under 1.5 seconds | Named focused test receipts summarized in the [feature evidence](../spec/features/agent-sdlc-throughput/README.md#measured-baseline) | Local developer-machine samples; not comparable to a full GitHub Actions matrix. |
| 2026-09-06 | Realized local reuse | Focused Go coverage cache reuse | First covered package run: 0.319 seconds. An identical second run with a different coverprofile path reported `(cached)` after WB removed its default `-count=1`. | Executable contract: [`TestGoCoverageArgumentsKeepTestResultCacheEnabled`](../internal/quality/go_test_shards_test.go) | One package and local toolchain; cache reuse depends on unchanged source and does not predict full-suite or remote CI timing. |
| 2026-09-06 | Remote cost sample | Broad WB PR CI | Required checks passed in 4 minutes 54 seconds; coverage dominated at 4 minutes 35 seconds. | [GitHub Actions run 33918379694](https://github.com/sneat-dev/wb/actions/runs/33918379694) | One workflow/run shape; queue time and cache state can change it. |
| 2026-09-06 | Realized remote reuse | Sneat Go exact-tree validation reuse | Go CI wall time fell from 9m33 to 5m01, about 47%; aggregate runner time fell from 21m28 to 8m01, about 63%. | Baseline `main` SHA `7286108c4e5dc40b14309a196c9d446c5ff51418`: [Go CI](https://github.com/sneat-co/sneat-go/actions/runs/34016094340), [deploy](https://github.com/sneat-co/sneat-go/actions/runs/34016497358). Result merge SHA `3649e98ac974c5049fdfbd6ecfa51584c4017c3a`: [Go CI](https://github.com/sneat-co/sneat-go/actions/runs/34019703409), [deploy](https://github.com/sneat-co/sneat-go/actions/runs/34019923016), [PR #1070](https://github.com/sneat-co/sneat-go/pull/1070). | One repository and workflow configuration. The exact-tree reuse path skipped lint, tests, coverage, Coveralls, and Java/Node/Firebase setup, but workflow changes or external cache state may also affect timing. |
| 2026-09-06 | Baseline opportunity | WB landing inventory | About 30–39 seconds per repository-wide worktree inventory; one redundant third scan cost about 28 seconds. | Private landing Work Log timings summarized in the [feature evidence](../spec/features/agent-sdlc-throughput/README.md#measured-baseline) | Inventory cost varies with worktree count and storage performance. |
| 2026-09-06 | Scale and correctness evidence | Five-organization merge-policy rollout | 159 repositories inspected; volatile GitHub metadata fingerprinting was exposed and fixed. | Durable report `tooling-friendly-five-orgs-apply-v01092-20260906/merge-policy.json` and [Agent SDLC Throughput feature](../spec/features/agent-sdlc-throughput/README.md) | Scope was selected organizations, not the entire GitHub account; this is not a measured saving. |
| 2026-09-06 | Capability evidence | Durable daemon handoff | Sandbox bridge, durable queue, and restart handoff demonstrated; progress cadence set to 10 seconds. | [WB PR #424](https://github.com/sneat-dev/wb/pull/424) | This establishes recoverability and visibility, not an aggregate speed percentage. |

The current operational contracts are documented in the [CLI flag
matrix](cli-flag-matrix.md) and the machine-readable
[capability inventory](../ai/capabilities.json). Those documents describe what
the commands promise; this page records whether a concrete run produced a
measurable benefit.

The [Agent SDLC Throughput feature](../spec/features/agent-sdlc-throughput/README.md)
is the detailed measurement and product-decision source. Update that evidence
first, then update the corresponding narrative and reusable proof point here.

## Reusable proof points

Use the evidence with its limit attached. These are starting points, not
marketing claims that apply to every repository.

| Surface | Self-contained starting point |
| --- | --- |
| Landing page | “In one Sneat Go workflow, exact-tree validation reuse cut Go CI wall time from 9m33 to 5m01 and aggregate runner time from 21m28 to 8m01. That is one repository result, valid only when the landed tree and receipt match exactly.” Link [Sneat Go PR #1070](https://github.com/sneat-co/sneat-go/pull/1070). |
| Article | “A 30-day local scan found 501 WB push attempts consuming about 23.4 machine-hours, while focused commit checks stayed below 787 milliseconds at p95. The opportunity is to retain fast edit feedback and remove repeated broad validation; this historical sample mixes repositories and change sizes.” Link the [measured baseline](../spec/features/agent-sdlc-throughput/README.md#measured-baseline). |
| Tweet | “One unchanged covered Go package ran in 0.319s, then returned `(cached)` even with a different coverprofile output. WB stopped adding `-count=1` by default; this proves local cache reuse for that package, not a fleet-wide speedup.” Link the [executable contract](../internal/quality/go_test_shards_test.go). |
| YouTube | “The WB workflow gives 3–7 parallel streams isolated worktrees, bounded machine capacity, exact-head CI receipts, and one integration path. Demonstrate the full journey; do not attach a speed percentage until the run records it.” Link the [Agent SDLC Throughput feature](../spec/features/agent-sdlc-throughput/README.md). |
| TikTok script | “We found one WB landing scanned the same repository-wide worktree inventory a third time, costing about 28 seconds. The lesson is measurable: optimize repeated coordination steps after proving they are redundant. Storage and worktree counts change the result.” Link the [measured baseline](../spec/features/agent-sdlc-throughput/README.md#measured-baseline). |

## How to keep this page credible

1. Record the exact revision, command, time window, and whether the sample was
   local or remote.
2. Keep wall time, runner aggregate, queue time, and human elapsed time in
   separate columns. They answer different questions.
3. Link a durable receipt or pull request where it is safe to do so.
4. State the comparison's limits beside the number.
5. Remove or qualify a proof point when the workflow, hardware, or policy that
   made it true changes.

WB earns its place when these records show that it eliminates a repeated,
deterministic cost while keeping the delivery decision visible and reversible
until the final remote action.
