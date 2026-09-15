---
name: offload
description: >-
  Delegate a bounded coding task to a cheaper WB-dispatched worker agent, have a
  cheap native supervisor subagent manage and verify it, and get back one
  concise PASS / FAIL / ESCALATE verdict instead of the worker's transcript. Use
  for /offload, "offload this to a cheaper model", "hand this bounded task to
  the deepseek worker", "dispatch this and tell me if it worked". Do not use for
  a task you have not bounded, for freezing work without a worker (that is
  /park), for starting an addressable successor session (that is `wb task
  offload` or /move), or for in-harness subagents with no WB involvement.
---

# Offload

Hand **one bounded task** to a WB-dispatched worker, and get back a verified
answer — not a transcript.

```text
you (strong parent)
    │
    └── /offload
            ↓
       cheap native supervisor subagent
            │
            ├── wb agent dispatch --new-worktree <name> | --use-worktree <name> \
            │       --profile <profile> --task-file <brief>
            ├── wb agent await <agent-id>
            ├── inspect result, diff, and tests
            └── PASS / FAIL / ESCALATE
                        ↓
                    you
```

WB is the execution layer: it creates the worktree, launches the worker, and
reports facts. It never judges the diff. You and your supervisor judge.

## Before you offload

Do the hard thinking **yourself**. The cheap supervisor cannot resolve
architecture. A task is ready to offload only when it has:

- one clear goal, in one repository;
- explicit acceptance criteria the supervisor can check;
- a boundary: what must NOT change;
- no unresolved design decision.

If any of those is missing, decompose further or do the work yourself. A vague
offload produces a confidently wrong `PASS`.

## The supervisor

Start a **cheap, fast subagent of your own harness** — Luna (or the equivalent
small/fast Codex subagent) under Codex, Haiku under Claude Code. Do not use your
own top-tier model for this; supervising mechanics is not where expensive
judgement belongs.

Give the supervisor, verbatim:

- the original bounded task;
- the acceptance criteria;
- the WB profile to dispatch with;
- the worktree name and which mode to use (`--new-worktree` for fresh work,
  `--use-worktree` to continue in a worktree a previous worker produced);
- the repository (`owner/repo`), unless the current checkout's origin is right.

Tell it explicitly: **do not ask the worker to continue, do not fix anything
itself, and do not read the whole worker transcript.** It dispatches, awaits,
inspects, and reports.

## What the supervisor runs

```sh
wb agent dispatch \
  --new-worktree <name> \
  --repo <owner/repository> \
  --profile <profile> \
  --task-file <brief>
```

`dispatch` starts the worker now and returns an agent run ID immediately. Then:

```sh
wb agent await <agent-id> --format json
```

`await` blocks until the run is terminal. Its exit code matters: `0` means the
run reached a terminal state; non-zero means the wait bound elapsed and the run
had **not** finished. Never treat a non-zero `await` as success.

The JSON carries everything the supervisor needs to verify: `state`,
`exit_code`, `resolved` (harness/provider/model/reasoning), `worktree`,
`branch`, `base_sha`, `changes.files`, `usage`, `result` (the worker's own final
message), and `log_path`.

To check the actual change, inspect the retained worktree directly:

```sh
git -C <worktree_dir> status --short
git -C <worktree_dir> diff <base_sha>
```

Run the acceptance tests the brief names. Do not accept the worker's own claim
that tests pass.

If the worker looks stuck, stop it with `wb agent stop <agent-id>`. Full
transcripts are at `wb agent logs <agent-id>` and are only for debugging.

## The verdict

Return **exactly one** of these, with the evidence that justifies it:

- **PASS** — the worker appears to have completed the bounded task and the
  relevant validation supports it.
- **FAIL** — the worker clearly did not complete the task, or introduced an
  obvious failure.
- **ESCALATE** — the supervisor cannot confidently determine correctness, or
  found something needing stronger judgement: an architectural decision, an
  unclear requirement, a security-sensitive concern, a surprisingly broad diff,
  conflicting implementation approaches, or tests too weak to establish
  correctness.

The supervisor is not the final authority. `ESCALATE` is the correct answer
whenever the evidence does not settle the question — a wrong `PASS` costs far
more than an escalation.

## What comes back to you

```text
PASS | FAIL | ESCALATE
task:      what was offloaded
agent:     agt-…
worktree:  <name>  (kept — it is the artefact)
resolved:  codex / deepseek / deepseek-flash / high
changes:   N files, +X/-Y
evidence:  the command(s) run and what they showed
concern:   …            (ESCALATE, or a caveat on a PASS)
```

Keep it that compact. You should not need the worker's transcript to decide what
to do next.

## Boundaries

- **No repair loop.** One dispatch, one verification, one verdict. If the work
  needs another attempt, that is a new decision you make, not an automatic
  cycle.
- **The worktree is kept.** WB never deletes, resets, commits, pushes, or merges
  on the worker's behalf. Land or retire it with the normal WB lifecycle
  commands (`/wb-worktrees`).
- **Profiles, not model names.** Pick a profile configured in `wb.yaml`. If none
  fits, say so and ask, rather than inventing one.
- **Different intent, different skill.** To freeze work without starting a
  worker, use `/park`. To start an addressable successor session you can message
  and recall — locally or on another machine — use `wb task offload` then
  `/pickup`, or `/move` for the whole session.
