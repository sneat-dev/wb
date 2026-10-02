# WB CI below ten minutes: read-only evidence and recommendation

Inspected checkout `cd0361f86fd641cc62123e3e55f2ecc96d917d45` on local macOS, 2026-10-02. No tests, Go commands, source edits, index refresh, publication or landing performed. Latest fix run 36968224792 was still running at observation; this analysis does not claim that fix passed.

## Recommendation

Keep one repository/module initially. Instrument and distribute coverage over independent Actions runners; do not split repositories solely to obtain a ten-minute gate. The latest completed successful main sample spends almost fifteen minutes waiting for coverage while every other required Go lane is below four minutes. A repository split retains the worktree/orchestration tests that dominate the gate and adds versioning/integration work. Tier parallelism alone may be insufficient: successful logs do not reveal whether the unit tier already exceeds nine minutes.

Define the target as commit-to-required-validation p95 under ten minutes. Publication/signing/install verification is a separate metric unless the user intends ten minutes through release completion.

## Measured evidence

Authoritative sources: GitHub Actions jobs API and archived job logs, not `watch` exit status. Successful main `ffab59bd` [run 36965072501](https://github.com/sneat-dev/wb/actions/runs/36965072501):

| Item | Observed time |
|---|---:|
| Creation to required aggregate completion | 14m59s |
| Coverage job | 14m34s |
| Coverage test step | 14m10s |
| E2E job / test step | 3m36s / 3m19s |
| Race job / test step | 2m33s / 2m15s |
| Lint job / lint step | 3m04s / 2m42s |
| Build/vet job | 1m30s |
| Full workflow including release | 20m34s |
| GoReleaser step after required aggregate | 4m17s |

Coverage succeeded at 96,739 / 101,147 statements (95.641986%), one attempt. Full successful logs suppress the individual successful shard output/timings; no measured unit/native split is available. Do not derive native coverage time from the independent E2E job, because coverage instruments `-coverpkg=./...`.

PR `ac7e3942` [run 36967188278](https://github.com/sneat-dev/wb/actions/runs/36967188278) failed: coverage job 12m13s, E2E 4m05s, lint 2m47s, race 2m12s, build/vet 1m30s. E2E had a fixture failure; coverage separately failed five repository guard tests (two filewrite-exemption guards, exec_sites.pending guard, executable WriteFile guard, and stale paralleltest_baseline guard). These coverage failures are independent of the subsequently fixed inherited working-directory fixture. Full diagnostics are in `/private/tmp/wb-ac7-coverage-diagnostics/`; bounded precise names/locations in `/private/tmp/wb-ac7-coverage-failures-excerpt.txt`. Its duration is not a successful performance sample. Earlier PR run 36966371120 was cancelled by superseding revision after approximately eleven minutes of coverage; that is a censored observation, not a timeout or successful duration. A 22-second successful docs-scoped main run also must not enter the Go-relevant timing sample.

Local publication admission waiting (111 seconds behind two other test jobs) is a separate local queue cost and is not part of Actions execution.

## What the implementation actually does

- `.wb/quality.yaml` specifies four process shards each for `internal/worktrees` and `internal/orchestrate`, plus one unsharded remainder job. The workflow's “8 shards” title is not eight GitHub runner jobs.
- `internal/quality/go_coverage_runner.go:320` runs that pool on one runner, capped at `min(requested shards, jobs, GOMAXPROCS-1)` with a floor of one worker. Its effective GOMAXPROCS was not logged; do not assume all four workers were active.
- `internal/quality/go_test_shards.go:23` sorts top-level test names and assigns round-robin. It balances test counts, not duration, and cannot divide one slow top-level test.
- The failed `ac7` diagnostics manifest records the unsharded remainder at 615.833 seconds (10m15.833s). This is a failed-run measurement, not a successful p95; it does show that parallelizing only unit/native tiers cannot guarantee the ten-minute gate while leaving that remainder unchanged.
- `runCombinedCoverageWithOptions` executes the unit profile first, then native E2E/contract profile serially, then merges them. Both instrument all selected packages. The separate required E2E job runs those native journeys again without coverage, under the documented existing pass/fail contract.
- The PR ratchet uses an exact merge-base artifact with matching native-tier mode, otherwise it measures that baseline itself (15-minute budget) before measuring head (20-minute budget). A missing or unusable baseline can double work. The inspected `ac7` log had no fallback warning; latest fix's fetch step finished in six seconds, but step completion alone does not prove baseline compatibility.
- Main always reproduces coverage/baseline, even when an exact PR receipt lets other checks reuse validation. This is deliberate current policy; safe reuse would require a head/tree, source identity, coverage-mode and provenance proof, especially across a GitHub merge commit.
- The required race job covers five concurrency-bearing package families and completed in 2m33s. The separate nightly full-race workflow has historical 40–48 minute heavy-package comments and does not delay/authorize publication. It is not the present PR critical path.
- `cmd/wb/cli_smoke_test.go` already builds the CLI once per test process. `cmd/wb` intentionally stays unsharded to avoid multiplying global/process setup. Git seed fixtures also already exist. Measure repeated fixture/build costs before recommending a broad rewrite or increasing process count.

## Minimum useful experiments, preserving coverage

1. Record successful unit/native and per-job elapsed times, discovered test membership, effective GOMAXPROCS, fixture/CLI compilation costs, and cache-hit status. Reuse existing runner progress/elapsed fields rather than introducing a second test harness. Persist successful timings, not only failed diagnostics.
2. Run unit coverage and native coverage in parallel Actions jobs, then merge exact profiles and enforce the unchanged minimum, changed-line/per-package ratchet, and failed-test result. Keep the independent E2E contract initially. This removes their serial sum; it does not guarantee ten minutes if unit alone exceeds the target.
3. Distribute heavy process shards over separate runners, plus a measured remainder lane. Balance shard membership using measured test durations, preserving a deterministic manifest and proving every discovered top-level test appears exactly once. Native-tier distribution needs its own discovery under the E2E build tags and its own completeness manifest. A long single top-level fixture/test may still need a focused structural change. Merge only matching source block identities and coverage modes; reject missing shard artifacts and failures. Instrumentation must preserve cross-package hits—simply using a narrower `-coverpkg` is not an equivalent optimization.
4. Ensure exact compatible main baseline artifacts are available before PR ratchets, and avoid measuring base twice. Consider exact-tree PR-profile reuse on main only after provenance is designed and checked. This lowers repeated runner-minutes and improves missing-baseline tails without weakening the ratchet.
5. Only if the measured unit/native tails remain too large, compare a larger runner against distributed shards. More processes on the same runner may worsen contention. Avoid preemptively optimizing the 1–3 minute build/lint/race lanes.

Expected benefit is an experiment hypothesis, not a promise: parallel tiers change roughly `unit + native` to `max(unit,native)` plus merge/setup; distributed runners can remove local worker queueing and CPU contention. Costs increase in runner-minutes, cache/artifact setup and profile assembly. Aim for each test lane below roughly eight minutes to leave checkout, queues, baseline fetch and aggregate headroom. The observed coverage test step needs at least 4m10s reduction to reach a ten-minute test step. The actual creation-to-required-aggregate target needs at least 4m59s reduction from the measured 14m59s gate, plus further margin for p95 headroom.

## Architecture options and cost

CodeGrapher status reports 538 added, 187 modified, 36 removed pending files; rebuilding was outside this read-only task. Current source import scan is therefore used for boundary evidence (not stale graph output). Counts below include unique WB-local imported packages across production Go files recursively, omit tests, and are coupling indicators rather than a dependency DAG proof.

| Area | Production files | WB-local imports | Boundary |
|---|---:|---:|---|
| worktrees | 104 | 36 | Session, Git, claims/journal/security/layout/landing helpers |
| orchestrate | 41 | 20 | Imports worktrees, quality, Git, session, streams |
| cockpit | 47 | 21 | Imports daemon, worktrees, sessions, remote/fleet services and embedded web |
| daemon | 13 | 5 | filewrite, generated protocol, process, runqueue, wbhome |
| cmd/wb | 134 | 88 | Composes all four areas plus hub and CLI services |

- **Package boundaries in one module:** cheapest; preserve atomic refactors, single release and current internal visibility. Independent CI lanes do not require a module or repository split. Best first move for this latency objective.
- **Multiple modules in one repository:** module boundaries alone do not prevent `internal` reuse. Go checks the import-path prefix above `internal`: sibling modules with importer paths under `github.com/sneat-dev/wb/...` may still import `github.com/sneat-dev/wb/internal/...`. Changing consumers to paths outside that prefix would require moving shared packages or exposing contracts. Independent versioning may also motivate stable APIs even where imports remain legal. See the [official Go command documentation](https://go.dev/cmd/go/?m=old#hdr-Internal_Directories) and [module-aware loader implementation](https://go.dev/src/cmd/go/internal/load/pkg.go). Multiple modules add module graph/tidy/workspace and consumer-integration checks; they do not automatically shorten heavy tests.
- **Two repositories:** core worktree/orchestration CLI and daemon/cockpit integration would be the likely product split, but cockpit currently reads worktrees/session directly and CLI embeds web/builds all features. If new module import paths leave the `github.com/sneat-dev/wb` prefix, a shared public library or protocol/client boundary must replace the current internal imports; preserving that prefix can keep imports legal but does not remove release/API coupling. Keep worktrees and orchestration together given current dependency direction.
- **Three repositories:** core library + CLI + daemon/web makes independent release ownership possible but creates the most API, tagging, dependency-bump, compatibility and end-to-end coordination. A core change would often require core CI/release, consumer version update and consumer integration CI, increasing developer lead time even if each dashboard shows a shorter individual run.

The daemon itself has the narrowest import boundary, but splitting those thirteen files is unlikely to remove the heavy coverage runtime. Choose a repository split for independently deployable products, different owners or stable external contracts—not as the first CI latency fix.

## Proving the ten-minute outcome

Collect the last 20–30 Go-relevant completed exact-head runs as a starting baseline, separately for PR required gate, main required gate, and release completion. Record creation-to-gate latency, runner queue delay, job/step elapsed, unit/native shard tails, baseline fallback, cache mode, test count and total runner-minutes. Exclude docs-only skips from the Go sample; label failures and cancellations separately rather than treating them as successes or discarding their reliability signal. Initial sample above is only one successful full main run and cannot establish p50/p95.

Compare one change at a time on representative core-only, daemon-only and combined changes, with warm and cold caches. Preserve full discovered test membership, unchanged global floor and ratchets, E2E pass/fail requirements, race scope and exact-source profile identities. Initial acceptance: at least 20 successful Go-relevant observations, observed p95 commit-to-required-gate under ten minutes, and no rise in timeout/flake/retry rate; report queue time separately. Twenty observations provide an initial empirical percentile, not strong tail confidence; keep collecting thereafter. A long single test, unsharded remainder, or profile merge becoming the tail falsifies the claim that distributing current shards alone is enough and directs the next focused optimization.

Review status: independently reviewed by the coordinator and a separate peer reviewer. This is a recommendation, not an approved implementation plan. No proposed optimization has been implemented or benchmarked. Gather performance samples from naturally occurring CI runs rather than launching twenty extra full-suite benchmarks.

## Implemented CI scope policy (2026-10-02)

The approved follow-up now scopes per-change coverage to changed packages and
their transitive reverse dependents from both revision graphs, including default
and native-tag test imports. Baseline and head measure an identical logical
selection, with new/deleted packages handled at their existing revision. Full
nightly coverage retains the 94% floor and publishes the baseline and standard
summary; selected profiles do not represent repository totals. Main may reuse an
exact trusted PR receipt. See the approved scope update in
`spec/plans/coverage-to-100/README.md` and `coverage --affected-packages` help.
This is an implementation policy, not a measured speedup; the historical timings
above remain historical evidence and dependency-heavy changes may still select
most packages.
