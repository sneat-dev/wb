# Hook metrics

WB records local JSONL events when metrics are enabled. Inspect the default
14-day summary:

```sh
wb hooks metrics .
```

Narrow or automate it:

```sh
wb hooks metrics . --days 7 --repo <text>
wb hooks metrics . --days 30 --json
```

Use `--file` only for a known alternate event file. The report distinguishes
commit checks, push attempts, failures, block counts, and average duration.
Git has no post-push hook, so push counts are attempts, not confirmed remote
acceptance.

Use the slowest repeated blocks to decide whether a check should be scoped,
cached, consolidated into one E2E run, or moved from pre-commit to pre-push.
Do not disable a correctness gate solely because it is slow.

## Profile cost and the stream saving — `wb hooks measure`

`wb hooks metrics` charts activity day by day. `wb hooks measure` prices the
**profiles** from the same recording:

```sh
wb hooks measure .
wb hooks measure . --days 30 --json
```

| profile | what runs |
|---|---|
| `commit` | formatting and static checks over the files changed in **that commit**; never a test suite |
| `stream push` | a push to a `stream/<name>` branch — **no local verification at all** |
| `other push` | every other push — the current full profile, unchanged |

Each row carries its **measured budget**: the slowest run actually observed, not
an estimate. A profile with no measured budget is named under *not measured*
rather than shown as free.

The stream saving is the number of stream-branch pushes priced at the measured
average of a non-stream push, and the basis is printed beside it so the
arithmetic can be checked. A zero saving with no basis is reported as
unmeasured, never as "the stream profile saved nothing".

## Why a push to a stream branch runs nothing locally

A `stream/<name>` branch carries a draft pull request whose CI verifies every
push. Re-running that verification locally duplicates it on the very machine the
stream is trying to keep free.

The classifier tests the stream namespace **before** the publication tests,
because a stream branch always has an open pull request — which is exactly what
would otherwise force the full tier on every push to it. A branch that merely
mentions the word (`feature/stream-thing`) is not a stream branch: the namespace
is a path prefix.

A push that mixes a stream branch with the default branch or a tag is still a
publication push. The stream exemption never lowers the overall decision.

Moving cost to CI obliges WB to bound CI, which is why `wb stream start` checks
that each member's pull-request workflow carries a `concurrency` group keyed to
the ref with `cancel-in-progress: true`, and reports every member that does not.
