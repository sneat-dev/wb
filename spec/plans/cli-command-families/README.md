---
format: https://specscore.md/plan-specification
status: Draft
---

# Plan: Thin executable and isolated CLI command families

**Status:** Draft
**Source:** none
**Date:** 2026-10-03
**Owner:** alex
**Supersedes:** —

## Summary

Make `cmd/wb` a thin executable, put root composition and shared CLI policy in
`internal/cli`, and move command families and their tests into packages such as
`internal/cli/cmdlayout` and `internal/cli/cmdworktree`. Command packages parse
arguments, translate options, render results and delegate operations through
narrow dependencies. Existing internal packages retain operational behavior.

The user approved an incremental pilot, then the full refactoring if the pilot
proves useful, prioritizing reusability and minimizing test execution time.

### User journey and observable results

1. A user invokes the same command, flags and arguments: help, defaults, alias
   resolution and persistent-flag acceptance remain unchanged.
2. Valid arguments reach the existing operation once with the same context and
   options. Invalid arguments retain their existing refusal and exit behavior.
3. Results, findings and partial failures retain stdout/stderr separation,
   machine-readable output, report files, and exit codes. No additional user
   action is required.
4. A developer changes one command family's argument handling: its isolated
   tests exercise that contract without building the executable, starting a
   daemon, or creating unrelated Git fixtures. Small composition tests still
   check production wiring; CI validates affected dependencies before merge.

## Approach

Start with the complete layout family (`audit`, `clean`, `migrate`), then use
the independently reviewed dependency map to extract cohesive domains. Each
extraction moves tests as well as implementation, removes the old implementation,
and reaches 100% statement coverage in the extracted command package. Keep one
implementation branch and accumulate related commits; publish meaningful batches.
Do not introduce a generic command framework or one service interface covering
all WB operations. This plan changes organization and test boundaries, not public
commands or CI coverage policy.

### Package contracts and reuse

- `cmd/wb` ultimately owns process startup and exit only. `internal/cli` owns
  root composition, invocation state, exit classification, help and shared
  persistent-flag policy. Reusable contracts live in the leaf package
  `internal/cli/shared`, so family packages never import root composition.
- Factories create fresh command-local state per invocation. Shared options
  must be read after Cobra parses inherited flags; taking an early value copy
  silently loses `--projects-root` and similar overrides.
- Dependencies use operation-specific functions or small consumer-owned
  interfaces. Production adapters use the existing internal operations; tests
  replace the effectful boundary instead of mocking Cobra or every helper.
- Reuse small output/exit contracts and deterministic fixture helpers only
  after concrete consumers establish their shape. Avoid test helpers importing
  the entire root tree or every family, which would recouple test invalidation.
- Real filesystem/Git/domain tests stay at the operational boundary. Command
  tests verify argument-to-option translation, rendering and error behavior.
  Keep a small genuine root/executable journey suite, not copies of all family
  tests in the executable package.

### Pilot acceptance and measurement

The layout pilot must preserve production behavior, remove its command logic
from `cmd/wb`, and give `cmdlayout` 100% statement coverage. The family unit
suite must run with injected operations, no daemon, executable build or Git
setup, and use parallel tests where safe. Record test elapsed time, removed
root tests/fixtures, production/test dependency direction, and remaining real
integration tests. Compare equivalent test responsibility, not unrelated full
suite times. Do not claim a whole-repository CI speedup from this small pilot.

Moving a command changes its importing root package, so root tests can still
be invalidated. The improvement depends on making those tests cheap and keeping
sibling-family tests independent. Existing affected-package CI and ratchets
remain in force; do not weaken them to make the extraction pass.
Concrete unaffected-sibling cache verification requires two extracted families
and belongs to the next adapter stage; the one-family pilot proves its dependency
boundary and isolated execution cost.

## Tasks

### Task 1: Extract and prove the layout family

**Status:** complete
**Verifies:** User journey steps 1–4 and the pilot acceptance criteria above.

Move all three constructors, rendering/report helpers and focused tests into
`cmdlayout`; bind production dependencies in one small root adapter. Cover
options, error identities and exit classification, partial reports, writers,
report failures, and separate simultaneous invocations. Run the full extracted
suite with coverage and focused root compatibility tests, then independent
review. Record actual evidence before accepting the pattern.

Pilot evidence: `cmd/wb/layout.go` shrank from 333 to 14 lines; the extracted
package has 105/105 statements covered. Its isolated suite completed in 0.340s;
focused root compatibility/journey/policy tests completed in 25.627s, and the
two additional production wiring tests in 0.948s. Vet, focused lint, static
flag/global policy and parallel-baseline guards passed. Independent review r1
found no remaining issues after an explicit default-option test was added.
These are local execution measurements, not whole-CI performance claims.

The pilot establishes a useful boundary: root no longer owns layout command
implementation or renderer tests, tests inject operations per invocation,
and one report writer replaces three copies. Existing real Git/executable
journeys remain a measurable integration cost. The user's conditional
authorization therefore applies to the remaining rollout.

### Task 2: Inventory families and shared dependencies

**Status:** complete
**Verifies:** User journey step 4 and package contracts above.

Map every production file and test group in `cmd/wb` to a command family,
shared CLI contract, operational package, or executable journey. Identify
cross-family callers, mutable globals, fixture builds and waits. Review the map
for cycles, excessive package fragmentation, and duplicated test setup.

The [domain map](./domain-map.md) records proposed ownership for the
entire current inventory. Shared contracts have one writer before independent
family extractions begin, avoiding parallel rewrites of invocation and exit
semantics. CI wait/audit and quality/selection are the next adapter domains;
the grouped stages below describe the full scope rather than a rigid order.

### Task 3: Consolidate shared CLI contracts and root composition

