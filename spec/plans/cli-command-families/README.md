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

### Task 6: Extract orchestration command families

**Status:** queued
**Verifies:** User journey steps 1–4.

Extract worktree, branch/PR, session/agent/task, fleet/repository and stream
domains in cohesive batches. Preserve aliases, ownership/landing safety,
telemetry and error classification. Move shared operations to existing internal
packages where possible instead of keeping command-to-command calls.

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
