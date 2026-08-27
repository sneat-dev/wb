---
format: https://specscore.md/idea-specification
status: Draft
---

# Idea: Unify session move and park continuation storage

**Status:** Draft
**Date:** 2026-08-27
**Owner:** alex
**Promotes To:** —
**Supersedes:** —
**Related Ideas:** —

## Problem Statement

How might WB give every agent-session continuation document (move handovers and park bundles alike) one durable, private, git-backed home instead of a same-machine-only private file or a commit into the repo under work?

## Context

`sneat-dev/wb` `origin/main` once committed a `wb session move` agent handover
document — `.wb/handoffs/<handoff-id>.md` — into the source repository under
work, on the same branch as the agent's real code changes. That repository is
public. A 2026-08-27 audit found the file, confirmed it carried no literal
credential (`SYNCHESTRA_TOKEN` appeared only as an environment-variable name),
and it was deleted (PR #201, `sneat-dev/wb`). Two things followed in the same
session:

1. A narrow, already-landed fix (branch `fix/session-move-private-handover`,
   merged to `sneat-dev/wb` `main` as PR #202,
   `9c2c0f717333de61c560b99694a5f3fe6b4fb15f`):
   `wb session move` no longer writes the handover into the repo under work at
   all. The rendered document now travels inline on a new
   `sessionmove.Request.HandoverContent` field, gets materialized as a private
   0600 file inside WB's own `sessionmove.Store` (`internal/sessionmove/store.go`,
   `EnsureHandoverUnderLock`/`ReadHandover`/`PrivateHandoverPath`), and is
   handed to the successor as `sessionauthority.ContinuationPrivate` via
   `WB_SESSION_CONTINUATION_FILE`, exactly the way `wb session park` already
   delivers its own continuation content. That fix is deliberately narrow: no
   new repo, no new config key, no shared package, `wb session park`
   untouched.
2. This Idea: while comparing the two features to design that fix, it became
   clear `wb session move` and `wb session park` independently solved
   overlapping problems with incompatible strengths and gaps (see
   `spec/features/agent-session-move/README.md` and
   `spec/features/park-and-resume-agent-sessions/README.md` for their current,
   still-accurate feature definitions). The founder's ruling that public
   commits are the wrong place for continuation text, plus that comparison,
   is what motivates unifying the storage/content/read layer underneath both
   — not merging their custody/lifecycle state machines, which stay separate
   on purpose (see Recommended Direction).

### What each protocol has that the other lacks

| | `wb session move` | `wb session park` |
|---|---|---|
| Delivery timing | Immediate: predecessor launches successor synchronously via SSH/Synchestra | Delayed: park now, resume whenever, from wherever |
| Repos/worktrees per handoff | Exactly one, fields live directly on `sessionmove.Request` | N, via `sessionpark.RemoteRequest.Members []RemoteMember`, each with its own repo/remote/branch/commit/work-log-ref |
| Continuation content shape | Structured: rendered Markdown with an Identity/Checkpoint field table plus Summary / Validation evidence / Remaining work / Agent-authored handover sections (`worktrees.SessionHandover`) | Flat: one opaque `Continuation string`, no schema-enforced sections |
| Content persistence today | Inline in WB's own private `sessionmove.Store` (as of the interim fix above) | Never touches Git: a private 0600 file under `~/.wb/parked-sessions/…`, copied to `~/.wb/park-resumes/…` over SSH |
| Resumable by a third machine? | N/A (synchronous, one target, no resume concept) | **No** — the gap this Idea exists to close: only the original source machine can serve a resume |
| Launch-time delivery kind | `sessionauthority.ContinuationPrivate` (as of the interim fix; was `ContinuationTracked` before it) | `sessionauthority.ContinuationPrivate` already |
| Custody model | Receipt-gated: predecessor keeps custody until a durable target receipt proves the successor is live | Claim-based; no live predecessor process required at resume time |

Net read: park already has the right transport shape (private, never
committed into the repo under work) but the wrong reach (single machine,
no durable store). Move now has the right transport shape too (after the
interim fix) but still only supports one repo per handoff, where park's
`Members` list is the more general shape. Each is missing exactly what the
other has, and both would benefit from one shared, once-hardened
implementation of the parts that are genuinely the same problem.

