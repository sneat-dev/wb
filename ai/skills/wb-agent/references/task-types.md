# Task types and validation

A task type is a named policy. It names no model:

```yaml
agents:
  task_types:
    coding:
      profile: opencode-flash
      complexity:
        trivial: cheap-codex
        high: dsh-flash
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
      - { name: vet,  argv: [go, vet, ./...],  timeout: 5m }
      - { name: test, argv: [go, test, ./...], timeout: 20m }
  defaults:
    task_type: coding
```

Fields:

| Field | Meaning |
|---|---|
| `profile` | default profile when no complexity match exists |
| `complexity` | complexity → profile, applied only when `--task-complexity` is given |
| `effort` | default effort for the task type |
| `worktree` | `auto` or `current` |
| `timeout` | default bound for a run of this type |
| `validate` | validation suites that must pass after a completed run |
| `acceptance` | `required` or `optional`; required blocks pruning while unreviewed |

Built-in task types (`coding`, `debugging`, `testing`, `review`, `research`,
`documentation`) are policy only. They name no profile, so a run resolves one
from `agents.defaults.profile` or fails with the configured profile list.

## Resolution order

Highest wins:

1. `--profile` (or `--harness` with `--model`);
2. the task type's `complexity` entry for `--task-complexity`;
3. the task type's `profile`;
4. `agents.defaults.profile`.

## Validation

Validation steps run after the harness completes, in the worker's worktree,
with the allowlisted worker environment minus the provider credential. Each step
is an argv list — never a shell string — with its own timeout and private log.

`validation.status` is independent of execution status:

- `skipped` when the run did not complete, or the task type configured nothing;
- `running` while steps execute;
- `passed` / `failed` per the steps.

The exact steps are copied into the run record at dispatch, so editing
`wb.yaml` during a run cannot change what that run is validated against.
