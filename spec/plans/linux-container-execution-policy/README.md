---
format: https://specscore.md/plan-specification
status: Draft
---

# Plan: Mandatory Linux-container execution policy for WB agents

**Status:** Draft
**Source:** none
**Date:** 2026-10-28
**Owner:** alex
**Supersedes:** —

## Summary

Give the operator one user-level switch that makes every dispatched agent run a
capped Linux container instead of a host process: `agents.execution` in
`~/.config/wb/wb.yaml`. The policy type, strict parsing, plan/record snapshots
and fail-closed admission are implemented, and a candidate branch now implements
the native runtime: a verified keyless image, one global per-user execution
slot, a bounded namespace lifetime, host Git and validation handoff after the
namespace is proved absent, and live native lifecycle proofs on a Linux host.
Those proofs are **local candidate evidence**: local proofs alone do not
establish CI, release, installation or policy activation; operators need the
exact corresponding receipts, and no image is published.

The policy is deliberately small and closed: mode, immutable image, memory and
memory-plus-swap bounds, CPUs, PIDs and `max_parallel`. A profile, task type,
harness override or command-line flag cannot reach it, lower it or bypass it.
Unknown fields are refused rather than ignored, so no arbitrary runtime
argument, mount or security escape can be smuggled through configuration.

### Implemented support (candidate)

- **Strictly optional policy.** With no `agents.execution` key, the host path is
  unchanged. When the key is present, every admitted run is contained.
- **`dsh` only, contained only.** `mode` is exactly `linux-container`; a
  harness the runtime cannot carry is refused, never run on the host.
- **Exact credential routing.** The one provider credential is resolved from the
  exact `credential_env`/`credential_file` routing and injected into the
  container by variable *name*. It never appears in argv, the overlay or the
  record; validation commands receive no credential at all.
- **One user-global budget.** A durable per-user slot is held before any Docker
  probe and enforces `max_parallel: 1`, so the container's cgroup bounds are the
  worker's real bounds.
- **Kernel verification.** The controller reads the container's own cgroup
  memory, swap, CPU and PIDs files and its `HostConfig`, and refuses a mismatch
  before the worker starts.
- **Closed mount set.** The worktree is read-write, the Git common directory is
  read-only and a generated scoped input directory is read-only. Host (macOS)
  dependency and cache paths are masked by fixed, container-owned tmpfs masks
  that are created fresh per run and explicitly executable, so Linux native
  dependencies load from the ephemeral mask and no host dependency tree is
  reused; the masks disappear with the container. No host home, Docker socket or
  credential store is mounted.
- **Keyless immutable image contract.** The image ships the composed `dsh`
  profile *contents* at `/opt/wb-dsh-profile`, the tool tree at
  `/opt/agent/node_modules`, and `dsh`, Node and coreutils on the worker `PATH`,
  with an unprivileged worker UID/GID and root PID1 watchdog. No image is
  published and WB contains no image-build or registry steps.
- **Bounded lifetime, total above the stage budgets.** The container's PID1
  kernel timeout is the complete validated total: an explicit setup pool
  (runtime open, worker home, read-only probes), the harness bound, every
  configured validation bound and a cleanup pool. A contained comparison waits
  for that same complete lifetime.
- **Stable handoff after cleanup.** Host Git change/diff metadata is captured
  only after `Close` proves the container namespace absent; a cancelled Docker
  exec client is never treated as proof that the workload stopped. Contained
  validation is fail-fast so a timed-out step cannot overlap a later step.
- **Truthful failure accounting.** A cleanup failure fails the execution, blocks
  the durable slot and keeps the validation evidence exactly as observed. An
  absent runtime receipt means no fully verified runtime was written, not that
  no container ever existed; a partial open can hold a cleanup failure in the
  slot. A hard owner death cannot write the final receipt: the durable block
  and the next verified recovery are the truthful state.

### User journey and observable results

1. The operator declares `agents.execution` in the user configuration file. A
   complete policy loads; a missing or mutable image, an unknown mode, a
   non-positive bound, a swap total different from memory, or unsupported
   parallelism is refused before any worktree exists. With no policy declared,
   WB keeps its current behaviour exactly.
2. The operator asks what would run (`wb agent run --dry-run`). The resolved
   plan states the mandatory policy — mode, image, memory, swap, CPUs, PIDs and
   parallelism — or says that host execution applies. The record and result
   types carry the same snapshot, so a later configuration edit cannot rewrite
   what a run was required to respect.
3. The planner resolves a task type, profile and harness exactly as today. The
   policy is not a profile property: explicit profiles, `--harness/--model`,
   preflight fallback, a comparison attempt and a retry all carry the identical
   snapshot.
4. The worker's preflight, model proof and version probe, the harness and its
   validation steps all execute inside one container whose memory, swap, CPU and
   PIDs limits are those of the policy, verified from the container's own
   cgroup rather than trusted from arguments. Implemented in the candidate and
   proven locally, not released.
5. Exactly one execution runs at a time, so the container's bounds are the
   worker's real bounds. The container's init process is bounded to the owner's
   lifetime so detached children die when the owner exits, and the durable
   record is completed before the dispatcher reports a terminal result.
   Implemented in the candidate and proven locally, not released.
