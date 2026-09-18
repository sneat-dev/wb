# Open (or land) a pull request with wb pr create

`wb pr create [<worktree|task>]` opens or adopts one worktree's pull request
without paying for the local suite `wb worktree merge`/`wb worktree land` run
first. It runs no build, test, or lint pass of its own: CI is the gate, and
`wb pr land` on the number it prints is the way to actually land it.

```sh
# Open a pull request from the current worktree.
wb pr create

# Commit everything, open the pull request, and land it once green.
wb pr create --commit-all -m "feat: the change" --land --approved-by review.md
wb pr create --commit-all -m the-change --land --approved-by review.md

# Commit only the named paths, open it, and land it once green.
wb pr create --add pkg/x.go,pkg/x_test.go -m "feat: the change" --land --approved-by review.md

# Open it, then land it separately once green.
wb pr create
wb pr land sneat-co/sneat-go#1041

# Always name the issue(s) this pull request closes when the task named one.
wb pr create --closes 591 -m "feat: the change" --commit-all

# --land with a declared reviewer identity: WB posts the review as a comment.
wb pr create --commit-all -m "feat: the change" --land \
  --approved-by opus@codex@run-42 --review-comment "scoped review, looks right"

# Open a draft with an explicit title.
wb pr create --draft --title "feat: the change"

# Arm auto-merge on a mechanical bump, without landing in-process.
wb pr create --auto-merge

# Machine-readable envelope.
wb pr create --format json
```

Exit codes: `0` created/adopted (or landed, with `--land`) · `1` a finding
(auto-merge could not be armed, or CI is not ready) · `2` a guard refused.

## Never `gh pr create`

Use `wb pr create` wherever you would otherwise run `gh pr create`. The
canonical clone is refused, naming `wb worktree create`. A dirty worktree is
refused, listing the uncommitted paths, unless one of the three commit modes
below is given. A branch with no commit ahead of its base is refused. The
branch is pushed through the normal `git push` path, so hooks run there too,
and the pull request is opened through the same idempotent primitive
`wb worktree merge` uses: an already-open pull request for the branch is
adopted, never duplicated. The default title/body come from the branch's own
commits; a "wip"-shaped subject never becomes the title.

## Three commit modes, mutually exclusive

- `--commit-staged` commits exactly the index; it refuses when nothing is
  staged.
- `--commit-all` runs `git add -A` first (respecting `.gitignore`) and then
  commits everything, refusing any path that looks like a secret (`.env`,
  `*.pem`, `*.key`, `id_rsa*`, `*.p12`) and never staging `.worktree.md`.
- `--add <path>[,<path>...]` (repeatable) commits exactly the named paths —
  `git add -- <paths>` then `git commit -m <message> -- <paths>` — so
  anything else already staged is left out. Every path must resolve inside
  the worktree (no `..` escape, no absolute path outside it) and must match
  something real, including a tracked file's own deletion; the same
  secret-path and `.worktree.md` refusals apply.

`-m`/`--message` is required with any one of the three, and is used verbatim.
Hooks always run — this never passes `--no-verify` — and a failing hook stops
before any push.

## `--land` fuses commit, push, open, wait, and merge

`--land` goes further than opening the pull request: it lands it in-process
through the same `wb pr land`, in one command. It implies arming auto-merge
the way `wb pr land` itself does, so `--auto-merge` alongside it is redundant
rather than an error. With `--commit-staged --land`, unstaged or untracked
changes that would remain are refused before anything is committed; with
`--add --land`, any change outside `--add`'s own paths is refused before
anything is committed — either way because landing retires the worktree and
a leftover dirty checkout cannot be cleaned up afterward. `--commit-all`,
naming every remaining path in `--add`, or `--keep` resolves it.

## `--auto-merge` needs the same authority as `wb pr land`

`--auto-merge` arms GitHub auto-merge immediately after the pull request is
created or adopted, without landing in-process. A mechanical diff needs no
approval; a non-mechanical one is refused without `--approved-by`. Arming is
skipped, with the pull request left open, wherever it would bypass a guard
`wb pr land` would also refuse to bypass without `--allow-unfenced`.
`--draft --auto-merge` is rejected: a draft pull request cannot be merged.
`--approved-by`'s reviewer-identity form (with `--review-comment`/
`--review-comment-file`) works with `--land`, exactly as it does for
`wb pr land` itself — see `pr-land.md`. Plain `--auto-merge` (without
`--land`) only accepts the back-compat review-file/comment-URL forms.

## `--closes` (issue #615)

`wb pr create --closes N[,N…]` writes one `Closes #N` line per issue at the
top of the pull request body, ahead of whatever body would otherwise be
sent. **Always pass it** when the task names an issue.

`wb pr create` never adds an issue number on its own: when the task's
original prompt (Work Log) names one (`#591`), it prints a suggestion on
stderr — `suggestion: this task's prompt names #591; pass --closes to link
them` — and stops there. Only `--closes` links it.

`wb pr land` then reports whichever issues GitHub's own
`closingIssuesReferences` names for the landed pull request — see
`pr-land.md`.
