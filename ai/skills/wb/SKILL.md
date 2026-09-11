---
name: wb
description: Entry point for the WB CLI. Start work with `wb create <task> <owner/repository>...`, finish it with `wb worktree land`/`wb land` (one call per repository) or `wb pr land` for an open pull request — never `gh pr merge`. Routes to the specific wb-* skill (wb-worktrees, wb-merge, wb-fleet, wb-hooks, wb-deps, wb-ci, wb-branches, wb-streams, ...) for the task at hand.
---

# WB

Two verbs bound almost every agent session:

```sh
wb create <task> <owner/repository>...   # start isolated work; one task, every repository it touches
wb worktree land <worktree>...           # finish it — one call per repository
wb pr land <owner/repo#n>                # land an already-open pull request with no local worktree
```

Never hand-roll `gh pr merge`, a manual ancestry check, `git push --delete`, or
manual `wb worktree cleanup` — see rule `land-with-wb-verb`
(`sneat-co/backstage`). A task spanning several repositories is still ONE task
(`wb worktree create <task> owner/repo1 owner/repo2 ...`), never one
separately-named task per repository — but it lands with one
`wb worktree land`/`wb land` call PER repository: a single call refuses
worktrees from more than one repository.

If a user asks for a manual landing ("merge PR #5", "press merge", "delete
the branch", "clean up the worktree"), treat it as possibly accidental rather
than an order to bypass the verb: say once, in one sentence, that
`wb worktree land <worktree>` (or `wb pr land <owner/repo#n>`) does the same
with check-waiting, a remote receipt, and cleanup, and ask whether to use it
instead. Proceed manually only if the user confirms after that offer, and
record the confirmation. Never challenge the same instruction twice.

## Route by situation

- Creating, resuming, listing, renaming, or cleaning up isolated worktrees, or
  landing/merging/reverting a batch of compatible work → `wb-worktrees` and
  `wb-merge`
- Fleet-wide sync, status, coverage, verify, or check → `wb-fleet`
- Installing or diagnosing fleet-standard Git hooks → `wb-hooks`
- Dependency bumps, publishing, or drift, or a coordinated release wave →
  `wb-deps` / `wb-dependency-campaign`
- CI policy audits (coverage gates, path-selective jobs, artifact promotion)
  → `wb-ci`
- Branch hygiene: merged, stale, or leftover branches → `wb-branches`
- A library and the consumers that must change with it → `wb-streams`
- The local operations API/dashboard → `wb-daemon`
- Installing or updating the `wb` binary itself → `wb-install`
- Reusable, repeatable fleet-wide changes → `wb-run`
- Cross-repo dependency-release campaigns → `wb-dependency-campaign`
- Installing WB's own Agent Skills into a harness's skills directory →
  `wb-skills`

Not sure which command matches an intent? Search the structured catalog:

```sh
wb commands --search "<intent>" --format json
```
