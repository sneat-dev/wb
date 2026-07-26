# Hook metrics

WB records local JSONL events when metrics are enabled. Inspect the default
14-day summary:

```sh
wb hooks metrics .
```

Narrow or automate it:

```sh
wb hooks metrics . --days 7 --repo <text>
wb hooks metrics . --days 30 --json
```

Use `--file` only for a known alternate event file. The report distinguishes
commit checks, push attempts, failures, block counts, and average duration.
Git has no post-push hook, so push counts are attempts, not confirmed remote
acceptance.

Use the slowest repeated blocks to decide whether a check should be scoped,
cached, consolidated into one E2E run, or moved from pre-commit to pre-push.
Do not disable a correctness gate solely because it is slow.
