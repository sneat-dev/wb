---
name: wb-agent
description: >-
  Delegate a bounded coding task through WB to a configured agent harness
  (Codex, OpenCode or the DeepSeek harness), monitor it asynchronously, and
  verify the result before accepting it. Use when work should be isolated in its
  own worktree, handed to a cheaper or different model, run in parallel, or
  compared across execution profiles. Covers `wb agent run`, `await`, `compare`,
  `experiment`, `diff`, `review`, `prune`, task types, complexity, effort and
  worktree selection. Do not use for in-harness subagents or for work you can
  finish correctly in this context yourself.
---

# WB agent

`wb agent` is WB's execution layer for coding agents. It resolves a task type
and an execution profile, starts one harness inside an isolated worktree,
records durable telemetry, and reports execution, validation and review as three
independent statuses. WB never decides whether a worker's change is correct —
you do.

## When to delegate, and when not to

Delegate through WB when at least one is true:

- the work needs an isolated checkout, so this session stays clean;
- a different or cheaper model should do it (`wb agent profiles`);
- the same task should run several ways so you can compare them;
- the work is long enough that you want to do something else meanwhile.

Do not delegate when the task is small enough to finish here, when it needs
your live conversation context, or when it needs a successor session rather than
a worker (`wb task offload`).

## Discover before you run: never hardcode a model

Model identifiers are configuration, not WB constants:

```sh
wb agent profiles --format json
wb agent task-types --format json
```

`profiles` reports each profile's harness, provider, model, effort mapping and
what that harness can report (usage, cost, effort, task delivery). `task-types`
reports the policy each task type resolves to. Use those names; do not invent a
model id.

## Task type vs profile

- A **task type** is policy: which profile to use, which profile per complexity,
  the default worktree mode, the timeout, and the validation suites that must
  pass.
- A **profile** is execution: harness, model, variant, effort, timeout.

Start with a task type. Name a profile only to override the policy for one run.
A task type can also list `fallback` profiles (cheapest first) and
`fallback_on: [preflight, runtime]`, so a provider outage moves to the next
configured profile instead of failing the work; `run --wait` performs that
failover and records `retry_of`/`fallback_from`/`fallback_reason`.

```sh
wb agent run --task-type coding --task-file task.md
wb agent run --task-type coding --profile opencode-flash --task-file task.md
wb agent run --task-type coding --profile antigravity-worker --task-file task.md
```

## Complexity is not effort

`--task-complexity` describes the work and deterministically selects the task
type's profile for that difficulty (`trivial`, `low`, `medium`, `high`,
`extreme`). `--effort` asks the selected model to think harder (`low`, `medium`,
`high`, `xhigh`).

A harness that cannot apply an effort refuses an explicit `--effort` and records
an inherited one as not applied. To see the resolved plan and the source of
every value, run the same command with `--dry-run`; it creates nothing and
starts no process.

```sh
wb agent run --task-type coding --task-complexity high --dry-run --task-file task.md
```

## Choose the workspace deliberately

```sh
# auto (default): a new isolated worktree for this run
wb agent run --task-type coding --task-file task.md

# an existing WB worktree you already own
wb agent run --task-type debugging --use-worktree fix-parser --task-file bug.md

# the checkout you are in; a canonical clone is refused and dirty work needs --allow-dirty
wb agent run --task-type review --worktree current --task-file review.md

# an explicit checkout path
wb agent run --task-type testing --worktree /abs/path/to/worktree --task-file brief.md
```

Parallel writable runs must use different worktrees. Prefer `auto` for anything
that writes. `current` is for a task that belongs in the work you already have.

## Launch, then wait without polling

`run` returns as soon as the detached owner starts. Use `await` — not a polling
loop — to get the result, and `logs` only when you need to debug.

```sh
wb agent run --task-type coding --task-file task.md --format json
wb agent await <agent-id> --format json
wb agent status <agent-id> --format json
wb agent logs <agent-id>
wb agent stop <agent-id>
wb agent stats --format json
```

`wb agent stats` groups retained runs by calling session, repository, and
session/repository pair. `--session <id>` and `--repo <owner/repo>` narrow the
report. WB uses a harness-native session ID when available (for example a Codex
thread ID), otherwise a registered WB session ID. Older unattributed runs remain
visible with an empty session ID. Usage and cost totals include only runs whose
harness supplied those measurements.

`await` reports the still-running state truthfully if its bound elapses; that is
not a success. `stop` (alias `cancel`) records `cancelled`, which is distinct
from `failed` and `timed_out`.

## Compare profiles on the same task

`compare` runs one task across several profiles from one pinned base commit.
Every attempt gets its own worktree; WB creates all worktrees before starting any
worker and never compares an attempt whose base moved. Options are identical
except the profile.

```sh
wb agent compare --profiles opencode-flash,dsh-flash --repeat 2 --task-file task.md
wb agent compare --profiles cheap-codex,opencode-flash --task-file task.md --format json
wb agent experiment <experiment-id> --format json
```

Interpret the result by reading all of it: execution status, wall time, tokens
where the harness reports them, cost only when labelled `harness-estimated` and
`api_equivalent`, validation, review, changed files and the worktree. `n/a` means
the harness reported nothing — never a zero. A faster profile that fails
validation is not the winner.

## Required check before you accept work

A zero exit status is not acceptance. Do all three:

1. Read the change: `wb agent diff <agent-id> --patch`.
2. Read the validation status in `status`; run the project's checks yourself if
   the task type configured none.
3. Record the decision so the acceptance axis is explicit:

```sh
wb agent review <agent-id> --status accepted --note tests-and-review-pass
wb agent review <agent-id> --status needs_changes --note missing-error-path
```

`needs_changes` and `rejected` are separate from a successful process exit. WB
never merges a worker's branch for you; land it with the usual `wb land`.

## Keep the private record clean

The task, transcript and validation logs are private files under WB's home.
Never put a credential in a task: a task is persisted, and on the `dsh` harness
it is also visible in the process table (`task_visibility` says which). Prune old
artefacts with a plan first:

```sh
wb agent prune --older-than 720h
wb agent prune --older-than 720h --apply
```

Read [profiles.md](references/profiles.md) when configuring profiles, providers
or effort; [task-types.md](references/task-types.md) for task-type policy and
validation suites; [worktrees.md](references/worktrees.md) for the three modes
and the writer lease; [compare.md](references/compare.md) for experiment
semantics; and [dispatch.md](references/dispatch.md) for the low-level
`dispatch`/`status`/`await`/`logs`/`stop` reference this skill replaces.