**Status:** in_progress
**Verifies:** User journey steps 1–4.

After the pilot passes, establish invocation/exit/flag/help contracts in the
shared leaf with clear dependency direction. Keep the explicit registration
table in `cmd/wb` while remaining constructors are in package `main`; a normal
import cannot call them. Use bounded temporary adapters without duplicating
operations or introducing plugin registration. Replace those adapters and move
final root composition to `internal/cli` after family cutover. Test shared policy
once and retain small registration and end-to-end executable checks.

The first shared-contract batch is committed as `2034043a`: a lazy invocation
snapshot and the existing root coded-error factory serve layout and subsequent
families; one format validator serves both extracted and legacy callers. Shared
and layout suites have 100% statement coverage. Independent review found no
remaining issues. Root composition remains queued until constructors can be
imported; this task is therefore still in progress.

### Task 4: Extract independent maintenance and inspection families

**Status:** in_progress
**Verifies:** User journey steps 1–4.

Follow the dependency map for disk, archive, layout, installation/version and
other independent reporting families; group by domain, not file/function count.
Move complete family tests and reuse established CLI contracts. Keep newly
extracted packages at 100% coverage.

Disk extraction passed independent review with 30/30 statements covered and a
0.288s family suite. Six former root tests using collector/environment fixtures
now exercise the family through a fake collection operation; two production
wiring checks remain (0.630s test execution, 13s governed command including
compilation). After a genuine disk-only test-source change, an identical ordinary
two-family run reused layout from cache and reran disk in 0.283s. This verifies
one concrete sibling-cache case, not all coverage modes or whole CI timing.
Archive extraction measures 64/64 covered statements and a 0.249s family suite.
The retained real archive authorization journey now supplies PATH, projects root
and HOME only to child processes and can run in parallel. One executable runner
replaces duplicated subprocess setup in the smoke and layout helpers. Focused
archive/root/smoke/layout journeys passed in 4.268s; lint and vet passed. Archive
text output now propagates writer errors while retaining successful output bytes.
Independent reviews r3 (disk) and r5 (archive/runner) accepted the batch with no
remaining findings. Publication remains deferred while reviewed commits accumulate.

Maintenance now separates `cmdinstall` and `cmdskills` from the Cobra-free
`wbupdate`, `wbskills` and `claudesettings` operations. Scoped profiles cover
333/333 statements, and the five-package race check, affected vet and lint pass.
The selected root production footprint shrank from 1660 to 554 lines. Pure
skills command tests execute in 0.267s; genuine embedded-bundle journeys remain
below the CLI and take 13.746s. These are different responsibilities, not a
matched before/after benchmark. One immutable parent source descriptor serves
eight real harness subtests; every child retains its own writable target and
upstream safety validation. The existing engine is reused rather than copied.
Root preserves real shell quoting, lazy daemon timeout bounds and exact exit
error identity. Capability evidence references follow the migrated tests;
its complete schema/runtime/evidence guard passed. Independent review r15
accepted the exact source, assertion migration, metadata and documentation.

A subsequent test-only improvement gives seven of the eight embedded harness
journeys explicit per-command Home/Getenv callbacks and runs them in parallel,
with separate writable targets and output state. The remaining serial cursor
journey retains real process-environment binding. Every original assertion and
upstream preparation/revalidation remains. The selected package race check
passed in 14.017s. Go's parent-test elapsed field excludes parallel children,
so it cannot establish an improvement over the earlier serial group. This
change establishes safe fixture isolation and parallel execution; no measured
speedup or production coverage change is claimed.

The complete hooks family now delegates through `cmdhooks`, with reusable
executable/quoting, settings merge, fleet discovery and lifecycle backfill
operations below the command layer. Scoped profiles cover 587/587 new or moved
statements and 21/21 root wiring statements. The 49 original tests have explicit
destinations; real hook exit codes, settings permissions, lifecycle workers and
agent security journeys remain. Isolated family tests avoid native setup, and
native root journeys reuse the existing shared executable builder. No matched
before/after timing or whole-CI speedup is claimed. Independent review r19
accepted the exact source, original assertion migration, metadata and evidence
with no remaining findings.

Independent architecture review r26 approved the complete wait family and its
Cobra-free polling/registry composition. Fresh root factories supply the
existing checks, agent and operation commands, avoiding sibling imports and
shared Cobra instances. The cohort also fixes two verified issues: warning
writer failure must finish/join started progress before releasing registration;
the read forecast uses the snapshot reader's six calls for an ordinary complete
observation. Early failures can use fewer and red-head detail can use more, so
the forecast is not a universal minimum or a billed-cost guarantee. Preserve
real identity/required-check and registry/liveness obligations. Extracted race
suites passed with 318/318 statements covered (175 command, 143 service),
including the native registry suite. Vet and lint passed. The combined root
gate passed all 22 selected tests in 3.103 seconds and covered the wait root
adapter at 17/17 statements. Independent implementation review r28 accepted
the exact source, all 31 original test responsibilities, moved capability
references and affected CI routing, with no remaining findings.

### Task 5: Extract quality and change command families

**Status:** in_progress
**Verifies:** User journey steps 1–4.

Extract coverage/check/verify/CI and dependency/migration/run domains in their
reviewed dependency order. Keep reusable output and progress helpers separate
from root composition, and retain genuine subprocess/Git tests at service
boundaries rather than in every argument test.

CI wait/audit implementation uses the established runtime, shared JSON flag
binding and a leaf progress renderer that remaining landing commands can reuse.
Repository selection and audit comparison/sorting move to `ciaudit.AuditBatch`.
Focused coverage measures cmdci 159/159 statements, shared progress 142/142,
shared contracts 19/19 and ciaudit 489/489 (including existing audit operations).
Family execution is 0.327s; genuine observer assertions remain operational tests,
and representative executable/root journeys remain at composition. These are
scoped implementation receipts; independent review r4 accepted the CI extraction
with no remaining findings. A combined race run over all six extracted/shared CLI
packages also passed. These checks do not constitute a new repository-wide
coverage measurement; quality adapters and final root composition remain queued.

