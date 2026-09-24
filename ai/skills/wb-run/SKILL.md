---
name: wb-run
description: Execute governed commands with telemetry and CPU admission, or list, preview, and apply reusable WB fleet recipes. Use for tests, builds, Git commands, command-cost history, and repeatable fleet recipes.
---

# WB run

Run tests, builds, Git inspections, and other commands through WB so the same
invocation can gain scheduling, receipts, resource limits, and audit metadata
without changing agent instructions:

```sh
wb run -- go test ./internal/worktrees -run TestCreate
wb run --async --worker codex-local -- go test ./internal/worktrees -run TestCreate
wb run --async --worker codex-local --idempotency-key create-2 -- go test ./internal/worktrees -run TestCreate
wb run -- git status --short
wb run --history --days 7
wb run --changed -- go test
wb run --changed --target origin/main -- go vet
```

Synchronous command mode preserves standard streams and the child exit code.
`--async --worker <stable-id>` submits through the authenticated durable local
daemon for only that worker identity and returns a JSON operation receipt. WB
rejects a missing target rather than choosing another compatible sandbox.
If a bridge response is lost, repeating the exact command reattaches to its
unresolved envelope-bound idempotency key. Use a distinct
`--idempotency-key` when intentionally submitting identical argv while an
earlier identical request is unresolved. Recovery expires after seven days;
completed and orphaned envelopes are removed after one day.
Start a long-lived worker from inside the same harness sandbox before
submitting daemon-backed work:

```sh
wb worker connect --id <stable-worker-id> --root <canonical-projects-root>
```

The daemon schedules and journals normal jobs, including argv; the named worker
independently checks the assigned cwd, inherits the harness environment and
sandbox, renews its lease every five seconds, and executes the command. Keep
secrets in the worker's inherited environment, never argv. Environment
overrides are refused and never enter the daemon request. The external administrator policy
is only for the trusted `wb daemon operation submit` raw fallback; agents and
WB commands must not create it. CPU-heavy work shares a cross-process budget of
`CPUCount-1`; WB leaves one logical CPU for the harness and OS and exports the
admitted units to supported tools. A focused job (a single-package Go test/vet,
or a light lint) always admits immediately at `max(1, NumCPU/8)`. On a machine
with 8+ CPUs, a "heavy" job (a broad Go/Node test or build, or coverage/race)
is admitted adaptively instead of at a fixed weight: it gets a share based on
how many heavy jobs are alive at that moment, capped so concurrently running
heavy jobs never sum past 150% of NumCPU (`wb run --help` has the exact
formula); on a machine with fewer than 8 CPUs the original fixed table applies
unchanged. `wb run --history` summarizes privacy-safe wall and CPU cost from
the current worktree without exposing raw arguments or output. The filesystem
lease remains the worker-level safety belt.

A CPU-heavy synchronous invocation reports its place in that shared budget on
stderr — even without a terminal, so a redirected log still shows progress
instead of going silent for minutes while several agents share one machine.
The moment it is admitted, it prints either `wb run: admitted (queue empty)`
or, when the budget is full, `wb run: queued <summary> (position N of M,
waiting on: <pid> <summary>)`; while still queued it heartbeats at most every
10s (`wb run: still queued ...; running: <pid> <summary> <age>`), then prints
`wb run: admitted after <wait>` and, on completion, `wb run: done in
<elapsed> (exit <code>)`. `--quiet` silences all four lines; stdout and any
`--format=json` receipt stay untouched either way. Inspect the same state
without submitting a command:

```sh
wb run --queue
```

`--queue` lists every command currently holding or waiting for a CPU lease
slot (pid, age, worktree, summary); add `--format=json` for a machine-readable
listing.

Keep the worker inside the harness sandbox. WB prefers the local protected
socket and reports an explicit project-root file-bridge fallback only for
socket permission or reachability failures. The fallback keeps the same daemon
queue authority and uses authenticated, generation- and worker-fenced atomic
envelopes; it is never a reason to run the worker outside the sandbox.

Use a recipe instead of re-reading and editing the same files repository by
repository.

```sh
wb run --list
wb run <recipe> --filter <scope>
```

`wb run` is dry-run by default. Inspect the preview, then apply the same
selection:

```sh
wb run <recipe> --filter <scope> --apply
```

`--apply` can commit and push or open a PR according to repository state and
recipe policy. Never add it speculatively. Narrow with `--filter` before a
fleet-wide apply.

Read [recipes.md](references/recipes.md) only when creating or diagnosing
`wb.yaml`.

## Scope a command to what a local diff changed

`--changed` appends the Go packages a local diff touches (staged, unstaged,
and already-committed changes since the merge base, combined) to the command
instead of running it as given:

```sh
wb run --changed -- go test
wb run --changed --target origin/main -- go vet
```

`--target` names the branch or ref to diff against; without it, WB detects
the repository's default branch from local Git state only (an explicit
`WB_DEFAULT_BRANCH` override, the recorded `origin/HEAD` symref, or a
conventional `main`/`master` remote-tracking ref) and fails closed with a
usage error if none resolves. When nothing changed, WB prints that and exits
0 without running the command at all — this never runs the underlying test
binary, `go vet`, or anything else on an empty package list. This is a cheap
smoke check scoped to what changed, never a prediction of a full or merged
coverage/vet run over the whole module (issue #570,
spec/plans/coverage-to-100/README.md task-19); `wb coverage --changed` shares
the same underlying changed-package computation.

## Await completion without heartbeat context

After asynchronous submission, retain the operation ID and start one waiter as
an asynchronous harness tool. Continue independent work until that tool completes:

```sh
wb daemon operation wait <operation-id> --format=json --progress-file /path/to/human-progress.log
```

Humans can tail the progress file in a separate terminal. The agent-facing
stdout/stderr contain no periodic operation heartbeats; the final receipt keeps
failure details and bounded output tails. `--progress=false` disables human
heartbeats when another observer already provides liveness. Do not repeatedly
poll `operation get` or forward the human log into the agent context. Harness
completion delivery is required: a quiet synchronous tool still blocks its caller.
