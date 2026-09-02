---
name: wb-deps
description: Inspect dependency topology and convergence, then set or propagate dependency versions with WB. Use to find consumers, detect version drift or replaces, pin an exact GitHub Actions or Go version, plan release order, or update dependent repositories in verified waves.
---

# WB dependencies

Choose the narrowest operation:

| Intent | Command | Reference |
|---|---|---|
| Understand consumers, versions, or release order | `wb deps graph` | [graph.md](references/graph.md) |
| Find version divergence, replaces, or major-path splits | `wb deps drift` | [drift.md](references/drift.md) |
| Decide whether a published npm package can be reused in one checkout | `wb deps peers` | [peers.md](references/peers.md) |
| Set one known version in existing references | `wb deps set` | [set.md](references/set.md) |
| Propagate published Go or npm releases through consumers | `wb deps bump` | [bump.md](references/bump.md) |
| Publish approved npm packages, verify the registry, and propagate consumers | `wb deps publish npm` | [publish-npm.md](references/publish-npm.md) |
| Enforce which dependencies and import directions are allowed | `wb deps policy` | [policy.md](references/policy.md) |
| Assess or land the `go 1.26.x` + `toolchain go1.27.0` directive policy | `wb deps go-directive` | [go-directive.md](references/go-directive.md) |

Use `$wb-dependency-campaign` for a breaking or multi-release rollout.

Note that `deps policy` is about which dependencies are *permitted*; the
other verbs are about which *versions* are selected. `deps go-directive` is
narrower still: it is about one specific pair of directives inside `go.mod`
itself, not about the module version references the other verbs manage.

Start read-only. `graph`, `drift`, `go-directive report`, `go-directive
check`, and every `policy` verb except `init` are read-only; `set` and `bump`
provide `--dry-run`, and `go-directive check` requires `--apply` to write
anything.
Inspect scope and reports before publication flags.

WB mutation happens in operation worktrees, not canonical clones. `--push`
implies `--commit`; `--pr` implies push; `--merge` implies all prior stages
and waits for observed passing GitHub checks. Prefer `--resume` after partial
progress so WB reuses validated branches, PRs, and report state.

Exit codes are `0` success, `1` findings/runtime failure, and `2` usage.
