# Paired provider and consumer PRs

Use when a provider PR can break the consumer repository that wires or tests
it, such as `sneat-bots` and `sneat-go`.

## Prepare both sides

1. Search both repositories for relevant open PRs and exact branches.
2. Create one WB task containing every repository that needs changes.
3. Make the consumer PR pass against the provider branch without committing
   local filesystem replacements.
4. Put this exact line in the provider PR body:

   ```md
   **sneat-go-PR**: https://github.com/sneat-co/sneat-go/pull/<number>
   ```

   Keep `sneat-go` lower case and the label bold. Use the equivalent consumer
   repository only when the CI contract explicitly supports it.

## E2E selection

Provider CI should:

1. use the linked `sneat-go` PR when the label is present;
2. otherwise fetch and use the latest `sneat-go/main`;
3. test the provider PR source against that consumer checkout;
4. fail on an invalid, closed, or inaccessible linked PR rather than silently
   falling back to main.

For local E2E, use the consumer task worktree when it has changes. Otherwise,
fast-forward pull its clean canonical `main` from origin immediately before
the run.

## Merge handoff

Require both PRs' checks to pass against the paired state. Merge the provider
first, wait for its release when the consumer needs a published version, then
update or resume the consumer PR and merge it promptly.

Do not merge the consumer first if it cannot build against the currently
published provider. Do not leave the fleet on the broken intermediate version
longer than the release and final consumer check require.
