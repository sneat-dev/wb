# Foreground CI polling

Use `wb ci wait` for the target repository. It observes one exact SHA in a
foreground slice (eight minutes by default; never ten), shorter than that harness's tool timeout.
For a direct push use
`--repo`, `--target`, and `--head`; add `--pr` only to corroborate a PR head.
If a bound expires, invoke the JSON `resume_args` again immediately.

For a PR, WB proves that the exact candidate contains the freshly read target
SHA and that a nonempty strict required-check policy makes freshness
server-enforced. It does not wait for current target CI to become green; the PR
may be its fix. Target movement requires reintegration. On a merge-queue
target this source-head receipt fails closed: land with `wb pr land`, which
enqueues the exact head and observes the merge group, and never merge around
the queue.

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
