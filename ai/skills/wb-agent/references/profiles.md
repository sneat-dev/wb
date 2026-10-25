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
