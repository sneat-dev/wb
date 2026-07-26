# Hierarchical migration campaign

Use for a declarative change spanning dependent Go modules:

```sh
wb migrate <spec.hcl> <root> --hierarchical \
  --github-dir <projects-root> --report-dir <dir>
```

The dry campaign defers worktree evaluation. Apply to create isolated
provider-first worktrees:

```sh
wb migrate <spec.hcl> <root> --hierarchical --apply \
  --github-dir <projects-root> --parallel 2 --report-dir <dir>
```

WB uses temporary relative `replace` directives so dependent worktrees can
compile together. Before PR publication it removes them, tidies modules, and
requires the migration specification's provider release declarations.

Publication flags compose:

- `--commit` requires `--apply`;
- `--push` implies commit;
- `--pr` implies push;
- `--merge` implies PR and merges only after required checks pass.

Use `--module-ref module=ref` only to select an intentional provider ref.
Prefer immutable released versions for publishable consumer PRs.

After interruption or a failed check, keep the same roots and report directory:

```sh
wb migrate <spec.hcl> <root> --hierarchical --apply --resume \
  --github-dir <projects-root> --report-dir <dir>
```

Use `--cleanup` only after reviewing the campaign report. It removes clean
campaign worktrees but leaves branches and reports.
