---
name: wb-ci
description: Audit repository CI/CD policy with WB. Use when reviewing coverage gates, path-selective jobs, E2E setup duplication, artifact promotion, or whether CI policy findings should block a commit, push, or merge.
---

# WB CI

`wb ci audit` is read-only. Audit one repository before scanning the fleet:

```sh
wb ci audit .
wb ci audit . --strict
```

Use `--strict` when findings must produce exit 1. Use JSON for automation:

```sh
wb ci audit . --strict --json
```

Use fleet mode only when the task genuinely spans local repositories:

```sh
wb ci audit --fleet --filter <owner-or-repository> --strict
```

The audit checks positive coverage thresholds, changed-path selection for
mixed stacks, duplicated Playwright setup, and build-artifact provenance.
It reports policy, not runtime test success; run the repository's actual
checks through `$wb-fleet` or its WB-managed pre-push hook as well.

Exit codes are `0` clean, `1` findings/runtime failure, and `2` invalid usage.
Do not treat exit 1 as a malformed command.
