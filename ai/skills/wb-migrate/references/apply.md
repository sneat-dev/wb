# Plan and apply a migration

Plan one or more roots:

```sh
wb migrate <spec.hcl> <root> [root...]
```

Use `--check` when a non-empty plan should exit 1, such as CI or a drift gate:

```sh
wb migrate <spec.hcl> <root> --check --format json
```

Write deterministic reports when the plan needs review or later resume:

```sh
wb migrate <spec.hcl> <root> --report-dir <dir> --format yaml
```

Apply only after reviewing the same inputs:

```sh
wb migrate <spec.hcl> <root> --apply
```

Verification defaults to `full`. Select `compile`, `test`, or `full` with
`--verify`; `--no-verify` requires an explicit reason.

For a repeat run after partial edits:

```sh
wb migrate <spec.hcl> <root> --apply --resume
```

Preserve manual corrections in the expected migration worktree. WB locks an
active apply campaign so concurrent runs fail instead of racing.