The reusable `reposelection` prerequisite now owns selection and bounded
dispatch for quality, status, fleet and remote consumers. It covers 70/70
statements; race execution passed in 1.406s and focused root guards in 2.782s.
Single-worker dispatch runs directly without a worker queue. Temporary root
adapters preserve existing consumers until their family cutovers. Independent
review r7 accepted the exact implementation without findings.

Version parsing/rendering now lives in `cmdversion`, with one lazy metadata
snapshot from the existing `buildinfo` package. Command tests need no executable,
environment changes or Git fixtures: 27/27 family statements and 24/24 buildinfo
statements are covered, with family execution in 0.285s. Genuine root heartbeat
and production-consumer guards remain; focused root version checks passed in
4.030s. Writer failures now correctly produce exit 1 on all four version paths,
with a failing-before regression receipt. Independent review r8 accepted the
implementation without findings. These scoped results do not update total
repository coverage or establish a whole-CI timing comparison.

The complete quality cohort now separates `cmdquality` adapters from the
Cobra-free `qualityrun` service: coverage modes, baseline/summary/worklist,
verify/check, deadcode and fleet stored coverage. Scoped race coverage measures
435/435 adapter and 523/523 service statements; suites passed in 1.646s and
5.925s. Focused root guards passed in 3.408s (9/9 new factory statements), and
native journeys in 4.009s. Independent review r11 accepted the exact 64-path
implementation and assertion migration without findings. Existing policy,
Git/Go, durable-artifact and executable guarantees remain at their responsible
boundaries. NoWork, NoRecords and worklist output now propagate writer failures.

Adapter tests use no filesystem, environment mutation or subprocess fixtures.
Two real read-only worklist fixtures now share one parent setup, reducing eight
file writes to four; memory-store read tests share one populated parent fixture
across four parallel children. Mutating workflows retain private writable
fixtures. These are concrete setup reductions, with no measured elapsed saving
or new whole-repository coverage percentage claimed. Install/skills are now
extracted; later domains, final composition and delivery remain pending.

The complete run family now owns argument handling and rendering in `cmdrun`,
with Cobra-free execution, changed-package selection, recipes, history, queue
and submission operations in `runexec`. Actual worker and daemon consumers
share environment and operation-receipt helpers; shared Git and queue fixture
helpers replace duplicate setup. Scoped profiles cover 499/499 new or moved
service, adapter and helper statements, plus 13/13 root factory statements.
The isolated command suite takes 0.593s and the execution-service race suite
3.873s. These measure different responsibilities, not a before/after speedup.
Native Git, admission, child streams/exit codes and authenticated daemon
submission assertions remain at their actual boundaries. Independent review
r20 accepted the exact source, original assertion migration, scoped profiles,
native results and affected-CI test-import closure with no remaining findings.

The next approved change cohort extracts the complete local and hierarchical
migration command. `migraterun` sequences the existing migration effects;
`cmdmigrate` owns arguments, output and exit policy. Shared campaign progress
serves migration and the existing dependency/worktree consumers. Preserve
cleanup's bypass of report setup, progress lookup timing, progress completion
before persistence, and report/format errors before campaign execution errors.
Independent architecture review r23 accepted this boundary. The implemented
cohort has 219/219 new or moved adapter/service/progress statements covered,
plus 7/7 root factory/forwarding statements. The selected combined root gate
passed 36 top-level cases in 14.811s; one additional existing quiet-policy case
completed the forwarding profile in 0.735s without another executable build.
Full extracted race suites passed. Independent implementation review r24
accepted the exact source, original assertions, native obligations, scoped
profiles and logical root-profile union with no remaining findings. These
scoped results are not a whole-repository measurement.

### Task 6: Extract orchestration command families

**Status:** in_progress
**Verifies:** User journey steps 1–4.

Extract worktree, branch/PR, session/agent/task, fleet/repository and stream
domains in cohesive batches. Preserve aliases, ownership/landing safety,
telemetry and error classification. Move shared operations to existing internal
packages where possible instead of keeping command-to-command calls.

The next approved cohort extracts the complete repository family and a shared
status boundary. Cobra-free `repostatus` collects and filters results;
`statusview` owns shared command presentation. Repository status, historical
status, fleet status, overview and stats all consume those implementations.
Preserve status report writes before stdout-format refusal, and keep
init-remote notices non-returning so a closed output stream cannot newly stop
an already-started publication. Native status fixtures remain privately
writable because optional Git index writes have not been ruled out.
Independent architecture review r22 accepted this boundary. Scoped profiles
cover 306/306 new or moved statements across the repository adapter, shared
status presentation/collector and Git init sequencing, plus 10/10 root adapter
statements. All 43 original repository/status tests have recorded destinations,
and the 59 protected declarations remain unchanged. Extracted race suites,
actual Git/ignore/failure cases, vet and lint passed; the combined root gate
above also serves this cohort. Independent implementation review r25 accepted
the exact source, original assertions, profiles, metadata and native evidence.
It caught a private fixture's reliance on ambient Git identity and missing
bare-origin maintenance configuration; the setup was corrected and the one
affected native race test passed. The review has no remaining findings. No
matched runtime saving or repository-wide percentage is claimed.

