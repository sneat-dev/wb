---
format: https://specscore.md/feature-specification
status: Draft
---

# Feature: Agent SDLC Throughput

> [SpecScore.**Studio**](https://specscore.studio): | [Explore](https://specscore.studio/app/github.com/sneat-dev/wb/spec/features/agent-sdlc-throughput?op=explore) | [Edit](https://specscore.studio/app/github.com/sneat-dev/wb/spec/features/agent-sdlc-throughput?op=edit) | [Ask question](https://specscore.studio/app/github.com/sneat-dev/wb/spec/features/agent-sdlc-throughput?op=ask) | [Request change](https://specscore.studio/app/github.com/sneat-dev/wb/spec/features/agent-sdlc-throughput?op=request-change) |
**Status:** Draft
**Source Ideas:** —

## Summary

Make the common agent change a two-command WB journey while preserving isolated
worktrees, exact remote receipts, and recoverability:

```text
wb worktree create <task> ...
# the agent edits and reviews files; exact-path formatters run immediately
wb worktree land <worktree...>
```

WB owns deterministic subprocess execution, resource admission, validation
receipts, integration queues, dependency waves, and lifecycle cleanup. The
primary operating model is one orchestrator with three to seven concurrent
author/research streams on a four-vCPU VM. Agents may remain numerous while WB
admits no more CPU-heavy work than the machine can sustain.

This Feature coordinates existing lifecycle, stream, quality, merge, and Work
Log contracts. It does not weaken canonical-clone immutability or create a
second ownership, receipt, cache, or recovery system.

## Founder Direction

The following are founder statements from the September 4, 2026 discussion:

- *“wherever possible try to offload deterministic work to CLIs - wb
  specifically. Ideally batched.”*
- *“Ideally small change should look like wb worktree create; AI agent work;
  wb worktree land.”*
- *“I tend to think that for me most comfortable way to work is to have
  orchestrator and 3-7 streams working in parallel.”*
- *“I often run agents in vm with limited cpu - mine has 4 virtual cores.”*
- *“I’d say agent should never run tests directly.”*
- *“Go already caches and reuse test results where possible.”*
- *“Go fmt and prettier are pretty fast and important to run as close to edits
  as possible.”*

The proposed three-unit CPU budget, scheduler shape, command spelling, warm-slot
policy, and lesson curator are design recommendations rather than founder
rulings.

## Problem

The current process protects source and recovery well, but agents repeatedly
pay for orchestration WB can perform deterministically. They invoke formatter,
test, build, and Git commands separately; wait on local hooks and CI; repeat
broad checks after focused failures; and manually carry dependency, landing,
cleanup, and lessons state between sessions.

### Measured Baseline

A read-only September 4, 2026 scan found:

| Evidence | Result | Interpretation |
|---|---:|---|
| Local WB hook events, last 30 days | 501 push attempts, about 23.4 machine-hours | Local push validation is a material cost. |
| Push-hook latency | p50 94.2 s, p90 422.8 s, p95 602.4 s | The ordinary push path often blocks an agent for minutes. |
| Commit-check latency | p50 76 ms, p90 407 ms, p95 787 ms | Focused commit feedback is cheap and should stay close to edits. |
| Laptop Work Logs | 2,126 claims; 1,798 sealed (84.57%) | There is enough lifecycle volume to justify deterministic coordination; unsealed claims are censored. |
| Laptop sealed claim lifetime | p50 24.19 min, p90 978.27 min | Flow has a long tail, but this includes work, waits, and parking. |
| Laptop claim to PR merge | p50 15.04 min, p95 915.06 min | Review and landing tails are substantial. |
| Laptop PR merge to cleanup | p50 4.30 min, p90 13.49 days | Cleanup is usually fast but has an extreme ownership tail. |
| VM Work Logs | 383 claims; 299 sealed (78.07%) | A second host shows similar incomplete lifecycle evidence. |
| VM sealed claim lifetime | p50 18.53 min, p90 530.20 min | The VM also has a long flow tail. |
| VM PR merge to cleanup | p50 1.37 min, p90 188.89 min | VM cleanup is materially healthier than laptop cleanup. |
| Laptop dependency reports | 47 reports; 81 downstream repositories; 19 revisited | Shared consumers are repeatedly revisited across waves. |
| Not-enforced lesson listing | 196 lines, 17,268 bytes | The whole improvement backlog is unsuitable worker preflight context. |
| WB skill source | 67 files, 258,374 bytes | Skill routing and deduplication matter; this does not prove every harness loads every byte. |
| Small WB candidate local gate | 21 min 24.8 s | A focused two-file repair paid a full candidate gate, then an unrelated target baseline, before CI. |
| `wb pr land` exact-CI waits | about 5–6 min each | One command absorbed 8 manual calls and about 3,400 estimated tokens, but emitted no progress while waiting. |
| Provider receipt-backed landing | 6 min 30.2 s | Merge and target CI were visible; cleanup spent about 3 min 48 s repeating the already-completed canonical-sync phase. |
| `wb ci wait` exact-head observations | about 2–5 min | JSON mode emitted no stderr heartbeat until its terminal result. |
| WB shared skills-sync focused test | 8.6 s | Named affected tests gave useful local feedback without running the integration-heavy `cmd/wb` package broadly. |
| WB shared skills-sync ordinary pushes | 4.38 s and 3.67 s | The fast publication lane kept both author pushes below five seconds. |
| WB shared skills-sync pull-request CI | 5 min 6 s | One exact candidate run carried build, vet, lint, race, and coverage; the agent did not repeat race or broad coverage locally. |
| WB shared skills-sync main/release CI | 6 min 16 s | The exact landed SHA passed 14 checks including release assets and platform smoke tests. |
| `wb pr land --timeout 20m` | refused before network access | The command exposed a total-timeout flag but forwarded it as a CI slice whose hidden maximum was nine minutes, wasting a deterministic retry. |
| WB shared skills-sync landing | about 6 min 30 s | Progress stayed visible at ten-second intervals, but preflight cleanup cost about 31 s and terminal cleanup exceeded 48 s before the caller transport closed. Remote and local receipts nevertheless proved cleanup completed. |
| WB landing-timeout pull-request CI | 4 min 44 s | Eight exact-head checks passed; the agent ran only the named 1.2-second local regression and scoped static checks. |
| WB landing-timeout landing | 2 min 6 s | Exact checks were reconfirmed in 27 s, preflight inventory cost 30 s, and terminal cleanup cost 65 s. Cleanup inventory, rather than test execution, dominated the WB-owned portion. |
| WB landing-timeout main/release CI | 5 min 59 s | Fourteen exact-main checks passed and published WB v0.96.1. |
| Released WB v0.96.1 repository-filtered inventory | 21.41 s, about 42 KB output | Registry recovery still invoked Git across unrelated canonical clones and emitted unrelated missing-worktree diagnostics. |
| Exact-repository inventory candidate | 10.73 s, about 17.7 KB output | Applying the known repository before canonical and registry Git inspection cut the same real-fleet walk by 49.9% and reduced diagnostic volume by about 58%. |
| WB exact-repository single-call landing | about 6 min wall | WB v0.96.1 absorbed 3 min 49 s of pull-request CI plus merge and cleanup in one call, but a local-link guard stayed silent for about 30 s before the progress clock began; reported landing time was 5 min 32 s. |
| WB exact-repository main/release CI | 5 min 42 s | Fourteen exact-main checks passed and published WB v0.96.2. |
| WB local-link progress landing | 6 min 17 s | Candidate CI consumed 5 min 27 s; exact-repository preflight cleanup took 10.5 s and terminal cleanup took about 34 s. The release smoke emitted immediately, heartbeated at 10 s, and finished the local-link preflight at 10.35 s. |
| WB local-link progress main/release CI | 6 min 1 s | Fourteen exact-main checks passed and published WB v0.96.3. |
| Shared self-update provider landing | 2 min | Exact candidate CI consumed 1 min 34 s; merge and remote verification took under 4 s; terminal cleanup took about 22 s. |

During this investigation the shared `/Users/alex/.local/bin/wb` changed from
the released `sneat-dev/wb` revision `6217a510` to feature-build revision
`172a853c`. The replacement then warned that synchronized skills came from the
other build. This is direct evidence that concurrent feature work can mutate a
tool used by every agent and invalidate another session's verified assumptions.

The Work Log schema does not record command start/end, CPU time, memory,
process count, cache state, commit/push/PR creation, CI queue time, retry cause,
tokens, or cost. Logical claim overlap is not CPU concurrency. The evidence
therefore cannot yet establish that Git worktree materialization is the primary
startup bottleneck. Repository-local placement is too new for a useful
performance comparison.

### Current Validation and CI

WB's staged Go commit hook checks `gofmt` and vets touched packages. Its Node
hook checks changed files with configured Prettier and ESLint. That scope is
appropriate, although formatting should happen immediately after edits and the
commit hook should normally only verify it.

The WB repository's non-stream pre-push template runs `go vet ./...` plus an
eight-process coverage run. On a four-core VM this can oversubscribe the machine
and duplicates pull-request CI. Coverage and nightly full race deliberately use
`-count=1`, bypassing Go's test-result cache while retaining compiler and
module caches. Focused development tests should allow Go's result cache.

GitHub CI is clear and usefully parallel: format/tidy, vet/build, lint,
eight-shard coverage, and scoped race are separate, while full-module race is
nightly/manual. In run `33918379694`, release eligibility took 6 seconds,
coverage dominated at 4 minutes 35 seconds, and the required aggregate was
green in 4 minutes 54 seconds. That run does not justify restructuring CI. The
measured local duplicate gate is the larger problem.

## Behavior

### Primary Journey

1. The orchestrator creates or resumes named isolated worktrees through WB.
   Creation returns when each checkout is safe to edit. Dependency preparation
   may continue as a queued operation with a durable ID.
2. Three to seven author/research agents edit concurrently. Exact edited paths
   are formatted immediately. Agents submit tests, builds, dependency
   operations, and Git commands through WB rather than launching heavy
   subprocesses directly.
3. WB admits work against one machine-wide budget, coalesces equivalent checks,
   supersedes obsolete queued checks, and publishes bounded receipts. Agents
   continue reasoning or editing while non-blocking checks run.
4. A stream reports only a fresh actionable failure. A success remains silent
   until status or landing needs its receipt.
5. The orchestrator submits compatible completed worktrees to one durable
   merger lane per repository/target. WB validates once, lands through the
   permitted route, proves the remote receipt, synchronizes an eligible
   canonical checkout, and terminalizes or explicitly recycles every source.
6. An AI merger is created only for a semantic conflict, behavioral decision,
   or review WB cannot resolve mechanically.

**Observable good result:** three to seven agents remain productive, heavy
subprocesses stay within the configured CPU/memory budget, equivalent
validation runs once, every result is bound to immutable inputs, and completed
work has one owner through remote receipt and cleanup.

### Other Operating Modes

- **One orchestrator per stream:** every orchestrator uses the same machine
  scheduler and repository merger lane, so session count does not multiply CPU
  budgets or landing owners.
- **Single agent or human-led task:** an interactive command waits by default;
  `--async` returns an operation receipt. The same create/edit/land journey
  applies.
- **Recovery:** recovery and landing work outrank new background validation.
  A successor resumes durable intents and lifecycle receipts.
- **Larger machines:** budget follows explicit capacity. Raising agent count
  never raises CPU admission automatically.

### Governed Command Execution

Agents MUST submit test, build, lint, dependency-install, and Git commands
through one WB execution gateway. Direct heavy commands in a WB-managed agent
worktree MUST be refused by the harness guard with the sanctioned WB command.

Use a compatible extension of the existing `wb run` recipe command:

```text
wb run [--async] -- go test ./internal/worktrees -run TestName
wb run -- git commit -m "..."
```

Without `--`, the existing `wb run <recipe>` behavior remains unchanged. The
boundary preserves the child argument vector and removes ambiguity between a
recipe name and an executable. Recognized commands receive resource
classification, cache policy, bounded diagnostics, and result fingerprints.
Unknown commands may be scheduled but MUST NOT be deduplicated or treated as
replayable.

Intent verbs remain preferred: `wb worktree land`, checkpoint, dependency,
cleanup, and recovery commands carry stronger semantics than raw Git. The Git
wrapper is the governed compatibility path when no intent verb exists.

Formatting, `git add`, `git commit`, checkout/branch mutation, and lifecycle
transitions are synchronous because the agent needs their effects. Tests,
builds, coverage, dependency preparation, CI observation, and exact-SHA pushes
may be asynchronous. A queued push records the exact source SHA and destination
ref and refuses if either changes.

### Toolchain Isolation

Feature work MUST NOT replace the shared installed WB executable or synchronize
global agent skills as a side effect of build, test, hook, or local validation.
WB execution uses the caller's pinned WB revision or a private content-addressed
build produced for that worktree. Only a verified release/install operation may
atomically update the shared executable and then synchronize skills from the
same exact revision. Operation receipts record the WB, Go/Node, dependency, and
policy fingerprints they used.

### Immediate Formatting

After an edit transaction, a harness/editor hook passes exact changed paths to
WB. WB runs `gofmt` on existing changed Go files and configured Prettier on
supported changed files. A short debounce may batch a multi-file edit, but
formatting never waits behind a heavy-work queue.

The commit hook verifies staged paths and keeps cheap focused static checks.
Landing rechecks candidate-changed paths. No edit or commit hook starts
repository-wide formatting, tests, coverage, or race.

### Validation Ladder

| Stage | Default work | Cache policy |
|---|---|---|
| Edit | Exact-path formatter; named diagnostic when requested | Reuse native caches. |
| Commit | Staged diff/format plus cheap touched-package static checks | Reuse native caches; no tests. |
| Development | Named test or affected package/direct dependants through WB | Allow Go test-result cache. |
| Land candidate | Changed-file format plus affected static/test scope; widen for shared/build surfaces | Reuse matching WB receipts and native caches. |
| Pull-request CI | Full tests/coverage policy and scoped race | Fresh policy-defined run. |
| Nightly/manual CI | Full-module race and other expensive assurance | Fresh deliberate run. |

Module manifests, lockfiles, build tags, generators, CI, and shared public APIs
widen candidate scope deterministically. Docs-only changes run no Go checks. A
test-only change starts with its package. After a broad failure WB schedules
the named failing package/test rather than repeating the broad gate.

`wb worktree land` consumes matching receipts and runs only missing required
checks. It returns to an agent for failure or judgment, not to orchestrate
successful subprocesses.

### Four-Core Resource Scheduler

Every WB-owned operation is submitted to one local daemon, which stores an
append-only intent queue and launches short-lived workers. Synchronous CLI calls
wait for the same receipt that `--async` returns immediately. Existing typed
packages continue to own Git descriptors, task locks, claims, Work Logs, merge
receipts, and recovery; the daemon owns admission, scheduling, and delivery.
Immediate exact-path formatting may run directly because it uses no CPU queue.

The CLI uses protobuf contracts through ConnectRPC over a user-restricted local
transport: `<projects-root>/.wb/runtime/daemon.sock`, a mode-0600 Unix domain
socket on macOS/Linux, or `\\.\pipe\wb-<user-SID>`, a current-user-only named
pipe on Windows. A custom dialer changes only the transport; generated request,
receipt, enum, error, deadline, and cancellation contracts remain identical.
The local channel accepts typed lifecycle operations and the local-only raw
`wb run -- <argv>` compatibility gateway. `SubmitOperation` carries an
idempotency key; `GetOperation`, `WaitOperation`, and `CancelOperation` own the
lifecycle. `WaitOperation` accepts an opaque `after_cursor` and bounded wait so
a dropped stream or daemon restart resumes without losing progress.
`GetDaemonInfo` reports daemon build, protocol version, queue schema, scheduler
generation, and `ready` or `draining` state.

If the daemon is absent, the CLI starts the registered launchd, systemd user,
or Windows per-user service and retries within a bounded startup window. An explicit recovery mode
may execute locally under the same cross-process CPU leases, but silent daemon
bypass is forbidden because it would create a second scheduler.
WB installation registers the per-user service; normal users and agents do not
start it manually. Help, version, daemon install/status/repair, and exact-path
formatting are bootstrap-safe without the daemon. If supervised startup fails,
other governed or mutating operations refuse with the exact repair command.
`--local-recovery` is an explicit, receipted emergency path rather than an
automatic fallback.

The lifecycle surface includes `wb daemon install`, `start`, `status`, `stop`,
`restart`, `repair`, and `uninstall`. `stop` reports active and queued work and
refuses while a mutating worker is between durable boundaries unless the caller
selects an explicit bounded drain mode. `restart` is the ordinary upgrade and
recovery operation: it persists new asynchronous requests while the old
generation drains, restarts through the platform supervisor, and resumes the
same queue under the new generation. These commands work through launchd,
systemd user services, and Windows per-user service/task supervision rather
than inferring ownership from a terminal or parent PID.

### Lifecycle MVP: loopback ownership and handoff

The first operational lifecycle slice is `wb daemon start|status|stop|restart`.
Each command accepts canonical `--format=text|json`; `--json` is the shortcut
for JSON. `start` is idempotent when a reachable loopback daemon has the exact
installed executable provenance (path, SHA-256, WB version, and revision). If
that provenance differs, it marks the old generation draining, waits for it to
stop, then starts the installed executable with the next durable queue
generation. `restart --if-running` is used only after a verified WB install so
an update never starts a previously absent daemon.

The lifecycle record is private local state, atomically written with a schema
version, fenced queue generation, owner provenance/token, and predecessor
handoff. This MVP does not yet dispatch asynchronous jobs, but the scheduler
MUST attach every queued job to that durable queue generation and either resume
it once or report an incompatible-schema disposition after handoff. Ordinary
commands remain daemon-free; only an explicitly daemon-backed async operation
may request startup. The foreground `serve` path emits an alive heartbeat to
stderr at least every ten seconds while it runs.

The existing loopback HTTP dashboard remains the transport in this slice.
ConnectRPC/gRPC and MCP adapters will use the typed lifecycle/queue boundary,
never the private state file. The lifecycle package includes a Windows process
boundary, but existing Unix-only WB packages prevent a Windows binary today;
port those dependencies before enabling the per-user Service Control
Manager/task supervisor with this same contract.

The four-vCPU default has three CPU units, preserving one core for interactive
work:

| Work class | Units | Limit |
|---|---:|---|
| Exact-path format/local metadata | 0 | Immediate with short timeout. |
| Focused Go test/vet or light lint | 1 | Fair-queued by session. |
| Broad Go/Node test or build | 2 | Child parallelism capped to units. |
| Angular/Nx production build | 2 | At most one at a time. |
| Coverage or race | 3 | One local run; normally CI-owned. |
| Git fetch/remote observation | network slot | Four by default. |
| Git/common-dir mutation | classified | One writer per canonical repository. |

WB sets `GOMAXPROCS`, Go `-p`, and supported Node/Nx/Vitest workers from the
allocation. One scheduler slot cannot hide eight test processes. The existing
dependency-stream cap of at most two Go builds, one Angular build, and three
validation lanes remains the upper bound.

Admission uses weighted fair queuing by session with aging. Recovery,
interactive human work, ready-to-land candidates, and blocking focused tests
outrank speculative background work. Priority reorders queued work but never
interrupts a destructive operation.

Queued cancellation is terminal. Cancellation or process death after mutation
begins records `recovery_required`. Late results are accepted only when intent,
scheduler generation, canonical identity, tree, target, and policy fingerprints
still match.

### Scheduler Upgrade

Every CLI/scheduler handshake carries the WB build revision, protocol version,
queue schema, process-start identity, and scheduler generation. A verified
shared WB installation triggers a controlled restart:

1. The installer atomically publishes the verified versioned binary and install
   receipt.
2. The old scheduler enters `draining`: it stops dispatching new operations to
   workers but continues accepting requests into the durable queue.
3. Read-only work may be cancelled and resubmitted; mutating workers reach the
   next durable lifecycle boundary.
4. The old scheduler checkpoints its queue generation and exits.
5. The supervisor starts the new exact revision, which replays durable intents,
   validates schemas/fingerprints, and reacquires only safe leases.
6. The new scheduler publishes `ready` and dispatches the queued operations
   under the new generation. Asynchronous callers already hold their operation
   receipts; synchronous callers continue waiting across the restart.

A compatible client may observe the old scheduler while it drains. New requests
are fingerprinted and persisted against the next scheduler generation, so an
upgrade does not create an availability gap. An incompatible client requests
this controlled restart or refuses with the exact recovery command. If drain
exceeds its timeout, WB names the blocking operation and keeps the old process
alive; it never force-kills a Git mutation. After a crash the supervisor
restarts immediately and rebuilds from durable queue and lifecycle receipts.
Private feature builds neither replace the shared binary nor restart the shared
scheduler.

### Fleet Inventory Index

Fleet-wide discovery remains available for unfiltered list, orphan, GC,
`cleanup --all-merged`, collision, and displaced-worktree recovery journeys.
Repository-scoped operations must apply their known owner/repository before
starting Git subprocesses.

The daemon maintains an incremental local inventory index. Canonical-clone
discovery is keyed by projects-root directory identity and metadata; each
clone's linked-worktree registry is keyed by `.git/worktrees` identity and
metadata; active claims are keyed by their task Work Log generation; placement
layouts are keyed by user configuration generation. Cache entries carry their
source fingerprint and observation time. Before merge, branch deletion, or
worktree removal, WB revalidates the exact selected repository, claim, Git
registry, and remote SHA. A cache reduces discovery work but never authorizes a
mutation by itself.

### Cross-machine synchronization

Each registered machine runs its own local scheduler and keeps durable queue
authority local. Cross-machine coordination distributes immutable events and
remote receipts; it does not migrate a running process or lease between
machines.

When a PR lands, WB publishes the repository, target ref, and exact observed
remote SHA. Every online registered machine fetches that repository promptly.
It fast-forwards the canonical target only when the checkout is clean, has no
local commits, and no local repository writer holds a lease. Otherwise WB
records `target_update_pending` and applies the fast-forward when the lease
clears. Active feature worktrees are never silently rebased or reset; only new
worktrees consume the synchronized target automatically.

The default transport is outbound HTTPS from each daemon to an event relay or
authoritative receipt feed, which avoids exposing a laptop or VM to unsolicited
inbound traffic. A local or mutually authenticated HTTPS ConnectRPC API exposes
typed enqueue, status, wait, wake, and health operations. SSH is a supported adapter for a
user-controlled registered machine. Delivery is at least once and processing
is idempotent on `(repository, target ref, remote SHA, machine)`.

The hosted GitHub App, relay, and typed control-plane API use the dedicated
origin `https://wb-github-app.sneat.dev`. That hostname is routed to the same
existing sneat-go service on Cloud Run; it is a host boundary inside the shared
deployment, not a separately deployed application service. sneat-go contains
only the narrow route/configuration adapter and mounts the Workbench-owned
module that implements the API. Human pages remain under
`https://sneat.work/bench`.

A machine may optionally publish that API through Cloudflare Tunnel. The WB
daemon binds only to loopback or a Unix socket; `cloudflared` creates the
outbound tunnel and Cloudflare Access requires a distinct, revocable service
identity for each calling machine. The published API is typed: it accepts
allow-listed operations and bounded repository/ref/SHA inputs with idempotency
keys. It never exposes the generic local `wb run -- <argv>` surface as remote
arbitrary command execution. WB validates authorization again at the daemon and
records the caller machine on every accepted intent.

An optional WB GitHub App is the preferred remote change signal. It subscribes
only to repository selection, push, pull request, check run/suite, workflow,
and release events needed by installed WB features. The receiver validates the
webhook signature, deduplicates the stable delivery ID, durably enqueues the
event, and returns promptly. Registered daemons keep an outbound authenticated
stream to the relay and receive a compact repository/SHA wakeup only when they
have declared an interest. Burst events are coalesced; the daemon performs one
fresh exact GitHub read before acting because webhook delivery is a wakeup, not
an authority receipt. Offline daemons resume from an opaque durable cursor, and
low-frequency reconciliation polling remains the missed-event safety net.

The relay has free and paid service modes without restricting the local WB CLI.
A public repository qualifies for free relay delivery when its root `README.md`
contains a discoverable WB section linking to `https://sneat.work/bench`.
Private repositories and public repositories without that attribution consume
a paid entitlement. An account may use a small configurable number of otherwise
paid repositories for evaluation. Entitlement is resolved from the installation
and repository identity at dispatch time, cached briefly, and recorded with the
delivery decision. The receiver still authenticates, deduplicates, persists,
and promptly acknowledges an ineligible event; it records `not_entitled`
instead of dispatching daemon wakeups or repeatedly redelivering the webhook.

The loopback/HTTPS listener never exposes the local raw-command endpoint.
Connect's browser-compatible protocol lets the future dashboard use the same
generated service without a separate REST gateway.
Read models expose machines and heartbeats, repositories and target freshness,
queued/running/terminal operations, validation cost percentiles, dependency
waves, streams, and worktree lifecycle alerts. Event delivery uses opaque
cursors so a dashboard can resume without replaying the full log. Read-only
dashboard credentials cannot enqueue or cancel work; operator actions use a
separate scope and idempotency key. CLI reports, the MCP adapter, and the web
dashboard consume these contracts instead of reading daemon database files.
The dashboard surface is `https://sneat.work/bench/dashboard`, implemented in
`sneat-co/workbench-web`; it remains after the scheduler, telemetry, and event
contracts in delivery order.

Each repository has a stable page at
`https://sneat.work/bench/repo/github.com/<org>/<repo>` and each organization at
`https://sneat.work/bench/org/github.com/<org>`. Anonymous pages include only
public repositories that explicitly opt in through the root README WB section.
Signed-in pages require GitHub App installation access before revealing a
private repository's identity, count, status, or metrics. Public views expose
non-sensitive CI/release/dependency and aggregate throughput evidence; private
daemon and worktree details require a separate permission and explicit telemetry
enablement. Organization pages aggregate the same repository event and metric
contracts rather than maintaining a second data model.

Signed-in users also receive a personal throughput view across repositories they
may access. It reports landed tasks and pull requests, lead-time percentiles,
queue/validation/CI/cleanup time, retries, dependency-wave reuse, changed files
and lines, tool calls, tokens, and estimated provider cost. Token fields are
optional and identify their harness/provider source because some runtimes do not
expose authoritative usage. Derived measures include accepted changed lines per
1,000 tokens and landed tasks per million tokens, always paired with scope type,
merge acceptance, failures, and reverts. Lines per token is diagnostic evidence,
not a performance target. Public per-user visibility is opt-in; private
organization views require authorized membership and role access.

The dashboard uses graphs whenever time, distribution, concurrency, or
dependency structure is the question: stacked create-to-land timelines;
queue/CPU concurrency series; CI and cleanup latency histograms; dependency-wave
DAGs; token and cost trends; and accepted-lines-per-token scatter plots colored
by landed, reverted, or failed outcome. Every graph has the same underlying
table view and filters for user, repository, machine, model, time range, and
task type so an operator can inspect the exact receipts behind a point.

Leaderboards are separate receipt-backed views rather than one composite score:
WB usage, landed contribution, review contribution, dependency-wave savings,
CI time saved, cleanup debt resolved, and token efficiency. Each supports
7-day, 30-day, 90-day, and all-time windows. Public participation is opt-in per
user and includes only public opted-in repositories; private organization boards
require membership. Contribution rankings count landed outcomes and display
reverts and failed landings beside volume. Raw added lines and raw token spend
never determine contribution rank on their own.

Repository, organization, and user views include a latest-merges feed backed by
verified landing receipts. Each entry names the repository, exact merge commit,
pull request and related issue links, author, merged time, CI duration,
published product/repository tag or release, downstream dependency wave, and
links to inspect the underlying receipt and artifacts. Private entries follow
the same installation-membership authorization as the aggregate that contains
them.

The daemon and direct WB commands append to one sequenced operation event log.
An open dashboard follows it through resumable Server-Sent Events with
monotonic event IDs, a cursor, bounded replay, authorization filtering, and
heartbeats; local pages may subscribe to the loopback daemon, while the hosted
dashboard subscribes through `https://wb-github-app.sneat.dev`. Events cover
queue admission, worker/phase progress, CI, cleanup, synchronization, terminal
receipts, and daemon-generation changes. WebSocket transport is reserved for
future bidirectional operator controls such as cancel or reprioritize.

`wb monitor` is the terminal view over that same source and supports repository,
task, operation, session, severity, and `--since` filters.
`wb monitor --format=jsonl` emits the stable machine stream; bounded snapshots
use `--format=json`, and human output is `--format=text`. Every WB command uses
the shared Cobra `--format=<text|json|jsonl>` contract for presentation; the
cutover removes command-local `--json` booleans and other output-format variants
from help, specifications, skills, capabilities, and tests in the same release.
Commands that write an artifact retain `--output` or a more specific artifact
path flag rather than overloading `--format`. JSONC is reserved for human-edited
configuration, never command output or receipts. A bare `--` separator is reserved for commands that
forward an arbitrary child argument vector, such as `wb run -- <command>` or
`wb exec -- <command>`; daemon, monitor, lifecycle, CI, and other typed commands
use ordinary Cobra positions and named flags without that separator. Immutable task evidence remains under
`wb worktree log`; any `wb log tail` spelling is an alias for the operation
stream and MUST NOT silently reinterpret or mutate Work Logs.

Repository synchronization follows every verified landing receipt. Replacing
the shared WB executable and restarting its daemon happens only for a verified
WB release installation; a merge to the WB repository's `main` is not itself
an executable upgrade.

### Validation Identity and Notifications

A reusable receipt is keyed by:

```text
operation + canonical repository + exact tree + scope + toolchain +
dependency/lock fingerprint + policy hash + environment class
```

Go compiler, module, and result caches remain enabled where policy permits. The
WB receipt prevents repeated command launch; it does not replace Go's cache.

Equivalent requests subscribe to one operation. A newer tree supersedes an
older queued check without cancelling a mutating worker. Success is silent and
discoverable. Failure notifies subscribers once with the smallest actionable
diagnostic. Obsolete results are recorded as stale and never reported current.

### Universal Progress Contract

Every WB operation that can run for ten seconds or longer MUST emit a progress
event to stderr at least once every ten seconds from start until terminal
result. Machine-readable stdout remains a single parseable document. A caller
must never need a second status command merely to distinguish healthy work from
a stuck process.

When work has measurable units, each event names the active phase, current
unit, and completed/total count. Examples include repository 4/17, check 3/8,
poll 2 with passed/pending/failed counts, or cleanup task 2/6. When a child
process cannot expose finer progress, WB emits an explicit alive heartbeat for
the active phase with elapsed time and the last completed boundary.

A completed phase is historical evidence, not the current phase. A heartbeat
MUST NOT repeat `sync canonical: completed` while cleanup is running, repeat a
candidate SpecScore result while target-baseline tests run, or leave `wb pr
land` and `wb ci wait` silent during GitHub polling. The contract applies to
worktree create/merge/land/cleanup, candidate and target-baseline validation,
pull-request landing, CI waiting, fleet scans and recipes, dependency waves,
daemon queue/admission/workers, cross-machine synchronization, and daemon
upgrade draining.

Interactive terminals may redraw one line. Non-terminal and forced-progress
surfaces emit newline-delimited events so harnesses receive them promptly.
Progress is bounded, contains no source or secrets, and never changes the
operation receipt or its exit semantics.

CI wait progress names queued and running jobs, and names the current running
step when the provider exposes it. Repeated ten-second heartbeats may abbreviate
an unchanged set, while every state change emits the full completed/total and
active-name summary. On terminal failure WB retrieves each failed job log once,
extracts a bounded redacted failing-step excerpt with useful surrounding lines,
and prints it with the exact run/job URL and retry or resume command. Raw full
logs require an explicit opt-in and never enter JSON stdout or the durable event
log by default.

GitHub read observations use one bounded retry policy. The default is one
initial attempt plus at most three retries with exponential full-jitter
backoff. HTTP `429`, `502`, `503`, and `504`, a `403` carrying authoritative
rate-limit evidence, and recognized temporary network failures are retryable.
Authentication and authorization failures, ordinary `403` responses, policy
refusals, exact-head or target drift, malformed responses, and other semantic
failures terminate immediately. `Retry-After` and rate-limit reset headers take
precedence over the jitter delay, but no wait or attempt may exceed the
caller's total timeout. Every retry emits an immediate progress event naming
the repository, failed attempt and maximum, cause, delay, and delay authority.
GitHub mutations are not automatically replayed after an ambiguous transport
failure; their existing receipt-specific recovery proves the remote effect
before any retry.

### Durable Merger Lanes

WB maintains one queue per `(canonical repository, target ref)`. It batches
compatible reviewed work, creates one integration candidate, validates it once,
then reuses the existing mechanical merge receipt for push, pull request,
checks, landing, canonical synchronization, and cleanup.

Landing is a durable DAG rather than one foreground chain. After WB verifies the
remote target receipt it immediately fast-forwards an eligible canonical target
because new work and dependency discovery depend on it. It then durably queues
remote-branch retirement, worktree cleanup, and optional recycle preparation.
The agent may return at `landed_cleanup_queued`; the task is not terminal until
the daemon records cleanup completion. `--wait=clean` retains synchronous
behavior, and no-daemon mode performs cleanup synchronously. Once an exact
package or tag release exists, consumer dependency preparation and cleanup run
as sibling jobs under the global resource budget. A consumer's local link must
be removed before the provider worktree it references is eligible for cleanup.
Independent repositories and Work Logs may clean concurrently; mutations sharing
a canonical repository, target, claim, or receipt remain serialized.

When synchronization discovers that a canonical checkout's authoritative remote
identity differs from its owner/name path, WB treats it as a durable repository
relocation. It locks old and new identities, refuses destination collisions,
preserves dirty and unpushed state plus append-only receipts, and refreshes the
checkout marker. A checkout with no linked worktrees can relocate atomically. A
checkout with live worktrees either repairs every Git administrative pointer and
verifies them transactionally or creates the new canonical clone while retaining
an old-to-new alias until the old worktrees drain. Reruns resume the receipt; they
never leave the renamed repository undiscoverable.

The orchestrator owns ordering and product decisions. WB owns the durable
queue. A temporary AI merger handles only conflicts, semantic review, or
contradictory behavior.

### Batched Dependency Propagation

One campaign owns a durable provider/consumer DAG and one integration
branch/worktree per downstream repository. Consumers validate combined
unpublished providers through local links. Upstream updates accumulate before
one downstream manifest change and push.

Default flush policy:

- five minutes with no new provider event;
- twenty minutes maximum delay;
- immediate when a consumer blocks active work, the change is critical, no
  other provider is pending, or a user asks;
- recovery-needed, failed, or stale streams move explicitly to the next wave.

Each wave creates at most one downstream change and CI run per repository.
Terminal consumers such as `sneat-co/sneat-go` update once after their
upstream set stabilizes. Reports name included/deferred providers, the flush
reason, and validation receipts.

### Recyclable Workspace Slots

Canonical clones remain read-only. Editable canonical checkouts are rejected
because they combine synchronization authority and private task ownership.

| Option | Safety | Expected performance | Policy |
|---|---|---|---|
| Always create/remove | Current strong isolation | Repeats local materialization | Baseline and concurrency fallback. |
| Editable canonical | Conflated authority | Avoids creation | Reject. |
| Explicit manual recycle | Existing guarded transaction | Preserves approved caches | Keep and instrument. |
| Automatic recycle slot | Strong if separately fenced | Potentially fastest | Configurable opt-in only after local measurement. |

A slot binds canonical identity to the authoritative layout resolver. It has a
stable path but no permanent warm branch, task claim, remote claim, or active
Work Log. Availability comes from metadata. There is at most one logical slot
per repository across repository-local and configured shared layouts,
including migration.

Acquisition atomically reserves the slot, fetches the exact target, creates a
fresh task branch and Work Log, assigns the live session, and reconciles
dependencies. Only declared repository-relative ignored caches survive. A
second agent receives a new isolated worktree. Claimed, interrupted, dirty,
unpushed, unlanded, or recovery-needed checkouts are never inferred available.

```text
absent -> provisioning -> released -> acquiring -> claimed
claimed -> releasing -> released
provisioning|acquiring|claimed|releasing -> recovery_needed
recovery_needed -> salvaging -> released|quarantined
```

`wb sync` may refresh only `released`; acquisition still revalidates the
remote target.

Recycle policy is disabled by default and configurable per user and per
repository because checkout size, dependency caches, filesystem, and machine
cost vary. WB first benchmarks repeated fresh create/remove, explicit recycle,
and fresh creation with declared dependency caches retained on both laptop and
VM. It reports materialization, refresh, cache warming, contamination checks,
and total create-to-ready time. Enabling recycle never follows from a global
fleet average.

### Lessons Without Worker Context Spam

Workers load only relevant compact Enforced rules selected by repository,
command, and change surface. They never load the full not-enforced backlog.

When a process gap is clear, the worker submits a small structured observation
to a private WB outbox: failed control, expected control, evidence reference,
repository, command category, and candidate known lesson. It contains no prompt
body, source, secrets, or arbitrary output.

A lower-cost asynchronous curator batches and deduplicates observations,
records or recurs SpecScore lessons, proposes promotion, lints once, and opens
at most one Backstage change per batch. Safety-critical, repeated, or blocking
gaps may force synchronous curation. Effectiveness is recurrence reduction
after enforcement, not lesson volume.

SpecScore should add compact/count/limit/fields output for preflight and a batch
occurrence input. Shared index updates remain transactional unless that contract
is deliberately redesigned.

### Work Log and Command Telemetry

WB appends bounded events for create, dependency preparation, formatting,
command execution, validation, commit, push, PR creation, CI wait, candidate,
landing, cleanup, recycle, and recovery. Events record:

- correlation/category/scope and start/end/outcome/retry cause;
- repository, before/after SHA, tree/policy/toolchain fingerprints;
- queue/lock wait, wall/CPU time, peak RSS, allocated units, child count;
- cache/receipt hit or miss and CI queue/start/end;
- actual provider tokens/cost only when exposed by the provider.

Events never record prompts, commit messages, diffs, source, secrets, raw
arguments, or arbitrary output. Raw events stay private under `WB_HOME`;
reports aggregate p50/p75/p90/p95 and throughput. Retention and remote
aggregation are user policy.

Read-only reports MUST be side-effect-free. `wb hooks metrics` and
`wb hooks measure` must read existing events without preparing or changing
hook-runtime permissions.

## Operation State

```text
requested -> validated -> queued -> admitted -> running -> succeeded|failed
queued -> cancelled|superseded
running -> recovery_required|stale_result
recovery_required -> queued|failed|cancelled
```

The intent is written before admission with bounded nonsecret fields:
operation ID, idempotency key, requester/session, repositories, expected
canonical/base/head/tree, kind, priority, resources, placement snapshot, and
input digest. Reissuing an idempotency key cannot create a second worktree,
branch, push, claim, or deletion.

## Compatibility and Migration

1. Add phase telemetry and side-effect-free reports without changing behavior.
   Measure cold creation, manual recycle, dependency preparation, and first edit
   at one, three, five, and seven active agents.
2. Introduce governed execution in observe-only mode.
3. Add the local scheduler, sync/async receipts, resource caps, coalescing, and
   stale-result suppression.
4. Add registered-machine receipt fan-out, fetch, and guarded canonical
   fast-forward; keep queue authority local to each machine.
5. Move broad non-stream pre-push validation into scheduler/landing policy.
   Keep diff/worktree guards and cheap commit checks.
6. Add `wb worktree land` over existing merge/receipt/sync/cleanup machinery.
7. Add dependency debounce and shared downstream integration branches to the
   existing stream/campaign engine.
8. Trial one explicit recycle slot per selected repository and compare it with
   always-create and manual recycle.
9. Add a thin MCP adapter only after CLI/API receipts stabilize.

Existing create, merge, quality, manual recycle, and both placement layouts
remain valid during migration. Older clients may inspect but cannot mutate
unknown newer operation states.

## Durable Delivery Plan

This checklist is the persistent resume point for the SDLC effort. A checked
item means its remote receipt has been verified, not merely that code exists in
a worktree.

- [x] Make repository-local `.worktrees/` the default while retaining a
  configurable shared worktree root.
- [x] Add `wb run -- <argv>` so agents submit deterministic commands through
  one auditable wrapper.
- [x] Replace broad ordinary pre-push work with a fast publication lane and
  keep full race/coverage in CI or deliberate final gates.
- [x] Measure laptop and VM Work Logs, hook latency, lifecycle tails,
  dependency revisits, and worker-context volume.
- [x] Publish `strongo/cli-helpers v0.9.0` and replace WB's duplicated skills
  synchronization engine with the shared immutable-plugin implementation.
- [x] Make `wb pr land --timeout` a usable total wait budget over bounded
  exact-CI slices, with working defaults and no hidden nine-minute refusal.
- [x] Narrow known-repository landing and cleanup inventory before subprocess
  inspection; preserve shared-root recovery and exact cleanup receipts.
- [ ] Make every pre-orchestrator landing guard emit immediate progress and a
  ten-second heartbeat, including local-link inventory.
- [ ] Add the fingerprinted local fleet-inventory index while retaining fresh
  exact-repository revalidation before every mutation.
- [ ] Benchmark opt-in worktree recycling against fresh creation and retained
  dependency caches on laptop and VM; configure the policy per user/repository.
- [ ] Move post-landing branch retirement, cleanup, and recycle preparation to
  durable background jobs; fast-forward the canonical target immediately and
  run released dependency waves in parallel with safe cleanup siblings.
- [ ] Detect authoritative repository renames during sync and perform resumable,
  collision-safe canonical relocation, using `strongo/selfupdate` to
  `strongo/cli-helpers` as the acceptance fixture.
- [ ] Complete shared skills synchronization across the remaining inventoried
  CLIs, preserving explicit publication holds and repository-owned decisions.
- [ ] Enforce the universal ten-second progress contract across every
  long-running command and daemon operation.
- [ ] Persist command telemetry for queue, subprocess, cache, CPU, memory,
  retries, CI, landing, cleanup, and saved agent calls/tokens.
- [ ] Add the per-user daemon with durable async intents, three CPU units on a
  four-vCPU host, fair queuing, deduplication, supersession, and controlled
  version draining/restart.
- [ ] Make `wb worktree land` consume focused receipts and escalate only
  actionable failures or semantic decisions.
- [ ] Add debounced provider-to-consumer dependency waves with one downstream
  integration branch and CI run per repository per wave.
- [ ] Fan verified landing receipts to registered machines and guardedly
  fast-forward clean idle canonical targets.
- [ ] Add the optional WB GitHub App event relay with signed, deduplicated
  webhooks, interest-scoped daemon wakeups, durable cursors, and reconciliation
  polling; support attributed-public free delivery, a small evaluation
  allowance, and paid private or unattributed repositories.
- [ ] Batch lesson observations through a compact asynchronous SpecScore
  curator without loading the unenforced backlog into worker context.
- [ ] Add the read-only dashboard at `https://sneat.work/bench/dashboard` in
  `sneat-co/workbench-web` over the typed daemon API, then the thin MCP adapter
  after CLI/API receipts stabilize.
- [ ] Add public-opt-in and private-authorized repository and organization pages
  under `/bench/repo/github.com/<org>/<repo>` and
  `/bench/org/github.com/<org>` using the same typed event contracts.
- [ ] Add an access-scoped personal throughput view with optional attributed
  token/cost telemetry and quality-paired lines-per-token metrics.
- [ ] Add receipt-backed charts for lead time, concurrency, latency,
  dependency waves, token/cost efficiency, and outcomes, with equivalent tables
  and consistent repository/user/machine/model/time/task filters.
- [ ] Add opt-in public and membership-scoped organization leaderboards for
  usage, landed/review contribution, saved CI/dependency/cleanup work, and token
  efficiency, with explicit time windows and quality context.
- [ ] Publish the measured article “How I run a fleet of 150 repos in 10
  streams at once to build 20+ products in parallel” with before/after charts.

## Acceptance Criteria

### AC: seven-agents-respect-four-core-budget

Given seven sessions submit mixed focused tests, broad tests, an Angular build,
coverage, and fetches on four vCPUs, when WB schedules them, then no more than
three CPU units run, child workers stay within allocations, network work uses a
separate cap, fair queuing gives every session progress, and one core remains
outside WB's budget.

### AC: equivalent-tests-run-once

Given three sessions request the same test for the same exact tree, toolchain,
dependencies, scope, policy, and environment, then one subprocess runs and all
three consume its receipt. A later request reuses the success.

### AC: landed-target-reaches-registered-machines

Given laptop and VM schedulers are registered and a PR lands on `origin/main`,
then both machines fetch the exact landed SHA. A clean idle canonical checkout
fast-forwards promptly; a busy or dirty canonical checkout records a pending
update and advances only after its local writer lease clears. Existing feature
worktrees are unchanged, and duplicate delivery creates no duplicate mutation.

### AC: changed-tree-invalidates-result

Given a queued or completed test and a changed worktree, then the old result is
stale for that request, no stale failure is announced as current, and WB creates
or finds a receipt for the new tree.

### AC: direct-agent-test-names-wrapper

Given a registered agent runs a direct test/build/coverage/race command in a
managed worktree, then the harness guard refuses before launch and prints the
equivalent governed command. Human use outside agent mode remains compatible.

### AC: feature-validation-cannot-replace-shared-wb

Given one session validates an unmerged WB feature while other sessions use the
released CLI, then validation builds and runs a private content-addressed
binary; the shared executable and synchronized skills remain byte-identical.
A release install updates both from one exact verified revision.

### AC: verified-install-drains-and-restarts-scheduler

Given an old scheduler has queued work and a mutating worker in flight, when a
verified WB release is installed, then new worker dispatch stops while durable
request intake continues, the mutating worker reaches a durable boundary, the
queue generation is checkpointed, one new scheduler starts from the installed
revision, and every queued intent is either resumed once or given an exact
incompatible-schema disposition. No second independent scheduler dispatches
work during the transition.

### AC: formatting-follows-the-edit

Given an edit changes Go and Prettier-supported files, then only those paths are
formatted before the next review; commit verifies staged paths; and neither
hook starts tests, coverage, race, or repository-wide formatting.

### AC: land-is-the-second-command

Given compatible clean worktrees and matching focused receipts, when the
orchestrator runs `wb worktree land`, WB prepares one candidate, runs only
missing validation, lands through a permitted route, proves remote target and
post-target checks, synchronizes an eligible canonical checkout, and
terminalizes or recycles every source without another deterministic agent call.

### AC: merger-agent-is-exceptional

Given a conflict-free reviewed batch, no AI merger session is created. Given a
semantic conflict, WB preserves exact candidate/source state and requests
judgment.

### AC: downstream-updates-once-per-wave

Given several provider changes reach one consumer during the debounce window,
one consumer change, validation, push, and CI run include all ready providers.
The report names deferred providers. A blocked critical consumer may force an
explicitly reasoned immediate flush.

### AC: interrupted-work-is-never-reused

Given a worker/session dies during mutation, the intent and checkout become
recovery-required. Owner death or clean Git status never releases them, and
resume/salvage uses existing exact lifecycle evidence.

### AC: one-slot-across-layout-migration

Given a released slot and a placement-root change, two concurrent acquisitions
identify one logical slot through canonical identity and `WB_HOME`; one
acquires it and the other receives a new isolated worktree.

### AC: recycle-is-local-opt-in

Given recycling is disabled, WB always creates a fresh isolated checkout.
Given one user or repository enables it after a local benchmark, WB reports the
measured create-to-ready saving, retains only declared ignored caches, refreshes
the exact target, and creates a fresh claim and Work Log. Another machine keeps
its own policy and measurements.

### AC: inventory-cache-never-authorizes-mutation

Given an indexed fleet inventory answers repository discovery without a full
scan, when WB is about to merge, delete a branch, or remove a worktree, it
freshly verifies the selected repository's Git registry, active claim, and
remote SHA. Stale cache state can cause a refresh, never a mutation.

### AC: github-event-wakes-only-interested-daemons

Given one GitHub App delivery affects a repository used on laptop and VM, the
relay validates and deduplicates it, acknowledges promptly, and wakes only
registered interested daemons. Each daemon coalesces bursts and performs one
fresh exact read before acting. Replayed delivery IDs create no duplicate work,
and an offline daemon resumes from its cursor or reconciliation poll.

### AC: github-relay-entitlement-is-explicit

Given an installed public repository has a root `README.md` WB section linking
to `https://sneat.work/bench`, its eligible webhook wakes interested daemons in
free mode. A private or unattributed repository uses an available evaluation
allowance or paid entitlement. Without either, the signed delivery is persisted
and acknowledged once with `not_entitled`, no daemon is woken, and the decision
can be audited by installation and repository identity.

### AC: telemetry-supports-causal-analysis

A create-to-land report separately measures queue, fetch/materialization,
dependency, validation, push/CI, landing, and cleanup. CPU/RSS/cache/retry data
is present where available, and private content is absent.

### AC: long-operation-progress-never-goes-silent

Given a fake child, GitHub observation, queue wait, or cleanup step blocks
without producing output, WB emits newline-delimited stderr progress with no
gap greater than ten seconds until terminal completion. Measurable work reports
completed/total and the current unit; opaque work reports an alive heartbeat.
When candidate validation transitions to target-baseline validation, or
canonical synchronization transitions to cleanup, every subsequent heartbeat
names the new active phase and never presents the completed predecessor as
current. JSON stdout remains independently parseable.

### AC: ci-wait failure is actionable in one invocation

Given an exact-head check run fails, `wb ci wait` reports the completed/total
check count and a capped list of pending job names while it observes GitHub.
On failure it prints each failed Actions check's exact run and job URLs plus a
bounded, redacted tail of that job's failed-step log. `--format=json` emits the
same structured receipt, and `--json` remains its shortcut; neither output
mode contains the raw full job log.

### AC: transient-github-read-recovers-in-process

Given GitHub returns `502`, `503`, or `504` for an exact CI observation and a
later attempt succeeds inside the command timeout, WB returns the successful
observation without requiring another agent call. Each retry appears in stderr
with attempt/max, cause, and delay. Given the same request returns `401`, an
ordinary `403`, or exact-head drift, WB makes one attempt and returns the
terminal failure unchanged.

### AC: lessons-are-curated-off-worker-path

Several observations of one gap create no global worker-context load and become
one deduplicated curation batch. Future workers receive only relevant compact
Enforced rules.

## Open Questions

1. Should noninteractive agent commands return operation receipts by default,
   while interactive human commands wait, or must `--async` always be explicit?
2. Is the default priority recovery, interactive human, ready-to-land,
   blocking focused test, dependency preparation, background validation?
3. Which cache paths, size/age caps, and eviction policy may recyclable slots
   retain? Is automatic acquisition opt-in until measured?
4. Does WB generate operation IDs from caller idempotency keys, or may trusted
   adapters supply final IDs?
5. How long are raw operation events retained, and may aggregate
   timing/resource/token metrics leave the machine?
6. After observe-only rollout, are direct heavy agent commands refused always
   or only while the scheduler service is available?
7. Which conditions force synchronous lesson curation beyond safety-critical,
   repeated, or currently blocking gaps?

## Dependencies

- worktree-lifecycle
- mechanical-worktree-merge
- dependency-streams
- work-log
- fleet-quality

---
*This document follows the https://specscore.md/feature-specification*
