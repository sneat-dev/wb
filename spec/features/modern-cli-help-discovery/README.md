---
format: https://specscore.md/feature-specification
status: Stable
---

# Feature: Modern CLI help and command discovery

> [SpecScore.**Studio**](https://specscore.studio): | [Explore](https://specscore.studio/app/github.com/sneat-dev/wb/spec/features/modern-cli-help-discovery?op=explore) | [Edit](https://specscore.studio/app/github.com/sneat-dev/wb/spec/features/modern-cli-help-discovery?op=edit) | [Ask question](https://specscore.studio/app/github.com/sneat-dev/wb/spec/features/modern-cli-help-discovery?op=ask) | [Request change](https://specscore.studio/app/github.com/sneat-dev/wb/spec/features/modern-cli-help-discovery?op=request-change) |
**Status:** Stable
**Source Ideas:** —

## Summary

Make WB commands fast to discover for people and cold AI agents through an agent-first help journey, structured search, terminal styling, and deterministic automation output.

## Problem

An agent that knows only `wb --help` sees a long alphabetical command list and
must spend tokens recursively opening help topics before it finds the normal
start-to-finish path. The most common commands, `wb worktree create` and
`wb worktree merge`, are not promoted as a journey. Human help is visually flat,
while unsupported inherited flags are advertised on leaves and unknown help
topics can fail green.

## Behavior

A person or cold AI agent starts with `wb --help`. The first screen names the
literal create, inspect, and merge commands and groups the wider command set by
intent. On a terminal, Charm Fang v2 renders adaptive colored help; redirected
output remains plain text and machine output remains unchanged.

An agent that is unsure which command matches an intent runs
`wb commands --search <terms> --format json`. Results include exact command
paths, summaries, examples, aliases, and discovery keywords. The agent can then
open the exact help page without scanning every sibling.

The normal journey is:

1. Start: `wb worktree create` creates an isolated worktree and prints its exact
   path. Observable good result: the worktree and Work Log exist and the agent
   can enter the printed path.
2. Middle: `wb worktree summary` reports the worktree state. Observable good
   result: the agent can identify whether implementation is dirty, committed,
   pushed, or ready to land without guessing from Git proxies.
3. End: `wb worktree merge --route auto --cleanup` lands compatible finished
   work. Observable good result: the remote target contains the exact candidate
   and the finished branch/worktree are retired.

If landing cannot continue, help promotes the resumable prepare/land route and
the receipt remains available. If work is deliberately discarded, cleanup is
explicit rather than inferred.

## Acceptance Criteria

- `wb --help` visibly names the literal create, summary, and merge journey before
  the full command inventory. **Verifies:** a root-help contract test.
- Root and worktree commands are grouped by user intent instead of being only
  alphabetical. **Verifies:** Cobra group metadata and rendered-help tests.
- `wb worktree create --help` and `wb worktree merge --help` contain copyable,
  safe examples and reciprocal next-step guidance. **Verifies:** command-help
  tests.
- Help rendered to a color-capable terminal uses Charm Fang v2 styling, while
  redirected help contains no ANSI escapes. **Verifies:** Fang adapter tests
  using terminal and buffer writers.
- Fang does not duplicate errors or change WB's documented exit codes.
  **Verifies:** existing end-to-end exit-code tests plus a single-error test.
- `wb commands --search "finish work" --format json` returns
  `wb worktree merge` ahead of unrelated commands. **Verifies:** ranked catalog
  search tests.
- Runnable commands that also have children, including `wb worktree merge`, are
  represented in the capability contract. **Verifies:** capability parity test.
- Leaf help omits root selectors that the selected command rejects at runtime.
  **Verifies:** inherited-flag help matrix test.

## Open Questions

None at this time.

---
*This document follows the https://specscore.md/feature-specification*
