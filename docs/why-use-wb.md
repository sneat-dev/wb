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

## Maintained evidence

Only measured results or clearly marked estimates belong here. Add the command,
receipt, date, and limitation when refreshing a row; do not turn one sample into
a fleet-wide claim.

| Date | Evidence | Result | Method/source | Limitation |
| --- | --- | --- | --- | --- |
| 2026-09-04 | Local WB push validation | 501 push attempts consumed about 23.4 machine-hours in 30 days; p50 94.2 seconds, p90 422.8 seconds, p95 602.4 seconds | Read-only WB hook-event scan recorded in the Agent SDLC Throughput feature | Historical local activity; mixes repositories and change sizes and does not equal human attention time. |
| 2026-09-04 | Local WB commit checks | p50 76 milliseconds, p90 407 milliseconds, p95 787 milliseconds | The same read-only hook-event scan | Historical local activity; supports keeping focused formatting and static feedback near edits, not removing it. |
| 2026-09-04 | Laptop and VM lifecycle completion | Laptop: 2,126 claims, 84.57% sealed. VM: 383 claims, 78.07% sealed. | Redacted WB Work Log analysis | A sealed claim is lifecycle evidence, not proof that the delivered change was valuable. |
| 2026-09-06 | Focused WB tests | Under 1.5 seconds | Focused local WB test command recorded during the SDLC work | Local developer-machine sample; not comparable to a full GitHub Actions matrix. |
| 2026-09-06 | Focused Go coverage cache reuse | First covered package run: 0.319 seconds. An identical second run with a different coverprofile path reported `(cached)` after WB removed its default `-count=1`. | One fresh `GOCACHE`, then two focused covered-package runs with unchanged source and distinct coverprofile outputs. | One package and local toolchain; cache reuse depends on unchanged source and does not predict full-suite or remote CI timing. |
| 2026-09-06 | Broad WB PR CI | About 5–6.5 minutes | One observed broad GitHub PR CI cycle | One workflow/run shape; queue time and cache state can change it. |
| 2026-09-06 | Sneat Go exact-tree validation reuse | Wall time about 47% lower; aggregate runner time about 63% lower | Earlier baseline: Go CI 9m33, end-to-end 15m37, aggregate 21m28. Reuse result: 5m01, 8m13, 8m01. See [Sneat Go PR #1070](https://github.com/sneat-co/sneat-go/pull/1070). | One repository and workflow configuration; reuse is valid only for the exact landed tree and receipt contract. |
| 2026-09-06 | WB landing inventory | About 30–39 seconds per repository-wide worktree inventory; one redundant third scan cost about 28 seconds | Landing Work Log timing samples | Inventory cost varies with worktree count and storage performance. |
| 2026-09-06 | Five-organization merge-policy rollout | 159 repositories inspected; volatile GitHub metadata fingerprinting was exposed and fixed | WB merge-policy rollout report | Scope was selected organizations, not the entire GitHub account; this is a correctness finding as well as a scale sample. |
| 2026-09-06 | Durable daemon handoff | Sandbox bridge, durable queue, and restart handoff demonstrated; progress cadence set to 10 seconds | WB daemon/handoff receipts | This establishes recoverability and visibility, not an aggregate speed percentage. |

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

| Surface | Reusable line | Evidence to link or show |
| --- | --- | --- |
| Landing page | “Reuse a successful validation for the exact tree that landed, instead of rebuilding the same code.” | The Sneat Go before/after timings and exact-tree receipt. |
| Article | “A worktree is useful only when its branch, owner, target, and recovery record stay together.” | A claimed-worktree journey and durable checkpoint example. |
| Tweet | “One exact CI receipt beats a chain of ‘looks green’ messages.” | Exact head/target receipt and bounded failure detail. |
| YouTube | “Watch several streams finish without sharing one checkout; integrate compatible work once.” | The 3–7 stream journey and target-refresh avoidance. |
| TikTok script | “The slow part is often repeating proof, not writing the fix.” | Focused local gate versus a broad CI cycle, with the stated limits. |

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
