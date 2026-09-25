Review-Of-Head (NOT approved): 074d3e8f78353405c0336715e59c67ed4d3982de

# Adversarial review: task-5 PR-4 (`cov-t5-ctx-4`, PR #760) — projectsRoot onto `*invocation`

Branch `cov-t5-ctx-4` at 074d3e8f. Merge-base with `origin/cov/integration` (2a953bab) is 493ff751. The branch's two merge parents are both already in cov/integration. `git merge-tree` against cov/integration is clean, and the merged tree passes `go vet ./cmd/wb` on linux.
Runtime: Claude Code (Opus) on the shared 4-core Linux VM. All test runs were targeted `-run` with GOMAXPROCS=2 -p 1. I ran mutations with `go test -overlay`. I checked the AST guard by editing a scratch detached worktree, because the guard reads source from disk and so ignores `-overlay`. I have removed all the scratch worktrees.

## Verdict: NOT APPROVED

The production refactor is mechanical and preserves behaviour. No production path can now reach an empty projectsRoot that could not reach it before (see N1).

The test side has problems:
- CI is red on Windows (a compile break).
- Removing the global silently un-pinned the fixture roots in the shared exec helpers. Tests that were hermetic at base now read the operator's real `~/projects/.wb`, and one of them writes to it. One new test writes into the source tree.
- One "restored" test now makes a live, authenticated GitHub API call.
- The new disk tests depend on the host's free disk space.
- 12 of 24 mutations survived.

## Blockers

**B1. CI red: the Windows build of cmd/wb tests fails.** `cmd/wb/worker_connect_daemon_lease_test.go:26` calls `cwWtDaemonOpFixture`, which is defined only in `cmd/wb/zz_cov_wt_daemon_operation_test.go:1` (`//go:build !windows`). Run 36114117821 "Native Windows compile and test" fails with `undefined: cwWtDaemonOpFixture`, so "Required checks passed" also fails. I reproduced it locally with `GOOS=windows go vet ./cmd/wb`, on both the head and the merged-into-cov/integration tree. Fix: add `//go:build !windows` to the new file, or move the fixture into an untagged file.

**B2. Fixture roots were silently dropped, so tests now reach the operator's real WB home and the source tree.** At base, `cwCovExec` (`cmd/wb/zz_cov_layout_gc_test.go:28`), `cwWtExecOut` (`cmd/wb/zz_cov_wt_helpers_test.go:37`) and `cwWtRunCmd` (`:52`) pinned the global `projectsRoot = projects`. The PR deleted the pin but kept the now-dead `projects` parameter. It also kept the doc comment that still says the helper points the global at the fixture (`zz_cov_layout_gc_test.go:20-27`). 80 single-line helper call sites, plus table constructors, still pass a bare `&invocation{}`, so these now run with root `""`. That resolves to `$HOME/projects` (wbhome) or to a cwd-relative `.wb/runtime/cpu` (runqueue).

I proved the effects with strace, and with a fake-HOME diff of 364 changed tests against base:
- a. **Writes to the real home.** At base, `TestCwDepsAcquireNpmPublicationLocksAndRelease` (`zz_cov_deps_publish_test.go:666-668`) pinned root to `t.TempDir()`. At head it creates `$HOME/projects/.wb/worktrees/deps-npm-publish-cwfixture/` and `npm-publish-claim-6ed7ed808c5ba937/` with lock files. Both already exist on this VM, created at 04:48 UTC by the lane's own runs. The fake-HOME diff shows base writes neither.
- b. **Reads of the real home.** These tests now scan the operator's real `~/projects/.wb/reports/worktree-merge/*.json` and resolve landing-lane owners against the real `~/projects/.wb/sessions`:
  - `TestCwWtMergeRecoveryCommands{RejectBadFormat,RejectBadAdmission,FailOnMissingReceipt}` (`zz_cov_wt_merge_test.go:353-420`, table at `:353`)
  - `TestCwWtMerge{Combined,Prepare,LandAndRevert}CommandErrorPaths` (`:424`, `:455`, `:481`)

  At head, `landing_lane.go:32-38` is covered only because a real registered session was found. The missing-receipt error changed from `open <fixture>/.wb/reports/worktree-merge: no such file` at base to `no worktree merge receipt owns …` at head. The tests still pass because they assert only `err != nil`. Base touches the real home 0 times in these tests. `TestCwCovStreamSyncCommandUsageRefusals` (`zz_cov_stream_migrate_test.go:268`) likewise reads the real `~/projects/.wb/streams`.