### What remains unmerged today

So a cold reader does not assume more has already happened than has: as of
the interim fix (PR #202) plus PR #201, **none of the following is unified
yet** — this Idea's Recommended Direction below is what would unify them,
none of it is implemented:

- **Storage.** `wb session park` still writes to a private local file under
  `~/.wb/parked-sessions/…` and moves it to the resuming machine over direct
  SSH (`~/.wb/park-resumes/…`). `wb session move`'s interim fix stores in its
  own local `sessionmove.Store`. Neither uses a durable, git-backed,
  third-machine-reachable store yet.
- **Content schema.** Move's structured Markdown document and park's flat
  opaque `Continuation` string remain two independent shapes; nothing has
  been generalized into one schema yet.
- **Shared package.** No shared store/reader package exists yet; move and
  park each still call their own storage and materialization code.
- **Config.** No new `continuations`-style config key exists; `wb remote`'s
  existing `remote.repo` (state) key is untouched by the interim fix.
- **Privacy-mode switch.** The `repo` vs. `local-only` config-level choice
  described under Open Questions does not exist yet — there is no switch
  because there is no repo-backed store yet to switch to.
- **`sneat-dev/wb-handoffs`.** The repository exists and is private, per
  founder provisioning, but is currently empty — nothing publishes to it yet.

### The trap this Idea's implementer must not fall into again

`internal/sessionlaunch/private.go`'s `verifyLauncherWorktree` is the exec-time
safety check the private launcher runs immediately before replacing itself
with the harness process. Before the interim fix, it unconditionally read the
handover from the pinned worktree at `request.HandoverPath`. Removing the
source-side commit into the repo under work, without also fixing this
exec-time read, would have broken every live session move silently — the
break surfaces only at the exact moment a real successor tries to launch, not
in any test that stops at `CreateSessionCheckpoint`. The interim fix branches
`verifyLauncherWorktree` (and the sibling `validatePrivatePlan`) on
`request.HandoverContent != ""`, and ships a dedicated integration test,
`TestRunPrivateLauncherReadsPrivateHandoverForNewStyleRequestsWithoutTouchingTheWorktree`
in `internal/sessionlaunch/launch_test.go`, that drives `runPrivateLauncher`
end to end and asserts the private file is what gets read and that no
`.wb/handoffs` directory ever appears in the pinned worktree. Whoever
implements this Idea's storage migration must keep an equivalent exec-time
integration test green for both protocols' read paths — this is exactly the
kind of change (a write-path fix landing several files away from the
read-path it must stay in sync with) that breaks silently without one.

### The `ContinuationTracked` replay caveat

`sessionauthority.ContinuationTracked` and `sessionmove.Request.HandoverPath`
are deprecated by the interim fix but **must not be deleted**. An in-flight
handoff admitted by a WB binary built before the cutover may still be sitting
in `sessionmove.Store` awaiting replay/resume when a newer binary runs; that
binary must still be able to decode and complete it by reading the legacy
path from the pinned worktree, exactly as before. Only revisit deleting
`ContinuationTracked` once no un-replayed pre-cutover handoff can plausibly
exist anywhere in the fleet — not as part of this Idea, and not casually.

## Journey

Both `wb session move` and `wb session park` are the same underlying act —
one agent handing a task to a successor — differing only in *when* the
successor picks it up. The journey below walks both branches from a single
shared start, because that shared premise is exactly what this Idea's
storage unification is for.

1. **Start (shared).** An agent is mid-task in one or more WB-managed
   worktrees and needs to hand off — either right now, to a successor it can
   watch start, or later, to whoever picks it up next, possibly on hardware
   that doesn't exist yet. Observable good result: nothing has moved yet; the
   predecessor still holds custody of every worktree in its Session, and a
   person watching sees ordinary, uninterrupted agent work.

2. **Branch A — immediate torch-pass (`wb session move`).**
   a. The predecessor runs `wb session move` naming a target. Observable good
      result: within that one command's lifetime, a person watching sees a
      successor process start on the target, already inside the correct
      pinned worktree(s), already narrating the same task the predecessor was
      on — no separate manual re-briefing step in between.
      *(Anticipated task 6 — move points its `HandoverContent` at the shared
      store; will carry its own `Verifies:` once specified.)*
   b. The successor's launcher receives the continuation via
      `sessionauthority.ContinuationPrivate` /
      `WB_SESSION_CONTINUATION_FILE` — never a re-read of anything committed
      into the worktree. Observable good result: the target repo's Git
      history gains no new commit anywhere containing the continuation text,
      and `internal/sessionlaunch/private.go`'s exec-time check is the thing
      that actually ran, not a path a future refactor quietly bypassed.
      *(Anticipated task 4's exec-time integration test pattern, generalized
      to move's read path.)*
   c. The predecessor releases custody only once it holds a durable receipt
      that the successor is alive. Observable good result: querying
      source-side state mid-handoff shows custody still with the
      predecessor; only after the receipt lands does it show the successor's
      session — there is no window where a person can observe custody as
      simultaneously nowhere or ambiguous.
   **Terminal good result for Branch A:** the successor is actually doing the
   work — new commits or new interactive output are visibly flowing from the
   successor's session — and the predecessor process is gone.

3. **Branch B — delayed resume (`wb session park`).**
   a. The agent runs `wb session park`. Observable good result: the terminal
      returns control to whoever's watching, the parked bundle now exists
      somewhere durable, and the originating machine is free to go away —
      sleep, reboot, get wiped — without anything depending on it staying up.
   b. **Null-action step.** Nobody does anything with the parked session for
      a while — no successor runs, no predecessor runs, the origin machine
      may be powered off entirely. Observable good result: time passes
      (minutes, hours, days) and the parked session is simply unchanged;
      nothing decays, nothing needs to be kept alive to remain valid, and a
      listing command still reports it as resumable exactly as it did the
      moment it was parked.
   c. Later, from a machine that is **not** the one that parked it, someone
      runs the resume command. Observable good result: the successor lands
      in the correct pinned worktree(s) and receives the same continuation
      content an immediate torch-pass would have delivered on Branch A — the
      delay changed nothing about what arrives.
      *(Anticipated task 4 — park's cutover to the shared store and reader.)*
   d. That resuming machine never spoke to the origin machine to do this —
      the origin may be gone, offline, or decommissioned by the time resume
      happens. Observable good result, **and this is the terminal state that
      does not exist today:** resume succeeds purely by fetching from the
      durable `wb-handoffs` store; no SSH reachability to the origin, or to
      any machine that ever touched this session, is required.
      *(Anticipated task 5 — the third-machine resume integration test —
      plus the new Docker journey test in Validation Strategy below, which
      is the one that proves this end to end rather than as an isolated
      fixture.)*
   **Terminal good result for Branch B:** a parked session survives its
   origin machine and is resumed, correctly, from a third machine that never
   spoke to the origin. Every other property in this journey already exists
   in one protocol or the other today; this is the one property neither
   protocol has, and the one this Idea's MVP has to newly deliver.

4. **Divergent epilogues (either branch).**
   - **Closes.** The successor finishes the task. The Session terminates
     cleanly; a person checking custody/Work Log state afterward sees no
     dangling claim on either the origin or any intermediate machine.
   - **Shares/replays.** The successor itself later hands off again — moves
     or parks in turn. Observable good result: the same journey composes at
     arbitrary depth (a second parked bundle, a second resume, and so on)
     with no step anywhere depending on the first origin machine still being
     reachable.

## Recommended Direction

Share the storage/content/read layer; keep the two custody/lifecycle state
machines separate. Concretely:

1. **One content schema.** Generalize move's richer, structured document
   (Summary / Validation evidence / Remaining work / Agent-authored body,
   rendered deterministically to Markdown, digest-able) with park's
   `Members []Member` list folded in as a first-class part of the schema —
   this is a strict superset: move's single-repo case becomes a `Members`
   list of length one, closing move's one-repo limitation for free, and
   park's flat `Continuation` string maps onto the `Body`/`RemainingWork`
   field the renderer already tolerates leaving unset (`_Not supplied._`).
2. **One durable, private, git-backed store: `sneat-dev/wb-handoffs`.** A new
   dedicated repository, parallel to and modeled on the existing
   `sneat-dev/wb-state` (machine snapshots + task claims — see
   `internal/remotestate/gitrepo`), reusing its exact mechanism: the same
   `internal/gitops` primitives (`Clone`, `AddCommit`, `Push`,
   `PullRebase`, `HeadSHA`, …) and the same clone-at-`<projects-root>/<owner>/<name>`
   convention, generalized rather than duplicated. **Must be private** —
   the whole point of this Idea is that continuation text never lands
   somewhere public again; a visibility check (`gh repo view --json
   isPrivate`) before ever writing to a configured destination is a hard
   requirement, not an enhancement.
3. **Disjoint, self-documenting path prefixes.** `wb-state` already owns
   `claims/` and `machines/` at its repo root. Continuations get their own
   top-level prefix (e.g. `continuations/`) so that setting both the
   existing state-repo config key and a new handoffs-repo config key to the
   *same* repository is legal and collision-free, for anyone who wants to
   co-locate rather than run two dedicated repos. Path shape inside that
   prefix must make the source repo, task/branch, and run/handoff id obvious
   at a glance, e.g. `continuations/<primary-owner>/<primary-repo>/<continuation-id>.md`
   with a sibling `.json` (or embedded frontmatter) carrying the full
   multi-member envelope for machine consumption.
4. **One shared, hardened read/materialize path.** Both `wb session move` and
   `wb session park` end at the same primitive: fetch by id from the
   configured store, verify the digest, materialize as a
   `ContinuationPrivate` 0600 file, hand the successor
   `WB_SESSION_CONTINUATION_FILE`. The already-hardened private-artifact
   reader in `internal/sessionlaunch/private.go` (`Openat(O_NOFOLLOW|O_CLOEXEC)`,
   `Fstat`, `S_IFREG`, `Nlink==1`, 0600, bounded-size) stays the single
   implementation both call into — never a second copy.
5. **SSH (or Synchestra) stays a pre-warm, never the source of truth.** For
   both protocols, the durable/resumable/third-machine-safe path is always
   "fetch from the continuations repo by id." A live SSH/Synchestra
   connection, when available, is purely a latency optimization on top of
   that — never a requirement, and never something a resume can depend on
   for correctness.
6. **The two custody/lifecycle state machines stay separate — do not merge
   them.** Move's synchronous, receipt-gated custody transfer (a live
   predecessor waiting for an acknowledgement) and park's asynchronous,
   claim-based resume (no live predecessor required, resumable from an
   arbitrary later machine) are genuinely different problems with different
   correctness properties. A future reader who notices they now share a
   storage layer must not conclude they should therefore share one
   orchestration package. State that reasoning directly in whatever PR
   description implements this, so nobody "simplifies" them together later.
7. **No pruning/retention in this Idea's scope, and whoever adds it must
   scope it correctly.** Any future pruning or retention policy on
   `wb-handoffs` (or a co-located `wb-state`) content must be **path-scoped**
   — operating only under `continuations/…` — never repo-scoped, so a
   retention job aimed at old continuations can never touch `wb-state`'s
   `claims/` or `machines/` history if the two are ever co-located. This
   Idea does not implement retention/pruning at all; that constraint is
   recorded here so whoever writes it later inherits it rather than
   discovering it the hard way.

## Validation Strategy

The founder's requirement, in his words: the implementer must validate this
both ways — from this MacBook to the Hetzner VM and back — the same way the
handoff work was validated, and where possible, automate the journey so it
does not depend on a person remembering to re-run a manual checklist. That
gives this Idea two validation tiers, with distinct roles. Neither
substitutes for the other.

### Tier 1 — automated Docker journey test (primary, CI gate)

Two containers stand in for two machines and exercise the full Journey above
end to end, on every change: Branch A (immediate torch-pass), Branch B
(delayed resume, including the null-action step — the parked container's
session sits untouched for a beat before a *different* container resumes
it), and the terminal Branch B property — the resuming container has no
network path back to the container that parked it.

This test must assert the **mechanism**, not just the outcome. "The
successor started" is not sufficient for a continuation-delivery feature —
the assertion has to show the successor received the *right* continuation
content *through the intended path*: read from `wb-handoffs` via the shared
reader, materialized as a 0600 `ContinuationPrivate` file, handed over via
`WB_SESSION_CONTINUATION_FILE` — not a fluke of some other channel still
being open, and not a stale copy left over from local disk.

This is real engineering in its own right — image build, SSH (or equivalent)
between containers, key material handling, deterministic teardown so runs
don't leak state into each other — and should be scoped, budgeted, and
verified as **its own anticipated task with its own `Verifies:`** (task 8
below), not a bullet folded into another task's acceptance criteria. If it
turns out disproportionately expensive relative to the rest of this Idea's
MVP, that is a trade-off to surface to the founder explicitly, not to quietly
absorb by cutting corners on the containers, the assertions, or the two
branches covered.

### Tier 2 — real MacBook ↔ Hetzner VM walk, both directions (one-time acceptance)

Required as a real-hardware acceptance step, not a suggestion, exactly as the
handoff work was validated: once **this MacBook → the Hetzner VM**, and once
**the Hetzner VM → back to this MacBook**. Both directions are required
because a one-way pass can be green while the reverse path is broken by an
asymmetry Docker cannot see: real SSH config differences between the two
specific machines, PATH differences, and which side holds custody. **`wb`
version skew between the two machines is itself a live risk worth calling
out explicitly** — nothing guarantees both sides are running the same build
during this walk, and a skew-induced failure would look identical to a
protocol bug from the outside.

One specific, previously time-costly gotcha to build into any scripted
invocation: on the Hetzner VM, `wb` lives at `~/go/bin/wb` and is **not on
the SSH PATH** — a bare `ssh <vm> wb ...` will fail to find it. Any scripted
step of this walk must use the full path.

State the reasoning plainly so a later reader does not "simplify" this away
once Tier 1 is green: **a container pair is a model of two machines, not two
machines.** The Docker journey test proves the protocol; the real-machine
walk proves the deployment. Re-run Tier 2 whenever the transport layer
changes (SSH/Synchestra courier changes, config-key changes, a `wb` release
that touches session move/park) — it is not a one-and-done checkbox that
stays valid forever once passed.

## Alternatives Considered

- **Keep `wb session move` on `ContinuationPrivate` alone, leave `wb session
  park` as same-machine-only.** Rejected as a permanent state (though it is
  exactly what the interim fix already ships): it leaves park's
  third-machine-resume gap open indefinitely and leaves two independent,
  differently-hardened implementations of "materialize a private
  continuation file" in the codebase, with no shared regression coverage.
- **Give `wb session move` its own dedicated git-backed store, separate from
  whatever `wb session park` eventually uses.** Rejected: it would recreate
  exactly the divergence (two schemas, two stores, two read paths) this Idea
  exists to close, just with git-backing on only one side.
- **Merge the custody/lifecycle state machines into one, since they'd share
  storage anyway.** Rejected: synchronous receipt-gated handoff and
  asynchronous claim-based resume have genuinely different correctness
  requirements (a live predecessor to acknowledge vs. none), and forcing one
  state machine to serve both would either weaken move's stronger guarantee
  or force spurious synchrony onto park.
- **Transport the continuation bytes only inline over the courier (SSH/
  Synchestra), skip a durable git-backed store entirely.** Rejected: this
  reproduces today's park behavior (no durable, third-machine-resumable
  record) and gives up the audit trail a git-backed store provides for
  free — which is also part of why the founder wants handoffs in a
  dedicated repo in the first place.

## MVP Scope

The single job the MVP nails: give `wb session park` the same durable,
private, git-backed, `sneat-dev/wb-handoffs`-based continuation storage `wb
session move` already has after the interim fix, resumable from a third
machine, without weakening either protocol's existing custody guarantees or
merging their state machines. `wb session move` itself does not need to
change again in the MVP beyond pointing its already-inline `HandoverContent`
at the new shared store instead of (or in addition to) WB's local
`sessionmove.Store`.

The MVP is not done when the code merges and unit tests pass. It is done
when the Journey above can actually be walked for both branches, and both
tiers in Validation Strategy pass: the automated Docker journey test (Branch
A, Branch B including the null-action step, and the no-network-path
third-machine assertion) is green, and the real MacBook↔Hetzner-VM walk has
succeeded in both directions.

## Not Doing (and Why)

- Merging move's and park's custody/lifecycle state machines — see
  Recommended Direction point 6; they solve genuinely different problems.
- Deleting `sessionauthority.ContinuationTracked` or
  `sessionmove.Request.HandoverPath` — see the replay caveat in Context;
  premature deletion strands any un-replayed pre-cutover handoff with no
  recovery path.
- Implementing retention/pruning on `wb-handoffs` content — deferred, but the
  path-scoped-not-repo-scoped constraint is recorded for whoever does.
- Choosing the privacy-mode default (repo-backed vs. local-only) — see Open
  Questions; this is a founder decision, not an implementer's to make from
  ambiguity, and this Idea does not pre-empt it.
- Creating `sneat-dev/wb-handoffs` itself — creating a brand-new repository
  is a founder decision (per standing policy), not something this Idea or
  its eventual Plan should do unilaterally; the Plan stage should stop and
  ask for that repository to be created rather than provisioning it.

## Key Assumptions to Validate

| Tier | Assumption | How to validate |
|------|------------|-----------------|
| Must-be-true | A visibility check (`gh repo view --json isPrivate`) reliably distinguishes a public from a private destination before the first write, for both a fresh and an already-cloned local copy of the store. | Point the config at a deliberately public test repo and assert the write refuses before anything is committed; repeat against a private one and assert it proceeds. |
| Must-be-true | `internal/sessionlaunch/private.go`'s hardened private-artifact reader generalizes to both protocols' materialized files without weakening either's mode/link-count/size checks. | Extend `TestRunPrivateLauncherReadsPrivateHandoverForNewStyleRequestsWithoutTouchingTheWorktree`-style coverage to park's resume path and assert identical tamper detection. |
| Should-be-true | Co-locating `wb-handoffs`'s `continuations/` prefix inside the existing `wb-state` repository is genuinely collision-free and doesn't confuse `wb remote status`/`wb remote publish` tooling that already reads that repo. | Configure both keys to the same repo in a test fixture and run the existing `wb remote` test suite plus new continuation-store tests against it together. |
| Might-be-true | A third machine that has never seen either the source or the original target can resume a parked session purely from `wb-handoffs`, with no SSH reachability to either prior machine. | Build a three-machine integration fixture: park on A, attempt resume from C with A and B both unreachable. |

## SpecScore Integration

- **New Features this would create:** a new feature under `spec/features/`
  (working title `unified-session-continuation-storage`) once this Idea is
  approved and specified; it depends on / supersedes the storage-relevant
  slice of both `spec/features/agent-session-move/README.md` and
  `spec/features/park-and-resume-agent-sessions/README.md` without replacing
  either feature's custody/lifecycle acceptance criteria.
- **Existing Features affected:** `agent-session-move` (storage/read path
  only — its custody/receipt acceptance criteria are unaffected) and
  `park-and-resume-agent-sessions` (gains third-machine resumability).
- **Dependencies:** `sneat-dev/wb-handoffs` must exist and be private before
  implementation starts (founder-provisioned, not agent-provisioned — see Not
  Doing); `internal/gitops` and `internal/remotestate/gitrepo` supply the
  reusable git-backed-store mechanism; `internal/sessionlaunch/private.go`
  supplies the reusable hardened private-artifact reader.

## Anticipated task breakdown (for the Plan stage)

Once specified into a Feature, the following is the expected task shape —
each already phrased as a Given/When/Then so it can become a real acceptance
criterion, and each will need a `Verifies: <feature-slug>#ac:<id>` trailer
once that Feature exists. This is not a substitute for running
`specstudio:specify` and `specstudio:plan`; it exists so a future implementer
on a different machine, with none of this session's context, can see the
intended shape immediately.

1. **Shared content schema.** Given a move-shaped single-repo handover and a
   park-shaped multi-member bundle, when both are encoded through the new
   shared schema, then both round-trip losslessly and a park bundle with an
   unset Summary/Validation-evidence renders with the existing
   `_Not supplied._` fallback.
2. **`wb-handoffs` store package, modeled on `internal/remotestate/gitrepo`.**
   Given a configured private destination repo, when a continuation is
   published, then it is committed and pushed under
   `continuations/<owner>/<repo>/<id>.md` (+ machine-readable sibling), and a
   second publish of identical content is a no-op commit-free replay.
3. **Visibility refusal.** Given a configured destination that is public, when
   any publish is attempted, then WB refuses before any write and names the
   repo and the `wb config` key to fix, exactly the way `wb remote`'s
   `UnconfiguredError`/`ConfigSnippet` pattern already reports a missing or
   invalid destination.
4. **`wb session park` cutover to the shared store and reader**, with an
   exec-time integration test proving the private-artifact read path is
   exercised end to end (mirroring
   `TestRunPrivateLauncherReadsPrivateHandoverForNewStyleRequestsWithoutTouchingTheWorktree`),
   plus a genuinely-tested `local-only` mode that preserves today's
   direct-SSH-only, non-third-machine-resumable behavior for anyone who
   opts out of off-machine replication.
5. **Third-machine resume integration test.** Given a park created on machine
   A and never contacted by machine C, when C resumes it using only
   `wb-handoffs`, then the successor launches correctly with no SSH
   reachability to A or B.
6. **`wb session move` points its existing inline `HandoverContent` at the
   shared store** (additive: `sessionmove.Store`'s local durable copy can
   remain as the replay/resume authority it already is; this task only adds
   the durable git-backed publish alongside it).
7. **PR description states explicitly why the two custody/lifecycle state
   machines were not merged** (Recommended Direction point 6), so a future
   reader doesn't "simplify" them together.
8. **Automated Docker journey test.** Given two containers standing in for
   two machines with no shared filesystem, when the full Journey above is
   driven end to end against them — Branch A torch-pass, Branch B park →
   null-action pause → resume from a *different* container with no network
   path to the parking container — then every stage's observable good result
   holds, and the assertions verify the successor received its continuation
   through the shared reader (`wb-handoffs` fetch → digest verify →
   `ContinuationPrivate` materialize → `WB_SESSION_CONTINUATION_FILE`), not
   merely that a successor process started. This is its own task, scoped and
   budgeted independently (see Validation Strategy) — do not fold it into
   task 4 or task 5's acceptance criteria.
9. **Real MacBook ↔ Hetzner VM walk, both directions.** Given the fully
   cut-over implementation, when the walk is run once this-MacBook→VM and
   once VM→this-MacBook (any scripted VM-side invocation using the VM's full
   `~/go/bin/wb` path, since it is not on the SSH PATH), then both directions
   complete with no asymmetry in custody, delivery, or `wb` version between
   the two machines. Required as real-hardware acceptance in addition to
   task 8, not instead of it (see Validation Strategy); re-run whenever the
   transport layer changes.

## Open Questions

1. **Privacy-mode default: `repo`-backed vs. `local-only`.** Moving
   continuation text (today never leaving the machine it's written on, for
   park) into a private GitHub-hosted repository keeps it access-controlled
   but does replicate it off-machine into a third-party-hosted store for the
   first time. **Recommendation:** make this a config-level switch —
   `repo` (durable, git-backed, third-machine-resumable) vs. `local-only`
   (today's direct-transport-only behavior, kept as a real, tested mode, not
   a stub) — defaulting to `repo`, since that is the founder's stated
   direction for handoffs and this Idea's explicit reason for existing.
   Founder's directional lean as of the 2026-08-27 session backing this
   Idea: default to `repo`; `local-only` must stay a real, tested option for
   anyone who wants zero off-machine replication. The final default and
   rollout timing are still the founder's call, not settled here.
2. Should the new `wb-handoffs`-repo config key and the existing
   `remote.repo` (state) config key share one generalized "repo-backed
   store" config concept in `wb.yaml`, or stay two independently-documented
   keys that happen to allow the same value? (The interim
   `ContinuationPrivate` fix deliberately did not touch `wb remote`'s
   existing config at all, to avoid scope creep into code another lane might
   be relying on; this question is for whoever specifies the shared store.)
3. Does `wb-handoffs` need its own retention/compaction policy from day one,
   or is "grows forever, git handles it" acceptable for an MVP given the
   store is text-only and per-handoff documents are small? (Not doing
   pruning in this Idea's MVP either way — see Not Doing.)
