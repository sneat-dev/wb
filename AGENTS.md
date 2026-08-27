# Agent instructions

Read this before modifying anything in this repository. It is also read by
Claude Code (as `CLAUDE.md`'s equivalent for parent-directory discovery, via
the symlink) and by Codex.

## 1. Find out where you are before you write

Every checkout WB manages carries a generated `.worktree.md` at its root.
**Read it before your first write.** It is one file and it answers the only
question that matters first: is this checkout one you may write to?

```
---
wb_checkout: 1
kind: canonical | worktree
writable: false | true
repository: "owner/name"
checkout_path: "…"
canonical_path: "…"
branch: "…"
base_branch: "main"
task: "…"            # worktrees only
worktrees_root: "…"  # worktrees only
generated_by: "wb vX.Y.Z"
generated_at: "…"
---
```

- **`writable: true` (`kind: worktree`)** — this is an isolated linked
  worktree. Work here. Edit, commit, and push from this path.
- **`writable: false` (`kind: canonical`)** — this is the shared canonical
  clone that every worktree in the fleet is cut from. It must stay clean and
  stay on its base branch. Read it, `git fetch` it, `git merge --ff-only` it —
  and write nothing. To do work, run the command the file names:

  ```sh
  wb worktree create <task> <owner/repository>
  ```

  Then work in the printed path.

- **The file is absent** — an older clone, a fresh manual clone, or a
  repository WB has never touched. **This does not mean the checkout is safe.**
  Treat the location as unknown and establish it before writing:

  ```sh
  wb worktree guard .
  ```

  `wb worktree marker .` then writes the missing `.worktree.md`.

`.worktree.md` is generated, untracked, and git-ignored on purpose — that is
how it can say all this without making a canonical clone dirty. **Never commit
it.** WB refreshes it on clone, on `wb worktree create`, and on `wb sync`, and
`wb worktree marker --fleet` refreshes every checkout on demand.

## 2. Why the canonical clone matters this much

Uncommitted work left in a canonical clone is invisible to WB and one routine
checkout away from being destroyed. On 2026-08-27 a `git checkout origin/main
-- .` run to read a single file staged 186 files against a stale HEAD, and a
generator run in the wrong directory left a finished, unlanded document sitting
untracked where the next checkout would have taken it.

If you find a canonical clone already dirty, **do not** reset, clean, stash, or
check out over it. Move the content onto a branch first:

```sh
wb worktree rescue <path>
```

## 3. Working in this repository

- Build: `go build ./...`  ·  Test: `go test ./...`  ·  Lint: `golangci-lint run`
- Total statement coverage must stay at or above the floor enforced in
  `.github/workflows/go-ci.yml`. Do not reduce approved scope to satisfy it —
  say so instead.
- Every public command leaf needs a matching row in `ai/capabilities.json` and
  a line in `docs/cli-flag-matrix.md`; `cmd/wb/skills_test.go` enforces both.
- Persistent flags a command ignores are rejected, not silently accepted. Add
  the command to `persistentFlagSupport` in `cmd/wb/main.go` when it genuinely
  consumes one.
- Exit codes are contract: `0` success, `1` findings, `2` usage. Nothing on the
  agent-hook path may ever reach exit 2 — see `cmd/wb/hooks_agent.go`.
