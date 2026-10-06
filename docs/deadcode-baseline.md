# Dead-code baseline: why each entry is tolerated

`.wb/deadcode-baseline.txt` lists functions that no path from any `main`
package reaches (`wb deadcode`). Everything in it is tolerated for a stated
reason; anything else is dead code and is deleted. The baseline file carries a
heading per category, but `wb deadcode --update-baseline` regenerates the file
without comments, so the rationale is kept here as well. Restore the headings
from git history after a regeneration.

The 2026-10-02 review took the baseline from 378 entries (385 findings on main,
because seven `internal/testenv` findings were never recorded) to 261: 124
unreachable functions were deleted or moved out of production code, and the
rest are the categories below. Since then the retired dashboard's one entry
left and seven native-test selection analysers arrived from main, for 267.

## How to decide a new unreachable function

1. Called only by tests in its own package: move it into a `_test.go` file
   (many `testsupport_test.go` files exist for exactly this), or delete it with
   its tests. Do not add it to the baseline.
2. Called only by tests in other packages: it is a cross-package fixture
   (category X below). Prefer rewriting the tests onto the live API.
3. Reached only from a consumer outside this module: category A2. Prove it by
   rooting the analysis at the consumer's wiring, as was done for A2 below.
4. Scheduled for wiring by a live spec or plan: category A3, and name the task.
5. Otherwise delete it.

## A1. Test support, by design (164 entries)

* 72: `internal/gitcli/gitclitest`, `internal/runner/runnertest`,
  `internal/sessiontransport/transporttest`, `internal/testenv`,
  `internal/testsweep`. Packages that exist to be imported by tests.
  `internal/testenv` includes seven entries added by the user-state isolation
  guard (`IsolateUserState`, `UserStateViolations`, `globalGitConfigFiles`,
  `goEnvFileValues`, `inheritedUserStateRoot`, `resolvedGoToolVariables`,
  `under`) that were missing from the baseline on main.
* 7: `internal/githubobserver/testfixture`: `InstallGH`, `State.Answer`,
  `WithEmptyActionsRuns`, `ScriptState`, `PullRequestView`,
  `InstallTransientReadGH`, `InstallDirectCIGH`. This package owns the concrete
  scripted GitHub fixtures shared by `internal/githubchecks` and
  `internal/orchestrate` tests after the observation-domain extraction. Its
  callers are test files, including CI observation/parser tests (`ciwait*_test.go`,
  `github_vendored_coverage_test.go`, `github_check_queries_test.go`,
  `direct_ci_test.go`), orchestration engine/PR tests
  (`engine_integration_test.go`, `pr_create_test.go`, `pr_land_coverage_test.go`,
  `pr_update_test.go`, `ciwait_transient_mutation_test.go`) and worktree checks
  (`worktree_merge_direct_ci_test.go`, `worktree_merge_tail_coverage_test.go`).
  `InstallGH` and `InstallTransientReadGH` share `WithEmptyActionsRuns`;
  `ScriptState` calls `InstallGH`. They retain private fixture files, native
  executable writing and environment isolation; `State.Answer` updates those
  files and `PullRequestView` constructs a test receipt. The package's own
  `fixtures_test.go` verifies real filesystem and JSON failure reporting.
  No production package imports it. These are A1 test-support identities,
  rather than exceptions for unreachable production mechanisms.
* 9: `internal/secureopen.Fake` and `NewFake`.
* 2: `internal/execfile` (`WriteExecutableFile`, `writeAndChmodTempExecutable`),
  reached only through `testenv.WriteExecutableFile` and `internal/envguard`
  tests.
* 3: `internal/runqueue` `QueueDirForTest`, `SetNumCPUForTest`,
  `SetQueueRootForTest`, documented test seams used by `cmd/wb` tests.
* 64: `internal/quality` guard analysers. A `_test.go` calls each family:
  clock seam (`clock_seam_guard_test.go`), exec sites (`execsites_guard_test.go`),
  unit tier (`unittier_guard_test.go`), inline write sequences
  (`filewrite_boundary_test.go`), exec write file (`execwritefile_guard_test.go`),
  campaign test names (`campaigntestnames_test.go`). They run as `go test`.

## A2. Hosted Workbench service (58 entries)

`api/githubapp` (48), `hub.GitHubOAuthVerifier` and `githubGET` (9) and
`api/githubapp/machinesnapshot.ResolveLatest` (1).

The hosted service is not a `main` package in this module.
`sneat-co/sneat-go/pkg/modules/workbench` (`module.go`, `document_store.go`)
mounts `DocumentProjectionStore`, `StoreReadModel`, `GitHubRESTProjectionReader`,
`InstallationTokenSource`, `DocumentProjectionDeliveryStore`,
`DocumentProjectionWriter` and `hub.GitHubOAuthVerifier` from this module
(sneat-go pins `github.com/sneat-dev/wb`). Wiring those same types into a
throwaway `main` in this module and re-running `wb deadcode` made all 57
`api/githubapp` and `hub` OAuth entries reachable, so none of them is dead.

