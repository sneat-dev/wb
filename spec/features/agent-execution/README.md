---
format: https://specscore.md/feature-specification
status: Draft
---

# Feature: Agent Execution — task types, profiles, harnesses, telemetry, experiments

> [SpecScore.**Studio**](https://specscore.studio): | [Explore](https://specscore.studio/app/github.com/sneat-dev/wb/spec/features/agent-execution?op=explore) | [Edit](https://specscore.studio/app/github.com/sneat-dev/wb/spec/features/agent-execution?op=edit) | [Ask question](https://specscore.studio/app/github.com/sneat-dev/wb/spec/features/agent-execution?op=ask) | [Request change](https://specscore.studio/app/github.com/sneat-dev/wb/spec/features/agent-execution?op=request-change) |
**Status:** Draft
**Source Ideas:** —
**Depends On:** [Agent Dispatch](../agent-dispatch/README.md) (reuses its dispatch engine, run owner, worktree service, remote protocol and run store), [Worktree Lifecycle](../worktree-lifecycle/README.md)

**Revision:** 2 — incorporates the Gate A review of 2026-10-24 (Opus 5.5, high effort).

## Summary

This feature turns the single-harness `wb agent dispatch` into a **deterministic
execution layer for coding agents**: a caller names a *task type* and an optional
*complexity* and *effort*, WB resolves an *execution profile* from `wb.yaml`,
launches one of three harness adapters (**Codex**, **OpenCode**, **`dsh`**) inside
a properly isolated worktree, and records a durable, schema-versioned execution
record with honest telemetry, objective validation results, and an independent
review/acceptance status.

`wb agent compare` runs the same task across several profiles, each in its own
worktree cut from **one pinned base commit**, repeats each profile on request, and
reports comparable facts. `run` and `compare` share one dispatch engine, so an
experiment cannot drift into a second implementation.

The agent skill `wb-agent` teaches an orchestrator (Claude Code, Codex) when to
delegate through WB rather than execute directly, how to launch and monitor
asynchronous workers, how to run a comparison, and how to verify a result before
accepting it.

## Problem

`wb agent dispatch` requires a profile name and one harness (`codex`). It has no
task-type vocabulary, no complexity/effort routing, no OpenCode or `dsh` adapter,
no objective validation, no review/acceptance status, and no way to compare two
execution configurations on identical input. A supervisor therefore hardcodes a
model, cannot answer "which configuration solves this task at what cost", and has
to reconstruct execution facts from a transcript.

## Behavior

### Layers

1. **Task** — the bounded request text plus its type, complexity and optional
   acceptance criteria. Immutable for the life of an execution; only its SHA-256
   and a one-line summary are rendered.
2. **Profile** — a reusable `harness + provider + model + effort + timeout`
   configuration, independent of any task type.
3. **Task type** — a named policy: default profile, per-complexity profile map,
   default worktree mode, default timeout, validation suite, acceptance policy.
4. **Execution** — one attempt by one resolved profile against one task in one
   worktree. Durable ID `agt-…`; the legacy five-value `State` vocabulary is
   unchanged and a richer `execution_status` is added beside it.
5. **Experiment** — one task, one pinned base SHA, N profiles × R repeats, each
   attempt an independent execution.
6. **Evaluation** — three independent axes: execution status (did the process
   run), validation status (did the configured objective checks pass), review
   status (did a human or orchestrator accept it). None is derived from another.

#### REQ:layers
Task, profile, task type, execution, experiment and evaluation are separate
concepts with separate durable identity; none is inferred from another.

### Configuration (extends the existing `agents` section of `wb.yaml`)

```yaml
agents:
  providers:            # unchanged registry; user entries override field by field
    deepseek: { base_url: https://api.deepseek.com, credential_env: DEEPSEEK_API_KEY, wire_api: responses }
  profiles:
    opencode-flash:
      harness: opencode                       # codex | opencode | dsh
      model: opencode-go/deepseek-v4.1-flash  # harness-native id, passed through
      variant: medium                         # opencode only; appended as #variant in argv
      effort_variants: { low: cheap, high: heavy }   # opencode effort mapping
      timeout: 45m
    dsh-flash:
      harness: dsh
      model: deepseek-v4-flash                # dsh native id; provider is dsh's default
    cheap-codex:
      harness: codex
      provider: deepseek
      model: deepseek-v4.1-flash
      effort: medium                          # legacy key `reasoning` also accepted
  task_types:
    coding:
      profile: opencode-flash
      complexity: { trivial: cheap-codex, high: dsh-flash }
      worktree: auto
      timeout: 60m
      validate: [go]
      acceptance: required
    review:
      profile: opencode-flash
      worktree: current
      validate: []
      acceptance: required
  validations:
    go:
      - { name: vet,  argv: [go, vet, ./...],   timeout: 5m  }
      - { name: test, argv: [go, test, ./...],  timeout: 20m }
  defaults:
    task_type: coding
  fallback: [cheap-codex]                     # ordered; preflight unavailability only
```

#### REQ:profile-config
A profile is `harness` + `model` plus optional `provider`, `variant`,
`effort_variants`, `effort`/`reasoning`, and `timeout`. `harness` and `model` are
required; nothing else is. A profile naming both `effort` and `reasoning` is a
configuration error. `model` never contains `#`: the OpenCode variant is a
separate field.

#### REQ:task-type-config
A task type is a named policy: optional `profile`, optional `complexity` map,
optional `effort`, optional `worktree` default (`auto|current`), optional
`timeout`, optional `validate` suite names, and `acceptance`
(`required|optional`). An unknown suite or profile name is a configuration error
reported before any worktree exists.

#### REQ:config-precedence
Resolution is deterministic and total. Highest wins:

| Value | Order |
|---|---|
| profile | `--profile` → `--harness`/`--model` synthesized → `task_types.<t>.complexity.<c>` → `task_types.<t>.profile` → `agents.defaults.profile` → error |
| task type | `--task-type` → `agents.defaults.task_type` → built-in `coding` |
| complexity | `--task-complexity` → task type default (none) |
| effort | `--effort` → `task_types.<t>.effort` → profile `effort`/`reasoning` → none |
| worktree | `--worktree`/`--new-worktree`/`--use-worktree` → `task_types.<t>.worktree` → `auto` |
| timeout | `--timeout` (`Flags().Changed`) → task type `timeout` → profile `timeout` → 30m |
| validation | `--validate`/`--no-validate` → `task_types.<t>.validate` → none |

`--profile` together with `--harness` or `--model` is a usage error. `--profile`
together with a complexity that would have routed elsewhere still uses the
explicit profile and records `complexity_overridden: true`.

#### REQ:provenance
Every resolved value records whether it was explicit, task-type, profile,
defaulted or overridden. `--dry-run` prints all of them.

#### REQ:no-hardcoded-models
WB ships no model identifier. Built-in task types are policy only and name no
default profile; a built-in that resolves to no profile fails closed with the
configured profile list.

#### REQ:complexity-vs-effort
Complexity (`trivial|low|medium|high|extreme`) describes work difficulty and
selects *what* runs. Effort (`low|medium|high|xhigh`) requests *how hard the
selected model thinks*. Both are optional, independent, and recorded separately
with requested and effective values.

#### REQ:effort-capability
Effort is translated by harness capability: Codex passes
`model_reasoning_effort`; OpenCode appends the profile's mapped `#variant`;
`dsh` has no seam. An **explicit** `--effort` a harness cannot honour is a usage
error naming the harness. An **inherited** effort that cannot be honoured is
recorded `effective: null` with a reason and a warning, and does not fail the
run.

#### REQ:ordered-fallback
A task type may name an ordered `fallback` list of profiles, with
`agents.fallback` as the global tail. `fallback_on` selects the triggers:
`preflight` (default; a harness binary or credential WB can prove missing) and
`runtime` (opt-in; the provider failed the run itself). The order is the
operator's cost policy and WB never reorders it. A runtime failover applies only
to `provider`, `credential` and `harness_missing` failures that left no change on
disk, always uses a fresh worktree, never swaps an explicit profile or a shared
checkout, needs a waiting caller (`run --wait`), is logged, and is recorded on
the new execution as `retry_of`, `fallback_from` and `fallback_reason`. A retry
is a new execution, never a resumed one. `compare` never falls back.

#### REQ:dry-run
`--dry-run` prints the fully resolved plan — profile and source, task type,
complexity and provenance, requested/effective effort, worktree mode and target,
timeout, validation suite, fallback decision, harness capabilities, detected
harness version, and the isolation level — and creates no worktree, writes no
record, and starts no process.

### Harness adapters

One `Harness` contract in `internal/agents`: `Argv`, `Environment`,
`Summarize`, `RecentActions`, `Capabilities`, `DetectVersion`, `Preflight`.
Verified CLI facts (2026-10-24, this machine):

| | Codex | OpenCode | `dsh` |
|---|---|---|---|
| Executable | `codex` | `opencode` | `dsh` |
| Model flag | `-m <model>` | `--model <provider/model[#variant]>` | none; `--patch` overlay sets `agent-default-model` |
| Task delivery | stdin (`-`) | stdin | one argv element |
| Events | `--json` JSONL | `--format json` JSONL | none (final message only) |
| Usage | `turn.completed.usage` | summed `step_finish.tokens` | unavailable |
| Cost | unavailable | `step_finish.cost`, harness-estimated USD | unavailable |
| Effort | `model_reasoning_effort` | `#variant` via `effort_variants` | unsupported |
| Non-interactive | `approval_policy=never` | `--auto --standalone` | headless profile |
| Credential | WB-injected env var | harness auth store | harness auth store |
| Error signal | non-zero exit / `turn.failed` | non-zero exit **or** `error` event | non-zero exit |

#### REQ:harness-contract
Exactly three adapters (`codex`, `opencode`, `dsh`) implement one contract;
`profile.Resolve` and the run owner contain no harness-specific flag spelling.

#### REQ:capability-aware-translation
Each adapter builds only the arguments its CLI documents. Unsupported
capabilities are refused or recorded, never silently dropped.

#### REQ:harness-error-normalization
A run is `completed` only when the harness produced its documented terminal
success evidence and no error event. OpenCode requires a terminal `step_finish`,
no `error` event, and no malformed event lines; Codex keeps its existing rule.

#### REQ:opencode-standalone
OpenCode is always launched with `--standalone` and never `--server`, so its
private server lives inside the worker's process group and inside WB's
allowlisted environment. No shared background service is used.

#### REQ:dsh-argv-delivery
`dsh` cannot read the task from stdin and its launcher consumes `--`, so the task
travels as one argv element, capped at 64 KiB, with a leading newline when the
text starts with `-` (proven necessary: `dsh --profile headless -- "-h"` prints
help). The record states that the task is visible in the process table. The
model override travels as a `--patch` overlay written `0600` into the run
directory, and preflight proves the effective model with `--dump-config`.

### CLI surface

```
wb agent run   --task-type coding [--task-complexity high] [--effort medium]
               [--worktree auto|current|<absolute path> | --new-worktree <name> | --use-worktree <name>]
               [--operation <name>] [--profile p] [--harness h --model m]
               [--validate suite|--no-validate] [--timeout d] [--wait] [--dry-run]
               (--task <text> | --task-file <path|->) [--format json]
wb agent profiles     [--format json]
wb agent task-types   [--format json]
wb agent compare      --profiles a,b [--repeat N] [--concurrency N]
                      (--task … | --task-file …) [--format json]
wb agent experiment <experiment-id> [--format json]
wb agent diff <agent-id> [--patch] [--format json]
wb agent review <agent-id> --status accepted|rejected|needs_changes [--note <text>]
wb agent prune --older-than <duration> [--apply] [--format json]
wb agent status|await|list|logs|stop    # unchanged; stop gains the alias cancel
wb agent dispatch                       # unchanged flags, now accepts all three harnesses
```

`--worktree` accepts `auto`, `current`, or an **absolute path**; a bare name is
rejected there and belongs to `--use-worktree`, so a worktree called `auto` can
never be ambiguous. `run` returns as soon as the owner is started, exactly like
`dispatch`; `--wait` blocks through the same awaiter `await` uses. `run` and
`compare` register no `--to` flag: the remote protocol carries the older
dispatch shape and a half-honoured remote mode would be worse than an absent
flag. `status`, `await`, `logs` and `stop` keep `--to`.

#### REQ:remote-scope
`run`, `compare`, `experiment`, `diff`, `review` and `prune` operate on the local
machine only and expose no `--to`. Remote dispatch/status/await/list/logs/stop
keep working unchanged.

### Worktrees and concurrency

`--worktree auto` creates a worktree through the existing `internal/worktrees`
service named `<task-type>-<hash8>-<id8>` (`id8` = first 8 hex characters of the
pre-allocated agent ID), from the configured base branch. `current` uses the
invoking checkout's root. A path must be a Git worktree root. `current` and path
refuse a canonical clone (`.worktree.md` `writable: false`) unless
`--allow-canonical`, and refuse a dirty checkout unless `--allow-dirty`; either
way the pre-run HEAD, a `git stash create` snapshot SHA (which does not touch the
working tree or the index) and the already-dirty paths are recorded, and nothing
is cleaned or reset.

#### REQ:worktree-modes
`auto`, `current` and an explicit path are supported; existing `--new-worktree`
and `--use-worktree` keep working; `run`/`compare` never create a worktree for
`current` or path.

#### REQ:canonical-refusal
A canonical clone is never a writable execution target without an explicit
`--allow-canonical`, and the refusal names the worktree command that would be
safe.

#### REQ:dirty-checkout
A dirty target is either refused or explicitly allowed, never cleaned; the
pre-existing paths and snapshot SHA are recorded so a later diff separates the
worker's change from what was already there.

#### REQ:writer-lease
Every writable execution holds an inherited `flock` on
`<home>/agents/leases/<sha256(realpath)>.lock`, keyed on the symlink-resolved
`git rev-parse --show-toplevel`. The dispatcher takes `LOCK_EX|LOCK_NB`; the
descriptor is passed to the detached owner through `ExtraFiles`, and the owner
passes it on to the harness and to every validation step, so the lock is held
until the last process holding it exits and no PID liveness test is needed. A
second claim fails with the owning agent ID read from the file body. The lock is
held through validation. On platforms where `flock` is a no-op (Windows) writable
`current`/path execution is refused, while `auto`/`--new-worktree` remain
available. A stray background process left behind by a worker keeps the lease
held: that is the safe direction, and `stop` terminates the whole group.

#### REQ:isolated-worktrees
Two writable executions never share a worktree; `auto` and `compare` give each
attempt its own checkout. `compare` always uses `auto`, because several attempts
inside one checkout would violate isolation and would contend for one writer
lease.

#### REQ:same-base
`compare` pins one base SHA: it creates every worktree sequentially **before**
dispatching anything, records the first created `BaseSHA`, and never dispatches
an attempt whose `BaseSHA` differs (recorded as `base_mismatch`). The worktree
service's contract is fetch-`origin/<base>`-then-create, so a pinned *creation*
is not available without changing that service; pinning by verification is used
instead and a moved base produces a reported, unstarted attempt rather than a
silently unfair comparison.

#### REQ:retention
`wb agent prune --older-than <duration>` is dry-run by default, never touches a
running or leased execution, never removes an unmerged run whose task type
requires acceptance while review is still `pending`, and never removes an
approved-but-unlanded run. It prints a durable plan; `--apply` deletes only
private run artefacts, never a worktree. Retention is otherwise unbounded: the
worktree is the artefact.

### Durable record and telemetry

`run.json` keeps `schema_version: 1` and gains **only new keys**; no existing
key changes type and no existing `state` value is added or removed. New keys:
`execution_status` (`queued|running|completed|failed|cancelled|timed_out`),
`failure_category` (closed vocabulary), task type and its source, complexity and
its source, requested/effective effort and their sources, profile source and
routing reason, harness version and capability snapshot, source SHA and dirty
paths, queue duration, time to first output, `telemetry`
(`tokens{input,output,cache_read,cache_write,reasoning}` as nullable pointers,
`source`, `available`, `unavailable_reason`, `raw`), `cost`
(`{usd, kind: api_equivalent, source, pricing_version, estimated_at}`),
`validation` (`{status: pending|running|passed|failed|skipped, steps[…]}`),
`diff_path`, `experiment_id`, `repeat_index`, `retries`, `acceptance_required`. Validation steps are
copied into the record at dispatch, so a worker editing `wb.yaml` mid-run cannot
change what was validated.

`review.json` is a separate append-only sidecar; `review` accepts a terminal,
unreviewed execution and appends one entry, so a reviewer write can never race
the owner's `run.json` save.

#### REQ:record-compatibility
A record written by this build is readable by the previous build without losing
the listing, and a record written by the previous build is readable here. No
existing key changes type; no new value is added to the legacy `state`
enumeration.

#### REQ:null-not-zero
An unobserved number is `null` with an explicit `available: false` and a reason.
No token, duration or cost field is ever defaulted to zero.

#### REQ:cost-provenance
Only OpenCode reports a per-run USD cost, derived from its own price catalogue;
it is labelled `harness-estimated`, `api_equivalent`, with a pricing version and
timestamp. A reported `0` alongside non-zero tokens is recorded as `null` with a
reason (subscription plan), never as a free run. Codex and `dsh` record `null`.
Subscription allowance is never presented as billed cost.

#### REQ:redaction
A provider credential WB injects is removed from the captured stream with a
rolling-window replacer that survives writes split across chunk boundaries; the
harness-written final message is redacted when read. The task, transcript and
validation output are private (`0700`/`0600`) and are never rendered by
`status`, `await`, `list`, `experiment` or `compare`.

#### REQ:separate-statuses
Execution status, validation status and review status are written by different
actors and reported independently. A process that exits `0` may still be
`validation: failed` and `review: rejected`.

#### REQ:validation-lifecycle
Validation is `skipped` unless execution `completed`. The owner records
`running` before the first step and a terminal validation status with the
execution's terminal save. Steps run in their own process group, with the
worker's allowlisted environment **minus the credential**, one timeout per step,
and output in a private per-step log. `await` therefore waits for validation
because the owner records terminal state only after it.

#### REQ:review-command
`wb agent review` is the only writer of review status, requires an existing
terminal execution, appends to `review.json`, and treats an unknown execution ID
as a finding (exit 1), not a usage error.

#### REQ:cancellation
`wb agent stop` (alias `cancel`) writes a `cancel-requested` sentinel before
signalling the process group. The owner classifies the outcome from the sentinel
and records `execution_status: cancelled` (with legacy `state: failed` and
`failure_category: cancelled`), distinct from a crash or a timeout.

#### REQ:failure-categories
Every failed run carries one category from a closed set: `usage`, `config`,
`credential`, `provider`, `harness_missing`, `harness_failed`, `validation_failed`,
`timeout`, `cancelled`, `base_mismatch`, `worktree`, `internal`. Retries are not
performed automatically; `retries` records how many were attempted (`0` in this
MVP) and a retry is simply a new execution.

### Experiments

`compare` resolves the task once, creates every worktree sequentially from the
same configured base **before** dispatching, writes `experiment.json` with the
pinned base SHA, profiles, repeats and pre-allocated agent IDs, then dispatches
with a bounded pool (`--concurrency`, default 3) and waits for each attempt. A
failed dispatch is recorded as a failed attempt and the rest continue. It prints
completion, wall time, tokens, cost, validation, review status, changed files and
worktree per attempt, plus the pinned base and uncontrolled differences (harness
tool sets, isolation level). `--format json` emits the whole experiment document.
`wb agent experiment <id>` re-reads `experiment.json` plus the member records, so
an orchestrator can inspect an interrupted experiment later. Fallback is
disabled inside a comparison: swapping a profile would invalidate it.

#### REQ:compare
`compare` compares profiles, not model strings, and uses the same dispatch engine
as `run`; every attempt records the pinned base SHA and its profile's resolved
identity.

#### REQ:repeat
`--repeat N` runs each profile N times as separate executions with separate
worktrees, grouped in one experiment, so variance is visible.

#### REQ:partial-failure
One attempt failing never loses another attempt's result or telemetry, and never
aborts the experiment.

#### REQ:compare-readback
An experiment is durably described by `experiment.json` plus its member records
and can be re-rendered after a restart, including partially completed ones. The
document is immutable from the moment it is written, before any execution
exists, so the pinned base and each attempt's outcome are recorded on the member
records and recovered from them when the experiment is read back.

### Skill

`ai/skills/wb-agent/` is the **single** canonical skill for `wb agent`
(`SKILL.md`, `agents/openai.yaml`, `references/{profiles,task-types,worktrees,compare,dispatch}.md`).
The existing `wb-agents` skill is retired into it; the dispatch/status reference
moves to `references/dispatch.md`, and every capability row, `commands.json`
entry and prose reference is repointed. Installation is the existing `wb skills
sync` bundle, which covers Claude Code, Codex and the other harnesses; the skill
contains no harness-specific execution logic.

#### REQ:skill
The skill teaches: when to delegate versus execute directly; choosing a task type
versus a profile; complexity ≠ effort; choosing `auto`/`current`/path; async
launch then `await`; `compare` and `experiment`; the mandatory pre-acceptance
check (`diff` → validation → `review`); config-driven model discovery
(`profiles --json`); and never putting secrets in a task.

#### REQ:one-skill-source
Exactly one skill documents `wb agent`; the retired skill leaves no duplicated
instructions and no stale command examples.

#### REQ:skill-install
The skill installs for both Claude Code and Codex through the existing sync
journey, and the existing harness-journey test asserts its presence.

#### REQ:skill-examples
Every `wb …` command line in the skill parses against the real command tree, and
the manifest markers, examples and test references stay valid.

### Documentation

#### REQ:docs
Every new command leaf has a `ai/capabilities.json` row whose flags exactly match
the implementation, a `docs/cli-flag-matrix.md` line, a README line and section,
and help text that matches observed behavior. Harness limitations (OpenCode and
`dsh` reading the real home; `dsh` task in argv; unavailable `dsh` usage) are
documented where the operator will see them.

## Non-goals

- Remote `run`/`compare`/`review` over SSH (no `--to`; follow-up).
- An analytics dashboard, budget enforcement, or dynamic model selection.
- Replacing `wb task offload` (successor sessions) or `wb worker`.
- Auto-merging worker output; WB never lands a worker's work.
- Automatic review: `review` is a deliberate human/orchestrator act.
- Blinded scoring in the MVP; the reviewer can choose to hide labels using the
  JSON documents.

## Acceptance Criteria

### AC: profile-and-task-type-resolution-is-deterministic (verifies REQ:config-precedence, REQ:provenance, REQ:no-hardcoded-models, REQ:dry-run)
`wb agent run --dry-run` resolves profile, effort, worktree, timeout and
validation by the documented precedence, records the source of each value, and
creates nothing.

### AC: three-harness-adapters (verifies REQ:harness-contract, REQ:capability-aware-translation, REQ:harness-error-normalization, REQ:opencode-standalone, REQ:dsh-argv-delivery, REQ:effort-capability)
Each adapter builds its documented non-interactive argv with `--standalone` for
OpenCode and one argv element (≤64 KiB, leading newline when needed) for `dsh`,
normalizes events into one summary, and refuses an explicit effort it cannot
honour while recording an inherited one as not applied.

### AC: fallback-is-ordered-and-cost-safe (verifies REQ:ordered-fallback, REQ:effort-capability, REQ:config-precedence)
A task type's fallback list is tried in the configured order; preflight fallback
applies by default and runtime failover only when enabled; a runtime failover
happens for an infrastructure failure with no work on disk, in a fresh worktree,
is logged, and records `retry_of`/`fallback_from`/`fallback_reason`; an explicit
profile, a shared checkout, a validation failure, a cancellation, a timeout, or
an attempt that produced changes is never retried elsewhere.

### AC: telemetry-is-honest (verifies REQ:null-not-zero, REQ:cost-provenance, REQ:redaction, REQ:record-compatibility)
An unparseable usage payload records `available: false` with a reason and no
numbers; an OpenCode cost is `api_equivalent`/`harness-estimated`; a zero cost
with tokens is null; an injected credential split across writes never reaches
disk; and a record written here still loads in the previous build's reader.

### AC: worktree-modes-and-writer-exclusion (verifies REQ:worktree-modes, REQ:canonical-refusal, REQ:dirty-checkout, REQ:writer-lease, REQ:isolated-worktrees)
`auto`, `current` and a path each work; a canonical clone and a dirty checkout
are refused without the explicit flag and are never cleaned; two live executions
cannot hold one worktree; a killed owner whose harness is still alive keeps the
lease, and the lease is released once the worker's process group is gone — the
kernel's lock, not a PID guess.

### AC: comparison-is-fair-and-durable (verifies REQ:same-base, REQ:repeat, REQ:partial-failure, REQ:compare, REQ:compare-readback)
`wb agent compare --profiles a,b --repeat 2` creates four worktrees from one
pinned base before dispatching, records a moved base as `base_mismatch` instead
of comparing it, survives one failed attempt, and `wb agent experiment <id>`
reproduces the same document from disk.

### AC: three-independent-statuses (verifies REQ:layers, REQ:separate-statuses, REQ:validation-lifecycle, REQ:review-command)
A process that exits `0` can still be `validation: failed` and `review: rejected`;
validation is `skipped` for a failed execution; `wb agent review` appends without
racing the owner and rejects an unknown ID with exit 1.

### AC: cancellation-and-failure-are-recorded (verifies REQ:cancellation, REQ:failure-categories, REQ:retention)
`wb agent stop` records `cancelled` distinctly from `failed` and `timed_out`, a
provider/credential/harness-missing failure carries its category, and `wb agent
prune` never removes a running, leased or unaccepted run.

### AC: skill-is-canonical-and-installed (verifies REQ:skill, REQ:one-skill-source, REQ:skill-install, REQ:skill-examples)
`ai/skills/wb-agent/SKILL.md` exists with valid frontmatter, is the only skill
listed for `agent`, is installed for Claude Code and Codex by the existing
harness journey, and every `wb …` line in it parses against the real command
tree.

### AC: documentation-matches-behavior (verifies REQ:docs, REQ:remote-scope)
Every new leaf has a capability row, a flag-matrix line, a README section, and
help output that matches the implementation; `run`/`compare` expose no `--to`.

## Open Questions

- Whether a pinned `CreateOptions.BaseSHA` should replace verification-in-compare
  once the worktree service can express "create from this exact commit without
  fetching". Verification is sufficient for fairness today but leaves a moved
  base as an excluded attempt.
- Whether `dispatch` should eventually be removed in favour of `run --profile`,
  or kept as the explicit low-level form. It is kept for compatibility and for
  callers that already pin a profile.
- Whether `dsh` will expose machine-readable events or a token report. If it
  does, the adapter's `Summarize` gains usage and cost without a schema change.
- Whether validation should also run for `review`-type executions, where the
  deliverable is prose rather than a passing build.

---
*This document follows the https://specscore.md/feature-specification*