6. Every termination path — success, failure, timeout, cancellation, missing
   Docker, missing image, unverifiable cgroup — leaves a stable handoff (run
   record, diff, validation result) and cleans the container; a failed cleanup
   is reported, not hidden. Only then does the next serial task start.
   Implemented in the candidate and proven locally; CI and independent re-review
   remain pending.

## Approach

The configuration boundary came first, because a runtime that accepts a policy
it cannot honour is worse than no policy. The closed `ExecutionPolicy` type,
strict YAML decoding, validation, plan/record/result snapshots and the
fail-closed admission guard are complete. The candidate branch then implements
the container runtime behind the existing preflight and owner boundaries: verify
the image and digest, run preflight/model dump/version through the runtime,
mount the worktree read-write and the Git common and scoped input directories
read-only, carry the credential as the only transport, and verify the
container's cgroup limits before the harness starts. The single global execution
slot and the bounded init lifecycle are in place too. Remaining delivery work is
proving the candidate through this repository's normal gates and then resuming
the adapter work one task at a time; no step adds a second execution record or a
second admission path.

## Tasks

### Task 1: Declare and validate the mandatory execution policy

**Status:** complete
**Verifies:** User journey steps 1–2.

Add the top-level `agents.execution` policy with a closed field set and
backward-compatible absence. Refuse an unknown mode, a missing or mutable image,
non-positive bounds, a swap total that differs from memory, and unsupported
parallelism; refuse unknown keys so no runtime argument, mount or security
option can be expressed.

### Task 2: Snapshot the policy and fail closed at admission

**Status:** complete
**Verifies:** User journey steps 2–3.

Carry the resolved policy in the dry-run plan document and in the run record and
result types, unchanged across explicit profiles, synthesized harness/model,
preflight fallback and comparison attempts. At admission the loaded user
configuration is the authority: a plan that carries no policy snapshot, carries
a removed one, or carries a different one is refused, so a hand-built or stale
plan cannot become a bypass. A matching policy admits only the contained `dsh`
path.

### Task 3: Run preflight, model proof and version discovery inside the container

**Status:** in_progress
**Verifies:** User journey step 4.

Pull-free verification of the policy's immutable local image, then execute the
existing preflight — executable presence, the `dsh` composed-model dump and the
harness version probe — through the container runtime. No host `dsh` probe. Pass
the credential as the only transport (no host home, agent socket or ambient
state), mount only the worktree, the read-only Git common directory and the
generated scoped input directory, and apply the policy's memory, swap, CPU and
PIDs limits to harness and validation alike. Record the runtime identity on the
run. A missing Docker, an unavailable or mismatched image, or an unsupported
harness fails closed. Implemented in the candidate; CI, coverage and independent
re-review are pending.

### Task 4: Verify the enforced limits and bound the container lifecycle

**Status:** in_progress
**Verifies:** User journey steps 4–5.

Independently read the running container's cgroup limits and host config and
refuse to start the harness when they disagree with the policy. Hold one global
execution slot so `max_parallel` is the real concurrency bound. Give the
container a bounded init process tied to the owner's lifetime so detached
children cannot outlive the owner, and report the container's exit status
through the existing owner record. The complete lifetime is an explicit
setup + run + validation + cleanup total below the existing four-hour ceiling.
Implemented in the candidate; CI, coverage and independent re-review are
pending.

### Task 5: Make every termination path a stable handoff

**Status:** in_progress
**Verifies:** User journey steps 5–6.

Complete the run record, diff and validation result before a terminal result is
reported, and clean the container on success, failure, timeout and cancellation.
Host Git metadata is read only after `Close` proves the namespace absent; a
cancelled Docker exec client is never treated as a stopped workload, and
contained validation is fail-fast so a timed-out step cannot run alongside a
later one. A cleanup failure is recorded, never hidden. Implemented in the
candidate; CI, coverage and independent re-review are pending.

### Task 6: Prove the enforced runtime with real tests and the normal gates

**Status:** in_progress
**Verifies:** User journey steps 1–6.

The candidate carries native lifecycle proofs for success, cancellation, owner
death, timeout and a shared slot, with the exact declared memory, swap, CPU and
PID bounds observed inside the container, credential-free validation and an
independent absence check. Those are local candidate proofs on one Linux host.
Remaining work is the repository's own gates: the full test, build, vet, race
and coverage runs, lint, required CI, and independent re-review on the landed
tree. A green unit test without a container is not evidence of enforcement.

### Task 7: Resume the DataTug adapters through the governed landing path

**Status:** planning
**Verifies:** User journey steps 6 and delivery.

After the runtime is accepted through the gates above, resume the dual-language
adapter work one serial task at a time, with no worker build or test executing
on macOS. Land each reviewed batch through the normal lifecycle, verify the
exact remote/main result, confirm required CI is green (never
`continue-on-error`) and clean up. No adapter-completion count is claimed here.

## Open Questions

- How is the single global execution slot coordinated across machines, if at
  all? This plan assumes one owner at a time.
- Which host prepares and stores the keyless image, and how is its immutable ID
  distributed to the machines that run contained work? The image contract is
  fixed; the distribution runbook is not.
- How should an operator be guided to restore the exact Docker Engine that owns
  a stale reservation, so a blocked slot can be recovered without deleting the
  durable reservation by hand?

---

*This document follows the https://specscore.md/plan-specification*
