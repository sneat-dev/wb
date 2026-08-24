---
name: wb-worktrees
description: Use WB to create, guard, resume, and inspect isolated task worktrees, and to safely clean up leftover worktrees and branches — for one finished task or as a historic, fleet-wide sweep across every repository. Use when asked to clean up branches, delete merged branches, prune remote branches, remove stale or leftover worktrees, tidy historic leftovers, or audit repository hygiene, and also before editing or branching, when coordinating repositories, when checking task state, after pull requests merge, or when recovering from an unsafe checkout. Never hand-roll `git branch -d`, `git branch --merged`, `git worktree remove`, or `git push --delete` branch or worktree sweeps.
---

# WB worktrees

Keep canonical clones clean and available for synchronization when possible;
prefer `main`, but never mutate a dirty or off-base canonical checkout to make
it eligible. WB creation leaves its current branch, index, and working tree
untouched while it fetches and pins the requested remote base.
Make feature changes only below the authoritative WB home:

```txt
~/.wb/worktrees/<task>/<owner>/<repository>
```

Set `WB_HOME` only when an explicit isolated home is intended. New work never
falls back to `<projects-root>/.wb`; without an explicit override, WB still
recognizes legacy linked worktrees there during migration.

## Route

- Read [create.md](references/create.md) to start or resume a task.
- Read [guard.md](references/guard.md) to validate or recover a checkout.
- Read [cleanup.md](references/cleanup.md) for ANY hygiene request — deleting
  merged branches, removing stale or leftover worktrees, or sweeping historic
  leftovers across the whole fleet. Start there before touching raw Git. If
  the branch in question has no worktree, it hands you to the `wb-branches`
  skill (`wb branch list` / `wb branch cleanup`) instead.
- Read [lifecycle.md](references/lifecycle.md) to inspect live tasks,
  finalize merged work, recycle caches safely, or abort interrupted claims.
- Read [worklog.md](references/worklog.md) for local work-log mutating verbs
  (checkpoint, steer, refresh, handoff, recover, finalize, sync).
- Read [ownership.md](references/ownership.md) to register a session
  (`wb session register`), attach an agent to a worktree, inspect owner
  metadata and PID liveness, or triage active/orphaned worktrees.
- Consult [capabilities.json](../../capabilities.json) before assuming a WB
  surface exists; execute only commands whose runtime evidence is present.
- Use `$wb-change` when the task spans implementation, hooks, tests, and PRs.

## Clean up branches and worktrees

WB retires a worktree and its branch — local and remote — in one audited
transaction, so branch hygiene is a WB command, never a raw-Git loop. These are
the exact commands; [cleanup.md](references/cleanup.md) has the decision table,
the full flag surface, and how to read a skip reason.

```sh
wb worktree orphans
wb worktree orphans --only remove
wb worktree backfill
wb worktree backfill --apply
wb worktree list
wb worktree list --only active
wb worktree list --only orphaned
wb worktree cleanup <task>
wb worktree cleanup --all-merged
wb worktree cleanup <task> --apply --remote --older-than 0
wb worktree abort <task> --disposition discarded --apply --remote
```

`wb worktree orphans` is the widest read-only triage WB has: every linked
worktree reachable from the projects root, across all three layout
generations, grouped by root effort with an explicit disposition and evidence.
Start a historic sweep there, not with `wb worktree cleanup`.

Four traps decide whether a sweep finds anything at all:

1. **`cleanup` only considers WB-managed tasks.** A worktree created by
   `git worktree add`, or one predating WB, is not skipped with a reason — it
   is outside the sweep entirely. Run `wb worktree backfill` first so it has a
   reconstructed manifest. A branch with no worktree at all is never in scope
   here — use `wb branch list` / `wb branch cleanup` (the `wb-branches` skill)
   instead.
2. **Eligibility needs a merge receipt**, not merged-looking content: the head
   is an ancestor of the freshly fetched exact `origin/<base>`, or GitHub's
   commit-to-PR index names a merged PR whose landing WB then proves locally.
   A squash-merged or cherry-picked branch whose patches are all upstream by
   patch-id is **not** a candidate, and `git cherry` emptiness is not evidence
   WB accepts — it cannot tell landed from landed-then-reverted.
3. **`--base` defaults to `main`.** A task branched from a feature branch stays
   `awaiting_push` until you pass `--base <feature-branch>`.
