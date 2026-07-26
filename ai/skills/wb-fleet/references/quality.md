# Coverage and verification

Run one local repository by default:

```sh
wb coverage .
wb verify . --checks lint,test,build
wb check . --profile ci
```

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
