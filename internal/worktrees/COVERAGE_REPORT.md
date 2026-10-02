# Worktrees refactoring and coverage report

This report records the locally verified batch before publication. Remote CI and landing receipts are reported separately.

## Result

- Package: `internal/worktrees`.
- Native runtime: macOS, Go 1.27.1, E2E enabled, race detector enabled.
- Statement coverage: **100% — 16,867 / 16,867 statements** from successful profiles.
- Whole CLI coverage: not remeasured locally for this batch.
- Landing requirement: protected-main CI and an authoritative WB remote receipt.

The measurement covers the production files compiled for this runtime. Linux and Windows vet checks are compile/static checks, not native execution coverage. CI must establish the candidate's platform results separately.

## Architecture changes

The batch follows six domains: branch transitions, shell/residue retirement, orphan inventory, retired evidence, work-log verbs, and dirty/legacy recovery. Shared implementations replace duplicate behavior where the contracts agree. Operations with distinct selection or retention semantics keep those policies explicit.

Concrete examples include sharing directory synchronization instead of the duplicate `syncFile` body, using one authoritative retired-repository validator, extracting orphan report finalization, and separating observations from filesystem effects in cleanup and recovery. Per-call observation points let most fixtures introduce real filesystem drift at the relevant boundary without new global mutable hooks. The competing-publication fixture reuses an existing serial publication hook.

Native refusal tests exercise actual held descriptors, competing immutable publication, replacement identities, missing paths, and permission failures. Assertions retain original bytes and owned inode identity where refusal must preserve evidence. Child processes isolate process-wide current-directory and home changes.

Deleted code is reported separately: 13 statements removed, including three historically uncovered statements identified in a source-matching failed historical profile. Those removals are not new runtime coverage hits.

## Validation and efficiency

The final coverage evidence must come from two successful complementary cohorts, selecting all 2,074 top-level declarations exactly once. Their atomic block coordinates and statement counts must match before actual counters are summed. Retained A has four explicit runtime skips; replacement B has none. Together the cohorts report 2,070 passing tests and four skips. The retained successful cohort runs 640 declarations; replacement B runs 1,434 declarations.

The first B run exposed two assertion errors: an obsolete orphan diagnostic and identity comparison of independently allocated error wrappers. The replacement preserves the native cause and operation/path diagnostic. Only two B-owned test files changed; all production and module bytes match retained A. Failed profiles are excluded from final coverage evidence.

The final audit passed for all 65 original logical components, changed/new bodies, closures, and all package statement blocks, with no crossing, overlapping, or unassigned blocks. After the test-only parallelism repair, its focused native E2E/race test passed, all three policy guards explicitly passed, and pinned default/E2E lint reported zero issues. Earlier native/Linux/Windows vet checks passed; their snapshot predates the final test-only edits and is not presented as a final-source cross-platform runtime result.

The successful A cohort took 2,612.346 seconds of native package elapsed time (2,613.624 seconds including WB). Original B took 2,220.421 seconds natively (2,221.564 seconds including WB) and failed. Replacement B took 2,207.480 seconds natively (2,216.824 seconds including WB). Retaining A avoids another complete A run. WB's protected PR route uses authoritative strict CI for landing instead of repeating local candidate validation.

The successful merged atomic profile SHA-256 is `bd3cb05ec54b615bd3952d2b75740d56fa6ba39a3da0cd08c3205d99105c313c`. The body audit SHA-256 is `823b2fd4925f6a9f7921edf33a8931180cd72cf919884c606caff15b4eae16e1`. The final test-only parallelism patch changes no production coverage coordinates.

## What worked and next steps

Domain batches preserve behavior and amortize fixture setup and test execution. Refactoring duplicated bodies reduces both maintenance and the surface to cover. Writing a cohesive batch before testing is useful; running the whole package after every small edit was expensive. Rare native refusals still need focused tests designed around their real boundary.

Validation latency is now a separate concern from missing statements. Passing test timings identify candidates for fixture measurement; elapsed time alone does not isolate setup cost or CPU cost. Follow up with measured immutable fixture seeding, explicit environment/config dependencies that allow safe parallel tests, and removal of unnecessary waits. Preserve independent mutable repositories and actual race diagnostics.

Do not start coverage programmes for other packages with more than 100 uncovered statements under the current authorization. Smaller packages may be batched by remaining uncovered statements. Do not claim whole-CLI completion from this package result.
