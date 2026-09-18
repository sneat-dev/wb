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

Agents reach for the cheapest tool that answers the job at hand because the
same message reaches them from several angles: a principle with numbers, a
job-to-tool router, tool descriptions written in the words agents think in,
a nudge at the moment of use, a Tools section in every brief, WB verbs that
call the expert tool themselves, and an efficient path that is always
present and fast. Adoption is measured, and the measurement feeds lessons and
rules. WB owns the mechanism; the rule text lives in `sneat-co/backstage` as
`rule:use-the-expert-tool`; SpecScore owns what the rule means.

## Problem

The founder: "we have tools that are efficient to do specific tasks — experts
in a niche. You should use the most efficient tool for the job at hand."
Measured in the week to 2026-09-18 (REPORT.md §9a, §9b):

| Signal | Value |
|---|---|
| codegrapher analysis calls | 0 |
| symbol-like `grep`/`rg` calls | 3,015 in 166 of 188 transcripts |
| grep followed within 3 calls by a `.go` Read | 630 (617 by Sonnet) |
| `.go` Reads with no `limit` | 867 of 1,947 (45%) |
| heavy commands governed by `wb run` | 7.7% `go test`, 1.2% `golangci-lint` |

The cost of one symbol lookup on wb (`githubActionsRunAndJob`): `grep` plus a
whole-file Read is about 20,230 tokens; `codegrapher callers` about 120;
`codegrapher query` about 50. A rule lookup through
`cat spec/rules/README.md` is 24.5 KB; `specscore rule show` is 1.2 KB. Each
avoided turn saves about 0.21–0.26M context tokens (*est.*).

