---
format: https://specscore.md/feature-specification
status: Implementing
---

# Feature: Pre-Push Tiering and Remote Checkpoints

> [SpecScore.**Studio**](https://specscore.studio): | [Explore](https://specscore.studio/app/github.com/sneat-dev/wb/spec/features/pre-push-tiering-and-remote-checkpoints?op=explore) | [Edit](https://specscore.studio/app/github.com/sneat-dev/wb/spec/features/pre-push-tiering-and-remote-checkpoints?op=edit) | [Ask question](https://specscore.studio/app/github.com/sneat-dev/wb/spec/features/pre-push-tiering-and-remote-checkpoints?op=ask) | [Request change](https://specscore.studio/app/github.com/sneat-dev/wb/spec/features/pre-push-tiering-and-remote-checkpoints?op=request-change) |
**Status:** Implementing
**Source Ideas:** —

## Summary

Tier the managed pre-push hook by push target and give wb worktree checkpoint a fast, tier-0-only remote ref, so agents can persist work often without paying broad validation cost on every push.

## Problem

WB's managed pre-push hook runs `go vet ./...` (or the Node profile's `lint`)
and `go test ./...` (`test`) on every push. Measured: a docs-only push — one
Markdown file, zero Go files changed — took just over 6 minutes to reach the
remote, with every Go package reporting `(cached)`. The suite did no real
work; it only walked ~40 packages to confirm nothing had changed. This tax on
every push is why agents batch up work instead of checkpointing often, which
is itself a data-loss risk: a machine that disappears mid-batch takes
unpersisted work with it.

The September 4, 2026 throughput review later found that limiting tests to
publication pushes was still too costly: 501 push attempts consumed about 23.4
machine-hours in 30 days, while pull-request CI already ran full coverage. Two
separate needs follow from this:

1. Static checks should run before publication, while tests, builds, coverage,
   and race run once in the landing/CI path instead of once per push.
2. Agents need an explicit, fast, non-reviewed persistence path that is
   neither the reviewed branch itself nor a local-only journal entry, so a
   disappearing machine never costs more than the last few minutes of work.

## Behavior

### Tiering the managed pre-push hook

#### REQ: three-fixed-tiers

