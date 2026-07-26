---
name: wb-migrate
description: Plan, apply, verify, resume, and publish declarative WB source migrations. Use when a versioned HCL migration should make deterministic code changes in one source root or coordinate provider-first changes across dependent Go repositories.
---

# WB migrate

`wb migrate` is dry-run by default and never edits without `--apply`.

## Route

- Read [apply.md](references/apply.md) for one or more explicit source roots.
- Read [campaign.md](references/campaign.md) for hierarchical,
  multi-repository Go migrations.
- Use `$wb-dependency-campaign` when the change is only a released dependency
  version propagation rather than a structural source migration.

## Safe sequence

1. Lint/review the migration specification.
2. Run the default dry plan and inspect its report.
3. Apply in isolated campaign worktrees.
4. Keep default verification enabled.
5. Publish only after local reports are truthful.
6. Use `--resume` after any interruption or manual correction.

Do not replace a migration with ad hoc search-and-replace across repositories.
Do not publish local `replace` directives. WB removes temporary replacements
and requires declared provider releases before dependent PRs.

`--merge` respects required GitHub checks and dependency order; it never
bypasses branch protection.