The causes are several and independent: no instruction names codegrapher; the
prescribed `specscore rule info` silently prints help and exits `0`
(specscore-cli #215); the codegrapher skill description lacks the trigger
phrases (codegrapher #48); no index exists where agents work; `codegrapher
node` crashes in wb (codegrapher #47); the wb guard is not registered. One
instruction line fixes none of the others, so the message must arrive from
every angle.

## Ownership

| Part | Owner | Artifact |
|---|---|---|
| Rule text, trigger, enforcement tier | `sneat-co/backstage` | `rule:use-the-expert-tool`, plus the pointer line in the agent-instructions index |
| Meaning of rules, lessons, their ladder | SpecScore | rule and lesson specifications |
| Router data, guard nudge, verbs using expert tools, setup, adoption events | `sneat-dev/wb` | this Feature |
| Tool self-descriptions | each tool's repository | codegrapher #48, specscore skill descriptions, wb's own skills |

## Behavior

### REQ: principle-with-numbers

The wb entry skill (`ai/skills/wb/SKILL.md`) MUST open its routing guidance
with the principle — "each niche has an expert tool; use the cheapest tool
that answers the job" — and the measured cost table above, citing its source
and date. The same text MUST be the body of `rule:use-the-expert-tool` in
backstage; wb MUST link to the rule rather than restate a second version.

### REQ: router-table

WB MUST ship one router table as data (`ai/skills/wb/references/expert-tools.yaml`)
rendered into the skill reference and searchable through
`wb commands --search <intent> --format json` as `routes[]`
(`{job, tool, command, fallback}`). Minimum rows:

| Job | Tool | Fallback when unavailable |
|---|---|---|
| where is symbol X defined | `codegrapher query X` | `grep` then a ranged Read |
| who calls X / find usages | `codegrapher callers X` | `grep -rn` |
| blast radius of changing X | `codegrapher impact X` | none; say so |
| which tests cover these files | `codegrapher affected <files>` | the package's tests |
| read a rule's recipe | `specscore rule show <slug>` | `cat spec/rules/<slug>/README.md` |
| which rules apply to a path | `specscore rule list --applies-to <path>` | none |
| wait for CI or a PR | `wb wait checks` / `wb wait pr` | none; never a sleep loop |
| run tests, build, lint | `wb run -- <cmd>` / `wb check` | none |
| land an open PR | `wb pr land` | none |
| find the wb verb for an intent | `wb commands --search` | `wb help` |

A row whose tool is not installed on the machine MUST be marked unavailable in
the JSON output, never omitted.

### REQ: trigger-phrase-descriptions

Every wb skill description MUST contain the intents agents phrase, not the
mechanism: for example `wb-merge` names "merge", "land", "finish", "ship";
`wb-run` names "run tests", "go test", "build". WB tracks the equivalent
change in other tools' skills by issue (codegrapher #48), not by editing them.
A wb unit test MUST fail when a skill description lacks every trigger phrase
listed for it in `expert-tools.yaml`.

### REQ: point-of-use-nudge

`wb hooks agent pre-tool-use` MUST support an expert-tool nudge behind
`agent_guard.nudges.expert_tools: off|on` in the trusted user `wb.yaml`,
default `off` until founder decision 7. When on, for a Bash call whose
command is a `grep`/`rg` for an identifier-shaped pattern, in a checkout
whose code-index freshness is `fresh`, the guard MUST allow the call
unchanged and attach `additionalContext` naming the router row (for example
"`codegrapher callers <sym>` answers this in one call, ~120 tokens"). It MUST
nudge at most once per harness session per repository per row, never block,
fail open on any internal error, add no network call, and stay within the
guard's existing latency budget. Each nudge MUST append
`{ts, harness_session_id, agent_id, repository, row, followed}` to
`~/.local/state/wb/agent-events.jsonl`, where `followed` is set when a call
to the routed tool happens within the next 3 tool calls of that session.

### REQ: create-output-names-tools

`wb create` (and `wb worktree create`) MUST end its text output with a
`Tools:` block of at most 5 lines naming the router rows usable in the new
checkout right now, with their state (for example
`codegrapher: index fresh at <sha>` or `codegrapher: no index; grep`), and
MUST expose the same as `tools[]` in `--format json`. A lane reads this at
the start of every task, whatever its brief says.

### REQ: brief-tools-section

The brief template and `rule:walk-brief-rules-before-dispatch` checklist in
backstage MUST gain a "Tools" section that names the router rows relevant to
the brief's work. WB's part is a copyable block:
`wb commands --routes --format markdown` prints the router table for pasting.

### REQ: verbs-use-experts-internally

`wb check --profile gates --changed` MAY narrow Go test packages through a
trusted user-configured `test_selection` executor (stdin: changed paths;
stdout: JSON package list), which [Machine Setup](../machine-setup/README.md)
binds to `codegrapher affected`. WB MUST fall back to full package scope, and
say why, when the checkout's freshness is not `fresh`, the executor fails or
times out, or it returns nothing. The receipt MUST record
`selection: graph|full` and the reason. CI keeps full scope; selection is a
local speed-up only.

### REQ: efficient-path-is-easy

The efficient path MUST be present before an agent needs it: `wb setup
--check` reports a missing index binding, missing skills, or an unregistered
guard as drift; `wb fleet status` reports stale indexes. Compact output is
part of the path: every router command above MUST have a bounded
machine-readable form. Known tool defects on the path (codegrapher #47,
specscore-cli #215) are tracked as blockers of this Feature's Stable status.

### REQ: adoption-measured

The SDLC metrics extractor (`tools/sdlc-metrics`, wb #640) MUST add an
`adoption` section per session, role and model: routed-tool calls by
subcommand, symbol-grep count, grep-to-Read chains within 3 calls, whole-file
Read bytes, `cat` of `spec/rules|lessons/**`, nudges shown and followed, and
`selection: graph|full` counts. When a week's symbol-grep to routed-call ratio
exceeds a configured threshold, the report MUST name `rule:use-the-expert-tool`
with its current enforcement tier and the per-row evidence, as the input to a
backstage lesson or a tier change. WB records counts only; the decision to
promote belongs to backstage.

## Acceptance Criteria

### AC: router-searchable

**Requirements:** expert-tool-routing#req:router-table

**Given** a machine with codegrapher installed and specscore absent
**When** the agent runs `wb commands --search "who calls" --format json`
**Then** it exits `0` and `routes[]` includes
`{"job":"who calls X / find usages","tool":"codegrapher","command":"codegrapher callers X"}`;
searching "rule recipe" returns the specscore row marked unavailable.

### AC: skill-trigger-guard

**Requirements:** expert-tool-routing#req:trigger-phrase-descriptions

**Given** the `wb-merge` skill description with "land" removed
**When** `go test ./...` runs in the wb repository
**Then** a test fails naming the skill and the missing phrase.

### AC: nudge-off-by-default

**Requirements:** expert-tool-routing#req:point-of-use-nudge

**Given** no `agent_guard.nudges` key and a fresh index
**When** the guard receives a Bash payload `grep -rn githubActionsRunAndJob .`
**Then** its output is byte-identical to the output without this Feature, and
no agent event is written.

### AC: nudge-once-and-never-blocks

**Requirements:** expert-tool-routing#req:point-of-use-nudge

**Given** `agent_guard.nudges.expert_tools: on` and a `fresh` index
**When** the guard receives the same grep payload twice in one session, then
once in another session, then once with a malformed payload
**Then** the first call returns `permissionDecision: "allow"` with
`additionalContext` naming `codegrapher callers`; the second returns allow with
no context; the other session is nudged again; the malformed payload is
allowed unchanged; and `agent-events.jsonl` holds exactly two nudge rows.

### AC: no-nudge-without-fresh-index

**Requirements:** expert-tool-routing#req:point-of-use-nudge

**Given** nudges on and the repository's code-index freshness `stale`
**When** the guard receives the grep payload
**Then** the call is allowed with no `additionalContext`.

### AC: followed-is-recorded

**Requirements:** expert-tool-routing#req:point-of-use-nudge, expert-tool-routing#req:adoption-measured

**Given** a nudge was shown for row "who calls X"
**When** the session's second following tool call is `codegrapher callers X`
**Then** the nudge row's `followed` is `true`, and the extractor's adoption
section counts one nudge shown and one followed.

### AC: create-names-tools

**Requirements:** expert-tool-routing#req:create-output-names-tools

**Given** a repository whose canonical index is fresh and a worktree binding
that seeds on create
**When** the agent runs `wb create t2 owner/repo --format json`
**Then** `tools[]` lists codegrapher with its index state and at most 5 rows,
and the text form ends with a `Tools:` block of at most 5 lines.

### AC: graph-selection-falls-back

**Requirements:** expert-tool-routing#req:verbs-use-experts-internally

**Given** a `test_selection` executor configured and a change to
`internal/orchestrate/ciwait.go`
**When** the agent runs `wb check --profile gates --changed --format json`
with the index `fresh`, then again with it `stale`
**Then** the first receipt records `selection: graph` with only
`./internal/orchestrate` tested; the second records `selection: full` with
reason `index-stale`; both exit with the tests' own result.

### AC: routes-printable-for-briefs

**Requirements:** expert-tool-routing#req:brief-tools-section

**Given** an installed wb
**When** the user runs `wb commands --routes --format markdown`
**Then** it exits `0` and prints the router table with one row per job and no
other content.

### AC: adoption-names-the-rule

**Requirements:** expert-tool-routing#req:adoption-measured

**Given** an extractor window with 3,015 symbol greps and 0 codegrapher calls,
and a threshold of 10:1
**When** the extractor runs
**Then** the report's adoption section names `rule:use-the-expert-tool`, its
tier, and the per-row counts, and contains no transcript text.

## Non-goals

- Refusing or rewriting a grep. Nudges inform; the agent decides.
- Choosing between rival tools in one niche; the router names one per job.

## Open Questions

- **Nudges (founder decision 7, REPORT.md §8).** May the guard add the nudge,
  once per session per repository, never blocking? Recommendation: yes, after
  code-index freshness ships, so a nudge never points at a missing index.
- **`rule:use-the-expert-tool` tier.** Start as Stated with the extractor as
  its measurement, and move to Enforced only if nudges stay on and the ratio
  improves?
- **Adoption threshold.** 10:1 greps to routed calls is a placeholder; set it
  after the first measured week with an index present.
- Should `wb check` graph selection, once recall is proven, also run in CI
  for pull requests, as the graph-assisted idea proposes?

---
*This document follows the https://specscore.md/feature-specification*