Independent architecture review r27 approved the complete stream family and
neutral service composition. Existing stream/worktree/link/hook engines retain
authority; a service outside `streams` avoids the existing worktrees-to-streams
dependency cycle. All six verbs share that boundary. Preserve partial
persistence, failure identity and the exact missing-graph transitive-membership
finding after successful start. Named status and end-preview operations fetch
refs, so their fixtures remain privately writable. Only proven immutable
list/graph inputs are shared. Extracted race suites passed with 622/622
statements covered (425 command, 197 service). The new store helper is covered
at 7/7 statements; existing native stream/streamsync authority checks also
passed. Vet and lint passed. The same combined root gate covered the stream
root adapter at 7/7 statements. These scoped profiles do not establish a new
repository-wide percentage or a matched runtime saving. Independent
implementation review r29 accepted the exact source, all 52 original test
responsibilities, 21 unchanged protected bodies and native evidence, with no
remaining findings.

Independent architecture review r30 approved the complete branch family:
list, count, cleanup, quarantine and archive-target. Four existing worktree
operations provide the boundary; list/count reuse the same inventory operation,
without a new orchestration service. Keep native fetched-target, deletion,
quarantine and archive-policy authority below the command adapter. The cohort
preserves 36 original test responsibilities and three unrelated protected
bodies, including two archive tests found outside the initial focused inventory.
YAML compatibility retains concrete date-marshaling failures while removing
only conversion branches proved unreachable for the actual result types.
Extracted race suites cover 271/271 statements; the native validation suite
also passed. Eight actual branch root cases passed and its factory is covered
at 2/2 statements. Independent implementation review r32 accepted the exact
273/273 designated scope, all original responsibilities and sequential shared
file handoff, with no remaining findings. A new root wiring fixture was
corrected to invoke the genuine family factory with private state, avoiding
ambient checkout heartbeat writes. No broad suite was repeated.

Independent architecture review r31 approved complete PR create/update/land,
including create's auto-merge/land paths. A neutral selector serves PR and wait;
small shared landing composition functions reuse the existing identity, link,
event and lifecycle authorities for PR and worktree consumers. Command adapters
preserve lazy flags, exact refusal classification, partial receipts before
errors and landed-incomplete exit/resume behavior. Native link-before-GitHub,
session/lane, publication and lifecycle guarantees remain genuine tests. The
branch and PR slices of their shared test file use sequential whole-file
handoff. Extracted race suites cover 433/433 statements (299 command, 119 shared
landing composition, 15 selector); final root PR/landing bindings cover 16/16,
for a designated new/moved scope of 449/449. The separately modified wait
selector binding is covered, and all five existing wait root/alias cases passed
with the touched factory at 17/17. Native full-root fixtures now use private
working directories; original quiet sink assertions remain intact.

The initial shared root gate recorded 26 passes and four failures caused by a
test adapter executing parent help instead of the genuine child operation.
Only those four native cases were retried after correcting the adapter, and
all passed. A focused actual dependency-binding check covers final `pr.go` at
11/11. Root profiles use a logical block union; stale coordinates from the
production factory extraction are excluded entirely. Vet and lint passed.
These scopes do not establish a new repository percentage or a matched runtime
saving. Independent implementation review r33 accepted the exact source,
all 56 original responsibilities, protected declarations and six actual test
reference migrations (including branch), with no remaining findings.

Independent architecture review r34 approved the complete agent family and
private remote protocol at committed base `6bebf086`. Command adapters delegate
to a Cobra-free agent service that retains existing dispatch, store, remote
transport and process authorities. Dispatch and worktree creation share narrow
checkout preparation helpers instead of constructing another command to run
its operation. Await uses instance-bound clock and wait functions; native
identity, cancellation, fixed SSH arguments and stdin privacy remain tested.
Remote dispatch warnings use the bound stderr stream. Extracted race suites
cover 487/487 statements (311 command, 150 service, 26 shared checkout setup);
the actual native integration package also passed. Final isolated command
tests independently cover 311/311 in 0.437 seconds without filesystem, config,
Git or environment fixtures. Final targeted race checks, vet and lint passed.
All 49 original agent responsibilities and three shared responsibilities have
verified destinations; unrelated worktree helpers and mixed test fragments
remain unchanged. The six actual agent/root safety cases passed in the shared
root gate, covering its designated adapters at 16/16, for a total scope of
503/503. The same gate's unrelated fleet fixture assertion failed and its
one-case corrected retry passed; both receipts remain recorded. Root coverage
uses identical-position block unions. These scopes do not establish a new
repository-wide percentage or a matched runtime saving. Independent
implementation review r36 accepted the exact frozen source, original tests,
protected bodies, metadata and evidence bundle, with no remaining findings.

Independent architecture review r35 approved the complete fleet family,
including merge-policy and default-branch workflows. Neutral discovery and
inspection owners serve real fleet, run, sync and dependency consumers; the
existing quality-owned coverage child remains attached in root composition.
Policy algorithms retain checkpoint ordering, archival restoration, remote
mutation and atomic persistence guarantees. Report collection and file writes
retain their existing ordering; concrete date-marshaling failures remain
reachable tests. Stats and overview use command-bound output and propagate
writer failures. A production-used, instance-bound persistence function makes
specific checkpoint failures deterministic while retaining the existing
atomic implementation and native tests. Only proven redundant serializer and
control-flow branches were removed; real authority and checkpoint refusals
remain tested.

