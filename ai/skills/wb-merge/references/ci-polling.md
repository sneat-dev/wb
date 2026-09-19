# Foreground CI polling

Use `wb ci wait` for the target repository. It observes one exact SHA in a
foreground slice (eight minutes by default; never ten), shorter than that harness's tool timeout.
For a direct push use
`--repo`, `--target`, and `--head`; add `--pr` only to corroborate a PR head.
If a bound expires, invoke the JSON `resume_args` again immediately.

For a PR, WB proves that the exact candidate contains the freshly read target
SHA and that a nonempty strict required-check policy makes freshness
server-enforced. It does not wait for current target CI to become green; the PR
may be its fix. Target movement requires reintegration. Merge-group/SHA
observation is planned, so do not use this source-head receipt to bypass a merge
queue.

`passed` means GitHub's required-check policy was read successfully, every
required context was present, and the complete observed terminal set was
unchanged on a later foreground read. That confirming read arrives on a
shorter bounded delay (15s default) once a checks-bearing set is terminal —
at most one shortened reread per terminal episode, with churn falling back to
the normal cadence — while the empty no-applicable-checks receipt always
waits a full poll interval first. In every mode WB verifies an App-pinned
context against an exact-head check run from that App; a same-named PR summary
or legacy commit status is not producer evidence. This is a bounded quiescence receipt.
Optional workflows can register after that window, so collect the
repository's separate release evidence before cleanup.

Never detach a watcher, use a background process, or leave one long shell loop
running. Pending is not completion. A failed, cancelled, or stale run blocks
terminalization; report the exact receipt and hand off/resume it through the
normal effort lifecycle.

## Waiting for one workflow or job

To wait for only one workflow or job — a release, a deploy, a single job
inside a bigger CI run — while other checks on the same head are still
running, add `--workflow <name>` (repeatable, exact GitHub Actions workflow
name — two workflows that share a name are both selected) or `--check
<pattern>` (repeatable, exact check-run name/commit-status context, or a
glob) to `wb wait checks` / `wb ci wait`. In a `--check` pattern, `*` matches
any run of characters — zero or more, **including `/`** — and every other
character, including `[`, `]`, `?` and `\`, is literal: there is no
character-class, escape, or `?` syntax, and it is not `path.Match` or regex.
`"Release / *"` therefore matches `"Release / Smoke test published artifact
(linux/amd64)"` and `"Release / Finalize public release tag"` alike, because
the glob crosses the `/`.

Required-check completeness is then evaluated only over the required checks
the filter selects, and the JSON result carries a `filter` block naming what
matched. What "wait for" means differs by how the check is named:

- An **exact `--check`** waits only for that one job. A sibling job in the
  same Actions run that is still running — or the whole run stuck on an
  environment approval — never holds up the wait once the selected job
  itself is terminal. With several exact patterns, every one of them must
  match a registered check before the wait can pass; an unmatched pattern is
  reported pending, naming it (plus any nearby observed name it almost
  matches, such as a matrix job `build (ubuntu)` or a
  reusable-workflow-qualified `caller / build`), rather than letting a
  different matched pattern wave the wait through.
- A **`--check` glob** keeps its owning Actions run open until the run
  itself finishes, because a glob can still match a job that run has not
  registered yet — a later job gated by `needs:` on an earlier job in the
  same run, for example.
- **`--workflow`** always waits for the whole run, regardless of `--check`:
  that is its meaning. With several `--workflow` names, every one of them
  must have produced an observed check before the wait can pass — the same
  "every name must match" rule exact `--check` patterns already have. This
  matters for a workflow that only starts via `workflow_run` after another
  one finishes: without the rule, the first workflow finishing would pass
  the wait before GitHub has even created the second workflow's run. When
  `--workflow` and `--check` are combined, "every name must match" means
  every named workflow needs some job matching the `--check` patterns — a
  workflow whose jobs never match any pattern simply never clears, and the
  wait names it in the pending reason.

**While `--check` is active**, a still-registering Actions run that has no
job of its own yet is also kept open, regardless of whether it "owns" a
match — but only among the workflows an active `--workflow` filter itself
allows: a job can never be selected under a workflow `--workflow` excludes
(both filters are an AND), so a jobless run under a different, excluded
workflow is dropped rather than held open. Among allowed workflows, an exact
or glob `--check` can still be held pending by any other still-registering
run on the head — under the same workflow on a different trigger event, or a
different allowed workflow altogether — not only one related to what has
already matched. WB cannot know in advance which run a job will register
under, so this is deliberately coarse; it is documented here rather than
left as a surprise. When a slice times out on a run held open this way, the
pending reason names which run and why ("run \"Deploy\" (pull_request) has
not registered a job yet").

**A skipped job is not automatically a pass.** When a selected `--check` or
`--workflow` names a job GitHub reports `skipped`, the wait's verdict
depends on the owning Actions run's own top-level status:

- **The run has not concluded yet.** The wait reports **pending**, not
  passed. This is the common real-CI shape: an earlier job fails and a
  `needs:`-gated downstream job reads `skipped` well before a slow sibling
  job in the same run finishes, so the run's own conclusion is still empty.
  Reading the skip as a pass in that window let a filtered wait finish
  before an unfiltered wait watching the same commit ever would — round 4's
  red-team finding on sneat-dev/wb#629.
- **The run concluded `failure` or was cancelled.** The wait reports
  **failed**, not passed. This covers a job skipped because a job it
  `needs:` failed, and — deliberately, failing closed — a job skipped for
  any other reason while an unrelated sibling job in the *same* run also
  failed: WB cannot tell "skipped because of the job this selection cares
  about" apart from "skipped because of an unrelated sibling" without
  evaluating the `needs:` graph itself, so both fail the wait rather than
  risk a false pass.
- **The run concluded, but not as a failure, and every selected check is
  `skipping`.** The wait still reports **failed**, with the reason "every
  selected check was skipped." This is the common shape of a
  `workflow_run` Release job gated `if: conclusion == 'success'` after CI
  failed: GitHub marks every Release job `skipped` and the run itself does
  not conclude `failure` — nothing in the selection individually reads as
  failed, but nothing in it ever ran either, and "is the release built?"
  must answer no.

This matches the verdict an unfiltered wait reaches by observing the
upstream job's own check-run directly; a filtered wait that dropped that
check-run must reach the same verdict some other way.

A filter that selects nothing is never a vacuous pass. It keeps observing, at
the normal poll cadence, until a matching check registers or the slice ends,
reporting pending with "no check matching the filter has registered yet" —
never a claim that the filter will never match, since a workflow triggered by
`workflow_run` (or one that is simply slow to register) can still appear
later in the same slice. Resume the same wait to keep watching. Never pass
`--workflow`/`--check` to a landing route (`wb pr land` or a worktree merge):
landing always evaluates the full required set.

Never hand-roll a `gh run list` / `gh api` polling loop to watch a single job
or workflow — that is exactly the case these flags exist to replace.

Worked example: waiting for the release job on a main merge commit.

```
wb wait checks --repo acme/app --target main --head 0123456789012345678901234567890123456789 --check "Release / *" --json
```