- c. **Writes into the source tree.** The new `TestCwCovExecuteWorkerAssignmentAdmitsExplicitCpuUnits` (`zz_cov_archive_worker_test.go:341`, the call at `:387`) creates an untracked `cmd/wb/.wb/runtime/cpu/slot-00.lock` that is not gitignored. The cause is `runqueue.queueRoot("")`, which is cwd-relative, and TestMain's queue override is keyed only to `defaultProjectsRoot()`. `TestCwCovExecuteWorkerAssignmentRunsAndReports` (`:254`, `:320`) lost its base pin `projectsRoot = root` (base `:319-321`); the PR replaced it with `&invocation{}`. Its admission now also resolves the relative queue.

Fix: change the helpers' signature to `build func(*invocation) *cobra.Command` and construct `&invocation{projectsRoot: projects}` inside the helper, so the root is injected by construction. Thread the root into the tests above. Afterwards, delete the two stray directories under `/home/ai/projects/.wb/worktrees/`.

**B3. `TestPRLandReportsLocalLinkPreflightBeforeGitHub` now calls the live GitHub API** (`cmd/wb/pr_test.go:43-71`, commit dd99f846). `PATH=filepath.Dir(git)` is `/usr/bin`, and `/usr/bin` also contains `gh`, both on this VM and on ubuntu runners. The observed error is `GitHub repos/acme/app/pulls/7 returned HTTP 404: gh: Not Found`, returned with the operator's credentials. The comment "Hide gh" and the failure text "missing gh unexpectedly let the landing continue" are false. This test is how the lane "restored" 5 of the 8 newly-uncovered lines: pr.go:145/146/173 and ci_wait_progress.go:113-117.

Verified fix: symlink only `git` into a `t.TempDir()` and set PATH to that directory. The error becomes `exec: "gh": executable file not found`, and the run still covers pr.go:145/146/173 and ci_wait_progress.go:113-117, with no network.

**B4. The new disk tests depend on the host.** `TestDiskReportsAnEmptyFleetWithNoFindings` and `TestDiskJSONReportsAnEmptyFleet` (`cmd/wb/disk_test.go:10-44`) fail deterministically on this VM. The VM has 8.8% free, below the 10% floor, so the output includes "only 13.2 GB of 149.9 GB available", which is a finding. Passing `--minimum-available 0` does not help; I tested it. These two tests are the only coverage for ratchet line disk.go:67. They pass in CI only because the runner has free disk. Fix: inject the free-space probe, or assert on the report while tolerating the floor finding. These are new failures, not pre-existing flakes.

**B5. Many new tests assert execution, not behaviour: 12 of 24 mutations survived.** The PR's own regression class is "a wrong or dropped projectsRoot", and most of the "Wires…ProjectsRoot…" tests cannot see it:
- `cmd/wb/remote_and_peers_cli_wiring_test.go`, all 13 tests. Its header admits they observe only an early refusal that does not depend on the root. M10 (peers invite root → `""`) and M11 (remote status root → `""`) survived.
- `TestSessionListCLIWiresProjectsRootIntoRunSessionList` (`session_list_test.go:257`), M19 survived.
- `TestCwCovRunHierarchicalMigrationDefaultsGithubDirToProjectsRoot` (`zz_cov_stream_migrate_test.go:577`), M18 (`githubDir = ""`) survived. It asserts only exit code 2.
- `TestDefaultTaskOffloadDependenciesStoreOpensUnderProjectsRoot` (`task_offload_deps_test.go:12`), M13 (`wbhome.Root("")`) survived. It never checks where the store is. Under the mutant it opens the store in the real home.
- `TestPRCreateAutoMergeArmsAndRunsTheLiveLinkPreflight` (`pr_create_link_preflight_test.go:198`), M17 (`LinkPreflight` → `return nil`) survived.
- `TestRefuseLinkedReceiptWorktreesGuardsAWorktreeArgumentDirectly` (`locallink_guard_test.go:65`), M09 (`return nil`) survived. It has no positive (refused) case.
- `TestCwCovExecuteWorkerAssignmentAdmitsExplicitCpuUnits` (`zz_cov_archive_worker_test.go:341`), M14 (AdmitExplicit → adaptive Admit) survived. It cannot tell the two admission paths apart.
- `TestStreamJoinReachesTheStreamEngine` (`stream_test.go:77`), M20 (inv → `&invocation{}`) survived. It has no outcome assertion at all.

Each of these should assert something that depends on the root, such as a path under the root in the refusal or output, a recorded argument, or a positive refusal case. Otherwise, rename it so it does not claim to verify wiring.