The final race profiles cover 2,635/2,635 designated leaf statements,
including the six-statement shared GitHub diagnostic helper. The combined
six-package profiles additionally cover 212 unchanged PR-inventory
statements, kept separate from the new/moved scope. Root adapters cover 10/10,
for a designated total of 2,645/2,645. Root-inclusive vet and lint passed.
All 192 original test responsibilities have actual destinations, and protected
mixed bodies remain unchanged. Historical root journeys retain real command
dispatch, private working directories and bound output. The shared gate's
one new binding fixture expected an error for an empty repository that the
service correctly classified as blocked; only that corrected case was retried,
and it passed. Final review caught a new heartbeat test waiting 9.25 seconds
inside fake discovery. A controlled per-instance timer now exercises the real
heartbeat path without that sleep, retaining the production nine-second
interval and joining shutdown before return. The fresh merge-policy race
profile covers 558/558 and replaces all prior merge-policy blocks; other
package and root profiles remain unchanged. Its observed package time was
1.823 seconds versus 12.153 in the earlier six-package batch, under different
loads; this is not a controlled timing comparison or a whole-CI saving.
The profile establishes scoped coverage, not a new repository-wide percentage.
Independent implementation review r37 and its heartbeat amendment accepted
the exact frozen source, original tests, protected scopes and validation
receipts, with no remaining findings.

Independent architecture review r38 approved the next complete session/task
cohort, with neutral move/park composition above existing protocol packages
and a shared bounded input reader. Task launch will consume the move operation
instead of executing a sibling Cobra command. Preserve move output before
pickup persistence, the existing detached task-launch and listing contexts,
and park's post-persistence warnings and registration details. Only the public
park output is serialized. Native descriptor, ownership, custody, transport
and persistence guarantees remain genuine tests. The agent/fleet cohort is
committed locally as `75545ab895d5442a47a352c29eacf8bde10541a8`; the r38
checkpoint review verified all 28 owned and six shared source bindings against
that commit. The full session implementation now separates command boundaries
from neutral operations and presentation. Independent review r39
accepted a disjoint task lane owning its four existing files and the new task
service/command packages, with all 13 original test responsibilities mapped.
The session lane owns the shared input, move service and presentation code;
the task lane must consume the actual root move-service factory after its
explicit API handoff. Both lanes batch leaf validation, followed by one shared
root gate and implementation review before the next substantial local commit.
Independent review r40 also approved a bounded continuation-reader effect seam:
instance-local path resolution, descriptor traversal/stat, owned file access
and the separate handover open policy. Native security assertions remain;
deterministic tests exercise actual I/O error, release and tamper boundaries.
The immediate nil-file branch after a successful native open was proved
unreachable on Unix and Windows and removed. This preserves inherited Windows
identity/link behavior; it does not establish new Windows hardlink protection.
The current session/input/presentation/producer-helper/snapshot scope is
1,162/1,162 statements; the task leaf scope is 150/150. Fresh profiles exclude
shifted list/resume source coordinates and combine identical blocks by maximum
hit count. Race, vet and lint pass for these leaf scopes. The combined root gate
covered its 22 designated statements and passed 18 selected checks, but caught
a native park/resume fixture mismatch. Creation records AgentID separately from
the claim session link, so the initial fixture selection missed members with
real PID/time custody. The corrected fixture selects its exact two known source
paths, verifies identity and custody, and preserves all capture, immutable retry,
transport and receipt assertions. The sole affected native retry passed with
race enabled (26.838 seconds package time; 33.376 seconds including admission
and build). The initial failed execution remains recorded, alongside the
18 passing combined checks; no repeated whole-root suite was needed. These scoped counts do not
establish a new repository-wide percentage or net coverage gain.

The extraction also reuses the existing process-owned source executable build
for the cross-process journey and moves tree snapshots into the existing
internal test support package. Pure boundary tests use per-instance operations;
native stateful fixtures remain private. Read-only sharing is appropriate only
when the full call path has no writes, cleanup or discovery side effects.

Native package extraction exposed a test-process isolation gap: the moved
session integration package initially omitted root TestMain's user-state
isolation. Real worktree discovery consequently scanned configured shared
roots, including cleanup-capable inventory. That run has no mutation receipt,
so it does not prove ambient housekeeping left user state unchanged. The
existing user, harness and process isolation is now restored before native
tests run. A focused three-case retry passed in 0.675 seconds; earlier case
times were 7.55, 13.73 and 7.58 seconds in the larger unisolated batch, not a
controlled benchmark. Future extraction proposals must account for TestMain
and process isolation before the first native run, including configuration
sources beyond an explicit projects root.

### Task 7: Separate daemon runtime from CLI adapters

**Status:** in_progress
**Verifies:** User journey steps 1–4.

Distinguish daemon command parsing from servers, transport, polling, process
supervision and platform behavior. Move runtime logic and its fixtures to
operational packages; daemon/worker/peers/cockpit command tests delegate through
the same narrow pattern. Do not replace genuine platform tests with mocks.

The first runtime cohort moves controller lifecycle, local authenticated
transport, file bridge, polling, identity, locking and platform supervision into
Cobra-free `internal/daemonruntime`. Root retains command composition and its
pinned lifecycle store; daemon protocol and durable operations remain in
`internal/daemon`. All 396 original test responsibilities have destinations:
198 native runtime, 118 affected root cases and 80 protected cases. Of the
protected cases, 72 bodies are unchanged and eight qualify moved fields,
constants or validation calls; one of those eight additionally tests the genuine
default owner transport while preserving its original assertions. Capability metadata relocates ten test paths
without changing assertions or capability definitions.

Per-instance private dependencies expose failure boundaries while successful
paths use actual private filesystem state, descriptor checks and ownership
records. Simulated process identity, supervisor mode and failed operations are
unit policy/effect proofs, not native platform evidence. The owner-token
transport factory is a narrow public exception shared by actual production
clients and portable authenticated fixtures; it preserves the existing cloning
and bearer-token behavior. Impossible catches for Go 1.27 random reads,
concrete JSON envelopes and successfully opened descriptors were removed after
review of their producer contracts, without removing security rechecks.

