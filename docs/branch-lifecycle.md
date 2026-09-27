# Branch lifecycle with WB

Use the branch name in `--base` or `--target` deliberately. In the examples below, `cov/integration` is the current campaign target; substitute the target of your own work. A target fetched **into** a feature worktree updates that feature's starting point. A feature landed **onto** a target changes the remote target. These are different operations.

This is a command reference, not an end-to-end verified runbook. The command forms were checked against installed `wb ... --help` and the implementation. The complete example below was **not executed** as a sequence for this documentation change. In particular, `wb worktree log integrate` was verified from help and source, but was not exercised in this campaign. Use the receipt and exact remote checks described below before calling any landing complete.

| Need | Command | Direction and result |
| --- | --- | --- |
| Observe target | `wb worktree log refresh . --base cov/integration --format json` | Remote target → Work Log evidence; feature `HEAD` unchanged. |
| Update feature | `wb worktree log integrate . --base cov/integration --strategy auto --format json` | Remote target → local feature history. |
| Finish managed work | `wb worktree land <source-worktree> --target cov/integration --route auto --progress --format json` | Feature → remote target, exact checks, receipt, and cleanup. |
| Split preparation and landing | `wb worktree merge prepare <source-worktree> --target cov/integration --format json`; then `wb worktree merge land <candidate-worktree-or-receipt> --route auto --cleanup --format json` | Feature → local candidate first; candidate → remote target later. |
| Open a PR first | `wb pr create <worktree> --base cov/integration --format json`; later `wb pr land <owner/repo#number> --format json` | Feature → remote PR branch first; reviewed PR → remote target later. |

## Get the target current

From a managed feature worktree, use `wb worktree log refresh . --base <target>` to fetch and measure without changing its files or `HEAD`. A successful command exit is not enough: check the JSON target SHA and notes for a real fetch. This is the narrow WB command for this situation. `wb worktree guard .` validates that the feature checkout is managed; it is not a pull.

For a canonical checkout already **on** its target branch, first inspect `git status --short --branch`, then run `wb worktree guard . --base <target>` for a fresh remote comparison. If the checkout is clean and you intentionally want its local target branch advanced, `git pull --ff-only origin <target>` fast-forwards it or refuses; it does not integrate target into a separate feature. A plain `git fetch origin <target>` fetches target data without advancing a checked-out branch. `wb sync --dry-run` / `wb sync --filter <repo>` is a fleet reconciliation path for canonical clones; it is not a substitute for selecting a non-default campaign target. Do not use a pull on a dirty or off-target canonical checkout to make it appear ready.

To integrate the target **into the feature**, save a clean commit, run `wb worktree log checkpoint . --message "ready to integrate" --skip-remote` if a clean checkpoint is not already recorded, then run `wb worktree log integrate . --base <target> --strategy auto`. `auto` rebases an unpublished feature and merges into a published feature; choose `--strategy merge` explicitly when preserving published commit IDs matters. The command fetches the target again, requires a clean checkout and prior clean checkpoint, and records a conflict if integration fails. Inspect `applied`, `event.result`, and `notes`, then resolve the conflict with the Work Log recovery guidance. Revalidate the feature after successful integration. A checkpoint ref, even if pushed without `--skip-remote`, only preserves work; it is never a landing receipt.

## Deliver the feature onto the target

Use `wb worktree land` for a completed, clean WB source. Pass all worktrees from one task **in the same repository** in one call; a cross-repository task needs one land call per repository. One landing owner drives each repository and target branch. `--route auto` uses a direct push only when GitHub policy permits it; otherwise it uses a PR or refuses an unsupported policy. Use `--route pr` when a PR is explicitly required. The target is the remote default branch unless `--target <branch>` is given, so campaign branches must be named. WB never force-pushes a landing. `--cleanup=false` deliberately retains proved sources for later receipt-based cleanup.

`wb worktree merge prepare` is useful when another worker needs a validated local candidate SHA before remote landing. Its receipt is **prepared**, not delivered. `wb worktree merge land` or `wb worktree merge resume` continues that receipt. For one worktree whose PR should be visible first, `wb pr create` is a shorter push-and-open path: it requires a clean branch with commits ahead of its base, unless one of its explicit commit options is used. It performs no local suite on its own; CI is the gate. `wb pr create --land` also invokes the PR landing journey. For a non-mechanical PR, `wb pr land` requires recorded review via `--approved-by`; follow its help for reviewer identity or review-file forms. A PR's existing base controls its target; `wb pr land` does not take `--target`.

If checks are still pending, keep the receipt and run its printed `resume_args`, normally `wb worktree merge resume <candidate-worktree-or-receipt> --progress --format json`. This revalidates the exact candidate and continues check observation, target proof, synchronization, and optional cleanup. For an open PR without a merge receipt, rerun `wb pr land <owner/repo#number>`; `wb wait pr <owner/repo#number> --until checks-settled` only observes and does not land. Do not equate a green feature-branch check, an opened PR, or a local merge with a remote target receipt.

An actual landed candidate can later fail post-target CI. Preserve its failed receipt and make a forward fix or use WB's forward-revert path; do not rewrite the target. `wb worktree merge acknowledge-landed-failed <receipt>` is a dry-run proof first. Use `--apply --actor <operator> --reason <reason>` only after it proves the exact failed candidate and receipted sources are contained in the current remote target. It writes a separate audit acknowledgement, not a success verdict or a repair. A validation failure that **never landed** is a different state: follow the receipt's `resume_args` or the specific recovery verb it names, and do not acknowledge it as a landed failure.

The landing receipt is the primary proof. Confirm its exact target SHA and terminal state, required exact-head CI result, and that the remote target contains the candidate. WB's land journey also attempts canonical fast-forward and receipt-gated cleanup; inspect any reported refusal or incomplete phase. After a separate feature-branch push, `wb worktree guard . --published` proves that exact `HEAD` reached its own remote branch, but does not prove target landing. For remaining eligible worktrees, preview `wb worktree cleanup <task> --base <target>`, then apply `wb worktree cleanup <task> --base <target> --apply` only when its fresh remote containment proof permits it. `--remote` additionally deletes an unchanged remote feature branch under WB's guard. Never hand-roll GitHub merging or branch/worktree deletion.

## Campaign example

The following is an **unexecuted** example for a clean WB feature worktree based on `cov/integration`. The landing owner runs the final command after review; a worker preparing the change stops before it.

```sh
wb worktree guard .
wb worktree log refresh . --base cov/integration --format json
wb worktree log checkpoint . --message "ready to integrate" --skip-remote
wb worktree log integrate . --base cov/integration --strategy auto --format json
# Revalidate the feature, then give its clean worktree to the landing owner.
wb worktree land <source-worktree> --target cov/integration --route auto --progress --format json
```

For detailed preconditions and recovery, see [worktree guard](../ai/skills/wb-worktrees/references/guard.md), [Work Log verbs](../ai/skills/wb-worktrees/references/worklog.md), [merge and land](../ai/skills/wb-worktrees/references/merge.md), and the [WB merger contract](../ai/skills/wb-merge/references/worktree-merge.md). Their instructions and the installed command help take precedence over an example here when a receipt reports a more specific next step.
