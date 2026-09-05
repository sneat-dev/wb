---
name: wb-fleet
description: Synchronize, inspect, test, build, and measure local repository fleets with WB. Use for wb sync, status, coverage, verify, or check, especially when scope, parallelism, retries, and resumable reports can avoid repeated repository scans or CI runs.
---

# WB fleet

For the preconfigured CodeGrapher local-tool plugin, see [CodeGrapher](references/codegrapher.md). It installs, updates, and reports the executable only; it does not refresh repository graphs.

Choose one command that answers the question:

| Need | Command | Reference |
|---|---|---|
| Reconcile canonical clones with GitHub | `wb sync` | [sync.md](references/sync.md) |
| Fix a repository sync cannot pull | `wb repo init-remote` / `wb repo ignore` | [unsynced.md](references/unsynced.md) |
| One glance at fleet size and attention | `wb fleet` / `wb fleet overview` | [status.md](references/status.md) |
| Fleet inventory and attention counts | `wb fleet stats` | [status.md](references/status.md) |
| Find local changes, stashes, conflicts, or unpushed commits | `wb fleet status` | [status.md](references/status.md) |
| Inventory every visible open remote pull request | `wb fleet prs` | [status.md](references/status.md) |
| Audit or clean non-canonical clone placement | `wb layout audit` / `wb layout clean` | [layout.md](references/layout.md) |
| Delete a local clone whose repository is archived on GitHub, only when nothing would be lost | `wb archive clean` | [archive.md](references/archive.md) |
| Inspect one repository checkout | `wb repo status` | [status.md](references/status.md) |
| Measure Go coverage | `wb coverage` | [quality.md](references/quality.md) |
| Run conventional lint/test/build | `wb verify` | [quality.md](references/quality.md) |
| Run a stable local CI profile | `wb check` | [quality.md](references/quality.md) |
| Publish this machine's state for other machines | `wb remote publish` | [remote.md](references/remote.md) |
| See every machine's attention worklist | `wb remote status` / `wb remote machines` | [remote.md](references/remote.md) |
| Reserve a task fleet-wide before starting work | `wb remote claim <task>` | [remote.md](references/remote.md) |
| Give up a task claim | `wb remote release <task>` | [remote.md](references/remote.md) |
| See who holds every task claim | `wb remote claims` | [remote.md](references/remote.md) |

Do not run overlapping `verify` and `check` commands unless they answer
different questions. Start with one repository; add `--fleet` and a filter
only when fleet evidence is required.

Reporting commands are non-mutating and support structured output. `sync`
changes canonical clones, so preview it first.

For a machine-readable remote PR snapshot:

```sh
wb fleet prs --format json
```

For automation, add `--non-interactive` and use YAML or JSON where supported.
Exit codes are `0` clean, `1` findings/runtime failure, and `2` usage.
