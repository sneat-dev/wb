---
format: https://specscore.md/feature-specification
status: Draft
---

# Feature: Expert Tool Routing

> [SpecScore.**Studio**](https://specscore.studio): | [Explore](https://specscore.studio/app/github.com/sneat-dev/wb/spec/features/expert-tool-routing?op=explore) | [Edit](https://specscore.studio/app/github.com/sneat-dev/wb/spec/features/expert-tool-routing?op=edit) | [Ask question](https://specscore.studio/app/github.com/sneat-dev/wb/spec/features/expert-tool-routing?op=ask) | [Request change](https://specscore.studio/app/github.com/sneat-dev/wb/spec/features/expert-tool-routing?op=request-change) |
**Status:** Draft
**Source Ideas:** —
**Related Ideas:** [efficient-agent-pipeline](../../ideas/efficient-agent-pipeline.md) (Draft; not promoted by this Feature)
**Depends On:** [Code Index Freshness](../code-index-freshness/README.md), [Machine Setup](../machine-setup/README.md)

## Summary

Agents use the cheapest tool for the job because the message arrives from
several angles: a one-line principle, a job-to-tool router shipped as data,
trigger-phrase skill descriptions, a point-of-use nudge, a `Tools:` block when
work starts, WB verbs that call the expert tool themselves, and an efficient
path that is always present. Adoption is measured. WB owns the router data
and the mechanism; the normative rule lives in a rules repository outside wb.

## Problem

The founder: "we have tools that are efficient to do specific tasks — experts
in a niche. You should use the most efficient tool for the job at hand."
The 2026-09-18 SDLC logging-gap analysis measured, over one week:

| Signal | Value |
|---|---|
| codegrapher analysis calls | 0 |
| symbol-like `grep`/`rg` calls | 3,015, in 166 of 188 transcripts |
| grep followed within 3 calls by a `.go` Read | 630 |
| `.go` Reads with no `limit` | 867 of 1,947 (45%) |
| heavy commands governed by `wb run` | 7.7% of `go test`, 1.2% of `golangci-lint` |

A symbol lookup on wb by `grep` plus a whole-file Read costs about 20,230
tokens; `codegrapher callers` about 120. A rule index read is 24.5 KB;
`specscore rule show` 1.2 KB. Each avoided turn saves about 0.21–0.26M
context tokens (estimate). The causes are independent — no instruction named
codegrapher; `specscore rule info` prints help and exits `0`
(specscore-cli #215); weak skill triggers (codegrapher #48); no index where
agents work; `codegrapher node` fails in wb (codegrapher #47); an
unregistered guard — so one instruction line fixes none of the others.

## Behavior

### REQ: principle-pointer

`ai/skills/wb/SKILL.md` MUST state in one line "each niche has an expert
tool; use the cheapest tool that answers the job", linking to the router
reference. No wb skill or doc may link to a non-public repository.

### REQ: router-table

WB MUST ship the router as data (`ai/skills/wb/references/expert-tools.yaml`),
rendered into `ai/skills/wb/references/expert-tools.md` with the cost table
above, and searchable through `wb commands --search <intent> --format json`
as `routes[]` (`{job, tool, command, fallback, executor}`). `executor` names
the lifecycle executor whose freshness gates the row. Minimum rows:

| Job | Tool | Fallback |
|---|---|---|
| where is symbol X defined | `codegrapher query X` | `codegrapher query X -p <canonical clone>`, then `grep` and a ranged Read |
| who calls X / find usages | `codegrapher callers X` | the same with `-p <canonical clone>`, then `grep -rn` |
| blast radius of changing X | `codegrapher impact X` | `-p <canonical clone>`; else say so |
| which tests cover these files | `codegrapher affected <files>` | the package's tests |
| read a rule's recipe | `specscore rule show <slug>` | the rule's own README |
| which rules apply to a path | `specscore rule list --applies-to <path>` | none |
| wait for CI or a PR | `wb wait checks` / `wb wait pr` | none; never a sleep loop |
| run tests, build, lint | `wb run -- <cmd>` / `wb check` | none |
| land an open PR | `wb pr land` | none |
| find the wb verb for an intent | `wb commands --search` | `wb help` |

The `-p <canonical clone>` fallback answers for the base branch, not the
worktree's edits. A row whose tool is not installed is marked unavailable in
JSON, never omitted. `wb commands --routes --format markdown` prints the table
alone, for pasting into briefs (`markdown` is a new `wb commands` format).

### REQ: trigger-phrase-descriptions

Every wb skill description MUST contain every trigger phrase that
`expert-tools.yaml` lists for it, phrased as agents' intents (for `wb-merge`:
"merge", "land", "finish", "ship"; for `wb-run`: "run tests", "go test",
"build"). A wb unit test MUST fail when a description lacks any listed phrase.
Other tools' skills are tracked by issue (codegrapher #48).

### REQ: point-of-use-nudge

`wb hooks agent pre-tool-use` MUST support an expert-tool nudge behind
`agent_guard.nudges.expert_tools: off|on` in the trusted user `wb.yaml`,
default `off` (open decision 7). The key lives in `wb.yaml`, whose top level
is not strictly decoded, rather than in the strictly decoded hooks policy's
`agent:` section, so an older wb ignores it. It changes the guard's matcher
(on adds `Grep`), so it takes effect through `wb hooks agent install`, and
machine-setup's `agent-guard` item reports drift when they disagree. For a Bash `grep`/`rg`, or a native `Grep` call, whose
pattern is identifier-shaped, in a repository where a routed row's executor
is `fresh` in the checkout or in its canonical clone, the guard MUST emit only
`hookSpecificOutput.additionalContext` naming the row (with `-p <canonical
clone>` when only the canonical index is fresh) and MUST NOT emit
`permissionDecision`, so the user's normal permission prompt is unchanged.
At most one nudge per harness session, repository and row; it never blocks,
fails open with empty output on any internal error, makes no network call,
and stays within the guard's latency budget. Each nudge appends a
`nudge` event `{id, ts, harness_session_id, agent_id, repository, row}` to
`~/.local/state/wb/agent-events.jsonl`. When a later call the guard observes
in the same session invokes the row's tool within the next 3 observed calls,
it appends a separate `nudge-followed` event `{nudge_id, ts}`.

### REQ: create-output-names-tools

`wb create` and `wb worktree create` MUST end text output with a `Tools:`
block of at most 5 lines naming the rows usable in the new checkout and their
state (for example `codegrapher: canonical index fresh at <sha>; use -p
<canonical clone>`, or `codegrapher: no index; grep`), and expose the same as
`tools[]` in `--format json`.

### REQ: verbs-use-experts-internally

`wb check` gains `--changed` for one repository with any profile (`fast`,
`full`, `ci`); with `--fleet` it is a usage error. Changed paths are the
union of working-tree changes (staged, unstaged, untracked) and commits since
the merge-base with `origin/<base>`, where `<base>` is the worktree's
recorded base branch, else the default branch. Go test packages MAY be
narrowed by `check.test_selection` in the trusted user `wb.yaml`
(`{run, args, timeout, gated_by: <lifecycle executor>}`), written by
machine-setup's `selector:<cli>` item. It lives outside `hooks:`, so it is
not a lifecycle executor and not versioned with it; `run` passes the trust
checks of trusted-repository-update-hooks#req:generic-executors. Contract:
stdin, changed paths one per line; stdout, a JSON array of Go package
patterns; exit `0`. WB MUST fall back to full scope, stating why, when the
`gated_by` executor is not `fresh` in this checkout, or the selector fails,
times out or returns nothing. JSON output and `check.yaml` (under
`--report-dir`) record `selection: graph|full` and the reason. CI keeps full
scope.

### REQ: efficient-path-is-easy

The efficient path MUST be present before it is needed: `wb setup --check`
reports a missing executor binding, missing skills, a missing selector or an
unregistered guard as drift, and `wb fleet status` reports a stale or failed
index. Every router command has a bounded machine-readable form. Known tool
defects on the path (codegrapher #47, specscore-cli #215) block Stable.

### REQ: adoption-measured

The SDLC metrics extractor (`tools/sdlc-metrics`, #640) MUST add an
`adoption` section per session, role and model: routed-tool calls by
subcommand, symbol greps, grep-to-Read chains within 3 calls, whole-file Read
bytes, reads of rule and lesson documents, `nudge` and `nudge-followed`
counts, and `selection: graph|full` counts. When a week's ratio of symbol
greps to routed calls exceeds a configured threshold, the report MUST name a
configured rule id (default `rule:use-the-expert-tool`) and the per-row
evidence. It records counts only, never transcript text.

## Rules-repository follow-ups (not wb requirements)

- Create `rule:use-the-expert-tool` (principle and cost table), tier Stated,
  measured by this Feature's extractor.
- Add a "Tools" section to the brief template and pre-dispatch checklist,
  filled from `wb commands --routes --format markdown`.
- Replace `specscore rule info` with `specscore rule show` in agent
  instructions; add a codegrapher trigger line.

## Acceptance Criteria

### AC: principle-pointer-present

**Requirements:** expert-tool-routing#req:principle-pointer

**Given** the wb repository
**When** `go test ./cmd/wb/...` runs
**Then** a test asserts that `ai/skills/wb/SKILL.md` contains the principle
line linking to `references/expert-tools.md`, and that no file under
`ai/skills/` links to a repository outside an allow-list of public ones.

### AC: router-searchable

**Requirements:** expert-tool-routing#req:router-table

**Given** codegrapher installed and specscore absent
**When** the agent runs `wb commands --search "who calls" --format json`,
then `--search "rule recipe"`, then `wb commands --routes --format markdown`
**Then** each exits `0`; the first returns the `codegrapher callers X` row
with its `-p <canonical clone>` fallback; the second returns the specscore
row marked unavailable; the third prints only the router table.

### AC: skill-trigger-guard

**Requirements:** expert-tool-routing#req:trigger-phrase-descriptions

**Given** the `wb-merge` description with "land" removed
**When** `go test ./cmd/wb/...` runs
**Then** a test fails naming `wb-merge` and "land".

### AC: nudge-off-by-default

**Requirements:** expert-tool-routing#req:point-of-use-nudge

**Given** no `agent_guard.nudges` key and a fresh index
**When** the guard receives a Bash payload `grep -rn githubActionsRunAndJob .`
**Then** its output is byte-identical to the output without this Feature and
no agent event is written.

### AC: nudge-is-context-only-and-once

**Requirements:** expert-tool-routing#req:point-of-use-nudge

**Given** nudges on and the canonical clone's index `fresh`, in a worktree
with no index of its own
**When** the guard receives the grep payload twice in one session, once in a
second session, once as a native `Grep` call in a third session, and once
malformed
**Then** the first, third and fourth calls' output has `additionalContext`
naming `codegrapher callers githubActionsRunAndJob -p <canonical clone>` and
has no `permissionDecision` field; the second call's output is empty; the
malformed payload yields empty output and exit `0`; `agent-events.jsonl`
gains exactly three `nudge` rows.

### AC: no-nudge-without-fresh-index

**Requirements:** expert-tool-routing#req:point-of-use-nudge

**Given** nudges on and the executor `stale` in both checkout and canonical
**When** the guard receives the grep payload
**Then** its output is empty.

### AC: followed-is-recorded

**Requirements:** expert-tool-routing#req:point-of-use-nudge, expert-tool-routing#req:adoption-measured

**Given** a nudge for "who calls X"
**When** the second guard-observed call after it in that session is the Bash
command `codegrapher callers X -p <canonical clone>`
**Then** one `nudge-followed` row references the nudge id, and the
extractor's adoption section counts one nudge and one followed.

### AC: create-names-tools

**Requirements:** expert-tool-routing#req:create-output-names-tools

**Given** a repository whose canonical index is fresh and no worktree binding
**When** the agent runs `wb create t2 owner/repo --format json`, then without
`--format`
**Then** `tools[]` names codegrapher with state "canonical index fresh" and
the `-p` path; the text output ends with a `Tools:` block of at most 5 lines.

### AC: graph-selection-falls-back

**Requirements:** expert-tool-routing#req:verbs-use-experts-internally

**Given** a `test_selection` executor and a change to
`internal/orchestrate/ciwait.go`
**When** the agent runs `wb check --profile fast --changed --format json`
with the `gated_by` executor `fresh`, then `stale`, then adds `--fleet`
**Then** the first output records `selection: graph` testing only
`./internal/orchestrate`; the second records `selection: full` with reason
`index-stale`; each exits with the tests' own result; the third exits `2`.

### AC: efficient-path-drift-reported

**Requirements:** expert-tool-routing#req:efficient-path-is-easy

**Given** a converged machine whose guard registration was removed
**When** the user runs `wb setup --check --format json`
**Then** `agent-guard:claude` reports `drift` and the command exits `1`.

### AC: adoption-names-the-rule

**Requirements:** expert-tool-routing#req:adoption-measured

**Given** an extractor window with 3,015 symbol greps, 0 codegrapher calls
and a 10:1 threshold
**When** the extractor runs
**Then** the adoption section names `rule:use-the-expert-tool` with per-row
counts and contains no transcript text.

## Delivery Slices

Each slice is one PR and ships with the ACs named.

1. Router data and docs, the principle pointer, `routes[]` and `--routes`,
   the trigger-phrase test — principle-pointer-present, router-searchable,
   skill-trigger-guard.
2. After code-index-freshness slice 1 — create-names-tools.
3. After #640 lands — adoption-names-the-rule.
4. After decision 7 — nudge-off-by-default, nudge-is-context-only-and-once,
   no-nudge-without-fresh-index, followed-is-recorded.
5. `wb check --changed` — graph-selection-falls-back.
6. efficient-path-drift-reported ships with machine-setup slice 1.

## Open Questions

- **Nudges (open decision 7).** Recommendation: allow them once the
  canonical-index fallback ships, so a nudge never points at a missing index.
- The 10:1 adoption threshold is a placeholder; should `--changed` selection
  run in CI once its recall is proven?

---
*This document follows the https://specscore.md/feature-specification*
