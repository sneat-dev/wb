# Land a pull request with wb pr land

`wb pr land <owner/repository#number>` is the one verb for "this pull request is
ready — land it". It verifies, merges, proves the merge reached the base,
deletes the branch, and retires the worktree that produced it.

Use it whenever you would otherwise run `gh pr merge`. Never run `gh pr merge`
by hand: that is the measured root cause of sixty abandoned worktrees on one
machine — the merge stage of the older verb broke on the installed `gh`, people
fell back to the raw command, and the opt-in cleanup that should have retired
the worktree never ran.

```sh
# A green dependency bump. Its worktree is retired; nothing is left behind.
wb pr land sneat-co/sneat-go#1041

# A reviewed change.
wb pr land sneat-co/sneat-go#1041 --approved-by review-sneat-go-1041.md

# Machine-readable envelope for an agent.
wb pr land sneat-co/sneat-go#1041 --format json
```

Exit codes: `0` landed · `1` the work is not ready (checks red or pending, or
the landing could not be verified) · `2` a guard refused.

## Cleanup is the default

The task's worktree is removed and its claim released as part of landing.
`--keep` is the only way to retain them, and it is for the case where the next
round of work continues in the same checkout. An opt-in cleanup is a cleanup
that does not happen.

## The squash message aggregates the branch

Squash is the default, and the message is built rather than defaulted:

- **subject** — the pull request's title. GitHub substitutes the branch's first
  commit subject when none is given, which is how a `wip(...)` message lands on
  `main` and cannot be corrected without rewriting history on a protected
  branch.
- **body** — the pull request's summary, then one line per source commit
  (`<short-sha> <subject>`, with any commit body that carries real information
  folded underneath), then the pull request number and the review that
  authorized it.

So `git log` on the default branch still answers what a change contained,
without carrying every "fix typo" commit into it.

### Keeping a commit in its own place in the history

```sh
wb pr land sneat-co/sneat-go#1041 --approved-by review.md \
  --keep-commits 4f2a1c9 --reason "the migration must be revertable on its own"
```

wb rebuilds the branch so the named commits land as their own commits, in their
original relative order, with everything else squashed into one aggregated
commit that records the reason. The rewritten SHAs are paired with their source
SHAs on the receipt, because a rebase merge rewrites every commit and that
pairing is the only way back to the originals.

- **`--reason` is mandatory.** A commit standing alone in the history of a
  default branch has to say why it is there.
- **Each kept commit must build on its own.** One that does not is refused,
  naming a smaller `--keep-commits` set — a commit that does not build is not a
  place anyone can bisect to, so promoting it is worse than aggregating it.

## Review: what needs one, decided from the diff

A **mechanical bump** — a diff touching only `go.mod`, `go.sum`, `package.json`
dependency fields, `pnpm-lock.yaml`, `pnpm-workspace.yaml` — lands on its batch
verification with no review ledger entry.

Anything else needs `--approved-by <review-file-or-comment-url>`, which is
recorded on the receipt and in the aggregated commit body.

The classification is made **from the diff's content**, never from filenames and
never from the title, author or labels:

- `package.json` counts only when **every changed line** is a dependency entry
  inside `dependencies`, `devDependencies`, `peerDependencies` or
  `optionalDependencies`. A `scripts` edit is a change to what CI runs; a
  `pnpm.overrides` edit rewrites the resolved graph for every package in the
  workspace. Neither is a version bump, and neither lands unreviewed.
- a manifest under `testdata/`, `docs/`, `examples/` or `fixtures/` is a
  fixture, so it is code.
- `go.mod`, `go.sum` and the lockfiles are mechanical **only when they are the
  only files changed**.
- a file GitHub could not diff cannot be classified, so it is not mechanical.
  Absence of evidence is not evidence that a change is safe to land unreviewed.

A pull request titled as a Renovate bump whose diff also edits a `.go` or `.ts`
file is not mechanical, and is refused until a review is recorded.

## Refusals and what resolves each

| Refusal code | What it means | Sanctioned next step |
| --- | --- | --- |
| `unapproved-patch-set` | the diff is not a mechanical bump | `wb pr land … --approved-by <review-file-or-comment-url>` |
| `draft-pull-request` | landing a draft would bypass the review it is waiting for | `gh pr ready <n> --repo <repo>` |
| `pull-request-not-open` | already merged, or closed | `wb worktree gc --apply` when it is merged; open it in the browser otherwise |
| `not-mergeable` | GitHub reports a conflict | `wb worktree merge <task> --route auto` |
| `head-moved` | the branch moved after its checks were observed | rerun `wb pr land …`; the head SHA is sent as a lease, so a race cannot land an unverified head |
| `target-has-no-strict-fence` | the target branch has no server-enforced strict up-to-date policy, so green checks prove the head was green, not that it still is | `wb pr land … --allow-unfenced`, recorded on the receipt |
| `keep-commits-without-reason` | `--keep-commits` with no justification | add `--reason "<why these commits stand alone>"` |
| `keep-commit-not-on-branch` | a named commit is not on this branch | name a commit of the branch being landed |
| `kept-commit-does-not-build` | a kept commit does not build on its own | `--keep-commits` with a smaller set |
| `checks-pending` / `checks-failed` | exit 1, not a refusal: the work is not ready | fix the failure, or rerun to keep waiting |
| `cleanup-blocked-dirty` | the worktree that produced the branch has uncommitted changes, so landing would merge the work and then be unable to retire the checkout | `wb worktree end <task>`, or land with `--keep` |
| `cleanup-blocked-live-link` | a worktree still holds a live local dependency link | `wb deps propagate local … --undo`, or land with `--keep` |

`wb pr land` is on WB's **landing surface**: before any GitHub call it refuses
when an open stream worktree of the repository still builds against an
unpublished tree. Landing then would publish a commit whose CI ran against
something the registry never carried.

Both of those are checked **before** the merge, while refusing is still free.
Discovering afterwards that the checkout cannot be retired leaves the landing
done and the tidy-up impossible — which is the shape that produced sixty
abandoned checkouts.

## What it records

Every invocation writes **one stream event** — success, findings or refusal, and
including `--keep` — carrying the pull request, the head, the mechanical
verdict, the approval reference, the kept commits and `saved_tool_calls`. A
refused invocation records **zero** saved calls: it did none of your work.

`--format json` emits the envelope: `outcome`, `refusal_code`,
`sanctioned_command`, the evidence it relied on, the changed files and the
classification made from them, the source-to-landed commit pairing, the cleaned
tasks, and `saved_tool_calls` / `saved_tokens_est` with the manual sequence it
replaced. In interactive mode the same figures appear as a footer line, labelled
an estimate; `--non-interactive` suppresses the footer and keeps the fields.

## Lane contract

Consume a library through `wb deps propagate local`; the orchestrator runs
`remote` at the end. End with `wb worktree end` — or let `wb pr land` end it for
you, which is the point.
