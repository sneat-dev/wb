---
format: https://specscore.md/feature-specification
status: Draft
---

# Feature: PR Land Waivers

> [SpecScore.**Studio**](https://specscore.studio): | [Explore](https://specscore.studio/app/github.com/sneat-dev/wb/spec/features/pr-land-waivers?op=explore) | [Edit](https://specscore.studio/app/github.com/sneat-dev/wb/spec/features/pr-land-waivers?op=edit) | [Ask question](https://specscore.studio/app/github.com/sneat-dev/wb/spec/features/pr-land-waivers?op=ask) | [Request change](https://specscore.studio/app/github.com/sneat-dev/wb/spec/features/pr-land-waivers?op=request-change) |
**Status:** Draft
**Source Ideas:** integration-branch-batch-landing

## Summary

Make `wb pr land --waive-check` usable on integration branches: settle pending waived checks before judging them, accept a bounded first-parent baseline when the target tip never ran a check, waive an aggregator whose `needs:` are all waived, and never waive a required check.

## Problem

On datatug/datatug-apps `public-demo` (2026-10) every waiver attempt failed
for a reason unrelated to safety:

- a waived check still running on the PR was judged immediately and refused
  `waived-check-not-failed`;
- `deploy-gate` never ran on the `public-demo` tip, so it was refused
  `waived-check-absent-on-target` even though it was red on the commit before;
- `deploy-gate` aggregates `e2e`: waiving `e2e` left `deploy-gate` red and
  unwaivable;
- the existing judgement code runs on a wait result that is still pending, and
  even after GitHub auto-merge has already merged the PR.

This Feature changes only `wb pr land` (and therefore `wb pr create --land`,
which drives it). It adds no new verb.

## Behavior

All REQs extend `verifyWaivedChecks` and its call site in
`internal/orchestrate/pr_land.go`; check reads go through the existing
`githubchecks` inspector. Extra GitHub reads (baseline search, workflow file)
happen only on the waiver or refusal path, never on a landing without
`--waive-*` flags.

### REQ: waiver-judged-only-on-terminal-wait

Waiver verification MUST run only when the wait result is terminal.

1. When a non-waived check failed, the result is the existing
   `checks_failed` refusal for that check, with no waiver verdict recorded,
   whatever state the waived checks are in.
2. When the wait is still pending and every check still running is waived,
   the result is `Outcome: findings`, `RefusalCode: waived-check-pending`, the
   reason names each running waived check, and the sanctioned command is the
   existing resume command. No waiver verdict is recorded.
3. When the wait is pending for any non-waived check, the existing
   `checks_pending` result is unchanged.
4. When GitHub auto-merge already merged the PR, waivers are not judged;
   the receipt records `waivers: moot (merged by GitHub)` and the landing
   succeeds as it does today.

### REQ: waiver-baseline-when-tip-has-no-terminal-run

The target tip's run stays the authority whenever it is terminal: a terminal
green run on the tip still refuses `waived-check-green-on-target`, whatever
baseline is passed. Only when the tip has no run of the check, or its run is
not terminal, MAY the waiver be judged against a baseline:

1. `--waive-baseline <sha>` names it. WB MUST verify by walking commit
   parents (`reviewCommitParents`) that the baseline is a first-parent
   ancestor of the tip at most 20 hops back, refuse when any commit on that
   first-parent path (tip excluded, baseline included) has a terminal green
   run of the check, and require the baseline's run to be terminal with the
   PR head's failing conclusion.
2. Without `--waive-baseline`, the refusal is `waived-check-absent-on-target`
   (no run) or `waived-check-hung-on-target` (run not terminal), and WB
   searches the same 20-hop first-parent path for the nearest commit that
   satisfies (1). When found, the sanctioned command is the original command
   plus `--waive-baseline <sha>`; when not, the reason says "no baseline
   within 20 first-parent commits".
3. The receipt's waived-check entry records `target_sha`, `baseline_sha`,
   and `baseline_is_tip`.

### REQ: waiver-aggregator-from-needs

`--waive-aggregate <check>` (repeatable) MUST waive a failed or cancelled
aggregator check only when:

1. it is a GitHub Actions job whose workflow file — the `path` of its own
   workflow run, read at that run's own `head_sha` as GitHub reports it —
   declares `needs:` for exactly one
   job matching the check; its inputs are exactly those jobs. A job's check
   name is its literal `name:` (or its job id when `name:` is absent or holds
   an expression); a matrix job's legs are the checks named `<name> (...)`.
   An aggregator or input that is a reusable-workflow call (`uses:`) is not
   provable and refuses;
2. every input either passed (or was skipped) on the PR head, or is waived in
   the same invocation under the other REQs;
3. at least one input is waived;
4. neither the aggregator nor any input is required (REQ
   waiver-never-waives-required).

Otherwise it refuses `waived-aggregate-unproven`, naming the first unmet
condition. The receipt records the aggregator, its inputs as read, the
workflow path and the head SHA, and `waived_by: aggregation`.

### REQ: waiver-never-waives-required

No waiver flag, baseline or aggregation MAY waive a check that the target's
server-enforced policy requires. The existing `waived-check-required` refusal
applies to each waived check, each aggregator, and each aggregator input.

### REQ: waiver-on-unfenced-target

On a target with no server-enforced required checks, waivers MUST be
evaluable when `--allow-unfenced` is passed, under the same judgement as a
fenced target. Because GitHub enforces nothing there, WB MUST NOT arm
auto-merge for a landing that carries any `--waive-*` flag on an unfenced
target; it merges itself only after the waiver verdict. Without
`--allow-unfenced` the existing `waived-check-target-unfenced` refusal
stands.

## Acceptance Criteria

### AC: running-waived-check-is-pending-not-refused

**Requirements:** pr-land-waivers#req:waiver-judged-only-on-terminal-wait

**Given** a PR whose only non-green check is waived `deploy-gate`, still in
progress when the wait slice ends
**When** `wb pr land <pr> --waive-check deploy-gate --waive-reason r` runs
**Then** it exits `1` with `refusal_code: waived-check-pending` naming
`deploy-gate`, a resume command, and no `waived_checks` entry.

### AC: real-failure-beats-pending-waiver

**Requirements:** pr-land-waivers#req:waiver-judged-only-on-terminal-wait

**Given** `test` (not waived) failed and waived `deploy-gate` is still running
**When** `wb pr land` runs with `--waive-check deploy-gate`
**Then** the refusal is `checks_failed` naming `test`, and no waiver verdict is
recorded.

### AC: merged-by-github-makes-waivers-moot

**Requirements:** pr-land-waivers#req:waiver-judged-only-on-terminal-wait

**Given** auto-merge merged the PR during the wait
**When** `wb pr land` with `--waive-check x` observes the merge
**Then** it exits `0` and the receipt records the waivers as moot.

### AC: green-tip-still-refuses-with-baseline

**Requirements:** pr-land-waivers#req:waiver-baseline-when-tip-has-no-terminal-run

**Given** the waived check passed on the target tip and failed on its parent
**When** `wb pr land` runs with `--waive-baseline <parent>`
**Then** it refuses `waived-check-green-on-target`.

### AC: absent-tip-run-suggests-and-accepts-baseline

**Requirements:** pr-land-waivers#req:waiver-baseline-when-tip-has-no-terminal-run

**Given** a waived check failed on the PR head, has no run on the target tip,
and failed identically on the tip's first parent
**When** `wb pr land` runs without `--waive-baseline`
**Then** it refuses `waived-check-absent-on-target` with a sanctioned command
carrying `--waive-baseline <parent-sha>`;
**and when** that command runs, the landing proceeds and the receipt records
`baseline_is_tip: false`.

### AC: hung-tip-run-is-its-own-refusal

**Requirements:** pr-land-waivers#req:waiver-baseline-when-tip-has-no-terminal-run

**Given** the waived check is `in_progress` on the target tip and no
first-parent commit within 20 hops has a matching terminal run
**When** `wb pr land` runs
**Then** it refuses `waived-check-hung-on-target` and the reason says no
baseline was found within 20 first-parent commits.

### AC: baseline-off-path-or-past-green-is-refused

**Requirements:** pr-land-waivers#req:waiver-baseline-when-tip-has-no-terminal-run

**Given** `--waive-baseline` naming (a) a commit reachable only through a
second parent, (b) a first-parent ancestor 21 hops back, or (c) a first-parent
ancestor older than a commit with a green run of the check
**When** `wb pr land` runs
**Then** each case refuses, naming the baseline and the reason, and nothing
merges.

### AC: aggregator-waived-from-needs

**Requirements:** pr-land-waivers#req:waiver-aggregator-from-needs

**Given** workflow job `deploy-gate` with `needs: [build, e2e]`, `build`
passed, `e2e` failed as on the target tip, and `deploy-gate` failed
**When** `wb pr land` runs with `--waive-check e2e --waive-aggregate
deploy-gate`
**Then** it proceeds and the receipt records `deploy-gate` waived by
aggregation of `[build, e2e]`;
**and given** `build` also failed and is not waived, it refuses
`waived-aggregate-unproven`.

### AC: required-aggregator-or-input-is-never-waived

**Requirements:** pr-land-waivers#req:waiver-never-waives-required, pr-land-waivers#req:waiver-aggregator-from-needs

**Given** the aggregator case above
**When** target policy requires `deploy-gate`, or separately requires `e2e`
**Then** each refuses `waived-check-required`.

### AC: unfenced-target-waives-without-arming

**Requirements:** pr-land-waivers#req:waiver-on-unfenced-target

**Given** a target with no server-enforced required checks and a waivable
failing `lint`
**When** `wb pr land --allow-unfenced --waive-check lint --waive-reason r`
runs
**Then** auto-merge is not armed, the waiver is judged, and WB merges after
the verdict;
**and without** `--allow-unfenced` it refuses `waived-check-target-unfenced`.

## Open Questions

- Should a non-required check hung on the PR head itself be waivable after a
  bounded settle window? Not in this Feature.

---
*This document follows the https://specscore.md/feature-specification*
