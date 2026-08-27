# Go directive fleet policy

Every first-party Go module should declare `go 1.26.x` paired with
`toolchain go1.27.0` — not `go 1.27.0`. Only the `go` line participates in
Go's minimal version selection (MVS), and MVS takes the **maximum** across the
whole build list, so a widely-consumed module declaring `go 1.27.0` drags
every consumer to 1.27 whether it wanted that or not. The `toolchain`
directive is not imposed on consumers, so `go 1.26.x` + `toolchain go1.27.0`
builds with 1.27 without forcing it on anyone downstream.

The policy is not always achievable: a module's own `go` directive must be at
least the maximum `go` directive declared by every dependency in its resolved
build list. `wb deps go-directive` determines that by resolving the real
module graph with `go` tooling — never by grepping `go.mod` files — so a
"cannot comply" verdict always names the exact forcing dependency.

## Choose the narrowest verb

| Intent | Command |
|---|---|
| One repository (or one directory under it) | `wb deps go-directive check` |
| Land it in one repository | `wb deps go-directive check --apply` |
| Fleet-wide dry-run plan | `wb deps go-directive report` |

## One repository

```
wb deps go-directive check ./backend
```

Discovers every `go.mod` at or under the directory — a repository with a root
module plus `backend/go.mod` reports one line per module — and prints one of:

- `compliant` — already `go 1.26.x` + `toolchain go1.27.0`.
- `would change go X -> go 1.26.x (toolchain ... -> go1.27.0)` — achievable,
  not yet written.
- `cannot comply: <dependency>@<version> declares go <version>` — a
  dependency's own `go` directive sets a ceiling above 1.26; that dependency
  is the worklist item to fix first, not this module.
- `go <version> is below the 1.26 floor ... not raising it automatically` —
  already below the target; raising a floor is a separate policy decision, so
  this is reported and left alone.

Exits `1` when any module needs attention, so it wires straight into CI as a
required check without a bare invocation ever changing anything.

## Land it

```
wb deps go-directive check ./backend --apply
```

Writes the `go` and `toolchain` directives only for a module whose verdict is
`would-change`, using `go mod edit`, then runs `go mod tidy` and re-resolves
the module to confirm the edit was not silently reverted by a forcing
dependency the assessment missed. A revert here fails the command loudly —
never a go.mod that looks compliant and is not.

## Watch the fleet

```
wb deps go-directive report --match 'acme/*'
wb deps go-directive report --format json
```

Walks every discovered repository and every Go module inside it — never one
row per repository — and reports the same five verdicts, plus a row per
repository with no `go.mod` at all. This command has no `--apply` flag: it
never writes to any repository, by construction. The `cannot-comply` rows read
as one worklist of which upstream module needs fixing first before its
consumers can move.