The complete macOS runtime race run passed 240 top-level tests and covered
1,906/1,906 statements, with 12.107 seconds package elapsed time. The scoped
root race gate passed all 144 selected cases in 22.173 seconds package elapsed
(28.219 seconds including admission/build), with source hashes unchanged.
A sole additional peer-client race case passed in 1.843 seconds using the
actual default local transport and stored owner token. The new runtime/shared
handoff bindings cover 6/6 statements, and the changed peer fallback covers
1/1. Three pre-existing dashboard callback statements remain uncovered in the
broader qualification-only diff; this batch does not claim whole-root 100%.
Root-inclusive vet and platform-aware lint passed. Linux and Windows test
packages compile; those platforms were not executed. One Unix-only metadata
test was relocated behind its platform build constraint after Windows compile
caught it; production source remained unchanged. The additional common-root test also initially
referenced a Unix-only fixture helper; its portable private-root setup was
corrected, then the sole race case and current root Linux/Windows test-package
compilation, vet and lint passed. These results do not establish
new repository-wide coverage or native Windows security coverage.

Twenty-three private-fixture cases and six long bridge cases now run in
parallel; genuine environment-forwarding cases remain serial. Fixtures register
joined cleanup immediately, including early-failure paths. The earlier runtime
race package took 22.340 seconds under a different batch/load, so the faster
final run is an observation, not a controlled benchmark or CI-time promise.
Active `WB_PROJECTS_ROOT` pins were redundant where explicit roots win;
retired compatibility pins were separately removed. Actual environment
forwarding remains tested. The daemon and Cockpit CLI cutovers are recorded
below; subsequent worker/peers cutovers and remaining host work are recorded here.

The next coordinated cohort extracts Cockpit open/export into
`internal/cli/cmdcockpit` and `internal/cockpitrun`. Its 50 original test
responsibilities retain actual owner-channel authentication, export HTTP/store
behavior and a fail-closed no-start guard. Local lifecycle defaults are created
only inside the invoked Local operation; export has five read effects and no
lifecycle bootstrap. The shared JSON selector preserves boolean-JSON precedence
and removes the private duplicate. Exact final leaf profiles cover 214/214
statements (83/83 family, independently covered by cheap unit tests, and 131/131
service). The replaced command file is counted only at its final coordinates.
Native export race tests passed in 2.985 seconds. Root callback coverage is 13/13. The joint gate initially passed 64 of 66
selected cases. Its new invalid-listen fixture wrongly expected an empty
directory, despite existing lifecycle directory/lock bookkeeping. The corrected
case proves typed refusal, absent state/socket and released ownership. The
no-start guard was updated for actual Go builtins/conversions, its exact local
error closure and verified Alive/Client function-value bindings. Unknown calls,
argument traversal and the original exactly-one launchd print check remain
protected. Both failing cases passed targeted retries; no full-root rerun was
needed. No repository-wide coverage is claimed.

Daemon serve, lifecycle and operation commands are extracted alongside
Cockpit with disjoint file ownership. The actual server composition remains a
plain context/stream callback; controller, transport, queue and policy authority
are reused. The alternate `wait operation` factory binds the actual new command
constructor, without a sibling-family import. The daemon cohort preserves 96 original test responsibilities: 11 moved, 42
retained native/adapted and 43 unchanged protected bodies. Its designated leaves
cover 332/332 statements: command family 197, renderer 81, operation service 46,
shared selector seven and terminal predicate one. The raw profile additionally
contains 28 existing helper statements, excluded from these designated counts.
All 42 retained native cases and the repeated-execution regression passed.
The approved serve change keeps derived state paths execution-local, preventing
the first invocation's resolved path from becoming a later implicit pin.

Three moved serve-preparation error returns were closed with actual private
blocked-path/malformed-record cases and an explicitly simulated per-instance
Token error contract. They preserve error identity, unchanged records and no
later listening/persistence; all three passed race tests. The daemon root
factory/preparation/forwarder declarations cover 34/34 statements, plus the one
changed alternate wait binding. With Cockpit's 13 root statements, the combined
new/moved designation is 594/594 (546 leaves, 48 root). Identical production
blocks use maximum hits across the preserved initial gate and targeted retries.
The qualification-only worker heartbeat call and two unchanged server diagnostic
statements remain unexecuted in this selection; they are separate from the
new/moved scope. Server-body equality after parameter substitutions is recorded.

All nine affected root/leaf test packages compile for Linux and Windows; those
test binaries were not executed. Root-inclusive vet and lint passed, as did the
private current-source WB build. The last neutral AST-only guard correction
postdates target compilation and adds no platform-specific code. Final source
review and normal-hook local commit bind the coordinated cohort; no publication
or whole-CI timing improvement is implied.

The complete worker connection, registration, assignment, heartbeat, CPU
admission and bounded-tail journey now lives in `internal/workerrun`, with
parsing and announcement rendering in `internal/cli/cmdworker`. All 11 original
test responsibilities have destinations; two genuine root factory and queued
operation journeys remain. Native private-assignment tests stay in the service
package, avoiding test-only exports. Existing daemonruntime, process, runqueue,
runenv and operation receipt contracts remain authoritative; runexec's different
synchronous execution contract was not broadened to share an incompatible loop.

Worker designated coverage is 224/224: family 34, service 186 and root binding
four. The complete native service race run passed 12 top-level cases in 1.350
seconds package elapsed (1.971 including admission/build). Its initial sandbox
listener refusal and a new StringArray repeat-execution fixture mistake are
retained as failed receipts; the corrected sole command case passed. Pure
command cases use independent parallel in-memory fixtures; actual writable
queues, CPU pools and child-process tests retain private state and joined cleanup.

