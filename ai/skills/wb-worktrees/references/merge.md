# Merge and land completed worktrees

Use `wb worktree land` as soon as one or more clean WB worktrees are ready and
their integration requires no behavioral judgment. It is the normal terminal
counterpart to `wb worktree create`, and a target may receive many such batches.

Trigger on requests and states such as: merge, integrate, land, finish, deliver,
ship, drain completed branches, merge to main, merge to a feature/integration
branch, push the target, open or merge a PR, wait for checks, sync/pull the
canonical clone, clean the worktree, resume a merge, or revert a bad landing.

## One-command default

```sh
wb worktree land <source-worktree...> --format json
wb worktree land <source-worktree...> --progress --format json
# Legacy spelling with explicit cleanup:
wb worktree merge <source-worktree...> --route auto --cleanup --format json
```

The default target is the repository's remote default branch. Pass
`--target <branch>` for a feature or integration target. `--route auto` uses a
direct target push only when authoritative GitHub policy supports it; otherwise
it uses a PR, or refuses an unsupported merge queue. WB derives PR text from
commit messages, waits for exact-head checks, verifies the remote landing, and
fast-forwards a clean canonical checkout already on the target.
Use `--progress` from non-terminal agent tools so stderr reports the current
stage and exact-check poll without contaminating the terminal JSON on stdout.

Cleanup defaults on for `wb worktree land`. Pass `--cleanup=false` only when
another agent still needs the proved source/candidate assets, then later run the
receipt with `merge resume ... --cleanup`. The legacy `wb worktree merge`
spelling keeps cleanup opt-in.

## wb land: the root-level alias

`wb land` is the identical root-level alias for `wb worktree land` — same
flags, same help, same exit codes, built from the same constructor, so the two
can never drift apart. Use whichever is shorter to type:

```sh
wb land <source-worktree...> --format json
```

It takes every worktree of one task IN ONE REPOSITORY in a single call — a
call refuses worktrees from more than one repository (`all source worktrees
must belong to one repository`). A task that spans several repositories
(`wb worktree create <task> owner/repo1 owner/repo2 ...`) still lands with one
call per repository. This is the fast path `wb-merge`'s Fast path section
points to; never reach for `gh pr merge` instead — see rule
land-with-wb-verb (sneat-co/backstage).

## Two phases

```sh
wb worktree merge prepare <source-worktree...> --target <branch> --progress --format json
wb worktree merge land <candidate-worktree-or-receipt> --route auto --cleanup --progress --format json
```

Prepare creates an isolated integration worktree and immutable candidate SHA,
without merging into local `main` or changing source worktrees. Other agents
can rebase onto that SHA while Phase 2 waits for GitHub.

## Recovery

```sh
wb worktree merge resume <candidate-worktree-or-receipt> --progress --format json
wb worktree merge revert <landing-receipt> --route auto --cleanup --progress --format json
wb worktree merge seal-validation-failed <validation-failed-receipt> --apply --actor <operator> --reason <reason>
```

Use the receipt's exact `resume_args`; do not reconstruct state from branch
names. Conflicts stop for judgment. A landed failure can create a forward
inverse candidate, never a reset or force-push. If post-target CI instead needs
a forward fix, keep the same source and receipt, commit the repair, and rerun
`merge prepare`; WB records the failed landing and advances the retained
candidate onto the current target before opening a new PR.

For the dedicated merger role and policy detail, read the `wb-merge` skill and
its `references/worktree-merge.md` contract.
