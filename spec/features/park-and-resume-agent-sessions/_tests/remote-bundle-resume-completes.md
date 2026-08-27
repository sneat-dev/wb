---
format: https://specscore.md/scenario-specification
---

# Rehearse: Remote Bundle Resume Completes

**Status:** pending
**Verifies:** park-and-resume-agent-sessions#ac:remote-bundle-resume-completes

Scenario source: [../README.md](../README.md) → `### AC: remote-bundle-resume-completes`.

**Given** a parked session has two clean members at exact pushed commits
**When** a coordinator resumes it to a configured machine through SSH
**Then** the target authenticates its machine, reconstructs both exact pins, prepares both active Work Log claims and owners before release, starts one successor attached to both, and returns one validated receipt containing every required member field before the source becomes resumed.

### Nonterminal negative baseline: transport not implemented

This scenario remains **pending**. The original TEST-FIRST command is:

```bash
go test ./cmd/wb -run 'TestSessionResumeRemote(SingleWorktree|Bundle)ReachesTransport' -count=1
```

At the negative baseline, both the hard-coded one-worktree gate and the
hard-coded two-worktree gate produced exit status 1 before any transfer:

- one worktree: `cross-machine resume to target via ssh is gated: the parked session contains 1 worktrees ... no worktree was transferred`
- two worktrees: `cross-machine resume to target via ssh is gated: the parked session contains 2 worktrees ... no worktree was transferred`

There is no courier invocation and no target receipt. Review therefore replaced the misleading
positive test names with a fail-closed checkpoint: single-member and bundled
requests must prove zero delivery, zero source aggregate mutation, zero session
registry mutation, and zero Work Log/custody mutation until the actual courier
and one-receipted-successor journey is implemented. That refusal evidence is a
nonterminal safety checkpoint, not evidence that this positive AC passes.