## Mutation results (overlay, targeted `-run`)
| id | mutation | test | result |
|---|---|---|---|
| M01 | agent_remote.go:33 BeforeCreate → nil | TestAgentDispatchDepsWiresBeforeAndAfterCreateHooks | killed |
| M02 | agent_remote.go:35 markCreatedCheckouts dropped | same | killed |
| M03 | agent_remote.go:149 inv → `&invocation{}` | TestAgentRemoteEntryPointAnswersAwaitLogsAndStop | survived (the env override makes `""` resolve to the same home) |
| M04 | branch.go:86 root → `""` | TestBranchQuarantineRequiresRepoBranchAndReason | killed |
| M05 | daemon.go:2445 raw policy closure → nil | TestServeDashboardRefusesARawCommandWithoutAnAdministratorOptIn | killed |
| M06 | deps.go:135 EnsureRoot(`""`) | TestCwDepsGraphCommandInProcess | killed |
| M07 | fleet_default_branch.go:493 dry-run report persistence disabled | TestRunDefaultBranchDryRunStillPersistsReportUnderReportDir | killed |
| M08 | landing_lane.go:68 Root(`""`) | TestReleaseWorktreeMergeLane* | killed |
| M09 | locallink_guard.go:68 → return nil | TestRefuseLinkedReceiptWorktreesGuardsAWorktreeArgumentDirectly | **survived** |
| M10 | peers.go:110 root → `""` | TestPeersInviteCLIWires… | **survived** |
| M11 | remote_status.go:29 root → `""` | TestRemoteStatusCLIWires… | **survived** |
| M12 | session_move.go:260 root → `""` | TestSessionMoveLoopbackCourier… | survived (the test itself uses `&invocation{}`) |
| M13 | task.go:52 Root(`""`) | TestDefaultTaskOffloadDependenciesStoreOpensUnderProjectsRoot | **survived** |
| M14 | worker.go:215 AdmitExplicit → Admit | TestCwCovExecuteWorkerAssignmentAdmitsExplicitCpuUnits | **survived** |
| M15 | worker.go:98 root → `""` | TestWorkerConnectLeasesAndExecutesARealQueuedOperation | killed only by a hang (see N6) |
| M16 | worktree_retire.go:60 root → `""` | TestWorktreeRetireApplyCompletesAndReleasesTheRemoteClaim | killed |
| M17 | pr_create.go:205 LinkPreflight → nil | TestPRCreateAutoMergeArmsAndRunsTheLiveLinkPreflight | **survived** |
| M18 | migrate.go:129 githubDir → `""` | TestCwCovRunHierarchicalMigrationDefaultsGithubDirToProjectsRoot | **survived** |
| M19 | session_list.go:52 root → `""` | TestSessionListCLIWiresProjectsRootIntoRunSessionList | **survived** |
| M20 | stream.go:268 inv → `&invocation{}` | TestStreamJoinReachesTheStreamEngine | **survived** |
| M21 | run.go:188 inv → `&invocation{}` | TestCwWtRunAsyncCLIWiresIntoSubmitWorkerOperation | killed |
| M22 | layout.go:194 root → `""` | TestLayoutMigrateCLIWiresProjectsRoot… | killed |
| M23 | worktree_retire.go:52 closure → nil | TestWorktreeRetireDryRunReachesTheRemoteOwnershipCheck | killed |
| M24 | session_message.go:190 inv → `&invocation{}` | TestCwWtDefaultSessionReceiveMessageDependenciesReachesSessionDir | survived (the env override makes it equivalent) |

## Notes

**N1. Behaviour preservation: OK.** I normalized the production diff (66 files, +662/−658) by stripping `inv`-threading tokens. What remains is formatting only: `newDaemonCmd`, `acknowledgeSessionMove`, and some closures that wrap former function values. `fleet_cmd.go:87` passes both `options.inv` and `options.inv.projectsRoot`, which is redundant but harmless. No function that had a local or parameter named `projectsRoot` was rewired to `inv.projectsRoot`.

Empty root in production:
- The only zero-valued production invocation is `dispatchWithHandlers` (`main.go:456`). It runs before cobra, and the retired global held `""` at that point too.
- `RunAgentRemote` still defaults the empty root (`agent_remote.go:57-59`).
- The resolver installed at `main.go:478` with the zero invocation is replaced at `main.go:538`, before any cobra command runs, because `SetSessionResolver` replaces rather than chains (`internal/worktrees/identity.go:98`).
- `--projects-root ""` behaves the same as at base.

So no regression. There is a pre-existing inconsistency, though: `wbhome.projectsRootAbs("")` defaults to `$WB_PROJECTS_ROOT` or `~/projects` (`internal/wbhome/wbhome.go:248-260`), while `runqueue.queueRoot("")` is cwd-relative (`internal/runqueue/visibility.go:168-176`). An empty root therefore splits the CPU admission queue away from the machine-wide one. Recommendation: fail loudly on an empty root, either with a usage error in `PersistentPreRunE` or with a refusal in runqueue. That is a behaviour change, so it belongs in a separate follow-up PR, not this behaviour-preserving one. It would also have turned B2 into loud failures.

