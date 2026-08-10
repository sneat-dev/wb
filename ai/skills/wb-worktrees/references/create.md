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

3. Keep `<projects-root>/<owner>/<repository>` clean. WB fetches and pins the
   exact remote base without switching or updating its current local branch.

## Create

From any checkout whose `origin` identifies the repository:

```sh
wb worktree create <task> --branch-prefix <prefix>/ \
  --original-prompt-file <private-prompt-file>
```

For one task spanning repositories:

```sh
wb worktree create <task> --branch-prefix <prefix>/ \
  <owner/repository-a> <owner/repository-b> \
  --effort <stable-effort-id> --run <agent-run-id> \
  --initiator <human-or-parent-agent> --agent <agent-id> \
  --agent-runtime codex --model <model-id> \
  --original-prompt-file <private-prompt-file>
```

A minimal literal command suitable for an adapter fixture is:

```sh
wb worktree create fair-split acme/app --agent codex-run-1 --agent-runtime codex --original-prompt-file <private-prompt-file>
```

With a non-default projects root, put the global option on every call:

```sh
wb --projects-root <root> worktree create <task> \
  --branch-prefix <prefix>/ <owner/repository> \
  --original-prompt-file <private-prompt-file>
```

Use the exact paths WB prints. Do not reconstruct or relocate them.

One command covering multiple repositories is one Run. WB writes a separate
immutable claim for each repository below
`<WB_HOME>/worklogs/<effort>/runs/<run>/claims/<claim-id>.json`; claim IDs are
portable collision-resistant digests of effort, canonical repository, branch,
and immutable base; Run ID and absolute worktree path are not identity inputs.
Never emulate this with a single hand-written shared JSON file. Before running
create, write the exact originating request to a readable non-empty 0600 file
outside source Git. `--original-prompt-file` is mandatory and copies those
exact bytes into the private archive only. They are intentionally absent from the
Git-excluded worktree projection and Synchestra outbox.

By default the printed path is below `~/.wb/worktrees`. A populated historic
`<projects-root>/.wb` is never a create target. If an old managed hook exists,
WB refreshes its home semantics before creation or fails before creating a
mixed-layout checkout.

## Branch policy

Without an exact branch or prefix, WB uses `<task>` itself. The precedence is:
exact `--branch`; CLI `--branch-prefix` (an explicit empty prefix means no
prefix); repository `.wb/worktrees.yaml` from the exact fetched target-base
commit; user `$XDG_CONFIG_HOME/wb/worktrees.yaml` or
`~/.config/wb/worktrees.yaml`; then the task. Repository policy never comes
from whichever branch the canonical checkout currently shows. A policy is:

```yaml
version: 1
worktrees:
  branch_prefix: feature/
```

Do not use harness names in branch spelling. Work Logs carry runtime and model
provenance. `--branch` and `--branch-prefix` together, including an explicitly
empty `--branch`, are rejected before fetch or worktree mutation.

## Resume

Use `--resume` only when the open work belongs to this exact task and branch:

```sh
wb worktree create <task> --branch-prefix <prefix>/ --resume \
  <owner/repository> --original-prompt-file <private-prompt-file>
```

WB validates the expected canonical clone, branch, and linked path. Preserve
all existing changes and inspect them before continuing.

If creation reports failure, run `git worktree list --porcelain` and inspect
the expected path and Work Log report before retrying. Prompt and identifier
errors are rejected before mutation; a rare storage failure after Git publishes
a worktree is currently reported as partial state rather than auto-deleted.
