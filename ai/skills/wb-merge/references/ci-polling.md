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
name) or `--check <pattern>` (repeatable, exact check-run name/commit-status
context, or a simple `*` glob — no regex) to `wb wait checks` / `wb ci wait`.
Required-check completeness is then evaluated only over the required checks
the filter selects, and the JSON result carries a `filter` block naming what
matched. A filter that selects nothing, once every observed check on the head
is terminal, is never a vacuous pass — it comes back pending with an explicit
not-found reason, so a mistyped workflow or check name cannot masquerade as
success. Never pass `--workflow`/`--check` to a landing route (`wb pr land` or
a worktree merge): landing always evaluates the full required set.

Never hand-roll a `gh run list` / `gh api` polling loop to watch a single job
or workflow — that is exactly the case these flags exist to replace.

Worked example: waiting for the release job on a main merge commit.

```
wb wait checks --repo acme/app --target main --head 0123456789012345678901234567890123456789 --check "Release / *" --json
```
