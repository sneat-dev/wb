---
name: wb-worktrees
description: Create or resume Git feature branches through the WB CLI in isolated central worktrees while keeping canonical clones clean and current. Use before creating a branch, starting code changes, coordinating the same task across repositories, moving development out of a canonical clone, or checking whether an agent is allowed to edit the current checkout.
metadata:
  author: sneat-dev
  version: "1.0"
---

# WB worktrees

Requires Git and a WB build that provides `wb worktree create` and
`wb worktree guard`.

Keep `<projects-root>/<owner>/<repository>` as the canonical clone: clean,
checked out on `main`, and synchronized with `origin/main`. Do feature work
only in a linked checkout created below:

```text
<projects-root>/.wb/worktrees/<task>/<owner>/<repository>
```

Use WB for branch creation and policy checks. Do not substitute raw
`git switch -c`, `git checkout -b`, or `git worktree add`.

## Before changing files

1. Verify the required WB surface:

   ```sh
   wb worktree --help
   ```

   If it is unavailable, stop and ask for WB to be installed or updated.
   Never fall back to changing branches in the canonical clone.

2. Inspect the current checkout:

   ```sh
   wb worktree guard .
   git status --short --branch
   ```

   A passing canonical checkout is safe for synchronization and inspection,
   not for editing. A passing linked checkout is safe for feature work.

3. Choose a short task slug that is stable across all participating
   repositories. Use a harness-specific branch prefix:

   - Codex: `codex/<task>`
   - Claude Code: `claude/<task>`
   - Other or unknown harness: `agent/<task>`

4. Create the checkout. From a repository, WB can derive its `owner/name`
   from `origin`:

   ```sh
   wb worktree create <task> --branch <prefix>/<task>
   ```

   For a coordinated change, create every checkout in one command:

   ```sh
   wb worktree create <task> \
     --branch <prefix>/<task> \
     sneat-co/sneat-bots sneat-co/sneat-go
   ```

   WB refuses to branch until every canonical clone is clean, on `main`, and
   updated from `origin/main` with a fast-forward-only pull.

5. Change directory to the exact worktree path printed by WB and verify it
   before editing:

   ```sh
   wb worktree guard <printed-worktree-path>
   git -C <printed-worktree-path> status --short --branch
   ```

## Existing work

Use `--resume` only when continuing the exact task and branch:

```sh
wb worktree create <task> \
  --branch <prefix>/<task> \
  --resume \
  <owner>/<repository>
```

Before resuming, inspect the reported path and branch. WB validates that the
existing checkout belongs to the expected canonical clone and refuses to
silently reuse a different branch. Preserve all existing changes.

## Unsafe canonical state

If WB reports that a canonical clone is dirty or on a feature branch:

- Do not reset, clean, stash, switch, or overwrite it automatically.
- Inspect and report the exact state to the user.
- If the changes belong to the current task, move them only with an explicit,
  preservation-safe plan.
- If another task or person owns the changes, leave them untouched.

## Hooks

Check that WB-managed hooks are installed:

```sh
wb hooks check .
```

When the repository policy includes the built-in `worktree` profile, use WB
to install or repair it:

```sh
wb hooks install .
```

The profile checks `post-checkout`, `pre-commit`, and `pre-push`. Git has no
pre-checkout hook, so a rejected `post-checkout` means the branch switch
already occurred: stop, preserve any work, and return the canonical clone to
`main`. Never treat the failed checkout command as proof that nothing changed.

## Cross-repository testing

Create worktrees for every repository that needs modifications. A read-only
sibling used only for integration testing may remain the clean canonical
`main` checkout after WB or the test runner synchronizes it. If the sibling
needs even one file change, create it under the same task operation instead.

Keep one task slug across paired pull requests so their local paths and
branches are easy to correlate.

## Non-negotiable rules

- Never develop directly in a canonical clone.
- Never create a feature branch by changing the canonical clone's branch.
- Never bypass a WB guard or hook with `--no-verify`.
- Never force-remove a worktree that contains changes.
- Never guess that an existing branch is safe to resume.
- Leave completed worktrees in place until their owner explicitly requests
  cleanup.
