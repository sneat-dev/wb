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

3. Never mutate `<projects-root>/<owner>/<repository>` to make it eligible.
   WB fetches and pins the exact remote base without switching or updating its
   current local branch, index, or working tree, including when that canonical
   checkout is already dirty or off-base.

## Create

From any checkout whose `origin` identifies the repository:

```sh
wb worktree create <task> --branch-prefix <prefix>/ \
  --model unknown \
  --original-prompt-file <private-prompt-file>
```

Prefer piping the prompt on stdin instead of staging a file:

```sh
printf '%s' "$ORIGINAL_PROMPT" | wb worktree create <task> --branch-prefix <prefix>/ \
  --model unknown \
  --original-prompt-file -
```

For one task spanning repositories, prefer one coordinated `create` call
naming every repository, not several parallel single-repository calls for the
same new task slug: `create` holds an exclusive per-task lock, so only one
concurrent invocation for a brand-new slug wins and every other one fails
clean with "claim already held by a concurrent create" rather than corrupting
shared state. A fleet that must fan out per-repository invocations anyway
should retry a losing one after the winner finishes, not treat the refusal as
fatal.

```sh
wb worktree create <task> --branch-prefix <prefix>/ \
  <owner/repository-a> <owner/repository-b> \
  --effort <stable-effort-id> --run <agent-run-id> \
  --initiator <human-or-parent-agent> --agent <agent-id> \
  --agent-runtime codex --model <exact-child-model-or-unknown> \
  --cli <invoking-cli-if-known> --provider <routing-or-billing-provider-if-known> \
  --original-prompt-file <private-prompt-file>
```

A minimal literal command suitable for an adapter fixture is:

```sh
wb worktree create fair-split acme/app --agent codex-run-1 --agent-runtime codex --model unknown --original-prompt-file <private-prompt-file>
```

With a non-default projects root, put the global option on every call:

```sh
wb --projects-root <root> worktree create <task> \
  --branch-prefix <prefix>/ <owner/repository> \
  --model unknown \
  --original-prompt-file <private-prompt-file>
```

Use the exact paths WB prints. Do not reconstruct or relocate them.

After create (or when a successor agent takes over), overview the task, inspect
identity, then dump the private journal only when the agent needs exact prompt
bodies:

```sh
wb worktree summary <task>
wb worktree info <printed-worktree-path>
wb worktree log <printed-worktree-path>
wb --projects-root <root> worktree log <printed-worktree-path> --format json
```

`summary` covers every live worktree for the task. `info` is redacted
(ordinals/digests only). `log` includes private prompt bodies for agent
bootstrap. Do not commit or publish that private output.

## Land it when ready

Creation starts the lifecycle; it does not define completion. When one or more
created worktrees are clean, validated, and compatible without a judgment call,
use the paired landing command:

```sh
wb worktree merge <printed-worktree-path...> --route auto --cleanup --progress --format json
```

This may be used many times against the same target. For a two-phase handoff,
run `wb worktree merge prepare ...` so dependent agents can consume the exact
candidate SHA, then `wb worktree merge land <receipt> ...`. Resume the receipt
after interruption; do not manually reconstruct its PR, target, or cleanup
state. See [merge.md](merge.md) and the `wb-merge` skill for the full contract.

One command covering multiple repositories is one Run. WB writes a separate
immutable claim for each repository below
`<WB_HOME>/worklogs/<effort>/runs/<run>/claims/<claim-id>.json`; claim IDs are
portable collision-resistant digests of effort, canonical repository, branch,
and immutable base; Run ID and absolute worktree path are not identity inputs.
Never emulate this with a single hand-written shared JSON file.
`--original-prompt-file` is mandatory. Prefer `--original-prompt-file -` and
pipe the exact originating request on stdin: WB reads it once, in memory, and
writes the private 0600 archive itself — no caller-managed staging file ever
exists, so a concurrent agent sharing your scratchpad directory cannot
overwrite or archive the wrong prompt. Only pass a file path when the prompt
cannot be piped; in that case use a per-invocation-unique path (never the
default `original-prompt.txt` name, which is exactly what collides when two
agents share a scratchpad). Either way the exact bytes go only into the
private archive and are intentionally absent from the Git-excluded worktree
projection and Synchestra outbox.

The dispatcher that creates a session, worktree, or successor claim must
explicitly supply the model it chose: `--model <exact-id>` or `--model
unknown`. WB rejects omission before publication and never guesses from
runtime, CLI, provider, or ambient configuration. Record `--cli` and
`--provider` only when independently known;
they are optional, independent route metadata. Provider is a routing/billing
or subscription identifier, never a credential.

## Correct an audited identity without rewriting history

Corrections target the durable claim rather than a live worktree, so they also
work after terminal cleanup. Supply a stable event ID for retry safety:

```sh
wb worktree correct-identity <effort> <run> <claim-id> \
  --event-id <stable-event-id> --actor <person-or-agent> --reason <why> \
  --model unknown
```

One-line retry-safe form: `wb worktree correct-identity <effort> <run> <claim-id> --event-id <stable-event-id> --actor <person-or-agent> --reason <why> --model unknown`.

Select only fields that change. `--cli=` or `--provider=` explicitly clears an
optional value. Do not edit private claim files: WB appends an immutable event
and offline outbox receipt, then projects the explicit predecessor chain.

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
wb worktree create <task> --resume \
  <owner/repository> --model unknown \
  --original-prompt-file <private-prompt-file>
```

WB recovers the registered branch and active Work Log claim before it consults
current naming policy. A changed repository/user prefix cannot split the task;
use exact `--branch` only to assert the recovered branch. WB preserves the
existing claim and projection. A different explicit run, agent, runtime, or
model requires an audited handoff instead of a silent reclaim. Preserve all
existing changes and inspect them before continuing.

If Work Log publication fails after Git publishes coordinated worktrees, WB
returns typed exact outcomes, writes durable cleanup receipts when possible,
rolls back every asset published by that invocation, and terminalizes written
claims append-only. If rollback cannot be proven, inspect the reported exact
path/backlog with `wb worktree list` and `wb worktree cleanup`; never guess or
delete it with raw Git. Prompt and identifier errors are rejected before
mutation.