**N2. The guard exists but is narrow.**
- `TestNoBareInvocationLiteralsOutsideEntryConstructors` (`cmd/wb/noglobals_test.go:151-214`) works on production files. I verified it by editing a scratch worktree: it flags `&invocation{}` at `agent_remote.go:149`.
- It misses `new(invocation)` (verified: the test passes with it), `var inv invocation`, and literals inside package-level function-literal vars.
- It scans non-test files only (`:161`), so it does not catch the zz_cov `executeWorkerAssignment(&invocation{}, …)` calls (`zz_cov_archive_worker_test.go:304/314/320/387`), or any of the ~785 `&invocation{}` literals in tests. That gap is exactly how B2 happened.
- `TestNoPackageLevelFlagBoundGlobalsReappear` misses `&pkgVar.field` flag targets.

Recommendation: extend the guard to `new()` and `var` declarations, and add a test-side rule, for example a `testInvocation(t)` helper plus the B2 signature change.

**N3. The 8 newly-uncovered lines.**
- worktree.go:1515/1521 (a9745fe0) and worktree_rescue.go:153 (3674c374): the fixes thread the fixture root, and the tests again fail for the intended write error. The original intent is restored.
- cfb677de restores intent for `TestSessionResumeLocalActualCustodyRefusalDoesNotClaimRoute` and `TestCwWtWorktreeRelocateRealTaskInProcess`.
- pr.go:145/173 and ci_wait_progress.go:113/115/117: see B3.
- The message of commit 2ebcedcf says `ciWaitProgress.fail` is unreachable from the CLI, but `pr land` reaches it at `pr.go:173`. The new direct unit test is fine; the claim is not.

**N4. Added `t.Skip` calls.** `pr_test.go:52` (git missing) and `daemon_raw_execution_policy_test.go:31` (policy path unresolvable) are environmental and acceptable. However, `TestServeDashboardRefusesARawCommandWithoutAnAdministratorOptIn` depends on whether the real account has `~/.config/wb/daemon-raw-exec.json` (resolved via `user.Current`, so it cannot be overridden). On an opted-in machine it fails and actually runs `true` via the raw path. It should skip when that file exists.

**N5. The `-skip` "known flakes".** The list is from #752's review (`mut752.sh:11`): TestCwWtWorktreeRelocateRealTaskInProcess, TestWorktreeRelocateCLIJSONEnvelopeAndShortcut, TestCwWtActiveSessionListingAndCmd, TestCwWtWorkLogAdmissionAndModeErrors and TestLifecycleBackfillPlansAndAppliesOnlyMatchingCanonicalRepositories. The lane also named TestSessionResumeLocalActualCustodyRefusalDoesNotClaimRoute. I ran all six at base and head:
- Under the agent shell, the same five fail on both. They fail because the test process sits under a live registered WB session (owner PID 1754144).
- With `env -i` and `setsid`, three of those pass on both. TestLifecycleBackfill… and TestCwWtActiveSessionListingAndCmd still fail on both, which looks VM-specific and pre-existing.
- None is skipped inside the code. The disk tests (B4) are not part of this set.

**N6. `TestWorkerConnectLeasesAndExecutesARealQueuedOperation` has no deadline** (`worker_connect_daemon_lease_test.go:25`). Only the watcher goroutine cancels its context, so under mutant M15 it hung until killed. Add `context.WithTimeout`.

**N7. Residual #733 issue.** `worktrees.SetSessionResolver` is process-global. Parallel `run()` calls each install a resolver that closes over their own `inv`, and the last writer wins. This is not a data race, because a mutex guards it, but attribution can still leak across invocations. Worth a follow-up.

**N8. Size and scope.** 162 files, +3457/−1634. Production is 66 files and mechanical, so it is reviewable. It does bundle the refactor with a 61-line coverage wave and #752's N1–N5 follow-ups, where decision 3 asks for a standalone refactor PR. That is tolerable because the ratchet forced it.
- The #760 body is stale. It does not mention the coverage tests, the PATH change in pr_test, or the repo-transfer test rename.
- #760 still targets `main`; under decision 21 this branch lands locally into `cov/integration`.
- The 61 ratchet lines are all covered on Linux. worktree_merge.go:192 is reached only through the `cwWtMergeAckConstructors` table, which is itself affected by B2b.

**N9. Stray state for cleanup.** I did not delete these directories: `/home/ai/projects/.wb/worktrees/deps-npm-publish-cwfixture` and `/home/ai/projects/.wb/worktrees/npm-publish-claim-6ed7ed808c5ba937`. The lane's runs created them; my targeted runs refreshed their lock files. Remove them after B2 is fixed.
