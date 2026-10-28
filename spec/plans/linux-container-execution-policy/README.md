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
`~/.config/wb/wb.yaml`. This plan records the prerequisite now implemented —
the policy type, its strict parsing and validation, its snapshot in the dry-run
plan and the durable run record, and fail-closed admission while the container
runtime does not exist — and states the remaining runtime, testing and delivery
work. It is infrastructure bootstrapping, not completed native enforcement:
nothing in this plan claims a worker already runs inside a container.

The policy is deliberately small and closed: mode, immutable image, memory and
memory-plus-swap bounds, CPUs, PIDs and `max_parallel`. A profile, task type,
harness override or command-line flag cannot reach it, lower it or bypass it.
Unknown fields are refused rather than ignored, so no arbitrary runtime
argument, mount or security escape can be smuggled through configuration.

### User journey and observable results

1. The operator declares `agents.execution` in the user configuration file. A
   complete policy loads; a missing or mutable image, an unknown mode, a
   non-positive bound, a swap total different from memory, or unsupported
   parallelism is refused before any worktree exists. With no policy declared,
   WB keeps its current behaviour exactly.
2. The operator asks what would run (`wb agent run --dry-run`). The resolved
   plan states the mandatory policy — mode, image, memory, swap, CPUs, PIDs and
   parallelism — or says that host execution applies. The record and result
   types carry the same snapshot for the runtime phase, so a later configuration
   edit cannot rewrite what a run was required to respect; until the runtime
   exists, admission refuses instead of recording a run that never had a
   sandbox.
3. The planner resolves a task type, profile and harness exactly as today. The
   policy is not a profile property: explicit profiles, `--harness/--model`,
   preflight fallback, a comparison attempt and a retry all carry the identical
   snapshot. Until the runtime can place the harness inside the sandbox, an
   admitted dispatch is refused rather than silently running outside it.
4. The worker's preflight, model proof and version probe, the harness and its
   validation steps all execute inside one container whose memory, swap, CPU and
   PIDs limits are those of the policy, verified from the container's own
   cgroup rather than trusted from arguments. Not implemented yet.
5. Exactly one execution runs at a time, so the container's bounds are the
   worker's real bounds. The container's init process is bounded to the owner's
   lifetime so detached children die when the owner exits, and the durable
   record is completed before the dispatcher reports a terminal result. Not
   implemented yet.
6. Every termination path — success, failure, timeout, cancellation, missing
   Docker, missing image, unverifiable cgroup — leaves a stable handoff (run
   record, diff, validation result) and cleans the container; a failed cleanup
   is reported, not hidden. Only then does the next serial task start. Not
   implemented yet.

## Approach

Finish the configuration boundary first, because a runtime that accepts a policy
it cannot honour is worse than no policy. The present change adds the closed
`ExecutionPolicy` type, strict YAML decoding, validation, plan/record/result
snapshots and a fail-closed admission guard, with focused tests only; it starts
no container and touches no adapter. Next, integrate the container runtime behind
the existing preflight and owner boundaries: probe image and digest, run
preflight/model dump/version through the runtime, mount the worktree read-write
and nothing else, carry the credential as the only transport, and verify the
container's cgroup limits before the harness starts. Then add the single global
execution slot and the bounded init lifecycle, and only after that resume the
fifteen DataTug dual-language adapters one task at a time against the enforced
runtime. Keep every step on the existing run record and owner vocabulary; no
second execution record and no second admission path.

## Tasks

### Task 1: Declare and validate the mandatory execution policy

**Status:** complete
**Verifies:** User journey steps 1–2.

Add the top-level `agents.execution` policy with a closed field set and
backward-compatible absence. Refuse an unknown mode, a missing or mutable image,
non-positive bounds, a swap total that differs from memory, and unsupported
parallelism; refuse unknown keys so no runtime argument, mount or security
option can be expressed. The schema is configuration only: it starts no
container.

### Task 2: Snapshot the policy and fail closed at admission

**Status:** complete
**Verifies:** User journey steps 2–3.

Carry the resolved policy in the dry-run plan document and in the run record and
result types, unchanged across explicit profiles, synthesized harness/model,
preflight fallback and comparison attempts, so the runtime phase needs no
schema change. At admission the loaded user
configuration is the authority: a plan that carries no policy snapshot, carries
a removed one, or carries a different one is refused, so a hand-built or stale
plan cannot become a bypass. While container execution is unimplemented, refuse
an admitted dispatch instead of running the harness outside the declared
sandbox. This is a refusal, not enforcement; no capability claims a sandbox.

### Task 3: Run preflight, model proof and version discovery inside the container

**Status:** planning
**Verifies:** User journey step 4.

Pull and verify the policy's immutable image, then execute the existing preflight
— executable presence, Codex credential resolution, the `dsh` composed-model dump
and the harness version probe — through the container runtime. No host `dsh`
probe. Pass the credential as the only transport (no host home, agent socket or
ambient state), mount only the worktree and the private run directory, and apply
the policy's memory, swap, CPU and PIDs limits to harness and validation alike.
Record the runtime identity on the run. A missing Docker, an unavailable or
mismatched image, or an unsupported harness fails closed before a worktree is
created.

### Task 4: Verify the enforced limits and bound the container lifecycle

**Status:** planning
**Verifies:** User journey steps 4–5.

Independently read the running container's cgroup limits and refuse to start the
harness when they disagree with the policy. Hold one global execution slot so
`max_parallel` is the real concurrency bound. Give the container a bounded init
process tied to the owner's lifetime so detached children cannot outlive the
owner, and report the container's exit status through the existing owner record.

### Task 5: Make every termination path a stable handoff

**Status:** planning
**Verifies:** User journey steps 5–6.

Complete the run record, diff and validation result before a terminal result is
reported, and clean the container on success, failure, timeout and cancellation.
A cleanup failure is recorded, never hidden. Keep the single-execution,
one-task-at-a-time sequence so the next serial task starts only after the
previous artifact is stable.

### Task 6: Prove the enforced runtime with real tests

**Status:** planning
**Verifies:** User journey steps 1–6.

Replace the admission refusal with runtime acceptance only when an integration
test proves a real container run: cgroup limits observed inside the container,
no host home or socket reachable, credential-only transport, detached children
dying with the owner, and fail-closed behaviour for each missing prerequisite.
Keep the existing configuration tests and add lifecycle and coverage cases; a
green unit test without a container is not evidence of enforcement.

### Task 7: Resume the DataTug adapters through the governed landing path

**Status:** planning
**Verifies:** User journey steps 6 and delivery.

After the runtime is accepted, resume the fifteen DataTug dual-language adapters
one serial task at a time, with no worker build or test executing on macOS.
Land each reviewed batch through the normal lifecycle, verify the exact
remote/main result, confirm required CI is green (never `continue-on-error`) and
clean up. This plan changes WB prerequisites only; adapter scope stays with the
existing adapter work and is not claimed here.

## Open Questions

- Which container runtime is available to the WB owner on every supported host,
  and how is its absence surfaced as a stable refusal rather than a timeout?
- How should the runtime prove the image digest was resolved to the intended
  immutable content rather than a locally retagged ID?
- Should a configured policy remain a hard refusal for a harness the runtime
  cannot yet carry, or should such a harness be excluded from planning with a
  typed finding? The present refusal is deliberately the conservative answer.
- How is the single global execution slot coordinated across machines, if at
  all? This plan assumes one owner at a time.

---
*This document follows the https://specscore.md/plan-specification*
