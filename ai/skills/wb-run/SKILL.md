---
name: wb-run
description: Execute governed commands with telemetry and CPU admission, or list, preview, and apply reusable WB fleet recipes. Use for tests, builds, Git commands, command-cost history, and repeatable fleet recipes.
---

# WB run

Run tests, builds, Git inspections, and other commands through WB so the same
invocation can gain scheduling, receipts, resource limits, and audit metadata
without changing agent instructions:

```sh
wb run -- go test ./internal/worktrees -run TestCreate
wb run -- git status --short
wb run --history --days 7
```

Command mode preserves standard streams and the child exit code. CPU-heavy work
shares a cross-process budget of `CPUCount-1`; WB leaves one logical CPU for the
harness and OS and exports the admitted units to supported tools. `wb run
--history` summarizes privacy-safe wall and CPU cost from the current worktree
without exposing raw arguments or output. The local daemon will become the
normal submission path; the filesystem lease is its worker-level safety belt.

Use a recipe instead of re-reading and editing the same files repository by
repository.

```sh
wb run --list
wb run <recipe> --filter <scope>
```

`wb run` is dry-run by default. Inspect the preview, then apply the same
selection:

```sh
wb run <recipe> --filter <scope> --apply
```

`--apply` can commit and push or open a PR according to repository state and
recipe policy. Never add it speculatively. Narrow with `--filter` before a
fleet-wide apply.

Read [recipes.md](references/recipes.md) only when creating or diagnosing
`wb.yaml`.
