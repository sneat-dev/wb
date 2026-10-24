# Low-level dispatch reference

`wb agent run` is the normal entry point. `dispatch` is the same engine without
task-type routing: it requires a profile and exactly one worktree mode.

```sh
wb agent dispatch --new-worktree task-one --profile cheap-coder --task-file brief.md
wb agent dispatch --use-worktree task-one --profile cheap-coder --task-file brief.md
```

## Inspecting a run

```sh
wb agent status <agent-id> --format json
wb agent await <agent-id> --wait-timeout 30m --format json
wb agent logs <agent-id>            # log location and recent actions
wb agent logs <agent-id> --raw      # the full transcript
wb agent list
wb agent stop <agent-id>            # alias: cancel
```

`status` reports execution facts only: resolved configuration and its source,
worktree, branch, base, changed files, timestamps, exit code, failure category,
tokens with their provenance, cost, validation, and review. It never prints the
task or the transcript.

A run whose owner vanished without recording a terminal state is reported as
`abandoned`; it is never reported as completed.

## Remote machines

`dispatch`, `status`, `await`, `list`, `logs` and `stop` accept `--to <machine>`,
or a `machine:agent-id` reference. The remote record and worktree stay on the
machine that owns them. `run`, `compare`, `experiment`, `diff`, `review` and
`prune` are local-only by design.

## Exit codes

- `0` — the command did what it said;
- `1` — findings: an unknown run, a run that refused to start, a transport
  failure, or an execution whose process failed;
- `2` — usage: the invocation itself was rejected.

An execution that exits zero is still only one axis: check validation and record
review.
