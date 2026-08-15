# Inspect and clean worktree tasks

## Inspect

Use the offline default for a fast view of every WB-managed worktree:

```sh
wb worktree list
wb worktree list <task> --format json
wb --filter acme worktree list --format json
wb worktree list fair-split --format json
```

Add GitHub PR evidence only when it affects the decision:

```sh
wb worktree list <task> --github
```

Do not replace this with recursive Git loops. WB validates that each path is a
real linked worktree belonging to its expected canonical clone, stops at Git
repository boundaries, and reports malformed candidates without hiding valid
siblings. Default-mode inventory includes legacy `<projects-root>/.wb`
worktrees during migration. JSON is a versioned control-plane envelope: inspect
`results`, `diagnostics`, and `artifacts`. An artifact-only task can still be
cleanup backlog even when `results` is empty. This replaces the legacy bare
JSON array; migrate consumers to the envelope and check `schema_version`.

This is live linked-checkout inventory only. The approved seven-day view that
joins archived Work Logs into active/recent/history is planned and is not
advertised by the current command.

Treat a `merged` result as cleanup-ready, not done. A feature is done only
after it is merged to `main` **and** every related worktree/branch has an
applied removal or audited recycle. A task has the same rule after integration
to its feature branch. A validated or pushed branch is never done.

## Plan cleanup

After all coordinated PRs merge, always inspect the dry run:

```sh
wb worktree cleanup <task>
wb worktree cleanup --all-merged
wb worktree cleanup fair-split
```

A specifically named task defaults to immediate eligibility (`--older-than 0`)
because this is its terminalization journey. `--all-merged` fleet sweeping
keeps the 24-hour grace window unless explicitly overridden.

WB skips the whole task if any member is locked, dirty, has an open PR, is not
integrated into the freshly fetched exact `origin/<target>`, or has an advanced
remote branch. A matching merged PR supplies merge-age evidence; a verified
direct push to the target is also eligible. A local-only merge is
`awaiting_push`. Preserve skipped work and report the reason; do not reset,
clean, stash, or delete it manually.

Work absorbed into a differently named integration branch — the batching a
target requiring linear history forces — is eligible too, because the branch
name is not the evidence. WB reads the merged pull request GitHub associates
with the branch's own immutable head commit, then proves containment locally:
merging the branch into the landing commit must add nothing to it, and merging
it into the fetched target must add nothing there. Work that landed and was
later reverted, or landed only in part, stays `awaiting_push`.

`--absorbed-by <pr|commit>` names the receipt to verify when the batch
cherry-picked rather than merged the branch, so GitHub associates nothing. It
selects which receipt to check and never replaces one; the named commit must
also be exactly where the work entered the target. A pointer that fails
verification refuses only its own candidate and says which check refused it.

## Apply

Apply only after reading the plan:

```sh
wb worktree cleanup <task> --apply --remote --older-than 0
wb worktree cleanup fair-split --apply --remote --older-than 0
```

For a named terminal task, `--apply` refuses without `--remote`. WB removes the
linked worktree and exact local branch, then retires an existing remote source
branch with force-with-lease against the previously observed SHA. It rechecks
safety immediately before mutation and writes an audit report below the
authoritative WB home, normally `~/.wb/reports/worktree-cleanup/`. If another
actor still needs the source branch, the effort is not done; hand it off rather
than terminalizing it.

WB also persists a private recovery stage before removing each worktree. If a
process stops after the checkout disappears but before the exact local branch
is deleted, rerun the same named `cleanup ... --apply --remote` command. Its
dry run exposes the durable cleanup backlog and apply corroborates that the
worktree path/registration and remote branch are absent and that the local ref
still has the recorded SHA before finishing it. Never delete that orphan ref
manually.

Cleanup also classifies reserved `.wb-stage-*` and `.wb-retired-stage-*`
control-plane entries instead of treating them as legacy repository
worktrees. The dry run reports an exact disposition. Apply descriptor-safely
archives a recognized empty stage outside the active task; a non-empty,
symlinked, or invalid stage remains explicit blocking cleanup backlog. Never
delete or move that evidence manually.

## Recycle only explicit caches

Recycling is an optimization, never a way to hide unfinished work. First use
the normal cleanup path when the work is merged. To reuse a clean, unlocked
worktree for a new task, plan then apply a rename:

```sh
wb worktree rename <finished-task> <next-task> --preserve-cache node_modules
wb worktree rename finished-task next-task --preserve-cache node_modules
wb worktree rename finished-task next-task --branch-prefix feature/ --preserve-cache node_modules
wb worktree rename <finished-task> <next-task> --apply --remote \
  --preserve-cache node_modules --effort <new-effort> --run <new-run> \
  --agent <new-agent> --agent-runtime <runtime> --model <model> \
  --original-prompt-file <private-prompt-file>
```

`--preserve-cache` is an allow-list. Any other ignored/untracked path blocks
recycle with a precise diagnostic; archive or intentionally remove it outside
the worktree before retrying. WB seals the old local Work Log/outbox before
the move, resets the Git-excluded projection, and creates a fresh run claim
after fetching the new base. Apply requires `--remote`; WB retires any exact
unchanged old remote source branch with force-with-lease and rolls every
already-moved repository back if a later repository fails. Never copy a prior
task's projection, prompt, or source state into the new task.

## Abort instead of abandoning

An unused or interrupted worktree has no merged PR, so `cleanup` must refuse
it. Do not delete it manually. Inspect an explicit disposition first:

```sh
wb worktree abort <task> --disposition handoff --successor <agent-or-session> \
  --model <exact-successor-model-or-unknown>
wb worktree abort <task> --disposition not_landed --successor <agent-or-session> \
  --model <exact-successor-model-or-unknown> --cli <invoking-cli-if-known> \
  --provider <routing-or-billing-provider-if-known> --apply
wb worktree abort <task> --disposition discarded --apply --remote
wb worktree abort fair-split --disposition handoff --successor codex-run-2 --model unknown
wb worktree abort fair-split --disposition discarded --apply --remote
```

Applied `handoff` and `not_landed` require exactly one successor and its exact
model or explicit `unknown` before publication. Pass independently known CLI
and commercial route identifiers separately (for example `--cli opencode
--provider opencode-go`); never pass credentials. WB terminalizes the old
claim, creates one deterministic active successor claim without inheriting its
execution route, and keeps even a dirty worktree/branch resumable.
`discarded --apply --remote` is the explicit
authorization to seal first, retire an exact unchanged remote source branch,
then remove a clean unlocked worktree and its exact local branch. WB repeats
the clean/head/registration checks at the removal boundary; a concurrent write
makes it refuse.
If discard is interrupted after the worktree disappears, rerun the same
`abort ... --disposition discarded --apply --remote` command; WB resumes only
the exact journaled local ref. Run `wb worktree list` and a final cleanup/abort
dry run afterwards and resolve every live entry or durable backlog record. The
normal terminal state is zero cleanup backlog, not apparently-finished branches.

## Planned coordination surfaces (not commands)

Portable merger-agent adapters, plan-overlap and migration-scope detection,
hourly/target-change refresh notification, distributed Synchestra fencing, and
Git-repository communication fallback are planned. So are the full `worktree
log` init/checkpoint/refresh/integrate/handoff/recover/finalize/sync/archive
group and authorized encrypted private-prompt export. The current WB CLI does
not implement or advertise those mutating coordination verbs.
`wb worktree log` is the shipped read-only agent bootstrap dump of the
local journal and original prompt. Later mutating verbs under this command
remain planned. Its private local outbox is durable recovery evidence during
server downtime; it is not an inter-agent Git transport.