The managed pre-push hook MUST have exactly three tiers. Tier 0 (the base
`git diff --check`, worktree admission, canonical-clone guard, custom policy,
and metrics blocks) MUST run unconditionally on every push and MUST stay
sub-second; it is never skippable by any push classification. Tier 1
(`go vet ./...` / the Node profile's `lint`) MUST run on every push that is
not a pure remote-ref deletion and not confined to the
`refs/wb/checkpoints/*` namespace. Tier 2 MUST identify a publication push for
telemetry and downstream policy, but MUST run the same static checks as Tier 1.
Local pre-push hooks MUST NOT run tests, builds, coverage, or race; landing and
CI own those gates.

#### REQ: publication-definition

A publication push is one whose remote ref is the repository's configured
default branch, OR is a tag (`refs/tags/*`), OR names a branch
(`refs/heads/<branch>`) that has an open pull request. Any other push to
`refs/heads/*` — a feature branch with no open pull request yet — runs Tier 1
only.

#### REQ: pr-status-never-blocks-on-network

Determining whether a branch has an open pull request MUST work fully
offline and MUST never hang the hook on a network round trip. The mechanism
MUST prefer a positive signal WB already owns (a local cache it wrote itself)
over asking GitHub. A negative answer is mutable as soon as a pull request is
created, so it MUST be revalidated rather than trusted from that cache. An
opportunistic `gh` lookup is permitted only with a bounded timeout; successful
positive results may be TTL-cached, while negative, failed, and timed-out
results MUST NOT be cached.
When PR status cannot be established within that bounded budget, the hook
MUST treat it as unknown and run Tier 1, never silently classify it as a
publication: CI remains the real gate either way.

#### REQ: one-line-explainable-decision

The hook MUST print exactly one line stating which tier it ran and why, on
every invocation, so an agent or a human can tell "fast tier, no PR" apart
from "hung" without guessing.

#### REQ: no-bypass-flag

`--no-verify` MUST NOT appear in any skill, doc, or agent instruction this
feature produces, and this feature MUST NOT introduce any other mechanism
that skips Tier 0. A manual override, if offered at all, MUST be a named,
logged mechanism (e.g. an environment variable the hook itself reads) that
still runs Tier 0 unconditionally.

### Remote checkpoints

#### REQ: dedicated-ref-namespace

`wb worktree log checkpoint` MUST, unless `--skip-remote` is given, publish
the worktree's exact current HEAD to `refs/wb/checkpoints/<task>` at origin,
where `<task>` is the active Work Log claim's task identity. This namespace
MUST NOT be `refs/heads/*` or `refs/tags/*`.

#### REQ: checkpoint-push-is-tier-0-only

A push confined to `refs/wb/checkpoints/*` MUST run Tier 0 only: no lint, no
test. The pre-push tiering classifier (see publication-definition above)
MUST recognize this namespace directly, so no separate bypass mechanism is
needed to make a checkpoint push fast.

#### REQ: checkpoint-push-does-not-trigger-ci

A push confined to `refs/wb/checkpoints/*` MUST NOT trigger a GitHub Actions
workflow run in this repository.

#### REQ: checkpoint-force-scoped-narrowly

The checkpoint push MUST be expressed as a single `+<sha>:refs/wb/checkpoints/<task>`
refspec, so any force-update it needs is scoped to exactly that one
destination ref. It MUST NOT pass a bare `--force`, a wildcard refspec, or
otherwise touch `refs/heads/*`.

#### REQ: checkpoint-push-is-non-fatal

A remote checkpoint push failure (no origin, offline, detached HEAD, no task
identity) MUST NOT fail `wb worktree log checkpoint`: the local Work Log
journal entry — the pre-existing, local-only behavior — MUST still be
recorded, with the push outcome reported as a note.

#### REQ: checkpoint-fetch-side

`wb worktree checkpoint-fetch --task <task>` MUST retrieve
`refs/wb/checkpoints/<task>` from origin into the identically named LOCAL
ref (never a branch), so a second machine can retrieve a checkpoint without
it appearing in branch listings or being checked out implicitly.

#### REQ: checkpoint-never-a-landing-receipt

No output of the checkpoint push or fetch path — text, JSON, log note, or
otherwise — MUST report, log, or imply that the checkpointed task is merged,
landed, or done. Every checkpoint result MUST carry an explicit, fixed
disclaimer to that effect. WB's Definition of Done — merged and pushed to the
target branch on origin — is unchanged by this feature.

## Acceptance Criteria

### AC: pre-push-hook-runs-the-minimum-tier-the-push-actually-needs

**Requirements:** pre-push-tiering-and-remote-checkpoints#req:three-fixed-tiers, pre-push-tiering-and-remote-checkpoints#req:publication-definition, pre-push-tiering-and-remote-checkpoints#req:pr-status-never-blocks-on-network, pre-push-tiering-and-remote-checkpoints#req:one-line-explainable-decision

Pushing a feature branch with no open pull request runs lint only; pushing
the default branch, a tag, or a branch with an open pull request runs lint
and test; a pure remote-ref deletion runs neither. PR status is resolved
without any unbounded network call, an unresolved status runs lint only
(never silently the full suite), and every run prints one line naming the
tier and the reason.

### AC: no-bypass-flag-exists-or-is-suggested

**Requirements:** pre-push-tiering-and-remote-checkpoints#req:no-bypass-flag

`--no-verify` appears nowhere in this feature's skills, docs, or agent
instructions, and Tier 0 (admission, canonical-clone guard, diff-check) runs
on every push this feature classifies, including a `refs/wb/checkpoints/*`
push.

### AC: remote-checkpoints-are-a-fast-non-landing-persistence-path

**Requirements:** pre-push-tiering-and-remote-checkpoints#req:dedicated-ref-namespace, pre-push-tiering-and-remote-checkpoints#req:checkpoint-push-is-tier-0-only, pre-push-tiering-and-remote-checkpoints#req:checkpoint-push-does-not-trigger-ci, pre-push-tiering-and-remote-checkpoints#req:checkpoint-force-scoped-narrowly, pre-push-tiering-and-remote-checkpoints#req:checkpoint-push-is-non-fatal, pre-push-tiering-and-remote-checkpoints#req:checkpoint-fetch-side, pre-push-tiering-and-remote-checkpoints#req:checkpoint-never-a-landing-receipt

`wb worktree log checkpoint` force-pushes exact HEAD to
`refs/wb/checkpoints/<task>` (Tier 0 only, no CI, no branch-listing
visibility, force scoped to that one ref), never fails the local journal
entry on a push failure, is retrievable from another machine via
`wb worktree checkpoint-fetch`, and never reports, logs, or implies a
landing outcome.

## Open Questions

None at this time.

---
*This document follows the https://specscore.md/feature-specification*