All seven peers verbs now live in `internal/cli/cmdpeers`, with Cobra-free
workflows in `internal/peersrun`. All 50 peers and eight reused-helper original
responsibilities are preserved. Five authenticated admin/read tests remain at
root; two renderer-only golden tests moved to the cheap command package.
Existing peer handlers, authenticated admin transport, atomic file writes and
protocol DTOs remain authoritative. Credential reading/private writing, origin
comparison and age presentation each have one shared owner with genuine retained
consumers. Invite's exclusive one-time token policy remains distinct from
machine credential idempotency. The sole bool-state JSON encoding error catch
was removed after review of its concrete producer; real filesystem checks remain.

The invite rescue result retains the normalized attempted path internally while
its JSON omits token_file and reveals the intended one-time token. Relative-path
normalization, writer-error precedence and actual upstream trust JSON fields are
asserted through command execution. Mutating mint/join/trust/persistence cases
have private writable state; immutable render inputs are cloned when needed.
Peers designated coverage is 537/537: family 172, service 279, credential 35,
shared age eight, origin 13 and root 30. Identical dependency blocks are deduplicated
before maximum-hit profile union. The first native race batch passed 41 top-level
cases in 2.443 seconds including admission/build; no native authority rerun was
needed to close subsequent pure service/adapter assertions.

The private production-used admin builder permits native child/authenticated
transport proof using existing daemon test dependencies; that is separate from
actual platform-default refusal and does not establish native launchd startup.
A writer helper thought unused still has two E2E consumers and is retained.
Seven peers capability paths and one worker path moved to actual declarations;
existing test kinds and names remain unchanged.

The joint root race gate passed 26 of 27 selected cases. Its new join fixture
wrongly expected success with default restart enabled: the test executable
rejected the CLI arguments after state was saved. The corrected sole retry
asserts the original wrapped child exit error, saved-state message, private
config/credential persistence and secret-free output, then exercises real read
bindings. That retry passed with source unchanged. Root attribution is 34/34,
including genuine retained remote enrollment, remote rendering and Cockpit route
consumers of changed shared forwarders. Combined worker/peers designation is
761/761 (727 leaves, 34 root); no repository-wide or net coverage gain is claimed.

Scoped vet and final lint passed. All ten affected test packages compiled for
Linux and Windows, without executing those target binaries. The private
current-source WB build passed after the final test corrections. Independent
implementation review and normal-hook local commit bind this cohort; publication
remains deferred. Remaining serving/hub and authenticated-client work has a
read-only domain proposal and preliminary architecture review, requiring exact
post-checkpoint ownership rebind before implementation.

The next daemon-host cohort moves serving, hub/webhook/poller/enrollment,
peer-server mounting and Cockpit mounting/export into `internal/daemonhost`.
The root supplies the genuine FleetOptions producer and keeps client and remote
publication code until their separate reviewed extractions. Forty-nine original
tests move to their native owner; 22 root journeys remain adapted and 48 protected
originals retain their responsibilities. A tagged helper with three genuine E2E
consumers remains at root. Actual authenticated Unix transport, private durable
Git stores, token/state ownership and shutdown cleanup remain authoritative.

Private per-host state/path effects delegate to the existing daemon.Store methods
and resolvers by default; stage tests replace only the failing observation. The
hostname observation is an argument of one private helper, with os.Hostname supplied
by its actual production caller. Config-first behavior and refusal bytes remain.
The infallible crypto/rand.Read catch and a redundant always-successful loopback
read authorizer were removed after examining their concrete producers; credential
and write/export authorization remain intact.

The 36 root cases passed across an initial 32 successes and targeted fixture
retries. The 52 gap cases passed across 45 initial successes, six socket/home
fixture corrections and a sole runtime-guard correction. The guard deliberately
recreates an owned stopped record after disappearance, with PID zero, original
token and actual failure reason. The final 37-case bounded hub refresh passed in
3.381 seconds with all 54 Go source states unchanged. First failures remain in
receipts; these are combined case results, not claims that the first suites passed.
Actual macOS transport execution is distinct from Linux/Windows compilation and
from injected lifecycle observations. Native Unix fixtures skip precisely on
Windows; portable command and early-error cases remain runnable. No repository-wide
coverage gain or end-to-end CI speed reduction is claimed.

Two existing retained root journeys refreshed 12 final hub statements in 8.220
seconds: the no-hub mount and the real self-hosted whole journey. A sole new
nonlistener case closes the final viewer callback through the actual mounted
private member dashboard read; its race run passed. The extracted daemon-host scope is 751/751 statements, with two genuine root
bindings also covered (753/753 combined). Only final-source hub profiles are
combined; changed serving files use fresh source-bound profiles, and unchanged
files require exact source hash equality. Linux and Windows compilation, the
private current-source WB build, capability declaration checks and SpecScore lint
passed. Publication remains deferred while later reviewed domains are extracted.

The publication/fleet prerequisite now consolidates manual and periodic fleet
collection, provider opening and publication in `internal/remotepublish`.
`internal/cockpitoptions` owns actual fleet/SSH/sampler/watcher composition;
`internal/cli/remotepublishview` owns shared output and progress. Remote publish
and sync consume the same implementation. Root FleetOptions retains its genuine
binding to daemonhost. Shared ShortPath preserves its original algorithm and
three inventory-progress consumers. Full remote command extraction remains
queued; claims/status/enrollment algorithms retain their existing authority.

All 38 selected original responsibilities have destinations and 35 protected
companions retain exact bodies. Actual source-only Git scans, authenticated
HTTP/SSH, per-publisher private fingerprint cache, durable markers and publication
before rendering remain intact. Root wrappers without genuine callers were
removed, including the obsolete periodic read override. The concrete Snapshot
YAML producer remains called; its unreachable error catch was removed under the
same existing Snapshot-marshalling invariant. Reachable JSON time errors and
all writer failures retain real tests.

