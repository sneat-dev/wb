# Proposed WB CLI domain inventory

Research supporting [the command-family plan](README.md), based on repository revision `3e7737d0901f4ebf4b90c38a9a84b4a6e1f4ff8f` (`3e7737d0`). The canonical source inventory has **135 production Go files, 43,889 physical lines and 285 test Go files** under `cmd/wb`. CodeGrapher 0.9.9 reported a clean index at research time. No tests or builds were performed for this map.

Historical coverage source: [WB nightly CI run 37111269874](https://github.com/sneat-dev/wb/actions/runs/37111269874), artifact `wb-nightly-go-coverage`, `profile.cov`. It reports **1,666 uncovered statements** in the inventoried executable package. These counts are planning evidence, not a fresh execution or CI verdict. All paths below are repository-relative; filenames in the appendices are relative to `cmd/wb`.

The assignments are **provisional ownership and responsibility splits**, not instructions to mechanically move whole files. A mixed file or test suite must split by behavior, with each resulting responsibility assigned to one owner. This inventory covers the base revision: the layout pilot has already begun removing and relocating some of these files on the implementation branch.

Founder intent: “cmd/wb should be relatively light - like registry of root level commands. And root level commands can be on packages like cmdworktree. That way if we register command or change command args it minimizes scope of retesting. Also command packages should handle arguments and delegate work to internal packages through seams - that way we can write light isolated tests.” Follow-up: “If it proved to be useful good design proceed with the full refactoring.” Latest emphasis: “Yes focus on reusability and minimising test execution time.”

## Journey and observable outcomes

A user calls the same `wb <verb>` with the same persistent/local flags. Cobra rejects unsupported flags before doing work; accepted flags reach the selected operation with their parsed values. The operation receives context, explicit input/output streams and its small dependencies; the command emits the same output and typed exit outcome. Side-effect-free version and cockpit-export probes remain side-effect-free. The executable still dispatches private launcher/Git helpers and daemon subprocess modes. Family tests construct just that family with fake operations and prove request/format/error behavior. A small executable suite proves dispatch, root registration, inherited flag policy, streams and exit codes. Real Git/process/network journeys remain on the service or executable boundary that actually owns them.

Observable payoff: a family argument-only change rebuilds that family and reverse importers (including root), while unrelated sibling tests remain eligible for cache reuse. Expensive fixtures disappear from command contract tests; executable coverage remains a bounded integration concern. The layout pilot must demonstrate behavior preservation, dependency isolation and cheap measured family tests. Concrete sibling-cache evidence is deferred until at least two families are extracted; a single-family pilot cannot prove it.

## Dependency shape and reusable contracts

Complete-cutover direction: `cmd/wb -> internal/cli -> internal/cli/cmd<family> -> existing internal domain services`. **`internal/cli` owns root composition and imports families; `internal/cli/shared` is the family-imported leaf contract package.** Family packages may import `internal/cli/shared` and operational services, but never `internal/cli` or a sibling command family. `internal/cli/shared` must not import root composition, command families or daemon runtime. This distinction prevents the composition-to-family-to-composition import cycle. Existing domain packages must not import Cobra or the CLI. Add a domain service package only where operations currently live in `cmd/wb`; avoid merely moving domain bodies into `internal/cli`.

- At complete cutover, one shared per-invocation contract owns `ProjectsRoot`, `Filter`, `ExtraOrgs`, `NonInteractive`, `Quiet` and execution-start state (or a distinct root-owned start marker). Root composition in `internal/cli` allocates it once and binds flags to it; the contract type lives in `internal/cli/shared`. Families capture the same pointer. Read its values during Args/RunE or deferred factories. Do not create copied family structs or compute a service with a default root at command construction time.
- The final constructor shape can stay concrete and simple: `New(inv *shared.Invocation, deps Dependencies) *cobra.Command`, with named function fields for the actual external operations. Keep command-local flags in each constructor closure. Factory defaults may be production composition, but dependency fields must not capture pre-parse invocation values. No command registry framework, command DSL, mega-interface or exported bag of all WB services.
- Shared CLI error type must preserve `errors.As` and nonstandard command exit codes, plus usage errors raised after Cobra starts the command. `ci.go:280` currently defines `exitError`; `deps_policy.go:177` defines `usageError`; `main.go:637` chooses nil -> 0, typed code -> code, pre-start error -> 2, command error -> 1. Preserve this exact precedence and the special landed/incomplete exit tests.
- Shared formatting owns the canonical JSON shortcut/value binding (`output_format.go`) and plain format validation (`worktree.go:2777`). JSON/stdout must remain parseable; diagnostics/progress stay on the documented stream. Do not silently redefine all format refusals as usage errors: existing generic errors after startup currently map to command failure unless explicitly typed.
- Shared command metadata owns discovery-term annotations (`commands.go:73`), quiet metadata (`quiet.go:93`) and landing annotations (`locallink_guard.go:136`). Root help grouping/catalog belong to composition in `internal/cli`; catalog traversal receives a command tree and does not import families. Persistent flag policy may remain root table initially; eventual family metadata must remain data, not sibling imports. Keep the central flag-policy conformance suite.
- Reuse progress rendering from `live_progress.go` and campaign progress only where semantics match. CI wait, quality, status and remote publish progress are specialized adapters; do not collapse them into one universal event type. Replace test clocks/tickers through small dependency functions where real waits are avoidable.
- Mutation admission and landing contracts compose existing `worktrees`, `landinglane`, `lifecyclehooks`, `locallink` logic. Their domain work belongs below CLI; command functions retain flag binding, stream selection and context. `landingLaneOwner` currently calls the session CLI's `sessionDirForRead`; move path/identity resolution to a shared service, never import `cmdsession` to get it.

### Parse-time binding examples to preserve

`newFleetOverviewOptions` stores the invocation pointer (`fleet_cmd.go:76`); `newFleetStatusCmd` resolves `inv.filterFlag/projectsRoot` **inside RunE** (`fleet_cmd.go:164-176`). `defaultTaskOffloadDependencies` defers store/path resolution inside functions (`task.go:45-59`). These are correct patterns, not diagnosed bugs. A refactor that passes `inv.ProjectsRoot` by value into `New` before Cobra parses it will break `--projects-root`; a copy of `Quiet` misses WB_QUIET applied by PersistentPreRunE. Verify two independently constructed roots with distinct options and root flags both before and after the family name. Avoid supporting concurrent Execute calls on one Cobra command; per-invocation roots are the meaningful isolation boundary.

## Temporary migration boundaries

Until the constructors have left `package main`, the root registry **must stay in `cmd/wb` as the existing explicit `AddCommand` calls**. `internal/cli` cannot import or call constructors in an executable package. Moving shared policy helpers and leaf contracts is allowed before the registry moves, but root still executes the policy through bounded adapters. Do not create a registry callback/plugin bridge, registration framework or factory injection mechanism merely to move the registry early.

Temporary adapters are small named functions for the migrating cohort: preserve the existing invocation owner, resolve inherited values through shared getters during Args/RunE, and pass only the operation dependencies that cohort consumes. A request snapshot is safe only after parsing and relevant root policy have applied; constructor-time snapshots lose root overrides or Quiet policy. Getter contracts remain leaf contracts, not per-family copied option state or a mega-interface. Replace each adapter when that family uses the final shared invocation contract; remove the old root invocation/error types at the final cutover.

The end-state sections and inventory destination rows describe final ownership, not an immediate Stage 1 move. Once all families have package constructors, Stage 8 moves the explicit composition and registry to `internal/cli`, leaving startup/dispatch/exit in `cmd/wb`. All newly added shared-package statements and temporary adapter statements are covered under the existing gates. Existing root call sites are not mechanically rewritten merely to normalize field names.

## Root and executable responsibilities

`cmd/wb/main.go` should finish as process entry, private-helper dispatch and exit. The explicit root-family registry lives in `internal/cli`. Move reusable invocation/error/output helpers out; process env/executable propagation and private dispatch stay near the executable. Registration must still include worktree/create/land aliases; pr/branch/session/agent/task/wait; stream/deps/migrate/run; status/fleet/sync/sync-report/repo; coverage/verify/check/deadcode/ci/hooks; disk/archive/layout; worker/daemon/remote/peers; cockpit/dashboard compatibility; self-update/install/upgrade/skills; version/commands. `verify_receipt.go` supplies a worktree verification surface and belongs with that family rather than inventing a new root command.

Root PersistentPreRunE keeps rejected ignored persistent flags, ignored-WB_HOME diagnostics, start marking, WB_QUIET eligibility, side-effect-free exceptions, invoked-command attribution and current-directory heartbeat. Do not give families a PersistentPreRunE that shadows this. Root owns bare-help, root `--version`, Fang behavior, ANSI suppression, `--` forwarding, signal/process exit and error printing. Build provenance/version data should become a tiny internal service reused by install/remote/skills/worker/daemon; root version command remains a cheap adapter.

## Cross-family coupling that must be cut at the service boundary

| Current definition | Consumers / concrete issue | Destination |
|---|---|---|
| `requireOutputFormat`, worktree.go:2777 | layout, PR, branch, archive, session, stream, deps, daemon | cli formatting leaf |
| `usageError`, deps_policy.go:177; `exitError`, ci.go:280 | most operational families | cli error leaf |
| `setDiscoveryTerms`, commands.go:73 | root catalog and many families | cli command metadata; tree catalog stays root |
| `defaultDaemonDependencies`, daemon.go:499 | run, wait operation, worker, cockpit, dashboard, peers | small daemon controller/client factories in domain runtime package |
| `daemonOutputFormat`, daemon.go:819; `writeJSONTo`, deps_policy_fleet.go:431 | cockpit/dashboard/worker and daemon/fleet | cli formatting/render helper; concrete result renderers stay family |
| `defaultRemoteDeps`, remote.go:39 | stream, worktree, sync, daemon periodic publisher | remote service facade over existing remotestate/remoteclaim/remotessh |
| `hookExecutable`, hooks.go:638 | fleet, skills, worktree, stream | executable resolver function / existing install/runtime service |
| `qualityTargets`, quality.go:387; `runTargets`, quality.go:558 | fleet, status, remote and quality | selection/parallel execution service using discover; do not import cmdquality |
| `fleet`, `fleetOwners`, fleet.go | sync, deps, run, fleet | fleet selection service; caller passes explicit orgs/root/filter |
| `newStreamEngine`, stream.go:91; stream_adapters.go | worktree cleanup, hooks checker, remote identity | internal streams composition/adapters with explicit deps |
| `launchTaskWithSessionMove`, task.go:246 | builds and executes the session command as business operation | shared session launch/move service; task and session CLI call it |
| `sessionDirForRead`, session.go:58 | landing owner lookup, session operations | existing session/sessionmove service path resolution |
| `ciWaitProgress`, quiet.go landing progress | PR/worktree/wait/ci | shared landing progress adapter or console primitive, no cmdci dependency |
| `lifecycleCheckoutUpdated`, lifecycle_hooks.go | sync/worktree/orchestration callbacks | shared lifecycle composition service |
| `report`, report.go | family summaries | writer passed explicitly; do not retain fmt.Print/global stdout |
| `openBrowser`, browser.go | cockpit/dashboard/session routes | reusable platform browser service with injected launcher |
| `loadSecretScanner`, secretscan.go | park/send/receive continuation scans | secretscan loader operation injected per invocation; CLI prints advisories |

### Mutable globals and test fixture dependencies

Current mutable seams: `migrateLayout` (layout); `fleetRemoteRepositories`, `fleetRemoteSync`, `fleetWorktreeList` (fleet); `captureParkedSessionAggregate`, `sessionWorktreeLister` and registration factory vars (session); `streamWorktreeCleanup`; `runQueueHeartbeatOverride`; `loadSecretScanner`; `runSystemctl`/timeout and `runLaunchctl`/timeout; daemon heartbeat/supervisor wait settings. Replace them with constructor/service dependency fields rather than moving variables to a new package. Immutable lookup maps/regexps need no forced DI.

94 test files contain `t.Setenv`; 67 contain root/run/dispatch calls. Those are lexical inventory counts, not measured serial time. `main_test.go:28` TestMain also routes private helpers, isolates user/harness/process state, disables host-load admission, configures Git maintenance and owns queue/build cleanup. Moving a file loses these protections unless its remaining real operations receive equivalent isolation. Reuse existing `internal/testenv` for subprocess/service integration tests; cheap fake command tests should not require a process-wide setup.

`cli_smoke_test.go:37` buildWB already builds once per test binary and TestMain removes its build directory. Copying it into every family would create many full builds. Keep a single executable test boundary; family tests never invoke buildWB/runWB. `testsupport_test.go` mixes daemon transport helpers, root constructor, run queue progress and npm publish adapters: split by owner, with only generic buffer/Cobra execution helpers in a leaf test utility (if reuse warrants it). Such a helper must not import root or any family. `zz_cov_*` files deliberately mix domains; split individual Test funcs by tested production symbol, not filename prefix. Source AST guard `noglobals_test.go` must be adapted to scan moved family packages; leaving it pointed only at cmd/wb weakens its intended check.

## Daemon runtime extraction

Daemon is **21 production files, 7,066 lines, 297 uncovered statements**. A command package must not become a daemon platform package. Reuse `internal/daemon` for existing models/state/queue/RPC, then add narrowly bounded runtime composition where required (for example `internal/daemonruntime`). Avoid putting a monolithic Controller plus HTTP hub plus RPC bridge plus all CLI renderers into one exported interface.

Split `daemon.go`: constructors and public CLI result rendering into cmddaemon; lifecycle controller/locking/recovery/status/start/restart/stop/supervisor behavior into daemon runtime; service assembly `serveDashboard` into runtime composition taking explicit config, context, writer, callbacks and roots; command-specific refusal text may stay in adapter only when it truly describes invocation.

Move identity/path ownership (`daemon_identity.go`), OS process/lock/listener/startlog implementations and build tags into the runtime package intact. Move file bridge envelope/transport/server and RPC auth/client protocol into daemon client/bridge service. Move hub mounting/webhooks/polling/peer-admin HTTP and cockpit sampler/SSH/periodic publisher into runtime/domain composition with existing hub/cockpit/remotestate packages. `daemon_operation.go` retains submit/get/wait/cancel Cobra adapters; authenticated operation clients and waiting belong below CLI. Cross-family run/wait/worker/peers/cockpit consumers depend on the small client or lifecycle service, never cmddaemon.

Keep OS-specific test constraints and helper-child entry handling. No assumption that moving daemon code permits simulated lifecycle tests to replace actual process/listener/ownership journeys; cheap controller tests can fake clock, health, process, supervisor, lock/file operations, while a small integration suite covers those real boundaries.

## Phased rollout, ordered for reuse and execution cost

| Stage | Cohesive change | Why this order / supplied uncovered counts | Required observable receipt |
|---|---|---|---|
| 0 | Prove layout pilot; shared exit semantics only as needed | Small 333-line family, 25 uncovered; existing mutable migrate seam gives meaningful isolation proof | Audit/clean/migrate flag and report equivalence; writes/error injection; inherited root parsed values; existing root contracts; record targeted execution, dependency isolation and cheap family tests; sibling-cache proof waits for two extracted families |
| 1 | Bounded internal/cli/shared error/format/metadata contracts and execution-time invocation getters; leaf test harness; explicit AddCommand registry remains in cmd/wb | Enables cohort migrations without touching every existing constructor or uncovered root call site | New shared statements reach 100%; bounded adapters covered; two roots isolated; usage/findings/custom exits; parsed flags/quiet/streams preserved; no harness imports back to root/families |
| 2 | CI/quality/hooks + leaf maintenance and install/skills/repo adapters | CI 14, quality 48, hooks 117 uncovered; narrow services already exist; eliminates real wait and command-global hooks from unit tests | Fake operation request tests and deterministic wait tests; actual secure hook/private-helper executable journey retained; leaf API/help tests |
| 3 | Daemon client/lifecycle/runtime foundation, then daemon/worker/run/wait/peers/cockpit/dashboard families | Daemon 297, worker 38, run 38, wait 24, peers 55 uncovered. Largest reusable infrastructure; risk is high, so split client/lifecycle/runtime first | Lock/supervisor/ownership, bridge auth/client and operation waits below CLI; cheap formatting/request tests; bounded real daemon lifecycle and operation journeys |
| 4 | Session/agent/task commands and session launch/move service seam | Session 173, agent 51, task 24 uncovered; existing WithDeps constructors provide good foundation; remove nested Cobra execution and secret scanner globals | Park/resume/send/receive/move request/refusal tests fake deps; persisted handoff and child launcher journeys still executable; no cmdtask -> cmdsession import |
| 5 | Worktree/create/land + PR/branch + landing/lifecycle contracts | Worktree 140, PR 26, branch 31 uncovered; mutation and landing risk is high; previous client/session/format seams make isolation possible | Admission, flags, receipts, linked-worktree refusal, custom incomplete exit; existing real merge/rescue/cleanup/collaboration/publication boundaries retained |
| 6 | Fleet/status/sync/remote and selection/aggregation services | Fleet 172, status 10, sync 25, remote 37 uncovered; shared selectors and runtime/identity now extracted | Fake scan/rollup/remote deps; org/filter/depth/report shape; representative real Git sync and remote publication journey |
| 7 | Deps/stream/migrate and final adapters; drain mixed tests | Deps 136, stream 44, migrate 23 uncovered; convergence and consumer graph logic belongs in existing internal services | Request/ordering/error/format tests cheap; actual publish/sync/worktree cleanup/lease end-to-end contract retained; every inventory row resolved |
| 8 | Move explicit root registry/composition into internal/cli after families cut over; remove temporary root adapters/types; full existing governed verification | Final root import shape and moved instrumentation affect coverage ownership even without changing behavior | No stale main adapters/invocation/error types, no command-to-command imports, all root verbs/aliases registered, no weakened flag or AST guard; existing required checks/coverage pass without policy changes |

Stages are boundary cohorts, not parallel permission or a requirement to postpone landings. Maintain one implementation/landing owner and use the approved worktree. Stage 2 includes archive/disk/repo/layout/install/skills leaves where dependencies are already ready; a leaf that still calls worktree/session helpers waits for that seam, rather than gaining sibling imports. Allocation uses isolation payoff and risk plus uncovered statement evidence; it is not a new coverage campaign or a promise to write tests for all 1,666 statements.

## What stays executable/root, and what moves

Keep root/executable contract tests: help/discovery tree, persistent flag matrix, quiet eligibility/environment adapter, root version/--version, code/stream rendering, dispatch/private helpers/`--`, process executable propagation, build/release provenance, no real-user-state reach, selected-scope CLI journey. Keep executable real daemon publish/cockpit lifecycle, canonical branch independence, worktree collaboration/abort/publication and dispatch-secure-helper journeys where they exercise the whole program. Their domain assertions can move to service suites while their executable journey stays.

Move family validation/flag mapping/rendering/request/operation-error tests to family packages with only that command constructed. Move pure algorithms, filesystem/report writes, controller/transport behavior and Git operations to their owning domain service package. A filename ending `_e2e_test.go` does not itself require root ownership; actual subprocess execution and root composition determine that. Test appendices below flag executable candidates and root constructor usage separately. Mixed files require splitting, not exporting private production helpers solely to preserve tests.

## Realistic Go testing/cache/coverage effects

Changing a family may invalidate root's test inputs because root imports that family; it may invalidate a service consumer if production or test imports share that dependency. Go caching can avoid re-executing independent sibling tests when their test inputs and flags are unchanged. Root integration tests can still build WB and run relevant executable journeys. Package cache is not function granularity; -count=1, changed coverage flags, different instrumentation scopes, process environment/read files and whole-program binary fixtures may prevent otherwise expected reuse.

WB's actual coverage scope follows Imports, TestImports and XTestImports (`internal/quality/coverage_scope.go:65`, graph.go:104-133). A shared test helper importing a root/tree that registers all families will recouple every consumer, and is both a package dependency and an affected-scope problem. Do not promise “only family tests ever run.” Preserve governed changed-package and coverage closures, including reverse-dependent tests. No CI policy change is needed to gain cheaper tests; after extraction root's required rerun becomes cheaper because heavyweight domain tests have moved.

Coverage profile paths move, and cross-package execution without the appropriate coverpkg cannot automatically credit new package statements. Let WB calculate existing scope and retain required aggregate/profile checks; compare before/after by behavior and source mapping rather than claiming line moves eliminate uncovered statements. Uncovered counts here prioritize potential seam work, not future coverage measurements. This map supplies no timing receipts; pilot/runtime work must supply measured results.

## Architecture risks and acceptance criteria

No blocker in the proposed boundary design. Major implementation risks to explicitly gate: copied invocation values at registration; losing root pre-run policy; facade packages importing one another; daemon runtime remaining Cobra-shaped; expensive tests left on main wrappers; lost TestMain isolation or cross-platform build tags; coverage closure/instrumentation drift. Minor risk: package count grows without payoff for tiny leaves; use coherent maintenance/install grouping where it does not create reverse sibling dependencies, and promote shared helpers only after actual reuse.

Acceptance is evidence-based: command contract tests run with fake operation deps; domain tests own real boundaries; root keeps registry and executable contracts; no API-breaking flag/output/exit changes; existing required checks pass. The inventory proposes boundaries; it is not an implementation or landing verdict.

## Appendix A — every production file

All paths below are relative to canonical `cmd/wb`. Counts use supplied historical coverage profile. Destination labels ending service require splitting the file by responsibility, not moving the entire file into that command package.

| File | Proposed owner(s) | LOC | Uncovered / statements | Move/split rule |
|---|---|---:|---:|---|
| `admission.go` | cli mutation admission | 95 | 3 / 41 | CLI flags adapter plus existing worktree admission operation; dependency injected. |
| `agent.go` | cmdagent | 803 | 33 / 384 | Command arguments, operation seam and local rendering; domain operations stay in existing internal packages. |
| `agent_remote.go` | cmdagent | 176 | 18 / 89 | Command arguments, operation seam and local rendering; domain operations stay in existing internal packages. |
| `archive.go` | cmdarchive | 158 | 5 / 60 | Command arguments, operation seam and local rendering; domain operations stay in existing internal packages. |
| `branch.go` | cmdbranch | 541 | 25 / 245 | Command arguments, operation seam and local rendering; domain operations stay in existing internal packages. |
| `branch_archive_target.go` | cmdbranch | 65 | 6 / 29 | Command arguments, operation seam and local rendering; domain operations stay in existing internal packages. |
| `browser.go` | browser service | 42 | 2 / 16 | Platform launcher with injected command runner. |
| `campaign_progress.go` | cli shared presentation | 139 | 2 / 67 | Reusable leaf presentation with explicit writer/clock; no family/root imports. |
| `ci.go` | cmdci + shared exit error | 388 | 14 / 186 | CI adapters/progress in cmdci; shared exit error is CLI leaf. |
| `ci_wait_progress.go` | cmdci + shared exit error | 139 | 0 / 64 | CI adapters/progress in cmdci; shared exit error is CLI leaf. |
| `cockpit.go` | cmdcockpit | 272 | 0 / 111 | Command arguments, operation seam and local rendering; domain operations stay in existing internal packages. |
| `cockpit_export.go` | cmdcockpit | 292 | 0 / 100 | Command arguments, operation seam and local rendering; domain operations stay in existing internal packages. |
| `commands.go` | root | 171 | 2 / 69 | Split: executable in cmd/wb; registry/help/catalog in internal/cli; contracts in internal/cli/shared; version model in build-info service. |
| `coverage_ratchet.go` | cmdquality | 439 | 1 / 213 | Coverage/verify/check/deadcode adapters; algorithms and targets in quality/selection services. |
| `coverage_summary.go` | cmdquality | 58 | 0 / 20 | Coverage/verify/check/deadcode adapters; algorithms and targets in quality/selection services. |
| `coverage_worklist.go` | cmdquality | 87 | 0 / 39 | Coverage/verify/check/deadcode adapters; algorithms and targets in quality/selection services. |
| `daemon.go` | daemon runtime + cmddaemon | 2825 | 156 / 1359 | Split constructors/renderers from runtime/controller/client/bridge/platform ownership. |
| `daemon_cockpit.go` | daemon runtime + cmddaemon | 366 | 0 / 141 | Split constructors/renderers from runtime/controller/client/bridge/platform ownership. |
| `daemon_file_bridge.go` | daemon runtime + cmddaemon | 959 | 29 / 556 | Split constructors/renderers from runtime/controller/client/bridge/platform ownership. |
| `daemon_file_bridge_mode_unix.go` | daemon runtime + cmddaemon | 20 | 1 / 6 | Split constructors/renderers from runtime/controller/client/bridge/platform ownership. |
| `daemon_file_bridge_mode_windows.go` | daemon runtime + cmddaemon | 90 | 0 / 0 | Split constructors/renderers from runtime/controller/client/bridge/platform ownership. |
| `daemon_hub.go` | daemon runtime + cmddaemon | 769 | 26 / 311 | Split constructors/renderers from runtime/controller/client/bridge/platform ownership. |
| `daemon_hub_webhook.go` | daemon runtime + cmddaemon | 155 | 0 / 40 | Split constructors/renderers from runtime/controller/client/bridge/platform ownership. |
| `daemon_identity.go` | daemon runtime + cmddaemon | 269 | 10 / 97 | Split constructors/renderers from runtime/controller/client/bridge/platform ownership. |
| `daemon_lifecycle_lock_unix.go` | daemon runtime + cmddaemon | 21 | 0 / 3 | Split constructors/renderers from runtime/controller/client/bridge/platform ownership. |
| `daemon_lifecycle_lock_windows.go` | daemon runtime + cmddaemon | 21 | 0 / 0 | Split constructors/renderers from runtime/controller/client/bridge/platform ownership. |
| `daemon_local_unix.go` | daemon runtime + cmddaemon | 99 | 16 / 42 | Split constructors/renderers from runtime/controller/client/bridge/platform ownership. |
| `daemon_local_windows.go` | daemon runtime + cmddaemon | 34 | 0 / 0 | Split constructors/renderers from runtime/controller/client/bridge/platform ownership. |
| `daemon_operation.go` | daemon runtime + cmddaemon | 314 | 14 / 162 | Split constructors/renderers from runtime/controller/client/bridge/platform ownership. |
| `daemon_peers.go` | daemon runtime + cmddaemon | 330 | 11 / 140 | Split constructors/renderers from runtime/controller/client/bridge/platform ownership. |
| `daemon_process_darwin.go` | daemon runtime + cmddaemon | 432 | 0 / 0 | Split constructors/renderers from runtime/controller/client/bridge/platform ownership. |
| `daemon_process_unix.go` | daemon runtime + cmddaemon | 72 | 22 / 28 | Split constructors/renderers from runtime/controller/client/bridge/platform ownership. |
| `daemon_process_windows.go` | daemon runtime + cmddaemon | 74 | 0 / 0 | Split constructors/renderers from runtime/controller/client/bridge/platform ownership. |
| `daemon_rpc.go` | daemon runtime + cmddaemon | 141 | 11 / 67 | Split constructors/renderers from runtime/controller/client/bridge/platform ownership. |
| `daemon_startlog.go` | daemon runtime + cmddaemon | 13 | 0 / 1 | Split constructors/renderers from runtime/controller/client/bridge/platform ownership. |
| `daemon_startlog_darwin.go` | daemon runtime + cmddaemon | 30 | 0 / 0 | Split constructors/renderers from runtime/controller/client/bridge/platform ownership. |
| `daemon_startlog_other.go` | daemon runtime + cmddaemon | 32 | 1 / 6 | Split constructors/renderers from runtime/controller/client/bridge/platform ownership. |
| `dashboard.go` | cmdcockpit (dashboard alias) | 129 | 10 / 49 | Compatibility alias and request adapter share cockpit service; no sibling command import. |
| `deadcode.go` | cmdquality | 177 | 3 / 65 | Coverage/verify/check/deadcode adapters; algorithms and targets in quality/selection services. |
| `deps.go` | cmddeps + dependency service | 1065 | 52 / 540 | Flags/request/rendering in cmddeps; operations in existing deps/policy/publish service. |
| `deps_go_directive.go` | cmddeps + dependency service | 370 | 9 / 165 | Flags/request/rendering in cmddeps; operations in existing deps/policy/publish service. |
| `deps_policy.go` | cmddeps + dependency service | 532 | 23 / 256 | Flags/request/rendering in cmddeps; operations in existing deps/policy/publish service. |
| `deps_policy_fleet.go` | cmddeps + dependency service | 435 | 20 / 240 | Flags/request/rendering in cmddeps; operations in existing deps/policy/publish service. |
| `deps_propagate.go` | cmddeps + dependency service | 287 | 10 / 120 | Flags/request/rendering in cmddeps; operations in existing deps/policy/publish service. |
| `deps_publish.go` | cmddeps + dependency service | 693 | 22 / 341 | Flags/request/rendering in cmddeps; operations in existing deps/policy/publish service. |
| `disk.go` | cmddisk | 119 | 5 / 31 | Command arguments, operation seam and local rendering; domain operations stay in existing internal packages. |
| `fleet.go` | fleet selection service | 93 | 2 / 47 | Reusable explicit root/filter/org selection below command families. |
| `fleet_cmd.go` | cmdfleet + fleet service | 631 | 22 / 305 | Flags/renderers in cmdfleet; aggregation/default-branch/merge-policy operation in service. |
| `fleet_coverage.go` | cmdquality | 219 | 0 / 106 | Coverage/verify/check/deadcode adapters; algorithms and targets in quality/selection services. |
| `fleet_default_branch.go` | cmdfleet + fleet service | 2598 | 85 / 1616 | Flags/renderers in cmdfleet; aggregation/default-branch/merge-policy operation in service. |
| `fleet_merge_policy.go` | cmdfleet + fleet service | 1148 | 60 / 616 | Flags/renderers in cmdfleet; aggregation/default-branch/merge-policy operation in service. |
| `fleet_prs.go` | cmdfleet + fleet service | 136 | 5 / 65 | Flags/renderers in cmdfleet; aggregation/default-branch/merge-policy operation in service. |
| `help.go` | root | 196 | 3 / 89 | Split: executable in cmd/wb; registry/help/catalog in internal/cli; contracts in internal/cli/shared; version model in build-info service. |
| `hooks.go` | cmdhooks | 646 | 57 / 311 | Command arguments, operation seam and local rendering; domain operations stay in existing internal packages. |
| `hooks_agent.go` | cmdhooks | 514 | 9 / 133 | Command arguments, operation seam and local rendering; domain operations stay in existing internal packages. |
| `hooks_lifecycle.go` | cmdhooks | 345 | 51 / 185 | Command arguments, operation seam and local rendering; domain operations stay in existing internal packages. |
| `install.go` | cmdinstall | 133 | 0 / 18 | Command arguments, operation seam and local rendering; domain operations stay in existing internal packages. |
| `landing_lane.go` | cli landing/lifecycle contracts | 81 | 2 / 18 | Split Cobra/stream binding from landing/lifecycle/session identity service. |
| `layout.go` | cmdlayout | 333 | 25 / 127 | Command arguments, operation seam and local rendering; domain operations stay in existing internal packages. |
| `lifecycle_hooks.go` | cli landing/lifecycle contracts | 40 | 2 / 11 | Split Cobra/stream binding from landing/lifecycle/session identity service. |
| `live_progress.go` | cli shared presentation | 178 | 1 / 76 | Reusable leaf presentation with explicit writer/clock; no family/root imports. |
| `locallink_guard.go` | cli landing/lifecycle contracts | 244 | 7 / 67 | Split Cobra/stream binding from landing/lifecycle/session identity service. |
| `main.go` | root | 678 | 3 / 204 | Split: executable in cmd/wb; registry/help/catalog in internal/cli; contracts in internal/cli/shared; version model in build-info service. |
| `migrate.go` | cmdmigrate | 298 | 23 / 161 | Command arguments, operation seam and local rendering; domain operations stay in existing internal packages. |
| `output_format.go` | cli shared presentation | 45 | 0 / 12 | Reusable leaf presentation with explicit writer/clock; no family/root imports. |
| `peers.go` | cmdpeers | 695 | 38 / 338 | Command arguments, operation seam and local rendering; domain operations stay in existing internal packages. |
| `peers_join.go` | cmdpeers | 248 | 17 / 98 | Command arguments, operation seam and local rendering; domain operations stay in existing internal packages. |
| `pr.go` | cmdpr | 430 | 14 / 179 | Command arguments, operation seam and local rendering; domain operations stay in existing internal packages. |
| `pr_create.go` | cmdpr | 382 | 12 / 147 | Command arguments, operation seam and local rendering; domain operations stay in existing internal packages. |
| `quality.go` | cmdquality | 936 | 44 / 517 | Coverage/verify/check/deadcode adapters; algorithms and targets in quality/selection services. |
| `quality_progress.go` | cmdquality | 82 | 0 / 33 | Coverage/verify/check/deadcode adapters; algorithms and targets in quality/selection services. |
| `quiet.go` | cli landing/lifecycle contracts | 96 | 0 / 23 | Split Cobra/stream binding from landing/lifecycle/session identity service. |
| `remote.go` | cmdremote + remote service | 134 | 1 / 37 | CLI rendering in cmdremote; reusable config/provider/identity/claim composition below CLI. |
| `remote_autoclaim.go` | cmdremote + remote service | 272 | 11 / 77 | CLI rendering in cmdremote; reusable config/provider/identity/claim composition below CLI. |
| `remote_claim.go` | cmdremote + remote service | 167 | 5 / 78 | CLI rendering in cmdremote; reusable config/provider/identity/claim composition below CLI. |
| `remote_claims.go` | cmdremote + remote service | 53 | 2 / 22 | CLI rendering in cmdremote; reusable config/provider/identity/claim composition below CLI. |
| `remote_collect.go` | cmdremote + remote service | 167 | 1 / 71 | CLI rendering in cmdremote; reusable config/provider/identity/claim composition below CLI. |
| `remote_enroll.go` | cmdremote + remote service | 207 | 10 / 94 | CLI rendering in cmdremote; reusable config/provider/identity/claim composition below CLI. |
| `remote_machines.go` | cmdremote + remote service | 44 | 1 / 18 | CLI rendering in cmdremote; reusable config/provider/identity/claim composition below CLI. |
| `remote_publish.go` | cmdremote + remote service | 177 | 2 / 69 | CLI rendering in cmdremote; reusable config/provider/identity/claim composition below CLI. |
| `remote_publish_progress.go` | cmdremote + remote service | 97 | 0 / 41 | CLI rendering in cmdremote; reusable config/provider/identity/claim composition below CLI. |
| `remote_release.go` | cmdremote + remote service | 68 | 2 / 26 | CLI rendering in cmdremote; reusable config/provider/identity/claim composition below CLI. |
| `remote_render.go` | cmdremote + remote service | 267 | 0 / 103 | CLI rendering in cmdremote; reusable config/provider/identity/claim composition below CLI. |
| `remote_status.go` | cmdremote + remote service | 139 | 2 / 63 | CLI rendering in cmdremote; reusable config/provider/identity/claim composition below CLI. |
| `repo.go` | cmdrepo | 114 | 16 / 56 | Command arguments, operation seam and local rendering; domain operations stay in existing internal packages. |
| `repo_ignore.go` | cmdrepo | 48 | 1 / 16 | Command arguments, operation seam and local rendering; domain operations stay in existing internal packages. |
| `repo_init_remote.go` | cmdrepo | 100 | 0 / 31 | Command arguments, operation seam and local rendering; domain operations stay in existing internal packages. |
| `report.go` | cli shared presentation | 34 | 0 / 11 | Reusable leaf presentation with explicit writer/clock; no family/root imports. |
| `run.go` | cmdrun | 694 | 37 / 352 | Command arguments, operation seam and local rendering; domain operations stay in existing internal packages. |
| `run_changed.go` | cmdrun | 56 | 0 / 18 | Command arguments, operation seam and local rendering; domain operations stay in existing internal packages. |
| `run_queue_progress.go` | cmdrun | 106 | 1 / 21 | Command arguments, operation seam and local rendering; domain operations stay in existing internal packages. |
| `secretscan.go` | secret-scan service + CLI advisories | 57 | 1 / 10 | Scanner loader in domain service; writer/advisories in CLI adapter. |
| `selfupdate.go` | cmdinstall | 286 | 8 / 69 | Command arguments, operation seam and local rendering; domain operations stay in existing internal packages. |
| `session.go` | cmdsession | 93 | 5 / 45 | Session adapters; launch/move/identity/secret operations in existing session services. |
| `session_attribution.go` | cmdsession | 86 | 0 / 30 | Session adapters; launch/move/identity/secret operations in existing session services. |
| `session_list.go` | cmdsession | 162 | 17 / 72 | Session adapters; launch/move/identity/secret operations in existing session services. |
| `session_message.go` | cmdsession | 353 | 18 / 179 | Session adapters; launch/move/identity/secret operations in existing session services. |
| `session_move.go` | cmdsession | 559 | 29 / 293 | Session adapters; launch/move/identity/secret operations in existing session services. |
| `session_park.go` | cmdsession | 670 | 66 / 352 | Session adapters; launch/move/identity/secret operations in existing session services. |
| `session_prune.go` | cmdsession | 35 | 2 / 10 | Session adapters; launch/move/identity/secret operations in existing session services. |
| `session_receive.go` | cmdsession | 135 | 21 / 54 | Session adapters; launch/move/identity/secret operations in existing session services. |
| `session_receive_park.go` | cmdsession | 118 | 15 / 47 | Session adapters; launch/move/identity/secret operations in existing session services. |
| `session_register.go` | cmdsession | 131 | 0 / 45 | Session adapters; launch/move/identity/secret operations in existing session services. |
| `skills.go` | cmdskills | 76 | 1 / 23 | Command arguments, operation seam and local rendering; domain operations stay in existing internal packages. |
| `skills_hook.go` | cmdskills | 62 | 0 / 12 | Command arguments, operation seam and local rendering; domain operations stay in existing internal packages. |
| `skills_hook_install.go` | cmdskills | 106 | 4 / 43 | Command arguments, operation seam and local rendering; domain operations stay in existing internal packages. |
| `skills_hook_print.go` | cmdskills | 41 | 1 / 7 | Command arguments, operation seam and local rendering; domain operations stay in existing internal packages. |
| `skills_hook_run.go` | cmdskills | 60 | 2 / 19 | Command arguments, operation seam and local rendering; domain operations stay in existing internal packages. |
| `skills_sync.go` | cmdskills | 256 | 6 / 88 | Command arguments, operation seam and local rendering; domain operations stay in existing internal packages. |
| `status.go` | cmdstatus | 354 | 10 / 145 | Command arguments, operation seam and local rendering; domain operations stay in existing internal packages. |
| `status_progress.go` | cmdstatus | 122 | 0 / 45 | Command arguments, operation seam and local rendering; domain operations stay in existing internal packages. |
| `stream.go` | cmdstream + stream adapters | 854 | 27 / 339 | Flags/rendering in cmdstream; engine composition and worktree/identity/hooks adapters below CLI. |
| `stream_adapters.go` | cmdstream + stream adapters | 208 | 4 / 78 | Flags/rendering in cmdstream; engine composition and worktree/identity/hooks adapters below CLI. |
| `stream_sync.go` | cmdstream + stream adapters | 413 | 13 / 183 | Flags/rendering in cmdstream; engine composition and worktree/identity/hooks adapters below CLI. |
| `sync.go` | cmdsync | 530 | 9 / 247 | Command arguments, operation seam and local rendering; domain operations stay in existing internal packages. |
| `sync_report.go` | cmdsync | 104 | 0 / 32 | Command arguments, operation seam and local rendering; domain operations stay in existing internal packages. |
| `sync_report_command.go` | cmdsync | 142 | 16 / 58 | Command arguments, operation seam and local rendering; domain operations stay in existing internal packages. |
| `task.go` | cmdtask | 280 | 24 / 149 | Command arguments, operation seam and local rendering; domain operations stay in existing internal packages. |
| `upgrade.go` | cmdinstall | 96 | 0 / 13 | Command arguments, operation seam and local rendering; domain operations stay in existing internal packages. |
| `verify_receipt.go` | cmdworktree (verify receipt) | 263 | 23 / 139 | Nested worktree verify receipt adapter; receipt validation in internal receipt/worktree service. |
| `version.go` | root | 114 | 6 / 29 | Split: executable in cmd/wb; registry/help/catalog in internal/cli; contracts in internal/cli/shared; version model in build-info service. |
| `wait.go` | cmdwait | 773 | 24 / 327 | Command arguments, operation seam and local rendering; domain operations stay in existing internal packages. |
| `wbhome_diagnostic.go` | root | 63 | 0 / 14 | Split: executable in cmd/wb; registry/help/catalog in internal/cli; contracts in internal/cli/shared; version model in build-info service. |
| `worker.go` | cmdworker | 381 | 38 / 224 | Command arguments, operation seam and local rendering; domain operations stay in existing internal packages. |
| `worktree.go` | cmdworktree | 3062 | 77 / 1392 | Worktree/create/land adapters, keep worktree mutation/orchestration below CLI. |
| `worktree_active.go` | cmdworktree | 372 | 3 / 156 | Worktree/create/land adapters, keep worktree mutation/orchestration below CLI. |
| `worktree_collaboration.go` | cmdworktree | 318 | 0 / 171 | Worktree/create/land adapters, keep worktree mutation/orchestration below CLI. |
| `worktree_end.go` | cmdworktree | 311 | 10 / 120 | Worktree/create/land adapters, keep worktree mutation/orchestration below CLI. |
| `worktree_gc.go` | cmdworktree | 255 | 2 / 94 | Worktree/create/land adapters, keep worktree mutation/orchestration below CLI. |
| `worktree_marker.go` | cmdworktree | 410 | 14 / 183 | Worktree/create/land adapters, keep worktree mutation/orchestration below CLI. |
| `worktree_merge.go` | cmdworktree | 1494 | 16 / 675 | Worktree/create/land adapters, keep worktree mutation/orchestration below CLI. |
| `worktree_own.go` | cmdworktree | 97 | 1 / 35 | Worktree/create/land adapters, keep worktree mutation/orchestration below CLI. |
| `worktree_progress.go` | cmdworktree | 120 | 0 / 41 | Worktree/create/land adapters, keep worktree mutation/orchestration below CLI. |
| `worktree_rescue.go` | cmdworktree | 207 | 8 / 101 | Worktree/create/land adapters, keep worktree mutation/orchestration below CLI. |
| `worktree_retire.go` | cmdworktree | 153 | 9 / 71 | Worktree/create/land adapters, keep worktree mutation/orchestration below CLI. |

## Appendix B — every test file and harness obligation

Ownership uses named local production call candidates plus filename intent. It is a migration map, not AST-proven statement ownership. Named call candidates are cross-checked with CodeGrapher at the base revision; final test ownership must be confirmed by each cohort. `split` means individual tests move by subject; `replace root harness` means root execution is a convenience to remove for command-only assertions, while root-level policy assertions remain root contracts. Executable journey rows retain real program coverage; internal domain checks in those files can still move. Every moved real integration test must retain process/user/Git/admission isolation.

| Test file | Proposed owners | Test boundary and harness obligation |
|---|---|---|
| `admission_test.go` | cli mutation admission; cmdworktree | Move command contract tests; move operations/fixtures to domain service; replace root harness for family-only assertions; retain isolation until Env/Cwd dependency replaces process mutation |
| `agent_print_await_test.go` | cmdagent | Move command contract tests; move operations/fixtures to domain service |
| `agent_remote_operation_test.go` | cmdagent | Move command contract tests; move operations/fixtures to domain service |
| `agent_remote_test.go` | cmdagent | Move command contract tests; move operations/fixtures to domain service; replace root harness for family-only assertions; retain isolation until Env/Cwd dependency replaces process mutation |
| `agent_test.go` | cmdagent | Move command contract tests; move operations/fixtures to domain service; replace root harness for family-only assertions; retain isolation until Env/Cwd dependency replaces process mutation |
| `archive_test.go` | cmdarchive | Keep executable journey; move pure/domain assertions separately; retain isolation until Env/Cwd dependency replaces process mutation |
| `branch_pr_evidence_test.go` | cmdbranch | Move command contract tests; move operations/fixtures to domain service |
| `branch_print_test.go` | cmdbranch | Move command contract tests; move operations/fixtures to domain service |
| `branch_test.go` | cmdbranch; cmdworktree | Move command contract tests; move operations/fixtures to domain service; replace root harness for family-only assertions |
| `browser_test.go` | browser service | Move command contract tests; move operations/fixtures to domain service |
| `campaign_progress_test.go` | cli shared presentation | Move command contract tests; move operations/fixtures to domain service |
| `canonical_branch_independence_e2e_test.go` | executable canonical/worktree/secure-hook journey | Keep executable journey; move pure/domain assertions separately |
| `ci_test.go` | cmdci + shared exit error | Move command contract tests; move operations/fixtures to domain service |
| `ci_unix_test.go` | cmdci + shared exit error | Move command contract tests; move operations/fixtures to domain service |
| `ci_wait_audit_test.go` | cmdci + shared exit error | Move command contract tests; move operations/fixtures to domain service |
| `ci_wait_progress_test.go` | cmdci + shared exit error | Move command contract tests; move operations/fixtures to domain service |
| `ci_wait_test.go` | cmdci + shared exit error | Move command contract tests; move operations/fixtures to domain service; replace root harness for family-only assertions; retain isolation until Env/Cwd dependency replaces process mutation |
| `cli_smoke_test.go` | root contracts / cli leaf as asserted | Keep executable journey; move pure/domain assertions separately; replace root harness for family-only assertions |
| `cockpit_export_test.go` | cmdcockpit | Move command contract tests; move operations/fixtures to domain service; replace root harness for family-only assertions |
| `cockpit_test.go` | cmdcockpit; daemon runtime + cmddaemon | Move command contract tests; move operations/fixtures to domain service |
| `commands_test.go` | root; root contracts / cli leaf as asserted | Keep actual root contracts; split leaf/family assertions |
| `coverage_ratchet_filewrite_injected_test.go` | cmddeps + dependency service; cmdfleet + fleet service; cmdquality | Move command contract tests; move operations/fixtures to domain service |
| `coverage_ratchet_redbase_e2e_test.go` | cmdquality | Keep executable journey; move pure/domain assertions separately; replace root harness for family-only assertions |
| `coverage_ratchet_redbase_test.go` | cmdquality | Move command contract tests; move operations/fixtures to domain service |
| `coverage_ratchet_test.go` | cmdquality | Move command contract tests; move operations/fixtures to domain service; replace root harness for family-only assertions; retain isolation until Env/Cwd dependency replaces process mutation |
| `coverage_ratchet_tolerance_e2e_test.go` | cmdquality | Keep executable journey; move pure/domain assertions separately; replace root harness for family-only assertions |
| `coverage_ratchet_tolerance_test.go` | cmdquality | Move command contract tests; move operations/fixtures to domain service; replace root harness for family-only assertions |
| `coverage_scope_e2e_test.go` | executable selected-scope journey; cmdquality; internal/quality | Keep executable journey; move pure/domain assertions separately; replace root harness for family-only assertions |
| `coverage_scope_errors_e2e_test.go` | cmdquality | Keep executable journey; move pure/domain assertions separately; replace root harness for family-only assertions |
| `coverage_scope_test.go` | cmdquality; root contracts / cli leaf as asserted | Keep actual root contracts; split leaf/family assertions |
| `coverage_summary_test.go` | cmdquality | Move command contract tests; move operations/fixtures to domain service; replace root harness for family-only assertions |
| `coverage_worklist_test.go` | cmdquality | Move command contract tests; move operations/fixtures to domain service; replace root harness for family-only assertions |
| `daemon_cockpit_e2e_test.go` | daemon runtime + cmddaemon | Keep executable journey; move pure/domain assertions separately |
| `daemon_cockpit_remote_test.go` | daemon runtime + cmddaemon | Move command contract tests; move operations/fixtures to domain service |
| `daemon_cockpit_test.go` | daemon runtime + cmddaemon | Move command contract tests; move operations/fixtures to domain service |
| `daemon_file_bridge_test.go` | daemon runtime + cmddaemon | Move command contract tests; move operations/fixtures to domain service |
| `daemon_home_test.go` | daemon runtime + cmddaemon | Move command contract tests; move operations/fixtures to domain service; retain isolation until Env/Cwd dependency replaces process mutation |
| `daemon_hub_redeliver_test.go` | daemon runtime + cmddaemon | Move command contract tests; move operations/fixtures to domain service |
| `daemon_hub_test.go` | daemon runtime + cmddaemon | Move command contract tests; move operations/fixtures to domain service; replace root harness for family-only assertions |
| `daemon_hub_webhook_test.go` | daemon runtime + cmddaemon | Move command contract tests; move operations/fixtures to domain service |
| `daemon_hub_writes_test.go` | daemon runtime + cmddaemon | Move command contract tests; move operations/fixtures to domain service |
| `daemon_identity_linux_test.go` | daemon runtime + cmddaemon | Move command contract tests; move operations/fixtures to domain service |
| `daemon_identity_test.go` | daemon runtime + cmddaemon | Move command contract tests; move operations/fixtures to domain service |
| `daemon_local_test.go` | daemon runtime + cmddaemon | Move command contract tests; move operations/fixtures to domain service |
| `daemon_local_unix_test.go` | daemon runtime + cmddaemon | Move command contract tests; move operations/fixtures to domain service; retain isolation until Env/Cwd dependency replaces process mutation |
| `daemon_operation_progress_test.go` | daemon runtime + cmddaemon | Move command contract tests; move operations/fixtures to domain service |
| `daemon_operation_start_test.go` | daemon runtime + cmddaemon | Move command contract tests; move operations/fixtures to domain service |
| `daemon_peers_failure_test.go` | daemon runtime + cmddaemon | Move command contract tests; move operations/fixtures to domain service |
| `daemon_peers_read_test.go` | daemon runtime + cmddaemon | Move command contract tests; move operations/fixtures to domain service |
| `daemon_peers_test.go` | daemon runtime + cmddaemon | Move command contract tests; move operations/fixtures to domain service |
| `daemon_poller_test.go` | daemon runtime + cmddaemon | Move command contract tests; move operations/fixtures to domain service |
| `daemon_process_darwin_test.go` | daemon runtime + cmddaemon | Move command contract tests; move operations/fixtures to domain service; retain isolation until Env/Cwd dependency replaces process mutation |
| `daemon_process_unix_test.go` | daemon runtime + cmddaemon | Move command contract tests; move operations/fixtures to domain service |
| `daemon_publish_e2e_test.go` | cmdquality; daemon runtime + cmddaemon | Keep executable journey; move pure/domain assertions separately |
| `daemon_publish_test.go` | cmdremote + remote service; daemon runtime + cmddaemon | Move command contract tests; move operations/fixtures to domain service; replace root harness for family-only assertions |
| `daemon_raw_execution_policy_test.go` | daemon runtime + cmddaemon | Move command contract tests; move operations/fixtures to domain service |
| `daemon_replace_other_root_test.go` | daemon runtime + cmddaemon | Move command contract tests; move operations/fixtures to domain service |
| `daemon_shutdown_test.go` | daemon runtime + cmddaemon | Move command contract tests; move operations/fixtures to domain service |
| `daemon_state_lock_test.go` | daemon runtime + cmddaemon | Move command contract tests; move operations/fixtures to domain service |
| `daemon_supervisor_presence_test.go` | daemon runtime + cmddaemon | Move command contract tests; move operations/fixtures to domain service |
| `daemon_supervisor_test.go` | daemon runtime + cmddaemon | Move command contract tests; move operations/fixtures to domain service; replace root harness for family-only assertions; retain isolation until Env/Cwd dependency replaces process mutation |
| `daemon_supervisor_unix_test.go` | daemon runtime + cmddaemon | Move command contract tests; move operations/fixtures to domain service; retain isolation until Env/Cwd dependency replaces process mutation |
| `daemon_test.go` | daemon runtime + cmddaemon | Move command contract tests; move operations/fixtures to domain service |
| `daemon_windows_test.go` | daemon runtime + cmddaemon | Move command contract tests; move operations/fixtures to domain service |
| `dashboard_test.go` | cmdcockpit (dashboard alias) | Move command contract tests; move operations/fixtures to domain service; replace root harness for family-only assertions |
| `deadcode_command_test.go` | cmdquality | Move command contract tests; move operations/fixtures to domain service |
| `deps_bump_test.go` | cli shared presentation; cmddeps + dependency service | Move command contract tests; move operations/fixtures to domain service |
| `deps_go_directive_sort_test.go` | cmddeps + dependency service | Move command contract tests; move operations/fixtures to domain service |
| `deps_go_directive_test.go` | cmddeps + dependency service | Move command contract tests; move operations/fixtures to domain service |
| `deps_options_validation_test.go` | cmddeps + dependency service; fleet selection service | Move command contract tests; move operations/fixtures to domain service |
| `deps_peers_test.go` | cmddeps + dependency service | Move command contract tests; move operations/fixtures to domain service; retain isolation until Env/Cwd dependency replaces process mutation |
| `deps_policy_fleet_test.go` | cmddeps + dependency service | Move command contract tests; move operations/fixtures to domain service |
| `deps_policy_show_test.go` | cmddeps + dependency service | Move command contract tests; move operations/fixtures to domain service |
| `deps_policy_test.go` | cmddeps + dependency service | Move command contract tests; move operations/fixtures to domain service |
| `deps_propagate_test.go` | cmddeps + dependency service | Move command contract tests; move operations/fixtures to domain service; replace root harness for family-only assertions; retain isolation until Env/Cwd dependency replaces process mutation |
| `deps_propagate_write_failure_test.go` | cmddeps + dependency service | Move command contract tests; move operations/fixtures to domain service; replace root harness for family-only assertions |
| `deps_publish_test.go` | cmddeps + dependency service | Move command contract tests; move operations/fixtures to domain service; retain isolation until Env/Cwd dependency replaces process mutation |
| `deps_test.go` | cmddeps + dependency service | Move command contract tests; move operations/fixtures to domain service; replace root harness for family-only assertions; retain isolation until Env/Cwd dependency replaces process mutation |
| `disk_format_test.go` | cmddisk | Move command contract tests; move operations/fixtures to domain service; retain isolation until Env/Cwd dependency replaces process mutation |
| `disk_test.go` | cmddisk | Move command contract tests; move operations/fixtures to domain service; retain isolation until Env/Cwd dependency replaces process mutation |
| `dispatch_secure_helpers_e2e_test.go` | executable private-helper dispatch; hooks/worktree services | Keep executable journey; move pure/domain assertions separately |
| `dispatch_test.go` | cmdhooks; root contracts / cli leaf as asserted | Keep actual root contracts; split leaf/family assertions; replace root harness for family-only assertions; retain isolation until Env/Cwd dependency replaces process mutation |
| `fleet_cmd_test.go` | cmdfleet + fleet service | Keep executable journey; move pure/domain assertions separately |
| `fleet_coverage_test.go` | cmdquality | Move command contract tests; move operations/fixtures to domain service; replace root harness for family-only assertions; retain isolation until Env/Cwd dependency replaces process mutation |
| `fleet_default_branch_apply_failures_test.go` | cmdfleet + fleet service | Move command contract tests; move operations/fixtures to domain service; replace root harness for family-only assertions |
| `fleet_default_branch_discovery_failures_test.go` | cmdfleet + fleet service | Move command contract tests; move operations/fixtures to domain service |
| `fleet_default_branch_pages_failures_test.go` | cmdfleet + fleet service | Move command contract tests; move operations/fixtures to domain service |
| `fleet_default_branch_reconcile_failures_test.go` | cmdfleet + fleet service | Move command contract tests; move operations/fixtures to domain service |
| `fleet_default_branch_test.go` | cmdfleet + fleet service | Move command contract tests; move operations/fixtures to domain service; replace root harness for family-only assertions |
| `fleet_default_branch_workflow_failures_test.go` | cmdfleet + fleet service | Move command contract tests; move operations/fixtures to domain service |
| `fleet_default_branch_workflow_read_failures_test.go` | cmdfleet + fleet service | Move command contract tests; move operations/fixtures to domain service |
| `fleet_hooks_rollup_test.go` | cmdfleet + fleet service | Move command contract tests; move operations/fixtures to domain service |
| `fleet_local_scan_test.go` | fleet selection service | Move command contract tests; move operations/fixtures to domain service |
| `fleet_merge_policy_test.go` | cmdfleet + fleet service | Move command contract tests; move operations/fixtures to domain service; replace root harness for family-only assertions |
| `fleet_remote_rollup_behavior_test.go` | cmdfleet + fleet service | Move command contract tests; move operations/fixtures to domain service |
| `help_discovery_test.go` | root; root contracts / cli leaf as asserted | Keep actual root contracts; split leaf/family assertions; replace root harness for family-only assertions; retain isolation until Env/Cwd dependency replaces process mutation |
| `help_test.go` | root; root contracts / cli leaf as asserted | Keep actual root contracts; split leaf/family assertions |
| `hooks_agent_entry_test.go` | cmdhooks | Move command contract tests; move operations/fixtures to domain service |
| `hooks_agent_install_command_test.go` | cmdhooks | Move command contract tests; move operations/fixtures to domain service |
| `hooks_agent_test.go` | cmdhooks | Move command contract tests; move operations/fixtures to domain service; replace root harness for family-only assertions; retain isolation until Env/Cwd dependency replaces process mutation |
| `hooks_fleet_test.go` | cmdhooks | Move command contract tests; move operations/fixtures to domain service |
| `hooks_lifecycle_behavior_test.go` | cmdhooks | Move command contract tests; move operations/fixtures to domain service; replace root harness for family-only assertions; retain isolation until Env/Cwd dependency replaces process mutation |
| `hooks_lifecycle_shortsha_test.go` | cmdhooks | Move command contract tests; move operations/fixtures to domain service |
| `hooks_lifecycle_test.go` | cmdhooks | Move command contract tests; move operations/fixtures to domain service; replace root harness for family-only assertions; retain isolation until Env/Cwd dependency replaces process mutation |
| `hooks_measure_test.go` | cmdhooks | Move command contract tests; move operations/fixtures to domain service; replace root harness for family-only assertions; retain isolation until Env/Cwd dependency replaces process mutation |
| `hooks_pushtier_integration_test.go` | cmdhooks | Keep executable journey; move pure/domain assertions separately |
| `hooks_test.go` | cmdhooks | Move command contract tests; move operations/fixtures to domain service; retain isolation until Env/Cwd dependency replaces process mutation |
| `hostload_admission_test.go` | cli mutation admission; cmdworktree | Move command contract tests; move operations/fixtures to domain service; replace root harness for family-only assertions; retain isolation until Env/Cwd dependency replaces process mutation |
| `install_test.go` | cmdinstall | Move command contract tests; move operations/fixtures to domain service; replace root harness for family-only assertions |
| `journey_process_unix_test.go` | executable test support (OS build tag) | Move command contract tests; move operations/fixtures to domain service |
| `journey_process_windows_test.go` | executable test support (OS build tag) | Move command contract tests; move operations/fixtures to domain service |
| `labels_test.go` | cmdsession; cmdskills; root contracts / cli leaf as asserted | Keep actual root contracts; split leaf/family assertions |
| `landed_incomplete_exit_test.go` | cmdpr; cmdworktree; root contracts / cli leaf as asserted | Keep actual root contracts; split leaf/family assertions; replace root harness for family-only assertions; retain isolation until Env/Cwd dependency replaces process mutation |
| `landing_lane_test.go` | cli landing/lifecycle contracts; cmdsession | Move command contract tests; move operations/fixtures to domain service |
| `layout_host_level_test.go` | cmdlayout | Keep executable journey; move pure/domain assertions separately |
| `layout_migrate_include_test.go` | cmdlayout | Move command contract tests; move operations/fixtures to domain service; retain isolation until Env/Cwd dependency replaces process mutation |
| `layout_migrate_output_test.go` | cmdlayout | Move command contract tests; move operations/fixtures to domain service |
| `layout_migrate_report_test.go` | cmdlayout | Move command contract tests; move operations/fixtures to domain service |
| `layout_migrate_test.go` | cmdlayout | Keep executable journey; move pure/domain assertions separately; retain isolation until Env/Cwd dependency replaces process mutation |
| `layout_test.go` | cmdlayout | Keep executable journey; move pure/domain assertions separately |
| `live_progress_test.go` | cli shared presentation | Move command contract tests; move operations/fixtures to domain service |
| `live_progress_update_test.go` | cli shared presentation | Move command contract tests; move operations/fixtures to domain service |
| `locallink_guard_test.go` | cli landing/lifecycle contracts | Move command contract tests; move operations/fixtures to domain service; retain isolation until Env/Cwd dependency replaces process mutation |
| `locallink_test.go` | cli landing/lifecycle contracts; root contracts / cli leaf as asserted | Keep actual root contracts; split leaf/family assertions |
| `main_test.go` | root; root contracts / cli leaf as asserted | Keep actual root contracts; split leaf/family assertions; replace root harness for family-only assertions; retain isolation until Env/Cwd dependency replaces process mutation |
| `merge_policy_test.go` | cmdfleet + fleet service; root contracts / cli leaf as asserted | Keep actual root contracts; split leaf/family assertions |
| `migrate_test.go` | cmdmigrate | Move command contract tests; move operations/fixtures to domain service |
| `module_archive_test.go` | root contracts / cli leaf as asserted | Keep actual root contracts; split leaf/family assertions |
| `native_coverage_test.go` | cmdquality; root contracts / cli leaf as asserted | Keep actual root contracts; split leaf/family assertions |
| `noglobals_test.go` | cmdsync; root contracts / cli leaf as asserted | Keep actual root contracts; split leaf/family assertions; replace root harness for family-only assertions |
| `output_format_test.go` | cli shared presentation; root contracts / cli leaf as asserted | Keep actual root contracts; split leaf/family assertions |
| `peers_get_refusal_test.go` | cmdpeers | Move command contract tests; move operations/fixtures to domain service |
| `peers_invite_test.go` | cmdpeers | Move command contract tests; move operations/fixtures to domain service |
| `peers_join_test.go` | cmdpeers | Move command contract tests; move operations/fixtures to domain service |
| `peers_state_test.go` | cmdpeers | Move command contract tests; move operations/fixtures to domain service |
| `peers_test.go` | cmdpeers; daemon runtime + cmddaemon | Move command contract tests; move operations/fixtures to domain service |
| `persistent_flags_test.go` | root contracts / cli leaf as asserted | Keep actual root contracts; split leaf/family assertions |
| `pr_create_link_preflight_test.go` | cli landing/lifecycle contracts; cmdpr | Move command contract tests; move operations/fixtures to domain service; retain isolation until Env/Cwd dependency replaces process mutation |
| `pr_create_test.go` | cmdpr | Move command contract tests; move operations/fixtures to domain service; retain isolation until Env/Cwd dependency replaces process mutation |
| `pr_helpers_test.go` | cmdpr | Move command contract tests; move operations/fixtures to domain service |
| `pr_print_test.go` | cmdpr; cmdquality; cmdsession | Move command contract tests; move operations/fixtures to domain service |
| `pr_test.go` | cli landing/lifecycle contracts; cmdpr | Move command contract tests; move operations/fixtures to domain service; retain isolation until Env/Cwd dependency replaces process mutation |
| `pr_update_output_test.go` | cmdpr | Move command contract tests; move operations/fixtures to domain service |
| `progress_nil_test.go` | cmdquality; root contracts / cli leaf as asserted | Keep actual root contracts; split leaf/family assertions |
| `progress_test.go` | cli shared presentation; cmdbranch; cmdci + shared exit error; root contracts / cli leaf as asserted | Keep actual root contracts; split leaf/family assertions |
| `projects_root_layout_test.go` | root invocation/path/ignored-WB_HOME contracts; daemon state service | Keep root policy contracts; move daemon path assertions to runtime; replace root harness for family-only assertions; retain isolation until Env/Cwd dependency replaces process mutation |
| `quality_progress_test.go` | cmdquality | Move command contract tests; move operations/fixtures to domain service |
| `quality_targets_test.go` | cmdquality | Move command contract tests; move operations/fixtures to domain service |
| `quality_test.go` | cmdquality; cmdstatus | Move command contract tests; move operations/fixtures to domain service |
| `quiet_test.go` | cli landing/lifecycle contracts; cmdworktree; root contracts / cli leaf as asserted | Keep actual root contracts; split leaf/family assertions; replace root harness for family-only assertions; retain isolation until Env/Cwd dependency replaces process mutation |
| `release_contract_test.go` | root contracts / cli leaf as asserted | Keep actual root contracts; split leaf/family assertions; replace root harness for family-only assertions |
| `remote_and_peers_cli_wiring_test.go` | cmdpeers; cmdremote + remote service; root contracts / cli leaf as asserted | Keep actual root contracts; split leaf/family assertions; retain isolation until Env/Cwd dependency replaces process mutation |
| `remote_claim_test.go` | cmdremote + remote service; cmdworktree | Move command contract tests; move operations/fixtures to domain service; replace root harness for family-only assertions; retain isolation until Env/Cwd dependency replaces process mutation |
| `remote_enroll_credential_test.go` | cmdremote + remote service | Move command contract tests; move operations/fixtures to domain service |
| `remote_enroll_test.go` | cmdremote + remote service | Move command contract tests; move operations/fixtures to domain service |
| `remote_publish_progress_test.go` | cmdremote + remote service | Move command contract tests; move operations/fixtures to domain service |
| `remote_status_test.go` | cmdremote + remote service | Move command contract tests; move operations/fixtures to domain service |
| `remote_test.go` | cmdremote + remote service; cmdsync | Move command contract tests; move operations/fixtures to domain service; retain isolation until Env/Cwd dependency replaces process mutation |
| `repo_ignore_test.go` | cmdrepo | Keep executable journey; move pure/domain assertions separately |
| `repo_init_remote_command_test.go` | cmdrepo | Move command contract tests; move operations/fixtures to domain service |
| `repo_init_remote_test.go` | cmdrepo | Keep executable journey; move pure/domain assertions separately |
| `repo_transfer_test.go` | cmdrepo | Move command contract tests; move operations/fixtures to domain service |
| `residue_hint_test.go` | root contracts / cli leaf as asserted | Keep actual root contracts; split leaf/family assertions; replace root harness for family-only assertions |
| `run_changed_test.go` | cmdrun | Move command contract tests; move operations/fixtures to domain service; replace root harness for family-only assertions |
| `run_command_test.go` | cmdrun; daemon runtime + cmddaemon | Move command contract tests; move operations/fixtures to domain service; replace root harness for family-only assertions; retain isolation until Env/Cwd dependency replaces process mutation |
| `run_queue_progress_test.go` | cmdrun | Move command contract tests; move operations/fixtures to domain service; retain isolation until Env/Cwd dependency replaces process mutation |
| `run_queue_test.go` | cmdrun | Move command contract tests; move operations/fixtures to domain service; replace root harness for family-only assertions; retain isolation until Env/Cwd dependency replaces process mutation |
| `run_test.go` | cmdrun | Move command contract tests; move operations/fixtures to domain service |
| `self_hosted_bench_e2e_test.go` | executable whole self-hosted journey; daemon runtime | Keep executable journey; move pure/domain assertions separately; retain isolation until Env/Cwd dependency replaces process mutation |
| `selfupdate_display_test.go` | cmdinstall | Move command contract tests; move operations/fixtures to domain service |
| `selfupdate_test.go` | cmdinstall | Move command contract tests; move operations/fixtures to domain service; replace root harness for family-only assertions |
| `session_attribution_test.go` | cmdsession | Move command contract tests; move operations/fixtures to domain service |
| `session_list_test.go` | cmdsession | Move command contract tests; move operations/fixtures to domain service; retain isolation until Env/Cwd dependency replaces process mutation |
| `session_message_test.go` | cmdsession | Move command contract tests; move operations/fixtures to domain service |
| `session_move_test.go` | cmdsession | Move command contract tests; move operations/fixtures to domain service |
| `session_park_journey_test.go` | cmdhooks; cmdsession | Move command contract tests; move operations/fixtures to domain service; retain isolation until Env/Cwd dependency replaces process mutation |
| `session_park_test.go` | cmdsession | Move command contract tests; move operations/fixtures to domain service; replace root harness for family-only assertions; retain isolation until Env/Cwd dependency replaces process mutation |
| `session_park_validation_test.go` | cmdsession | Move command contract tests; move operations/fixtures to domain service |
| `session_receive_park_test.go` | cmdsession | Move command contract tests; move operations/fixtures to domain service |
| `session_receive_test.go` | cmdsession | Move command contract tests; move operations/fixtures to domain service |
| `session_register_test.go` | cmdsession | Move command contract tests; move operations/fixtures to domain service; retain isolation until Env/Cwd dependency replaces process mutation |
| `session_resume_refusal_test.go` | cmdsession | Move command contract tests; move operations/fixtures to domain service |
| `skills_hook_install_test.go` | cmdskills | Move command contract tests; move operations/fixtures to domain service; retain isolation until Env/Cwd dependency replaces process mutation |
| `skills_hook_test.go` | cmdskills | Move command contract tests; move operations/fixtures to domain service; retain isolation until Env/Cwd dependency replaces process mutation |
| `skills_sync_errors_test.go` | cmdskills | Move command contract tests; move operations/fixtures to domain service |
| `skills_sync_test.go` | cmdskills | Move command contract tests; move operations/fixtures to domain service; replace root harness for family-only assertions; retain isolation until Env/Cwd dependency replaces process mutation |
| `skills_test.go` | cmdskills | Move command contract tests; move operations/fixtures to domain service; replace root harness for family-only assertions |
| `status_output_format_test.go` | cmdstatus | Move command contract tests; move operations/fixtures to domain service |
| `status_progress_test.go` | cmdstatus | Move command contract tests; move operations/fixtures to domain service |
| `status_test.go` | cmdstatus | Keep executable journey; move pure/domain assertions separately |
| `stream_adapters_test.go` | cmdstream + stream adapters | Move command contract tests; move operations/fixtures to domain service; retain isolation until Env/Cwd dependency replaces process mutation |
| `stream_adapters_transitive_test.go` | cmdstream + stream adapters | Move command contract tests; move operations/fixtures to domain service |
| `stream_output_test.go` | cmdstream + stream adapters | Move command contract tests; move operations/fixtures to domain service |
| `stream_sync_command_batch_test.go` | cmdstream + stream adapters | Move command contract tests; move operations/fixtures to domain service |
| `stream_sync_test.go` | cmdstream + stream adapters | Move command contract tests; move operations/fixtures to domain service |
| `stream_sync_write_failure_test.go` | cmdstream + stream adapters | Move command contract tests; move operations/fixtures to domain service |
| `stream_test.go` | cmdstream + stream adapters | Move command contract tests; move operations/fixtures to domain service; replace root harness for family-only assertions; retain isolation until Env/Cwd dependency replaces process mutation |
| `sync_report_command_test.go` | cmdsync | Move command contract tests; move operations/fixtures to domain service |
| `sync_report_test.go` | cmdsync | Move command contract tests; move operations/fixtures to domain service; retain isolation until Env/Cwd dependency replaces process mutation |
| `sync_test.go` | cmdsync; fleet selection service | Move command contract tests; move operations/fixtures to domain service |
| `sync_tui_e2e_test.go` | cmdsync | Keep executable journey; move pure/domain assertions separately |
| `task_failure_behavior_test.go` | cmdtask | Move command contract tests; move operations/fixtures to domain service |
| `task_offload_deps_test.go` | cmdtask | Move command contract tests; move operations/fixtures to domain service |
| `task_test.go` | cmdtask | Move command contract tests; move operations/fixtures to domain service |
| `testsupport_test.go` | cmddeps publish; cmdrun progress; daemon client/runtime; root contracts / cli leaf as asserted; root fixture | Split adapters by owner; generic test helper remains leaf; replace root harness for family-only assertions |
| `upgrade_test.go` | cmdinstall | Move command contract tests; move operations/fixtures to domain service; replace root harness for family-only assertions |
| `user_state_isolation_test.go` | cmdagent; cmdremote + remote service; root contracts / cli leaf as asserted | Keep actual root contracts; split leaf/family assertions |
| `verify_receipt_test.go` | cmdworktree (verify receipt) | Move command contract tests; move operations/fixtures to domain service |
| `verify_receipt_validation_test.go` | cmdworktree (verify receipt) | Move command contract tests; move operations/fixtures to domain service |
| `version_test.go` | root; root contracts / cli leaf as asserted | Keep actual root contracts; split leaf/family assertions; replace root harness for family-only assertions |
| `wait_bounds_test.go` | cmdwait | Move command contract tests; move operations/fixtures to domain service |
| `wait_failure_output_test.go` | cmdwait | Move command contract tests; move operations/fixtures to domain service |
| `wait_list_behavior_test.go` | cmdwait | Move command contract tests; move operations/fixtures to domain service |
| `wait_test.go` | cmdwait | Move command contract tests; move operations/fixtures to domain service; replace root harness for family-only assertions; retain isolation until Env/Cwd dependency replaces process mutation |
| `wait_verb_test.go` | cmdwait | Move command contract tests; move operations/fixtures to domain service; replace root harness for family-only assertions |
| `wb_test.go` | cmdworktree; root contracts / cli leaf as asserted | Keep actual root contracts; split leaf/family assertions |
| `wbhome_diagnostic_json_test.go` | root; root contracts / cli leaf as asserted | Keep actual root contracts; split leaf/family assertions |
| `worker_connect_daemon_lease_test.go` | cmdworker; daemon runtime + cmddaemon | Move command contract tests; move operations/fixtures to domain service |
| `worker_connect_wiring_test.go` | cmdworker; daemon runtime + cmddaemon | Move command contract tests; move operations/fixtures to domain service |
| `worker_test.go` | cmdworker; daemon runtime + cmddaemon | Move command contract tests; move operations/fixtures to domain service; retain isolation until Env/Cwd dependency replaces process mutation |
| `worktree_abort_closed_pr_e2e_test.go` | cmdworktree | Keep executable journey; move pure/domain assertions separately; replace root harness for family-only assertions; retain isolation until Env/Cwd dependency replaces process mutation |
| `worktree_abort_closed_pr_test.go` | cmdworktree | Move command contract tests; move operations/fixtures to domain service |
| `worktree_active_test.go` | cmdworktree | Move command contract tests; move operations/fixtures to domain service |
| `worktree_cleanup_target_test.go` | cmdworktree | Move command contract tests; move operations/fixtures to domain service |
| `worktree_collaboration_e2e_test.go` | cmdsession; cmdworktree | Keep executable journey; move pure/domain assertions separately |
| `worktree_collaboration_peer_e2e_test.go` | cmdworktree | Keep executable journey; move pure/domain assertions separately; replace root harness for family-only assertions |
| `worktree_collaboration_test.go` | cmdsession; cmdworktree | Move command contract tests; move operations/fixtures to domain service |
| `worktree_end_test.go` | cmdworktree | Move command contract tests; move operations/fixtures to domain service; replace root harness for family-only assertions; retain isolation until Env/Cwd dependency replaces process mutation |
| `worktree_gc_test.go` | cmdworktree | Keep executable journey; move pure/domain assertions separately; retain isolation until Env/Cwd dependency replaces process mutation |
| `worktree_guard_pushrefs_test.go` | cmdworktree | Move command contract tests; move operations/fixtures to domain service; replace root harness for family-only assertions |
| `worktree_host_level_test.go` | cmdworktree | Keep executable journey; move pure/domain assertions separately; replace root harness for family-only assertions; retain isolation until Env/Cwd dependency replaces process mutation |
| `worktree_marker_test.go` | cmdworktree | Move command contract tests; move operations/fixtures to domain service; replace root harness for family-only assertions |
| `worktree_merge_direct_ci_test.go` | cmdworktree | Move command contract tests; move operations/fixtures to domain service |
| `worktree_merge_test.go` | cmdworktree | Move command contract tests; move operations/fixtures to domain service; replace root harness for family-only assertions; retain isolation until Env/Cwd dependency replaces process mutation |
| `worktree_publication_test.go` | cmdworktree | Move command contract tests; move operations/fixtures to domain service; retain isolation until Env/Cwd dependency replaces process mutation |
| `worktree_rescue_test.go` | cmdworktree | Keep executable journey; move pure/domain assertions separately; retain isolation until Env/Cwd dependency replaces process mutation |
| `worktree_result_output_test.go` | cmdworktree | Move command contract tests; move operations/fixtures to domain service |
| `worktree_retire_test.go` | cmdworktree | Move command contract tests; move operations/fixtures to domain service; replace root harness for family-only assertions; retain isolation until Env/Cwd dependency replaces process mutation |
| `worktree_test.go` | cmdhooks; cmdworktree | Move command contract tests; move operations/fixtures to domain service; replace root harness for family-only assertions; retain isolation until Env/Cwd dependency replaces process mutation |
| `worktree_upgrade_integration_test.go` | cmdworktree | Keep executable journey; move pure/domain assertions separately |
| `zz_cov_archive_worker_test.go` | cmdarchive; cmdworker; daemon runtime + cmddaemon | Split individual Test funcs by subject; no multi-family unit harness; retain isolation until Env/Cwd dependency replaces process mutation |
| `zz_cov_bridge_client_test.go` | daemon runtime + cmddaemon | Move command contract tests; move operations/fixtures to domain service |
| `zz_cov_bridge_test.go` | daemon runtime + cmddaemon | Move command contract tests; move operations/fixtures to domain service |
| `zz_cov_daemon_op_test.go` | cmdstatus; daemon runtime + cmddaemon | Split individual Test funcs by subject; no multi-family unit harness |
| `zz_cov_deps_enroll_adapters_pr_test.go` | cmdpr; cmdremote + remote service; cmdstream + stream adapters | Split individual Test funcs by subject; no multi-family unit harness; retain isolation until Env/Cwd dependency replaces process mutation |
| `zz_cov_deps_graph_drift_test.go` | cmddeps + dependency service; cmdfleet + fleet service | Split individual Test funcs by subject; no multi-family unit harness; retain isolation until Env/Cwd dependency replaces process mutation |
| `zz_cov_deps_hooks_test.go` | cmdhooks | Move command contract tests; move operations/fixtures to domain service |
| `zz_cov_deps_merge_policy_test.go` | cmdfleet + fleet service | Move command contract tests; move operations/fixtures to domain service; retain isolation until Env/Cwd dependency replaces process mutation |
| `zz_cov_deps_policy_extra_test.go` | cmddeps + dependency service | Move command contract tests; move operations/fixtures to domain service |
| `zz_cov_deps_policy_fleet_test.go` | cmddeps + dependency service | Move command contract tests; move operations/fixtures to domain service |
| `zz_cov_deps_propagate_test.go` | cmddeps + dependency service | Move command contract tests; move operations/fixtures to domain service |
| `zz_cov_deps_publish_test.go` | cmddeps + dependency service | Move command contract tests; move operations/fixtures to domain service; retain isolation until Env/Cwd dependency replaces process mutation |
| `zz_cov_deps_run_test.go` | cmdrun | Move command contract tests; move operations/fixtures to domain service; retain isolation until Env/Cwd dependency replaces process mutation |
| `zz_cov_deps_session_move_test.go` | cmdsession | Move command contract tests; move operations/fixtures to domain service; retain isolation until Env/Cwd dependency replaces process mutation |
| `zz_cov_deps_set_bump_test.go` | cmddeps + dependency service | Move command contract tests; move operations/fixtures to domain service; retain isolation until Env/Cwd dependency replaces process mutation |
| `zz_cov_deps_stream_test.go` | cmdstream + stream adapters | Move command contract tests; move operations/fixtures to domain service; retain isolation until Env/Cwd dependency replaces process mutation |
| `zz_cov_deps_sync_test.go` | cmdrun; cmdsync | Split individual Test funcs by subject; no multi-family unit harness; retain isolation until Env/Cwd dependency replaces process mutation |
| `zz_cov_deps_writers_test.go` | cli shared presentation; cmddeps + dependency service | Split individual Test funcs by subject; no multi-family unit harness; retain isolation until Env/Cwd dependency replaces process mutation |
| `zz_cov_fleet_test.go` | cmddeps + dependency service; cmdfleet + fleet service; fleet selection service | Split individual Test funcs by subject; no multi-family unit harness; replace root harness for family-only assertions; retain isolation until Env/Cwd dependency replaces process mutation |
| `zz_cov_go_directive_test.go` | cmddeps + dependency service | Move command contract tests; move operations/fixtures to domain service; retain isolation until Env/Cwd dependency replaces process mutation |
| `zz_cov_layout_gc_test.go` | cmdlayout; cmdworktree | Split individual Test funcs by subject; no multi-family unit harness; retain isolation until Env/Cwd dependency replaces process mutation |
| `zz_cov_quality_test.go` | cmdquality | Move command contract tests; move operations/fixtures to domain service; retain isolation until Env/Cwd dependency replaces process mutation |
| `zz_cov_render_adapters_test.go` | cmdremote + remote service; cmdsession; cmdstream + stream adapters | Split individual Test funcs by subject; no multi-family unit harness; retain isolation until Env/Cwd dependency replaces process mutation |
| `zz_cov_status_pr_branch_test.go` | cmdbranch; cmdpr; cmdstatus | Split individual Test funcs by subject; no multi-family unit harness |
| `zz_cov_stream_migrate_test.go` | browser service; cli landing/lifecycle contracts; cmdmigrate; cmdsession; cmdstream + stream adapters | Split individual Test funcs by subject; no multi-family unit harness; replace root harness for family-only assertions; retain isolation until Env/Cwd dependency replaces process mutation |
| `zz_cov_wt_active_test.go` | cmdworktree | Move command contract tests; move operations/fixtures to domain service; retain isolation until Env/Cwd dependency replaces process mutation |
| `zz_cov_wt_create_cleanup_test.go` | cmdworktree | Move command contract tests; move operations/fixtures to domain service; retain isolation until Env/Cwd dependency replaces process mutation |
| `zz_cov_wt_daemon_controller_test.go` | daemon runtime + cmddaemon | Move command contract tests; move operations/fixtures to domain service; retain isolation until Env/Cwd dependency replaces process mutation |
| `zz_cov_wt_daemon_lock_test.go` | daemon runtime + cmddaemon | Move command contract tests; move operations/fixtures to domain service; retain isolation until Env/Cwd dependency replaces process mutation |
| `zz_cov_wt_daemon_operation_test.go` | cmdrun; daemon runtime + cmddaemon | Split individual Test funcs by subject; no multi-family unit harness; retain isolation until Env/Cwd dependency replaces process mutation |
| `zz_cov_wt_daemon_test.go` | daemon runtime + cmddaemon | Move command contract tests; move operations/fixtures to domain service; retain isolation until Env/Cwd dependency replaces process mutation |
| `zz_cov_wt_end_retire_test.go` | cmdworktree | Move command contract tests; move operations/fixtures to domain service |
| `zz_cov_wt_end_test.go` | cmdworktree | Move command contract tests; move operations/fixtures to domain service |
| `zz_cov_wt_extra_test.go` | cmdworktree | Move command contract tests; move operations/fixtures to domain service; retain isolation until Env/Cwd dependency replaces process mutation |
| `zz_cov_wt_gc_progress_test.go` | cmdworktree | Move command contract tests; move operations/fixtures to domain service; retain isolation until Env/Cwd dependency replaces process mutation |
| `zz_cov_wt_guard_test.go` | cmdworktree | Move command contract tests; move operations/fixtures to domain service; retain isolation until Env/Cwd dependency replaces process mutation |
| `zz_cov_wt_helpers_test.go` | leaf CLI test utility; family-specific fixture helpers | Split: reusable failing writer/buffer execution helper; safe invocation fixture helper; no root/family imports |
| `zz_cov_wt_logverbs_test.go` | cmdsession; cmdworktree | Split individual Test funcs by subject; no multi-family unit harness |
| `zz_cov_wt_marker_test.go` | cmdworktree | Move command contract tests; move operations/fixtures to domain service |
| `zz_cov_wt_merge_test.go` | cmdworktree | Move command contract tests; move operations/fixtures to domain service; retain isolation until Env/Cwd dependency replaces process mutation |
| `zz_cov_wt_own_test.go` | cmdworktree | Move command contract tests; move operations/fixtures to domain service; retain isolation until Env/Cwd dependency replaces process mutation |
| `zz_cov_wt_rescue_test.go` | cmdworktree | Move command contract tests; move operations/fixtures to domain service; retain isolation until Env/Cwd dependency replaces process mutation |
| `zz_cov_wt_session_message_test.go` | cmdsession | Move command contract tests; move operations/fixtures to domain service; retain isolation until Env/Cwd dependency replaces process mutation |
| `zz_cov_wt_worktree_test.go` | cmdworktree | Move command contract tests; move operations/fixtures to domain service; retain isolation until Env/Cwd dependency replaces process mutation |
| `zz_cov_wt_writer_sweep_test.go` | cmdworktree | Move command contract tests; move operations/fixtures to domain service; replace root harness for family-only assertions |

## Immediate continuation after independent pilot acceptance

The first prerequisite is one-owner **bounded shared-contract preparation**: centralize new exit/usage/format/discovery contracts in `internal/cli/shared`, establish narrow execution-time getters over the existing root invocation, and add only the generic test execution helpers with demonstrated consumers to a leaf harness. Keep the existing invocation and root error fields at old call sites until their family migrates. New family constructors use the agreed leaf contracts; a small explicit root adapter supplies getters/dependencies from the existing state. Preserve root policy and cover every new shared statement at 100%. Do not mechanically rename all existing root invocation/error fields or replace every constructor/helper signature: the changed-line 100% gate would treat touched, previously uncovered root statements as new obligations unrelated to this cohort. Migrate those call sites only with the owning family and its moved tests. This bounded prerequisite settles contract ownership before file-disjoint family work, without a module-wide rewrite or daemon mega-dependency.

Next two cohesive domains: **CI wait/audit** (`ci.go`, `ci_wait_progress.go`, 14 uncovered historical statements) and **quality adapters** (coverage/verify/check/deadcode, 48). CI is the highest immediate test-runtime opportunity: ci_wait_test.go has repeated shell/PATH/Setenv fixtures; inject the existing wait/audit operation and use deterministic operation results to verify requests/output. Keep one real gh-runner boundary journey in the domain service and executable exit contract. Quality already delegates to internal/quality and can reuse the formatting/harness; move qualityTargets/runTargets below CLI because fleet/status/remote use them. Implement CI first; quality second, or file-disjoint only after contracts and selector ownership are settled. Hooks is the subsequent strong cohort (117 uncovered, secure helper boundary retained).

## Complete cutover criteria

The full refactoring is complete only when all of the following are true:

- `cmd/wb` contains executable startup, private-helper dispatch and exit wiring; it delegates root creation to `internal/cli`. Root composition retains a small explicit family registry and centralized root policy. The old command implementations, mutable test replacement globals and unnecessary family adapter wrappers are gone from `cmd/wb`.
- Every public root/nested verb and compatibility alias from the base revision still resolves with the same help, argument, default, flag, stdout/stderr, report and exit behavior. Capability catalog and flag-matrix checks continue to cover all leaves. Private launcher/Git helpers and version/cockpit-export side-effect exceptions remain executable contracts.
- All production/test inventory rows have a final owner or an explicitly recorded split. Family tests move with their command implementations; operation tests move to the service that owns those operations. Existing real Git, process, daemon, filesystem and persisted journey coverage remains executable at its appropriate boundary. No test is deleted simply because it makes migration inconvenient.
- Extracted command packages have the plan's required 100% statement coverage. Shared errors, inherited-flag binding and invocation isolation are tested once at their contract plus representative family wiring. Existing affected-package closures and coverage ratchets remain in force. Moving statements between packages must not hide coverage or weaken scope.
- `internal/cli` may import families and `internal/cli/shared`; families import only the shared leaf and operations. Shared leaf imports no root or family. Operational packages import no Cobra/CLI. Test utilities are leaves, so independent sibling tests do not gain reverse imports through a mega-harness.
- After at least two families are extracted, verify sibling cache eligibility and the counterexample explicitly: if `cmdlayout` tests import a helper that imports `internal/cli`, that helper transitively imports every family, so a CI-family change can invalidate layout tests through TestImports/XTestImports despite separate production packages. Remove that helper direction; an independent layout harness imports only shared leaves and its own operation dependencies. Do not promise cached execution under -count=1, changed coverage inputs or ambient file/environment dependencies.
- Record actual family test elapsed time, removed executable builds/Git fixtures/process waits, remaining root integration cost and sibling cache evidence. A fast family pilot is insufficient evidence for a whole-CI speedup. Required governed checks, independent reviews, exact remote-target delivery and WB cleanup belong to the parent plan's landing task.

## Review status

Proposed research inventory awaiting independent review together with the pilot and parent plan. Review round 1 corrections: keep the temporary explicit registry in cmd/wb until family constructors cut over; defer concrete sibling-cache proof until two families exist; avoid mechanical root field/signature rewrites by using bounded temporary adapters and execution-time shared getters. The risks above are acceptance gates, not final-code findings. No implementation changes or validation were performed while versioning this map.
