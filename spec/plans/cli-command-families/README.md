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

**Status:** queued
**Verifies:** User journey steps 1–4.

Distinguish daemon command parsing from servers, transport, polling, process
supervision and platform behavior. Move runtime logic and its fixtures to
operational packages; daemon/worker/peers/cockpit command tests delegate through
the same narrow pattern. Do not replace genuine platform tests with mocks.

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