Specs relied on: `spec/features/self-hosted-bench` (Implementing; the hosted
instance keeps its OAuth viewer), `spec/features/github-app-repository-events`
(Draft; the provider). **No spec under `spec/` describes the projection read
model, the public-eligibility rules or the leaderboard/series/latest-merges
projections.** The review ruling was to delete such code, but doing so would
break the hosted service on its next dependency bump, so it is kept. Write the
spec, or move the hosted-only code into the sneat-go repository.

`machinesnapshot.ResolveLatest` is the documented comparison durable adapters
apply inside their transaction; `api/githubapp/machine_snapshots_test.go` holds
the service to it. It is public API for external store adapters.

## A3. Scheduled by a live spec or plan (30 entries)

* 6 in `internal/herdr` and 24 in `internal/sessiontransport`:
  `spec/features/herdr-session-transport` (Draft) requirements
  `single-transport-interface`, `transport-capability-matrix`,
  `explicit-transport-override`, `none-transport-is-first-class`,
  `herdr-version-detected-and-bounded`, `automatic-transport-identity-capture`;
  `spec/plans/herdr-session-transport.md` Task 1 (herdr adapter: identity from
  the environment, fakeable exec seam), Task 2 (interface, capability matrix,
  override, none transport), Task 5 (selection) and Task 10 (failure modes).
  The plan is Draft, not approved; the feature is a live Draft spec.

## C. Possibly unwired by accident (5 entries)

`hub.WorkflowRunHarvester` and `hub.HTTPArtifactDownloader`
(`HarvestWorkflowRun`, `narrateLine`, `DownloadWorkflowArtifact`, `newRequest`,
`extractCoverageSummaryFromZip`).

* `spec/features/fleet-quality` requirement `hub-coverage-harvester` (feature
  Implementing) says the hub MUST download the `wb-coverage-summary` artifact on
  `workflow_run.completed`. `spec/plans/remote-ci-coverage.md` (Approved) Task 3
  schedules it.
* `hub/http.go` calls `HandlerOptions.CoverageHarvester` when it is set, but
  nothing in this module (`cmd/wb/daemon_hub*.go`) or in sneat-go sets it, so
  the harvester never runs. `wb coverage --ci` and `wb fleet coverage` read a
  store that nothing in production fills through this path.

The fix is to wire a `WorkflowRunHarvester` with an `HTTPArtifactDownloader`
into the hub handler options, not to delete it.

## X. Not covered by the ruling (17 entries)

Cross-package test fixtures (14): production-package functions whose only
callers are tests in another package, so they cannot become `_test.go` helpers
without a shared test package.

* `internal/landinglane.Read`, `lockShared`: `cmd/wb/landing_lane_test.go`,
  `internal/orchestrate/landing_lane_guard_test.go`,
  `internal/orchestrate/misc_coverage_test.go`.
* `internal/worktrees.CaptureParkedSessionWorktree` and the two helpers it calls
  (`ParkedSessionWorkLogSnapshot`, `parkedSessionWorkLogSnapshotWithReads`):
  `cmd/wb/session_park_test.go`, `internal/layout/migrate_test.go`.
* `internal/worktrees.WithParkedLocalResumeCustody`: `internal/layout/migrate_test.go`.
* `internal/worktrees.TouchHeartbeat`: `cmd/wb/version_test.go`,
  `internal/cockpit/fleet/collectors_test.go`, `collectors_e2e_test.go`.
* `internal/worktrees.ValidateDependencyDeltas`,
  `internal/worktreebranches.SupersessionService.ValidateDependencyDeltas`,
  `internal/worktreebranches.DependencyManifestValue`: `internal/deps/report_test.go`
  holds dependency reports to the supersession proof.
* `internal/worktreejournal.OpenJournalComponent`: `internal/worktrees` tests.
* `internal/runqueue.Peek`: `cmd/wb/run_queue_test.go`. Its doc comment still
  says `wb run --queue` uses it; that command now uses `ListQueue`.
* `internal/buildinfo.Set`: documented as the way tests force a version;
  `cmd/wb` and `internal/worktrees` tests call it.
* `internal/quality.ReadCoverageSummary`: `cmd/wb/coverage_summary_test.go`.

Implicit interface (1): `internal/repopath.Address.String` is the `fmt.Stringer`
used whenever an address is printed with `%s` or `%v`; reachability analysis
cannot see that call.

Per-OS test probes (2): `internal/worktrees.platformGitFilesystemCapabilityConfines`
and `cmd/wb.daemonSupervisorRecordsStartLog` have one definition per platform
and are read only by tests.
