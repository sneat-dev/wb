# Profiles, providers and effort

A profile is one execution configuration in `wb.yaml` (under the `agents` key):

```yaml
agents:
  profiles:
    opencode-flash:
      harness: opencode
      model: opencode-go/deepseek-v4.1-flash
      variant: medium
    dsh-flash:
      harness: dsh
      model: deepseek-v4-flash
    antigravity-worker:
      harness: antigravity
      model: gemini-3.1-pro-high # choose an available slug from agy models
      effort: high
    cheap-codex:
      harness: codex
      provider: deepseek
      model: deepseek-v4.1-flash
      effort: medium
```

- `harness` and `model` are required.
- `provider` is required for `codex` and names an entry in `agents.providers`.
  For `opencode` it is derived from the `provider/model` prefix and, if written,
  must match it. For `dsh` it is `deepseek-official`.
  For `antigravity`, omit it: `agy` manages model routing and credentials.
- `variant` is OpenCode only and is appended as `#variant` when the command is
  built. Keep it out of `model`: `model` must stay a legal execution
  identifier.
- `effort` (or the legacy `reasoning`) is the profile's default effort. Naming
  both is an error.

## Effort, per harness

| Harness | Effort setting | What WB does |
|---|---|---|
| `codex` | `model_reasoning_effort` | passes `low`, `medium`, `high`, `xhigh` through |
| `opencode` | model variant | maps the effort through the profile's `effort_variants` |
| `dsh` | none | refuses an explicit effort, records an inherited one as not applied |
| `antigravity` | `--effort` | accepts only `low`, `medium`, `high` |

For OpenCode, a mapped effort wins over the static variant:

```yaml
      effort_variants:
        low: cheap
        high: heavy
```

## Providers

```yaml
agents:
  providers:
    my-provider:
      base_url: https://api.example.com
      credential_env: MY_PROVIDER_API_KEY
      wire_api: responses
```

A provider names exactly one credential source (`credential_env` or
`credential_file`), never both, and never stores the secret. Codex runs receive
the credential in their environment only; OpenCode and `dsh` read their own auth
stores and run with the real home directory, so their tool subprocesses can read
those stores. That difference is recorded on every run as
`harness_capabilities.isolation`.

Antigravity also uses the real home and its own cached authentication. Install
`agy` and authenticate interactively before dispatching. WB probes its required
flags before creating a worktree (verified with CLI 1.1.13). It runs
`--mode accept-edits --sandbox`, with a private 0600 brief in a dedicated
subdirectory under the run directory; only that brief directory is added with
`--add-dir`. The task body stays off argv (`task_visibility: private-file`).
The bootstrap names the absolute worktree for edits. This is user-home
execution, not a guarantee that the process cannot access other user files.

Workspace file edits need no interactive approval. Commands still use the
operator's `agy` permission settings and may be soft-denied in headless mode;
configure scoped command permissions and require WB validation for the task.
WB records that limitation in harness diagnostics and never passes
`--dangerously-skip-permissions`. A terminal `SUCCESS` means the harness
finished, not that the work passed validation or review. Token fields come only
from the final `result.usage`; missing fields stay unavailable, and cost is
unavailable. See the [official headless contract](https://antigravity.google/docs/cli/headless/).

## Fallback: an ordered list, not a guess

A task type lists the profiles to try, cheapest first:

```yaml
  task_types:
    coding:
      profile: oc-go
      fallback: [deepseek, codex, claude]
      fallback_on: [preflight, runtime]
```

`agents.fallback` is the global tail, used after the task type's own list.

Triggers, chosen by `fallback_on` (default `[preflight]`):

| Trigger | When it applies |
|---|---|
| `preflight` | a harness binary or credential is missing before a worktree exists |
| `runtime` | the provider failed the run itself: `provider`, `credential` or `harness_missing` |

Rules that protect the work and the budget:

- The order is the operator's cost policy; WB never reorders or promotes a
  profile on its own.
- An explicit `--profile` (or `--harness`/`--model`) is never swapped.
- A runtime failover needs a fresh worktree, so it applies to `auto`/`--new-worktree`
  only; `current` and an explicit path are never retried elsewhere.
- It never retries a validation failure, a cancellation, a timeout, or an
  attempt that left changes on disk.
- It needs a waiting caller: `run --wait` performs it; an asynchronous `run`
  cannot, and `compare` disables fallback because swapping a profile would
  invalidate the experiment.
- Each failover is logged (`wb agent: execution … failed (…); falling back to
  profile …`) and the new execution records `retry_of`, `fallback_from` and
  `fallback_reason`. A retry is a new execution, not a resumed one.
