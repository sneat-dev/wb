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

## Fallback

```yaml
agents:
  fallback: [cheap-codex, dsh-flash]
```

Fallback is consulted only when a preflight probe fails before any worktree
exists: a missing harness binary or a missing provider credential. An explicit
`--profile` is never swapped, and `compare` disables fallback entirely.
