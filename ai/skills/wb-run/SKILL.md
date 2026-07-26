---
name: wb-run
description: List, preview, and apply reusable WB fleet recipes. Use for an existing template-section or command recipe that should make the same bounded change across selected repositories.
---

# WB run

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