The tagged native batch passed 42 of 43 cases. A mechanical field qualification
had incorrectly capitalized three YAML fixture keys; restoring their original
lowercase spelling fixed the sole configuration case in a targeted 2.158-second
retry. Production was unchanged. The original 400-repository native fixture took
78.7 seconds and was retained without another run. Fixture setup reuse and bounded
parallel setup remain performance opportunities, not implemented speed claims.
The 13 retained root wiring/sync cases passed in 8.547 seconds; a sole additional
real-Git nil-progress case proves stderr notes routing and clean JSON stdout.
Designated coverage is 337/337 statements: 317 in the owners/shared helper and 20
in six genuine root bindings. Source-bound profile union deduplicates identical
blocks; no repository-wide coverage gain is claimed. Publication remains deferred.

The worktree receipt/active cohort separates Git receipt observation into
`internal/graduation`, active inventory into `internal/worktreerun`, and argument
handling/rendering into `internal/cli/cmdworktree`. Root constructors bind real
native observers and the existing remote configuration loader. Original command
and filesystem journeys live in the adapter integration package; domain tests
retain inventory and Git authority checks. Native owners isolate harness,
process, Git and user state before fixture discovery. The mixed writer sweep
retains its unrelated orphan checks in the root package.

The isolated four-package race batch passed 50 top-level tests in 3.45 seconds;
all 306 statements across the four new production files were covered. A focused
root race gate passed four registration/default-binding cases plus the moved
writer sweep in 12.57 seconds, covering all six statements in the two root
constructors. These are bounded cohort results, not a fresh repository-wide
coverage measurement. Worktree and dependency extraction now run in parallel
with separate file ownership; root wiring changes are sequenced through scoped
gates. Publication remains deferred.

The parallel follow-up separates collaboration/session binding and redacted
checkout inspection into `worktreerun`, with argument/rendering adapters in
`cmdworktree`. Existing `worktreecollab.Service` and `Store` retain ownership,
corroborated process/session checks, messaging and persistence. Actual session
registration remains lazily bound at root. Four new files have 228/228
statements covered; the unchanged domain race result was reused after repairing
an integration build failure by moving a private presentation assertion to its
proper CLI test owner. The repair gate passed in 2.44 seconds.

The first dependency cohort moves graph/drift/peers/set/bump into `cmddeps` and
`depsrun`, composing the four remaining children at root. Existing dependency
engines remain the authority. One closed concrete-report renderer replaces four
switches; a type-tree invariant test protects its infallible serialization
premise. Native report-home and checkpoint/resume inputs remain separate from
engine options. Shared validation and derived-scope rules have genuine root and
family consumers. Propagation refusals and early bump failures now stop owned
progress correctly; actual npm selection progress remains intact. The fresh
leaf race batch passed 46 tests in 3.65 seconds with 517/517 statements covered.
Nine original external integration cases remain separate earlier journey
proof, not a profile attributed to later production changes.

One combined root race gate ran 17 targeted cases, including distinct registered
processes, linked Git checkout custody, prompt redaction, active merger lanes,
session registration and real npm adapters. Sixteen passed; a new registry test
fixture missing its Git origin failed. A sole-case retry passed after adding a
private bare origin without weakening the actual fetch. All 26 statements in
the changed root declarations are covered, yielding 771/771 for these two
cohorts. Source-bound passing native cases were not repeated. These counts do
not measure repository-wide coverage or end-to-end CI speed. Final scoped lint
and serial checks, target compilation, review and local commits close the
cohorts; publication remains deferred. The full repository parallel baseline
is still failed and has not been attributed to an old target without evidence.

The next parallel cohort extracts complete inventory list/summary and dependency
go-directive check/report domains. Inventory delegates directly to the existing
ListWithDiagnostics operation and shares diagnostics/artifact rendering, while
keeping list and summary defaults and state precedence distinct. Five original
pure contracts move to their CLI owner; native privacy, purge receipts, filters
and journal behavior remain at the production root entrypoint. Its eight-case
race batch passed in 2.46 seconds with 175/175 leaf statements covered.

Go-directive retains actual Go assessment/application authority, synchronous
mutation/output order, read-only report behavior and original error/exit policy.
Module discovery now has one neutral owner, used by directive operations and two
existing policy paths. Thirteen original responsibilities move with the domain;
23 selected race cases passed in 3.31 seconds. Three new files have 190/190
statements covered. The changed default factory and root adapters bring the
combined designated scope to 380/380; this is not repository-wide coverage.

One combined root gate passed all twelve cases in 31.54 seconds with source
unchanged. These exercise the real root entrypoint inside the Go test binary,
private native Git/manifest fixtures, and both remaining policy consumers;
they do not claim separately spawned WB executable proof. The two-repository
inventory fixture took 11.37 seconds for its complete case, a future performance
candidate rather than a measured setup-only cost. Unchanged global flag guards
were reused, and no full CLI suite or global coverage run was added. Capability
metadata changes only three test paths; all 790 references resolve. Scoped
lint/serial checks, cross-platform compilation and independent final review
close the local checkpoint; publication remains deferred.

### Task 8: Verify full cutover and land reviewed batches

**Status:** queued
**Verifies:** Complete user journey and all package contracts.

Prove no old command implementations or unnecessary compatibility test copies
remain in `cmd/wb`. Verify registrations, help, aliases, persistent-flag catalog
guards, exit semantics and representative executable journeys. Use focused
local checks and existing affected CI per meaningful reviewed publication,
then WB landing, exact remote/main verification and cleanup. Report actual
test/fixture reductions and remaining bottlenecks.

## Open Questions

The reviewed file/domain inventory sets the exact extraction order after the
pilot. Whole CI elapsed-time improvement remains to be measured; a package move
alone is not evidence of reduced end-to-end CI time.

---
*This document follows the https://specscore.md/plan-specification*