4. **Dry run is the default.** `--apply` is required to act, and for a named
   task `--apply` refuses without `--remote`.

Flags on `wb worktree cleanup`: `--base`, `--all-merged`, `--parallel`,
`--verbose`, `--apply`,
`--remote`, `--older-than`, `--report-dir`, `--absorbed-by`,
`--resume-interrupted`, `--format`, plus root `--filter` and
`--projects-root`. Apply writes durable audit evidence below
`<wb-home>/reports/worktree-cleanup/`.

Never delete a branch or worktree WB refused. A skip is preserved evidence —
`awaiting push`, an open PR, local changes, or a blocked coordinated sibling —
and the fix is to land, close, or decide, not to force the sweep through.

## Fast path

```sh
wb worktree guard .
wb worktree create <task> --branch <prefix>/<task> <owner>/<repository> \
  --effort <effort> --run <run> --agent <agent> --agent-runtime <runtime> --model <exact-child-model-or-unknown> \
  --cli <invoking-cli-if-known> --provider <routing-or-billing-provider-if-known> \
  --original-prompt-file <private-prompt-file>
wb worktree summary <task>
wb worktree info <printed-worktree-path>
wb worktree log <printed-worktree-path>
wb worktree guard <printed-worktree-path>
wb worktree list <task>
```

With no prefix, WB uses the task slug itself as the branch name. Use
`--branch-prefix <team-or-workflow>/` for one invocation, or configure a user
or repository policy; use `--branch` only for an exact pre-agreed branch.
Branch names are not agent provenance — use the Work Log for runtime and model
identity. Use one task slug and one creation command for a coordinated
multi-repository change.

WB fetches and pins `origin/main` before branching without switching,
fast-forwarding, staging, or otherwise changing the canonical checkout,
including when it is dirty or off-base. If a repository only supplies read-only
integration-test input, its clean, freshly synchronized canonical checkout may
be used. Create a worktree as soon as that repository needs a modification.

Before create, write the exact originating request to a readable non-empty
0600 private file outside source Git. Every create requires that file and has
a private Hybrid Work Log. Its per-repository claim is under
`<WB_HOME>/worklogs/<effort>/runs/<run>/claims/`, while the tiny local
`.wb-worklog/recovery.json` projection has no prompt/history and its
`/.wb-worklog/` directory is locally Git-excluded. Pass
`--original-prompt-file` with the exact private original prompt; never put
prompt text in a repository or command argument. The local outbox preserves
recovery evidence while Synchestra is unavailable, so local create, seal, and
cleanup do not wait for a server. It is not the planned Git-repository
communication fallback and does not deliver messages to agents.

To inspect a checkout without leaking private prompts:

```sh
wb worktree info .
wb worktree info . --format json
```

That summary includes claim identity, prompt ordinals/digests, and live Git
state, plus append-only owner metadata and live PID status. Prompt bodies stay
omitted. See [ownership.md](references/ownership.md) before treating a dead PID
as authority to discard work.

To overview every live worktree for one task/effort:

```sh
wb worktree summary <task>
wb worktree summary <task> --github
```

To resume or bootstrap an agent on an existing worktree, dump the private
journal (exact original prompt, later steering instructions, claim identity,
and live Git evidence):

```sh
wb worktree log .
wb worktree log . --format json
wb worktree log show .
wb worktree log checkpoint . --message progress
```

Do not commit that output or paste it into public reports. See
[worklog.md](references/worklog.md) for the mutating verb group.

The dispatcher/session/worktree/successor-claim creator must pass the exact
child `--model` it selected, or the literal `unknown`; omission is rejected
before WB publishes a worktree or claim. Never infer it from a harness, CLI,
environment, or provider. Also pass `--cli` and `--provider` independently when known. `cli`
names the invoking client (for example `codex` or `opencode`); `provider` is
only the routing/billing/subscription identifier and must never be a token or
credential. A direct API call may omit `cli`.

One agent is the concurrent writer for each worktree/branch. Helpers are
read-only and return patches/findings. Inspect upstream state explicitly and
never auto-stash, reset, rebase, merge, or rewrite dirty work. Do not invent a
command absent from the capability manifest and built-in help.

Never substitute `git switch -c`, `git checkout -b`, or `git worktree add`.
Never reset, clean, stash, bypass hooks, or overwrite work to satisfy a guard.
If a WB command fails, inspect state before retrying; a hook may reject an
operation after Git has already changed state.
