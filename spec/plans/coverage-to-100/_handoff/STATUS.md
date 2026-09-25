# VM in-flight status

The VM coordinator's final snapshot, 2026-09-25. Nothing runs on the VM any more, so this file won't change again.

| Agent | Branch | State |
|---|---|---|
| cov-fix-769 fixer (Sonnet) | `cov-fix-769` | **DONE, pushed at 5875404b** (2026-09-25 ~10:45Z). Ready for the Mac's Sonnet re-review against `reviews/review-fix-769.md`. |

No VM agents are running now. The VM starts no other agents (founder: "Do not start new tasks on this vm").

VM worktrees `cov-t5-ctx-4`, `cov-t8-pr1` and `cov-t9-pr8` still exist on disk; their branches are fully pushed. `wb worktree end` didn't recognise them as tasks, which is a small wb finding. They can be removed at the end of the programme; the Mac doesn't need them.

## What the cov-fix-769 fixer reported (claims only; the re-review must check them)

- **B1.** The fixer says `runCIAudit`'s loop moved into `auditReports(paths, target, compareAgainstTarget)`, which takes the comparator as a plain parameter, and that the new `cmd/wb/ci_test.go` tests it with fakes. It also says the process-evading `cmd/wb/ci_audit_target_test.go` is deleted.
- **N1/N2.** The fixer says the detector now knows `runCommand` and `installFakeGH`, and that `unit_tier.pending` was regenerated: **4,772 → 4,841** across 10 files.
- **Decision for the Mac.** `ci audit --target cov/integration --strict` now fails with `unit-tier-pending-total-rose`. The rise comes from the better detector, not from new process-starting tests. Decide how the plan's rules treat a rise the detector causes, and write that down before merging. Don't shrink other entries to hide it.
- **Known gap left open.** Test helpers named `run(t, dir, name, args...)` in canonicalrescue, archiveprune and layout still hide from the detector. Adding the bare name `run` would clash with cmd/wb's `run(args, …)`. This needs a detector design fix, such as matching on the signature.
- **Separate from this branch.** `GOOS=windows go vet ./internal/quality` fails at `quality_test.go:1862: undefined: syscall.Kill`. The fixer reproduced the failure without its change. Check why CI's Windows job doesn't catch it.
