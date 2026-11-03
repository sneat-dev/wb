---
format: https://specscore.md/feature-specification
status: Stable
---

# Feature: Managed job cache policy

> [SpecScore.**Studio**](https://specscore.studio): | [Explore](https://specscore.studio/app/github.com/sneat-dev/wb/spec/features/managed-job-cache?op=explore) | [Edit](https://specscore.studio/app/github.com/sneat-dev/wb/spec/features/managed-job-cache?op=edit) | [Ask question](https://specscore.studio/app/github.com/sneat-dev/wb/spec/features/managed-job-cache?op=ask) | [Request change](https://specscore.studio/app/github.com/sneat-dev/wb/spec/features/managed-job-cache?op=request-change) |
**Status:** Stable
**Source Ideas:** —

## Summary

Apply size, age and free-space policy to WB-owned immutable Linux dependency archives at real job boundaries. This is the directly authorized 2026-11-02 archive-helper adoption slice; it does not approve or implement the broader Draft [Disk Reclaim](../disk-reclaim/README.md) feature.

## Problem

Reinstalling dependencies in every fresh contained validation job costs time, while an unrestricted shared cache can exhaust disk. Existing global VM stores have active or uncertain consumers and are excluded. The proven archive producer can instead restore into the existing ephemeral dependency masks and publish completed cache pairs under its own retained writer lock.

## Behavior

### REQ: explicit-owned-archive-scope

`wb disk dependency-cache` plans without cache writes or a worktree heartbeat. `--apply` explicitly restores/installs dependencies and enforces policy in `.wb-worklog/linux-dependency-cache`, backed by a dedicated owned shared volume in the adopting runner. Only regular, singly linked, owner-controlled completed archive/manifest pairs in the first-party format are eviction candidates. Unknown files, incomplete publication, source, databases, receipts, browser installations and global package stores are preserved. No daemon, general Docker census or unrelated executor/cache adoption is included.

### REQ: identity-and-safe-materialization

Derive and validate the exact pnpm version from `packageManager`, verify the provided CLI version, and retain frozen/serial install in a separate ephemeral store. Bind cache identity to dependency inputs, Node version, architecture and immutable image. The adopting transport supplies its read-only frozen source manifest so no `.git` is required; dependency input bytes must match its hashes before and after use. Preserve the proven explicit-workspace masks, archive/link validation, entry/unpacked bounds, empty-target restore and source guards.

### REQ: one-producer-lock

All restore, publish and eviction operations use the same retained `writer.lock` inode, with a kernel lock inherited by the package child. Restore/extraction is synchronous; package children use only separate ephemeral stores/masks, not shared archives. Root/lock or entry identity changes fail closed. Concurrent jobs cannot delete archives being read or written. Publication becomes eligible only after archive and metadata completion/fsync; interrupted unknown files remain protected.

### REQ: bounded-admission-and-retention

Under that lock, enforce maximum unused age, aggregate completed/unknown bytes and at most three generations, oldest-use first. Defaults retain the existing 2 GiB aggregate bound, with seven-day unused age and a 512 MiB free-space reserve; declared pre-job growth defaults to 4 GiB and is configurable. Preserve the active archive. Recheck actual free space after deletion and refuse work when safe candidates cannot satisfy budget/reserve. Bound compressed writes as they happen. Report plan/apply decisions, unknown bytes and measured post-apply headroom; a failed/partial deletion must fail the job and retain diagnostic output.

### REQ: real-runner-adoption

Adopt the first-party asset/command in a new, independently reviewed successor of the current Graph Linux validation profile, with its exact image, source manifest and both actual dependency masks (`node_modules`, `landings/incidentius/node_modules`). A pinned dedicated shared archive volume and the same lock must be used by cold and warm jobs. Per-run package stores and extracted modules remain ephemeral and are removed by normal job cleanup. Never modify an active/old helper or claim unrelated async workers or foreign VM stores are covered.

## Acceptance Criteria

### AC: plan-preserves-state

**Requirements:** managed-job-cache#req:explicit-owned-archive-scope

**Given** completed, active and unknown archive entries, **When** planning runs, **Then** bytes/mtimes and source heartbeat remain unchanged, decisions are visible, and only recognized inactive pairs can be proposed.

### AC: bounded-policy

**Requirements:** managed-job-cache#req:bounded-admission-and-retention

**Given** aged entries, multiple dependency identities, aggregate pressure or insufficient reserve, **When** apply runs under the lock, **Then** eligible oldest entries are reclaimed, active/unknown entries survive, compressed writes stop at their bound, and unreachable admission fails before package work.

### AC: lease-and-race-safety

**Requirements:** managed-job-cache#req:one-producer-lock

**Given** a held/inherited producer lock or a replaced root, lock or entry, **When** another job attempts reclaim, **Then** it cannot delete the protected files; unknown interrupted publication and deletion failures are not reported as success.

### AC: pinned-inputs-and-masks

**Requirements:** managed-job-cache#req:identity-and-safe-materialization

**Given** a frozen source snapshot without `.git`, exact pnpm 12.16.1 and the two workspace dependency mounts, **When** cold install or warm restore runs, **Then** manifest hashes/masks are checked, no Git bootstrap or online package-manager replacement is needed, and traversal/link/source drift is rejected.

### AC: real-cold-warm-journey

**Requirements:** managed-job-cache#req:real-runner-adoption, managed-job-cache#req:bounded-admission-and-retention, managed-job-cache#req:one-producer-lock

**Given** the reviewed successor Graph profile and an empty dedicated shared archive volume, **When** a cold install publishes, cleanup closes its job, and a fresh equivalent job runs without another action, **Then** the latter restores the exact completed archive, skips install and passes owning checks; source/privacy/resource guards and ordinary per-job cleanup hold throughout, and policy applies before and after both jobs.

## Open Questions

None requiring product permission. The Plan records verified source, cold/warm policy and cleanup evidence for [WB v0.205.0](https://github.com/sneat-dev/wb/releases/tag/v0.205.0), published through successful main CI, signed macOS promotion and published-artifact checks.

---
*This document follows the https://specscore.md/feature-specification*
