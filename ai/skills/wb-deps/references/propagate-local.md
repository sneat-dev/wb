# `wb deps propagate local`

Build consumers against a library's **working tree** instead of a published
version, so a change is proven across every affected repository before anything
is published. This is the normal path inside a stream; remote propagation is the
end-of-stream wave.

```sh
wb deps propagate local <library-worktree> --to <consumer-worktree>... [--verify]
wb deps propagate local --to <consumer-worktree>... --undo
```

Worked examples:

```sh
wb deps propagate local /path/to/library --to /path/to/app --verify
wb deps propagate local --to /path/to/app --undo
```

## What it discovers

Discovery reads the **library worktree itself**, never an operator-supplied
name:

- the Go module path from `backend/go.mod`, or from the module root where the
  repository has no `backend/`
- npm package names from `libs/**/package.json`

A consumer that declares none of the discovered identities is **reported and
skipped**, not linked to something it does not use. Declarations are read from
the same canonical dependency sections `wb deps graph` uses — `dependencies`,
`devDependencies`, `peerDependencies`, `optionalDependencies`, and Go
`require`.

## Go consumers

A Git-excluded `go.work` is written at the consumer worktree root. Its `use`
entries name **every** module in the consumer worktree — `backend/`, the module
root, any nested tooling module — **plus** the library's modules. A workspace
holding only the library would leave the consumer's own module outside the
workspace it now sits under, and `go build ./...` in `backend/` would not
resolve at all.

- `go.mod` is never touched, and **no `replace` directive is added**.
- `go.work` and `go.work.sum` are both excluded through the worktree's own
  exclude file, never through a tracked `.gitignore`.
- CI is unaffected structurally: the file does not exist in the repository, so
  a CI checkout has no `go.work` to honour. `GOWORK=off` is the explicit
  guarantee where a toolchain might discover one anyway.

**Do not run `go mod tidy` or `go get` while a link is live.** Both resolve
against the workspace and would write a `go.sum` describing an unpublished
library tree. This verb never runs them, and any WB verb that mutates the module
graph sets `GOWORK=off` itself.

## npm consumers

1. A **clean frozen install of the unlinked tree** is proved first, so a link
   never masks a lockfile or manifest mismatch. A failed frozen install stops
   the link; nothing is built. It runs **once per consumer**, before any
   linking — not once per package. A second install would run against a tree
   that already carries the first link, and `pnpm install --frozen-lockfile`
   reconciles `node_modules` against the lockfile, so it would remove the very
   link it was meant to validate.
2. The library is built **once** with the repository's own build target, cached
   against the library's **content hash** and rebuilt whenever that hash moves.
   Building once and reusing it would have consumers verifying against a stale
   `dist` and reporting false green.
3. The built dist is linked into the consumer's `node_modules`. Whatever was
   there is preserved: pnpm's isolated store makes `node_modules/<pkg>` a
   **symlink** into `.pnpm/…`, and its target is recorded so `--undo` re-creates
   it exactly; npm's flat layout leaves a real directory, which is moved aside.

**No `pnpm` override, alias, or `workspace:` entry is ever written**, and no
tracked file changes. `pnpm-workspace.yaml` and every `package.json` stay
byte-identical to their committed contents. WB does not run `pnpm link`: that
command writes a `link:` entry into the consumer's `package.json`, which is a
tracked file.

## `--verify`

Runs each consumer's lint and tests against the linked copy through the existing
`wb verify` profiles, constrained to a single worker:

- Go: `go test -p 1 ./...`, and **not** `-race`
- Node: `--parallel=1 -- --maxWorkers=1`, with `NX_DAEMON=false` and
  `NX_SKIP_NX_CACHE=true`; per-file isolation stays on

It reports **per consumer** and does not stop at the first failure — the point
of a stream is to learn about every consumer in one pass.

Every run prints, verbatim:

```
verified against unpublished <library> at content-hash <h> (dirty)
```

plus the links in effect and the published version each replaced, so a result
can be tied to an exact library tree after the fact.

It also runs a **`GOWORK=off` build and vet** as the pre-landing check, proving
the consumer still resolves its *published* dependency.

## `--undo`

Restores the published versions the record names and removes the links. It
succeeds **even when the library worktree has since been removed**, because the
recorded state — not the library — is the source of truth for reversal.

It also clears an **unrecorded** `go.work` it finds in a named consumer — a
hand-written one, or one left by an interrupted link. That is what makes the
command the merge guard names able to actually satisfy the guard.

A removal that **fails keeps its record**. The link is still on disk, so the
merge guard and `wb stream end` must keep refusing; clearing the record would
hide a live link from both, and an npm link has no filesystem signal to catch
it later. Fix the cause and re-run `--undo`.

`--undo` never edits a manifest, because linking never did: nothing changed a
declared version, so there is no version to write back. "Restores the published
versions" means the consumer resolves its published dependency again once the
untracked link artefacts are gone.

## Refusals

| what fired | do this |
|---|---|
| `link-not-recordable` — no open stream holds the consumer | `wb stream start` or `wb stream join` the consumer first, or pass `--stream <name>`. A link WB cannot record cannot be undone and is invisible to the merge guard, so it is refused **before** anything is written. |
| `wb worktree merge` / `wb pr land` refuses a linked worktree | run the exact `wb deps propagate local <library> --to <consumer> --undo` the refusal names |
| the library publishes no discoverable identity | fix the library's `backend/go.mod` or `libs/**/package.json`; WB will not accept a supplied name as a substitute |
| the frozen install failed | fix the consumer's lockfile (`pnpm install`, commit the lockfile) and re-run |
| linking changed a tracked file | that is a defect — report it; a local link must never change tracked config |

There is **no flag that both bypasses the merge guard and pushes**.

## Where links are recorded

Every link is recorded in stream state at the moment it is created, with the
consumer, the library, the mechanism, and the dependency version that was in
effect before linking. `wb stream status` reports them as gap one, and
`wb stream end` refuses while any remains.

## Ordering: record, then act

Every link is written to stream state **before** the filesystem is touched, and
re-written with the exact artefacts afterwards. The guard is therefore already
closed while the change is being applied, and a crash mid-apply leaves a record
`--undo` can act on.

Recording afterwards left a window in which `go.work` was on disk with nothing
recorded: the merge guard fired on the file, `--undo` reported "nothing to
undo", and the worktree could never be landed.

## Which verbs refuse a live link

Every verb that pushes, lands or absorbs work: `wb worktree merge`,
`wb worktree merge prepare`, `wb worktree merge land`, `wb worktree merge
resume`, and any later landing or absorb verb. The land verbs take a **receipt**
rather than a worktree path and resolve the worktrees to guard out of it.

The requirement is declared on the command itself, and a test walks the command
tree and drives every declaring verb, so a landing verb added later cannot
quietly skip the guard.
