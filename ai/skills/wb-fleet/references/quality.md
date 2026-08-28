# Coverage and verification

Run one local repository by default:

```sh
wb coverage .
wb verify . --checks lint,test,build
wb check . --profile ci
```

When one Go package has many process-global fixtures and therefore cannot use
`t.Parallel`, opt it into isolated process shards instead of weakening or
skipping the suite:

```sh
wb coverage . --test-shards 8 --shard-package ./internal/worktrees

# Preserve a merged profile and enforce a floor.
wb coverage . --test-shards 8 \
  --shard-package ./internal/worktrees \
  --coverage-profile profile.cov --minimum 58
```

WB discovers top-level tests, examples, and fuzz targets, sorts them, assigns
each exactly once by deterministic round-robin, runs all unnamed packages once,
and losslessly merges the coverage profiles. `--shard-package` is deliberately
opt-in because discovery invokes that package's `TestMain` once before every
process shard invokes it again. Use it only when process isolation is safe; WB
rejects fleet scope and ambiguous package patterns rather than guessing.

For repeatable repository-owned validation, commit the approved shard plan as
`.wb/quality.yaml` with `version: 1` and `go_test.shards` plus
`go_test.packages`. `wb worktree merge` consumes that policy for candidate
validation; ad hoc fleet runs continue to require explicit command flags.

Choose one verification surface:

- `coverage` measures statement-weighted Go coverage.
- `verify` runs selected conventional Go and Node checks.
- `check --profile fast|full|ci` uses a stable policy; `ci` also runs
  SpecScore lint when `spec/` exists.

For a selected fleet:

```sh
wb check --fleet --match '<owner>/*' --profile ci \
  --parallel 2 --report-dir <dir>
```

After partial failure, preserve the report directory and rerun only failures:

```sh
wb check --fleet --match '<owner>/*' --profile ci \
  --resume --report-dir <dir>
```

`--timeout` applies per external check and `--retry` retries only failures.
Use bounded parallelism: more workers are not faster when repositories contend
for the same CPU, disk, package cache, or external rate limit.

These commands inspect existing clones and do not fetch, modify, commit, or
push. For a repository-specific E2E suite wired into a pre-push hook, run the
hook's orchestrating command once instead of duplicating it here.

## Graduation receipts

`wb verify receipt` is the read-only final composition step, not an assertion
that arbitrary prose is evidence. Give it the closed, versioned outputs from
the local CI-equivalent check, CI wait, target observation, external deployment
producer, and WB terminal cleanup:

```sh
wb check --fleet --match acme/wb --profile ci --format json >local-check.json
wb ci wait --repo acme/wb --target main --head <final-target-sha> --json >ci-wait.json
wb verify receipt remote-target --repo acme/wb --target main --output remote-target.json
wb verify receipt --local-check local-check.json --ci-wait ci-wait.json --remote-target remote-target.json --deployed-revision deployment.json --terminal-cleanup cleanup.json
```

Use the exact fleet match for the local report: direct-path quality output has
only a directory basename, while graduation requires the controlled
`owner/repository` identity. The checkout must remain clean at one unchanged
revision for the whole check. CI evidence must come from the final target SHA,
not a pull-request head.

The target-observation subcommand resolves the configured GitHub remote, runs
a canonical `git ls-remote` observation, and emits evidence that the composer
corroborates with every other component. The external deployment receipt must
retain its exact structured payload as `payload_json`, its SHA-256 digest, a
JSON pointer resolving the deployed revision, and a credential-free immutable
run URL. Keep all evidence files immutable for review; a receipt refuses
mismatched revisions, self-attestation fields, stale or non-monotonic
timestamps, and incomplete cleanup. Cleanup removes campaign worktrees and
source branches; it never removes the canonical target checkout.
