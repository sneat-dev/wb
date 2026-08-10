# Create or resume a worktree

## Before creation

1. Identify every repository that will be edited.
2. Check for an existing relevant PR and the exact remote branch before making
   a duplicate:

   ```sh
   gh pr list --repo <owner/repository> --state open \
     --json number,title,headRefName,url
   git ls-remote --heads origin <branch>
   ```

3. Keep `<projects-root>/<owner>/<repository>` clean and on the base branch.
   WB performs `git pull --ff-only --no-tags origin <base>` before branching.

## Create

From any checkout whose `origin` identifies the repository:

```sh
wb worktree create <task> --branch <prefix>/<task>
```

For one task spanning repositories:

```sh
wb worktree create <task> --branch <prefix>/<task> \
  <owner/repository-a> <owner/repository-b> \
  --effort <stable-effort-id> --run <agent-run-id> \
  --initiator <human-or-parent-agent> --agent <agent-id> \
  --agent-runtime codex|claude --model <model-id> \
  --original-prompt-file <private-prompt-file>
```

With a non-default projects root, put the global option on every call:

```sh
wb --projects-root <root> worktree create <task> \
  --branch <prefix>/<task> <owner/repository>
```

Use the exact paths WB prints. Do not reconstruct or relocate them.

One command covering multiple repositories is one Run. WB writes a separate
immutable claim for each repository below
`<WB_HOME>/worklogs/<effort>/runs/<run>/claims/`; never emulate this with a
single hand-written shared JSON file. The original-prompt option copies the
file into that private archive only. It is intentionally absent from the
Git-excluded worktree projection and Synchestra outbox.

By default the printed path is below `~/.wb/worktrees`. A populated historic
`<projects-root>/.wb` is never a create target. If an old managed hook exists,
WB refreshes its home semantics before creation or fails before creating a
mixed-layout checkout.

## Resume

Use `--resume` only when the open work belongs to this exact task and branch:

```sh
wb worktree create <task> --branch <prefix>/<task> --resume \
  <owner/repository>
```

WB validates the expected canonical clone, branch, and linked path. Preserve
all existing changes and inspect them before continuing.

If creation reports failure, run `git worktree list --porcelain` and inspect
the expected path before retrying. A post-checkout hook can fail after Git has
already created or switched the worktree.
